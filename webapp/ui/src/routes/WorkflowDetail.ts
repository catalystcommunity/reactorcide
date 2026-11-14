import type { PushButton } from '@catalystcommunity/controls'
import { api } from '~/api/client.ts'
import type { GetCapabilitiesResponse, GetWorkflowResponse } from '~/api/csilapi/types.gen.ts'
import { RcPage } from '~/app/page.ts'
import { DagView } from '~/components/DagView.ts'
import { describeError } from '~/components/ManagementPage.ts'
import { ErrorNotice, ResourceView, duration, relativeTime } from '~/components/States.ts'
import { isTerminal, setStatus, statusBadge } from '~/components/StatusBadge.ts'
import type { ResourceHandle } from '~/store/index.ts'
import { useCapabilities, useWorkflow } from '~/store/resources.ts'
import { button } from '~/ui/controls.ts'
import { h, setAttr, setText, show, syncKeyed } from '~/ui/dom.ts'
import { define } from '~/ui/element.ts'
import { href } from '~/ui/router.ts'

type Tab = 'jobs' | 'graph'
type WorkflowNode = GetWorkflowResponse['nodes'][number]
type WorkflowJob = GetWorkflowResponse['jobs'][number]

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
  #tab: Tab = 'jobs'
  #actionError = new ErrorNotice()

  #id(): string {
    return this.route().id ?? ''
  }

  protected build(): void {
    this.#view = new ResourceView<GetWorkflowResponse>({ content: () => this.#content() })
    this.append(
      h(
        'div',
        { class: 'page' },
        h('div', { style: { marginBottom: 'var(--space-4)' } }, h('a', { href: href('/'), class: 'meta' }, '← All workflows')),
        this.#actionError.el,
        this.#view.el,
      ),
    )
  }

  protected connected(): void {
    this.#workflow = useWorkflow(this, () => this.#id())
    // Scoped to the workflow's project (see JobDetail). A workflow has no
    // capability of its own: cancelling one cancels its jobs, so the job
    // capabilities are the right question.
    this.#caps = useCapabilities(this, () => {
      const workflow = this.#workflow.state().data?.workflow
      return workflow ? { projectId: workflow.projectId } : undefined
    })
  }

  render(): void {
    this.#workflow.sync()
    this.#caps.sync()
    this.#view.update(this.#workflow.state())
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
    const subtitle = h('p', { class: 'meta' })
    const cancel: PushButton = button('Cancel workflow', {
      small: true,
      variant: 'danger',
      onClick: () => void this.#act(() => api.cancelWorkflow({ workflowInstanceId: this.#id() }), 'The workflow could not be cancelled.'),
    })
    const retry: PushButton = button('Retry unsuccessful', {
      small: true,
      onClick: () => void this.#act(() => api.retryUnsuccessfulJobs({ workflowInstanceId: this.#id() }), 'The jobs could not be retried.'),
    })

    const dd = (cls: string): HTMLElement => h('dd', { class: cls })
    const idValue = dd('mono truncate')
    const commitValue = dd('mono')
    const jobsValue = dd('')
    const durationValue = dd('')
    const originValue = dd('mono')
    const profileValue = dd('mono')
    const originItem = h('div', null, h('dt', null, 'CI origin'), originValue)
    const profileItem = h('div', null, h('dt', null, 'Execution profile'), profileValue)
    const decision = h('p', { class: 'meta', style: { marginTop: 'var(--space-3)' } })

    const jobsTab = h('button', { type: 'button', class: 'tab', role: 'tab' }, 'Jobs')
    const graphTab = h('button', { type: 'button', class: 'tab', role: 'tab' }, 'Graph')
    const nodeTable = new NodeTable()
    const dag = new DagView()
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
        h('div', null, h('div', { class: 'row' }, title, badge), subtitle),
        h('div', { class: 'row' }, cancel, retry),
      ),
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
          originItem,
          profileItem,
        ),
        decision,
      ),
      h(
        'div',
        { class: 'card' },
        h('div', { class: 'tabs', role: 'tablist', 'aria-label': 'Workflow views' }, jobsTab, graphTab),
        nodeTable.el,
        dag.el,
      ),
    )

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
        show(retry, Boolean(caps?.retryJob) && isTerminal(wf.status))
        setText(idValue, wf.workflowId)
        setText(commitValue, wf.commitSha?.slice(0, 12) || '—')
        setText(jobsValue, `${wf.completedCount} of ${wf.jobCount} complete${wf.failedCount > 0 ? `, ${wf.failedCount} failed` : ''}`)
        setText(durationValue, duration(wf.createdAt, wf.completedAt))
        show(originItem, wf.ciOrigin)
        setText(originValue, wf.ciOrigin ?? '')
        show(profileItem, wf.executionProfile)
        setText(profileValue, wf.executionProfile ?? '')
        show(decision, wf.decisionSummary)
        setText(decision, wf.decisionSummary ?? '')
        nodeTable.update(data.nodes, data.jobs)
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

  constructor() {
    this.#empty = h('div', { class: 'empty' }, 'No nodes yet.')
    this.#body = h('tbody')
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
        this.#body,
      ),
    )
    this.el = h('div', null, this.#empty, this.#table)
  }

  update(nodes: WorkflowNode[], jobs: WorkflowJob[]): void {
    show(this.#empty, nodes.length === 0)
    show(this.#table, nodes.length > 0)
    const jobById = new Map(jobs.map((job) => [job.jobId, job]))
    syncKeyed(
      this.#body,
      nodes,
      (node) => node.nodeId,
      () =>
        h(
          'tr',
          null,
          h('td', null, h('a', { class: 'node-link' }), h('span', { class: 'node-name' }), h('div', { class: 'meta node-reason' })),
          h('td', null, statusBadge()),
          h('td', { class: 'meta node-deps' }),
          h('td', { class: 'meta node-runtime' }),
          h('td', { class: 'numeric mono node-exit' }),
        ),
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
        const reason = row.querySelector<HTMLElement>('.node-reason')!
        show(reason, node.decisionReason)
        setText(reason, node.decisionReason ?? '')
        setStatus(row.querySelector('.status')!, node.status)
        setText(row.querySelector('.node-deps')!, node.dependsOn?.join(', ') || '—')
        setText(row.querySelector('.node-runtime')!, duration(job?.startedAt, job?.completedAt))
        setText(row.querySelector('.node-exit')!, job?.exitCode !== undefined ? String(job.exitCode) : '—')
      },
    )
  }
}

export const tag = 'rc-workflow-detail-page'
define(tag, WorkflowDetailPage)
