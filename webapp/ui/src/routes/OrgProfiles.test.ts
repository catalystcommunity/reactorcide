import { describe, expect, it, vi, beforeEach } from 'vitest'
import { waitFor } from '@testing-library/dom'
import type { CheckBox, ListBox, TextField } from '@catalystcommunity/controls'
import type { ExecutionProfileSummary, OrgSummary } from '~/api/csilapi/types.gen.ts'
import type { SessionSummary } from '~/api/auth.ts'

const ORGS: OrgSummary[] = [{ orgId: 'org-a', name: 'alpha', displayName: 'Alpha', status: 'active', isDefault: true, isPrivate: false }]
const PROFILES: ExecutionProfileSummary[] = [
  {
    name: 'standard',
    denySecrets: false,
    secretPathAllowlist: [],
    runtimeCapabilities: ['docker'],
    mayRunAsRoot: true,
    allowedWorkerClasses: [],
    resourceCeilings: {},
    trustedCacheWrites: true,
    cacheNamespaceClasses: [],
    cacheWriteClasses: ['default', 'tag'],
    egressControl: false,
  },
  {
    name: 'integration-pr',
    denySecrets: true,
    secretPathAllowlist: ['reactorcide/public'],
    runtimeCapabilities: ['gpu'],
    mayRunAsRoot: false,
    allowedWorkerClasses: ['integration'],
    timeoutCeilingSeconds: 1800,
    resourceCeilings: { cpu_limit: '4', memory_limit: '8Gi' },
    cacheNamespace: 'integration-pr',
    artifactNamespace: 'integration-pr',
    trustedCacheWrites: false,
    cacheNamespaceClasses: ['release'],
    cacheWriteClasses: [],
    egressControl: true,
  },
]

