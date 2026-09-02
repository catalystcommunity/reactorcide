import { For, Show, createResource, createSignal, type JSX } from 'solid-js'
import { A } from '@solidjs/router'
import { api } from '~/api/client.ts'
import { useSession } from '~/lib/session.tsx'
import { Field } from '~/components/Field.tsx'
import { relativeTime } from '~/components/States.tsx'
import { ActionError, NotPermitted, useAction } from '~/components/ManagementPage.tsx'

/**
 * Instance administration: global settings, and who may sign in.
 *
 * Global-admin only. Everything here affects the whole deployment rather than
 * one organization, which is why it is a separate page from the org management
 * screens and why each control says what it changes for everyone.
 */
export function Admin(): JSX.Element {
  const { session } = useSession()
  return (
    <Show when={session()?.is_global_admin} fallback={<NotPermitted what="this instance" />}>
      <AdminPage />
    </Show>
  )
}

function AdminPage(): JSX.Element {
  return (
    <div class="page">
      <div class="page-header">
        <div>
          <h1>Administration</h1>
          <p class="meta">Settings and access rules for the whole instance.</p>
        </div>
        <div class="row">
          <A href="/org/groups" class="btn btn-sm">Groups</A>
          <A href="/org/roles" class="btn btn-sm">Roles</A>
        </div>
      </div>

      <GlobalSettingsCard />
      <TrustedIdentitiesCard />
      <TrustedDomainsCard />
    </div>
  )
}

function GlobalSettingsCard(): JSX.Element {
  const [settings, { refetch }] = createResource(() => api.getGlobalSettings({}))
  const action = useAction()

  return (
    <div class="card">
      <h3 class="section-title">Defaults for new projects</h3>
      <ActionError error={action.error()} />
      <Show when={settings()} fallback={<p class="meta">Loading…</p>}>
        <div class="checkbox-row">
          <input
            id="new-projects-private"
            type="checkbox"
            checked={settings()!.newProjectsPrivate}
            disabled={action.busy()}
            onChange={async (event) => {
              const next = event.currentTarget.checked
              await action.run('The setting could not be saved.', () =>
                api.updateGlobalSettings({ newProjectsPrivate: next }),
              )
              void refetch()
            }}
          />
          <label for="new-projects-private">
            <strong>New projects are private by default</strong>
            <div class="field-hint">
              Applies when a project is created without an explicit choice. A private project's
              jobs and logs are visible only to its members and administrators. Changing this does
              not alter projects that already exist.
            </div>
          </label>
        </div>
      </Show>
    </div>
  )
}

/**
 * Individually trusted identities.
 *
 * An identity listed here may sign in regardless of the domain patterns below.
 * Removing one does not end a session it already holds.
 */
