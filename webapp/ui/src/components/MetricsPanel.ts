import type { CheckBox } from '@catalystcommunity/controls'
import { api } from '~/api/client.ts'
import type { GetJobMetricsResponse, JobMetricSeries } from '~/api/csilapi/types.gen.ts'
import { ChoiceSelect } from '~/components/ChoiceInput.ts'
import { checkBox } from '~/ui/controls.ts'
import { h, s, setText, show, syncKeyed } from '~/ui/dom.ts'
import { RcElement, define } from '~/ui/element.ts'
import { Loader } from '~/ui/load.ts'
import './metrics.css'

/**
 * Job resource metrics.
 *
 * What this replaces: a flat checkbox list of every series the collectors
 * emitted, which on Kubernetes meant the job roll-up sitting next to its own
 * parts, memory answered four ways, and on Docker one series per host core.
 *
 * The default view is the summary the server computes -- memory used, swap,
 * request and limit; CPU used, request and limit; storage used -- with no scope
 * or component qualification at all. The controls below are built from the
 * DATA, not from a fixed list, so a component picker appears only when there is
 * more than one component, including when a builder sidecar starts ten minutes
 * into a job.
 *
 * Every control here is built ONCE and updated in place. The old
 * implementation rebuilt its controls with innerHTML on each refresh, which
 * closed any open dropdown; see ChoiceInput.test.ts for the regression test.
 *
 * Set `jobId` and `live` before it connects; `live` may change later.
 */

type Family = 'cpu' | 'memory' | 'storage'

const FAMILY_CHOICES = [
  { value: 'cpu', label: 'CPU', description: 'Processor use, requests and limits' },
  { value: 'memory', label: 'Memory', description: 'Memory in use, swap, requests and limits' },
  { value: 'storage', label: 'Storage', description: 'Disk used by this job' },
]

interface MetricsKey {
  jobId: string
  component: string
  detailed: boolean
}

export class MetricsPanel extends RcElement {
  jobId = ''
  live = false

