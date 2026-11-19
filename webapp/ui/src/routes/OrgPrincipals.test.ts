import { describe, expect, it, vi } from 'vitest'
import { waitFor, within } from '@testing-library/dom'
import type { OrgSummary, PrincipalDetail } from '~/api/csilapi/types.gen.ts'
import type { SessionSummary } from '~/api/auth.ts'

const ORGS: OrgSummary[] = [{ orgId: 'org-a', name: 'alpha', displayName: 'Alpha', status: 'active', isDefault: true, isPrivate: false }]

const DETAIL: PrincipalDetail = {
  principal: { principalId: 'p-bot', kind: 'service', name: 'deployer', displayName: 'Deployer', status: 'active', orgId: 'org-a', createdAt: '' },
  identities: [{ identityType: 'token', identityValue: 't-1', verifiedBy: 'system', createdAt: '' }],
  groups: ['release'],
  tokens: [{ tokenId: 't-1', name: 'ci', capabilities: ['jobs:read'], createdAt: '', isActive: true }],
  subjects: ['principal:p-bot', 'service:deployer', 'service:alpha/deployer'],
}

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
      listOrgs: vi.fn(async () => ({ orgs: ORGS })),
      listPrincipals: vi.fn(async () => ({
        principals: [DETAIL.principal, { principalId: 'p-human', kind: 'human', name: 'github/alice', displayName: 'alice', status: 'active', createdAt: '' }],
      })),
      getPrincipal: vi.fn(async () => ({ principal: DETAIL })),
      listGroups: vi.fn(async () => ({ groups: [{ groupId: 'g-1', orgId: 'org-a', name: 'release', createdAt: '', updatedAt: '' }] })),
      createServicePrincipal: vi.fn(async () => ({ principal: DETAIL })),
      issuePrincipalToken: vi.fn(async () => ({ token: 'secret-value', info: DETAIL.tokens[0] })),
      setPrincipalStatus: vi.fn(),
      linkPrincipalIdentity: vi.fn(),
      unlinkPrincipalIdentity: vi.fn(),
      setPrincipalGroup: vi.fn(),
      revokePrincipalToken: vi.fn(),
      listFederationIssuers: vi.fn(async () => ({ issuers: [], mappings: [] })),
      saveFederationIssuer: vi.fn(),
      deleteFederationIssuer: vi.fn(),
      saveFederationMapping: vi.fn(),
      deleteFederationMapping: vi.fn(),
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
import { tag, type OrgPrincipalsPage } from './OrgPrincipals.ts'

function adminSession(): SessionSummary {
  return {
    logged_in: true,
    user_id: 'user-1',
    display_name: 'Admin',
    is_global_admin: false,
    capabilities: {} as SessionSummary['capabilities'],
    roles: [{ scope_type: 'org', scope_id: 'org-a', role: 'admin' }],
  }
}

async function renderPage(): Promise<HTMLElement> {
  window.history.pushState({}, '', '/app/org/principals')
  syncRouteFromLocation()
  const page = document.createElement(tag) as OrgPrincipalsPage
  page.routeName = 'orgPrincipals'
  mount(page)
  await settle()
  return page
}

describe('OrgPrincipals', () => {
  it('refuses a caller who administers no organization', async () => {
    setSessionForTest({ ...adminSession(), roles: [] })
    const page = await renderPage()
    expect(within(page).getByText('You do not have permission to manage principals.').closest('[hidden]')).toBeNull()
    expect(api.listPrincipals).not.toHaveBeenCalled()
  })

  it('lists the principals of the org and shows one in detail', async () => {
    setSessionForTest(adminSession())
    const page = await renderPage()
    await waitFor(() => expect(api.listPrincipals).toHaveBeenCalledWith({ orgId: 'org-a' }))
    await waitFor(() => expect(page.querySelectorAll('.principal-toggle').length).toBe(2))
    const toggle = page.querySelector('.principal-toggle') as HTMLElement
    toggle.click()
    await settle()
    await waitFor(() => expect(api.getPrincipal).toHaveBeenCalledWith({ orgId: 'org-a', principalId: 'p-bot' }))
    await waitFor(() => expect(within(page).getByText('service:alpha/deployer')).toBeTruthy())
    expect(page.querySelector('.principal-groups')!.textContent).toContain('release')
    expect(page.querySelector('.principal-tokens')!.textContent).toContain('jobs:read')
    // The federation panel of the org is on the same page.
    await waitFor(() => expect(api.listFederationIssuers).toHaveBeenCalledWith({ orgId: 'org-a' }))
    expect(page.querySelector('rc-federation-panel')!.textContent).toContain('No federated issuers')
    // The row element stays when the list refreshes: no rebuild from data.
    const before = page.querySelector('.principal-toggle')
    await settle()
    expect(page.querySelector('.principal-toggle')).toBe(before)
  })
})
