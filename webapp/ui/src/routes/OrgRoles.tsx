import { For, Show, createMemo, createResource, createSignal, type Accessor, type JSX } from 'solid-js'
import { A, useSearchParams } from '@solidjs/router'
import { api } from '~/api/client.ts'
import type { GroupSummary, OrgSummary, UserSummary } from '~/api/csilapi/types.gen.ts'
import { adminOrgIds, canManageSomeOrg, useSession } from '~/lib/session.tsx'
import { useOrgs } from '~/store/resources.ts'
import { Field } from '~/components/Field.tsx'
import { ChoiceSelect } from '~/components/ChoiceInput.tsx'
import { OrgPicker, pickInitialOrg } from '~/components/OrgPicker.tsx'
import { UserPicker, userLabel } from '~/components/UserPicker.tsx'
import { Skeleton, relativeTime } from '~/components/States.tsx'
import { ActionError, NotPermitted, useAction } from '~/components/ManagementPage.tsx'

/**
 * Role assignments.
 *
 * A role binds a PRINCIPAL (a user, or a group) to a SCOPE (the organization,
 * or one project). The scope is what makes the same role name mean different
 * things: `admin` on an org administers that org; `owner` on a project owns
 * only that project.
 */

/* ------------------------------------------------------------------------ */
/* Which organization a management page is about                             */
/* ------------------------------------------------------------------------ */

/**
 * The org an org-management page manages, and how to change it.
 *
 * These pages used to pass the caller's own user id as the org id. Projects,
 * groups, roles and secrets live in `organizations` rows with their own ids,
 * and a person who signed in through LinkKeys has no such row at all, so the
 * pages managed an org that owned nothing: a role granted there reached
 * nobody and a secret set there was resolvable by no project.
 *
 * The choice lives in `?org=` so it survives moving between Access, Groups
 * and Secrets and can be linked to. Absent or not administered by the caller,
 * it falls back to the default org when they may manage it, else the first
 * one they may. `orgId()` is '' until the list has loaded or when the caller
 * administers nothing; callers must not issue a request with that.
 *
 * Exported from here, rather than from a component module, because the three
 * pages that need it are the only callers and share this file boundary.
 */
export interface ManagedOrg {
  /** The chosen org's id, or '' when there is none yet. */
  orgId: Accessor<string>
  /** The chosen org, or undefined when there is none yet. */
  org: Accessor<OrgSummary | undefined>
  /** Every org the caller administers. */
  orgs: Accessor<OrgSummary[]>
  /** True until the org list has answered (successfully or not). */
  loading: Accessor<boolean>
  setOrg: (orgId: string) => void
}

export function useManagedOrg(): ManagedOrg {
  const { session } = useSession()
  const { state } = useOrgs()
  const [params, setParams] = useSearchParams<{ org?: string }>()

  const allowed = (org: OrgSummary): boolean => {
    const current = session()
    return Boolean(current?.is_global_admin) || adminOrgIds(current).includes(org.orgId)
  }

  const orgs = createMemo(() => (state().data?.orgs ?? []).filter(allowed))
  const orgId = createMemo(() => {
    const requested = typeof params.org === 'string' ? params.org : ''
    if (requested && orgs().some((org) => org.orgId === requested)) return requested
    return pickInitialOrg(state().data?.orgs ?? [], allowed)
  })
  const org = createMemo(() => orgs().find((candidate) => candidate.orgId === orgId()))
  const loading = () => !state().loaded && !state().error

  return {
    orgId,
    org,
    orgs,
    loading,
    setOrg: (next) => setParams({ org: next }),
  }
}

/**
 * The gate every org-management page passes through.
 *
 * Order matters. While the session is still loading nothing is drawn, so a
 * page never flashes "not permitted" at somebody who is about to be. Then the
 * caller must administer SOME org (the nav uses the same test), then the org
 * list must have answered, then there must be an org to manage. Only then does
 * the page render, and only then does any org-scoped request go out.
 */
export function ManagedOrgPage(props: {
  what: string
  children: (managed: ManagedOrg) => JSX.Element
}): JSX.Element {
  const { session } = useSession()
  return (
    <Show when={Boolean(session())} fallback={<div class="page"><Skeleton rows={3} /></div>}>
      <Show when={canManageSomeOrg(session())} fallback={<NotPermitted what={props.what} />}>
        <ManagedOrgBody what={props.what}>{props.children}</ManagedOrgBody>
      </Show>
    </Show>
  )
}

function ManagedOrgBody(props: {
  what: string
  children: (managed: ManagedOrg) => JSX.Element
}): JSX.Element {
  const managed = useManagedOrg()
  return (
    <Show when={!managed.loading()} fallback={<div class="page"><Skeleton rows={3} /></div>}>
      <Show when={Boolean(managed.orgId())} fallback={<NoManagedOrg what={props.what} />}>
        {props.children(managed)}
      </Show>
    </Show>
  )
}

function NoManagedOrg(props: { what: string }): JSX.Element {
  return (
    <div class="page">
      <div class="empty">
        <div class="empty-title">You do not administer any organization.</div>
        <p>
          Managing {props.what} needs the admin role on an organization. Ask an administrator to
          grant it.
        </p>
      </div>
    </div>
  )
}

