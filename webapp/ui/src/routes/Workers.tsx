import { For, Show, createResource, createSignal, type JSX } from 'solid-js'
import { A } from '@solidjs/router'
import { api } from '~/api/client.ts'
import { useSession } from '~/lib/session.tsx'
import { Field } from '~/components/Field.tsx'
import { relativeTime } from '~/components/States.tsx'
import { ActionError, NotPermitted, OneTimeSecret, useAction } from '~/components/ManagementPage.tsx'
import type { EnrollmentTokenSummary, WorkerPoolSummary } from '~/api/csilapi/types.gen.ts'

/**
 * Worker pools, their enrollment tokens, and the workers that enrolled.
 *
 * Gated on the ManageWorkers capability. The gate decides what is DRAWN; the
 * coordinator decides what is allowed, and re-checks every operation here.
 */
export function Workers(): JSX.Element {
  const { session } = useSession()

  return (
    <Show when={session()?.capabilities?.manageWorkers} fallback={<NotPermitted what="workers" />}>
      <WorkersPage />
    </Show>
  )
}

function WorkersPage(): JSX.Element {
  const [pools, { refetch: refetchPools }] = createResource(() => api.listPools({}))
  const [workers, { refetch: refetchWorkers }] = createResource(() => api.listWorkers({}))
  const [selectedPool, setSelectedPool] = createSignal<string>()

  return (
    <div class="page">
      <div class="page-header">
        <div>
          <h1>Workers</h1>
          <p class="meta">Pools, their enrollment tokens, and the workers that joined them.</p>
        </div>
        <div class="row">
          <A href="/workers/classes" class="btn btn-sm">
            Worker classes
          </A>
          <Show when={useSession().session()?.is_global_admin}>
            <A href="/workers/queues" class="btn btn-sm">
              Queues
            </A>
          </Show>
        </div>
      </div>

      <CreatePoolCard onCreated={() => void refetchPools()} />

      <div class="card">
        <h3 class="section-title">Pools</h3>
        <Show
          when={(pools()?.pools.length ?? 0) > 0}
          fallback={<div class="empty">No pools yet. Create one above, then enrol a worker against it.</div>}
        >
          <div class="scroll-x">
            <table class="table">
              <thead>
                <tr>
                  <th>Pool</th>
                  <th>Description</th>
                  <th>Created</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                <For each={pools()?.pools}>
                  {(pool) => (
                    <PoolRow
                      pool={pool}
                      expanded={selectedPool() === pool.poolId}
                      onToggle={() =>
                        setSelectedPool(selectedPool() === pool.poolId ? undefined : pool.poolId)
                      }
                      onChanged={() => void refetchPools()}
                    />
                  )}
                </For>
              </tbody>
            </table>
          </div>
        </Show>
      </div>

      <div class="card">
        <h3 class="section-title">Enrolled workers</h3>
        <Show
          when={(workers()?.workers.length ?? 0) > 0}
          fallback={<div class="empty">No workers have enrolled yet.</div>}
        >
          <div class="scroll-x">
            <table class="table">
              <thead>
                <tr>
                  <th>Worker</th>
                  <th>Status</th>
                  <th>Platform</th>
                  <th>Active leases</th>
                  <th>Last seen</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                <For each={workers()?.workers}>
                  {(worker) => <WorkerRow worker={worker} onChanged={() => void refetchWorkers()} />}
                </For>
              </tbody>
            </table>
          </div>
        </Show>
      </div>
    </div>
  )
}

function CreatePoolCard(props: { onCreated: () => void }): JSX.Element {
  const [name, setName] = createSignal('')
  const [description, setDescription] = createSignal('')
  const action = useAction()

  const submit = async (event: Event) => {
    event.preventDefault()
    if (!name().trim()) return
    const ok = await action.run('The pool could not be created.', () =>
      api.createPool({ name: name().trim(), description: description().trim() || undefined }),
    )
    if (ok) {
      setName('')
      setDescription('')
      props.onCreated()
    }
  }

  return (
    <div class="card">
      <h3 class="section-title">New pool</h3>
      <form onSubmit={submit}>
        <ActionError error={action.error()} />
        <div class="form-row">
          <Field label="Name" required hint="How this pool is identified when a worker enrols.">
            {(ids) => (
              <input
                id={ids.id}
                class="input"
                type="text"
                value={name()}
                onInput={(event) => setName(event.currentTarget.value)}
              />
            )}
          </Field>
          <Field label="Description">
            {(ids) => (
              <input
                id={ids.id}
                class="input"
                type="text"
                value={description()}
                onInput={(event) => setDescription(event.currentTarget.value)}
              />
            )}
          </Field>
        </div>
        <button type="submit" class="btn btn-primary" disabled={action.busy() || !name().trim()}>
          {action.busy() ? 'Creating…' : 'Create pool'}
        </button>
      </form>
    </div>
  )
}

function PoolRow(props: {
  pool: WorkerPoolSummary
  expanded: boolean
  onToggle: () => void
  onChanged: () => void
}): JSX.Element {
  return (
    <>
      <tr>
        <td>
          <button type="button" class="btn btn-sm btn-ghost" onClick={props.onToggle}>
            <span aria-hidden="true">{props.expanded ? '▾' : '▸'}</span> {props.pool.name}
          </button>
        </td>
        <td class="meta">{props.pool.description || '—'}</td>
        <td class="meta">{relativeTime(props.pool.createdAt)}</td>
        <td>
          <DeletePoolButton poolId={props.pool.poolId} name={props.pool.name} onDeleted={props.onChanged} />
        </td>
      </tr>
      <Show when={props.expanded}>
        <tr>
          <td colspan="4">
            <EnrollmentTokens poolId={props.pool.poolId} />
          </td>
        </tr>
      </Show>
    </>
  )
}

