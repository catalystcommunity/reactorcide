import type { PushButton } from '@catalystcommunity/controls'
import { logout } from '~/api/auth.ts'
import { PAGES, type RcPage } from '~/app/page.ts'
import { SESSION, authConfig, canManageSomeOrg, session } from '~/lib/session.ts'
import { UI, nextTheme, setTheme, theme, themeGlyph, themeLabel } from '~/lib/theme.ts'
import { eventSocket } from '~/store/events.ts'
import { button } from '~/ui/controls.ts'
import { h, s, setAttr, setClass, setText, show } from '~/ui/dom.ts'
import { RcElement, define } from '~/ui/element.ts'
import { ROUTE, currentRoute, href, type Route } from '~/ui/router.ts'
import './app.css'

interface NavEntry {
  path: string
  label: string
  /** Whether the entry shows. Rendering hints only; the coordinator authorizes every call. */
  visible: () => boolean
}

/**
 * The navigation. routes.test.ts asserts every path here resolves to a page,
 * so add an entry there too when you add one here.
 */
export const NAV: readonly NavEntry[] = [
  { path: '/', label: 'Workflows', visible: () => true },
  { path: '/jobs', label: 'Jobs', visible: () => true },
  { path: '/projects', label: 'Projects', visible: () => true },
  // Management links show for anyone who administers SOME organization. The
  // session's capability set is the global scope, which is all-false for an
  // org admin, so it cannot drive these; the roles on the identity can. Each
  // page then picks the org.
  { path: '/org/roles', label: 'Access', visible: () => canManageSomeOrg(session()) },
  { path: '/org/secrets', label: 'Secrets', visible: () => canManageSomeOrg(session()) },
  // Pools, workers and queues are instance-wide and the coordinator requires
  // a global admin for them, so an org admin would land on a refusal. Worker
  // classes are org-scoped, so an org admin gets that link instead.
  { path: '/workers', label: 'Workers', visible: () => Boolean(session()?.is_global_admin) },
  {
    path: '/workers/classes',
    label: 'Worker classes',
    visible: () => !session()?.is_global_admin && canManageSomeOrg(session()),
  },
  { path: '/admin', label: 'Admin', visible: () => Boolean(session()?.is_global_admin) },
]

function isActive(entry: string, route: Route): boolean {
  if (entry === '/') return route.path === '/'
  return route.path === entry || route.path.startsWith(`${entry}/`)
}

/** The application shell: header, navigation, the live-connection state, and the routed page. */
export class RcApp extends RcElement {
  static watches = [SESSION, UI, ROUTE]

  #nav: { entry: NavEntry; link: HTMLAnchorElement }[] = []
  #connection!: HTMLSpanElement
  #connectionText!: HTMLSpanElement
  #theme!: PushButton
  #signIn!: HTMLAnchorElement
  #user!: HTMLSpanElement
  #signOut!: PushButton
  #main!: HTMLElement
  #page: RcPage | null = null
  #pageRoute: Route | null = null
  #request = 0