vi.mock('~/api/client.ts', () => {
  class ServiceError extends Error {}
  class TransportFailure extends Error {}
  return {
    ServiceError,
    TransportFailure,
    api: {
      listOrgs: vi.fn(async () => ({ orgs: ORGS })),
      listExecutionProfiles: vi.fn(async () => ({ profiles: PROFILES })),
      listWorkerClasses: vi.fn(async () => ({ workerClasses: [{ name: 'default', protected: false, poolIds: [] }, { name: 'integration', protected: false, poolIds: [] }] })),
      putExecutionProfile: vi.fn(async (req: { profile: ExecutionProfileSummary }) => ({ profile: req.profile })),
      deleteExecutionProfile: vi.fn(async () => ({ deleted: true })),
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

vi.mock('~/ui/controls.ts', async (importOriginal) => {
  const real = await importOriginal<typeof import('~/ui/controls.ts')>()
  return { ...real, confirmAction: vi.fn(async () => true) }
})

import { api } from '~/api/client.ts'
import { setSessionForTest } from '~/lib/session.ts'
import { syncRouteFromLocation } from '~/ui/router.ts'
import { mount, settle } from '~/test/mount.ts'
import { parseCeiling, tag, type OrgProfilesPage } from './OrgProfiles.ts'

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

async function renderPage(): Promise<OrgProfilesPage> {
  window.history.pushState({}, '', '/app/org/profiles')
  syncRouteFromLocation()
  const page = document.createElement(tag) as OrgProfilesPage
  page.routeName = 'orgProfiles'
  mount(page)
  await settle()
  return page
}

function type(el: Element, value: string): void {
  ;(el as TextField).value = value
  el.dispatchEvent(new Event('input', { bubbles: true }))
}

function control(root: Element, label: string): Element {
  const f = [...root.querySelectorAll('.field')].find((el) => el.querySelector('.field-label span')?.textContent === label)
  if (!f) throw new Error(`no field ${label}`)
  return f.querySelector('text-field, combo-box, list-box')!
}

function box(root: Element, text: string): CheckBox {
  return [...root.querySelectorAll('check-box')].find((b) => b.textContent === text) as CheckBox
}

function tick(root: Element, text: string, checked: boolean): void {
  const b = box(root, text)
  b.checked = checked
  b.dispatchEvent(new Event('change', { bubbles: true }))
}

function rowButton(page: Element, label: string): HTMLElement {
  return [...page.querySelectorAll('push-button')].find((b) => b.getAttribute('aria-label') === label) as HTMLElement
}

beforeEach(() => {
  vi.clearAllMocks()
  document.body.replaceChildren()
})

describe('OrgProfiles', () => {
  it('parses the timeout ceiling', () => {
    expect(parseCeiling('')).toBeUndefined()
    expect(parseCeiling(' 1800 ')).toBe(1800)
    expect(parseCeiling('0')).toBeNull()
    expect(parseCeiling('1.5')).toBeNull()
  })

  it('lists profiles and offers no delete for built-ins', async () => {
    setSessionForTest(adminSession())
    const page = await renderPage()
    await waitFor(() => expect(page.querySelectorAll('.profile-name').length).toBe(2))
    expect(page.querySelector('.profile-name')!.textContent).toBe('standard (built-in)')
    expect(rowButton(page, 'Delete profile standard').hidden).toBe(true)
    expect(rowButton(page, 'Delete profile integration-pr').hidden).toBe(false)
  })

  it('creates a profile with a full put', async () => {
    setSessionForTest(adminSession())
    const page = await renderPage()
    await waitFor(() => expect(api.listWorkerClasses).toHaveBeenCalled())
    const form = page.querySelector('form.profile-form')!
    type(control(form, 'Name'), 'nightly')
    tick(form, 'Deny secrets', true)
    tick(form, 'Egress control', true)
    type(control(form, 'Timeout ceiling (seconds)'), '3600')
    type(control(form, 'Cache write classes'), 'default, tag')
    const classes = control(form, 'Allowed worker classes') as ListBox
    classes.values = ['integration']
    classes.dispatchEvent(new Event('change', { bubbles: true }))
    await settle()
    form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await waitFor(() => expect(api.putExecutionProfile).toHaveBeenCalled())
    expect(vi.mocked(api.putExecutionProfile).mock.calls[0][0]).toEqual({
      organization: 'alpha',
      profile: {
        name: 'nightly',
        denySecrets: true,
        secretPathAllowlist: [],
        runtimeCapabilities: [],
        mayRunAsRoot: false,
        allowedWorkerClasses: ['integration'],
        resourceCeilings: {},
        trustedCacheWrites: false,
        cacheNamespaceClasses: [],
        cacheWriteClasses: ['default', 'tag'],
        egressControl: true,
        timeoutCeilingSeconds: 3600,
      },
    })
  })

  it('edits a profile, keeps the draft through a refresh, and sends the loaded ceilings back', async () => {
    setSessionForTest(adminSession())
    const page = await renderPage()
    await waitFor(() => expect(page.querySelectorAll('.profile-name').length).toBe(2))
    rowButton(page, 'Edit profile integration-pr').click()
    await settle()
    const form = page.querySelector('form.profile-form')!
    expect((control(form, 'Name') as TextField).value).toBe('integration-pr')
    expect(box(form, 'Egress control').checked).toBe(true)
    tick(form, 'Egress control', false)
    type(control(form, 'Cache namespace'), 'integration-v2')
    // A refresh of the list does not refill the form.
    page.invalidate()
    await settle()
    expect((control(form, 'Cache namespace') as TextField).value).toBe('integration-v2')
    form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await waitFor(() => expect(api.putExecutionProfile).toHaveBeenCalled())
    expect(vi.mocked(api.putExecutionProfile).mock.calls[0][0]).toEqual({
      organization: 'alpha',
      profile: {
        name: 'integration-pr',
        denySecrets: true,
        secretPathAllowlist: ['reactorcide/public'],
        runtimeCapabilities: ['gpu'],
        mayRunAsRoot: false,
        allowedWorkerClasses: ['integration'],
        timeoutCeilingSeconds: 1800,
        resourceCeilings: { cpu_limit: '4', memory_limit: '8Gi' },
        cacheNamespace: 'integration-v2',
        artifactNamespace: 'integration-pr',
        trustedCacheWrites: false,
        cacheNamespaceClasses: ['release'],
        cacheWriteClasses: [],
        egressControl: false,
      },
    })
  })

  it('deletes a profile after confirmation', async () => {
    setSessionForTest(adminSession())
    const page = await renderPage()
    await waitFor(() => expect(page.querySelectorAll('.profile-name').length).toBe(2))
    rowButton(page, 'Delete profile integration-pr').click()
    await waitFor(() => expect(api.deleteExecutionProfile).toHaveBeenCalled())
    expect(vi.mocked(api.deleteExecutionProfile).mock.calls[0][0]).toEqual({ organization: 'alpha', name: 'integration-pr' })
  })
})
