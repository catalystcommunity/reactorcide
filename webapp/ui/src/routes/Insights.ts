import { api } from '~/api/client.ts'
import type {
  GetInfraDashboardResponse,
  GetPipelineDashboardResponse,
  GetUsageDashboardResponse,
  ObsClassCount,
  ObsFlakyTest,
  ObsGroupUsage,
  ObsLaneHealth,
  ObsQueueBacklog,
  ObsSloStatus,
  ObsWorkflowHealth,
  OrgSummary,
} from '~/api/csilapi/types.gen.ts'
import { RcPage } from '~/app/page.ts'
import {
  LineChart,
  MAX_SERIES,
  barCell,
  formatDuration,
  formatNumber,
  formatPercent,
  sparkline,
  updateBar,
  updateSparkline,
  type ChartSeries,
} from '~/components/Charts.ts'
import { alertRulesEditor } from '~/components/AlertRules.ts'
import '~/components/insights.css'
import { OrgPicker, pickInitialOrg } from '~/components/OrgPicker.ts'
import { ErrorNotice, skeleton } from '~/components/States.ts'
import { SESSION } from '~/lib/session.ts'
import type { ResourceHandle } from '~/store/index.ts'
import { useOrgs } from '~/store/resources.ts'
import { h, setAttr, setText, show, syncKeyed } from '~/ui/dom.ts'
import { define } from '~/ui/element.ts'
import { Loader } from '~/ui/load.ts'
import { ROUTE, href, setQuery } from '~/ui/router.ts'

/**
 * Insights: the dashboards of docs/observability.md.
 *
 * - Pipeline (pipeline owners): the health of each workflow, the failure
 *   class mix, flaky tests, resources, and alert rules.
 * - Infrastructure (CI owners): infrastructure failures by class, queue
 *   wait by lane, eval latency, SLOs, backlog, and workers.
 * - Usage (directors): usage by group over 7, 30, or 90 days.
 *
 * Every view shows only the projects that the viewer can see. A public
 * project is visible with no login.
 */

export type InsightsView = 'pipeline' | 'infrastructure' | 'usage'
const VIEWS: { view: InsightsView; label: string }[] = [
  { view: 'pipeline', label: 'Pipelines' },
  { view: 'infrastructure', label: 'Infrastructure' },
  { view: 'usage', label: 'Usage' },
]
const DAYS = [7, 30, 90]

export function parseView(value: string | null): InsightsView {
  return value === 'infrastructure' || value === 'usage' ? value : 'pipeline'
}

export function parseDays(value: string | null): number {
  const n = Number(value)
  return DAYS.includes(n) ? n : 30
}

/** Failure class display: pipeline classes first, then infrastructure. */
export function classOwner(cls: string): string {
  if (cls === 'job' || cls === 'timeout' || cls === 'oom') return 'pipeline'
  if (cls.startsWith('infra.')) return 'infrastructure'
  return ''
}

export function classMixText(classes: ObsClassCount[]): string {
  const parts = classes.filter((c) => c.count > 0 && c.failureClass !== 'cancelled').map((c) => `${c.failureClass} ${c.count}`)
  return parts.length ? parts.join(', ') : '—'
}

/** Folds the series after the first `MAX_SERIES - 1` into "Other" when there are more than `MAX_SERIES`. */
export function foldSeries(series: ChartSeries[]): ChartSeries[] {
  if (series.length <= MAX_SERIES) return series
  const kept = series.slice(0, MAX_SERIES - 1)
  const rest = series.slice(MAX_SERIES - 1)
  const other: ChartSeries = {
    key: '__other',
    label: `Other (${rest.length})`,
    points: rest[0].points.map((p, i) => ({ x: p.x, y: rest.reduce((sum, one) => sum + (one.points[i]?.y ?? 0), 0) })),
  }
  return [...kept, other]
}

// ---------------------------------------------------------------------------
// Small parts.

