import { For, Show, createResource, createSignal, type JSX } from 'solid-js'
import { api } from '~/api/client.ts'
import { useSession } from '~/lib/session.tsx'
import { Field } from '~/components/Field.tsx'
import { ChoiceSelect } from '~/components/ChoiceInput.tsx'
import { ActionError, NotPermitted, useAction } from '~/components/ManagementPage.tsx'

/**
 * Secrets and the grants that let jobs read them.
 *
 * THIS PAGE IS WRITE-ONLY BY DESIGN. It lists secret PATHS and KEY NAMES and
 * never a value: the UI service has no operation that returns one, so there is
 * nothing here to accidentally render, log, or screenshot. A value can be set
 * and replaced; it cannot be read back.
 *
 * A grant is what makes a secret reachable from a job. Without one, a secret
 * exists and no job can resolve it.
 */
export function OrgSecrets(): JSX.Element {
  const { session } = useSession()
  return (
    <Show when={session()?.capabilities?.manageSecrets} fallback={<NotPermitted what="secrets" />}>
      <SecretsPage orgId={session()!.user_id!} />
    </Show>
  )
}

function SecretsPage(props: { orgId: string }): JSX.Element {
  const [paths, { refetch: refetchPaths }] = createResource(
    () => props.orgId,
    (orgId) => api.listSecretPaths({ orgId }),
  )
  const [grants, { refetch: refetchGrants }] = createResource(
    () => props.orgId,
    (orgId) => api.listSecretGrants({ orgId }),
  )

  return (
    <div class="page">
      <div class="page-header">
        <div>
          <h1>Secrets</h1>
          <p class="meta">
            Values are never shown here and cannot be read back through this interface — only set,
            replaced, and deleted. Jobs reach a secret through a grant.
          </p>
        </div>
      </div>

      <SetSecretCard orgId={props.orgId} onSaved={() => void refetchPaths()} />

      <div class="card">
        <h3 class="section-title">Stored secrets</h3>
        <p class="meta" style={{ 'margin-bottom': 'var(--space-3)' }}>
          Paths and key names only.
        </p>
        <Show
          when={(paths()?.paths.length ?? 0) > 0}
          fallback={<div class="empty">No secrets stored for this organization.</div>}
        >
          <div class="scroll-x">
            <table class="table">
              <thead>
                <tr>
                  <th>Path</th>
                  <th>Keys</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                <For each={paths()?.paths}>
                  {(entry) => (
                    <tr>
                      <td class="mono">{entry.path}</td>
                      <td>
                        <div class="row">
                          <For each={entry.keys}>
                            {(key) => <code class="secret-key">{key}</code>}
                          </For>
                        </div>
                      </td>
                      <td>
                        <DeleteSecretControl
                          orgId={props.orgId}
                          path={entry.path}
                          keys={entry.keys}
                          onDeleted={() => void refetchPaths()}
                        />
                      </td>
                    </tr>
                  )}
                </For>
              </tbody>
            </table>
          </div>
        </Show>
      </div>

      <CreateGrantCard orgId={props.orgId} onCreated={() => void refetchGrants()} />

      <div class="card">
        <h3 class="section-title">Grants</h3>
        <Show
          when={(grants()?.grants.length ?? 0) > 0}
          fallback={
            <div class="empty">
              No grants. Without one, a job cannot resolve any secret in this organization.
            </div>
          }
        >
          <div class="scroll-x">
            <table class="table">
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Secret paths</th>
                  <th>Job names</th>
                  <th>Project</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                <For each={grants()?.grants}>
                  {(grant) => (
                    <GrantRow grant={grant} onDeleted={() => void refetchGrants()} />
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

/**
 * Setting a secret value.
 *
 * The value field is a password input, is never echoed back, and is cleared the
 * moment the write succeeds OR fails. Leaving a secret sitting in a form field
 * after an error is how one ends up in a screenshot or a password manager.
 */
function SetSecretCard(props: { orgId: string; onSaved: () => void }): JSX.Element {
  const [path, setPath] = createSignal('')
  const [key, setKey] = createSignal('')
  const [value, setValue] = createSignal('')
  const [saved, setSaved] = createSignal(false)
  const action = useAction()

  return (
    <div class="card">
      <h3 class="section-title">Set a secret</h3>
      <form
        onSubmit={async (event) => {
          event.preventDefault()
          if (!path().trim() || !key().trim() || !value()) return
          const ok = await action.run('The secret could not be stored.', () =>
            api.setSecret({
              orgId: props.orgId,
              path: path().trim(),
              key: key().trim(),
              value: value(),
            }),
          )
          // Cleared on every outcome, not only success.
          setValue('')
          setSaved(ok)
          if (ok) props.onSaved()
        }}
      >
        <ActionError error={action.error()} />
        <Show when={saved()}>
          <div class="alert alert-info" role="status">
            Secret stored. Its value cannot be displayed again.
          </div>
        </Show>

        <div class="form-row">
          <Field label="Path" required hint="For example reactorcide/github." >
            {(ids) => (
              <input
                id={ids.id}
                class="input mono"
                type="text"
                aria-describedby={ids.describedBy}
                value={path()}
                onInput={(event) => setPath(event.currentTarget.value)}
              />
            )}
          </Field>
          <Field label="Key" required hint="For example api_token.">
            {(ids) => (
              <input
                id={ids.id}
                class="input mono"
                type="text"
                aria-describedby={ids.describedBy}
                value={key()}
                onInput={(event) => setKey(event.currentTarget.value)}
              />
            )}
          </Field>
        </div>

        <Field
          label="Value"
          required
          hint="Write-only. This is never displayed again, here or anywhere else."
          tooltip="Setting a value that already exists replaces it. Jobs reference it as ${secret:path:key} and never see it in a job definition."
        >
          {(ids) => (
            <input
              id={ids.id}
              class="input"
              type="password"
              autocomplete="off"
              aria-describedby={ids.describedBy}
              value={value()}
              onInput={(event) => {
                setValue(event.currentTarget.value)
                setSaved(false)
              }}
            />
          )}
        </Field>

        <button
          type="submit"
          class="btn btn-primary"
          disabled={action.busy() || !path().trim() || !key().trim() || !value()}
        >
          {action.busy() ? 'Storing…' : 'Store secret'}
        </button>
      </form>
    </div>
  )
}

function DeleteSecretControl(props: {
  orgId: string
  path: string
  keys: string[]
  onDeleted: () => void
}): JSX.Element {
  const [key, setKey] = createSignal('')
  const action = useAction()

  return (
    <div class="row">
      <ChoiceSelect
        value={key()}
        placeholder="Delete a key…"
        choices={props.keys.map((k) => ({ value: k, label: k, description: '' }))}
        onChange={setKey}
      />
      <Show when={key()}>
        <button
          type="button"
          class="btn btn-sm btn-danger"
          disabled={action.busy()}
          onClick={async () => {
            if (!confirm(`Delete ${props.path}:${key()}? Any job that references it will fail.`)) return
            const ok = await action.run('The secret could not be deleted.', () =>
              api.deleteSecret({ orgId: props.orgId, path: props.path, key: key() }),
            )
            if (ok) {
              setKey('')
              props.onDeleted()
            }
          }}
        >
          Delete
        </button>
      </Show>
      <ActionError error={action.error()} />
    </div>
  )
}

const MATCH_MODES = [
  { value: 'exact', label: 'Exact', description: 'Matches one literal value.' },
  { value: 'prefix', label: 'Prefix', description: 'Matches anything starting with the pattern.' },
  { value: 'glob', label: 'Glob', description: 'Matches a shell-style pattern, such as build-*.' },
]

function CreateGrantCard(props: { orgId: string; onCreated: () => void }): JSX.Element {
  const [name, setName] = createSignal('')
  const [pathMatch, setPathMatch] = createSignal('prefix')
  const [pathPattern, setPathPattern] = createSignal('')
  const [jobMatch, setJobMatch] = createSignal('glob')
  const [jobPattern, setJobPattern] = createSignal('')
  const action = useAction()

  return (
    <div class="card">
      <h3 class="section-title">New grant</h3>
      <p class="meta" style={{ 'margin-bottom': 'var(--space-4)' }}>
        A grant says which jobs may resolve which secrets. Keep the patterns as narrow as the work
        allows — a broad grant hands every matching job every matching secret.
      </p>
      <form
        onSubmit={async (event) => {
          event.preventDefault()
          if (!name().trim() || !pathPattern().trim()) return
          const ok = await action.run('The grant could not be created.', () =>
            api.createSecretGrant({
              orgId: props.orgId,
              name: name().trim(),
              secretPathMatch: pathMatch(),
              secretPathPattern: pathPattern().trim(),
              jobNameMatch: jobMatch(),
              jobNamePattern: jobPattern().trim() || undefined,
            }),
          )
          if (ok) {
            setName('')
            setPathPattern('')
            setJobPattern('')
            props.onCreated()
          }
        }}
      >
        <ActionError error={action.error()} />

        <Field label="Name" required hint="What this grant is for, in a few words.">
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

        <div class="form-row">
          <Field label="Secret path match" required>
            {(ids) => (
              <ChoiceSelect id={ids.id} value={pathMatch()} choices={MATCH_MODES} onChange={setPathMatch} />
            )}
          </Field>
          <Field label="Secret path pattern" required>
            {(ids) => (
              <input
                id={ids.id}
                class="input mono"
                type="text"
                placeholder="reactorcide/"
                value={pathPattern()}
                onInput={(event) => setPathPattern(event.currentTarget.value)}
              />
            )}
          </Field>
        </div>

        <div class="form-row">
          <Field label="Job name match" required>
            {(ids) => (
              <ChoiceSelect id={ids.id} value={jobMatch()} choices={MATCH_MODES} onChange={setJobMatch} />
            )}
          </Field>
          <Field label="Job name pattern" hint="Leave empty to match every job in the organization.">
            {(ids) => (
              <input
                id={ids.id}
                class="input mono"
                type="text"
                placeholder="deploy-*"
                aria-describedby={ids.describedBy}
                value={jobPattern()}
                onInput={(event) => setJobPattern(event.currentTarget.value)}
              />
            )}
          </Field>
        </div>

        <button
          type="submit"
          class="btn btn-primary"
          disabled={action.busy() || !name().trim() || !pathPattern().trim()}
        >
          {action.busy() ? 'Creating…' : 'Create grant'}
        </button>
      </form>
    </div>
  )
}

function GrantRow(props: {
  grant: {
    grantId: string
    name: string
    secretPathMatch: string
    secretPathPattern: string
    jobNameMatch: string
    jobNamePattern?: string
    projectId?: string
  }
  onDeleted: () => void
}): JSX.Element {
  const action = useAction()

  return (
    <tr>
      <td>{props.grant.name}</td>
      <td class="mono meta">
        {props.grant.secretPathMatch} {props.grant.secretPathPattern}
      </td>
      <td class="mono meta">
        {props.grant.jobNameMatch} {props.grant.jobNamePattern || '(any)'}
      </td>
      <td class="mono meta truncate">{props.grant.projectId || 'whole org'}</td>
      <td>
        <button
          type="button"
          class="btn btn-sm btn-danger"
          disabled={action.busy()}
          onClick={async () => {
            if (
              !confirm(
                `Delete grant "${props.grant.name}"? Jobs relying on it will stop being able to resolve those secrets.`,
              )
            )
              return
            const ok = await action.run('The grant could not be deleted.', () =>
              api.deleteSecretGrant({ grantId: props.grant.grantId }),
            )
            if (ok) props.onDeleted()
          }}
        >
          Delete
        </button>
        <ActionError error={action.error()} />
      </td>
    </tr>
  )
}
