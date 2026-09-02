import { For, Show, createMemo, createSignal, onCleanup, type JSX } from 'solid-js'
import { A, useParams } from '@solidjs/router'
import { useJob } from '~/store/resources.ts'
import { api } from '~/api/client.ts'
import { StatusBadge, isTerminal } from '~/components/StatusBadge.tsx'
import { ResourceView, relativeTime, duration } from '~/components/States.tsx'
import { MetricsPanel } from '~/components/MetricsPanel.tsx'
import { appendBounded, BUFFER_CAPS } from '~/store/index.ts'
import { eventSocket, topics } from '~/store/events.ts'
import { useSession } from '~/lib/session.tsx'
import './job.css'

type Tab = 'logs' | 'metrics'

export function JobDetail(): JSX.Element {
  const params = useParams<{ id: string }>()
  const { state, refresh } = useJob(() => params.id)
  const [tab, setTab] = createSignal<Tab>('logs')
  const { session } = useSession()

  const job = createMemo(() => state().data?.job)
  const live = createMemo(() => !isTerminal(job()?.status ?? ''))

  return (
    <div class="page">
      <div style={{ 'margin-bottom': 'var(--space-4)' }}>
        <Show when={job()?.workflowId} fallback={<A href="/jobs" class="meta">← All jobs</A>}>
          <A href={`/workflows/${job()!.workflowId}`} class="meta">
            ← Back to workflow
          </A>
        </Show>
      </div>

      <ResourceView state={state()}>
        {(data) => (
          <>
            <div class="page-header">
              <div>
                <div class="row">
                  <h1>{data().job.name}</h1>
                  <StatusBadge status={data().job.status} />
                  <Show when={data().job.exitCode !== undefined}>
                    <span class="meta mono">exit {data().job.exitCode}</span>
                  </Show>
                </div>
                <p class="meta">{data().job.description || 'No description'}</p>
              </div>

              <div class="row">
                <Show when={session()?.capabilities?.cancelJob && live()}>
                  <button
                    type="button"
                    class="btn btn-sm"
                    onClick={async () => {
                      await api.cancelJob({ jobId: params.id })
                      await refresh()
                    }}
                  >
                    Cancel
                  </button>
                </Show>
                <Show when={session()?.capabilities?.killJob && live()}>
                  <button
                    type="button"
                    class="btn btn-sm btn-danger"
                    onClick={async () => {
                      // Kill skips cleanup hooks, so it gets a confirmation
                      // that says exactly that rather than a generic one.
                      if (!confirm('Kill this job immediately? Cleanup hooks will not run.')) return
                      await api.killJob({ jobId: params.id })
                      await refresh()
                    }}
                  >
                    Kill
                  </button>
                </Show>
                <Show when={session()?.capabilities?.retryJob && isTerminal(data().job.status)}>
                  <button
                    type="button"
                    class="btn btn-sm"
                    onClick={async () => {
                      await api.retryJob({ jobId: params.id })
                      await refresh()
                    }}
                  >
                    Retry
                  </button>
                </Show>
              </div>
            </div>

            <div class="card">
              <dl class="grid-dl">
                <div>
                  <dt>Job ID</dt>
                  <dd class="mono truncate">{data().job.jobId}</dd>
                </div>
                <div>
                  <dt>Duration</dt>
                  <dd>{duration(data().job.startedAt, data().job.completedAt)}</dd>
                </div>
                <div>
                  <dt>Updated</dt>
                  <dd>{relativeTime(data().job.updatedAt)}</dd>
                </div>
                <div>
                  <dt>Queue</dt>
                  <dd class="mono truncate">{data().job.queueName}</dd>
                </div>
                <Show when={data().job.sourceRef}>
                  <div>
                    <dt>Source ref</dt>
                    <dd class="mono truncate">{data().job.sourceRef}</dd>
                  </div>
                </Show>
                <Show when={data().job.runnerImage}>
                  <div>
                    <dt>Runner image</dt>
                    <dd class="mono truncate">{data().job.runnerImage}</dd>
                  </div>
                </Show>
                <Show when={data().job.workerClass}>
                  <div>
                    <dt>Worker class</dt>
                    <dd class="mono">{data().job.workerClass}</dd>
                  </div>
                </Show>
              </dl>
            </div>

            <Show when={data().job.lastError}>
              <div class="card">
                <h3 class="section-title">Failure reason</h3>
                <pre class="log-viewer">{data().job.lastError}</pre>
              </div>
            </Show>

          </>
        )}
      </ResourceView>

      {/*
        Deliberately OUTSIDE ResourceView.

        ResourceView calls its children function inside a <Show>, so the whole
        subtree is re-created every time the resource refreshes. These two
        panels own long-lived state -- a bounded log buffer, a scroll position,
        the selected metric family -- and re-creating them on every job_update
        threw the logs away and jumped the viewport mid-read. They need only the
        job id and whether it is still running, both of which are available
        here.

        The keyed <Show> is what still rebuilds them when the JOB changes: the
        router reuses this component when only the :id param changes, so
        without it a second job would inherit the first job's log buffer.
      */}
      <Show when={params.id} keyed>
        {(jobId) => (
          <div class="card">
            <div class="tabs" role="tablist" aria-label="Job detail views">
              <button
                type="button"
                class="tab"
                role="tab"
                aria-selected={tab() === 'logs'}
                onClick={() => setTab('logs')}
              >
                Logs
              </button>
              <button
                type="button"
                class="tab"
                role="tab"
                aria-selected={tab() === 'metrics'}
                onClick={() => setTab('metrics')}
              >
                Resources
              </button>
            </div>

            <Show when={tab() === 'logs'}>
              <LogViewer jobId={jobId} live={live()} />
            </Show>
            <Show when={tab() === 'metrics'}>
              <MetricsPanel jobId={jobId} live={live()} />
            </Show>
          </div>
        )}
      </Show>
    </div>
  )
}

