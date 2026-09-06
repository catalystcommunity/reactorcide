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
 * A capability check for rendering.
 *
 * Returns false while the session is still loading, so a control never flashes
 * into view and then disappears.
 */
export function useCan(): (capability: keyof SessionSummary['capabilities']) => boolean {
  const { session } = useSession()
  return (capability) => Boolean(session()?.capabilities?.[capability])
}
