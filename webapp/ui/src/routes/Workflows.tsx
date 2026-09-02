import { For, Show, createMemo, type JSX } from 'solid-js'
import { A, useSearchParams } from '@solidjs/router'
import { useWorkflows } from '~/store/resources.ts'
import { useFormMetadata } from '~/store/resources.ts'
import { StatusBadge } from '~/components/StatusBadge.tsx'
import { ResourceView, EmptyState, relativeTime } from '~/components/States.tsx'
import { ChoiceSelect } from '~/components/ChoiceInput.tsx'

/**
 * The workflow list, updating live.
 *
 * A workflow submitted while this page is open appears without a reload, and a
 * running one recolours as it advances. Before the coordinator emitted workflow
 * events at all, neither was possible: the only workflow-shaped signal on the
 * bus was a job transition.
 */
export function Workflows(): JSX.Element {
  const [params, setParams] = useSearchParams<{ status?: string }>()
  const filters = createMemo(() => ({ status: params.status }))
  const { state } = useWorkflows(filters)
  const metadata = useFormMetadata()

  return (
    <div class="page">
      <div class="page-header">
        <div>
          <h1>Workflows</h1>
          <p class="meta">Every workflow you can see, updating as it runs.</p>
        </div>

        <div class="row">
          <label class="sr-only" for="workflow-status">
            Filter by status
          </label>
          <ChoiceSelect
            id="workflow-status"
            value={params.status ?? ''}
            placeholder="All statuses"
            choices={metadata.state().data?.workflowStatuses ?? []}
            onChange={(value) => setParams({ status: value || undefined })}
          />
        </div>
      </div>

      <div class="card">
        <ResourceView
          state={state()}
          isEmpty={(data) => data.workflows.length === 0}
          empty={
            <EmptyState title="No workflows yet.">
              A workflow appears here the moment it is submitted.
            </EmptyState>
          }
        >
          {(data) => (
            <div class="scroll-x">
              <table class="table">
                <thead>
                  <tr>
                    <th>Workflow</th>
                    <th>Status</th>
                    <th>Jobs</th>
                    <th>Repository</th>
                    <th>Updated</th>
                  </tr>
                </thead>
                <tbody>
                  {/*
                    Keyed by workflow id. A live update mutates the row in
                    place; it does not rebuild the table, so an open control or
                    a text selection survives.
                  */}
                  <For each={data().workflows}>
                    {(workflow) => (
                      <tr>
                        <td>
                          <A href={`/workflows/${workflow.workflowId}`}>{workflow.name}</A>
                          <Show when={workflow.commitSha}>
                            <div class="meta mono truncate">{workflow.commitSha.slice(0, 12)}</div>
                          </Show>
                        </td>
                        <td>
                          <StatusBadge status={workflow.status} />
                        </td>
                        <td>
                          <JobCounts workflow={workflow} />
                        </td>
                        <td class="truncate">{workflow.vcsRepo || '—'}</td>
                        <td class="meta">{relativeTime(workflow.updatedAt)}</td>
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

/** A compact "3/5, 1 failed" summary rather than five separate columns. */
function JobCounts(props: {
  workflow: { jobCount: number; runningCount: number; completedCount: number; failedCount: number }
}): JSX.Element {
  return (
    <span class="row" style={{ gap: 'var(--space-2)' }}>
      <span class="mono">
        {props.workflow.completedCount}/{props.workflow.jobCount}
      </span>
      <Show when={props.workflow.runningCount > 0}>
        <span class="status status-info">
          <span class="status-glyph" aria-hidden="true">
            ●
          </span>
          {props.workflow.runningCount} running
        </span>
      </Show>
      <Show when={props.workflow.failedCount > 0}>
        <span class="status status-danger">
          <span class="status-glyph" aria-hidden="true">
            ✕
          </span>
          {props.workflow.failedCount} failed
        </span>
      </Show>
    </span>
  )
}
