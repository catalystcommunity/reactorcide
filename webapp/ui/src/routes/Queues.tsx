import { For, Show, createResource, createSignal, type JSX } from 'solid-js'
import { A } from '@solidjs/router'
import { api } from '~/api/client.ts'
import { useSession } from '~/lib/session.tsx'
import { ActionError, NotPermitted, useAction } from '~/components/ManagementPage.tsx'

/**
 * Characteristic queues.
 *
 * Global-admin only, and deliberately so: a queue is instance-wide routing, not
 * an org-scoped resource, and deleting one cancels every non-terminal job on it.
 */
export function Queues(): JSX.Element {
  const { session } = useSession()
  return (
    <Show when={session()?.is_global_admin} fallback={<NotPermitted what="queues" />}>
      <QueuesPage />
    </Show>
  )
}

function QueuesPage(): JSX.Element {
  const [queues, { refetch }] = createResource(() => api.listQueues({}))
  const action = useAction()

  return (
    <div class="page">
      <div class="page-header">
        <div>
          <h1>Queues</h1>
          <p class="meta">
            Jobs route to a queue by their characteristics. A queue is created automatically the
            first time a job needs one, so this page is mostly for naming and cleanup.
          </p>
        </div>
        <A href="/workers" class="btn btn-sm">
          Back to workers
        </A>
      </div>

      <div class="card">
        <ActionError error={action.error()} />
        <Show
          when={(queues()?.queues.length ?? 0) > 0}
          fallback={<div class="empty">No queues yet.</div>}
        >
          <div class="scroll-x">
            <table class="table">
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Characteristics</th>
                  <th>Backlog</th>
                  <th>Default</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                <For each={queues()?.queues}>
                  {(queue) => (
                    <tr>
                      <td>
                        <QueueName
                          queueId={queue.queueId}
                          displayName={queue.displayName}
                          onRenamed={() => void refetch()}
                        />
                      </td>
                      <td class="meta mono">
                        {queue.characteristics.map((c) => `${c.key}=${c.value}`).join(', ') || '—'}
                      </td>
                      <td class="numeric">{queue.backlogCount}</td>
                      <td>{queue.isDefault ? 'Yes' : 'No'}</td>
                      <td>
                        <Show when={!queue.isDefault}>
                          <button
                            type="button"
                            class="btn btn-sm btn-danger"
                            disabled={action.busy()}
                            onClick={async () => {
                              if (
                                !confirm(
                                  `Delete queue "${queue.displayName}"? Every job still waiting on it (${queue.backlogCount}) will be cancelled.`,
                                )
                              )
                                return
                              await action.run('The queue could not be deleted.', () =>
                                api.deleteQueue({ queueId: queue.queueId }),
                              )
                              void refetch()
                            }}
                          >
                            Delete
                          </button>
                        </Show>
                      </td>
                    </tr>
                  )}
                </For>
              </tbody>
            </table>
          </div>
        </Show>
      </div>
    </div>
  )
}

/** An inline rename, since naming is the main thing this page is for. */
function QueueName(props: {
  queueId: string
  displayName: string
  onRenamed: () => void
}): JSX.Element {
  const [editing, setEditing] = createSignal(false)
  const [value, setValue] = createSignal(props.displayName)
  const action = useAction()

  return (
    <Show
      when={editing()}
      fallback={
        <button type="button" class="btn btn-sm btn-ghost" onClick={() => setEditing(true)}>
          {props.displayName || <span class="meta">unnamed</span>}
        </button>
      }
    >
      <form
        class="row"
        onSubmit={async (event) => {
          event.preventDefault()
          const ok = await action.run('The queue could not be renamed.', () =>
            api.renameQueue({ queueId: props.queueId, displayName: value() }),
          )
          if (ok) {
            setEditing(false)
            props.onRenamed()
          }
        }}
      >
        <input
          class="input"
          style={{ 'max-width': '14rem' }}
          aria-label="Queue name"
          value={value()}
          onInput={(event) => setValue(event.currentTarget.value)}
        />
        <button type="submit" class="btn btn-sm btn-primary" disabled={action.busy()}>
          Save
        </button>
        <button type="button" class="btn btn-sm btn-ghost" onClick={() => setEditing(false)}>
          Cancel
        </button>
        <ActionError error={action.error()} />
      </form>
    </Show>
  )
}