  #family: Family = 'cpu'
  #component = ''
  #detailed = false
  #metrics = new Loader<MetricsKey, GetJobMetricsResponse>(this, (key) =>
    api.getJobMetrics({
      jobId: key.jobId,
      metrics: [],
      maxPoints: 600,
      view: key.detailed ? 'raw' : 'summary',
      component: key.component || undefined,
    }),
  )

  #familySelect!: ChoiceSelect
  #componentSelect!: ChoiceSelect
  #detailedBox!: CheckBox
  #updating!: HTMLSpanElement
  #empty!: HTMLDivElement
  #chart!: MetricChart
  #legend!: HTMLUListElement
  #unavailable!: HTMLDivElement

  protected build(): void {
    this.classList.add('metrics')
    this.#familySelect = new ChoiceSelect({
      ariaLabel: 'Metric group',
      value: this.#family,
      choices: FAMILY_CHOICES,
      onChange: (value) => {
        this.#family = value as Family
        this.invalidate()
      },
    })
    this.#componentSelect = new ChoiceSelect({
      ariaLabel: 'Component',
      placeholder: 'Whole job',
      value: '',
      onChange: (value) => {
        this.#component = value
        this.invalidate()
      },
    })
    this.#detailedBox = checkBox('Show every series', {
      onChange: (checked) => {
        this.#detailed = checked
        this.invalidate()
      },
    })
    this.#detailedBox.classList.add('metrics-detailed')
    this.#updating = h('span', { class: 'meta', role: 'status', hidden: true }, 'Updating…')
    this.#empty = h('div', { class: 'empty' })
    this.#chart = new MetricChart()
    this.#legend = h('ul', { class: 'metric-legend' })
    this.#unavailable = h('div', { class: 'metrics-unavailable meta', hidden: true })
    this.append(
      h('div', { class: 'metrics-controls' }, this.#familySelect.el, this.#componentSelect.el, this.#detailedBox, this.#updating),
      this.#empty,
      this.#chart.el,
      this.#legend,
      this.#unavailable,
    )
  }

  protected connected(): void {
    // A running job polls. Metric-availability events exist on the bus, but a
    // chart redrawn on every batch is distracting and costs more than it is
    // worth; a steady cadence reads better and bounds the request rate.
    //
    // The visibility check matters for a tab left open for days: a
    // backgrounded tab must not keep asking. own() is what stops the timer
    // when this panel leaves the page -- without it every visit to a job page
    // would leave another timer running for the life of the session.
    const timer = setInterval(() => {
      if (this.live && document.visibilityState === 'visible') void this.#metrics.reload()
    }, 5_000)
    this.own(() => clearInterval(timer))
  }

  render(): void {
    this.#metrics.sync({ jobId: this.jobId, component: this.#component, detailed: this.#detailed })
    const state = this.#metrics.state
    const data = state.data

    this.#familySelect.update(FAMILY_CHOICES, this.#family)

    // Shown ONLY when the job actually has components. A single-container job
    // never grows a pointless dropdown, and a job whose sidecar starts part way
    // through grows one at that moment, because this is derived from the
    // response rather than from a fixed list.
    const components = data?.components ?? []
    show(this.#componentSelect.el, components.length > 0)
    this.#componentSelect.update(
      components.map((name) => ({
        value: name,
        label: name === 'main' ? 'Job container' : name,
        description: name === 'main' ? 'The container running the job itself' : `The ${name} sidecar, working on this job's behalf`,
      })),
      this.#component,
    )

    show(this.#updating, state.loading)

    const visible = (data?.series ?? []).filter((series) => series.name.startsWith(`${this.#family}.`))
    const hasSeries = visible.length > 0
    show(this.#empty, !hasSeries)
    setText(this.#empty, state.loading && !data ? 'Loading metrics…' : `No ${this.#family} metrics were recorded for this job.`)
    show(this.#chart.el, hasSeries)
    show(this.#legend, hasSeries)
    if (hasSeries) {
      this.#chart.update(visible)
      updateLegend(this.#legend, visible)
    }

    // Only what the coordinator still considers unavailable for the range it
    // answered. A warning contradicted by a real sample in the same attempt is
    // dropped server-side, so a "not collected" line here means exactly that:
    // nothing arrived.
    const unavailable = data?.unavailable ?? []
    show(this.#unavailable, unavailable.length > 0)
    syncKeyed(
      this.#unavailable,
      unavailable,
      (item) => `${item.metricPrefix}\u0000${item.reason}`,
      () => h('div'),
      (el, item) => setText(el, describeUnavailable(item)),
    )
  }
}
define('rc-metrics-panel', MetricsPanel)

export function metricsPanel(jobId: string, live: boolean): MetricsPanel {
  const el = document.createElement('rc-metrics-panel') as MetricsPanel
  el.jobId = jobId
  el.live = live
  return el
}

/** One warning line: the family by its friendly name, then the reason. */
export function describeUnavailable(item: { metricPrefix: string; reason: string }): string {
  const name = FRIENDLY[item.metricPrefix] ?? item.metricPrefix
  if (item.reason === 'temporarily_unavailable') {
    return `${name}: no sample was available before the job ended`
  }
  if (item.reason === 'buffer_gap') {
    return `${name}: some samples were lost to ${humanReason(item.reason)}`
  }
  return `${name}: not collected (${humanReason(item.reason)})`
}

export function humanReason(reason: string): string {
  switch (reason) {
    case 'runtime_not_supported':
      return 'the container runtime does not report it'
    case 'permission_denied':
      return 'the worker is not permitted to read it'
    case 'metric_api_not_installed':
      return 'the cluster metrics API is not installed'
    case 'guest_helper_not_installed':
      return 'the VM guest helper is missing'
    case 'not_applicable':
      return 'not applicable to this job'
    case 'buffer_gap':
      return 'a gap in the telemetry buffer'
    case 'temporarily_unavailable':
      return 'no sample was available yet'
    default:
      return reason
  }
}

const WIDTH = 720
const HEIGHT = 240
const PAD = { left: 62, right: 16, top: 12, bottom: 28 }
const DASHES = ['', '8 4', '2 3', '12 3 2 3', '5 3 1 3', '14 4']

/**
 * A line chart, drawn as SVG.
 *
 * SVG rather than canvas so the shapes are inspectable and scale crisply. The
 * chart holds nothing focusable or stateful, so each update redraws its
 * contents; the <svg> element itself stays.
 */
class MetricChart {
  readonly el: HTMLDivElement
  readonly #svg: SVGElement

  constructor() {
    this.#svg = s('svg', { class: 'metric-chart', viewBox: `0 0 ${WIDTH} ${HEIGHT}`, role: 'img' })
    this.el = h('div', { class: 'scroll-x' }, this.#svg)
  }

  update(series: JobMetricSeries[]): void {
    let minTime = Infinity
    let maxTime = -Infinity
    let maxValue = 0
    for (const one of series) {
      for (const point of one.points) {
        const time = Date.parse(point.observedAt)
        if (Number.isNaN(time)) continue
        minTime = Math.min(minTime, time)
        maxTime = Math.max(maxTime, time)
        maxValue = Math.max(maxValue, point.max ?? point.value)
      }
    }
    if (!Number.isFinite(minTime)) {
      show(this.el, false)
      return
    }
    if (maxTime === minTime) maxTime = minTime + 1000
    if (maxValue === 0) maxValue = 1

    const scaleX = (time: number): number => PAD.left + ((time - minTime) / (maxTime - minTime)) * (WIDTH - PAD.left - PAD.right)
    const scaleY = (value: number): number => PAD.top + (1 - value / maxValue) * (HEIGHT - PAD.top - PAD.bottom)

    const parts: SVGElement[] = []
    // Gridlines and value labels
    for (const fraction of [0, 0.25, 0.5, 0.75, 1]) {
      const y = PAD.top + fraction * (HEIGHT - PAD.top - PAD.bottom)
      const value = maxValue * (1 - fraction)
      parts.push(s('line', { class: 'metric-grid', x1: PAD.left, y1: y, x2: WIDTH - PAD.right, y2: y }))
      parts.push(s('text', { class: 'metric-axis', x: PAD.left - 8, y: y + 4, 'text-anchor': 'end' }, formatValue(value, series[0]?.unit ?? '')))
    }
    series.forEach((one, index) => {
      const d = one.points
        .map((point, i) => `${i === 0 ? 'M' : 'L'} ${scaleX(Date.parse(point.observedAt)).toFixed(1)} ${scaleY(point.value).toFixed(1)}`)
        .join(' ')
      parts.push(
        s('path', {
          class: 'metric-line',
          d,
          style: { stroke: `var(--series-${(index % 6) + 1})` },
          // A dash pattern per series, so the chart is readable without
          // relying on colour alone.
          'stroke-dasharray': DASHES[index % DASHES.length] || null,
        }),
      )
    })
    this.#svg.setAttribute('aria-label', `Chart of ${series.map((one) => one.name).join(', ')}`)
    this.#svg.replaceChildren(...parts)
  }
}

function seriesKey(series: JobMetricSeries): string {
  return `${series.name}|${(series.labels ?? []).map((label) => `${label.key}=${label.value}`).join(',')}`
}

function updateLegend(list: HTMLUListElement, series: JobMetricSeries[]): void {
  syncKeyed(
    list,
    series,
    seriesKey,
    () =>
      h(
        'li',
        { class: 'metric-legend-item' },
        h('span', { class: 'metric-swatch', 'aria-hidden': 'true' }),
        h('span', { class: 'metric-legend-name' }),
        h('span', { class: 'metric-legend-value mono' }),
      ),
    (item, one, index) => {
      const latest = one.points[one.points.length - 1]
      item.querySelector<HTMLElement>('.metric-swatch')!.style.background = `var(--series-${(index % 6) + 1})`
      setText(item.querySelector('.metric-legend-name')!, friendlyName(one))
      setText(item.querySelector('.metric-legend-value')!, latest ? formatValue(latest.value, one.unit) : '—')
    },
  )
}

/** A human name, with the component only when there is one. */
function friendlyName(series: JobMetricSeries): string {
  const base = FRIENDLY[series.name] ?? series.name
  const component = series.labels?.find((label) => label.key === 'component')?.value
  return component ? `${base} (${component})` : base
}

const FRIENDLY: Record<string, string> = {
  'cpu.utilization': 'CPU used',
  'telemetry.buffer': 'Telemetry',
  'cpu.request': 'CPU request',
  'cpu.limit': 'CPU limit',
  'cpu.capacity': 'CPU capacity',
  'memory.usage': 'Memory used',
  'memory.swap.usage': 'Swap used',
  'memory.request': 'Memory request',
  'memory.limit': 'Memory limit',
  'storage.used': 'Storage used',
}

export function formatValue(value: number, unit: string): string {
  if (unit === 'bytes') {
    const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
    let scaled = value
    let index = 0
    while (Math.abs(scaled) >= 1024 && index < units.length - 1) {
      scaled /= 1024
      index++
    }
    return `${scaled.toFixed(index === 0 ? 0 : 1)} ${units[index]}`
  }
  if (unit === 'millicores') {
    return value >= 1000 ? `${(value / 1000).toFixed(2)} CPU` : `${Math.round(value)} mCPU`
  }
  if (unit === 'nanoseconds') {
    return `${(value / 1_000_000).toFixed(0)} ms`
  }
  return Math.round(value).toLocaleString()
}
