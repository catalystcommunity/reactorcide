import { For, Show, createResource, createSignal, type JSX } from 'solid-js'
import { A } from '@solidjs/router'
import { api } from '~/api/client.ts'
import { useSession } from '~/lib/session.tsx'
import { Field } from '~/components/Field.tsx'
import { ActionError, NotPermitted, useAction } from '~/components/ManagementPage.tsx'
import type { GroupSummary } from '~/api/csilapi/types.gen.ts'

/**
 * Groups and their members.
 *
 * A group is how a role reaches more than one person: assign a role to a group
 * once, and membership does the rest. Roles themselves live on the Roles page.
 *
 * Scoped to the caller's own organization. `user_id` IS the org id throughout
 * this system, so a logged-in caller's own user id is exactly their org.
 */
export function OrgGroups(): JSX.Element {
  const { session } = useSession()
  return (
    <Show when={session()?.capabilities?.manageGroups} fallback={<NotPermitted what="groups" />}>
      <GroupsPage orgId={session()!.user_id!} />
    </Show>
  )
}

function GroupsPage(props: { orgId: string }): JSX.Element {
  const [groups, { refetch }] = createResource(
    () => props.orgId,
    (orgId) => api.listGroups({ orgId }),
  )
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
        <A href="/org/roles" class="btn btn-sm">
          Manage roles
        </A>
      </div>

      <CreateGroupCard orgId={props.orgId} onCreated={() => void refetch()} />

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
            <GroupMembers groupId={props.group.groupId} />
          </td>
        </tr>
      </Show>
    </>
  )
}

function GroupMembers(props: { groupId: string }): JSX.Element {
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
        class="row"
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
        <input
          class="input"
          style={{ 'max-width': '22rem' }}
          type="text"
          placeholder="User ID"
          aria-label="User ID to add"
          value={userId()}
          onInput={(event) => setUserId(event.currentTarget.value)}
        />
        <button type="submit" class="btn btn-sm btn-primary" disabled={action.busy()}>
          Add member
        </button>
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
