import type { DescribeFormMetadataResponse, ListWorkflowsResponse, ProjectSummary } from '~/api/csilapi/types.gen.ts'
import { RcPage } from '~/app/page.ts'
import { ChoiceSelect } from '~/components/ChoiceInput.ts'
import { LIST_PAGE_SIZE, ListFilterBar, Pager, listQuery } from '~/components/ListControls.ts'
import { ResourceView, emptyState, relativeTime } from '~/components/States.ts'
import { setStatus, statusBadge } from '~/components/StatusBadge.ts'
import type { ResourceHandle } from '~/store/index.ts'
import { useFormMetadata, useProjects, useWorkflows } from '~/store/resources.ts'
import { actorShort, ownerGroupText } from '~/components/Labels.ts'
import { h, setAttr, setText, show, syncKeyed } from '~/ui/dom.ts'
import { define } from '~/ui/element.ts'
import { href, setQuery } from '~/ui/router.ts'

type WorkflowRow = ListWorkflowsResponse['workflows'][number]

/**
 * The workflow list, updating live.
 *
 * A workflow submitted while this page is open appears without a reload, and a
 * running one recolours as it advances. Before the coordinator emitted workflow
 * events at all, neither was possible: the only workflow-shaped signal on the
 * bus was a job transition.
 */
export class WorkflowsPage extends RcPage {
  #workflows!: ResourceHandle<ListWorkflowsResponse>
  #metadata!: ResourceHandle<DescribeFormMetadataResponse>
  #projects!: ResourceHandle<{ projects: ProjectSummary[] }>
  #status!: ChoiceSelect
  #filters!: ListFilterBar
  #pager!: Pager
  #view!: ResourceView<ListWorkflowsResponse>

  #statusFilter(): string {
    return this.route().query.get('status') ?? ''
  }

  protected build(): void {
    this.#status = new ChoiceSelect({
      ariaLabel: 'Filter by status',
      placeholder: 'All statuses',
      value: this.#statusFilter(),
      onChange: (value) => setQuery({ status: value || undefined, page: undefined }),
    })
    this.#filters = new ListFilterBar({ nameLabel: 'Filter by workflow name' })
    this.#filters.el.append(this.#status.el)
    this.#pager = new Pager()
    this.#view = new ResourceView<ListWorkflowsResponse>({
      isEmpty: (data) => data.workflows.length === 0,
      empty: emptyState('No workflows found.', 'A workflow appears here the moment it is submitted. With filters set, only matching workflows show.'),
      content: () => workflowTable(),
    })
    this.append(
      h(
        'div',
        { class: 'page' },
        h(
          'div',
          { class: 'page-header' },
          h('div', null, h('h1', null, 'Workflows'), h('p', { class: 'meta' }, 'Every workflow you can see, updating as it runs.')),
        ),
        this.#filters.el,
        h('div', { class: 'card' }, this.#view.el, this.#pager.el),
      ),
    )
  }

  protected connected(): void {
    this.#workflows = useWorkflows(this, () => {
      const q = listQuery(this.route().query)
      return { status: this.#statusFilter() || undefined, projectId: q.projectId, name: q.name, branch: q.branch, offset: q.offset, limit: LIST_PAGE_SIZE }
    })
    this.#metadata = useFormMetadata(this)
    this.#projects = useProjects(this)
    this.own(() => this.#filters.dispose())
  }

  render(): void {
    this.#workflows.sync()
    this.#status.update(this.#metadata.state().data?.workflowStatuses ?? [], this.#statusFilter())
    this.#filters.update(listQuery(this.route().query), this.#projects.state().data?.projects)
    const state = this.#workflows.state()
    this.#view.update(state)
    this.#pager.update(state.data && { total: state.data.total, offset: state.data.offset, shown: state.data.workflows.length })
  }
}

