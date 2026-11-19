import { describe, expect, it, vi } from 'vitest'
import { waitFor } from '@testing-library/dom'
import type { CheckBox, PushButton } from '@catalystcommunity/controls'
import type { EnrollmentTokenSummary, WorkerSummary } from '~/api/csilapi/types.gen.ts'
import type { SessionSummary } from '~/api/auth.ts'

function worker(id: string, extra: Partial<WorkerSummary>): WorkerSummary {
  return { workerId: id, poolId: 'pool-1', workerKey: `key-${id}`, os: 'linux', arch: 'amd64', characteristics: [], status: 'active', activeLeaseCount: 0, ...extra }
}

const WORKERS: WorkerSummary[] = [
  worker('w-old', { hostname: 'old-host', enrollmentTokenId: 'tok-old', enrollmentTokenName: '2025 token', enrollmentTokenActive: false, onDeactivatedToken: true }),
  worker('w-new', { hostname: 'new-host', enrollmentTokenId: 'tok-new', enrollmentTokenActive: true, onDeactivatedToken: false }),
]

const TOKENS: EnrollmentTokenSummary[] = [
  { tokenId: 'tok-new', poolId: 'pool-1', isActive: true, createdAt: '2026-11-01T00:00:00Z', activeWorkerCount: 1 },
  { tokenId: 'tok-old', poolId: 'pool-1', name: '2025 token', isActive: false, createdAt: '2025-01-01T00:00:00Z', activeWorkerCount: 1 },
]

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
      listPools: vi.fn(async () => ({ pools: [{ poolId: 'pool-1', name: 'main', createdAt: '2026-01-01T00:00:00Z', updatedAt: '2026-01-01T00:00:00Z' }] })),
      listWorkers: vi.fn(async () => ({ workers: WORKERS })),
      listEnrollmentTokens: vi.fn(async () => ({ tokens: TOKENS })),
      deactivateEnrollmentToken: vi.fn(async (req: { tokenId: string }) => ({ summary: { ...TOKENS[0], tokenId: req.tokenId, isActive: false } })),
    },
  }
})

vi.mock('~/ui/controls.ts', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/ui/controls.ts')>()
  return { ...actual, confirmAction: vi.fn(async () => true) }
})

import { api } from '~/api/client.ts'
import { setSessionForTest } from '~/lib/session.ts'
import { confirmAction } from '~/ui/controls.ts'
import { syncRouteFromLocation } from '~/ui/router.ts'
import { mount, settle } from '~/test/mount.ts'
import { deactivateText, filterWorkers, tag, tokenLabel, type WorkersPage } from './Workers.ts'

function adminSession(): SessionSummary {
  return {
    logged_in: true,
    user_id: 'user-1',
    display_name: 'Admin',
    is_global_admin: true,
    capabilities: { manageWorkers: true } as SessionSummary['capabilities'],
    roles: [],
  }
}

async function renderPage(): Promise<HTMLElement> {
  window.history.pushState({}, '', '/app/workers')
  syncRouteFromLocation()
  const page = document.createElement(tag) as WorkersPage
  page.routeName = 'workers'
  mount(page)
  await settle()
  return page
}

function workerHosts(page: HTMLElement): string[] {
  return [...page.querySelectorAll('.worker-name')].filter((el) => !el.closest('tr')?.hidden).map((el) => el.textContent ?? '')
}

describe('enrollment token helpers', () => {
  it('filters workers by deactivated token and by token ID', () => {
    expect(filterWorkers(WORKERS, false, undefined).map((w) => w.workerId)).toEqual(['w-old', 'w-new'])
    expect(filterWorkers(WORKERS, true, undefined).map((w) => w.workerId)).toEqual(['w-old'])
    expect(filterWorkers(WORKERS, false, 'tok-new').map((w) => w.workerId)).toEqual(['w-new'])
    expect(filterWorkers(WORKERS, true, 'tok-new')).toEqual([])
  })

  it('labels a token by name and ID, and says deactivation is permanent', () => {
    expect(tokenLabel(TOKENS[0])).toBe('tok-new')
    expect(tokenLabel(TOKENS[1])).toBe('2025 token (tok-old)')
    expect(deactivateText(TOKENS[0])).toContain('permanent')
    expect(deactivateText(TOKENS[0])).toContain('1 worker runs on this token')
    expect(deactivateText({ ...TOKENS[0], activeWorkerCount: 0 })).toContain('No worker with an active session')
  })
})

describe('Workers page enrollment tokens', () => {
  it('shows the token of each worker and warns about a deactivated token', async () => {
    setSessionForTest(adminSession())
    const page = await renderPage()
    await waitFor(() => expect(workerHosts(page)).toEqual(['old-host', 'new-host']))
    const warning = page.querySelector<HTMLElement>('.alert-error[role=alert]:not([hidden])')
    expect(warning?.textContent).toContain('1 worker runs on a deactivated enrollment token')
    const rows = [...page.querySelectorAll('.worker-token-id')].map((el) => el.textContent)
    expect(rows).toEqual(['tok-old', 'tok-new'])
    const badges = [...page.querySelectorAll<HTMLElement>('.worker-token-warn')].map((el) => !el.hidden)
    expect(badges).toEqual([true, false])
  })

  it('filters to the workers on a deactivated token', async () => {
    setSessionForTest(adminSession())
    const page = await renderPage()
    await waitFor(() => expect(workerHosts(page).length).toBe(2))
    const box = [...page.querySelectorAll<CheckBox>('check-box')].find((el) => el.textContent?.includes('deactivated token'))!
    box.checked = true
    box.dispatchEvent(new Event('change', { bubbles: true }))
    await settle()
    expect(workerHosts(page)).toEqual(['old-host'])
  })

  it('asks for confirmation and sends confirm_permanent on deactivate', async () => {
    setSessionForTest(adminSession())
    const page = await renderPage()
    await waitFor(() => expect(page.querySelector('tbody push-button')).not.toBeNull())
    const toggle = [...page.querySelectorAll<PushButton>('push-button')].find((el) => el.textContent?.includes('main'))!
    toggle.click()
    await waitFor(() => expect(page.querySelectorAll('.token-id').length).toBe(2))
    expect([...page.querySelectorAll('.token-id')].map((el) => el.textContent)).toEqual(['tok-new', 'tok-old'])
    const deactivate = [...page.querySelectorAll<PushButton>('rc-enrollment-tokens push-button')].find(
      (el) => el.textContent === 'Deactivate' && !el.hidden,
    )!
    deactivate.click()
    await waitFor(() => expect(api.deactivateEnrollmentToken).toHaveBeenCalled())
    expect(vi.mocked(confirmAction).mock.calls[0][0]).toContain('permanent')
    expect(vi.mocked(api.deactivateEnrollmentToken).mock.calls[0][0]).toEqual({ tokenId: 'tok-new', confirmPermanent: true })
  })
})
