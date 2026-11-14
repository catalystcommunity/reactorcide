import { beforeEach, describe, expect, it, vi } from 'vitest'
import { waitFor, within } from '@testing-library/dom'
import type { ComboBox } from '@catalystcommunity/controls'
import type { OrgSummary } from '~/api/csilapi/types.gen.ts'
import type { SessionSummary } from '~/api/auth.ts'

/**
 * The org pages manage a REAL organization, chosen by the caller.
 *
 * Before this, every one of them passed the signed-in user's id as the org id.
 * Projects live in `organizations` rows with their own ids, and a LinkKeys
 * login has no such row, so those pages managed an org nothing belonged to.
 * These tests pin the three things that fix it: the gate is "administers
 * some org", the first request names an org the caller may actually manage,
 * and `?org=` in the URL is honoured.
 */

const ORGS: OrgSummary[] = [
  { orgId: 'org-a', name: 'alpha', displayName: 'Alpha', status: 'active', isDefault: true, isPrivate: false },
  { orgId: 'org-b', name: 'beta', displayName: 'Beta', status: 'active', isDefault: false, isPrivate: true },
]

vi.mock('~/api/client.ts', () => {
  class ServiceError extends Error {
    constructor(
      readonly code: string,
      message: string,
    ) {
      super(message)
    }
  }
  class TransportFailure extends Error {}
  return {
    ServiceError,
    TransportFailure,
    api: {
      listRoleAssignments: vi.fn(async () => ({ assignments: [] })),
      listUsers: vi.fn(async () => ({ users: [] })),
      listGroups: vi.fn(async () => ({ groups: [] })),
      listOrgs: vi.fn(async () => ({ orgs: ORGS })),
      assignRole: vi.fn(),
      revokeRole: vi.fn(),
    },
  }
})

vi.mock('~/store/resources.ts', () => ({
  useOrgs: () => ({
    state: () => ({ data: { orgs: ORGS }, error: undefined, loading: false, loaded: true }),
    sync: () => {},
    refresh: async () => {},
    release: () => {},
  }),
}))


import { api } from '~/api/client.ts'
// The real session helpers: the point is to test that the page gates on
// adminOrgIds and canManageSomeOrg correctly, not to restate what they return.
import { setSessionForTest } from '~/lib/session.ts'
import { syncRouteFromLocation } from '~/ui/router.ts'
import { mount, settle } from '~/test/mount.ts'
import { tag, type OrgRolesPage } from './OrgRoles.ts'

function sessionFor(overrides: Partial<SessionSummary>): SessionSummary {
  return {
    logged_in: true,
    user_id: 'user-1',
    display_name: 'Someone',
    is_global_admin: false,
    capabilities: {} as SessionSummary['capabilities'],
    roles: [],
    ...overrides,
  }
}

async function renderAt(path: string): Promise<HTMLElement> {
  window.history.pushState({}, '', `/app${path}`)
  syncRouteFromLocation()
  const page = document.createElement(tag) as OrgRolesPage
  page.routeName = 'orgRoles'
  mount(page)
  await settle()
  return page
}

function orgCombo(page: HTMLElement): ComboBox | null {
  return page.querySelector<ComboBox>('.org-header-picker combo-box')
}

// Cleared for EVERY test in this file, so an assertion about which org a
// request named can never be satisfied by a call left over from the last test.
beforeEach(() => {
  vi.mocked(api.listRoleAssignments).mockClear()
})

describe('OrgRoles', () => {
  it('refuses a signed-in user who administers no organization', async () => {
    setSessionForTest(sessionFor({ roles: [{ scope_type: 'project', scope_id: 'p1', role: 'owner' }] }))
    const page = await renderAt('/org/roles')

    const refusal = within(page).getByText('You do not have permission to manage roles.')
    expect(refusal.closest('[hidden]')).toBeNull()
    expect(api.listRoleAssignments).not.toHaveBeenCalled()
  })

  it('opens an org admin on the only org they administer, not the default one', async () => {
    setSessionForTest(sessionFor({ roles: [{ scope_type: 'org', scope_id: 'org-b', role: 'admin' }] }))
    const page = await renderAt('/org/roles')

    await waitFor(() => expect(api.listRoleAssignments).toHaveBeenCalled())
    expect(api.listRoleAssignments).toHaveBeenCalledWith({ scopeType: 'org', scopeId: 'org-b' })
    expect(vi.mocked(api.listRoleAssignments).mock.calls.every(([req]) => req.scopeId === 'org-b')).toBe(true)

    // One administered org: the picker collapses to a label naming it.
    const label = within(page).getByText('Beta')
    expect(label.closest('[hidden]')).toBeNull()
    expect(orgCombo(page)!.closest('[hidden]')).not.toBeNull()
    expect(within(page).queryByText('Alpha (default)')).toBeNull()
  })

  it('honours ?org= for a global admin', async () => {
    setSessionForTest(sessionFor({ is_global_admin: true }))
    const page = await renderAt('/org/roles?org=org-a')

    await waitFor(() => expect(api.listRoleAssignments).toHaveBeenCalled())
    expect(api.listRoleAssignments).toHaveBeenCalledWith({ scopeType: 'org', scopeId: 'org-a' })

    // Two administered orgs: a real picker, showing the chosen one.
    const picker = orgCombo(page)
    expect(picker).toBeTruthy()
    expect(picker!.closest('[hidden]')).toBeNull()
    expect(picker!.value).toBe('org-a')
  })
})

describe('OrgRoles picker', () => {
  it('re-keys the assignments to the org chosen in the picker and writes ?org=', async () => {
    setSessionForTest(sessionFor({ is_global_admin: true }))
    const page = await renderAt('/org/roles')

    // No ?org=: the default org wins for a global admin.
    await waitFor(() => expect(api.listRoleAssignments).toHaveBeenCalledWith({ scopeType: 'org', scopeId: 'org-a' }))

    // What the combo box does when the person picks an option.
    const picker = orgCombo(page)!
    picker.value = 'org-b'
    picker.dispatchEvent(new Event('change', { bubbles: true }))
    await settle()

    await waitFor(() => expect(api.listRoleAssignments).toHaveBeenCalledWith({ scopeType: 'org', scopeId: 'org-b' }))
    expect(window.location.search).toContain('org=org-b')
  })
})