/** The org picker as it appears in every org-management page header. */
export function OrgHeaderPicker(props: { managed: ManagedOrg }): JSX.Element {
  return (
    <div class="row" style={{ 'align-items': 'center', gap: 'var(--space-2)' }}>
      <label class="field-label" for="managed-org" style={{ margin: 0 }}>
        Organization
      </label>
      <OrgPicker
        id="managed-org"
        orgs={props.managed.orgs()}
        value={props.managed.orgId()}
        onChange={props.managed.setOrg}
      />
    </div>
  )
}

/** A link to another org-management page that keeps the chosen org. */
export function orgHref(path: string, orgId: string): string {
  return orgId ? `${path}?org=${encodeURIComponent(orgId)}` : path
}

/* ------------------------------------------------------------------------ */
/* Roles                                                                     */
/* ------------------------------------------------------------------------ */

const PRINCIPAL_TYPES = [
  { value: 'user', label: 'User', description: 'One person.' },
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
  return <ManagedOrgPage what="roles">{(managed) => <RolesPage managed={managed} />}</ManagedOrgPage>
}

/**
 * The people and groups of an org, for turning a principal id into a name.
 *
 * Fetched once per org and looked up by id. Both lists are decorative here --
 * the assignments table works without them -- so a failure to load either
 * degrades to showing the raw id rather than taking the page down.
 */
function useOrgDirectory(orgId: Accessor<string>): {
  users: Accessor<UserSummary[]>
  groups: Accessor<GroupSummary[]>
  userName: (userId: string) => string | undefined
  groupName: (groupId: string) => string | undefined
} {
  const [users] = createResource(orgId, async (id) => {
    try {
      return (await api.listUsers({ orgId: id })).users
    } catch {
      return [] as UserSummary[]
    }
  })
  const [groups] = createResource(orgId, async (id) => {
    try {
      return (await api.listGroups({ orgId: id })).groups
    } catch {
      return [] as GroupSummary[]
    }
  })
  const userIndex = createMemo(() => new Map((users() ?? []).map((user) => [user.userId, user])))
  const groupIndex = createMemo(() => new Map((groups() ?? []).map((group) => [group.groupId, group])))

  return {
    users: () => users() ?? [],
    groups: () => groups() ?? [],
    userName: (userId) => {
      const user = userIndex().get(userId)
      return user ? userLabel(user) : undefined
    },
    groupName: (groupId) => groupIndex().get(groupId)?.name,
  }
}

function RolesPage(props: { managed: ManagedOrg }): JSX.Element {
  const orgId = props.managed.orgId
  const [assignments, { refetch }] = createResource(orgId, (scopeId) =>
    api.listRoleAssignments({ scopeType: 'org', scopeId }),
  )
  const directory = useOrgDirectory(orgId)

  const principalName = (type: string, id: string): string | undefined =>
    type === 'group' ? directory.groupName(id) : directory.userName(id)

  return (
    <div class="page">
      <div class="page-header">
        <div>
          <h1>Roles</h1>
          <p class="meta">
            Who can do what, and where. A role granted to a group reaches everyone in it.
          </p>
        </div>
        <div class="row" style={{ 'align-items': 'center' }}>
          <OrgHeaderPicker managed={props.managed} />
          <A href={orgHref('/org/groups', orgId())} class="btn btn-sm">
            Manage groups
          </A>
        </div>
      </div>

      <AssignRoleCard orgId={orgId()} groups={directory.groups()} onAssigned={() => void refetch()} />

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
                    <AssignmentRow
                      assignment={assignment}
                      principalName={principalName(assignment.principalType, assignment.principalId)}
                      onRevoked={() => void refetch()}
                    />
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

function AssignRoleCard(props: {
  orgId: string
  groups: GroupSummary[]
  onAssigned: () => void
}): JSX.Element {
  const [principalType, setPrincipalType] = createSignal('user')
  const [principalId, setPrincipalId] = createSignal('')
  const [scopeType, setScopeType] = createSignal('org')
  const [scopeId, setScopeId] = createSignal('')
  const [role, setRole] = createSignal('member')
  const action = useAction()

  const groupChoices = () =>
    props.groups.map((group) => ({
      value: group.groupId,
      label: group.name,
      description: group.description ?? '',
    }))

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
                onChange={(next) => {
                  // A user id is not a group id; a change of kind clears the choice.
                  setPrincipalType(next)
                  setPrincipalId('')
                }}
              />
            )}
          </Field>
          <Show
            when={principalType() === 'group'}
            fallback={
              <Field label="User" required hint="Search the accounts that have signed in.">
                {(ids) => (
                  <UserPicker
                    id={ids.id}
                    orgId={props.orgId}
                    value={principalId()}
                    describedBy={ids.describedBy}
                    onChange={setPrincipalId}
                  />
                )}
              </Field>
            }
          >
            <Field
              label="Group"
              required
              hint={props.groups.length ? 'A group from the Groups page.' : 'No groups yet. Create one on the Groups page.'}
            >
              {(ids) => (
                <ChoiceSelect
                  id={ids.id}
                  value={principalId()}
                  placeholder="Choose a group…"
                  choices={groupChoices()}
                  describedBy={ids.describedBy}
                  disabled={props.groups.length === 0}
                  onChange={setPrincipalId}
                />
              )}
            </Field>
          </Show>
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
  /** The principal's name, when the org directory knows it. */
  principalName: string | undefined
  onRevoked: () => void
}): JSX.Element {
  const action = useAction()

  return (
    <tr>
      <td>
        <div>
          <span class="meta">{props.assignment.principalType}</span>{' '}
          <Show when={props.principalName}>
            <span>{props.principalName}</span>
          </Show>
        </div>
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
