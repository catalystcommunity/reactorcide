import { For, Show, createResource, createSignal, type JSX } from 'solid-js'
import { A } from '@solidjs/router'
import { api } from '~/api/client.ts'
import { useSession } from '~/lib/session.tsx'
import { Field } from '~/components/Field.tsx'
import { ActionError, NotPermitted, useAction } from '~/components/ManagementPage.tsx'

/**
 * Worker classes.
 *
 * A class names a set of pools a job may run on. A PROTECTED class is the point
 * of the feature: only coordinator policy can route a job to one, so untrusted
 * pull-request CI cannot reach the pools that hold deployment credentials.
 */
export function WorkerClasses(): JSX.Element {
  const { session } = useSession()
  return (
    <Show
      when={session()?.capabilities?.manageWorkers}
      fallback={<NotPermitted what="worker classes" />}
    >
      <WorkerClassesPage orgId={session()!.user_id!} />
    </Show>
  )
}

function WorkerClassesPage(props: { orgId: string }): JSX.Element {
  const [classes, { refetch }] = createResource(
    () => props.orgId,
    (organization) => api.listWorkerClasses({ organization }),
  )
  const [pools] = createResource(() => api.listPools({}))
  const [name, setName] = createSignal('')
  const [isProtected, setIsProtected] = createSignal(false)
  const action = useAction()

  const poolName = (poolId: string) =>
    pools()?.pools.find((pool) => pool.poolId === poolId)?.name ?? poolId

  return (
    <div class="page">
      <div class="page-header">
        <div>
          <h1>Worker classes</h1>
          <p class="meta">
            A class names the pools a job may run on. Protected classes are reachable only through
            coordinator policy, which is what keeps untrusted CI away from privileged workers.
          </p>
        </div>
        <A href="/workers" class="btn btn-sm">
          Back to workers
        </A>
      </div>

      <div class="card">
        <h3 class="section-title">New class</h3>
        <form
          onSubmit={async (event) => {
            event.preventDefault()
            if (!name().trim()) return
            const ok = await action.run('The worker class could not be saved.', () =>
              api.putWorkerClass({
                organization: props.orgId,
                workerClass: { name: name().trim(), protected: isProtected(), poolIds: [] },
              }),
            )
            if (ok) {
              setName('')
              setIsProtected(false)
              void refetch()
            }
          }}
        >
          <ActionError error={action.error()} />
          <Field label="Name" required hint="Referenced from a job's worker class.">
            {(ids) => (
              <input
                id={ids.id}
                class="input mono"
                type="text"
                value={name()}
                onInput={(event) => setName(event.currentTarget.value)}
              />
            )}
          </Field>
          <div class="checkbox-row">
            <input
              id="class-protected"
              type="checkbox"
              checked={isProtected()}
              onChange={(event) => setIsProtected(event.currentTarget.checked)}
            />
            <label for="class-protected">
              <strong>Protected</strong>
              <div class="field-hint">
                Only coordinator policy can route a job to this class. Use it for any pool that
                holds credentials untrusted CI must not reach.
              </div>
            </label>
          </div>
          <button type="submit" class="btn btn-primary" disabled={action.busy() || !name().trim()}>
            {action.busy() ? 'Saving…' : 'Create class'}
          </button>
        </form>
      </div>

      <div class="card">
        <h3 class="section-title">Classes</h3>
        <Show
          when={(classes()?.workerClasses.length ?? 0) > 0}
          fallback={<div class="empty">No worker classes defined.</div>}
        >
          <div class="scroll-x">
            <table class="table">
              <thead>
                <tr>
                  <th>Class</th>
                  <th>Protected</th>
                  <th>Pools</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                <For each={classes()?.workerClasses}>
                  {(workerClass) => (
                    <tr>
                      <td class="mono">{workerClass.name}</td>
                      <td>
                        <Show when={workerClass.protected} fallback={<span class="meta">No</span>}>
                          <span class="status status-ok">
                            <span class="status-glyph" aria-hidden="true">
                              ⚿
                            </span>
                            Protected
                          </span>
                        </Show>
                      </td>
                      <td class="meta">
                        {workerClass.poolIds.map(poolName).join(', ') || 'no pools assigned'}
                      </td>
                      <td>
                        <button
                          type="button"
                          class="btn btn-sm btn-danger"
                          disabled={action.busy()}
                          onClick={async () => {
                            if (
                              !confirm(
                                `Delete worker class "${workerClass.name}"? Jobs that name it will fail to route.`,
                              )
                            )
                              return
                            await action.run('The class could not be deleted.', () =>
                              api.deleteWorkerClass({
                                organization: props.orgId,
                                name: workerClass.name,
                              }),
                            )
                            void refetch()
                          }}
                        >
                          Delete
                        </button>
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
