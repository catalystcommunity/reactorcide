import { describe, expect, it, vi } from 'vitest'
import { waitFor } from '@testing-library/dom'
import type { TextField } from '@catalystcommunity/controls'
import type { OrgSummary } from '~/api/csilapi/types.gen.ts'
import type { SessionSummary } from '~/api/auth.ts'

const ORGS: OrgSummary[] = [{ orgId: 'org-a', name: 'alpha', displayName: 'Alpha', status: 'active', isDefault: true, isPrivate: false }]

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
      listWorkerClasses: vi.fn(async () => ({
        workerClasses: [
          { name: 'default', protected: false, poolIds: [] },
          { name: 'deploy', protected: true, poolIds: [], refClassesAny: ['production'] },
        ],
      })),
      listPools: vi.fn(async () => ({ pools: [] })),
      putWorkerClass: vi.fn(async (req: { workerClass: unknown }) => ({ workerClass: req.workerClass })),
      deleteWorkerClass: vi.fn(),
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
import { tag, type WorkerClassesPage } from './WorkerClasses.ts'

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
  window.history.pushState({}, '', '/app/workers/classes')
  syncRouteFromLocation()
  const page = document.createElement(tag) as WorkerClassesPage
  page.routeName = 'workerClasses'
  mount(page)
  await settle()
  return page
}

describe('WorkerClasses ref classes', () => {
  it('shows the required ref classes of each class', async () => {
    setSessionForTest(adminSession())
    const page = await renderPage()
    await waitFor(() => expect(page.querySelectorAll('.class-ref-classes').length).toBe(2))
    expect([...page.querySelectorAll('.class-ref-classes')].map((td) => td.textContent)).toEqual(['any', 'production'])
  })

  it('sends the required ref classes when it saves a class', async () => {
    setSessionForTest(adminSession())
    const page = await renderPage()
    await waitFor(() => expect(api.listWorkerClasses).toHaveBeenCalled())
    const form = [...page.querySelectorAll('form')].find((f) => f.textContent?.includes('Required ref classes'))!
    const [name, classes] = [...form.querySelectorAll('text-field')] as TextField[]
    name.value = 'release'
    name.dispatchEvent(new Event('input', { bubbles: true }))
    classes.value = 'release, production'
    classes.dispatchEvent(new Event('input', { bubbles: true }))
    await settle()
    form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await waitFor(() => expect(api.putWorkerClass).toHaveBeenCalled())
    expect(vi.mocked(api.putWorkerClass).mock.calls[0][0]).toEqual({
      organization: 'alpha',
      workerClass: { name: 'release', protected: false, poolIds: [], refClassesAny: ['release', 'production'] },
    })
  })
})
