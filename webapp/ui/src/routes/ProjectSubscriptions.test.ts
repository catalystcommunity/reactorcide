import { afterEach, describe, expect, it, vi } from 'vitest'
import { waitFor } from '@testing-library/dom'
import type { ComboBox, TextField } from '@catalystcommunity/controls'
import type { EventSubscriptionSummary, ProjectDetail } from '~/api/csilapi/types.gen.ts'

const PROJECT = { projectId: 'p1', orgId: 'org1', name: 'Widget' } as unknown as ProjectDetail

const SUB: EventSubscriptionSummary = {
  subscriptionId: 's-1',
  orgId: 'org1',
  name: 'build-failures',
  description: '',
  ownerPrincipalId: 'p-1',
  ownerActive: true,
  scope: 'project',
  projectId: 'p1',
  workflowSecurityId: '',
  ownerGroup: '',
  eventTypes: [],
  refClassesAny: [],
  results: [],
  actorKinds: [],
  sinkType: 'webhook',
  webhookUrl: 'https://hooks.example.com/rc',
  maxAttempts: 0,
  enabled: true,
  createdAt: '',
  updatedAt: '',
}

vi.mock('~/store/resources.ts', () => ({
  useProject: () => ({
    state: () => ({ data: { project: PROJECT }, error: undefined, loading: false, loaded: true }),
    refresh: async () => {},
    sync: () => {},
    release: () => {},
  }),
  useOrgs: () => ({ state: () => ({ data: { orgs: [] }, loaded: true }), refresh: async () => {}, sync: () => {}, release: () => {} }),
}))

vi.mock('~/api/client.ts', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/client.ts')>()
  return {
    ...actual,
    api: {
      listEventSubscriptions: vi.fn(async () => ({ subscriptions: [SUB] })),
      createEventSubscription: vi.fn(async () => ({ subscription: SUB, signingSecret: 'rcwh_x' })),
    },
  }
})

import { api } from '~/api/client.ts'
import { mount, settle } from '~/test/mount.ts'
import { tag, type ProjectSubscriptionsPage } from './ProjectSubscriptions.ts'

async function page(): Promise<ProjectSubscriptionsPage> {
  window.history.pushState({}, '', '/app/projects/p1/subscriptions')
  const { syncRouteFromLocation } = await import('~/ui/router.ts')
  syncRouteFromLocation()
  const el = document.createElement(tag) as ProjectSubscriptionsPage
  el.routeName = 'projectSubscriptions'
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

function typeInto(el: TextField, value: string): void {
  el.value = value
  el.dispatchEvent(new Event('input', { bubbles: true }))
}

describe('project subscriptions page', () => {
  it('lists the subscriptions of the project and offers only project and workflow scopes', async () => {
    const el = (current = await page())
    await waitFor(() => expect(api.listEventSubscriptions).toHaveBeenCalledWith({ orgId: 'org1', projectId: 'p1' }))
    await waitFor(() => expect(el.querySelectorAll('.subscription-toggle').length).toBe(1))
    const scope = el.querySelector('.subscription-form combo-box') as ComboBox
    expect(scope.options.map((o) => (o as { value: string }).value)).toEqual(['project', 'workflow'])
    expect(scope.value).toBe('project')
  })

  it('creates a workflow subscription for the project, and a refresh keeps a draft', async () => {
    const el = (current = await page())
    await waitFor(() => expect(el.querySelectorAll('.subscription-toggle').length).toBe(1))
    const fields = [...el.querySelectorAll<TextField>('.subscription-form text-field')]
    const [name, url, , workflow, , eventTypes] = fields
    typeInto(name, 'nightly-alerts')
    // A list refresh does not touch the typed text.
    const scope = el.querySelector('.subscription-form combo-box') as ComboBox
    scope.value = 'workflow'
    scope.dispatchEvent(new Event('change', { bubbles: true }))
    ;(el.querySelector('rc-subscriptions-manager') as unknown as { reloadList(): void }).reloadList()
    await settle()
    expect(api.listEventSubscriptions).toHaveBeenCalledTimes(2)
    expect(name.value).toBe('nightly-alerts')
    expect(el.querySelector('.subscription-form text-field')).toBe(name)
    typeInto(url, 'https://hooks.example.com/w')
    typeInto(workflow, 'nightly')
    eventTypes.value = 'workflow.completed'
    await settle()
    const form = el.querySelector('form.subscription-form') as HTMLFormElement
    form.dispatchEvent(new Event('submit', { cancelable: true }))
    await waitFor(() =>
      expect(api.createEventSubscription).toHaveBeenCalledWith({
        orgId: 'org1',
        name: 'nightly-alerts',
        scope: 'workflow',
        projectId: 'p1',
        workflowSecurityId: 'nightly',
        ownerGroup: undefined,
        webhookUrl: 'https://hooks.example.com/w',
        eventTypes: ['workflow.completed'],
        results: [],
        refClassesAny: [],
        actorKinds: [],
        failureClasses: [],
      }),
    )
    await waitFor(() => expect(el.textContent).toContain('rcwh_x'))
  })
})
