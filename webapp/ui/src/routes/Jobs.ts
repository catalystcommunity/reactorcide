import type { JobSummary, ListJobsResponse, DescribeFormMetadataResponse } from '~/api/csilapi/types.gen.ts'
import { RcPage } from '~/app/page.ts'
import { ChoiceSelect } from '~/components/ChoiceInput.ts'
import { ResourceView, duration, emptyState, relativeTime } from '~/components/States.ts'
import { setStatus, statusBadge } from '~/components/StatusBadge.ts'
import type { ResourceHandle } from '~/store/index.ts'
import { useFormMetadata, useJobs } from '~/store/resources.ts'
import { h, setAttr, setText, show, syncKeyed } from '~/ui/dom.ts'
import { define } from '~/ui/element.ts'
import { href, setQuery } from '~/ui/router.ts'

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
  #view!: ResourceView<ListJobsResponse>

  #statusFilter(): string {
    return this.route().query.get('status') ?? ''
  }

  protected build(): void {
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
          h('div', { class: 'row' }, this.#status.el),
        ),
        h('div', { class: 'card' }, this.#view.el),
      ),
    )
  }

  protected connected(): void {
    this.#jobs = useJobs(this, () => ({ status: this.#statusFilter() || undefined }))
    this.#metadata = useFormMetadata(this)
  }

  render(): void {
    this.#jobs.sync()
    this.#status.update(this.#metadata.state().data?.jobStatuses ?? [], this.#statusFilter())
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
      h('thead', null, h('tr', null, h('th', null, 'Job'), h('th', null, 'Status'), h('th', null, 'Runtime'), h('th', null, 'Queue'), h('th', null, 'Updated'))),
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
    h('td', null, h('a', { class: 'job-link' }), h('div', { class: 'meta job-node' })),
    h('td', null, statusBadge()),
    h('td', { class: 'meta job-runtime' }),
    h('td', { class: 'meta truncate mono job-queue' }),
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
  setText(row.querySelector('.job-runtime')!, duration(job.startedAt, job.completedAt))
  setText(row.querySelector('.job-queue')!, job.queueName ?? '')
  setText(row.querySelector('.job-updated')!, relativeTime(job.updatedAt))
}

export const tag = 'rc-jobs-page'
define(tag, JobsPage)
