import { describe, expect, it } from 'vitest'
import { adminOrgIds, canManageSomeOrg } from './session.tsx'
import type { SessionSummary } from '~/api/auth.ts'

function sessionWith(overrides: Partial<SessionSummary>): SessionSummary {
  return {
    logged_in: true,
    is_global_admin: false,
    capabilities: {} as SessionSummary['capabilities'],
    roles: [],
    ...overrides,
  }
}

/**
 * The nav bar's management links hang off these two. The rule they replace
 * asked the coordinator about "the caller's own org", which for a person who
 * signed in through LinkKeys is an organization that owns nothing, and at that
 * scope everyone was an admin. So: roles, not a phantom scope.
 */
describe('adminOrgIds', () => {
  it('returns the orgs where the caller holds admin, once each', () => {
    const session = sessionWith({
      roles: [
        { scope_type: 'org', scope_id: 'org-a', role: 'admin' },
        { scope_type: 'org', scope_id: 'org-a', role: 'admin' },
        { scope_type: 'org', scope_id: 'org-b', role: 'member' },
        { scope_type: 'project', scope_id: 'proj-1', role: 'admin' },
        { scope_type: 'org', scope_id: 'org-c', role: 'admin' },
      ],
    })
    expect(adminOrgIds(session)).toEqual(['org-a', 'org-c'])
  })

  it('is empty for a signed-out visitor even if roles were somehow present', () => {
    const session = sessionWith({
      logged_in: false,
      roles: [{ scope_type: 'org', scope_id: 'org-a', role: 'admin' }],
    })
    expect(adminOrgIds(session)).toEqual([])
    expect(adminOrgIds(undefined)).toEqual([])
  })
})

describe('canManageSomeOrg', () => {
  it('is true for a global admin with no per-org rows', () => {
    expect(canManageSomeOrg(sessionWith({ is_global_admin: true }))).toBe(true)
  })

  it('is true for an org admin and false for a plain member', () => {
    expect(canManageSomeOrg(sessionWith({ roles: [{ scope_type: 'org', scope_id: 'o', role: 'admin' }] }))).toBe(true)
    expect(canManageSomeOrg(sessionWith({ roles: [{ scope_type: 'org', scope_id: 'o', role: 'member' }] }))).toBe(false)
    expect(canManageSomeOrg(sessionWith({ roles: [{ scope_type: 'project', scope_id: 'p', role: 'admin' }] }))).toBe(false)
  })

  it('is false while the session is still loading', () => {
    expect(canManageSomeOrg(undefined)).toBe(false)
  })
})
