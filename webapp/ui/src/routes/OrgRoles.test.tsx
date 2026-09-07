import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, waitFor } from '@solidjs/testing-library'
import { MemoryRouter, Route, createMemoryHistory } from '@solidjs/router'
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
    refresh: async () => {},
  }),
}))

// Only `useSession` is replaced. `adminOrgIds` and `canManageSomeOrg` are the
// real ones: the point is to test that the page gates on them correctly, not
// to restate what they return.
let currentSession: SessionSummary
vi.mock('~/lib/session.tsx', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/lib/session.tsx')>()
  return {
    ...actual,
    useSession: () => ({
      session: (() => currentSession) as unknown,
      config: (() => undefined) as unknown,
      refetch: () => {},
    }),
  }
})

import { api } from '~/api/client.ts'
import { OrgRoles } from './OrgRoles.tsx'

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

function renderAt(path: string) {
  const history = createMemoryHistory()
  history.set({ value: path, replace: true, scroll: false })
  return render(() => (
    <MemoryRouter history={history}>
      <Route path="/org/roles" component={OrgRoles} />
    </MemoryRouter>
  ))
}

// Cleared for EVERY test in this file, so an assertion about which org a
// request named can never be satisfied by a call left over from the last test.
beforeEach(() => {
  vi.mocked(api.listRoleAssignments).mockClear()
})

describe('OrgRoles', () => {
  it('refuses a signed-in user who administers no organization', async () => {
    currentSession = sessionFor({ roles: [{ scope_type: 'project', scope_id: 'p1', role: 'owner' }] })
    const { findByText } = renderAt('/org/roles')

    expect(await findByText('You do not have permission to manage roles.')).toBeTruthy()
    expect(api.listRoleAssignments).not.toHaveBeenCalled()
  })

  it('opens an org admin on the only org they administer, not the default one', async () => {
    currentSession = sessionFor({ roles: [{ scope_type: 'org', scope_id: 'org-b', role: 'admin' }] })
    const { findByText, queryByText } = renderAt('/org/roles')

    await waitFor(() => expect(api.listRoleAssignments).toHaveBeenCalled())
    expect(api.listRoleAssignments).toHaveBeenCalledWith({ scopeType: 'org', scopeId: 'org-b' })
    expect(vi.mocked(api.listRoleAssignments).mock.calls.every(([req]) => req.scopeId === 'org-b')).toBe(true)

    // One administered org: the picker collapses to a label naming it.
    expect(await findByText('Beta')).toBeTruthy()
    expect(queryByText('Alpha (default)')).toBeNull()
  })

  it('honours ?org= for a global admin', async () => {
    currentSession = sessionFor({ is_global_admin: true })
    const { container } = renderAt('/org/roles?org=org-a')

    await waitFor(() => expect(api.listRoleAssignments).toHaveBeenCalled())
    expect(api.listRoleAssignments).toHaveBeenCalledWith({ scopeType: 'org', scopeId: 'org-a' })

    // Two administered orgs: a real picker, showing the chosen one.
    const picker = container.querySelector<HTMLSelectElement>('select#managed-org')
    expect(picker).toBeTruthy()
    expect(picker!.value).toBe('org-a')
  })
})

describe('OrgRoles picker', () => {
  it('re-keys the assignments to the org chosen in the picker and writes ?org=', async () => {
    currentSession = sessionFor({ is_global_admin: true })
    const history = createMemoryHistory()
    history.set({ value: '/org/roles', replace: true, scroll: false })
    const { container } = render(() => (
      <MemoryRouter history={history}>
        <Route path="/org/roles" component={OrgRoles} />
      </MemoryRouter>
    ))

    // No ?org=: the default org wins for a global admin.
    await waitFor(() =>
      expect(api.listRoleAssignments).toHaveBeenCalledWith({ scopeType: 'org', scopeId: 'org-a' }),
    )

    const picker = container.querySelector<HTMLSelectElement>('select#managed-org')!
    picker.value = 'org-b'
    picker.dispatchEvent(new Event('change', { bubbles: true }))

    await waitFor(() =>
      expect(api.listRoleAssignments).toHaveBeenCalledWith({ scopeType: 'org', scopeId: 'org-b' }),
    )
    expect(history.get()).toContain('org=org-b')
  })
})
