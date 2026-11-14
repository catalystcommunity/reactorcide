import type { CheckBox } from '@catalystcommunity/controls'
import { api } from '~/api/client.ts'
import type { EnumChoice, GetJobResourceHistoryResponse, JobRunResourceSummary } from '~/api/csilapi/types.gen.ts'
import { ChoiceSelect } from './ChoiceInput.ts'
import { describeError } from './ManagementPage.ts'
import { formatValue } from './MetricsPanel.ts'
import { ErrorNotice, emptyState, skeleton } from './States.ts'
import { checkBox } from '~/ui/controls.ts'
import { h, s, setAttr, setClass, setText, show, syncKeyed } from '~/ui/dom.ts'
import { RcElement, define } from '~/ui/element.ts'
import { Loader } from '~/ui/load.ts'
import { href, navigate } from '~/ui/router.ts'
import './history.css'

/**
 * Resource use across the runs of one job: what it uses, how predictable it
 * is, which runs spiked, and how much of its reservation sits unused.
 *
 * A "job" is every run with the same project and name (the server decides
 * that from any one run's id). Each point is one finished run, summarized
 * from its stored telemetry.
 */

export type HistoryMetric = 'cpu' | 'memory' | 'duration' | 'storage'
export type HistoryWindow = '25' | '50' | '100' | '200' | 'all'

const METRIC_CHOICES: EnumChoice[] = [
  { value: 'cpu', label: 'CPU', description: 'Peak, 95th percentile and average CPU of each run' },
  { value: 'memory', label: 'Memory', description: 'Peak, 95th percentile and average memory of each run' },
  { value: 'duration', label: 'Duration', description: 'How long each run took' },
  { value: 'storage', label: 'Storage', description: 'Peak storage each run wrote' },
]

const WINDOW_CHOICES: EnumChoice[] = [
  { value: '25', label: 'Last 25 runs', description: '' },
  { value: '50', label: 'Last 50 runs', description: '' },
  { value: '100', label: 'Last 100 runs', description: '' },
  { value: '200', label: 'Last 200 runs', description: '' },
  { value: 'all', label: 'All runs (up to 1,000)', description: '' },
]

/** Re-fetch cadence while the server still summarizes older runs. */
export const PENDING_RETRY_MS = 3_000
export const PENDING_MAX_TRIES = 20
/** Fewer measured runs than this give no anomaly verdict: the spread means nothing yet. */
export const MIN_RUNS_FOR_ANOMALIES = 5

// ---------------------------------------------------------------------------
// Statistics. Pure, exported for tests.

/** The q-quantile (0..1) by linear interpolation. NaN for an empty list. */
export function quantile(values: readonly number[], q: number): number {
  if (values.length === 0) return NaN
  const sorted = [...values].sort((a, b) => a - b)
  const position = (sorted.length - 1) * Math.min(1, Math.max(0, q))
  const lower = Math.floor(position)
  const upper = Math.ceil(position)
  return sorted[lower] + (sorted[upper] - sorted[lower]) * (position - lower)
}

export function median(values: readonly number[]): number {
  return quantile(values, 0.5)
}

/** Median absolute deviation. */
export function mad(values: readonly number[]): number {
  if (values.length === 0) return NaN
  const m = median(values)
  return median(values.map((v) => Math.abs(v - m)))
}

/**
 * The value above which a run is an anomaly: median + 3×MAD. When MAD is 0
 * (most runs identical), twice the median. Infinity when there is too little
 * data, or nothing but zeros, to call anything unusual.
 */
export function anomalyThreshold(values: readonly number[]): number {
  if (values.length < MIN_RUNS_FOR_ANOMALIES) return Infinity
  const m = median(values)
  const spread = mad(values)
  if (spread > 0) return m + 3 * spread
  return m > 0 ? 2 * m : Infinity
}

/**
 * The share of a reservation the job does not use: (request − a quantile of
 * the per-run p95) / request. The default 0.95 is the cautious figure: what
 * stays unused even for the busiest runs. 0.5 is the typical run, which a few
 * spikes do not move. Negative means the job runs ABOVE its request.
 * Undefined without a request or without measurements.
 */
export function unusedReservation(request: number | undefined, perRunP95: readonly number[], q = 0.95): number | undefined {
  if (!request || request <= 0 || perRunP95.length === 0) return undefined
  return (request - quantile(perRunP95, q)) / request
}

function formatUnused(share: number | undefined): string {
  if (share === undefined) return '—'
  return share >= 0 ? `${formatPercent(share)} unused` : `${formatPercent(-share)} over`
}

