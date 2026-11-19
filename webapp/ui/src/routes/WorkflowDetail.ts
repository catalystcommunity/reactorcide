import type { PushButton } from '@catalystcommunity/controls'
import { api } from '~/api/client.ts'
import type { GetCapabilitiesResponse, GetEventResponse, GetWorkflowResponse } from '~/api/csilapi/types.gen.ts'
import { RcPage } from '~/app/page.ts'
import { DagView } from '~/components/DagView.ts'
import { diagnosisCard, type DiagnosisCard } from '~/components/Diagnoses.ts'
import { describeError } from '~/components/ManagementPage.ts'
import { ReasonList, reasonSummary } from '~/components/Reasons.ts'
import { OwnerView } from '~/components/Owner.ts'
import { actorText, ownerGroupText, requiredStateBadge, setRequiredState } from '~/components/Labels.ts'
import { refClassLabels, setRefClasses } from '~/components/RefClasses.ts'
import { ShiftFields } from '~/components/ShiftFields.ts'
import { ErrorNotice, ResourceView, duration, relativeTime } from '~/components/States.ts'
import { isTerminal, setStatus, statusBadge } from '~/components/StatusBadge.ts'
import type { ResourceHandle } from '~/store/index.ts'
import { useCapabilities, useWorkflow } from '~/store/resources.ts'
import { button } from '~/ui/controls.ts'
import { h, setAttr, setText, show, syncKeyed } from '~/ui/dom.ts'
import { Loader } from '~/ui/load.ts'
import { define } from '~/ui/element.ts'
import { href, navigate } from '~/ui/router.ts'

type Tab = 'jobs' | 'graph'
type WorkflowNode = GetWorkflowResponse['nodes'][number]
type WorkflowJob = GetWorkflowResponse['jobs'][number]
type WorkflowEvent = NonNullable<GetWorkflowResponse['events']>[number]

/**
 * One workflow: its jobs, and optionally its graph.
 *
 * The graph is a tab rather than the default because a list is what most people
 * want most of the time, and a wide DAG pushes everything else off screen.
 * Both views read the same live store, so both animate as the workflow runs.
 */
