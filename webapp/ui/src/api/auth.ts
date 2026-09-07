import type { GetCapabilitiesResponse } from './csilapi/types.gen.ts'

/**
 * The auth endpoints that are NOT CSIL calls.
 *
 * Each of these has a Set-Cookie side effect. A CSIL operation returns a typed
 * value and cannot set a header, and a session token must never reach page
 * JavaScript, so the session cookie is minted and cleared server-side and this
 * module only ever triggers those endpoints.
 *
 * Nothing here returns a token. If you find yourself wanting one, the design
 * has gone wrong.
 */

export interface AuthConfig {
  auth_mode: string
  login_enabled: boolean
  bootstrap_available: boolean
  has_global_admin: boolean
}

/** One role the signed-in caller holds: `admin` on an org, `member` on a project, ... */
export interface SessionRole {
  scope_type: string
  scope_id?: string
  role: string
}

export interface SessionSummary {
  logged_in: boolean
  user_id?: string
  display_name?: string
  is_global_admin: boolean
  /**
   * The GLOBAL-scope capability set. It is all-false for everyone but a global
   * admin (and, in auth mode `none`, carries the anonymous cancel/retry grant).
   * Org- and project-level questions are answered by `roles` and by a scoped
   * `get-capabilities` call (see `useCapabilities` in the store), not by this.
   */
  capabilities: GetCapabilitiesResponse
  roles: SessionRole[]
}

async function getJSON<T>(path: string): Promise<T> {
  const response = await fetch(path, {
    credentials: 'same-origin',
    headers: { Accept: 'application/json' },
  })
  if (!response.ok) throw new Error(`${path}: ${response.status}`)
  return (await response.json()) as T
}

export function fetchAuthConfig(): Promise<AuthConfig> {
  return getJSON<AuthConfig>('/app/auth/config')
}

/**
 * Who the browser is, as far as the server is concerned.
 *
 * Every field is a HINT for rendering. The coordinator re-authorizes every
 * operation, so a client that lied to itself here would only draw the wrong
 * buttons — it could not do anything it is not allowed to do.
 */
export async function fetchSession(): Promise<SessionSummary> {
  return decodeSession(await getJSON<unknown>('/app/auth/session'))
}

/**
 * Turns the webapp's session JSON into a SessionSummary.
 *
 * The capabilities object is the coordinator's Go client type, serialized with
 * its snake_case json tags (`cancel_job`, `manage_groups`). The generated
 * TypeScript type for the same shape is camelCase (`cancelJob`). For a while
 * the SPA read the camelCase names off the snake_case object, so every
 * capability check was undefined, every management button was hidden for
 * everybody including global admins, and nothing failed loudly. The conversion
 * lives here, in one place, with a test that feeds it the exact key set the Go
 * side emits.
 */
export function decodeSession(raw: unknown): SessionSummary {
  const body = (raw ?? {}) as Record<string, unknown>
  const capabilities = camelizeKeys(body.capabilities) as unknown as GetCapabilitiesResponse
  const roles = Array.isArray(body.roles)
    ? (body.roles as Record<string, unknown>[]).map((role) => ({
        scope_type: String(role.scope_type ?? ''),
        scope_id: typeof role.scope_id === 'string' && role.scope_id !== '' ? role.scope_id : undefined,
        role: String(role.role ?? ''),
      }))
    : []
  return {
    logged_in: body.logged_in === true,
    user_id: typeof body.user_id === 'string' ? body.user_id : undefined,
    display_name: typeof body.display_name === 'string' ? body.display_name : undefined,
    is_global_admin: body.is_global_admin === true,
    capabilities,
    roles,
  }
}

/** `manage_webhook_secrets` -> `manageWebhookSecrets`. Non-objects become `{}`. */
export function camelizeKeys(value: unknown): Record<string, unknown> {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return {}
  const out: Record<string, unknown> = {}
  for (const [key, entry] of Object.entries(value as Record<string, unknown>)) {
    out[key.replace(/_([a-z0-9])/g, (_, ch: string) => ch.toUpperCase())] = entry
  }
  return out
}

async function postForm(path: string, fields: Record<string, string>): Promise<Response> {
  const body = new URLSearchParams(fields)
  return fetch(path, {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    body,
  })
}

// There is deliberately no beginLogin here. The sign-in form in
// routes/SignIn.tsx posts natively to /app/auth/login, because the answer is a
// 302 to the identity provider and only a top-level navigation can follow it
// off this origin. A fetch would follow the redirect itself, hit the
// provider's page without CORS headers, and reject.

export async function logout(): Promise<void> {
  await postForm('/app/auth/logout', {})
  // A full navigation rather than a client-side route change: the whole point
  // is to discard every scrap of state that belonged to the old session,
  // including anything the store still holds.
  window.location.assign('/app/')
}

/** Redeems the one-time bootstrap token. Returns an error message, or null. */
export async function bootstrapAdmin(token: string): Promise<string | null> {
  const response = await postForm('/app/auth/bootstrap', { token })
  if (response.ok || response.redirected) {
    window.location.assign('/app/')
    return null
  }
  const body = await response.json().catch(() => ({ error: 'Bootstrap failed.' }))
  return (body as { error?: string }).error ?? 'Bootstrap failed.'
}
