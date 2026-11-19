import { describe, expect, it, vi } from 'vitest'
import { waitFor } from '@testing-library/dom'
import type { PushButton, TextField } from '@catalystcommunity/controls'
import type { OrgSummary } from '~/api/csilapi/types.gen.ts'
import type { SessionSummary } from '~/api/auth.ts'

/** The priority shift of a group on the Groups page (docs/priority.md). */

const ORGS: OrgSummary[] = [{ orgId: 'org-a', name: 'alpha', displayName: 'Alpha', status: 'active', isDefault: true, isPrivate: false }]
let shift = 0

vi.mock('~/api/client.ts', () => {
  class ServiceError extends Error {}
  class TransportFailure extends Error {}
  return {
    ServiceError,
    TransportFailure,
    api: {
      listGroups: vi.fn(async () => ({
        groups: [{ groupId: 'g1', orgId: 'org-a', name: 'release', createdAt: '', updatedAt: '', priorityShift: shift }],
      })),
      listOrgs: vi.fn(async () => ({ orgs: ORGS })),
      setGroupPriorityShift: vi.fn(async (req: { groupId: string; shift: number }) => {
        shift = req.shift
        return { group: { groupId: req.groupId, orgId: 'org-a', name: 'release', createdAt: '', updatedAt: '', priorityShift: req.shift } }
      }),
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
import { setSessionForTest } from '~/lib/session.ts'
import { syncRouteFromLocation } from '~/ui/router.ts'
import { mount, settle } from '~/test/mount.ts'
import { tag, type OrgGroupsPage } from './OrgGroups.ts'

function session(): SessionSummary {
  return {
    logged_in: true,
    user_id: 'user-1',
    display_name: 'Someone',
    is_global_admin: false,
    capabilities: {} as SessionSummary['capabilities'],
    roles: [{ scope_type: 'org', scope_id: 'org-a', role: 'admin' }],
  }
}

async function open(): Promise<HTMLElement> {
  window.history.pushState({}, '', '/app/org/groups?org=org-a')
  syncRouteFromLocation()
  const page = document.createElement(tag) as OrgGroupsPage
  page.routeName = 'orgGroups'
  mount(page)
  await settle()
  return page
}

describe('OrgGroups priority shift', () => {
  it('shows the shift of a group and saves a new value', async () => {
    shift = 20
    setSessionForTest(session())
    const page = await open()
    await waitFor(() => expect(page.querySelector('.group-shift-input')).not.toBeNull())
    const input = page.querySelector('.group-shift-input') as TextField
    await waitFor(() => expect(input.value).toBe('20'))
    const save = page.querySelector('.group-shift-save') as PushButton
    expect(save.disabled).toBe(true)

    input.value = '-15'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await settle()
    expect(save.disabled).toBe(false)
    save.click()
    await waitFor(() => expect(api.setGroupPriorityShift).toHaveBeenCalledWith({ groupId: 'g1', shift: -15 }))
    await waitFor(() => expect(input.value).toBe('-15'))
  })

  it('refuses a shift out of bounds, and sends nothing', async () => {
    vi.mocked(api.setGroupPriorityShift).mockClear()
    shift = 0
    setSessionForTest(session())
    const page = await open()
    await waitFor(() => expect(page.querySelector('.group-shift-input')).not.toBeNull())
    const input = page.querySelector('.group-shift-input') as TextField
    input.value = '99'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await settle()
    ;(page.querySelector('.group-shift-save') as PushButton).click()
    await waitFor(() => expect(page.textContent).toContain('The priority shift must be a whole number from -50 to 50.'))
    expect(api.setGroupPriorityShift).not.toHaveBeenCalled()
  })
})
