import { For, Show, createMemo, type JSX } from 'solid-js'
import { A, useSearchParams } from '@solidjs/router'
import { useJobs, useFormMetadata } from '~/store/resources.ts'
import { StatusBadge } from '~/components/StatusBadge.tsx'
import { ResourceView, EmptyState, relativeTime, duration } from '~/components/States.tsx'
import { ChoiceSelect } from '~/components/ChoiceInput.tsx'

/**
 * The job list.
 *
 * A status change here costs no request: the job_update event carries the new
 * status, so the store patches the row in place (see resources.ts applyEvent).
 * Only a genuinely new job triggers a refetch.
 */
export function Jobs(): JSX.Element {
  const [params, setParams] = useSearchParams<{ status?: string }>()
  const filters = createMemo(() => ({ status: params.status }))
  const { state } = useJobs(filters)
  const metadata = useFormMetadata()

  return (
    <div class="page">
      <div class="page-header">
        <div>
          <h1>Jobs</h1>
          <p class="meta">Every job you can see, updating as it runs.</p>
        </div>
        <div class="row">
          <label class="sr-only" for="job-status">
            Filter by status
          </label>
          <ChoiceSelect
            id="job-status"
            value={params.status ?? ''}
            placeholder="All statuses"
            choices={metadata.state().data?.jobStatuses ?? []}
            onChange={(value) => setParams({ status: value || undefined })}
          />
        </div>
      </div>

      <div class="card">
        <ResourceView
          state={state()}
          isEmpty={(data) => data.jobs.length === 0}
          empty={<EmptyState title="No jobs match this filter." />}
        >
          {(data) => (
            <div class="scroll-x">
              <table class="table">
                <thead>
                  <tr>
                    <th>Job</th>
                    <th>Status</th>
                    <th>Duration</th>
                    <th>Queue</th>
                    <th>Updated</th>
                  </tr>
                </thead>
                <tbody>
                  <For each={data().jobs}>
                    {(job) => (
                      <tr>
                        <td>
                          <A href={`/jobs/${job.jobId}`}>{job.name}</A>
                          <Show when={job.workflowNodeName}>
                            <div class="meta">node: {job.workflowNodeName}</div>
                          </Show>
                        </td>
                        <td>
                          <StatusBadge status={job.status} />
                        </td>
                        <td class="meta">{duration(job.startedAt, job.completedAt)}</td>
                        <td class="meta truncate mono">{job.queueName}</td>
                        <td class="meta">{relativeTime(job.updatedAt)}</td>
                      </tr>
                    )}
                  </For>
                </tbody>
              </table>
            </div>
          )}
        </ResourceView>
      </div>
    </div>
  )
}
