/**
 * Routes over the Zest SimpleRouter.
 *
 * The SPA is served under /app/ by the Go binary. Pages and links use APP
 * paths (`/jobs/01ABC`); `href()` adds the prefix. The current route goes into
 * the store (`route` / `current`), and the shell shows the page for it.
 *
 * Matching is a pure function, `resolveRoute`, so the route table is testable
 * without a browser: routes.test.ts asserts every path the navigation and the
 * pages link to resolves to a real page. That test exists because the first
 * cut of the SPA linked /workers and /admin while defining neither.
 */
import SimpleRouter from '@catalystcommunity/simple-router'
import { cell } from './store.ts'

export const BASE = '/app'

export type RouteName =
  | 'workflows'
  | 'workflow'
  | 'event'
  | 'jobs'
  | 'job'
  | 'projects'
  | 'projectNew'
  | 'project'
  | 'workers'
  | 'workerClasses'
  | 'queues'
  | 'orgGroups'
  | 'orgRoles'
  | 'orgSecrets'
  | 'admin'
  | 'signin'
  | 'notFound'

export interface Route {
  name: RouteName
  /** The app path, without the /app prefix. Always starts with '/'. */
  path: string
  /** The id segment of a detail route (`/jobs/:id`). */
  id?: string
  query: URLSearchParams
}

/** Fixed paths. A detail route is `<prefix>/:id` and is handled in resolveRoute. */
export const STATIC_ROUTES: Readonly<Record<string, RouteName>> = {
  '/': 'workflows',
  '/jobs': 'jobs',
  '/projects': 'projects',
  '/projects/new': 'projectNew',
  '/workers': 'workers',
  '/workers/classes': 'workerClasses',
  '/workers/queues': 'queues',
  '/org/groups': 'orgGroups',
  '/org/roles': 'orgRoles',
  '/org/secrets': 'orgSecrets',
  '/admin': 'admin',
  '/signin': 'signin',
}

const DETAIL_ROUTES: Readonly<Record<string, RouteName>> = {
  '/workflows': 'workflow',
  '/events': 'event',
  '/jobs': 'job',
  '/projects': 'project',
}

function normalize(path: string): string {
  let p = path.startsWith('/') ? path : `/${path}`
  if (p.length > 1 && p.endsWith('/')) p = p.slice(0, -1)
  return p
}

/** The app path of a browser pathname, or null when it is outside /app. */
export function appPathOf(pathname: string): string | null {
  if (pathname === BASE) return '/'
  if (!pathname.startsWith(`${BASE}/`)) return null
  return normalize(pathname.slice(BASE.length))
}

/** Which page an app path shows. */
export function resolveRoute(appPath: string): { name: RouteName; id?: string } {
  const path = normalize(appPath)
  const fixed = STATIC_ROUTES[path]
  if (fixed) return { name: fixed }
  const cut = path.lastIndexOf('/')
  const prefix = path.slice(0, cut)
  const id = path.slice(cut + 1)
  const detail = DETAIL_ROUTES[prefix]
  if (detail && id) return { name: detail, id: decodeURIComponent(id) }
  return { name: 'notFound' }
}

/** The browser path of an app path. */
export function href(appPath: string): string {
  const path = normalize(appPath)
  return path === '/' ? `${BASE}/` : `${BASE}${path}`
}

/** The store collection of this module. */
export const ROUTE = 'route'

function routeOfLocation(): Route {
  const appPath = appPathOf(location.pathname) ?? '/'
  const resolved = resolveRoute(appPath)
  return { ...resolved, path: appPath, query: new URLSearchParams(location.search) }
}

const routeCell = cell<Route>(ROUTE, 'current', { name: 'workflows', path: '/', query: new URLSearchParams() })

export function currentRoute(): Route {
  return routeCell.get()
}

let router: SimpleRouter | null = null

function publish(): void {
  const next = routeOfLocation()
  const prev = routeCell.get()
  // Same URL, same route: send nothing, so a page is not rendered for nothing.
  if (prev.path === next.path && prev.query.toString() === next.query.toString() && prev.name === next.name) return
  routeCell.set(next)
}

/** Registers the routes, follows the browser history, and routes the current URL. */
export function startRouter(): void {
  router?.cleanup()
  const r = new SimpleRouter()
  // The store tells the shell; no document event is needed.
  r.eventSender = () => {}
  // One route for the whole app. resolveRoute does the matching, so the
  // route table has one source.
  r.registerRoute(BASE, publish)
  r.registerRoute('/notfound', publish, [], true)
  router = r
  // Seed the cell directly: the first route must be in place even when the
  // current value already compares equal.
  routeCell.set(routeOfLocation())
}

/** For tests: route the current location again. */
export function syncRouteFromLocation(): void {
  routeCell.set(routeOfLocation())
}

/** Goes to an app path. `replace` changes the current history entry. */
export function navigate(appPath: string, options: { replace?: boolean } = {}): void {
  const [pathPart, search = ''] = appPath.split('?')
  const target = href(pathPart) + (search ? `?${search}` : '')
  if (target === location.pathname + location.search) return
  if (options.replace) {
    history.replaceState({}, '', target)
    publish()
    return
  }
  if (!router) {
    history.pushState({}, '', target)
    publish()
    return
  }
  // changePage pushes the entry and routes it, which calls publish().
  router.changePage(target)
}

/**
 * Changes query parameters of the current route. An undefined or empty value
 * removes the parameter. Replaces the history entry, so a filter that is
 * changed ten times does not leave ten entries behind the back button.
 */
export function setQuery(params: Record<string, string | undefined>): void {
  const query = new URLSearchParams(location.search)
  for (const [key, value] of Object.entries(params)) {
    if (value) query.set(key, value)
    else query.delete(key)
  }
  const search = query.toString()
  const target = location.pathname + (search ? `?${search}` : '')
  if (target === location.pathname + location.search) return
  history.replaceState({}, '', target)
  publish()
}

/** Paths under /app that are server endpoints, not pages. A link to one is a real navigation. */
const SERVER_PATHS = ['/auth', '/rpc', '/ws']

function isPagePath(url: URL): boolean {
  if (url.origin !== location.origin) return false
  const appPath = appPathOf(url.pathname)
  if (appPath === null) return false
  return !SERVER_PATHS.some((p) => appPath === p || appPath.startsWith(`${p}/`))
}

/** Routes clicks on same-origin page links in the document, as a SPA link does. */
export function routeLinks(root: EventTarget = document): () => void {
  const onClick = (e: Event): void => {
    const ev = e as MouseEvent
    if (ev.defaultPrevented || ev.button !== 0 || ev.metaKey || ev.ctrlKey || ev.shiftKey || ev.altKey) return
    const start = ev.composedPath()[0] as Element | null
    const a = start?.closest?.('a[href]') as HTMLAnchorElement | SVGAElement | null
    if (!a) return
    const rawHref = a instanceof SVGAElement ? a.href.baseVal : a.href
    const target = a.getAttribute('target')
    if ((target && target !== '_self') || a.hasAttribute('download')) return
    const url = new URL(rawHref, location.href)
    if (!isPagePath(url)) return
    ev.preventDefault()
    const appPath = appPathOf(url.pathname) ?? '/'
    navigate(appPath + url.search)
  }
  root.addEventListener('click', onClick)
  return () => root.removeEventListener('click', onClick)
}