function DeletePoolButton(props: { poolId: string; name: string; onDeleted: () => void }): JSX.Element {
  const action = useAction()
  return (
    <>
      <button
        type="button"
        class="btn btn-sm btn-danger"
        disabled={action.busy()}
        onClick={async () => {
          if (!confirm(`Delete pool "${props.name}"? Workers enrolled through it will no longer be able to authenticate.`)) return
          const ok = await action.run('The pool could not be deleted.', () =>
            api.deletePool({ poolId: props.poolId }),
          )
          if (ok) props.onDeleted()
        }}
      >
        Delete
      </button>
      <ActionError error={action.error()} />
    </>
  )
}

/**
 * A pool's enrollment tokens.
 *
 * create-enrollment-token is the ONLY operation that ever returns the token
 * value; the store keeps a hash and no later call can retrieve it. That is why
 * the result goes through OneTimeSecret rather than into a table cell.
 */
function EnrollmentTokens(props: { poolId: string }): JSX.Element {
  const [tokens, { refetch }] = createResource(
    () => props.poolId,
    (poolId) => api.listEnrollmentTokens({ poolId }),
  )
  const [minted, setMinted] = createSignal<string>()
  const [name, setName] = createSignal('')
  const action = useAction()

  return (
    <div class="stack">
      <ActionError error={action.error()} />
      <Show when={minted()}>
        <OneTimeSecret
          label="Enrollment token"
          value={minted()!}
          onDismiss={() => setMinted(undefined)}
        />
      </Show>

      <div class="row">
        <input
          class="input"
          style={{ 'max-width': '18rem' }}
          type="text"
          placeholder="Token name (optional)"
          aria-label="Enrollment token name"
          value={name()}
          onInput={(event) => setName(event.currentTarget.value)}
        />
        <button
          type="button"
          class="btn btn-sm btn-primary"
          disabled={action.busy()}
          onClick={async () => {
            const ok = await action.run('The token could not be created.', async () => {
              const response = await api.createEnrollmentToken({
                poolId: props.poolId,
                name: name().trim() || undefined,
              })
              setMinted(response.token)
              setName('')
            })
            if (ok) void refetch()
          }}
        >
          Create enrollment token
        </button>
      </div>

      <Show
        when={(tokens()?.tokens.length ?? 0) > 0}
        fallback={<p class="meta">No enrollment tokens for this pool.</p>}
      >
        <table class="table">
          <thead>
            <tr>
              <th>Name</th>
              <th>Active</th>
              <th>Last used</th>
              <th />
            </tr>
          </thead>
          <tbody>
            <For each={tokens()?.tokens}>
              {(token: EnrollmentTokenSummary) => (
                <tr>
                  <td>{token.name || <span class="meta">unnamed</span>}</td>
                  <td>{token.isActive ? 'Yes' : 'No'}</td>
                  <td class="meta">{token.lastUsedAt ? relativeTime(token.lastUsedAt) : 'never'}</td>
                  <td>
                    <Show when={token.isActive}>
                      <button
                        type="button"
                        class="btn btn-sm"
                        onClick={async () => {
                          await action.run('The token could not be deactivated.', () =>
                            api.deactivateEnrollmentToken({ tokenId: token.tokenId }),
                          )
                          void refetch()
                        }}
                      >
                        Deactivate
                      </button>
                    </Show>
                  </td>
                </tr>
              )}
            </For>
          </tbody>
        </table>
      </Show>
    </div>
  )
}

function WorkerRow(props: {
  worker: {
    workerId: string
    hostname?: string
    workerKey: string
    os: string
    arch: string
    status: string
    activeLeaseCount: number
    lastSeenAt?: string
  }
  onChanged: () => void
}): JSX.Element {
  const action = useAction()

  return (
    <tr>
      <td>
        <div>{props.worker.hostname || props.worker.workerKey}</div>
        <div class="meta mono truncate">{props.worker.workerId}</div>
      </td>
      <td>{props.worker.status}</td>
      <td class="meta">
        {props.worker.os}/{props.worker.arch}
      </td>
      <td class="numeric">{props.worker.activeLeaseCount}</td>
      <td class="meta">{props.worker.lastSeenAt ? relativeTime(props.worker.lastSeenAt) : '—'}</td>
      <td>
        <div class="row">
          {/*
            Drain lets a worker finish what it is running and take nothing new.
            Disabling it is the harder stop, so the two are offered separately
            rather than as one ambiguous "stop" button.
          */}
          <button
            type="button"
            class="btn btn-sm"
            disabled={action.busy()}
            onClick={async () => {
              await action.run('The worker could not be drained.', () =>
                api.drainWorker({ workerId: props.worker.workerId }),
              )
              props.onChanged()
            }}
          >
            Drain
          </button>
          <button
            type="button"
            class="btn btn-sm"
            disabled={action.busy()}
            onClick={async () => {
              const next = props.worker.status === 'disabled' ? 'active' : 'disabled'
              await action.run('The worker status could not be changed.', () =>
                api.setWorkerStatus({ workerId: props.worker.workerId, status: next }),
              )
              props.onChanged()
            }}
          >
            {props.worker.status === 'disabled' ? 'Enable' : 'Disable'}
          </button>
        </div>
        <ActionError error={action.error()} />
      </td>
    </tr>
  )
}
