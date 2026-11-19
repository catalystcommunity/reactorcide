import { beforeEach, describe, expect, it, vi } from 'vitest'
import { waitFor } from '@testing-library/dom'
import type { GetWorkerImageStateResponse, WorkerSummary } from '~/api/csilapi/types.gen.ts'
import type { SessionSummary } from '~/api/auth.ts'

const D1 = 'sha256:' + '1'.repeat(64)
const D2 = 'sha256:' + '2'.repeat(64)
const D3 = 'sha256:' + '3'.repeat(64)

function worker(extra: Partial<WorkerSummary> = {}): WorkerSummary {
  return {
    workerId: 'w-1',
    poolId: 'pool-1',
    workerKey: 'key-1',
    hostname: 'mac-1',
    os: 'macos',
    arch: 'arm64',
    characteristics: [],
    status: 'active',
    activeLeaseCount: 0,
    online: true,
    imageStatus: 'image update failed',
    imageStatusDetail: 'no space left on device',
    imageRuntime: 'vm',
    imageSetVersion: 5,
    imageReportedVersion: 4,
    ...extra,
  }
}

let workers: WorkerSummary[] = []
let state: GetWorkerImageStateResponse

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
      listWorkers: vi.fn(async () => ({ workers })),
      listPools: vi.fn(async () => ({ pools: [] })),
      getWorkerImageState: vi.fn(async () => state),
    },
  }
})

import { api } from '~/api/client.ts'
import { imageFailureText, imageStatusClass, imageVersionText, jobImageText, rolloutPoolText, parseCharacteristics, aliasNameError, aliasRefError } from '~/components/imageText.ts'
import { setSessionForTest } from '~/lib/session.ts'
import { mount, settle } from '~/test/mount.ts'
import { syncRouteFromLocation } from '~/ui/router.ts'
import { tag, type WorkerDetailPage } from './WorkerDetail.ts'
import { tag as workersTag, type WorkersPage } from './Workers.ts'

function session(manageWorkers: boolean): SessionSummary {
  return {
    logged_in: true,
    user_id: 'u1',
    display_name: 'Admin',
    is_global_admin: manageWorkers,
    capabilities: { manageWorkers } as SessionSummary['capabilities'],
    roles: [],
  }
}

async function openWorker(id: string): Promise<WorkerDetailPage> {
  window.history.pushState({}, '', `/app/workers/${id}`)
  syncRouteFromLocation()
  const page = document.createElement(tag) as WorkerDetailPage
  page.routeName = 'worker'
  mount(page)
  await settle()
  return page
}

beforeEach(() => {
  vi.clearAllMocks()
  document.body.replaceChildren()
  workers = [worker()]
  state = {
    workerId: 'w-1',
    imageAware: true,
    imageRuntime: 'vm',
    imageStatus: 'image update failed',
    imageStatusDetail: 'no space left on device',
    imageSetVersion: 5,
    imageReportedVersion: 4,
    state: 'failed',
    readyDigests: [D1, D3],
    images: [
      { ref: `registry.example.com/vm/mac:1@${D1}`, digest: D1, kind: 'vm', aliases: ['mac', 'mac-xcode'], ready: true },
      { ref: `registry.example.com/vm/mac:2@${D2}`, digest: D2, kind: 'vm', aliases: ['mac'], ready: false, pending: true },
    ],
  }
})

describe('image text helpers', () => {
  it('shows alias (ref) only when the job used an alias', () => {
    expect(jobImageText({ runnerImage: 'alpine:3', imageDisplay: 'mac (r@sha256:x)' })).toBe('mac (r@sha256:x)')
    expect(jobImageText({ runnerImage: 'r@sha256:x', imageAlias: 'mac' })).toBe('mac (r@sha256:x)')
    expect(jobImageText({ runnerImage: 'alpine:3' })).toBe('alpine:3')
  })

  it('maps the three image statuses to their look', () => {
    expect(imageStatusClass('healthy')).toContain('status-ok')
    expect(imageStatusClass('updating images')).toContain('status-info')
    expect(imageStatusClass('image update failed')).toContain('status-danger')
    expect(imageVersionText(3, undefined)).toBe('v3 (no report)')
    expect(imageFailureText({ permanent: true, detail: 'denied' })).toBe('failed (permanent): denied')
    expect(imageFailureText({ permanent: false, detail: ' ' })).toBe('failed (will retry)')
    expect(rolloutPoolText({ poolName: 'p', workers: 1, onlineWorkers: 1, readyWorkers: 0, failedWorkers: 0 })).toBe('p: 1 worker, 1 online, 0 ready, 0 failed')
  })

  it('validates names, references and characteristics as the coordinator does', () => {
    expect(aliasNameError('win-11')).toBeUndefined()
    expect(aliasNameError('-win')).toBeDefined()
    expect(aliasNameError('Win')).toBeDefined()
    expect(aliasRefError(`r/x:1@${D1}`)).toBeUndefined()
    expect(aliasRefError('r/x:1')).toBeDefined()
    expect(parseCharacteristics(' a=1 , b=x ')).toEqual({ entries: [{ key: 'a', value: '1' }, { key: 'b', value: 'x' }] })
    expect(parseCharacteristics('os=linux')).toHaveProperty('error')
    expect(parseCharacteristics('a=1,a=2')).toHaveProperty('error')
    expect(parseCharacteristics('nokey')).toHaveProperty('error')
    expect(parseCharacteristics('')).toEqual({ entries: [] })
  })
})

