import { afterEach, describe, expect, it, vi } from 'vitest'
import { waitFor } from '@testing-library/dom'
import type { OrgSummary, ProjectDetail } from '~/api/csilapi/types.gen.ts'
import type { ResourceState } from '~/store/index.ts'

let orgs: ResourceState<{ orgs: OrgSummary[] }>

const PROJECT = { projectId: 'p1', orgId: 'org1', name: 'Widget' } as unknown as ProjectDetail

vi.mock('~/store/resources.ts', () => ({
  useProject: () => ({
    state: () => ({ data: { project: PROJECT }, error: undefined, loading: false, loaded: true }),
    refresh: async () => {},
    sync: () => {},
    release: () => {},
  }),
  useOrgs: () => ({ state: () => orgs, refresh: async () => {}, sync: () => {}, release: () => {} }),
}))

vi.mock('~/api/client.ts', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/client.ts')>()
  return {
    ...actual,
    api: { getCiTrustSettings: vi.fn(async () => ({ settings: {}, effective: {}, organizationEffective: {}, canLoosen: true })) },
  }
})

import { api } from '~/api/client.ts'
import { mount, settle } from '~/test/mount.ts'
import { tag, type ProjectTrustPage } from './ProjectTrust.ts'

async function page(): Promise<ProjectTrustPage> {
  window.history.pushState({}, '', '/app/projects/p1/trust')
  const { syncRouteFromLocation } = await import('~/ui/router.ts')
  syncRouteFromLocation()
  const el = document.createElement(tag) as ProjectTrustPage
  el.routeName = 'projectTrust'
  mount(el)
  await settle()
  return el
}

let current: Element | null = null
afterEach(() => {
  current?.remove()
  current = null
  vi.clearAllMocks()
})

describe('project CI trust page', () => {
  it("asks for the settings with the organization NAME and the project id, and links the project's tabs", async () => {
    orgs = { data: { orgs: [{ orgId: 'org1', name: 'acme', displayName: 'Acme', status: 'active', isDefault: false, isPrivate: false }] }, error: undefined, loading: false, loaded: true }
    const el = (current = await page()) as ProjectTrustPage
    await waitFor(() => expect(api.getCiTrustSettings).toHaveBeenCalledWith({ organization: 'acme', projectId: 'p1' }))
    expect(el.querySelector('h1')?.textContent).toBe('CI trust: Widget')
    const tabs = [...el.querySelectorAll('.tabs a')].map((a) => a.getAttribute('href'))
    expect(tabs).toEqual(['/app/projects/p1', '/app/projects/p1/trust', '/app/projects/p1/policy'])
  })

  it('says so when the organization of the project is not visible', async () => {
    orgs = { data: { orgs: [] }, error: undefined, loading: false, loaded: true }
    const el = (current = await page()) as ProjectTrustPage
    expect(el.textContent).toContain("This project's organization is not visible to you.")
    expect(api.getCiTrustSettings).not.toHaveBeenCalled()
  })
})
