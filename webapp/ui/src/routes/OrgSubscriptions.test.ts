import { describe, expect, it, vi } from 'vitest'
import { waitFor, within } from '@testing-library/dom'
import type { EventDeliverySummary, EventSubscriptionSummary, OrgSummary } from '~/api/csilapi/types.gen.ts'
import type { SessionSummary } from '~/api/auth.ts'

const ORGS: OrgSummary[] = [{ orgId: 'org-a', name: 'alpha', displayName: 'Alpha', status: 'active', isDefault: true, isPrivate: false }]

const SUB: EventSubscriptionSummary = {
  subscriptionId: 's-1',
  orgId: 'org-a',
  name: 'diagnosis',
  description: '',
  ownerPrincipalId: 'p-1',
  ownerActive: true,
  scope: 'org',
  workflowSecurityId: '',
  ownerGroup: '',
  eventTypes: ['node.post_complete'],
  refClassesAny: [],
  results: ['failed'],
  actorKinds: [],
  sinkType: 'webhook',
  webhookUrl: 'https://hooks.example.com/rc',
  maxAttempts: 0,
  enabled: true,
  createdAt: '',
  updatedAt: '',
}

const DEAD: EventDeliverySummary = {
  deliveryId: 'd-1',
  subscriptionId: 's-1',
  eventId: 'e-1',
  eventType: 'node.post_complete',
  orderKey: 'workflow:w-1',
  sequence: 3,
  status: 'dead_letter',
  attempts: 12,
  lastHttpStatus: 500,
  lastLatencyMs: 40,
  lastError: 'the receiver answered HTTP 500',
  nextAttemptAt: '',
  createdAt: '',
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
      listEventSubscriptions: vi.fn(async () => ({ subscriptions: [SUB] })),
      getEventSubscription: vi.fn(async () => ({ subscription: SUB })),
      listEventDeliveries: vi.fn(async () => ({ deliveries: [DEAD, { ...DEAD, deliveryId: 'd-2', status: 'pending', attempts: 1 }] })),
      createEventSubscription: vi.fn(async () => ({ subscription: SUB, signingSecret: 'rcwh_secret-value' })),
      updateEventSubscription: vi.fn(async () => ({ subscription: SUB })),
      deleteEventSubscription: vi.fn(async () => ({ ok: true })),
      replayEventDelivery: vi.fn(async () => ({ replayed: 1 })),
      testEventSubscription: vi.fn(async () => ({ delivered: true, httpStatus: 204, latencyMs: 12, error: '', refused: false })),
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
import { tag, type OrgSubscriptionsPage } from './OrgSubscriptions.ts'

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
  window.history.pushState({}, '', '/app/org/subscriptions')
  syncRouteFromLocation()
  const page = document.createElement(tag) as OrgSubscriptionsPage
  page.routeName = 'orgSubscriptions'
  mount(page)
  await settle()
  return page
}

describe('OrgSubscriptions', () => {
  it('refuses a caller who administers no organization', async () => {
    setSessionForTest({ ...adminSession(), roles: [] })
    const page = await renderPage()
    expect(within(page).getByText('You do not have permission to manage event subscriptions.').closest('[hidden]')).toBeNull()
    expect(api.listEventSubscriptions).not.toHaveBeenCalled()
  })

  it('lists subscriptions and shows the delivery log with replay', async () => {
    setSessionForTest(adminSession())
    const page = await renderPage()
    await waitFor(() => expect(api.listEventSubscriptions).toHaveBeenCalledWith({ orgId: 'org-a' }))
    await waitFor(() => expect(page.querySelectorAll('.subscription-toggle').length).toBe(1))
    expect(page.querySelector('.subscription-events')!.textContent).toBe('node.post_complete')
    const toggle = page.querySelector('.subscription-toggle') as HTMLElement
    toggle.click()
    await settle()
    await waitFor(() => expect(api.listEventDeliveries).toHaveBeenCalledWith({ subscriptionId: 's-1', limit: 50 }))
    await waitFor(() => expect(page.querySelectorAll('.delivery-log tbody tr').length).toBe(2))
    const rows = page.querySelectorAll('.delivery-log tbody tr')
    expect(rows[0].querySelector('.delivery-status')!.textContent).toBe('dead letter')
    expect(rows[0].querySelector('.delivery-last')!.textContent).toContain('HTTP 500')
    const replays = page.querySelectorAll<HTMLElement>('.delivery-replay')
    expect(replays[0].hidden).toBe(false)
    expect(replays[1].hidden, 'an open delivery has no replay').toBe(true)
    replays[0].click()
    await waitFor(() => expect(api.replayEventDelivery).toHaveBeenCalledWith({ subscriptionId: 's-1', deliveryId: 'd-1' }))

    // A refresh keeps the row elements: no rebuild from data.
    const before = page.querySelector('.delivery-log tbody tr')
    await settle()
    expect(page.querySelector('.delivery-log tbody tr')).toBe(before)
  })
})