export class WorkflowDetailPage extends RcPage {
  #workflow!: ResourceHandle<GetWorkflowResponse>
  #caps!: ResourceHandle<GetCapabilitiesResponse>
  #view!: ResourceView<GetWorkflowResponse>
  #diagnoses!: DiagnosisCard
  #tab: Tab = 'jobs'
  #actionError = new ErrorNotice()
  // The workflow's event, for the "Evaluate event" row. A failure (the event
  // is older than its retention, or not visible) only hides that row.
  #event = new Loader<string, GetEventResponse | null>(this, async (eventId) => {
    try {
      return await api.getEvent({ eventId })
    } catch {
      return null
    }
  })
  #applyEvent: () => void = () => {}

  #id(): string {
    return this.route().id ?? ''
  }

  protected build(): void {
    this.#view = new ResourceView<GetWorkflowResponse>({ content: () => this.#content() })
    // Outside the view: the entries load on their own, keyed by the status.
    this.#diagnoses = diagnosisCard()
    this.append(
      h(
        'div',
        { class: 'page' },
        h('div', { style: { marginBottom: 'var(--space-4)' } }, h('a', { href: href('/'), class: 'meta' }, '← All workflows')),
        this.#actionError.el,
        this.#view.el,
        this.#diagnoses,
      ),
    )
  }

  protected connected(): void {
    this.#workflow = useWorkflow(this, () => this.#id())
    // Scoped to this workflow (see JobDetail): cancelling one cancels its
    // jobs, so the job capabilities are the right question. The workflow
    // scope adds the owners of this workflow (workflow roles, owner group).
    this.#caps = useCapabilities(this, () => {
      const workflow = this.#workflow.state().data?.workflow
      return workflow ? { projectId: workflow.projectId, workflowId: workflow.workflowId } : undefined
    })
  }

  render(): void {
    this.#workflow.sync()
    this.#caps.sync()
    this.#view.update(this.#workflow.state())
    const workflow = this.#workflow.state().data?.workflow
    this.#diagnoses.setTarget(workflow ? { workflowId: this.#id() } : undefined, workflow?.status ?? '')
    this.#event.sync(this.#workflow.state().data?.workflow.eventId || undefined)
    this.#applyEvent()
  }

  async #act(work: () => Promise<unknown>, fallback: string): Promise<void> {
    this.#actionError.set(undefined)
    try {
      await work()
      await this.#workflow.refresh()
    } catch (cause) {
      if (this.isActive()) this.#actionError.set(describeError(cause, fallback))
    }
  }

  #content(): { node: Node; update(data: GetWorkflowResponse): void } {
    const title = h('h1')
    const badge = statusBadge()
    const required = requiredStateBadge()
    const subtitle = h('p', { class: 'meta' })
    const cancel: PushButton = button('Cancel workflow', {
      small: true,
      variant: 'danger',
      onClick: () => void this.#act(() => api.cancelWorkflow({ workflowInstanceId: this.#id() }), 'The workflow could not be cancelled.'),
    })
    // A retry or a re-run can change only the shifts (resources and
    // timeout). It keeps the recorded authority of this workflow.
    const shifts = new ShiftFields('retry or re-run')
    const retry: PushButton = button('Retry unsuccessful', {
      small: true,
      onClick: () =>
        void this.#act(async () => {
          await api.retryUnsuccessfulJobs({ workflowInstanceId: this.#id(), shifts: shifts.value() })
          shifts.clear()
        }, 'The jobs could not be retried.'),
    })
    const rerun: PushButton = button('Re-run', {
      small: true,
      onClick: () =>
        void this.#act(async () => {
          const result = await api.rerunWorkflow({ workflowInstanceId: this.#id(), shifts: shifts.value() })
          shifts.clear()
          if (this.isActive()) navigate(`/workflows/${result.workflowInstanceId}`)
        }, 'The workflow could not be re-run.'),
    })

    const dd = (cls: string): HTMLElement => h('dd', { class: cls })
    const idValue = dd('mono truncate')
    const commitValue = dd('mono')
    const jobsValue = dd('')
    const durationValue = dd('')
    const originValue = dd('mono')
    const profileValue = dd('mono')
    const originItem = h('div', null, h('dt', null, 'CI origin'), originValue)
    const actorValue = dd('')
    const actorItem = h('div', null, h('dt', null, 'Started by'), actorValue)
    const ownerValue = dd('mono')
    const ownerItem = h('div', null, h('dt', null, 'Owner group'), ownerValue)
    const profileItem = h('div', null, h('dt', null, 'Execution profile'), profileValue)
    const decision = h('p', { class: 'meta', style: { marginTop: 'var(--space-3)' } })

    const jobsTab = h('button', { type: 'button', class: 'tab', role: 'tab' }, 'Jobs')
    const graphTab = h('button', { type: 'button', class: 'tab', role: 'tab' }, 'Graph')
    const nodeTable = new NodeTable()
    const dag = new DagView()

    // Why is this workflow running? Closed by default: most visits are about
    // the jobs, and the answer is one click away when it is the question.
    const whyReasons = new ReasonList()
    const whyClasses = refClassLabels()
    const whyOwner = new OwnerView()
    const whyEventLink = h('a', null, 'View the event and every workflow it considered')
    const whyLegacy = h(
      'p',
      { class: 'meta' },
      'No reason was recorded for this workflow. It was created before Reactorcide recorded why workflows run, or not from an event.',
    )
    const why = h(
      'details',
      { class: 'card disclosure why' },
      h('summary', null, 'Why is this workflow running?'),
      h('div', { class: 'why-ref-classes' }, h('div', { class: 'field-label' }, 'Ref classes'), whyClasses),
      whyOwner.el,
      whyLegacy,
      whyReasons.el,
      h('p', { style: { marginTop: 'var(--space-3)' } }, whyEventLink),
    )

    // What the coordinator decided, in order. Closed by default.
    const eventsCount = h('span', { class: 'meta' })
    const timeline = h('ol', { class: 'timeline' })
    const events = h('details', { class: 'card disclosure workflow-events' }, h('summary', null, 'Events ', eventsCount), timeline)
    let latest: GetWorkflowResponse | undefined

    const applyTab = (): void => {
      setAttr(jobsTab, 'aria-selected', String(this.#tab === 'jobs'))
      setAttr(graphTab, 'aria-selected', String(this.#tab === 'graph'))
      show(nodeTable.el, this.#tab === 'jobs')
      show(dag.el, this.#tab === 'graph')
      // The graph is laid out only while it shows.
      if (latest && this.#tab === 'graph') dag.update(latest.nodes)
    }
    jobsTab.addEventListener('click', () => {
      this.#tab = 'jobs'
      applyTab()
    })
    graphTab.addEventListener('click', () => {
      this.#tab = 'graph'
      applyTab()
    })

    const node = h(
      'div',
      null,
      h(
        'div',
        { class: 'page-header' },
        h('div', null, h('div', { class: 'row' }, title, badge, required), subtitle),
        h('div', { class: 'row' }, cancel, retry, rerun),
      ),
      shifts.el,
      h(
        'div',
        { class: 'card' },
        h(
          'dl',
          { class: 'grid-dl' },
          h('div', null, h('dt', null, 'Workflow ID'), idValue),
          h('div', null, h('dt', null, 'Commit'), commitValue),
          h('div', null, h('dt', null, 'Jobs'), jobsValue),
          h('div', null, h('dt', null, 'Duration'), durationValue),
          actorItem,
          ownerItem,
          originItem,
          profileItem,
        ),
        decision,
      ),
      why,
      h(
        'div',
        { class: 'card' },
        h('div', { class: 'tabs', role: 'tablist', 'aria-label': 'Workflow views' }, jobsTab, graphTab),
        nodeTable.el,
        dag.el,
      ),
      events,
    )

    this.#applyEvent = () => {
      const event = this.#event.state.data
      // The eval belongs to the workflow only when it made this one workflow.
      const fold = event && event.workflowIds.length === 1 && event.event.evalJobId ? event : undefined
      nodeTable.setEval(fold)
    }

    return {
      node,
      update: (data) => {
        latest = data
        const wf = data.workflow
        const caps = this.#caps.state().data
        setText(title, wf.name)
        setStatus(badge, wf.status)
        setText(subtitle, `${wf.vcsRepo || 'No repository'} · ${relativeTime(wf.updatedAt)}`)
        show(cancel, Boolean(caps?.cancelJob) && !isTerminal(wf.status))
        const unsuccessful = wf.status === 'failed' || wf.status === 'cancelled'
        show(retry, Boolean(caps?.retryJob) && unsuccessful)
        show(rerun, Boolean(caps?.retryJob) && isTerminal(wf.status))
        show(shifts.el, Boolean(caps?.retryJob) && isTerminal(wf.status))
        setText(idValue, wf.workflowId)
        setText(commitValue, wf.commitSha?.slice(0, 12) || '—')
        setText(jobsValue, `${wf.completedCount} of ${wf.jobCount} complete${wf.failedCount > 0 ? `, ${wf.failedCount} failed` : ''}`)
        setText(durationValue, duration(wf.createdAt, wf.completedAt))
        show(originItem, wf.ciOrigin)
        setText(originValue, wf.ciOrigin ?? '')
        show(actorItem, wf.actor)
        setText(actorValue, actorText(wf.actor))
        const group = ownerGroupText(wf.owner)
        show(ownerItem, group)
        setText(ownerValue, group)
        setRequiredState(required, data.why?.requiredState, data.why?.requiredLabel)
        show(profileItem, wf.executionProfile)
        setText(profileValue, wf.executionProfile ?? '')
        show(decision, wf.decisionSummary)
        setText(decision, wf.decisionSummary ?? '')
        nodeTable.update(data.nodes, data.jobs)
        this.#applyEvent()

        const reasons = data.why?.reasons ?? []
        show(whyLegacy, reasons.length === 0)
        show(whyReasons.el, reasons.length > 0)
        whyReasons.update(reasons, { projectId: wf.projectId })
        setRefClasses(whyClasses, wf.refClasses)
        whyOwner.update(data.why?.owner ?? wf.owner)
        show(whyEventLink.parentElement!, wf.eventId)
        setAttr(whyEventLink, 'href', wf.eventId ? href(`/events/${wf.eventId}`) : null)

        const list = data.events ?? []
        show(events, list.length > 0)
        setText(eventsCount, `(${list.length})`)
        const nodeNames = new Map(data.nodes.map((n) => [n.nodeId, n.displayName || n.name]))
        // The list only grows, oldest first, so a position is a stable key.
        syncKeyed(timeline, list.map((e, i) => ({ e, i })), (x) => String(x.i), createEvent, (el, x) => updateEvent(el, x.e, nodeNames))
        applyTab()
      },
    }
  }
}

/**
 * Nodes with their jobs.
 *
 * Keyed on the node rather than the job: a node exists before its job is
 * submitted, and showing it as pending is more informative than hiding it until
 * something starts.
 */
class NodeTable {
  readonly el: HTMLDivElement
  readonly #empty: HTMLDivElement
  readonly #table: HTMLDivElement
  readonly #body: HTMLTableSectionElement
  readonly #evalBody: HTMLTableSectionElement
  readonly #evalLink: HTMLAnchorElement
  readonly #evalStatus: HTMLSpanElement
  readonly #evalReason: HTMLAnchorElement

  constructor() {
    this.#empty = h('div', { class: 'empty' }, 'No nodes yet.')
    this.#body = h('tbody')
    // The eval that made this workflow, first. Its own tbody, because the
    // node tbody holds only keyed rows.
    this.#evalLink = h('a', null, 'Evaluate event')
    this.#evalStatus = statusBadge()
    this.#evalReason = h('a', { class: 'meta' }, 'Decided which workflows run for this event')
    this.#evalBody = h(
      'tbody',
      { class: 'eval-row', hidden: true },
      h(
        'tr',
        null,
        h('td', null, this.#evalLink, h('div', null, this.#evalReason)),
        h('td', null, this.#evalStatus),
        h('td', { class: 'meta' }, '—'),
        h('td', { class: 'meta' }, '—'),
        h('td', { class: 'numeric mono' }, '—'),
      ),
    )
    this.#table = h(
      'div',
      { class: 'scroll-x' },
      h(
        'table',
        { class: 'table' },
        h(
          'thead',
          null,
          h('tr', null, h('th', null, 'Node'), h('th', null, 'Status'), h('th', null, 'Depends on'), h('th', null, 'Runtime'), h('th', null, 'Exit')),
        ),
        this.#evalBody,
        this.#body,
      ),
    )
    this.el = h('div', null, this.#empty, this.#table)
  }

  /** Shows the "Evaluate event" row for the event that made only this workflow; undefined hides it. */
  setEval(event: GetEventResponse | undefined): void {
    show(this.#evalBody, event)
    if (!event) return
    setAttr(this.#evalLink, 'href', href(`/jobs/${event.event.evalJobId}`))
    setAttr(this.#evalReason, 'href', href(`/events/${event.event.eventId}`))
    setStatus(this.#evalStatus, event.event.status)
  }

  update(nodes: WorkflowNode[], jobs: WorkflowJob[]): void {
    show(this.#empty, nodes.length === 0)
    show(this.#table, nodes.length > 0)
    const jobById = new Map(jobs.map((job) => [job.jobId, job]))
    syncKeyed(
      this.#body,
      nodes,
      (node) => node.nodeId,
      () => {
        const reasons = new ReasonList()
        const row = h(
          'tr',
          null,
          h(
            'td',
            null,
            h('a', { class: 'node-link' }),
            h('span', { class: 'node-name' }),
            h('div', { class: 'meta node-reason' }),
            h('details', { class: 'node-why' }, h('summary', null, 'Why'), reasons.el),
          ),
          h('td', null, statusBadge()),
          h('td', { class: 'meta node-deps' }),
          h('td', { class: 'meta node-runtime' }),
          h('td', { class: 'numeric mono node-exit' }),
        ) as HTMLTableRowElement & { reasons: ReasonList }
        row.reasons = reasons
        return row
      },
      (row, node) => {
        const label = node.displayName || node.name
        const job = node.jobId ? jobById.get(node.jobId) : undefined
        const link = row.querySelector<HTMLAnchorElement>('.node-link')!
        const plain = row.querySelector<HTMLElement>('.node-name')!
        show(link, node.jobId)
        show(plain, !node.jobId)
        setAttr(link, 'href', node.jobId ? href(`/jobs/${node.jobId}`) : null)
        setText(link, label)
        setText(plain, label)
        // The summary line is the server's sentence; the details list every
        // check, including the ones that passed.
        const summary = node.decisionReason || reasonSummary(node.decisionReasons)
        const reason = row.querySelector<HTMLElement>('.node-reason')!
        show(reason, summary)
        setText(reason, summary)
        const details = row.querySelector<HTMLDetailsElement>('.node-why')!
        show(details, node.decisionReasons?.length)
        row.reasons.update(node.decisionReasons)
        setStatus(row.querySelector('.status')!, node.status)
        setText(row.querySelector('.node-deps')!, node.dependsOn?.join(', ') || '—')
        setText(row.querySelector('.node-runtime')!, duration(job?.startedAt, job?.completedAt))
        setText(row.querySelector('.node-exit')!, job?.exitCode !== undefined ? String(job.exitCode) : '—')
      },
    )
  }
}

function createEvent(): HTMLLIElement {
  return h(
    'li',
    { class: 'timeline-item' },
    h('time', { class: 'timeline-time' }),
    h('div', { class: 'timeline-body' }, h('span', { class: 'timeline-type' }), h('span', { class: 'timeline-node meta' }), h('div', { class: 'timeline-reason' })),
  )
}

function updateEvent(el: HTMLLIElement, e: WorkflowEvent, nodeNames: Map<string, string>): void {
  const time = el.querySelector('time')!
  setAttr(time, 'datetime', e.createdAt)
  setText(time, e.createdAt ? new Date(e.createdAt).toLocaleTimeString() : '')
  setText(el.querySelector('.timeline-type')!, e.eventType.replace(/_/g, ' '))
  const node = e.nodeId ? nodeNames.get(e.nodeId) : undefined
  setText(el.querySelector('.timeline-node')!, node ? ` · ${node}` : '')
  const reason = el.querySelector<HTMLElement>('.timeline-reason')!
  show(reason, e.reason)
  setText(reason, e.reason)
}

export const tag = 'rc-workflow-detail-page'
define(tag, WorkflowDetailPage)