/**
 * The log viewer.
 *
 * Two things here matter beyond rendering lines.
 *
 * The buffer is BOUNDED. A chatty job watched for hours would otherwise grow
 * until the tab died; appendBounded keeps the newest BUFFER_CAPS.logLines and
 * drops the rest.
 *
 * Scrolling FOLLOWS only when the reader is already at the bottom. Yanking the
 * viewport down while somebody is reading an error further up is the single
 * most annoying thing a log view can do.
 */
function LogViewer(props: { jobId: string; live: boolean }): JSX.Element {
  const [lines, setLines] = createSignal<{ timestamp: string; stream: string; message: string }[]>([])
  const [cursor, setCursor] = createSignal<string | undefined>()
  const [error, setError] = createSignal<string>()
  let viewport: HTMLDivElement | undefined
  let inFlight = false

  const atBottom = () =>
    !viewport || viewport.scrollHeight - viewport.scrollTop - viewport.clientHeight < 40

  const poll = async () => {
    if (inFlight) return
    inFlight = true
    try {
      const response = await api.getJobLogs({
        jobId: props.jobId,
        stream: 'combined',
        cursor: cursor(),
        maxEntries: 1000,
      })
      const wasAtBottom = atBottom()
      if (response.entries.length > 0) {
        setLines((existing) => appendBounded(existing, response.entries, BUFFER_CAPS.logLines))
      }
      if (response.nextCursor) setCursor(response.nextCursor)
      setError(undefined)
      if (wasAtBottom && viewport) {
        queueMicrotask(() => {
          if (viewport) viewport.scrollTop = viewport.scrollHeight
        })
      }
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Logs are unavailable.')
    } finally {
      inFlight = false
    }
  }

  void poll()

  // A log_available event triggers a fetch. Falling back to a timer as well
  // means a missed notification costs a few seconds of staleness rather than
  // leaving the view wrong until a reload.
  const releaseTopic = eventSocket.acquireTopic(topics.job(props.jobId))
  const removeListener = eventSocket.addListener((event) => {
    if (event.job_id === props.jobId && event.type === 'log_available') void poll()
  })
  const timer = setInterval(() => {
    if (props.live && document.visibilityState === 'visible') void poll()
  }, 4_000)

  onCleanup(() => {
    releaseTopic()
    removeListener()
    clearInterval(timer)
  })

  return (
    <>
      <Show when={error()}>
        <div class="alert alert-error" role="alert">
          {error()}
        </div>
      </Show>
      <div class="log-viewer" ref={viewport} tabindex="0" role="log" aria-label="Job logs">
        <Show when={lines().length > 0} fallback={<div class="meta">No logs yet.</div>}>
          <For each={lines()}>
            {(line) => (
              <div class={`log-line ${line.stream === 'stderr' ? 'log-stderr' : ''}`}>
                <span class="log-timestamp">{line.timestamp}</span>
                {line.message}
              </div>
            )}
          </For>
        </Show>
      </div>
    </>
  )
}
