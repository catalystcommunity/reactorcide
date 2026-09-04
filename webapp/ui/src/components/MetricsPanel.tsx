import { For, Show, createMemo, createResource, createSignal, onCleanup, type JSX } from 'solid-js'
import { api } from '~/api/client.ts'
import type { GetJobMetricsResponse, JobMetricSeries } from '~/api/csilapi/types.gen.ts'
import { ChoiceSelect } from '~/components/ChoiceInput.tsx'
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
 * Every control here is a reconciled Solid component. The old implementation
 * rebuilt its controls with innerHTML on each refresh, which closed any open
 * dropdown; see ChoiceInput.test.tsx for the regression test.
 */

type Family = 'cpu' | 'memory' | 'storage'

const FAMILY_CHOICES = [
  { value: 'cpu', label: 'CPU', description: 'Processor use, requests and limits' },
  { value: 'memory', label: 'Memory', description: 'Memory in use, swap, requests and limits' },
  { value: 'storage', label: 'Storage', description: 'Disk used by this job' },
]

export function MetricsPanel(props: { jobId: string; live: boolean }): JSX.Element {
  const [family, setFamily] = createSignal<Family>('cpu')
  const [component, setComponent] = createSignal('')
  const [detailed, setDetailed] = createSignal(false)

  // Refetching is driven by an explicit dependency on the controls plus a tick
  // signal, so a poll and a control change go through the same path.
  const [tick, setTick] = createSignal(0)

  const [metrics] = createResource(
    () => ({ jobId: props.jobId, component: component(), detailed: detailed(), tick: tick() }),
    async (key): Promise<GetJobMetricsResponse> =>
      api.getJobMetrics({
        jobId: key.jobId,
        metrics: [],
        maxPoints: 600,
        view: key.detailed ? 'raw' : 'summary',
        component: key.component || undefined,
      }),
  )

  // A running job polls. Metric-availability events exist on the bus, but a
  // chart redrawn on every batch is distracting and costs more than it is
  // worth; a steady cadence reads better and bounds the request rate.
  //
  // The visibility check matters for a tab left open for days: a backgrounded
  // tab must not keep asking. onCleanup is what stops the timer when this panel
  // unmounts -- without it every visit to a job page would leave another timer
  // running for the life of the session.
  const timer = setInterval(() => {
    if (props.live && document.visibilityState === 'visible') setTick((t) => t + 1)
  }, 5_000)
  onCleanup(() => clearInterval(timer))

  const components = createMemo(() => metrics()?.components ?? [])

  const componentChoices = createMemo(() => [
    ...components().map((name) => ({
      value: name,
      label: name === 'main' ? 'Job container' : name,
      description:
        name === 'main'
          ? 'The container running the job itself'
          : `The ${name} sidecar, working on this job's behalf`,
    })),
  ])

  const visible = createMemo(() =>
    (metrics()?.series ?? []).filter((series) => series.name.startsWith(`${family()}.`)),
  )

  return (
    <div class="metrics">
      <div class="metrics-controls">
        <label class="sr-only" for="metric-family">
          Metric group
        </label>
        <ChoiceSelect
          id="metric-family"
          value={family()}
          choices={FAMILY_CHOICES}
          onChange={(value) => setFamily(value as Family)}
        />

        {/*
          Shown ONLY when the job actually has components. A single-container
          job never grows a pointless dropdown, and a job whose sidecar starts
          part way through grows one at that moment, because this is derived
          from the response rather than from a fixed list.
        */}
        <Show when={components().length > 0}>
          <label class="sr-only" for="metric-component">
            Component
          </label>
          <ChoiceSelect
            id="metric-component"
            value={component()}
            placeholder="Whole job"
            choices={componentChoices()}
            onChange={setComponent}
          />
        </Show>

        <label class="metrics-detailed">
          <input
            type="checkbox"
            checked={detailed()}
            onChange={(event) => setDetailed(event.currentTarget.checked)}
          />
          Show every series
        </label>

        <Show when={metrics.loading}>
          <span class="meta" role="status">
            Updating…
          </span>
        </Show>
      </div>

      <Show
        when={visible().length > 0}
        fallback={
          <div class="empty">
            <Show
              when={metrics.loading}
              fallback={<>No {family()} metrics were recorded for this job.</>}
            >
              Loading metrics…
            </Show>
          </div>
        }
      >
        <MetricChart series={visible()} />
        <MetricLegend series={visible()} />
      </Show>

      <Show when={(metrics()?.unavailable?.length ?? 0) > 0}>
        <div class="metrics-unavailable meta">
          <For each={metrics()!.unavailable}>
            {(item) => (
              <div>
                {item.metricPrefix}: not collected ({humanReason(item.reason)})
              </div>
            )}
          </For>
        </div>
      </Show>
    </div>
  )
}