// ---------------------------------------------------------------------------
// Formatting.

export type HistoryUnit = 'millicores' | 'bytes' | 'seconds'

export function formatSeconds(seconds: number): string {
  const total = Math.max(0, Math.round(seconds))
  const h = Math.floor(total / 3600)
  const m = Math.floor((total % 3600) / 60)
  const sec = total % 60
  const pad = (n: number) => String(n).padStart(2, '0')
  return h > 0 ? `${h}:${pad(m)}:${pad(sec)}` : `${m}:${pad(sec)}`
}

export function formatMetric(value: number | undefined, unit: HistoryUnit): string {
  if (value === undefined || !Number.isFinite(value)) return '—'
  return unit === 'seconds' ? formatSeconds(value) : formatValue(value, unit)
}

export function formatPercent(fraction: number): string {
  return `${Math.round(fraction * 100)}%`
}

/** Clean axis ticks from 0 to at least `max`, labelled in the unit's natural scale. */
export function niceTicks(max: number, unit: HistoryUnit, count = 4): { top: number; ticks: number[]; label: (value: number) => string } {
  let divisor = 1
  let suffix = ''
  if (unit === 'millicores') {
    divisor = max >= 1000 ? 1000 : 1
    suffix = max >= 1000 ? ' CPU' : ' mCPU'
  } else if (unit === 'bytes') {
    const steps: [number, string][] = [
      [1024 ** 4, ' TiB'],
      [1024 ** 3, ' GiB'],
      [1024 ** 2, ' MiB'],
      [1024, ' KiB'],
    ]
    const found = steps.find(([size]) => max >= size)
    if (found) [divisor, suffix] = found
    else suffix = ' B'
  } else {
    divisor = max >= 3600 ? 3600 : max >= 60 ? 60 : 1
    suffix = max >= 3600 ? ' h' : max >= 60 ? ' min' : ' s'
  }
  const scaled = Math.max(max / divisor, 1e-9)
  const raw = scaled / count
  const magnitude = 10 ** Math.floor(Math.log10(raw))
  const step = ([1, 2, 2.5, 5, 10].find((f) => f * magnitude >= raw) ?? 10) * magnitude
  const top = Math.ceil(scaled / step) * step
  const ticks: number[] = []
  for (let v = 0; v <= top + step / 2; v += step) ticks.push(v * divisor)
  const decimals = step < 1 ? Math.min(2, Math.ceil(-Math.log10(step))) : step % 1 === 0 ? 0 : 1
  const label = (value: number) => `${(value / divisor).toFixed(decimals)}${suffix}`
  return { top: top * divisor, ticks, label }
}

// ---------------------------------------------------------------------------
// Picking values out of a run, per metric.

export interface RunValues {
  run: JobRunResourceSummary
  /** The per-run peak (or the single value for duration and storage). */
  peak?: number
  p95?: number
  avg?: number
  request?: number
  limit?: number
  peakAt?: string
}

export function unitOf(metric: HistoryMetric): HistoryUnit {
  return metric === 'cpu' ? 'millicores' : metric === 'duration' ? 'seconds' : 'bytes'
}

export function valuesOf(run: JobRunResourceSummary, metric: HistoryMetric): RunValues {
  switch (metric) {
    case 'cpu':
      return {
        run,
        peak: run.cpuMaxMillicores,
        p95: run.cpuP95Millicores,
        avg: run.cpuAvgMillicores,
        request: run.cpuRequestMillicores,
        limit: run.cpuLimitMillicores,
        peakAt: run.cpuPeakAt,
      }
    case 'memory':
      return {
        run,
        peak: run.memoryMaxBytes,
        p95: run.memoryP95Bytes,
        avg: run.memoryAvgBytes,
        // Memory is limit-only: the limit IS the reservation, so there is no
        // separate request line.
        limit: run.memoryLimitBytes,
        peakAt: run.memoryPeakAt,
      }
    case 'duration':
      return { run, peak: run.durationSeconds }
    case 'storage':
      return { run, peak: run.storageMaxBytes }
  }
}

export interface HistoryAnalysis {
  unit: HistoryUnit
  shown: RunValues[]
  measured: RunValues[]
  unmeasured: number
  anomalies: RunValues[]
  threshold: number
  peakMedian: number
  peakP95: number
  peakMax: number
  request?: number
  limit?: number
  unused?: number
  /** The same share for the typical (median) run. */
  unusedTypical?: number
  durationMedian: number
  durationP95: number
}

