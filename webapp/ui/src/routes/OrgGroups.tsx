import { For, Show, createResource, createSignal, type JSX } from 'solid-js'
import { A } from '@solidjs/router'
import { api } from '~/api/client.ts'
import { Field } from '~/components/Field.tsx'
import { UserPicker } from '~/components/UserPicker.tsx'
import { ActionError, useAction } from '~/components/ManagementPage.tsx'
import type { GroupSummary } from '~/api/csilapi/types.gen.ts'
import { ManagedOrgPage, OrgHeaderPicker, orgHref, type ManagedOrg } from './OrgRoles.tsx'

/**
 * Groups and their members.
 *
 * A group is how a role reaches more than one person: assign a role to a group
 * once, and membership does the rest. Roles themselves live on the Roles page.
 *
 * Scoped to the organization chosen in the header (`?org=`), which is a real
 * `organizations` row the caller administers. See `useManagedOrg` for why the
 * caller's own user id was never the right thing to put here.
 */
export function OrgGroups(): JSX.Element {
  return <ManagedOrgPage what="groups">{(managed) => <GroupsPage managed={managed} />}</ManagedOrgPage>
}

function GroupsPage(props: { managed: ManagedOrg }): JSX.Element {
  const orgId = props.managed.orgId
  const [groups, { refetch }] = createResource(orgId, (id) => api.listGroups({ orgId: id }))
  const [expanded, setExpanded] = createSignal<string>()

  return (
    <div class="page">
      <div class="page-header">
        <div>
          <h1>Groups</h1>
          <p class="meta">
            Collect people into a group, then grant that group a role once instead of granting
            each person separately.
          </p>
        </div>
        <div class="row" style={{ 'align-items': 'center' }}>
          <OrgHeaderPicker managed={props.managed} />
          <A href={orgHref('/org/roles', orgId())} class="btn btn-sm">
            Manage roles
          </A>
        </div>
      </div>

      <CreateGroupCard orgId={orgId()} onCreated={() => void refetch()} />

      <div class="card">
        <h3 class="section-title">Groups</h3>
        <Show
          when={(groups()?.groups.length ?? 0) > 0}
          fallback={<div class="empty">No groups yet.</div>}
        >
          <div class="scroll-x">
            <table class="table">
              <thead>
                <tr>
                  <th>Group</th>
                  <th>Description</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                <For each={groups()?.groups}>
                  {(group) => (
                    <GroupRow
                      group={group}
                      orgId={orgId()}
                      expanded={expanded() === group.groupId}
                      onToggle={() =>
                        setExpanded(expanded() === group.groupId ? undefined : group.groupId)
                      }
                      onChanged={() => void refetch()}
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

function CreateGroupCard(props: { orgId: string; onCreated: () => void }): JSX.Element {
  const [name, setName] = createSignal('')
  const [description, setDescription] = createSignal('')
  const action = useAction()

  return (
    <div class="card">
      <h3 class="section-title">New group</h3>
      <form
        onSubmit={async (event) => {
          event.preventDefault()
          if (!name().trim()) return
          const ok = await action.run('The group could not be created.', () =>
            api.createGroup({
              orgId: props.orgId,
              name: name().trim(),
              description: description().trim() || undefined,
            }),
          )
          if (ok) {
            setName('')
            setDescription('')
            props.onCreated()
          }
        }}
      >
        <ActionError error={action.error()} />
        <div class="form-row">
          <Field label="Name" required>
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
          {action.busy() ? 'Creating…' : 'Create group'}
        </button>
      </form>
    </div>
  )
}

function GroupRow(props: {
  group: GroupSummary
  orgId: string
  expanded: boolean
  onToggle: () => void
  onChanged: () => void
}): JSX.Element {
  const action = useAction()

  return (
    <>
      <tr>
        <td>
          <button type="button" class="btn btn-sm btn-ghost" onClick={props.onToggle}>
            <span aria-hidden="true">{props.expanded ? '▾' : '▸'}</span> {props.group.name}
          </button>
        </td>
        <td class="meta">{props.group.description || '—'}</td>
        <td>
          <button
            type="button"
            class="btn btn-sm btn-danger"
            disabled={action.busy()}
            onClick={async () => {
              if (
                !confirm(
                  `Delete group "${props.group.name}"? Any role granted to it stops applying to its members.`,
                )
              )
                return
              const ok = await action.run('The group could not be deleted.', () =>
                api.deleteGroup({ groupId: props.group.groupId }),
              )
              if (ok) props.onChanged()
            }}
          >
            Delete
          </button>
          <ActionError error={action.error()} />
        </td>
      </tr>
      <Show when={props.expanded}>
        <tr>
          <td colspan="3">
            <GroupMembers groupId={props.group.groupId} orgId={props.orgId} />
          </td>
        </tr>
      </Show>
    </>
  )
}

function GroupMembers(props: { groupId: string; orgId: string }): JSX.Element {
  const [members, { refetch }] = createResource(
    () => props.groupId,
    (groupId) => api.listGroupMembers({ groupId }),
  )
  const [userId, setUserId] = createSignal('')
  const action = useAction()

  return (
    <div class="stack">
      <ActionError error={action.error()} />
      <form
        class="stack"
        onSubmit={async (event) => {
          event.preventDefault()
          if (!userId().trim()) return
          const ok = await action.run('The member could not be added.', () =>
            api.addGroupMember({ groupId: props.groupId, userId: userId().trim() }),
          )
          if (ok) {
            setUserId('')
            void refetch()
          }
        }}
      >
        <Field label="Add a member" hint="Search the accounts that have signed in, then pick one.">
          {(ids) => (
            <div style={{ 'max-width': '28rem' }}>
              <UserPicker
                id={ids.id}
                orgId={props.orgId}
                value={userId()}
                describedBy={ids.describedBy}
                onChange={setUserId}
              />
            </div>
          )}
        </Field>
        <div class="row">
          <button type="submit" class="btn btn-sm btn-primary" disabled={action.busy() || !userId()}>
            Add member
          </button>
        </div>
      </form>

      <Show
        when={(members()?.members.length ?? 0) > 0}
        fallback={<p class="meta">This group has no members.</p>}
      >
        <table class="table">
          <thead>
            <tr>
              <th>Member</th>
              <th />
            </tr>
          </thead>
          <tbody>
            <For each={members()?.members}>
              {(member) => (
                <tr>
                  <td>
                    <div>{member.username}</div>
                    <div class="meta mono truncate">{member.userId}</div>
                  </td>
                  <td>
                    <button
                      type="button"
                      class="btn btn-sm"
                      disabled={action.busy()}
                      onClick={async () => {
                        await action.run('The member could not be removed.', () =>
                          api.removeGroupMember({ groupId: props.groupId, userId: member.userId }),
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
      </Show>
    </div>
  )
}