function humanReason(reason: string): string {
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
    default:
      return reason
  }
}

/**
 * A line chart, drawn as SVG.
 *
 * SVG rather than canvas so the shapes are inspectable and scale crisply, and
 * so the whole thing redraws through Solid's reconciliation rather than an
 * imperative clear-and-repaint.
 */
function MetricChart(props: { series: JobMetricSeries[] }): JSX.Element {
  const WIDTH = 720
  const HEIGHT = 240
  const PAD = { left: 62, right: 16, top: 12, bottom: 28 }

  const bounds = createMemo(() => {
    let minTime = Infinity
    let maxTime = -Infinity
    let maxValue = 0
    for (const series of props.series) {
      for (const point of series.points) {
        const time = Date.parse(point.observedAt)
        if (Number.isNaN(time)) continue
        minTime = Math.min(minTime, time)
        maxTime = Math.max(maxTime, time)
        maxValue = Math.max(maxValue, point.max ?? point.value)
      }
    }
    if (!Number.isFinite(minTime)) return null
    if (maxTime === minTime) maxTime = minTime + 1000
    if (maxValue === 0) maxValue = 1
    return { minTime, maxTime, maxValue }
  })

  const scaleX = (time: number) => {
    const b = bounds()!
    return PAD.left + ((time - b.minTime) / (b.maxTime - b.minTime)) * (WIDTH - PAD.left - PAD.right)
  }
  const scaleY = (value: number) => {
    const b = bounds()!
    return PAD.top + (1 - value / b.maxValue) * (HEIGHT - PAD.top - PAD.bottom)
  }

  const pathFor = (series: JobMetricSeries) =>
    series.points
      .map((point, index) => {
        const x = scaleX(Date.parse(point.observedAt))
        const y = scaleY(point.value)
        return `${index === 0 ? 'M' : 'L'} ${x.toFixed(1)} ${y.toFixed(1)}`
      })
      .join(' ')

  return (
    <Show when={bounds()}>
      <div class="scroll-x">
        <svg
          class="metric-chart"
          viewBox={`0 0 ${WIDTH} ${HEIGHT}`}
          role="img"
          aria-label={`Chart of ${props.series.map((s) => s.name).join(', ')}`}
        >
          {/* Gridlines and value labels */}
          <For each={[0, 0.25, 0.5, 0.75, 1]}>
            {(fraction) => {
              const y = PAD.top + fraction * (HEIGHT - PAD.top - PAD.bottom)
              const value = bounds()!.maxValue * (1 - fraction)
              return (
                <>
                  <line class="metric-grid" x1={PAD.left} y1={y} x2={WIDTH - PAD.right} y2={y} />
                  <text class="metric-axis" x={PAD.left - 8} y={y + 4} text-anchor="end">
                    {formatValue(value, props.series[0]?.unit ?? '')}
                  </text>
                </>
              )
            }}
          </For>

          <For each={props.series}>
            {(series, index) => (
              <path
                class="metric-line"
                d={pathFor(series)}
                style={{ stroke: `var(--series-${(index() % 6) + 1})` }}
                // A dash pattern per series, so the chart is readable without
                // relying on colour alone.
                stroke-dasharray={DASHES[index() % DASHES.length]}
              />
            )}
          </For>
        </svg>
      </div>
    </Show>
  )
}

const DASHES = ['', '8 4', '2 3', '12 3 2 3', '5 3 1 3', '14 4']

function MetricLegend(props: { series: JobMetricSeries[] }): JSX.Element {
  return (
    <ul class="metric-legend">
      <For each={props.series}>
        {(series, index) => {
          const latest = () => series.points[series.points.length - 1]
          return (
            <li class="metric-legend-item">
              <span
                class="metric-swatch"
                style={{ background: `var(--series-${(index() % 6) + 1})` }}
                aria-hidden="true"
              />
              <span class="metric-legend-name">{friendlyName(series)}</span>
              <span class="metric-legend-value mono">
                {latest() ? formatValue(latest().value, series.unit) : '—'}
              </span>
            </li>
          )
        }}
      </For>
    </ul>
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