function TrustedIdentitiesCard(): JSX.Element {
  const [identities, { refetch }] = createResource(() => api.listTrustedIdentities({}))
  const [handle, setHandle] = createSignal('')
  const [domain, setDomain] = createSignal('')
  const action = useAction()

  return (
    <div class="card">
      <h3 class="section-title">Trusted identities</h3>
      <p class="meta" style={{ 'margin-bottom': 'var(--space-4)' }}>
        People allowed to sign in by name, whatever the domain rules say.
      </p>

      <form
        onSubmit={async (event) => {
          event.preventDefault()
          if (!handle().trim() || !domain().trim()) return
          const ok = await action.run('The identity could not be added.', () =>
            api.addTrustedIdentity({ handle: handle().trim(), domain: domain().trim() }),
          )
          if (ok) {
            setHandle('')
            setDomain('')
            void refetch()
          }
        }}
      >
        <ActionError error={action.error()} />
        <div class="form-row">
          <Field label="Handle" required>
            {(ids) => (
              <input
                id={ids.id}
                class="input mono"
                type="text"
                placeholder="alice"
                value={handle()}
                onInput={(event) => setHandle(event.currentTarget.value)}
              />
            )}
          </Field>
          <Field label="Domain" required>
            {(ids) => (
              <input
                id={ids.id}
                class="input mono"
                type="text"
                placeholder="example.com"
                value={domain()}
                onInput={(event) => setDomain(event.currentTarget.value)}
              />
            )}
          </Field>
        </div>
        <button
          type="submit"
          class="btn btn-primary"
          disabled={action.busy() || !handle().trim() || !domain().trim()}
        >
          Add identity
        </button>
      </form>

      <Show when={(identities()?.identities.length ?? 0) > 0}>
        <div class="scroll-x" style={{ 'margin-top': 'var(--space-4)' }}>
          <table class="table">
            <thead>
              <tr>
                <th>Identity</th>
                <th>Source</th>
                <th>Added</th>
                <th />
              </tr>
            </thead>
            <tbody>
              <For each={identities()?.identities}>
                {(identity) => (
                  <tr>
                    <td class="mono">
                      {identity.handle}@{identity.domain}
                    </td>
                    <td class="meta">{identity.source}</td>
                    <td class="meta">{relativeTime(identity.createdAt)}</td>
                    <td>
                      <button
                        type="button"
                        class="btn btn-sm btn-danger"
                        disabled={action.busy()}
                        onClick={async () => {
                          if (
                            !confirm(
                              `Remove ${identity.handle}@${identity.domain}? They will not be able to sign in again, but any session they already hold continues until it expires.`,
                            )
                          )
                            return
                          await action.run('The identity could not be removed.', () =>
                            api.removeTrustedIdentity({
                              handle: identity.handle,
                              domain: identity.domain,
                            }),
                          )
                          void refetch()
                        }}
                      >
                        Remove
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
  )
}

function TrustedDomainsCard(): JSX.Element {
  const [patterns, { refetch }] = createResource(() => api.listTrustedDomainPatterns({}))
  const [pattern, setPattern] = createSignal('')
  const [description, setDescription] = createSignal('')
  const action = useAction()

  return (
    <div class="card">
      <h3 class="section-title">Trusted domain patterns</h3>
      <p class="meta" style={{ 'margin-bottom': 'var(--space-4)' }}>
        Anyone whose identity domain matches a pattern here may sign in. A pattern is far broader
        than a single identity — check it says what you mean before adding it.
      </p>

      <form
        onSubmit={async (event) => {
          event.preventDefault()
          if (!pattern().trim()) return
          const ok = await action.run('The pattern could not be added.', () =>
            api.addTrustedDomainPattern({
              pattern: pattern().trim(),
              description: description().trim() || undefined,
            }),
          )
          if (ok) {
            setPattern('')
            setDescription('')
            void refetch()
          }
        }}
      >
        <ActionError error={action.error()} />
        <div class="form-row">
          <Field label="Pattern" required hint="For example *.example.com">
            {(ids) => (
              <input
                id={ids.id}
                class="input mono"
                type="text"
                aria-describedby={ids.describedBy}
                value={pattern()}
                onInput={(event) => setPattern(event.currentTarget.value)}
              />
            )}
          </Field>
          <Field label="Description" hint="Why this domain is trusted.">
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
        <button type="submit" class="btn btn-primary" disabled={action.busy() || !pattern().trim()}>
          Add pattern
        </button>
      </form>

      <Show when={(patterns()?.patterns.length ?? 0) > 0}>
        <div class="scroll-x" style={{ 'margin-top': 'var(--space-4)' }}>
          <table class="table">
            <thead>
              <tr>
                <th>Pattern</th>
                <th>Description</th>
                <th>Added</th>
                <th />
              </tr>
            </thead>
            <tbody>
              <For each={patterns()?.patterns}>
                {(entry) => (
                  <tr>
                    <td class="mono">{entry.pattern}</td>
                    <td class="meta">{entry.description || '—'}</td>
                    <td class="meta">{relativeTime(entry.createdAt)}</td>
                    <td>
                      <button
                        type="button"
                        class="btn btn-sm btn-danger"
                        disabled={action.busy()}
                        onClick={async () => {
                          if (!confirm(`Remove pattern "${entry.pattern}"?`)) return
                          await action.run('The pattern could not be removed.', () =>
                            api.removeTrustedDomainPattern({ patternId: entry.patternId }),
                          )
                          void refetch()
                        }}
                      >
                        Remove
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
  )
}
