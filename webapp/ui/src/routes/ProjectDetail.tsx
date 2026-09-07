import { For, Show, createMemo, createResource, createSignal, type JSX } from 'solid-js'
import { A, useParams } from '@solidjs/router'
import { useProject, useFormMetadata, useCapabilities } from '~/store/resources.ts'
import { api, ServiceError } from '~/api/client.ts'
import type {
  EnumChoice,
  GetCapabilitiesResponse,
  GroupSummary,
  RoleAssignment,
  UserSummary,
} from '~/api/csilapi/types.gen.ts'
import { ResourceView, relativeTime } from '~/components/States.tsx'
import { VisibilityBadge } from './Projects.tsx'
import { Field } from '~/components/Field.tsx'
import { ChoiceSelect, ChoiceMultiSelect } from '~/components/ChoiceInput.tsx'
import { UserPicker, userLabel } from '~/components/UserPicker.tsx'
import { ActionError, describeError, useAction } from '~/components/ManagementPage.tsx'

export function ProjectDetail(): JSX.Element {
  const params = useParams<{ id: string }>()
  const { state, refresh } = useProject(() => params.id)

  // PROJECT-scoped capabilities, asked of the coordinator for this project.
  //
  // The session's capability set is the global scope, which is all-false for
  // everyone but a global admin. A project owner and the owning org's admins
  // may manage this project and show up nowhere globally, so the old gate
  // (`session.capabilities.manageProjectSettings`) hid every card from exactly
  // the people meant to use them. Nothing here is a security control: the
  // coordinator authorizes each operation again.
  const caps = useCapabilities(() => ({ projectId: params.id }))
  const can = (capability: keyof GetCapabilitiesResponse) => Boolean(caps.state().data?.[capability])
  const canManage = () => can('manageProjectSettings')

  return (
    <div class="page">
      <div style={{ 'margin-bottom': 'var(--space-4)' }}>
        <A href="/projects" class="meta">
          ← All projects
        </A>
      </div>

      <ResourceView state={state()}>
        {(data) => (
          <>
            <div class="page-header">
              <div>
                <div class="row">
                  <h1>{data().project.name}</h1>
                  <VisibilityBadge isPrivate={data().project.isPrivate} />
                  <Show when={!data().project.enabled}>
                    <span class="status status-neutral">
                      <span class="status-glyph" aria-hidden="true">
                        ⊘
                      </span>
                      Disabled
                    </span>
                  </Show>
                </div>
                <p class="meta">{data().project.repoUrl}</p>
              </div>
            </div>

            <Show when={canManage()}>
              <VisibilityCard project={data().project} onChanged={refresh} />
            </Show>

            <div class="card">
              <dl class="grid-dl">
                <div>
                  <dt>Project ID</dt>
                  <dd class="mono truncate">{data().project.projectId}</dd>
                </div>
                <div>
                  <dt>Organization</dt>
                  <dd class="mono truncate">{data().project.orgId}</dd>
                </div>
                <div>
                  <dt>Target branches</dt>
                  <dd>{data().project.targetBranches.join(', ') || 'Every branch'}</dd>
                </div>
                <div>
                  <dt>Allowed events</dt>
                  <dd>{data().project.allowedEventTypes.join(', ') || 'Default set'}</dd>
                </div>
              </dl>
            </div>

            <Show when={canManage()}>
              <SettingsCard project={data().project} onSaved={refresh} />
            </Show>

            <Show when={can('manageGroups')}>
              <AccessCard project={data().project} />
            </Show>

            <Show when={can('manageWebhookSecrets')}>
              <RotationCard
                title="Webhook secrets"
                noun="webhook secret"
                purpose="A webhook secret is what this project uses to verify the signature on each incoming webhook from that provider. An event whose signature matches no active secret is rejected."
                valueLabel="Secret"
                projectId={data().project.projectId}
                list={async (projectId) => (await api.listWebhookSecrets({ projectId })).secrets}
                add={(request) => api.addWebhookSecret(request)}
                deactivate={(id) => api.deactivateWebhookSecret({ id })}
                remove={(id) => api.deleteWebhookSecret({ id })}
              />
            </Show>

            <Show when={can('manageVcsCredentials')}>
              <RotationCard
                title="VCS credentials"
                noun="VCS credential"
                purpose="A VCS credential is the token this project uses to clone the repository and to post commit statuses back to the provider."
                valueLabel="Token"
                projectId={data().project.projectId}
                list={async (projectId) => (await api.listVcsCredentials({ projectId })).credentials}
                add={(request) => api.addVcsCredential(request)}
                deactivate={(id) => api.deactivateVcsCredential({ id })}
                remove={(id) => api.deleteVcsCredential({ id })}
              />
            </Show>
          </>
        )}
      </ResourceView>
    </div>
  )
}

