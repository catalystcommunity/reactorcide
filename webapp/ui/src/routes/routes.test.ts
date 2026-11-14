import { describe, expect, it } from 'vitest'
import { NAV } from '~/App.ts'
import { PAGES } from '~/app/page.ts'
import { STATIC_ROUTES, appPathOf, href, resolveRoute } from '~/ui/router.ts'

/**
 * Every navigable path must have a route.
 *
 * This exists because it already went wrong: the nav rendered /workers and
 * /admin links for exactly the people meant to have them, while the route table
 * defined neither, so both landed on the catch-all NotFound. A dead link shown
 * only to privileged users is the kind of thing that survives a long time,
 * because the person who built it never sees it.
 */
describe('route table', () => {
  it('resolves every path the navigation links to', () => {
    for (const entry of NAV) {
      expect(resolveRoute(entry.path).name, `navigation links to ${entry.path}`).not.toBe('notFound')
    }
  })

  it('resolves every path a page cross-links to', () => {
    for (const linked of ['/workers/classes', '/workers/queues', '/org/groups', '/projects/new', '/signin']) {
      expect(resolveRoute(linked).name, `a page links to ${linked}`).not.toBe('notFound')
    }
  })

  it('resolves detail routes and keeps fixed paths ahead of them', () => {
    expect(resolveRoute('/jobs/01ABC')).toEqual({ name: 'job', id: '01ABC' })
    expect(resolveRoute('/workflows/w-1')).toEqual({ name: 'workflow', id: 'w-1' })
    expect(resolveRoute('/projects/p-1')).toEqual({ name: 'project', id: 'p-1' })
    // "new" is a page, not a project id.
    expect(resolveRoute('/projects/new')).toEqual({ name: 'projectNew' })
    expect(resolveRoute('/projects/new/')).toEqual({ name: 'projectNew' })
  })

  it('sends unknown paths to NotFound', () => {
    expect(resolveRoute('/nope').name).toBe('notFound')
    expect(resolveRoute('/jobs/a/b').name).toBe('notFound')
    expect(resolveRoute('/workflows').name).toBe('notFound')
  })

  it('has a page module for every route name', () => {
    const names = new Set([...Object.values(STATIC_ROUTES), 'workflow', 'job', 'project', 'notFound'])
    for (const name of names) expect(PAGES, name).toHaveProperty(name)
  })

  it('maps between app paths and browser paths under /app', () => {
    expect(href('/')).toBe('/app/')
    expect(href('/jobs/x')).toBe('/app/jobs/x')
    expect(appPathOf('/app')).toBe('/')
    expect(appPathOf('/app/')).toBe('/')
    expect(appPathOf('/app/jobs/x/')).toBe('/jobs/x')
    expect(appPathOf('/other')).toBeNull()
  })
})
