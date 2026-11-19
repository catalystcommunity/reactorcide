import { beforeEach, describe, expect, it, vi } from 'vitest'
import { waitFor } from '@testing-library/dom'
import type { OrgSummary, ProjectDetail } from '~/api/csilapi/types.gen.ts'
import type { SessionSummary } from '~/api/auth.ts'

const ORGS: OrgSummary[] = [{ orgId: 'org-a', name: 'alpha', displayName: 'Alpha', status: 'active', isDefault: true, isPrivate: false }]
const PROJECT = { projectId: 'p1', orgId: 'org-a', name: 'Widget' } as unknown as ProjectDetail

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
      listImageAliases: vi.fn(async () => ({ aliases: [] })),
      getImageRolloutStatus: vi.fn(async () => ({ rollouts: [] })),
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
  useProject: () => ({
    state: () => ({ data: { project: PROJECT }, error: undefined, loading: false, loaded: true }),
    sync: () => {},
    refresh: async () => {},
    release: () => {},
  }),
}))

import { api } from '~/api/client.ts'
import { setSessionForTest } from '~/lib/session.ts'
import { mount, settle } from '~/test/mount.ts'
import { syncRouteFromLocation, type RouteName } from '~/ui/router.ts'
import { tag as orgTag } from './OrgImages.ts'
import { tag as projectTag } from './ProjectImages.ts'
import type { RcPage } from '~/app/page.ts'

function adminSession(): SessionSummary {
  return {
    logged_in: true,
    user_id: 'u1',
    display_name: 'Admin',
    is_global_admin: false,
    capabilities: {} as SessionSummary['capabilities'],
    roles: [{ scope_type: 'org', scope_id: 'org-a', role: 'admin' }],
  }
}

async function open(path: string, tagName: string, routeName: RouteName): Promise<RcPage> {
  window.history.pushState({}, '', path)
  syncRouteFromLocation()
  const page = document.createElement(tagName) as RcPage
  page.routeName = routeName
  mount(page)
  await settle()
  return page
}

beforeEach(() => {
  vi.clearAllMocks()
  document.body.replaceChildren()
  setSessionForTest(adminSession())
})

describe('Images pages', () => {
  it('lists the aliases of the chosen organization by its NAME', async () => {
    const page = await open('/app/org/images', orgTag, 'orgImages')
    await waitFor(() => expect(api.listImageAliases).toHaveBeenCalledWith({ organization: 'alpha' }))
    expect(page.querySelector('h1')!.textContent).toBe('Images')
    expect(page.textContent).toContain('No image aliases yet.')
  })

  it('lists the aliases of a project with the organization name and the project id, under an Images tab', async () => {
    const page = await open('/app/projects/p1/images', projectTag, 'projectImages')
    await waitFor(() => expect(api.listImageAliases).toHaveBeenCalledWith({ organization: 'alpha', projectId: 'p1' }))
    expect(page.querySelector('h1')!.textContent).toBe('Images: Widget')
    const current = page.querySelector('.tabs a[aria-current="page"]')!
    expect(current.textContent).toBe('Images')
    expect(current.getAttribute('href')).toBe('/app/projects/p1/images')
  })
})