/**
 * Changing a project between public and private.
 *
 * Deliberately its own card with its own confirmed action, rather than a
 * checkbox at the bottom of a long settings form. Making a project public
 * exposes every job, workflow and log it owns to anyone who can reach the
 * instance -- that is not a change to make by accident while editing a runner
 * image, and it is not one whose consequence should have to be remembered.
 */
function VisibilityCard(props: {
  project: { projectId: string; isPrivate: boolean; name: string }
  onChanged: () => Promise<void>
}): JSX.Element {
  const [busy, setBusy] = createSignal(false)
  const [error, setError] = createSignal<string>()

  const goingPublic = () => props.project.isPrivate

  const change = async () => {
    const target = !props.project.isPrivate
    const warning = target
      ? `Make "${props.project.name}" private? Its jobs, workflows and logs will become visible only to its members and administrators.`
      : `Make "${props.project.name}" PUBLIC? Everyone who can reach this instance will be able to read its jobs, workflows and logs, including anyone not signed in.`

    if (!confirm(warning)) return

    setBusy(true)
    setError(undefined)
    try {
      await api.updateProject({ projectId: props.project.projectId, isPrivate: target })
      await props.onChanged()
    } catch (cause) {
      setError(
        cause instanceof ServiceError ? cause.message : 'The visibility could not be changed.',
      )
    } finally {
      setBusy(false)
    }
  }

  return (
    <div class="card">
      <div class="card-header">
        <div>
          <h3>Visibility</h3>
          <p class="meta">
            <Show
              when={props.project.isPrivate}
              fallback="Anyone who can reach this instance can read this project's jobs, workflows and logs — including signed-out visitors."
            >
              Only this project's members, its organization's administrators, and global
              administrators can see its jobs, workflows and logs.
            </Show>
          </p>
        </div>
        <button
          type="button"
          class={`btn btn-sm ${goingPublic() ? 'btn-danger' : ''}`}
          disabled={busy()}
          onClick={() => void change()}
        >
          {goingPublic() ? 'Make public' : 'Make private'}
        </button>
      </div>
      <Show when={error()}>
        <div class="alert alert-error" role="alert">
          {error()}
        </div>
      </Show>
    </div>
  )
}

