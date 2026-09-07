import { createContext, createResource, useContext, type JSX, type Resource } from 'solid-js'
import { fetchAuthConfig, fetchSession, type AuthConfig, type SessionSummary } from '~/api/auth.ts'

/**
 * Who the browser is, and what the deployment offers.
 *
 * Everything here is a RENDERING HINT. The coordinator authorizes every
 * operation independently, so a client that lied about its capabilities would
 * only draw buttons that then fail. Nothing in this file is a security control,
 * and nothing downstream should treat it as one.
 */

interface SessionContextValue {
  session: Resource<SessionSummary>
  config: Resource<AuthConfig>
  refetch: () => void
}

const SessionContext = createContext<SessionContextValue>()

const ANONYMOUS: SessionSummary = {
  logged_in: false,
  is_global_admin: false,
  capabilities: {} as SessionSummary['capabilities'],
  roles: [],
}

export function SessionProvider(props: { children: JSX.Element }): JSX.Element {
  const [session, { refetch }] = createResource<SessionSummary>(async () => {
    try {
      return await fetchSession()
    } catch {
      // An unreachable server must not leave the shell blank. Rendering as
      // anonymous shows public data and a sign-in affordance, which is the
      // correct fallback and also what a logged-out visitor sees.
      return ANONYMOUS
    }
  })

  const [config] = createResource<AuthConfig>(async () => {
    try {
      return await fetchAuthConfig()
    } catch {
      return { auth_mode: 'none', login_enabled: false, bootstrap_available: false, has_global_admin: true }
    }
  })

  return (
    <SessionContext.Provider value={{ session, config, refetch: () => void refetch() }}>
      {props.children}
    </SessionContext.Provider>
  )
}

export function useSession(): SessionContextValue {
  const ctx = useContext(SessionContext)
  if (!ctx) throw new Error('useSession must be used inside a SessionProvider')
  return ctx
}

/**
 * A GLOBAL capability check for rendering.
 *
 * Returns false while the session is still loading, so a control never flashes
 * into view and then disappears. For anything that belongs to a project, use
 * `useCapabilities` from the store instead: the session's set is the global
 * scope, and a project owner or an org admin has nothing there.
 */
export function useCan(): (capability: keyof SessionSummary['capabilities']) => boolean {
  const { session } = useSession()
  return (capability) => Boolean(session()?.capabilities?.[capability])
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