function workflowTable(): { node: Node; update(data: ListWorkflowsResponse): void } {
  const body = h('tbody')
  const node = h(
    'div',
    { class: 'scroll-x' },
    h(
      'table',
      { class: 'table' },
      h(
        'thead',
        null,
        h('tr', null, h('th', null, 'Workflow'), h('th', null, 'Status'), h('th', null, 'Jobs'), h('th', null, 'Repository'), h('th', null, 'Started by'), h('th', null, 'Updated')),
      ),
      body,
    ),
  )
  // Keyed by workflow id. A live update mutates the row in place; it does not
  // rebuild the table, so an open control or a text selection survives.
  return { node, update: (data) => syncKeyed(body, data.workflows, (w) => w.workflowId, createRow, updateRow) }
}

function createRow(): HTMLTableRowElement {
  return h(
    'tr',
    null,
    h('td', null, h('a', { class: 'wf-link' }), h('div', { class: 'meta mono truncate wf-commit' }), h('div', { class: 'meta truncate wf-owner' })),
    h('td', null, statusBadge()),
    h('td', null, jobCounts()),
    h('td', { class: 'truncate wf-repo' }),
    h('td', { class: 'meta truncate wf-actor' }),
    h('td', { class: 'meta wf-updated' }),
  )
}

function updateRow(row: HTMLTableRowElement, workflow: WorkflowRow): void {
  const link = row.querySelector<HTMLAnchorElement>('.wf-link')!
  // An eval that made zero or several workflows is a loose job row. Its event
  // page lists every workflow decision, which is what that row stands for.
  const toEvent = workflow.kind === 'job' && workflow.eventId
  setAttr(link, 'href', href(toEvent ? `/events/${workflow.eventId}` : `/workflows/${workflow.workflowId}`))
  setText(link, workflow.name)
  const commit = row.querySelector<HTMLElement>('.wf-commit')!
  const commitText = [workflow.branch, workflow.commitSha ? workflow.commitSha.slice(0, 12) : ''].filter(Boolean).join(' · ')
  show(commit, commitText)
  setText(commit, commitText)
  setStatus(row.querySelector('.status')!, workflow.status)
  updateJobCounts(row.querySelector('.job-counts')!, workflow)
  setText(row.querySelector('.wf-repo')!, workflow.vcsRepo || '—')
  setText(row.querySelector('.wf-actor')!, actorShort(workflow.actor) || '—')
  const owner = row.querySelector<HTMLElement>('.wf-owner')!
  const group = ownerGroupText(workflow.owner)
  show(owner, group)
  setText(owner, group ? `Owner ${group}` : '')
  setText(row.querySelector('.wf-updated')!, relativeTime(workflow.updatedAt))
}

/** A compact "3/5, 1 failed" summary rather than five separate columns. */
function jobCounts(): HTMLSpanElement {
  return h(
    'span',
    { class: 'row job-counts', style: { gap: 'var(--space-2)' } },
    h('span', { class: 'mono counts-ratio' }),
    h('span', { class: 'status status-info counts-running' }, h('span', { class: 'status-glyph', 'aria-hidden': 'true' }, '●'), h('span', { class: 'counts-text' })),
    h('span', { class: 'status status-danger counts-failed' }, h('span', { class: 'status-glyph', 'aria-hidden': 'true' }, '✕'), h('span', { class: 'counts-text' })),
  )
}

function updateJobCounts(
  el: HTMLElement,
  workflow: { jobCount: number; runningCount: number; completedCount: number; failedCount: number },
): void {
  setText(el.querySelector('.counts-ratio')!, `${workflow.completedCount}/${workflow.jobCount}`)
  const running = el.querySelector<HTMLElement>('.counts-running')!
  show(running, workflow.runningCount > 0)
  setText(running.querySelector('.counts-text')!, `${workflow.runningCount} running`)
  const failed = el.querySelector<HTMLElement>('.counts-failed')!
  show(failed, workflow.failedCount > 0)
  setText(failed.querySelector('.counts-text')!, `${workflow.failedCount} failed`)
}

export const tag = 'rc-workflows-page'
define(tag, WorkflowsPage)