function SettingsCard(props: {
  project: {
    projectId: string
    name: string
    description: string
    enabled: boolean
    targetBranches: string[]
    allowedEventTypes: string[]
    checkoutMode: string
    defaultRunnerImage: string
    defaultQueueName: string
  }
  onSaved: () => Promise<void>
}): JSX.Element {
  const metadata = useFormMetadata()
  const [name, setName] = createSignal(props.project.name)
  const [description, setDescription] = createSignal(props.project.description)
  const [branches, setBranches] = createSignal(props.project.targetBranches.join(', '))
  const [eventTypes, setEventTypes] = createSignal([...props.project.allowedEventTypes])
  const [checkoutMode, setCheckoutMode] = createSignal(props.project.checkoutMode)
  const [enabled, setEnabled] = createSignal(props.project.enabled)
  const [runnerImage, setRunnerImage] = createSignal(props.project.defaultRunnerImage)

  const [busy, setBusy] = createSignal(false)
  const [error, setError] = createSignal<string>()
  const [saved, setSaved] = createSignal(false)
  const nameError = createMemo(() => (name().trim() ? undefined : 'A project needs a name.'))

  const submit = async (event: Event) => {
    event.preventDefault()
    if (nameError()) return

    setBusy(true)
    setError(undefined)
    setSaved(false)
    try {
      await api.updateProject({
        projectId: props.project.projectId,
        name: name().trim(),
        description: description().trim(),
        enabled: enabled(),
        targetBranches: branches()
          .split(',')
          .map((b) => b.trim())
          .filter(Boolean),
        allowedEventTypes: eventTypes(),
        checkoutMode: checkoutMode() || undefined,
        defaultRunnerImage: runnerImage().trim() || undefined,
      })
      await props.onSaved()
      setSaved(true)
    } catch (cause) {
      setError(cause instanceof ServiceError ? cause.message : 'The settings could not be saved.')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div class="card">
      <h3 class="section-title">Settings</h3>
      <form onSubmit={submit} novalidate>
        <Show when={error()}>
          <div class="alert alert-error" role="alert">
            {error()}
          </div>
        </Show>
        <Show when={saved()}>
          <div class="alert alert-info" role="status">
            Settings saved.
          </div>
        </Show>

        <Field label="Name" required error={nameError()}>
          {(ids) => (
            <input
              id={ids.id}
              class="input"
              type="text"
              maxlength="255"
              aria-invalid={ids.invalid ? 'true' : undefined}
              aria-describedby={ids.describedBy}
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
              maxlength="255"
              value={description()}
              onInput={(event) => setDescription(event.currentTarget.value)}
            />
          )}
        </Field>

        <Field label="Target branches" hint="Comma separated. Empty accepts every branch.">
          {(ids) => (
            <input
              id={ids.id}
              class="input"
              type="text"
              aria-describedby={ids.describedBy}
              value={branches()}
              onInput={(event) => setBranches(event.currentTarget.value)}
            />
          )}
        </Field>

        <Field
          label="Allowed event types"
          hint="Which repository events start CI."
          tooltip="Choosing none applies the default set: push, pull request opened, pull request updated, and tag created."
        >
          {(ids) => (
            <ChoiceMultiSelect
              id={ids.id}
              values={eventTypes()}
              choices={metadata.state().data?.eventTypes ?? []}
              describedBy={ids.describedBy}
              onChange={setEventTypes}
            />
          )}
        </Field>

        <Field label="Pull-request checkout mode">
          {(ids) => (
            <ChoiceSelect
              id={ids.id}
              value={checkoutMode()}
              placeholder="Use the coordinator default"
              choices={metadata.state().data?.checkoutModes ?? []}
              onChange={setCheckoutMode}
            />
          )}
        </Field>

        <Field label="Default runner image" hint="Used when a job does not name its own.">
          {(ids) => (
            <input
              id={ids.id}
              class="input mono"
              type="text"
              aria-describedby={ids.describedBy}
              value={runnerImage()}
              onInput={(event) => setRunnerImage(event.currentTarget.value)}
            />
          )}
        </Field>

        <div class="checkbox-row">
          <input
            id="project-enabled"
            type="checkbox"
            checked={enabled()}
            onChange={(event) => setEnabled(event.currentTarget.checked)}
          />
          <label for="project-enabled">
            <strong>Enabled</strong>
            <div class="field-hint">A disabled project ignores incoming webhook events.</div>
          </label>
        </div>

        <button type="submit" class="btn btn-primary" disabled={busy() || Boolean(nameError())}>
          {busy() ? 'Saving…' : 'Save settings'}
        </button>
      </form>
    </div>
  )
}

/**
 * Who may see and manage this project.
 *
 * Roles granted here are PROJECT-scoped: `member` on this project sees this
 * project's private jobs and nothing else in the organization. That is the
 * grant an operator reaches for when "change who sees a private project" is
 * the task, and until this card existed it could only be done from the org
 * Roles page by pasting a project id and a user UUID.
 */

const PRINCIPAL_TYPES: EnumChoice[] = [
  { value: 'user', label: 'User', description: 'One person.' },
  { value: 'group', label: 'Group', description: 'Everyone in a group, now and in future.' },
]

const PROJECT_ROLES: EnumChoice[] = [
  { value: 'member', label: 'Member', description: 'Can see this project, including its private jobs, workflows and logs.' },
  { value: 'owner', label: 'Owner', description: 'Owns this project and its settings, but does not administer access to it.' },
  {
    value: 'admin',
    label: 'Admin',
    description: 'Full administration of this project, including who else may access it. Unusual on a single project; org admins already have this.',
  },
]

function roleLabel(role: string): string {
  return PROJECT_ROLES.find((choice) => choice.value === role)?.label ?? role
}

function AccessCard(props: { project: { projectId: string; orgId: string } }): JSX.Element {
  // The list itself. A ServiceError (typically forbidden) is captured as a
  // message rather than thrown, so the card explains itself instead of the
  // whole page erroring.
  const [assignments, { refetch }] = createResource(
    () => props.project.projectId,
    async (projectId) => {
      try {
        return {
          rows: (await api.listRoleAssignments({ scopeType: 'project', scopeId: projectId })).assignments,
          error: undefined,
        }
      } catch (cause) {
        return { rows: [] as RoleAssignment[], error: describeError(cause, 'The access list could not be loaded.') }
      }
    },
  )

  // Names for the ids. Fetched ONCE per org, not per row; a failure here only
  // means ids are shown raw, so it is swallowed.
  const [users] = createResource(
    () => props.project.orgId,
    async (orgId) => {
      try {
        return (await api.listUsers({ orgId })).users
      } catch {
        return [] as UserSummary[]
      }
    },
  )
  const [groups] = createResource(
    () => props.project.orgId,
    async (orgId) => {
      try {
        return (await api.listGroups({ orgId })).groups
      } catch {
        return [] as GroupSummary[]
      }
    },
  )

  const userNames = createMemo(() => new Map((users() ?? []).map((user) => [user.userId, userLabel(user)])))
  const groupNames = createMemo(() => new Map((groups() ?? []).map((group) => [group.groupId, group.name])))
  const principalName = (assignment: RoleAssignment): string | undefined =>
    assignment.principalType === 'group'
      ? groupNames().get(assignment.principalId)
      : userNames().get(assignment.principalId)

  return (
    <div class="card">
      <h3 class="section-title">Access</h3>
      <p class="meta" style={{ 'margin-bottom': 'var(--space-4)' }}>
        A private project is visible to its members, to the administrators of its organization,
        and to global administrators. Roles granted here apply to THIS project only; roles on
        the whole organization are managed on the Roles page.
      </p>

      <GrantForm
        project={props.project}
        groups={groups() ?? []}
        onGranted={() => void refetch()}
      />

      <Show when={assignments()?.error}>
        <div class="alert alert-error" role="alert">
          {assignments()?.error}
        </div>
      </Show>

      <Show when={assignments() && !assignments()?.error}>
        <Show
          when={(assignments()?.rows.length ?? 0) > 0}
          fallback={
            <div class="empty">
              No roles are granted on this project. Only its organization's administrators and
              global administrators can see it while it is private.
            </div>
          }
        >
          <div class="scroll-x">
            <table class="table">
              <thead>
                <tr>
                  <th>Type</th>
                  <th>Principal</th>
                  <th>Role</th>
                  <th>Granted</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                <For each={assignments()?.rows}>
                  {(assignment) => (
                    <AssignmentRow
                      assignment={assignment}
                      name={principalName(assignment)}
                      onRevoked={() => void refetch()}
                    />
                  )}
                </For>
              </tbody>
            </table>
          </div>
        </Show>
      </Show>
    </div>
  )
}

function GrantForm(props: {
  project: { projectId: string; orgId: string }
  groups: GroupSummary[]
  onGranted: () => void
}): JSX.Element {
  const [principalType, setPrincipalType] = createSignal('user')
  const [principalId, setPrincipalId] = createSignal('')
  const [role, setRole] = createSignal('member')
  const action = useAction()

  const groupChoices = createMemo<EnumChoice[]>(() =>
    props.groups.map((group) => ({
      value: group.groupId,
      label: group.name,
      description: group.description ?? '',
    })),
  )

  return (
    <form
      style={{ 'margin-bottom': 'var(--space-4)' }}
      onSubmit={async (event) => {
        event.preventDefault()
        if (!principalId()) return
        const ok = await action.run('The role could not be granted.', () =>
          api.assignRole({
            principalType: principalType(),
            principalId: principalId(),
            scopeType: 'project',
            scopeId: props.project.projectId,
            role: role(),
          }),
        )
        if (ok) {
          setPrincipalId('')
          props.onGranted()
        }
      }}
    >
      <ActionError error={action.error()} />

      <div class="form-row">
        <Field label="Grant to" required>
          {(ids) => (
            <ChoiceSelect
              id={ids.id}
              value={principalType()}
              choices={PRINCIPAL_TYPES}
              onChange={(value) => {
                setPrincipalType(value)
                // A user id is not a group id; a type change starts over.
                setPrincipalId('')
              }}
            />
          )}
        </Field>
        <Field
          label="Role"
          required
          tooltip="Member can see this project's private resources. Owner controls the project's settings but not who else may reach it. Admin administers access as well."
        >
          {(ids) => <ChoiceSelect id={ids.id} value={role()} choices={PROJECT_ROLES} onChange={setRole} />}
        </Field>
      </div>

      <Show
        when={principalType() === 'group'}
        fallback={
          <Field label="User" required hint="Accounts known to this project's organization.">
            {(ids) => (
              <UserPicker
                id={ids.id}
                orgId={props.project.orgId}
                value={principalId()}
                describedBy={ids.describedBy}
                onChange={setPrincipalId}
              />
            )}
          </Field>
        }
      >
        <Field label="Group" required hint="Groups belong to this project's organization.">
          {(ids) => (
            <ChoiceSelect
              id={ids.id}
              value={principalId()}
              placeholder={groupChoices().length ? 'Choose a group' : 'This organization has no groups'}
              choices={groupChoices()}
              describedBy={ids.describedBy}
              disabled={groupChoices().length === 0}
              onChange={setPrincipalId}
            />
          )}
        </Field>
      </Show>

      <button type="submit" class="btn btn-primary" disabled={action.busy() || !principalId()}>
        {action.busy() ? 'Granting…' : 'Grant role'}
      </button>
    </form>
  )
}

function AssignmentRow(props: {
  assignment: RoleAssignment
  name: string | undefined
  onRevoked: () => void
}): JSX.Element {
  const action = useAction()

  return (
    <tr>
      <td>{props.assignment.principalType === 'group' ? 'Group' : 'User'}</td>
      <td>
        <Show when={props.name} fallback={<span class="mono truncate">{props.assignment.principalId}</span>}>
          <div>{props.name}</div>
          <div class="meta mono truncate">{props.assignment.principalId}</div>
        </Show>
      </td>
      <td>{roleLabel(props.assignment.role)}</td>
      <td class="meta">{relativeTime(props.assignment.createdAt)}</td>
      <td>
        <button
          type="button"
          class="btn btn-sm btn-danger"
          disabled={action.busy()}
          onClick={async () => {
            if (!confirm('Revoke this role? Access it granted is removed immediately.')) return
            const ok = await action.run('The role could not be revoked.', () =>
              api.revokeRole({ assignmentId: props.assignment.assignmentId }),
            )
            if (ok) props.onRevoked()
          }}
        >
          Revoke
        </button>
        <ActionError error={action.error()} />
      </td>
    </tr>
  )
}

/**
 * Webhook secrets and VCS credentials.
 *
 * One implementation for both, because the coordinator models them
 * identically: a write-only value with a provider, a name, an active flag and
 * a last-used time. The value is never returned by any operation, and this
 * component never holds it past the request that stores it.
 *
 * Several rows may be active at once, on purpose: rotation is "add the new
 * one, switch the provider over, then deactivate the old one", and a
 * one-active-at-a-time rule would force a window in which webhooks fail.
 */

interface RotationRow {
  id: string
  provider: string
  name: string
  isActive: boolean
  lastUsedAt?: string
  createdAt: string
  deactivatedAt?: string
}

function RotationCard(props: {
  title: string
  /** Lower-case singular, for sentences: "webhook secret". */
  noun: string
  /** One or two sentences on what the value does. */
  purpose: string
  /** Label for the write-only value field: "Secret" or "Token". */
  valueLabel: string
  projectId: string
  list: (projectId: string) => Promise<RotationRow[]>
  add: (request: { projectId: string; provider: string; name: string; value: string }) => Promise<unknown>
  deactivate: (id: string) => Promise<unknown>
  remove: (id: string) => Promise<unknown>
}): JSX.Element {
  const metadata = useFormMetadata()
  const providers = () => metadata.state().data?.vcsProviders ?? []

  const [rows, { refetch }] = createResource(
    () => props.projectId,
    async (projectId) => {
      try {
        return { rows: await props.list(projectId), error: undefined }
      } catch (cause) {
        return { rows: [] as RotationRow[], error: describeError(cause, `The ${props.noun}s could not be loaded.`) }
      }
    },
  )

  const [provider, setProvider] = createSignal('')
  const [name, setName] = createSignal('')
  const [value, setValue] = createSignal('')
  const action = useAction()
  const [added, setAdded] = createSignal(false)

  const incomplete = () => !provider() || !name().trim() || !value()

  const providerLabel = (id: string) => providers().find((choice) => choice.value === id)?.label ?? id

  return (
    <div class="card">
      <h3 class="section-title">{props.title}</h3>
      <p class="meta" style={{ 'margin-bottom': 'var(--space-4)' }}>
        {props.purpose} More than one may be active at a time: to rotate, add the new one, then
        deactivate the old one once the provider is switched over.
      </p>

      <form
        style={{ 'margin-bottom': 'var(--space-4)' }}
        autocomplete="off"
        onSubmit={async (event) => {
          event.preventDefault()
          if (incomplete()) return
          setAdded(false)
          const ok = await action.run(`The ${props.noun} could not be added.`, () =>
            props.add({
              projectId: props.projectId,
              provider: provider(),
              name: name().trim(),
              value: value(),
            }),
          )
          // The value leaves memory whether or not the request succeeded, so a
          // failed submit cannot leave a secret sitting in a form field.
          setValue('')
          if (ok) {
            setName('')
            setAdded(true)
            void refetch()
          }
        }}
      >
        <ActionError error={action.error()} />
        <Show when={added()}>
          <div class="alert alert-info" role="status">
            The {props.noun} was stored. Its value is not shown again.
          </div>
        </Show>

        <div class="form-row">
          <Field label="Provider" required>
            {(ids) => (
              <ChoiceSelect
                id={ids.id}
                value={provider()}
                placeholder="Choose a provider"
                choices={providers()}
                onChange={setProvider}
              />
            )}
          </Field>
          <Field label="Name" required hint="Something that tells this one from the next: the date, or the account it belongs to.">
            {(ids) => (
              <input
                id={ids.id}
                class="input"
                type="text"
                maxlength="255"
                aria-describedby={ids.describedBy}
                value={name()}
                onInput={(event) => setName(event.currentTarget.value)}
              />
            )}
          </Field>
        </div>

        <Field label={props.valueLabel} required hint="Stored encrypted. It is never shown again.">
          {(ids) => (
            <input
              id={ids.id}
              class="input mono"
              type="password"
              autocomplete="off"
              aria-describedby={ids.describedBy}
              value={value()}
              onInput={(event) => setValue(event.currentTarget.value)}
            />
          )}
        </Field>

        <button type="submit" class="btn btn-primary" disabled={action.busy() || incomplete()}>
          {action.busy() ? 'Adding…' : `Add ${props.noun}`}
        </button>
      </form>

      <Show when={rows()?.error}>
        <div class="alert alert-error" role="alert">
          {rows()?.error}
        </div>
      </Show>

      <Show when={rows() && !rows()?.error}>
        <Show
          when={(rows()?.rows.length ?? 0) > 0}
          fallback={<div class="empty">No {props.noun}s yet.</div>}
        >
          <div class="scroll-x">
            <table class="table">
              <thead>
                <tr>
                  <th>Provider</th>
                  <th>Name</th>
                  <th>Status</th>
                  <th>Last used</th>
                  <th>Created</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                <For each={rows()?.rows}>
                  {(row) => (
                    <RotationRowView
                      row={row}
                      noun={props.noun}
                      providerLabel={providerLabel(row.provider)}
                      deactivate={props.deactivate}
                      remove={props.remove}
                      onChanged={() => void refetch()}
                    />
                  )}
                </For>
              </tbody>
            </table>
          </div>
        </Show>
      </Show>
    </div>
  )
}

function RotationRowView(props: {
  row: RotationRow
  noun: string
  providerLabel: string
  deactivate: (id: string) => Promise<unknown>
  remove: (id: string) => Promise<unknown>
  onChanged: () => void
}): JSX.Element {
  const action = useAction()

  return (
    <tr>
      <td>{props.providerLabel}</td>
      <td>{props.row.name}</td>
      <td>
        <Show
          when={props.row.isActive}
          fallback={
            <span class="status status-neutral">
              <span class="status-glyph" aria-hidden="true">
                ⊘
              </span>
              Deactivated {relativeTime(props.row.deactivatedAt)}
            </span>
          }
        >
          <span class="status status-ok">
            <span class="status-glyph" aria-hidden="true">
              ●
            </span>
            Active
          </span>
        </Show>
      </td>
      <td class="meta">{props.row.lastUsedAt ? relativeTime(props.row.lastUsedAt) : 'Never'}</td>
      <td class="meta">{relativeTime(props.row.createdAt)}</td>
      <td>
        <div class="row">
          <Show when={props.row.isActive}>
            <button
              type="button"
              class="btn btn-sm"
              disabled={action.busy()}
              onClick={async () => {
                if (
                  !confirm(
                    `Deactivate the ${props.noun} "${props.row.name}"? Anything still using it stops working. It stays listed and can be deleted later.`,
                  )
                )
                  return
                const ok = await action.run(`The ${props.noun} could not be deactivated.`, () =>
                  props.deactivate(props.row.id),
                )
                if (ok) props.onChanged()
              }}
            >
              Deactivate
            </button>
          </Show>
          <button
            type="button"
            class="btn btn-sm btn-danger"
            disabled={action.busy()}
            onClick={async () => {
              if (!confirm(`Delete the ${props.noun} "${props.row.name}"? This cannot be undone.`)) return
              const ok = await action.run(`The ${props.noun} could not be deleted.`, () =>
                props.remove(props.row.id),
              )
              if (ok) props.onChanged()
            }}
          >
            Delete
          </button>
        </div>
        <ActionError error={action.error()} />
      </td>
    </tr>
  )
}