describe('worker detail page', () => {
  it('shows the image status, the versions, and the image set with alias names', async () => {
    setSessionForTest(session(true))
    const page = await openWorker('w-1')
    await waitFor(() => expect(api.getWorkerImageState).toHaveBeenCalledWith({ workerId: 'w-1' }))
    await waitFor(() => expect(page.querySelectorAll('.worker-images tbody tr').length).toBe(2))
    expect(page.querySelector('h1')!.textContent).toBe('Worker mac-1')
    expect(page.querySelector('.worker-online')!.textContent).toBe('online')
    expect(page.querySelector('.worker-image-version')!.textContent).toBe('v5 (reported v4)')
    const card = page.querySelector('rc-worker-image-state')!
    expect(card.querySelector('.worker-image-facts span')!.textContent).toBe('image update failed')
    expect(card.querySelector('.worker-image-facts span')!.className).toContain('status-danger')
    expect(card.querySelector('.worker-image-detail')!.textContent).toBe('no space left on device')
    const rows = [...card.querySelectorAll('.worker-images tbody tr')]
    expect(rows[0].querySelector('.image-aliases')!.textContent).toBe('mac, mac-xcode')
    expect(rows[0].querySelector('.image-entry-ready')!.textContent).toBe('ready')
    expect(rows[1].querySelector('.image-entry-ready')!.textContent).toBe('not ready')
    expect((rows[0].querySelector('.image-entry-pending') as HTMLElement).hidden).toBe(true)
    expect((rows[1].querySelector('.image-entry-pending') as HTMLElement).hidden).toBe(false)
    expect(card.querySelector('.worker-image-extra')!.textContent).toContain('sha256:333333333333')
  })

  it('shows the pull failures of the images, permanent or transient', async () => {
    setSessionForTest(session(true))
    state = {
      ...state,
      images: [
        { ...state.images[0] },
        { ...state.images[1], failure: { digest: D2, permanent: true, detail: 'manifest unknown' } },
      ],
      failed: [
        { digest: D2, permanent: true, detail: 'manifest unknown' },
        { digest: D3, permanent: false, detail: 'i/o timeout' },
      ],
    }
    const page = await openWorker('w-1')
    await waitFor(() => expect(page.querySelectorAll('.worker-images tbody tr').length).toBe(2))
    const card = page.querySelector('rc-worker-image-state')!
    const rows = [...card.querySelectorAll('.worker-images tbody tr')]
    expect((rows[0].querySelector('.image-entry-failure') as HTMLElement).hidden).toBe(true)
    const failure = rows[1].querySelector('.image-entry-failure') as HTMLElement
    expect(failure.hidden).toBe(false)
    expect(failure.textContent).toBe('failed (permanent): manifest unknown')
    expect(failure.className).toContain('image-failure-permanent')
    const list = card.querySelector('.worker-image-failed') as HTMLElement
    expect(list.hidden).toBe(false)
    const items = [...list.querySelectorAll('li')].map((li) => li.textContent)
    expect(items).toEqual(['sha256:222222222222 failed (permanent): manifest unknown', 'sha256:333333333333 failed (will retry): i/o timeout'])
  })

  it('hides the failure list when the report has no failures', async () => {
    setSessionForTest(session(true))
    const page = await openWorker('w-1')
    await waitFor(() => expect(page.querySelectorAll('.worker-images tbody tr').length).toBe(2))
    expect((page.querySelector('rc-worker-image-state .worker-image-failed') as HTMLElement).hidden).toBe(true)
  })

  it('says so for a worker that does not use image sets', async () => {
    setSessionForTest(session(true))
    workers = [worker({ imageStatus: undefined, imageStatusDetail: undefined, imageRuntime: undefined, imageSetVersion: undefined, imageReportedVersion: undefined })]
    state = { workerId: 'w-1', imageAware: false, imageSetVersion: 0, readyDigests: [], images: [] }
    const page = await openWorker('w-1')
    await waitFor(() => expect((page.querySelector('.worker-image-not-aware') as HTMLElement).hidden).toBe(false))
    expect((page.querySelector('rc-worker-image-state .worker-image-facts') as HTMLElement).hidden).toBe(true)
  })

  it('shows a refusal and loads nothing without the workers capability', async () => {
    setSessionForTest(session(false))
    const page = await openWorker('w-1')
    expect(page.textContent).toContain('You do not have permission to manage workers.')
    expect(api.listWorkers).not.toHaveBeenCalled()
    expect(api.getWorkerImageState).not.toHaveBeenCalled()
  })
})

describe('workers list', () => {
  it('shows the image status, its detail, the online state and links each worker', async () => {
    setSessionForTest(session(true))
    workers = [worker(), worker({ workerId: 'w-2', hostname: 'linux-1', imageStatus: 'healthy', imageStatusDetail: undefined, online: false, imageReportedVersion: 5 })]
    window.history.pushState({}, '', '/app/workers')
    syncRouteFromLocation()
    const page = document.createElement(workersTag) as WorkersPage
    page.routeName = 'workers'
    mount(page)
    await settle()
    await waitFor(() => expect(page.querySelectorAll('.worker-image-status').length).toBe(2))
    const [failed, healthy] = [...page.querySelectorAll('.worker-image-status')]
    expect(failed.textContent).toBe('image update failed')
    expect(healthy.textContent).toBe('healthy')
    expect(healthy.className).toContain('status-ok')
    const details = [...page.querySelectorAll<HTMLElement>('.worker-image-detail')]
    expect(details[0].textContent).toBe('no space left on device')
    expect(details[1].hidden).toBe(true)
    expect([...page.querySelectorAll('.worker-online')].map((e) => e.textContent)).toEqual(['online', 'offline'])
    expect(page.querySelector('a.worker-name')!.getAttribute('href')).toBe('/app/workers/w-1')
  })
})
