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

export interface SessionSummary {
  logged_in: boolean
  user_id?: string
  display_name?: string
  is_global_admin: boolean
  capabilities: GetCapabilitiesResponse
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
export function fetchSession(): Promise<SessionSummary> {
  return getJSON<SessionSummary>('/app/auth/session')
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

/**
 * Starts a login. The server answers with a redirect to the identity provider,
 * so this navigates rather than returning: a login is a top-level journey out
 * of the application and back, not a fetch.
 */
export async function beginLogin(identity: string): Promise<string | null> {
  const response = await postForm('/app/auth/login', { identity })
  if (response.redirected) {
    window.location.assign(response.url)
    return null
  }
  if (response.ok) {
    window.location.assign('/app/')
    return null
  }
  const body = await response.json().catch(() => ({ error: 'Sign-in failed.' }))
  return (body as { error?: string }).error ?? 'Sign-in failed.'
}

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
