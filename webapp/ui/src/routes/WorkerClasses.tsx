import { For, Show, createResource, createSignal, type JSX } from 'solid-js'
import { A } from '@solidjs/router'
import { api } from '~/api/client.ts'
import { Field } from '~/components/Field.tsx'
import { ActionError, useAction } from '~/components/ManagementPage.tsx'
import type { WorkerPoolSummary } from '~/api/csilapi/types.gen.ts'
import { ManagedOrgPage, OrgHeaderPicker, type ManagedOrg } from './OrgRoles.tsx'

/**
 * Worker classes.
 *
 * A class names a set of pools a job may run on. A PROTECTED class is the point
 * of the feature: only coordinator policy can route a job to one, so untrusted
 * pull-request CI cannot reach the pools that hold deployment credentials.
 *
 * Scoped to the organization chosen in the header (`?org=`). The worker-class
 * operations name their org by NAME (`organization`), not by id: the
 * coordinator resolves it with a lookup by name. The previous version passed
 * the caller's user id here, which matched no organization at all.
 */
export function WorkerClasses(): JSX.Element {
  return (
    <ManagedOrgPage what="worker classes">
      {(managed) => <WorkerClassesPage managed={managed} />}
    </ManagedOrgPage>
  )
}

function WorkerClassesPage(props: { managed: ManagedOrg }): JSX.Element {
  const orgName = () => props.managed.org()?.name ?? ''
  const [classes, { refetch }] = createResource(orgName, (organization) =>
    api.listWorkerClasses({ organization }),
  )
  // Pool names are decoration for the table. The org's own pools are what an
  // org admin may list; a failure here shows ids rather than taking the page down.
  const [pools] = createResource(props.managed.orgId, async (orgId) => {
    try {
      return (await api.listPools({ orgId })).pools
    } catch {
      return [] as WorkerPoolSummary[]
    }
  })
  const [name, setName] = createSignal('')
  const [isProtected, setIsProtected] = createSignal(false)
  const action = useAction()

  const poolName = (poolId: string) =>
    pools()?.find((pool) => pool.poolId === poolId)?.name ?? poolId

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
        <div class="row" style={{ 'align-items': 'center' }}>
          <OrgHeaderPicker managed={props.managed} />
          <A href="/workers" class="btn btn-sm">
            Back to workers
          </A>
        </div>
      </div>

      <div class="card">
        <h3 class="section-title">New class</h3>
        <form
          onSubmit={async (event) => {
            event.preventDefault()
            if (!name().trim()) return
            const ok = await action.run('The worker class could not be saved.', () =>
              api.putWorkerClass({
                organization: orgName(),
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
                                organization: orgName(),
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
