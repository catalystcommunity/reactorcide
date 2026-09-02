import { Show, createSignal, onCleanup, onMount, type JSX } from 'solid-js'
import { A, useLocation } from '@solidjs/router'
import { SessionProvider, useSession } from '~/lib/session.tsx'
import { theme, setTheme, nextTheme, themeGlyph, themeLabel, useThemeBootstrap } from '~/lib/theme.ts'
import { logout } from '~/api/auth.ts'
import { eventSocket } from '~/store/events.ts'
import './app.css'

/** The application shell: header, navigation, and the live-connection state. */
export function App(props: { children?: JSX.Element }): JSX.Element {
  useThemeBootstrap()
  return (
    <SessionProvider>
      <Shell>{props.children}</Shell>
    </SessionProvider>
  )
}

function Shell(props: { children?: JSX.Element }): JSX.Element {
  const { session, config } = useSession()
  const location = useLocation()

  return (
    <>
      {/* The first thing a keyboard user reaches, before the whole nav. */}
      <a class="skip-link" href="#main">
        Skip to content
      </a>

      <header class="app-header">
        <div class="app-header-inner">
          <A href="/" class="brand" aria-label="Reactorcide home">
            <FlameMark />
            <span class="brand-name">Reactorcide</span>
          </A>

          <nav class="app-nav" aria-label="Main">
            <NavLink href="/" label="Workflows" active={location.pathname === '/app' || location.pathname === '/app/'} />
            <NavLink href="/jobs" label="Jobs" />
            <NavLink href="/projects" label="Projects" />
            <Show when={session()?.capabilities?.manageWorkers}>
              <NavLink href="/workers" label="Workers" />
            </Show>
            <Show when={session()?.capabilities?.manageGroups}>
              <NavLink href="/org/roles" label="Access" />
            </Show>
            <Show when={session()?.capabilities?.manageSecrets}>
              <NavLink href="/org/secrets" label="Secrets" />
            </Show>
            <Show when={session()?.is_global_admin}>
              <NavLink href="/admin" label="Admin" />
            </Show>
          </nav>

          <div class="app-header-actions">
            <ConnectionIndicator />
            <ThemeToggle />
            <Show
              when={session()?.logged_in}
              fallback={
                <Show when={config()?.login_enabled}>
                  <A href="/signin" class="btn btn-sm">
                    Sign in
                  </A>
                </Show>
              }
            >
              <span class="app-user" title={session()?.user_id}>
                {session()?.display_name}
              </span>
              <button type="button" class="btn btn-sm btn-ghost" onClick={() => void logout()}>
                Sign out
              </button>
            </Show>
          </div>
        </div>
      </header>

      <main id="main">{props.children}</main>
    </>
  )
}

function NavLink(props: { href: string; label: string; active?: boolean }): JSX.Element {
  return (
    <A
      href={props.href}
      class="app-nav-link"
      activeClass="app-nav-link-active"
      end={props.href === '/'}
    >
      {props.label}
    </A>
  )
}

/**
 * Whether live updates are flowing.
 *
 * Worth showing: when the socket is down the page still works but stops
 * updating on its own, and an operator staring at a stale status deserves to
 * know that is what they are looking at rather than assuming nothing changed.
 */
function ConnectionIndicator(): JSX.Element {
  const [connected, setConnected] = createSignal(false)
  onMount(() => {
    const remove = eventSocket.onStatusChange(setConnected)
    onCleanup(remove)
  })

  return (
    <span
      class={`conn ${connected() ? 'conn-live' : 'conn-down'}`}
      title={connected() ? 'Receiving live updates' : 'Live updates are not connected; data refreshes on a timer'}
      role="status"
    >
      <span class="conn-dot" aria-hidden="true" />
      <span class="sr-only">{connected() ? 'Live' : 'Reconnecting'}</span>
    </span>
  )
}

function ThemeToggle(): JSX.Element {
  return (
    <button
      type="button"
      class="btn btn-sm btn-ghost"
      onClick={() => setTheme(nextTheme(theme()))}
      // The label states the CURRENT theme and what pressing does, because a
      // three-state toggle is not guessable from an icon alone.
      aria-label={`Theme: ${themeLabel(theme())}. Activate to change.`}
      title={`Theme: ${themeLabel(theme())}`}
    >
      <span aria-hidden="true">{themeGlyph(theme())}</span>
    </button>
  )
}

/** The atomic flame, inline so it inherits the header's colour. */
function FlameMark(): JSX.Element {
  return (
    <svg viewBox="0 0 64 64" width="24" height="24" aria-hidden="true" class="brand-mark">
      <defs>
        <linearGradient id="brand-flame" x1="32" y1="4" x2="32" y2="54" gradientUnits="userSpaceOnUse">
          <stop offset="0" stop-color="#ffd166" />
          <stop offset="0.45" stop-color="#f7842b" />
          <stop offset="1" stop-color="#e0322c" />
        </linearGradient>
      </defs>
      <g fill="none" stroke="currentColor" stroke-width="2.5" opacity="0.5">
        <ellipse cx="32" cy="32" rx="29" ry="10.5" />
        <ellipse cx="32" cy="32" rx="29" ry="10.5" transform="rotate(60 32 32)" />
        <ellipse cx="32" cy="32" rx="29" ry="10.5" transform="rotate(-60 32 32)" />
      </g>
      <path
        fill="url(#brand-flame)"
        d="M32 4c6.6 11.2 16 16.6 16 28.6C48 44 41 52.4 32 54c-9-1.6-16-10-16-21.4 0-7.4 3.6-11.4 7-15.6 1.1 5.9 3.6 8.9 5.7 10.3C26.4 17.6 28.4 10.2 32 4Z"
      />
    </svg>
  )
}