/** Everything the chart and the summary read, from the runs and the controls. */
export function analyze(
  runs: readonly JobRunResourceSummary[],
  metric: HistoryMetric,
  window: HistoryWindow,
  completedOnly: boolean,
): HistoryAnalysis {
  const filtered = completedOnly ? runs.filter((run) => run.status === 'completed') : [...runs]
  const limited = window === 'all' ? filtered : filtered.slice(-Number(window))
  const shown = limited.map((run) => valuesOf(run, metric))
  const measured = shown.filter((v) => v.peak !== undefined && Number.isFinite(v.peak))
  const peaks = measured.map((v) => v.peak!)
  const threshold = anomalyThreshold(peaks)
  const anomalies = measured.filter((v) => v.peak! > threshold)
  // The reservation in force now: the newest run that recorded one.
  const latestWith = (key: 'request' | 'limit') => [...shown].reverse().find((v) => v[key] !== undefined)?.[key]
  const request = latestWith('request')
  const limit = latestWith('limit')
  const p95s = measured.map((v) => v.p95).filter((v): v is number => v !== undefined)
  const durations = limited.map((run) => run.durationSeconds).filter((v): v is number => v !== undefined)
  return {
    unit: unitOf(metric),
    shown,
    measured,
    unmeasured: shown.length - measured.length,
    anomalies,
    threshold,
    peakMedian: median(peaks),
    peakP95: quantile(peaks, 0.95),
    peakMax: peaks.length ? Math.max(...peaks) : NaN,
    request,
    limit,
    // The reservation is the request for CPU and the limit for memory.
    unused: unusedReservation(request ?? limit, p95s),
    unusedTypical: unusedReservation(request ?? limit, p95s, 0.5),
    durationMedian: median(durations),
    durationP95: quantile(durations, 0.95),
  }
}

// ---------------------------------------------------------------------------
// The element.

interface HistoryKey {
  jobId: string
  limit: number
}

const dateTime = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' })
const shortDate = new Intl.DateTimeFormat(undefined, { month: 'short', day: 'numeric' })
const shortTime = new Intl.DateTimeFormat(undefined, { hour: 'numeric', minute: '2-digit' })

function sameDay(a: string, b: string): boolean {
  return new Date(a).toDateString() === new Date(b).toDateString()
}

function when(iso: string | undefined): string {
  if (!iso) return '—'
  const t = Date.parse(iso)
  return Number.isNaN(t) ? '—' : dateTime.format(t)
}

export class ResourceHistory extends RcElement {
  jobId = ''

