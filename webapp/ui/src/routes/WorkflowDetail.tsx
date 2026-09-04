import { For, Show, createMemo, createSignal, type JSX } from 'solid-js'
import { A, useParams } from '@solidjs/router'
import { useWorkflow } from '~/store/resources.ts'
import { StatusBadge, isTerminal } from '~/components/StatusBadge.tsx'
import { ResourceView, relativeTime, duration } from '~/components/States.tsx'
import { DagView } from '~/components/DagView.tsx'
import { api } from '~/api/client.ts'
import { useSession } from '~/lib/session.tsx'

type Tab = 'jobs' | 'graph'

/**
 * One workflow: its jobs, and optionally its graph.
 *
 * The graph is a tab rather than the default because a list is what most people
 * want most of the time, and a wide DAG pushes everything else off screen.
 * Both views read the same live store, so both animate as the workflow runs.
 */
export function WorkflowDetail(): JSX.Element {
  const params = useParams<{ id: string }>()
  const { state, refresh } = useWorkflow(() => params.id)
  const [tab, setTab] = createSignal<Tab>('jobs')
  const { session } = useSession()

  const workflow = createMemo(() => state().data?.workflow)
  const canCancel = () =>
    Boolean(session()?.capabilities?.cancelJob) && !isTerminal(workflow()?.status ?? '')

  return (
    <div class="page">
      <div style={{ 'margin-bottom': 'var(--space-4)' }}>
        <A href="/" class="meta">
          ← All workflows
        </A>
      </div>

      <ResourceView state={state()}>
        {(data) => (
          <>
            <div class="page-header">
              <div>
                <div class="row">
                  <h1>{data().workflow.name}</h1>
                  <StatusBadge status={data().workflow.status} />
                </div>
                <p class="meta">
                  {data().workflow.vcsRepo || 'No repository'} ·{' '}
                  {relativeTime(data().workflow.updatedAt)}
                </p>
              </div>

              <div class="row">
                <Show when={canCancel()}>
                  <button
                    type="button"
                    class="btn btn-danger btn-sm"
                    onClick={async () => {
                      await api.cancelWorkflow({ workflowInstanceId: params.id })
                      await refresh()
                    }}
                  >
                    Cancel workflow
                  </button>
                </Show>
                <Show when={session()?.capabilities?.retryJob && isTerminal(data().workflow.status)}>
                  <button
                    type="button"
                    class="btn btn-sm"
                    onClick={async () => {
                      await api.retryUnsuccessfulJobs({ workflowInstanceId: params.id })
                      await refresh()
                    }}
                  >
                    Retry unsuccessful
                  </button>
                </Show>
              </div>
            </div>

            <div class="card">
              <dl class="grid-dl">
                <div>
                  <dt>Workflow ID</dt>
                  <dd class="mono truncate">{data().workflow.workflowId}</dd>
                </div>
                <div>
                  <dt>Commit</dt>
                  <dd class="mono">{data().workflow.commitSha?.slice(0, 12) || '—'}</dd>
                </div>
                <div>
                  <dt>Jobs</dt>
                  <dd>
                    {data().workflow.completedCount} of {data().workflow.jobCount} complete
                    <Show when={data().workflow.failedCount > 0}>
                      , {data().workflow.failedCount} failed
                    </Show>
                  </dd>
                </div>
                <div>
                  <dt>Duration</dt>
                  <dd>{duration(data().workflow.createdAt, data().workflow.completedAt)}</dd>
                </div>
                <Show when={data().workflow.ciOrigin}>
                  <div>
                    <dt>CI origin</dt>
                    <dd class="mono">{data().workflow.ciOrigin}</dd>
                  </div>
                </Show>
                <Show when={data().workflow.executionProfile}>
                  <div>
                    <dt>Execution profile</dt>
                    <dd class="mono">{data().workflow.executionProfile}</dd>
                  </div>
                </Show>
              </dl>
              <Show when={data().workflow.decisionSummary}>
                <p class="meta" style={{ 'margin-top': 'var(--space-3)' }}>
                  {data().workflow.decisionSummary}
                </p>
              </Show>
            </div>

            <div class="card">
              <div class="tabs" role="tablist" aria-label="Workflow views">
                <button
                  type="button"
                  class="tab"
                  role="tab"
                  aria-selected={tab() === 'jobs'}
                  onClick={() => setTab('jobs')}
                >
                  Jobs
                </button>
                <button
                  type="button"
                  class="tab"
                  role="tab"
                  aria-selected={tab() === 'graph'}
                  onClick={() => setTab('graph')}
                >
                  Graph
                </button>
              </div>

              <Show when={tab() === 'jobs'}>
                <NodeTable nodes={data().nodes} jobs={data().jobs} />
              </Show>
              <Show when={tab() === 'graph'}>
                <DagView nodes={data().nodes} />
              </Show>
            </div>
          </>
        )}
      </ResourceView>
    </div>
  )
}

/**
 * Nodes with their jobs.
 *
 * Keyed on the node rather than the job: a node exists before its job is
 * submitted, and showing it as pending is more informative than hiding it until
 * something starts.
 */
function NodeTable(props: {
  nodes: { nodeId: string; name: string; displayName: string; status: string; jobId?: string; dependsOn: string[]; decisionReason: string }[]
  jobs: { jobId: string; status: string; startedAt?: string; completedAt?: string; exitCode?: number }[]
}): JSX.Element {
  const jobById = createMemo(() => new Map(props.jobs.map((job) => [job.jobId, job])))

  return (
    <Show when={props.nodes.length > 0} fallback={<div class="empty">No nodes yet.</div>}>
      <div class="scroll-x">
        <table class="table">
          <thead>
            <tr>
              <th>Node</th>
              <th>Status</th>
              <th>Depends on</th>
              <th>Duration</th>
              <th>Exit</th>
            </tr>
          </thead>
          <tbody>
            <For each={props.nodes}>
              {(node) => {
                const job = () => (node.jobId ? jobById().get(node.jobId) : undefined)
                return (
                  <tr>
                    <td>
                      <Show
                        when={node.jobId}
                        fallback={<span>{node.displayName || node.name}</span>}
                      >
                        <A href={`/jobs/${node.jobId}`}>{node.displayName || node.name}</A>
                      </Show>
                      <Show when={node.decisionReason}>
                        <div class="meta">{node.decisionReason}</div>
                      </Show>
                    </td>
                    <td>
                      <StatusBadge status={node.status} />
                    </td>
                    <td class="meta">{node.dependsOn?.join(', ') || '—'}</td>
                    <td class="meta">{duration(job()?.startedAt, job()?.completedAt)}</td>
                    <td class="numeric mono">
                      {job()?.exitCode !== undefined ? job()!.exitCode : '—'}
                    </td>
                  </tr>
                )
              }}
            </For>
          </tbody>
        </table>
      </div>
    </Show>
  )
}
