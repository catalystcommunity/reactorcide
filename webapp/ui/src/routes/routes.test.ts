import { describe, expect, it } from 'vitest'
import { routes } from './index.tsx'

/**
 * Every navigable path must have a route.
 *
 * This exists because it already went wrong: the nav rendered /workers and
 * /admin links for exactly the people meant to have them, while the route table
 * defined neither, so both landed on the catch-all NotFound. A dead link shown
 * only to privileged users is the kind of thing that survives a long time,
 * because the person who built it never sees it.
 */

const paths = routes.map((route) => route.path)

describe('route table', () => {
  it('defines every path the navigation links to', () => {
    // Kept in step with App.tsx's <NavLink> list by hand. If a nav entry is
    // added there without a route here, this fails rather than shipping a 404.
    for (const navPath of ['/', '/jobs', '/projects', '/workers', '/org/roles', '/org/secrets', '/admin']) {
      expect(paths, `navigation links to ${navPath}`).toContain(navPath)
    }
  })

  it('defines every path a page cross-links to', () => {
    for (const linked of ['/workers/classes', '/workers/queues', '/org/groups', '/projects/new', '/signin']) {
      expect(paths, `a page links to ${linked}`).toContain(linked)
    }
  })

  it('keeps the catch-all last so it cannot shadow a real route', () => {
    expect(paths[paths.length - 1]).toBe('*')
    expect(paths.filter((p) => p === '*')).toHaveLength(1)
  })

  it('has no duplicate paths', () => {
    expect(new Set(paths).size).toBe(paths.length)
  })
})
