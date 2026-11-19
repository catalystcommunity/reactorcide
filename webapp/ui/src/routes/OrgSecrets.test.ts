import { describe, expect, it, vi, beforeEach } from 'vitest'
import { waitFor } from '@testing-library/dom'
import type { ComboBox, TextField } from '@catalystcommunity/controls'
import type { OrgSummary, SecretGrant } from '~/api/csilapi/types.gen.ts'
import type { SessionSummary } from '~/api/auth.ts'

const ORGS: OrgSummary[] = [{ orgId: 'org-a', name: 'alpha', displayName: 'Alpha', status: 'active', isDefault: true, isPrivate: false }]
const GRANT: SecretGrant = {
  grantId: 'g-1',
  orgId: 'org-a',
  name: 'deploy',
  secretPathMatch: 'prefix',
  secretPathPattern: 'reactorcide/',
  jobNameMatch: 'glob',
  jobNamePattern: 'deploy-*',
  refClassesAny: ['production'],
  delivery: 'env',
  proxyHosts: [],
  createdAt: '2026-11-01T00:00:00Z',
  updatedAt: '2026-11-01T00:00:00Z',
}

vi.mock('~/api/client.ts', () => {
  class ServiceError extends Error {}
  class TransportFailure extends Error {}
  return {
    ServiceError,
    TransportFailure,
    api: {
      listOrgs: vi.fn(async () => ({ orgs: ORGS })),
      listSecretPaths: vi.fn(async () => ({ paths: [] })),
      listSecretGrants: vi.fn(async () => ({ grants: [GRANT] })),
      createSecretGrant: vi.fn(async () => ({ grant: GRANT })),
      updateSecretGrant: vi.fn(async () => ({ grant: GRANT })),
      deleteSecretGrant: vi.fn(async () => ({ deleted: true })),
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
import { deliveryRequest, grantDeliveryText, headerFormatError, tag, type OrgSecretsPage } from './OrgSecrets.ts'

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

async function renderPage(): Promise<OrgSecretsPage> {
  window.history.pushState({}, '', '/app/org/secrets')
  syncRouteFromLocation()
  const page = document.createElement(tag) as OrgSecretsPage
  page.routeName = 'orgSecrets'
  mount(page)
  await settle()
  return page
}

function type(el: Element, value: string): void {
  ;(el as TextField).value = value
  el.dispatchEvent(new Event('input', { bubbles: true }))
}

function choose(el: Element, value: string): void {
  ;(el as ComboBox).value = value
  el.dispatchEvent(new Event('change', { bubbles: true }))
}

/** The control of the field with this label inside root. */
function control(root: Element, label: string): Element {
  const f = [...root.querySelectorAll('.field')].find((el) => el.querySelector('.field-label span')?.textContent === label)
  if (!f) throw new Error(`no field ${label}`)
  return f.querySelector('text-field, combo-box, list-box')!
}

beforeEach(() => {
  vi.clearAllMocks()
  document.body.replaceChildren()
})

describe('secret grant helpers', () => {
  it('builds the delivery fields', () => {
    expect(deliveryRequest('env', 'x', 'y', 'z')).toEqual({ delivery: 'env', proxyHosts: [], proxyHeader: '', proxyHeaderFormat: '' })
    expect(deliveryRequest('proxy', 'api.github.com, *.example.com', ' Authorization ', 'Bearer {}')).toEqual({
      delivery: 'proxy',
      proxyHosts: ['api.github.com', '*.example.com'],
      proxyHeader: 'Authorization',
      proxyHeaderFormat: 'Bearer {}',
    })
    expect(grantDeliveryText({ ...GRANT, delivery: 'proxy', proxyHosts: ['api.github.com'], proxyHeader: 'Authorization' })).toBe('proxy → api.github.com · Authorization')
    expect(headerFormatError('Bearer {}')).toBeUndefined()
    expect(headerFormatError('Bearer')).toBeDefined()
    expect(headerFormatError('{}{}')).toBeDefined()
  })
})

describe('OrgSecrets grants', () => {
  it('creates a proxy grant with the delivery fields', async () => {
    setSessionForTest(adminSession())
    const page = await renderPage()
    await waitFor(() => expect(page.querySelector('.grant-name')?.textContent).toBe('deploy'))
    const card = [...page.querySelectorAll('.card')].find((c) => c.querySelector('h3')?.textContent === 'New grant')!
    type(control(card, 'Name'), 'github api')
    type(control(card, 'Secret path pattern'), 'reactorcide/github')
    choose(control(card, 'Delivery'), 'proxy')
    await settle()
    type(control(card, 'Proxy hosts'), 'api.github.com')
    type(control(card, 'Header'), 'Authorization')
    type(control(card, 'Header format'), 'Bearer {}')
    await settle()
    card.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await waitFor(() => expect(api.createSecretGrant).toHaveBeenCalled())
    expect(vi.mocked(api.createSecretGrant).mock.calls[0][0]).toEqual({
      orgId: 'org-a',
      name: 'github api',
      secretPathMatch: 'prefix',
      secretPathPattern: 'reactorcide/github',
      jobNameMatch: 'glob',
      jobNamePattern: undefined,
      delivery: 'proxy',
      proxyHosts: ['api.github.com'],
      proxyHeader: 'Authorization',
      proxyHeaderFormat: 'Bearer {}',
    })
  })

  it('edits a grant with ref classes and delivery, and keeps the draft through a refresh', async () => {
    setSessionForTest(adminSession())
    const page = await renderPage()
    await waitFor(() => expect(page.querySelector('.grant-delivery')?.textContent).toBe('env'))
    const edit = [...page.querySelectorAll('push-button')].find((b) => b.getAttribute('aria-label') === 'Edit grant deploy') as HTMLElement
    edit.click()
    await settle()
    const card = page.querySelector('.grant-edit')!
    expect((card as HTMLElement).hidden).toBe(false)
    expect((control(card, 'Only for ref classes') as TextField).value).toBe('production')
    type(control(card, 'Name'), 'deploy prod')
    type(control(card, 'Never for ref classes'), 'fork')
    choose(control(card, 'Delivery'), 'proxy')
    await settle()
    type(control(card, 'Proxy hosts'), 'api.example.com')
    // A list refresh does not refill the open form.
    page.invalidate()
    await settle()
    expect((control(card, 'Name') as TextField).value).toBe('deploy prod')
    card.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await waitFor(() => expect(api.updateSecretGrant).toHaveBeenCalled())
    expect(vi.mocked(api.updateSecretGrant).mock.calls[0][0]).toEqual({
      grantId: 'g-1',
      name: 'deploy prod',
      secretPathMatch: 'prefix',
      secretPathPattern: 'reactorcide/',
      jobNameMatch: 'glob',
      jobNamePattern: 'deploy-*',
      description: '',
      setRefClasses: true,
      refClassesAny: ['production'],
      refClassesNone: ['fork'],
      delivery: 'proxy',
      proxyHosts: ['api.example.com'],
      proxyHeader: '',
      proxyHeaderFormat: '',
    })
  })

  it('deletes a grant after confirmation', async () => {
    setSessionForTest(adminSession())
    const page = await renderPage()
    await waitFor(() => expect(page.querySelector('.grant-name')).not.toBeNull())
    const buttons = [...page.querySelector('.grant-name')!.closest('tr')!.querySelectorAll('push-button')] as HTMLElement[]
    buttons.find((b) => b.textContent === 'Delete')!.click()
    await waitFor(() => expect(api.deleteSecretGrant).toHaveBeenCalled())
    expect(vi.mocked(api.deleteSecretGrant).mock.calls[0][0]).toEqual({ grantId: 'g-1' })
  })
})
