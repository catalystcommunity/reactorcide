import { For, Show, createResource, createSignal, type JSX } from 'solid-js'
import { api } from '~/api/client.ts'
import type { UserSummary } from '~/api/csilapi/types.gen.ts'
import { describeError } from './ManagementPage.tsx'

/**
 * Choosing a person by name, not by pasting a UUID.
 *
 * Roles and group membership are keyed by user id, and until `list-users`
 * existed nothing in the UI showed one: an administrator had to read the id
 * out of the database. This asks the coordinator for the accounts it knows,
 * filtered as the administrator types, and hands back the chosen id.
 *
 * The list is authorized by the org the caller administers (`orgId`), which
 * is how the coordinator decides whether they may see accounts at all.
 */
export function UserPicker(props: {
  id?: string
  orgId: string
  value: string
  onChange: (userId: string) => void
  describedBy?: string
}): JSX.Element {
  const [query, setQuery] = createSignal('')
  const [users] = createResource(
    () => ({ orgId: props.orgId, query: query().trim() }),
    async (key) => {
      try {
        return { users: (await api.listUsers({ orgId: key.orgId, query: key.query || undefined })).users, error: undefined }
      } catch (cause) {
        return { users: [] as UserSummary[], error: describeError(cause, 'The user list could not be loaded.') }
      }
    },
  )

  return (
    <div class="user-picker">
      <input
        id={props.id}
        class="input"
        type="search"
        placeholder="Search by name or sign-in"
        aria-describedby={props.describedBy}
        value={query()}
        onInput={(event) => setQuery(event.currentTarget.value)}
      />
      <Show when={users()?.error}>
        <div class="field-error" role="alert">
          {users()?.error}
        </div>
      </Show>
      <Show
        when={(users()?.users.length ?? 0) > 0}
        fallback={
          <Show when={users() && !users()?.error}>
            <div class="field-hint">No matching accounts. A person appears here after their first sign-in.</div>
          </Show>
        }
      >
        <div class="user-picker-list" role="listbox" aria-label="Accounts">
          <For each={users()?.users}>
            {(user) => (
              <button
                type="button"
                role="option"
                aria-selected={props.value === user.userId}
                class={`user-picker-option${props.value === user.userId ? ' user-picker-option-selected' : ''}`}
                onClick={() => props.onChange(user.userId)}
              >
                <span>{userLabel(user)}</span>
                <Show when={user.subject}>
                  <span class="meta mono">{user.subject}</span>
                </Show>
              </button>
            )}
          </For>
        </div>
      </Show>
    </div>
  )
}

export function userLabel(user: UserSummary): string {
  return user.displayName || user.username || user.userId
}
