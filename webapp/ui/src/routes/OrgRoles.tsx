import { For, Show, createResource, createSignal, type JSX } from 'solid-js'
import { A } from '@solidjs/router'
import { api } from '~/api/client.ts'
import { useSession } from '~/lib/session.tsx'
import { Field } from '~/components/Field.tsx'
import { ChoiceSelect } from '~/components/ChoiceInput.tsx'
import { relativeTime } from '~/components/States.tsx'
import { ActionError, NotPermitted, useAction } from '~/components/ManagementPage.tsx'

/**
 * Role assignments.
 *
 * A role binds a PRINCIPAL (a user, or a group) to a SCOPE (the organization,
 * or one project). The scope is what makes the same role name mean different
 * things: `admin` on an org administers that org; `owner` on a project owns
 * only that project.
 */

const PRINCIPAL_TYPES = [
  { value: 'user', label: 'User', description: 'One person, named by their user ID.' },
  { value: 'group', label: 'Group', description: 'Everyone in a group, now and in future.' },
]

const SCOPE_TYPES = [
  { value: 'org', label: 'Organization', description: 'Applies across the whole organization.' },
  { value: 'project', label: 'Project', description: 'Applies to one project only.' },
]

const ROLES = [
  { value: 'admin', label: 'Admin', description: 'Full administration of the scope, including who else may access it.' },
  { value: 'owner', label: 'Owner', description: 'Owns the scope and its settings, but does not administer access.' },
  { value: 'member', label: 'Member', description: 'Can see the scope, including its private resources.' },
]

export function OrgRoles(): JSX.Element {
  const { session } = useSession()
  return (
    <Show when={session()?.capabilities?.manageGroups} fallback={<NotPermitted what="roles" />}>
      <RolesPage orgId={session()!.user_id!} />
    </Show>
  )
}

function RolesPage(props: { orgId: string }): JSX.Element {
  const [assignments, { refetch }] = createResource(
    () => props.orgId,
    (orgId) => api.listRoleAssignments({ scopeType: 'org', scopeId: orgId }),
  )

  return (
    <div class="page">
      <div class="page-header">
        <div>
          <h1>Roles</h1>
          <p class="meta">
            Who can do what, and where. A role granted to a group reaches everyone in it.
          </p>
        </div>
        <A href="/org/groups" class="btn btn-sm">
          Manage groups
        </A>
      </div>

      <AssignRoleCard orgId={props.orgId} onAssigned={() => void refetch()} />

      <div class="card">
        <h3 class="section-title">Assignments in this organization</h3>
        <Show
          when={(assignments()?.assignments.length ?? 0) > 0}
          fallback={<div class="empty">No roles have been assigned in this organization.</div>}
        >
          <div class="scroll-x">
            <table class="table">
              <thead>
                <tr>
                  <th>Principal</th>
                  <th>Role</th>
                  <th>Scope</th>
                  <th>Granted</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                <For each={assignments()?.assignments}>
                  {(assignment) => (
                    <AssignmentRow assignment={assignment} onRevoked={() => void refetch()} />
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

function AssignRoleCard(props: { orgId: string; onAssigned: () => void }): JSX.Element {
  const [principalType, setPrincipalType] = createSignal('user')
  const [principalId, setPrincipalId] = createSignal('')
  const [scopeType, setScopeType] = createSignal('org')
  const [scopeId, setScopeId] = createSignal('')
  const [role, setRole] = createSignal('member')
  const action = useAction()

  return (
    <div class="card">
      <h3 class="section-title">Grant a role</h3>
      <form
        onSubmit={async (event) => {
          event.preventDefault()
          if (!principalId().trim()) return
          const ok = await action.run('The role could not be granted.', () =>
            api.assignRole({
              principalType: principalType(),
              principalId: principalId().trim(),
              scopeType: scopeType(),
              // An org-scoped grant defaults to THIS org, so the common case
              // needs no id typed at all.
              scopeId: scopeType() === 'org' ? props.orgId : scopeId().trim() || undefined,
              role: role(),
            }),
          )
          if (ok) {
            setPrincipalId('')
            setScopeId('')
            props.onAssigned()
          }
        }}
      >
        <ActionError error={action.error()} />

        <div class="form-row">
          <Field label="Principal type" required>
            {(ids) => (
              <ChoiceSelect
                id={ids.id}
                value={principalType()}
                choices={PRINCIPAL_TYPES}
                onChange={setPrincipalType}
              />
            )}
          </Field>
          <Field
            label={principalType() === 'group' ? 'Group ID' : 'User ID'}
            required
            hint={principalType() === 'group' ? 'From the Groups page.' : 'The person’s user ID.'}
          >
            {(ids) => (
              <input
                id={ids.id}
                class="input mono"
                type="text"
                aria-describedby={ids.describedBy}
                value={principalId()}
                onInput={(event) => setPrincipalId(event.currentTarget.value)}
              />
            )}
          </Field>
        </div>

        <div class="form-row">
          <Field label="Scope" required>
            {(ids) => (
              <ChoiceSelect id={ids.id} value={scopeType()} choices={SCOPE_TYPES} onChange={setScopeType} />
            )}
          </Field>
          <Show when={scopeType() === 'project'}>
            <Field label="Project ID" required hint="From the project's detail page.">
              {(ids) => (
                <input
                  id={ids.id}
                  class="input mono"
                  type="text"
                  aria-describedby={ids.describedBy}
                  value={scopeId()}
                  onInput={(event) => setScopeId(event.currentTarget.value)}
                />
              )}
            </Field>
          </Show>
        </div>

        <Field
          label="Role"
          required
          tooltip="Admin administers access as well as settings. Owner controls the scope but not who else may reach it. Member can see private resources in the scope."
        >
          {(ids) => <ChoiceSelect id={ids.id} value={role()} choices={ROLES} onChange={setRole} />}
        </Field>

        <button type="submit" class="btn btn-primary" disabled={action.busy() || !principalId().trim()}>
          {action.busy() ? 'Granting…' : 'Grant role'}
        </button>
      </form>
    </div>
  )
}

function AssignmentRow(props: {
  assignment: {
    assignmentId: string
    principalType: string
    principalId: string
    scopeType: string
    scopeId?: string
    role: string
    createdAt: string
  }
  onRevoked: () => void
}): JSX.Element {
  const action = useAction()

  return (
    <tr>
      <td>
        <div>{props.assignment.principalType}</div>
        <div class="meta mono truncate">{props.assignment.principalId}</div>
      </td>
      <td>{props.assignment.role}</td>
      <td class="meta">
        {props.assignment.scopeType}
        <Show when={props.assignment.scopeId}>
          <div class="mono truncate">{props.assignment.scopeId}</div>
        </Show>
      </td>
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