interface Column<T> {
  label: string
  numeric?: boolean
  text: (row: T) => string
  /** Decoration after the text (a sparkline, a bar). */
  decorate?: () => Element
  update?: (el: Element, row: T) => void
}

class DataTable<T> {
  readonly el: HTMLElement
  readonly #body: HTMLTableSectionElement
  readonly #empty: HTMLElement
  readonly #table: HTMLElement

  constructor(
    readonly columns: Column<T>[],
    readonly key: (row: T) => string,
    emptyText: string,
  ) {
    this.#body = h('tbody')
    this.#table = h(
      'div',
      { class: 'scroll-x' },
      h('table', { class: 'table' }, h('thead', null, h('tr', null, columns.map((c) => h('th', c.numeric ? { class: 'numeric' } : null, c.label)))), this.#body),
    )
    this.#empty = h('div', { class: 'meta' }, emptyText)
    this.el = h('div', null, this.#table, this.#empty)
  }

  update(rows: readonly T[]): void {
    show(this.#table, rows.length > 0)
    show(this.#empty, rows.length === 0)
    syncKeyed(
      this.#body,
      rows,
      this.key,
      () =>
        h(
          'tr',
          null,
          this.columns.map((c) => h('td', c.numeric ? { class: 'numeric' } : null, h('span', { class: 'cell-text' }), c.decorate ? c.decorate() : null)),
        ),
      (tr, row) => {
        this.columns.forEach((c, i) => {
          const td = tr.children[i] as HTMLElement
          setText(td.firstElementChild!, c.text(row))
          if (c.update && td.children[1]) c.update(td.children[1], row)
        })
      },
    )
  }
}

class Tile {
  readonly el: HTMLElement
  readonly #value: HTMLElement
  readonly #note: HTMLElement
  constructor(label: string) {
    this.#value = h('div', { class: 'insight-tile-value' })
    this.#note = h('div', { class: 'insight-tile-note' })
    this.el = h('div', { class: 'insight-tile' }, h('div', { class: 'insight-tile-label' }, label), this.#value, this.#note)
  }
  set(value: string, note = ''): void {
    setText(this.#value, value)
    setText(this.#note, note)
  }
}

/** A state with a glyph and a word: never color alone. */
function stateText(state: string): string {
  switch (state) {
    case 'firing':
      return '▲ Firing'
    case 'ok':
      return '✓ OK'
    case 'warning':
      return '! Warning'
    case 'exhausted':
      return '✕ Exhausted'
    case 'no_data':
      return '– No data'
  }
  return state
}

function card(title: string, description: string, ...children: (Node | null)[]): HTMLElement {
  return h('section', { class: 'card' }, h('h2', null, title), description ? h('p', { class: 'meta' }, description) : null, ...children)
}

// ---------------------------------------------------------------------------
// Views. Each view is made one time and updated with each response.

class PipelineView {
  readonly el: HTMLElement
  readonly #runs = new Tile('Runs')
  readonly #pass = new Tile('Pass rate, default branch')
  readonly #duration = new Tile('Duration p95')
  readonly #cpu = new Tile('CPU core-hours')
  readonly #passChart = new LineChart({ title: 'Pass rate by day', description: 'Successes / (successes + job, timeout, and oom failures). Infrastructure failures do not count.', unit: 'percent' })
  readonly #durationChart = new LineChart({ title: 'Duration p95 by day', description: 'Run time of pipeline runs (successes and pipeline failures).', unit: 'seconds' })
  readonly #classes = new DataTable<ObsClassCount>(
    [
      { label: 'Failure class', text: (c) => c.failureClass },
      { label: 'Owner', text: (c) => classOwner(c.failureClass) || '—' },
      { label: 'Runs', numeric: true, text: (c) => String(c.count) },
    ],
    (c) => c.failureClass,
    'No failures in this range.',
  )
  readonly #workflows = new DataTable<ObsWorkflowHealth>(
    [
      { label: 'Workflow', text: (w) => w.workflowSecurityId || '(no workflow)' },
      { label: 'Project', text: (w) => w.projectName ?? w.projectId ?? '—' },
      { label: 'Owner group', text: (w) => w.ownerGroup || '—' },
      { label: 'Runs', numeric: true, text: (w) => String(w.summary.runs) },
      {
        label: 'Pass rate, default branch',
        numeric: true,
        text: (w) => formatPercent(w.defaultBranch.passRate),
        decorate: sparkline,
        update: (el, w) => updateSparkline(el as SVGSVGElement, w.trend.map((p) => p.passRate), 1),
      },
      { label: 'Pass rate, all', numeric: true, text: (w) => formatPercent(w.summary.passRate) },
      { label: 'Duration p95', numeric: true, text: (w) => formatDuration(w.summary.durationP95Seconds) },
      { label: 'Failure classes', text: (w) => classMixText(w.summary.byClass) },
      { label: 'Core-hours per run', numeric: true, text: (w) => formatNumber(w.cpuCoreHoursPerRun, 3) },
      { label: 'Core-hours', numeric: true, text: (w) => formatNumber(w.summary.cpuCoreHours, 2) },
    ],
    (w) => `${w.projectId ?? ''}/${w.workflowSecurityId}`,
    'No workflow runs in this range.',
  )
  readonly #flaky = new DataTable<ObsFlakyTest>(
    [
      { label: 'Test', text: (t) => [t.classname, t.name].filter(Boolean).join('.') },
      { label: 'Suite', text: (t) => t.suite || '—' },
      { label: 'Flaky commits', numeric: true, text: (t) => String(t.flakyCount) },
      { label: 'Runs', numeric: true, text: (t) => String(t.totalRuns) },
      { label: 'Last flaky', text: (t) => (t.lastFlakyAt ? new Date(t.lastFlakyAt).toLocaleString() : '—') },
    ],
    (t) => `${t.projectId ?? ''}/${t.testId}`,
    'No flaky tests in this range.',
  )
  // The alert rules, with the editor (components/AlertRules.ts). It has its
  // own load (list-alert-rules), so it knows which rules the caller can change.
  readonly #alerts = alertRulesEditor()

  constructor() {
    this.el = h(
      'div',
      { class: 'stack' },
      h('div', { class: 'insight-tiles' }, this.#runs.el, this.#pass.el, this.#duration.el, this.#cpu.el),
      card('Trends', '', h('div', { class: 'insight-charts' }, this.#passChart.el, this.#durationChart.el)),
      card('Workflows', 'Lowest default-branch pass rate first. A pipeline owner owns pass rate and duration; infrastructure classes belong to the CI owner.', this.#workflows.el),
      card('Failure class mix', '', this.#classes.el),
      card('Flaky tests', 'Tests that passed and failed on the same commit.', this.#flaky.el),
      card(
        'Alert rules',
        'Only the classes job, timeout, and oom count. Firing and resolved events go to outbound event subscriptions. A project owner manages the rules of a project; an org admin manages owner group rules.',
        this.#alerts,
      ),
    )
  }

  update(d: GetPipelineDashboardResponse): void {
    const t = d.totals
    this.#runs.set(String(t.runs), `${t.successes} passed, ${t.pipelineFailures} pipeline failures, ${t.infraFailures} infrastructure`)
    const def = d.workflows.reduce(
      (acc, w) => ({ s: acc.s + w.defaultBranch.successes, n: acc.n + w.defaultBranch.successes + w.defaultBranch.pipelineFailures }),
      { s: 0, n: 0 },
    )
    this.#pass.set(def.n ? formatPercent(def.s / def.n) : '—', `${def.n} pipeline runs on the default branch`)
    this.#duration.set(formatDuration(t.durationP95Seconds), `p50 ${formatDuration(t.durationP50Seconds)}`)
    this.#cpu.set(formatNumber(t.cpuCoreHours, 2), `${formatNumber(t.memoryGibHours, 2)} GiB-hours memory, ${t.measuredRuns} measured runs`)
    this.#passChart.update([{ key: 'pass', label: 'Pass rate', points: d.trend.map((p) => ({ x: p.bucketStart, y: p.passRate })) }])
    this.#durationChart.update([{ key: 'p95', label: 'Duration p95', points: d.trend.map((p) => ({ x: p.bucketStart, y: p.durationP95Seconds })) }])
    this.#workflows.update(d.workflows)
    this.#classes.update(
      [...t.byClass].filter((c) => c.count > 0).sort((a, b) => (classOwner(a.failureClass) === 'pipeline' ? 0 : 1) - (classOwner(b.failureClass) === 'pipeline' ? 0 : 1)),
    )
    this.#flaky.update(d.flakyTests)
  }

  /** The organization and the project (or none) of the alert rules. */
  setAlertScope(orgId: string, projectId: string): void {
    this.#alerts.setScope({ orgId, projectId: projectId || undefined })
  }
}

const SLI_LABELS: Record<string, string> = {
  eval_latency: 'Eval latency (webhook to first status)',
  queue_wait: 'Queue wait',
  infra_failure_rate: 'Infrastructure failure rate',
  availability: 'Control-plane availability',
}

class InfraView {
  readonly el: HTMLElement
  readonly #infra = new Tile('Infrastructure failure rate')
  readonly #wait = new Tile('Queue wait p95')
  readonly #eval = new Tile('Eval latency p95')
  readonly #backlog = new Tile('Backlog')
  readonly #workers = new Tile('Workers')
  readonly #rateChart = new LineChart({ title: 'Infrastructure failure rate by day', description: 'infra.* failures / finished runs that were not cancelled.', unit: 'percent' })
  readonly #latencyChart = new LineChart({ title: 'Latency p95 by day', description: 'Queue wait of jobs, and webhook to first commit status of CI events.', unit: 'seconds' })
  readonly #slos = new DataTable<ObsSloStatus>(
    [
      { label: 'SLI', text: (s) => SLI_LABELS[s.sli] ?? s.sli },
      { label: 'Lane', text: (s) => s.lane || '—' },
      { label: 'Objective', numeric: true, text: (s) => `${formatPercent(s.objective, 2)}${s.thresholdSeconds ? ` within ${formatDuration(s.thresholdSeconds)}` : ''}` },
      { label: 'Compliance', numeric: true, text: (s) => formatPercent(s.compliance, 2) },
      { label: 'p95', numeric: true, text: (s) => formatDuration(s.p95Seconds) },
      { label: 'Events', numeric: true, text: (s) => formatNumber(s.total, 0) },
      {
        label: 'Error budget left',
        numeric: true,
        text: (s) => (s.budgetState === 'no_data' ? '—' : formatPercent(s.errorBudgetRemaining, 0)),
        decorate: barCell,
        update: (el, s) => updateBar(el as HTMLElement, Math.max(0, s.errorBudgetRemaining), 1),
      },
      { label: 'State', text: (s) => stateText(s.budgetState) },
      { label: `Window`, numeric: true, text: (s) => `${s.windowDays} d` },
    ],
    (s) => `${s.sli}/${s.lane}`,
    'No SLO data yet.',
  )
  readonly #lanes = new DataTable<ObsLaneHealth>(
    [
      { label: 'Lane', text: (l) => l.lane || '(none)' },
      { label: 'Runs', numeric: true, text: (l) => String(l.summary.runs) },
      { label: 'Infra failures', numeric: true, text: (l) => String(l.summary.infraFailures) },
      { label: 'Infra failure rate', numeric: true, text: (l) => formatPercent(l.summary.infraFailureRate, 2) },
      { label: 'Queue wait p50', numeric: true, text: (l) => formatDuration(l.summary.queueWaitP50Seconds) },
      { label: 'Queue wait p95', numeric: true, text: (l) => formatDuration(l.summary.queueWaitP95Seconds) },
    ],
    (l) => l.lane,
    'No runs in this range.',
  )
  readonly #infraClasses = new DataTable<ObsClassCount>(
    [
      { label: 'Failure class', text: (c) => c.failureClass },
      { label: 'Runs', numeric: true, text: (c) => String(c.count) },
    ],
    (c) => c.failureClass,
    'No infrastructure failures in this range.',
  )
  readonly #queues = new DataTable<ObsQueueBacklog>(
    [
      { label: 'Queue', text: (q) => q.displayName || q.queueUuid },
      { label: 'Waiting jobs', numeric: true, text: (q) => String(q.count) },
      { label: 'Oldest wait', numeric: true, text: (q) => (q.count ? formatDuration(q.oldestAgeSeconds) : '—') },
    ],
    (q) => q.queueUuid,
    'No queue details (they need access to every project of the organization).',
  )

  constructor() {
    this.el = h(
      'div',
      { class: 'stack' },
      h('div', { class: 'insight-tiles' }, this.#infra.el, this.#wait.el, this.#eval.el, this.#backlog.el, this.#workers.el),
      card('SLOs', 'Error budget over the window of each target. Set targets with "reactorcide slo set".', this.#slos.el),
      card('Trends', '', h('div', { class: 'insight-charts' }, this.#rateChart.el, this.#latencyChart.el)),
      card('Lanes', 'Queue wait and infrastructure failures by priority lane. Per-queue wait is in the Prometheus metric reactorcide_job_queue_wait_seconds.', this.#lanes.el),
      card('Infrastructure failure classes', '', this.#infraClasses.el),
      card('Backlog by queue', '', this.#queues.el),
    )
  }

  update(d: GetInfraDashboardResponse): void {
    const t = d.totals
    this.#infra.set(formatPercent(t.infraFailureRate, 2), `${t.infraFailures} of ${t.runs - t.cancelled} runs`)
    this.#wait.set(formatDuration(t.queueWaitP95Seconds), `p50 ${formatDuration(t.queueWaitP50Seconds)}`)
    this.#eval.set(formatDuration(d.evalP95Seconds), `${d.evalEvents} events with a first status`)
    const c = d.capacity
    this.#backlog.set(String(c.backlogJobs), c.backlogJobs ? `oldest waits ${formatDuration(c.backlogOldestAgeSeconds)}` : 'no job waits')
    this.#workers.set(`${c.workersSeen} / ${c.workersActive}`, `seen in 5 min / active; ${c.workersQuarantined} quarantined, ${c.workersDisabled} disabled`)
    this.#rateChart.update([{ key: 'infra', label: 'Infrastructure failure rate', points: d.trend.map((p) => ({ x: p.bucketStart, y: p.infraFailureRate })) }])
    this.#latencyChart.update([
      { key: 'wait', label: 'Queue wait p95', points: d.trend.map((p) => ({ x: p.bucketStart, y: p.queueWaitP95Seconds })) },
      { key: 'eval', label: 'Eval latency p95', points: d.trend.map((p) => ({ x: p.bucketStart, y: p.evalP95Seconds })) },
    ])
    this.#slos.update(d.slos)
    this.#lanes.update(d.lanes)
    this.#infraClasses.update(t.byClass.filter((x) => x.failureClass.startsWith('infra.') && x.count > 0))
    this.#queues.update(c.queues)
  }
}

class UsageView {
  readonly el: HTMLElement
  readonly #attribution: HTMLElement
  readonly #note: HTMLElement
  readonly #chart = new LineChart({ title: 'CPU core-hours by day', description: 'The groups with the most use; the rest are in "Other".', unit: 'number' })
  #max = 0
  readonly #groups = new DataTable<ObsGroupUsage>(
    [
      { label: 'Group', text: (g) => g.group || '(no group)' },
      {
        label: 'CPU core-hours',
        numeric: true,
        text: (g) => formatNumber(g.cpuCoreHours, 2),
        decorate: barCell,
        update: (el, g) => updateBar(el as HTMLElement, g.cpuCoreHours, this.#max),
      },
      { label: 'Reserved core-hours', numeric: true, text: (g) => formatNumber(g.cpuReservedCoreHours, 2) },
      { label: 'Memory GiB-hours', numeric: true, text: (g) => formatNumber(g.memoryGibHours, 2) },
      {
        label: 'Runs',
        numeric: true,
        text: (g) => String(g.runs),
        decorate: sparkline,
        update: (el, g) => updateSparkline(el as SVGSVGElement, g.series.map((p) => p.runs)),
      },
      { label: 'Pass rate', numeric: true, text: (g) => formatPercent(g.passRate) },
      { label: 'Infra failures', numeric: true, text: (g) => String(g.infraFailures) },
    ],
    (g) => g.group,
    'No usage in this range.',
  )

  constructor() {
    this.#attribution = h('span', { class: 'insights-segment', role: 'group', 'aria-label': 'Attribution' })
    this.#note = h('p', { class: 'meta' })
    this.el = h(
      'div',
      { class: 'stack' },
      card('Usage by group', '', h('div', { class: 'row' }, h('span', { class: 'field-label' }, 'Attribute to'), this.#attribution), this.#note, this.#groups.el),
      card('Over time', '', this.#chart.el),
    )
    for (const [value, label] of [
      ['owner', 'Owner group'],
      ['actor', 'Group of the actor'],
    ]) {
      this.#attribution.append(h('a', { class: 'btn btn-sm', 'data-attribution': value }, label))
    }
  }

  setAttribution(current: string, query: URLSearchParams): void {
    for (const a of this.#attribution.children) {
      const value = (a as HTMLElement).dataset.attribution!
      const q = new URLSearchParams(query)
      q.set('attribution', value)
      setAttr(a, 'href', `${href('/insights')}?${q.toString()}`)
      setAttr(a, 'aria-current', value === current ? 'true' : null)
    }
    setText(
      this.#note,
      current === 'actor'
        ? 'Each run counts for the groups that its actor was a member of when the run started (group membership history). A run of an actor in two groups counts in both.'
        : 'Each run counts for the owner group of its workflow at the time of the run (the owner snapshot on the job).',
    )
  }

  update(d: GetUsageDashboardResponse): void {
    this.#max = Math.max(0, ...d.groups.map((g) => g.cpuCoreHours))
    this.#groups.update(d.groups)
    this.#chart.update(
      foldSeries(d.groups.map((g) => ({ key: g.group, label: g.group || '(no group)', points: g.series.map((p) => ({ x: p.bucketStart, y: p.cpuCoreHours })) }))),
    )
  }
}

// ---------------------------------------------------------------------------
// The page.

type Key = { view: InsightsView; org: string; days: number; project: string; attribution: string }
type Data = GetPipelineDashboardResponse | GetInfraDashboardResponse | GetUsageDashboardResponse

export class InsightsPage extends RcPage {
  static watches = [ROUTE, SESSION]

  #orgs!: ResourceHandle<{ orgs: OrgSummary[] }>
  #picker!: OrgPicker
  #tabs!: HTMLElement
  #days!: HTMLElement
  #error = new ErrorNotice()
  #loading!: HTMLElement
  #noOrg!: HTMLElement
  #pipeline = new PipelineView()
  #infra = new InfraView()
  #usage = new UsageView()
  #data = new Loader<Key, Data>(this, (key) => {
    const days = key.days
    if (key.view === 'infrastructure') return api.getInfraDashboard({ orgId: key.org, days })
    if (key.view === 'usage') return api.getUsageDashboard({ orgId: key.org, days, attribution: key.attribution })
    return api.getPipelineDashboard({ orgId: key.org, days, projectId: key.project || undefined })
  })

  protected connected(): void {
    this.#orgs = useOrgs(this)
  }

  protected build(): void {
    this.#picker = new OrgPicker({ ariaLabel: 'Organization', onChange: (org) => setQuery({ org }) })
    this.#tabs = h('nav', { class: 'tabs', 'aria-label': 'Insights view' })
    for (const v of VIEWS) this.#tabs.append(h('a', { class: 'tab', 'data-view': v.view }, v.label))
    this.#days = h('span', { class: 'insights-segment', role: 'group', 'aria-label': 'Look-back range' })
    for (const d of DAYS) this.#days.append(h('a', { class: 'btn btn-sm', 'data-days': String(d) }, `${d} days`))
    this.#loading = skeleton(4)
    this.#noOrg = h('div', { class: 'empty' }, h('div', { class: 'empty-title' }, 'No organization is visible to you.'))
    this.append(
      h(
        'div',
        { class: 'page insights' },
        h(
          'div',
          { class: 'page-header' },
          h('div', null, h('h1', null, 'Insights'), h('p', { class: 'meta' }, 'Pipeline health, infrastructure health, and usage, from the hourly and daily rollups. Rollups update about one time each minute.')),
        ),
        this.#tabs,
        h('div', { class: 'insights-filters' }, h('span', { class: 'field-label' }, 'Organization'), this.#picker.el, this.#days),
        this.#error.el,
        this.#noOrg,
        this.#loading,
        this.#pipeline.el,
        this.#infra.el,
        this.#usage.el,
      ),
    )
  }

  #orgId(orgs: OrgSummary[]): string {
    const requested = this.route().query.get('org') ?? ''
    if (requested && orgs.some((o) => o.orgId === requested)) return requested
    return pickInitialOrg(orgs, () => true)
  }

  render(): void {
    const query = this.route().query
    const view = parseView(query.get('view'))
    const days = parseDays(query.get('days'))
    const attribution = query.get('attribution') === 'actor' ? 'actor' : 'owner'
    const orgs = this.#orgs?.state().data?.orgs ?? []
    const org = this.#orgId(orgs)
    const orgsLoaded = Boolean(this.#orgs?.state().data)

    for (const a of this.#tabs.children) {
      const v = (a as HTMLElement).dataset.view!
      const q = new URLSearchParams(query)
      q.set('view', v)
      setAttr(a, 'href', `${href('/insights')}?${q.toString()}`)
      setAttr(a, 'aria-selected', v === view ? 'true' : 'false')
      setAttr(a, 'aria-current', v === view ? 'page' : null)
    }
    for (const a of this.#days.children) {
      const d = (a as HTMLElement).dataset.days!
      const q = new URLSearchParams(query)
      q.set('days', d)
      setAttr(a, 'href', `${href('/insights')}?${q.toString()}`)
      setAttr(a, 'aria-current', Number(d) === days ? 'true' : null)
    }
    show(this.#picker.el, orgs.length > 0)
    if (orgs.length > 0) this.#picker.update(orgs, org)
    this.#usage.setAttribution(attribution, query)

    show(this.#noOrg, orgsLoaded && !org)
    this.#data.sync(org ? { view, org, days, project: query.get('project') ?? '', attribution } : undefined)
    const state = this.#data.state
    this.#error.set(state.error, Boolean(state.data))
    show(this.#loading, Boolean(org) && !state.data && !state.error)
    show(this.#pipeline.el, view === 'pipeline' && Boolean(state.data))
    show(this.#infra.el, view === 'infrastructure' && Boolean(state.data))
    show(this.#usage.el, view === 'usage' && Boolean(state.data))
    if (!state.data) return
    if (view === 'pipeline') {
      this.#pipeline.update(state.data as GetPipelineDashboardResponse)
      this.#pipeline.setAlertScope(org, query.get('project') ?? '')
    }
    else if (view === 'infrastructure') this.#infra.update(state.data as GetInfraDashboardResponse)
    else this.#usage.update(state.data as GetUsageDashboardResponse)
  }
}

export const tag = 'rc-insights-page'
define(tag, InsightsPage)
