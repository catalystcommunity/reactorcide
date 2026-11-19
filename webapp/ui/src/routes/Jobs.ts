import type { JobSummary, ListJobsResponse, DescribeFormMetadataResponse } from '~/api/csilapi/types.gen.ts'
import { RcPage } from '~/app/page.ts'
import { ChoiceSelect } from '~/components/ChoiceInput.ts'
import { ResourceView, duration, emptyState, relativeTime } from '~/components/States.ts'
import { failureClassBadge, setFailureClass, setStatus, statusBadge, FAILURE_CLASS_CHOICES } from '~/components/StatusBadge.ts'
import type { ResourceHandle } from '~/store/index.ts'
import { useFormMetadata, useJobs } from '~/store/resources.ts'
import { h, setAttr, setText, show, syncKeyed } from '~/ui/dom.ts'
import { define } from '~/ui/element.ts'
import { href, setQuery } from '~/ui/router.ts'
import { actorShort, ownerGroupText } from '~/components/Labels.ts'

/**
 * The job list.
 *
 * A status change here costs no request: the job_update event carries the new
 * status, so the store patches the row in place (see resources.ts applyEvent).
 * Only a genuinely new job triggers a refetch.
 */
export class JobsPage extends RcPage {
  #jobs!: ResourceHandle<ListJobsResponse>
  #metadata!: ResourceHandle<DescribeFormMetadataResponse>
  #status!: ChoiceSelect
  #failure!: ChoiceSelect
  #view!: ResourceView<ListJobsResponse>

  #statusFilter(): string {
    return this.route().query.get('status') ?? ''
  }

  #failureFilter(): string {
    return this.route().query.get('failure_class') ?? ''
  }

  protected build(): void {
    this.#failure = new ChoiceSelect({
      ariaLabel: 'Filter by failure class',
      placeholder: 'All failure classes',
      choices: FAILURE_CLASS_CHOICES,
      value: this.#failureFilter(),
      onChange: (value) => setQuery({ failure_class: value || undefined }),
    })
    this.#status = new ChoiceSelect({
      ariaLabel: 'Filter by status',
      placeholder: 'All statuses',
      value: this.#statusFilter(),
      onChange: (value) => setQuery({ status: value || undefined }),
    })
    this.#view = new ResourceView<ListJobsResponse>({
      isEmpty: (data) => data.jobs.length === 0,
      empty: emptyState('No jobs match this filter.'),
      content: () => jobTable(),
    })
    this.append(
      h(
        'div',
        { class: 'page' },
        h(
          'div',
          { class: 'page-header' },
          h('div', null, h('h1', null, 'Jobs'), h('p', { class: 'meta' }, 'Every job you can see, updating as it runs.')),
          h('div', { class: 'row' }, this.#status.el, this.#failure.el),
        ),
        h('div', { class: 'card' }, this.#view.el),
      ),
    )
  }

  protected connected(): void {
    this.#jobs = useJobs(this, () => ({ status: this.#statusFilter() || undefined, failureClass: this.#failureFilter() || undefined }))
    this.#metadata = useFormMetadata(this)
  }

  render(): void {
    this.#jobs.sync()
    this.#status.update(this.#metadata.state().data?.jobStatuses ?? [], this.#statusFilter())
    this.#failure.update(FAILURE_CLASS_CHOICES, this.#failureFilter())
    this.#view.update(this.#jobs.state())
  }
}

function jobTable(): { node: Node; update(data: ListJobsResponse): void } {
  const body = h('tbody')
  const node = h(
    'div',
    { class: 'scroll-x' },
    h(
      'table',
      { class: 'table' },
      h('thead', null, h('tr', null, h('th', null, 'Job'), h('th', null, 'Status'), h('th', null, 'Runtime'), h('th', null, 'Queue'), h('th', null, 'Started by'), h('th', null, 'Updated'))),
      body,
    ),
  )
  return {
    node,
    update: (data) => syncKeyed(body, data.jobs, (job) => job.jobId, createJobRow, updateJobRow),
  }
}

function createJobRow(): HTMLTableRowElement {
  return h(
    'tr',
    null,
    h('td', null, h('a', { class: 'job-link' }), h('div', { class: 'meta job-node' }), h('div', { class: 'meta truncate job-owner' })),
    h('td', null, statusBadge(), ' ', failureClassBadge()),
    h('td', { class: 'meta job-runtime' }),
    h('td', { class: 'meta truncate mono job-queue' }),
    h('td', { class: 'meta truncate job-actor' }),
    h('td', { class: 'meta job-updated' }),
  )
}

function updateJobRow(row: HTMLTableRowElement, job: JobSummary): void {
  const link = row.querySelector<HTMLAnchorElement>('.job-link')!
  setAttr(link, 'href', href(`/jobs/${job.jobId}`))
  setText(link, job.name)
  const node = row.querySelector<HTMLElement>('.job-node')!
  show(node, job.workflowNodeName)
  setText(node, job.workflowNodeName ? `node: ${job.workflowNodeName}` : '')
  setStatus(row.querySelector('.status')!, job.status)
  setFailureClass(row.querySelector('.failure-class')!, job.failureClass, job.failureOwner)
  setText(row.querySelector('.job-runtime')!, duration(job.startedAt, job.completedAt))
  setText(row.querySelector('.job-queue')!, job.queueName ?? '')
  setText(row.querySelector('.job-actor')!, actorShort(job.actor) || '—')
  const owner = row.querySelector<HTMLElement>('.job-owner')!
  const group = ownerGroupText(job.owner)
  show(owner, group)
  setText(owner, group ? `Owner ${group}` : '')
  setText(row.querySelector('.job-updated')!, relativeTime(job.updatedAt))
}

export const tag = 'rc-jobs-page'
define(tag, JobsPage)
