import { fetchAuthConfig, fetchSession, type AuthConfig, type SessionSummary } from '~/api/auth.ts'
import { dataStore } from '~/ui/store.ts'

/**
 * Who the browser is, and what the deployment offers.
 *
 * Everything here is a RENDERING HINT. The coordinator authorizes every
 * operation independently, so a client that lied about its capabilities would
 * only draw buttons that then fail. Nothing in this file is a security control,
 * and nothing downstream should treat it as one.
 *
 * Both values live in the store's `session` collection. An element that shows
 * anything session-dependent lists `SESSION` in its `static watches`. Both are
 * `undefined` until the first load resolves.
 */

export const SESSION = 'session'

const ANONYMOUS: SessionSummary = {
  logged_in: false,
  is_global_admin: false,
  capabilities: {} as SessionSummary['capabilities'],
  roles: [],
}

const NO_LOGIN: AuthConfig = { auth_mode: 'none', login_enabled: false, bootstrap_available: false, has_global_admin: true }

export function session(): SessionSummary | undefined {
  return dataStore.get(SESSION, 'session') as SessionSummary | undefined
}

export function authConfig(): AuthConfig | undefined {
  return dataStore.get(SESSION, 'config') as AuthConfig | undefined
}

/** Sets the session directly. For tests, and for nothing else. */
export function setSessionForTest(value: SessionSummary | undefined, config?: AuthConfig): void {
  dataStore.set(SESSION, 'session', value)
  if (config) dataStore.set(SESSION, 'config', config)
}

/** Loads (or reloads) the session. */
export async function refetchSession(): Promise<void> {
  let next: SessionSummary
  try {
    next = await fetchSession()
  } catch {
    // An unreachable server must not leave the shell blank. Rendering as
    // anonymous shows public data and a sign-in affordance, which is the
    // correct fallback and also what a logged-out visitor sees.
    next = ANONYMOUS
  }
  dataStore.set(SESSION, 'session', next)
}

/** Loads the session and the deployment's login config. Called once at boot. */
export async function loadSession(): Promise<void> {
  const config = fetchAuthConfig().catch(() => NO_LOGIN)
  await refetchSession()
  dataStore.set(SESSION, 'config', await config)
}

/**
 * A GLOBAL capability check for rendering.
 *
 * Returns false while the session is still loading, so a control never flashes
 * into view and then disappears. For anything that belongs to a project, use
 * `useCapabilities` from the store instead: the session's set is the global
 * scope, and a project owner or an org admin has nothing there.
 */
export function can(capability: keyof SessionSummary['capabilities']): boolean {
  return Boolean(session()?.capabilities?.[capability])
}

/**
 * The organizations the caller administers, by id.
 *
 * Read off the session's roles rather than asked of the coordinator per org:
 * the nav bar needs "does this person administer anything?" on every page,
 * and the answer is already in the identity the login produced. A global
 * admin administers every org but holds no per-org row, so callers must check
 * `is_global_admin` separately (see `canManageSomeOrg`).
 */
export function adminOrgIds(session: SessionSummary | undefined): string[] {
  if (!session?.logged_in) return []
  const ids = new Set<string>()
  for (const role of session.roles) {
    if (role.scope_type === 'org' && role.role === 'admin' && role.scope_id) ids.add(role.scope_id)
  }
  return [...ids]
}

/**
 * Whether any org-management page is worth showing at all.
 *
 * This decides the Workers, Access and Secrets nav links. The old rule asked
 * the coordinator for the caller's capabilities in "their own org", meaning
 * an org whose id is the caller's user id. Projects live in real organization
 * rows with their own ids, so that scope described an org nobody uses, and at
 * that scope every signed-in person counted as its admin. The links would have
 * shown for everyone and led to pages managing nothing.
 */
export function canManageSomeOrg(session: SessionSummary | undefined): boolean {
  if (!session?.logged_in) return false
  return session.is_global_admin || adminOrgIds(session).length > 0
}