  #metric: HistoryMetric = 'cpu'
  #window: HistoryWindow = '50'
  #completedOnly = true
  #history = new Loader<HistoryKey, GetJobResourceHistoryResponse>(this, (key) =>
    api.getJobResourceHistory({ jobId: key.jobId, limit: key.limit }),
  )
  #retryTimer: ReturnType<typeof setTimeout> | undefined
  #tries = 0
  #lastData: GetJobResourceHistoryResponse | undefined

  #metricSelect!: ChoiceSelect
  #windowSelect!: ChoiceSelect
  #completedBox!: CheckBox
  #updating!: HTMLSpanElement
  #pending!: HTMLDivElement
  #error = new ErrorNotice()
  #skeleton!: HTMLElement
  #empty!: HTMLElement
  #body!: HTMLDivElement
  #chart!: HistoryChart
  #legend!: HTMLUListElement
  #stats!: HTMLDListElement
  #statValues = new Map<string, HTMLElement>()
  #statLabels = new Map<string, HTMLElement>()
  #anomalyBox!: HTMLDivElement
  #anomalyList!: HTMLUListElement
  #tableBody!: HTMLTableSectionElement
  #tableHead!: HTMLTableRowElement

  /** Exposed for tests: how many times the pending re-fetch has run for this key. */
  get pendingTries(): number {
    return this.#tries
  }

  protected build(): void {
    this.classList.add('history')
    this.#metricSelect = new ChoiceSelect({
      ariaLabel: 'Metric',
      value: this.#metric,
      choices: METRIC_CHOICES,
      onChange: (value) => {
        if (!value) return
        this.#metric = value as HistoryMetric
        this.invalidate()
      },
    })
    this.#windowSelect = new ChoiceSelect({
      ariaLabel: 'Runs to show',
      value: this.#window,
      choices: WINDOW_CHOICES,
      onChange: (value) => {
        if (!value) return
        this.#window = value as HistoryWindow
        this.invalidate()
      },
    })
    this.#completedBox = checkBox('Completed runs only', {
      checked: this.#completedOnly,
      onChange: (checked) => {
        this.#completedOnly = checked
        this.invalidate()
      },
    })
    this.#completedBox.classList.add('metrics-detailed')
    this.#updating = h('span', { class: 'meta', role: 'status', hidden: true }, 'Updating…')
    this.#pending = h('div', { class: 'meta history-pending', role: 'status', hidden: true })
    this.#skeleton = skeleton(4)
    this.#empty = emptyState('No finished runs of this job have resource data yet.')
    this.#chart = new HistoryChart((jobId) => navigate(`/jobs/${jobId}`))
    this.#legend = h('ul', { class: 'metric-legend history-legend' })

    this.#stats = h('dl', { class: 'grid-dl history-stats' })
    for (const [key, label] of [
      ['runs', 'Runs measured'],
      ['median', 'Median peak'],
      ['p95', '95th percentile of peaks'],
      ['max', 'Highest peak'],
      ['request', 'Request (latest)'],
      ['limit', 'Limit (latest)'],
      ['unused', 'Reservation unused: typical run / busy runs (p95)'],
      ['duration', 'Duration median / p95'],
    ] as const) {
      const dd = h('dd', { class: 'mono' })
      const dt = h('dt', null, label)
      this.#statValues.set(key, dd)
      this.#statLabels.set(key, dt)
      this.#stats.append(h('div', { 'data-stat': key }, dt, dd))
    }

    this.#anomalyList = h('ul', { class: 'history-anomalies' })
    this.#anomalyBox = h('div', { class: 'history-anomaly-box', hidden: true }, h('h3', { class: 'history-subtitle' }, 'Unusual runs'), this.#anomalyList)

    this.#tableHead = h('tr')
    this.#tableBody = h('tbody')
    const table = h(
      'details',
      { class: 'history-table' },
      h('summary', null, 'Show runs as a table'),
      h('div', { class: 'scroll-x' }, h('table', { class: 'table' }, h('thead', null, this.#tableHead), this.#tableBody)),
    )

    this.#body = h('div', { class: 'history-body', hidden: true }, this.#chart.el, this.#legend, this.#stats, this.#anomalyBox, table)

    this.append(
      h('div', { class: 'metrics-controls' }, this.#metricSelect.el, this.#windowSelect.el, this.#completedBox, this.#updating),
      this.#pending,
      this.#error.el,
      this.#skeleton,
      this.#empty,
      this.#body,
    )
  }

  protected connected(): void {
    const observer = typeof ResizeObserver === 'function' ? new ResizeObserver(() => this.#chart.resize()) : undefined
    observer?.observe(this.#chart.el)
    this.own(() => {
      observer?.disconnect()
      this.#clearRetry()
      // A later reconnect starts the "summarizing" re-fetch fresh.
      this.#tries = 0
    })
  }

  #clearRetry(): void {
    if (this.#retryTimer !== undefined) clearTimeout(this.#retryTimer)
    this.#retryTimer = undefined
  }

  #schedulePendingRetry(data: GetJobResourceHistoryResponse | undefined): void {
    if (!data || data.pending <= 0 || this.#retryTimer !== undefined || this.#tries >= PENDING_MAX_TRIES || !this.isActive()) return
    this.#retryTimer = setTimeout(() => {
      this.#retryTimer = undefined
      if (!this.isActive()) return
      this.#tries += 1
      void this.#history.reload()
    }, PENDING_RETRY_MS)
  }

  render(): void {
    const key: HistoryKey | undefined = this.jobId ? { jobId: this.jobId, limit: this.#window === 'all' ? 1000 : 200 } : undefined
    const before = this.#history.state
    this.#history.sync(key)
    if (this.#history.state !== before && !this.#history.state.data) {
      // A new key: its own re-fetch budget.
      this.#clearRetry()
      this.#tries = 0
    }
    const state = this.#history.state
    // Keep the previous frame on screen while a new key loads.
    if (state.data) this.#lastData = state.data
    const data = state.data ?? (state.loading ? this.#lastData : undefined)

    this.#metricSelect.update(METRIC_CHOICES, this.#metric)
    this.#windowSelect.update(WINDOW_CHOICES, this.#window)
    if (this.#completedBox.checked !== this.#completedOnly) this.#completedBox.checked = this.#completedOnly

    show(this.#updating, state.loading && Boolean(data))
    this.#error.set(state.error ? describeError(state.error, 'The run history could not be loaded.') : undefined, Boolean(data))
    show(this.#skeleton, !data && !state.error)

    const pending = state.data?.pending ?? 0
    show(this.#pending, pending > 0)
    setText(this.#pending, pending > 0 ? `Summarizing ${pending} older run${pending === 1 ? '' : 's'}…` : '')
    if (state.data && !state.loading) this.#schedulePendingRetry(state.data)
    if (pending <= 0) this.#clearRetry()

    if (!data) {
      show(this.#empty, false)
      show(this.#body, false)
      return
    }
    const analysis = analyze(data.runs, this.#metric, this.#window, this.#completedOnly)
    const hasData = analysis.measured.length > 0
    show(this.#empty, !hasData)
    show(this.#body, hasData)
    setClass(this.#body, 'history-stale', state.loading)
    if (!hasData) return

    this.#chart.update(analysis, this.#metric, this.jobId)
    this.#renderLegend()
    this.#renderStats(analysis)
    this.#renderAnomalies(analysis)
    this.#renderTable(analysis)
  }

  #renderLegend(): void {
    const items = SERIES.filter((one) => one.metrics.includes(this.#metric))
    syncKeyed(
      this.#legend,
      items,
      (one) => one.key,
      (one) =>
        h(
          'li',
          { class: 'metric-legend-item' },
          one.key === 'peak'
            ? h('span', { class: `history-key-dot history-${one.key}`, 'aria-hidden': 'true' })
            : h('span', { class: `history-key-line history-${one.key}`, 'aria-hidden': 'true' }),
          h('span', { class: 'metric-legend-name' }),
        ),
      // A duration is one value per run, not a peak of many samples.
      (li, one) => setText(li.querySelector('.metric-legend-name')!, one.key === 'peak' && this.#metric === 'duration' ? 'Duration of each run' : one.label),
    )
  }

  #renderStats(a: HistoryAnalysis): void {
    const set = (key: string, text: string) => setText(this.#statValues.get(key)!, text)
    // "33 of 46": the runs with data out of the runs in the window. A run with
    // no telemetry is in the window but has no point on the chart.
    set('runs', a.unmeasured > 0 ? `${a.measured.length} of ${a.measured.length + a.unmeasured}` : String(a.measured.length))
    // For Duration the per-run value is the duration itself, so "peak" is the
    // wrong word, and the separate duration row would repeat the same numbers.
    const isDuration = this.#metric === 'duration'
    setText(this.#statLabels.get('median')!, isDuration ? 'Median duration' : 'Median peak')
    setText(this.#statLabels.get('p95')!, isDuration ? '95th percentile of durations' : '95th percentile of peaks')
    setText(this.#statLabels.get('max')!, isDuration ? 'Longest run' : 'Highest peak')
    show(this.#stats.querySelector<HTMLElement>('[data-stat="duration"]')!, !isDuration)
    set('median', formatMetric(a.peakMedian, a.unit))
    set('p95', formatMetric(a.peakP95, a.unit))
    set('max', formatMetric(a.peakMax, a.unit))
    const reservable = this.#metric === 'cpu' || this.#metric === 'memory'
    for (const key of ['limit', 'unused']) {
      show(this.#stats.querySelector<HTMLElement>(`[data-stat="${key}"]`)!, reservable)
    }
    // Memory has no request; its limit is the reservation.
    show(this.#stats.querySelector<HTMLElement>('[data-stat="request"]')!, reservable && this.#metric !== 'memory')
    set('request', formatMetric(a.request, a.unit))
    set('limit', formatMetric(a.limit, a.unit))
    set(
      'unused',
      a.unused === undefined ? '—' : `${formatUnused(a.unusedTypical)} / ${formatUnused(a.unused)}`,
    )
    set('duration', `${formatMetric(a.durationMedian, 'seconds')} / ${formatMetric(a.durationP95, 'seconds')}`)
  }

  #renderAnomalies(a: HistoryAnalysis): void {
    show(this.#anomalyBox, a.anomalies.length > 0)
    syncKeyed(
      this.#anomalyList,
      [...a.anomalies].reverse(),
      (v) => v.run.jobId,
      () => h('li', null, h('a', { class: 'mono' }), h('span', { class: 'meta' })),
      (li, v) => {
        const link = li.querySelector('a')!
        setAttr(link, 'href', href(`/jobs/${v.run.jobId}`))
        setText(link, when(v.run.createdAt))
        const peakAt = v.peakAt ? `, peak at ${shortTime.format(Date.parse(v.peakAt))}` : ''
        setText(li.querySelector('span')!, ` — ${formatMetric(v.peak, a.unit)} (usual ${formatMetric(a.peakMedian, a.unit)})${peakAt}`)
      },
    )
  }

  #renderTable(a: HistoryAnalysis): void {
    const reservable = this.#metric === 'cpu' || this.#metric === 'memory'
    const headings = reservable ? ['Run', 'Status', 'Peak', 'p95', 'Average', 'Duration'] : ['Run', 'Status', this.#metric === 'duration' ? 'Duration' : 'Peak storage']
    syncKeyed(
      this.#tableHead,
      headings,
      (text) => text,
      (text) => h('th', { scope: 'col' }, text),
    )
    syncKeyed(
      this.#tableBody,
      [...a.shown].reverse(),
      (v) => v.run.jobId,
      () => h('tr'),
      (tr, v) => {
        const cells = reservable
          ? [when(v.run.createdAt), v.run.status, formatMetric(v.peak, a.unit), formatMetric(v.p95, a.unit), formatMetric(v.avg, a.unit), formatMetric(v.run.durationSeconds, 'seconds')]
          : [when(v.run.createdAt), v.run.status, formatMetric(v.peak, a.unit)]
        while (tr.children.length > cells.length) tr.lastElementChild!.remove()
        while (tr.children.length < cells.length) tr.append(h('td'))
        cells.forEach((text, i) => {
          const td = tr.children[i] as HTMLElement
          if (i === 0) {
            let link = td.querySelector('a')
            if (!link) td.replaceChildren((link = h('a')))
            setAttr(link, 'href', href(`/jobs/${v.run.jobId}`))
            setText(link, text)
          } else setText(td, text)
        })
        setClass(tr, 'history-row-anomaly', a.anomalies.includes(v))
        setClass(tr, 'history-row-current', v.run.jobId === this.jobId)
      },
    )
  }
}
define('rc-resource-history', ResourceHistory)

export function resourceHistory(jobId: string): ResourceHistory {
  const el = document.createElement('rc-resource-history') as ResourceHistory
  el.jobId = jobId
  return el
}

// ---------------------------------------------------------------------------
// The chart.

interface SeriesSpec {
  key: 'peak' | 'p95' | 'avg'
  label: string
  metrics: HistoryMetric[]
}

const SERIES: SeriesSpec[] = [
  { key: 'peak', label: 'Peak of each run', metrics: ['cpu', 'memory', 'duration', 'storage'] },
  { key: 'p95', label: '95th percentile', metrics: ['cpu', 'memory'] },
  { key: 'avg', label: 'Average', metrics: ['cpu', 'memory'] },
]

const HEIGHT = 260
const PAD = { left: 64, right: 56, top: 14, bottom: 28 }

/**
 * One point per run on a time axis, with the run's p95 and average as lines.
 *
 * The points are focusable (one tab stop; arrow keys move between runs), so
 * the chart redraws only when what it shows changes, and gives the focus back
 * to the same run when it does.
 */
class HistoryChart {
  readonly el: HTMLDivElement
  readonly #svg: SVGSVGElement
  readonly #tooltip: HTMLDivElement
  readonly #open: (jobId: string) => void
  #signature = ''
  #width = 720
  #last: { analysis: HistoryAnalysis; metric: HistoryMetric; currentId: string } | undefined
  #points: SVGGElement[] = []
  #active = -1

  constructor(open: (jobId: string) => void) {
    this.#open = open
    this.#svg = s('svg', { class: 'history-chart', role: 'group' }) as SVGSVGElement
    this.#tooltip = h('div', { class: 'history-tooltip', role: 'tooltip', hidden: true })
    this.el = h('div', { class: 'history-chart-wrap' }, this.#svg, this.#tooltip)
    this.#svg.addEventListener('keydown', (event) => this.#key(event))
    this.#svg.addEventListener('pointerleave', () => this.#hideTip())
  }

  resize(): void {
    const width = Math.round(this.el.clientWidth)
    if (width > 0 && Math.abs(width - this.#width) > 4) {
      this.#width = Math.max(280, width)
      this.#signature = ''
      if (this.#last) this.update(this.#last.analysis, this.#last.metric, this.#last.currentId)
    }
  }

  update(analysis: HistoryAnalysis, metric: HistoryMetric, currentId: string): void {
    this.#last = { analysis, metric, currentId }
    const signature = JSON.stringify([this.#width, metric, currentId, analysis.measured.map((v) => [v.run.jobId, v.peak, v.p95, v.avg]), analysis.request, analysis.limit])
    if (signature === this.#signature) return
    this.#signature = signature
    const focusedId = this.#points.find((g) => g === document.activeElement)?.dataset.jobId

    const width = this.#width
    const runs = analysis.measured
    const times = runs.map((v) => Date.parse(v.run.createdAt))
    let minTime = Math.min(...times)
    let maxTime = Math.max(...times)
    if (maxTime === minTime) {
      minTime -= 3_600_000
      maxTime += 3_600_000
    }
    const highest = Math.max(analysis.peakMax, analysis.request ?? 0, analysis.limit ?? 0, 1)
    const axis = niceTicks(highest, analysis.unit)
    const plotW = width - PAD.left - PAD.right
    const plotH = HEIGHT - PAD.top - PAD.bottom
    const x = (t: number) => PAD.left + ((t - minTime) / (maxTime - minTime)) * plotW
    const y = (v: number) => PAD.top + (1 - v / axis.top) * plotH

    setAttr(this.#svg, 'viewBox', `0 0 ${width} ${HEIGHT}`)
    setAttr(this.#svg, 'aria-label', `${METRIC_CHOICES.find((c) => c.value === metric)?.label} per run, ${runs.length} runs. Use the arrow keys to move between runs and Enter to open one.`)

    const parts: SVGElement[] = []
    for (const tick of axis.ticks) {
      parts.push(s('line', { class: 'metric-grid', x1: PAD.left, y1: y(tick), x2: width - PAD.right, y2: y(tick) }))
      parts.push(s('text', { class: 'metric-axis', x: PAD.left - 8, y: y(tick) + 4, 'text-anchor': 'end' }, axis.label(tick)))
    }
    // Time ticks: up to four, never closer than ~110px.
    const span = maxTime - minTime
    const tickCount = Math.max(2, Math.min(4, Math.floor(plotW / 110)))
    const format = span < 2 * 86_400_000 ? shortTime : shortDate
    for (let i = 0; i < tickCount; i++) {
      const t = minTime + (span * i) / (tickCount - 1)
      const anchor = i === 0 ? 'start' : i === tickCount - 1 ? 'end' : 'middle'
      parts.push(s('text', { class: 'metric-axis', x: x(t), y: HEIGHT - 8, 'text-anchor': anchor }, format.format(t)))
    }

    // Reservation lines, labelled directly at the right edge.
    for (const [key, value] of [
      ['request', analysis.request],
      ['limit', analysis.limit],
    ] as const) {
      if (value === undefined) continue
      parts.push(s('line', { class: `history-ref history-ref-${key}`, x1: PAD.left, y1: y(value), x2: width - PAD.right, y2: y(value) }))
      parts.push(s('text', { class: 'history-ref-label', x: width - PAD.right + 6, y: y(value) + 4 }, key))
    }

    // The current run: a hairline so it is findable among many.
    const current = runs.find((v) => v.run.jobId === currentId)
    if (current) {
      const cx = x(Date.parse(current.run.createdAt))
      parts.push(s('line', { class: 'history-current-line', x1: cx, y1: PAD.top, x2: cx, y2: PAD.top + plotH }))
    }

    for (const key of ['avg', 'p95'] as const) {
      const pts = runs.filter((v) => v[key] !== undefined)
      if (pts.length === 0) continue
      const d = pts.map((v, i) => `${i === 0 ? 'M' : 'L'} ${x(Date.parse(v.run.createdAt)).toFixed(1)} ${y(v[key]!).toFixed(1)}`).join(' ')
      parts.push(s('path', { class: `history-line history-${key}`, d }))
    }

    const anomalies = new Set(analysis.anomalies)
    this.#points = runs.map((v, index) => {
      const cx = x(Date.parse(v.run.createdAt))
      const cy = y(v.peak!)
      const anomaly = anomalies.has(v)
      const isCurrent = v.run.jobId === currentId
      const mark = anomaly
        ? s('path', { class: 'history-point history-point-anomaly', d: `M ${cx} ${cy - 6} L ${cx + 6} ${cy} L ${cx} ${cy + 6} L ${cx - 6} ${cy} Z` })
        : s('circle', { class: 'history-point history-peak', cx, cy, r: 4 })
      const g = s(
        'g',
        {
          class: `history-hit${isCurrent ? ' history-hit-current' : ''}`,
          tabindex: '-1',
          role: 'link',
          'aria-label': this.#describe(v, analysis.unit, anomaly, isCurrent),
        },
        s('circle', { class: 'history-hit-area', cx, cy, r: 12 }),
        isCurrent ? s('circle', { class: 'history-current-ring', cx, cy, r: 8 }) : null,
        mark,
      ) as SVGGElement
      g.dataset.jobId = v.run.jobId
      g.dataset.index = String(index)
      g.addEventListener('pointerenter', () => this.#showTip(index))
      g.addEventListener('focus', () => {
        this.#setActive(index)
        this.#showTip(index)
      })
      g.addEventListener('blur', () => this.#hideTip())
      g.addEventListener('click', () => this.#open(v.run.jobId))
      return g
    })
    parts.push(...this.#points)
    this.#svg.replaceChildren(...parts)

    const restore = focusedId ? this.#points.findIndex((g) => g.dataset.jobId === focusedId) : -1
    const start = restore >= 0 ? restore : Math.max(0, runs.findIndex((v) => v.run.jobId === currentId))
    this.#setActive(start >= 0 && start < runs.length ? start : runs.length - 1)
    if (restore >= 0) this.#points[restore].focus()
    else this.#hideTip()
  }

  #describe(v: RunValues, unit: HistoryUnit, anomaly: boolean, current: boolean): string {
    const parts = [when(v.run.createdAt), v.run.status, `peak ${formatMetric(v.peak, unit)}`]
    if (v.p95 !== undefined) parts.push(`p95 ${formatMetric(v.p95, unit)}`)
    if (v.avg !== undefined) parts.push(`average ${formatMetric(v.avg, unit)}`)
    if (anomaly) parts.push('unusual')
    if (current) parts.push('this run')
    return parts.join(', ')
  }

  #setActive(index: number): void {
    this.#active = index
    this.#points.forEach((g, i) => setAttr(g, 'tabindex', i === index ? '0' : '-1'))
  }

  #key(event: KeyboardEvent): void {
    if (this.#points.length === 0) return
    let next = this.#active
    switch (event.key) {
      case 'ArrowRight':
      case 'ArrowUp':
        next = Math.min(this.#points.length - 1, this.#active + 1)
        break
      case 'ArrowLeft':
      case 'ArrowDown':
        next = Math.max(0, this.#active - 1)
        break
      case 'Home':
        next = 0
        break
      case 'End':
        next = this.#points.length - 1
        break
      case 'Enter':
      case ' ': {
        const id = this.#points[this.#active]?.dataset.jobId
        if (id) {
          event.preventDefault()
          this.#open(id)
        }
        return
      }
      default:
        return
    }
    event.preventDefault()
    this.#setActive(next)
    this.#points[next].focus()
  }

  #showTip(index: number): void {
    const v = this.#last?.analysis.measured[index]
    const g = this.#points[index]
    if (!v || !g || !this.#last) return
    const unit = this.#last.analysis.unit
    const rows: [string, string][] = []
    if (v.p95 !== undefined || v.avg !== undefined) {
      rows.push(['Peak', formatMetric(v.peak, unit)], ['p95', formatMetric(v.p95, unit)], ['Average', formatMetric(v.avg, unit)])
      if (v.request !== undefined) rows.push(['Request', formatMetric(v.request, unit)])
      if (v.limit !== undefined) rows.push(['Limit', formatMetric(v.limit, unit)])
      // The title already has the run's date; repeat only the time when the
      // peak is on the same day, so the row fits the tooltip width.
      if (v.peakAt) rows.push(['Peak at', sameDay(v.peakAt, v.run.createdAt) ? shortTime.format(Date.parse(v.peakAt)) : when(v.peakAt)])
    } else {
      rows.push([this.#last.metric === 'duration' ? 'Duration' : 'Peak', formatMetric(v.peak, unit)])
    }
    if (this.#last.metric !== 'duration') rows.push(['Duration', formatMetric(v.run.durationSeconds, 'seconds')])
    this.#tooltip.replaceChildren(
      h('div', { class: 'history-tip-title' }, when(v.run.createdAt)),
      h('div', { class: 'meta' }, [v.run.status, v.run.sourceRef].filter(Boolean).join(' · ')),
      ...rows.map(([label, value]) => h('div', { class: 'history-tip-row' }, h('span', { class: 'meta' }, label), h('strong', { class: 'mono' }, value))),
    )
    show(this.#tooltip, true)
    const wrap = this.el.getBoundingClientRect()
    const box = g.getBoundingClientRect()
    const left = box.left - wrap.left + box.width / 2
    const flip = left > wrap.width / 2
    this.#tooltip.style.left = `${Math.round(left)}px`
    this.#tooltip.style.top = `${Math.round(box.top - wrap.top + box.height + 4)}px`
    setClass(this.#tooltip, 'history-tooltip-flip', flip)
  }

  #hideTip(): void {
    show(this.#tooltip, false)
  }
}
