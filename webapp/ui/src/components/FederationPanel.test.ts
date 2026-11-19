import { afterEach, describe, expect, it, vi } from 'vitest'
import { waitFor } from '@testing-library/dom'
import type { ComboBox, TextArea, TextField } from '@catalystcommunity/controls'
import type { ListFederationIssuersResponse } from '~/api/csilapi/types.gen.ts'

let response: ListFederationIssuersResponse

vi.mock('~/api/client.ts', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/client.ts')>()
  return {
    ...actual,
    api: {
      listFederationIssuers: vi.fn(async () => response),
      listPrincipals: vi.fn(async () => ({
        principals: [{ principalId: 'p-bot', kind: 'service', name: 'deployer', displayName: 'Deployer', status: 'active', orgId: 'org-a', createdAt: '' }],
      })),
      saveFederationIssuer: vi.fn(async () => ({ issuer: response.issuers[0] })),
      deleteFederationIssuer: vi.fn(async () => ({ deleted: true })),
      saveFederationMapping: vi.fn(async () => ({ mapping: response.mappings[0] })),
      deleteFederationMapping: vi.fn(async () => ({ deleted: true })),
    },
  }
})

import { api } from '~/api/client.ts'
import { mount, settle, unmount } from '~/test/mount.ts'
import { conditionText, parseCondition, type FederationPanel } from './FederationPanel.ts'

const ISSUER = {
  issuerId: 'i-1',
  name: 'gha',
  issuerUrl: 'https://token.actions.githubusercontent.com',
  jwksUrl: '',
  audiences: ['reactorcide'],
  algorithms: ['RS256'],
  clockSkewSeconds: 60,
  status: 'active',
  createdAt: '',
}
const MAPPING = {
  mappingId: 'm-1',
  issuerId: 'i-1',
  name: 'main',
  principalId: 'p-bot',
  principalName: 'deployer',
  conditions: [{ claim: 'sub', op: 'glob', value: 'repo:acme/app:ref:refs/heads/*' }],
  capabilities: ['jobs:submit'],
  tokenTtlSeconds: 900,
  status: 'active',
  createdAt: '',
}

let mounted: Element | null = null
afterEach(() => {
  if (mounted) unmount(mounted)
  mounted = null
  vi.clearAllMocks()
})

async function panel(): Promise<FederationPanel> {
  const el = document.createElement('rc-federation-panel') as FederationPanel
  el.orgId = 'org-a'
  mounted = mount(el)
  await settle()
  await waitFor(() => expect(api.listFederationIssuers).toHaveBeenCalledWith({ orgId: 'org-a' }))
  await settle()
  return el
}

function byLabel<T extends Element>(root: Element, selector: string, label: RegExp): T {
  const match = [...root.querySelectorAll(selector)].find((el) => label.test(el.getAttribute('aria-label') ?? ''))
  if (!match) throw new Error(`no ${selector} labelled ${label}`)
  return match as T
}

function type(control: TextField | TextArea, value: string): void {
  control.value = value
  control.dispatchEvent(new Event('input', { bubbles: true }))
}

describe('parseCondition', () => {
  it('reads equals and glob conditions', () => {
    expect(parseCondition('repository=acme/app')).toEqual({ claim: 'repository', op: 'equals', value: 'acme/app' })
    expect(parseCondition('sub~repo:acme/*')).toEqual({ claim: 'sub', op: 'glob', value: 'repo:acme/*' })
    expect(parseCondition('sub=a~b')).toEqual({ claim: 'sub', op: 'equals', value: 'a~b' })
    expect(parseCondition('nothing')).toBeUndefined()
    expect(conditionText({ claim: 'sub', op: 'glob', value: 'x*' })).toBe('sub~x*')
  })
})

describe('FederationPanel', () => {
  it('shows the empty state and hides the mapping form without issuers', async () => {
    response = { issuers: [], mappings: [] }
    const el = await panel()
    expect(el.textContent).toContain('No federated issuers')
    expect((el.querySelector('.federation-mappings')!.closest('.stack') as HTMLElement).hidden).toBe(true)
  })

  it('lists issuers and mappings and keeps rows on refresh', async () => {
    response = { issuers: [ISSUER], mappings: [MAPPING] }
    const el = await panel()
    await waitFor(() => expect(el.querySelector('.issuer-url')?.textContent).toBe(ISSUER.issuerUrl))
    expect(el.querySelector('.mapping-conditions')!.textContent).toBe('sub~repo:acme/app:ref:refs/heads/*')
    expect(el.querySelector('.mapping-principal')!.textContent).toBe('deployer')
    const row = el.querySelector('.federation-issuers tbody tr')
    el.invalidate()
    await settle()
    expect(el.querySelector('.federation-issuers tbody tr')).toBe(row)
  })

  it('adds an issuer through the CSIL operation', async () => {
    response = { issuers: [], mappings: [] }
    const el = await panel()
    type(byLabel<TextField>(el, 'text-field', /^Name/), 'gha')
    type(byLabel<TextField>(el, 'text-field', /^Issuer URL/), ISSUER.issuerUrl)
    type(byLabel<TextField>(el, 'text-field', /^Audiences/), 'reactorcide, other')
    await settle()
    const form = el.querySelector('form') as HTMLFormElement
    form.dispatchEvent(new Event('submit', { cancelable: true }))
    await waitFor(() =>
      expect(api.saveFederationIssuer).toHaveBeenCalledWith({
        orgId: 'org-a',
        name: 'gha',
        issuerUrl: ISSUER.issuerUrl,
        audiences: ['reactorcide', 'other'],
        algorithms: undefined,
        jwksUrl: undefined,
      }),
    )
  })

  it('adds a mapping with parsed conditions', async () => {
    response = { issuers: [ISSUER], mappings: [] }
    const el = await panel()
    await waitFor(() => expect(el.querySelector('.issuer-url')?.textContent).toBe(ISSUER.issuerUrl))
    const forms = el.querySelectorAll('form')
    const mappingForm = forms[1] as HTMLFormElement
    const principal = byLabel<ComboBox>(mappingForm, 'combo-box', /Service principal/)
    principal.value = 'p-bot'
    principal.dispatchEvent(new Event('change', { bubbles: true }))
    type(byLabel<TextField>(mappingForm, 'text-field', /^Name/), 'main')
    type(byLabel<TextArea>(mappingForm, 'text-area', /^Conditions/), 'sub~repo:acme/app:*\nrepository=acme/app')
    type(byLabel<TextField>(mappingForm, 'text-field', /^Capabilities/), 'jobs:submit,jobs:read')
    await settle()
    mappingForm.dispatchEvent(new Event('submit', { cancelable: true }))
    await waitFor(() =>
      expect(api.saveFederationMapping).toHaveBeenCalledWith({
        orgId: 'org-a',
        issuerId: 'i-1',
        name: 'main',
        principalId: 'p-bot',
        conditions: [
          { claim: 'sub', op: 'glob', value: 'repo:acme/app:*' },
          { claim: 'repository', op: 'equals', value: 'acme/app' },
        ],
        capabilities: ['jobs:submit', 'jobs:read'],
        tokenTtlSeconds: undefined,
      }),
    )
  })
})