  protected build(): void {
    const nav = h('nav', { class: 'app-nav', 'aria-label': 'Main' })
    for (const entry of NAV) {
      const link = h('a', { href: href(entry.path), class: 'app-nav-link' }, entry.label)
      this.#nav.push({ entry, link })
      nav.append(link)
    }

    this.#connectionText = h('span', { class: 'sr-only' })
    this.#connection = h('span', { class: 'conn conn-down', role: 'status' }, h('span', { class: 'conn-dot', 'aria-hidden': 'true' }), this.#connectionText)

    this.#theme = button('', { small: true, variant: 'flat', onClick: () => setTheme(nextTheme(theme())) })
    this.#theme.classList.add('theme-toggle')
    this.#signIn = h('a', { href: href('/signin'), class: 'btn btn-sm' }, 'Sign in')
    this.#user = h('span', { class: 'app-user' })
    this.#signOut = button('Sign out', { small: true, variant: 'flat', onClick: () => void logout() })

    this.#main = h('main', { id: 'main', tabindex: '-1' })

    this.append(
      // The first thing a keyboard user reaches, before the whole nav.
      h('a', { class: 'skip-link', href: '#main', onClick: (e: Event) => this.#skip(e) }, 'Skip to content'),
      h(
        'header',
        { class: 'app-header' },
        h(
          'div',
          { class: 'app-header-inner' },
          h('a', { href: href('/'), class: 'brand', 'aria-label': 'Reactorcide home' }, flameMark(), h('span', { class: 'brand-name' }, 'Reactorcide')),
          nav,
          h('div', { class: 'app-header-actions' }, this.#connection, this.#theme, this.#signIn, this.#user, this.#signOut),
        ),
      ),
      this.#main,
    )
  }

  protected connected(): void {
    // Whether live updates are flowing. When the socket is down the page still
    // works but stops updating on its own, and an operator staring at a stale
    // status deserves to know that is what they are looking at.
    this.own(eventSocket.onStatusChange((connected) => this.#setConnected(connected)))
  }

  /**
   * A skip link is a fragment navigation, and a SPA can lose the focus on it.
   * Move the focus to main directly.
   */
  #skip(event: Event): void {
    event.preventDefault()
    this.#main.focus()
  }

  #setConnected(connected: boolean): void {
    setAttr(this.#connection, 'class', `conn ${connected ? 'conn-live' : 'conn-down'}`)
    setAttr(this.#connection, 'title', connected ? 'Receiving live updates' : 'Live updates are not connected; data refreshes on a timer')
    setText(this.#connectionText, connected ? 'Live' : 'Reconnecting')
  }

  render(): void {
    const route = currentRoute()
    const current = session()

    for (const { entry, link } of this.#nav) {
      show(link, entry.visible())
      const active = isActive(entry.path, route)
      setClass(link, 'app-nav-link-active', active)
      setAttr(link, 'aria-current', active ? 'page' : null)
    }

    const t = theme()
    // The label states the CURRENT theme and what pressing does, because a
    // three-state toggle is not guessable from an icon alone.
    setAttr(this.#theme, 'aria-label', `Theme: ${themeLabel(t)}. Activate to change.`)
    setAttr(this.#theme, 'title', `Theme: ${themeLabel(t)}`)
    setText(this.#theme, themeGlyph(t))

    const loggedIn = Boolean(current?.logged_in)
    show(this.#signIn, !loggedIn && Boolean(authConfig()?.login_enabled))
    show(this.#user, loggedIn)
    show(this.#signOut, loggedIn)
    setText(this.#user, current?.display_name ?? '')
    setAttr(this.#user, 'title', current?.user_id ?? null)

    void this.#showRoute(route)
  }

  async #showRoute(route: Route): Promise<void> {
    const page = this.#page
    if (page && this.#pageRoute && page.acceptRoute(route, this.#pageRoute)) {
      this.#pageRoute = route
      return
    }
    if (this.#pageRoute === route) return
    this.#pageRoute = route
    const request = ++this.#request
    const tag = await PAGES[route.name]()
    if (request !== this.#request || !this.isActive()) return
    const next = document.createElement(tag) as RcPage
    next.routeName = route.name
    this.#main.replaceChildren(next)
    this.#page = next
    window.scrollTo?.(0, 0)
  }
}
define('rc-app', RcApp)

/** The atomic flame, inline so it inherits the header's colour. */
function flameMark(): SVGElement {
  return s(
    'svg',
    { viewBox: '0 0 64 64', width: '24', height: '24', 'aria-hidden': 'true', class: 'brand-mark' },
    s(
      'defs',
      null,
      s(
        'linearGradient',
        { id: 'brand-flame', x1: '32', y1: '4', x2: '32', y2: '54', gradientUnits: 'userSpaceOnUse' },
        s('stop', { offset: '0', 'stop-color': '#ffd166' }),
        s('stop', { offset: '0.45', 'stop-color': '#f7842b' }),
        s('stop', { offset: '1', 'stop-color': '#e0322c' }),
      ),
    ),
    s(
      'g',
      { fill: 'none', stroke: 'currentColor', 'stroke-width': '2.5', opacity: '0.5' },
      s('ellipse', { cx: '32', cy: '32', rx: '29', ry: '10.5' }),
      s('ellipse', { cx: '32', cy: '32', rx: '29', ry: '10.5', transform: 'rotate(60 32 32)' }),
      s('ellipse', { cx: '32', cy: '32', rx: '29', ry: '10.5', transform: 'rotate(-60 32 32)' }),
    ),
    s('path', {
      fill: 'url(#brand-flame)',
      d: 'M32 4c6.6 11.2 16 16.6 16 28.6C48 44 41 52.4 32 54c-9-1.6-16-10-16-21.4 0-7.4 3.6-11.4 7-15.6 1.1 5.9 3.6 8.9 5.7 10.3C26.4 17.6 28.4 10.2 32 4Z',
    }),
  )
}
