import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

/**
 * The job page on a live job.
 *
 * Every job_update refreshes the job. The log viewer and its buffer must not
 * notice: an operator reading an error half way up a log lost their place
 * every time the job's status ticked over, when the panel was rebuilt with the
 * job's content.
 */

let status = 'running'

vi.mock('~/api/client.ts', () => ({
  api: {
    getJob: vi.fn(async ({ jobId }: { jobId: string }) => ({
      job: { jobId, name: 'build', status, projectId: 'p1', queueName: 'q', updatedAt: new Date().toISOString() },
    })),
    cancelJob: vi.fn(async () => ({})),
    getCapabilities: vi.fn(async () => ({ cancelJob: true, killJob: true, retryJob: true })),
    getJobLogs: vi.fn(async () => ({
      entries: [
        { timestamp: 't1', stream: 'stdout', message: 'first' },
        { timestamp: 't2', stream: 'stderr', message: 'second' },
      ],
      nextCursor: 'c1',
    })),
    getJobResourceHistory: vi.fn(async () => ({ name: 'build', runs: [], pending: 0 })),
  },
  ServiceError: class extends Error {},
  TransportFailure: class extends Error {},
}))

import { resetStore } from '~/store/index.ts'
import { mount, settle } from '~/test/mount.ts'
import { syncRouteFromLocation } from '~/ui/router.ts'
import { tag, type JobDetailPage, type LogViewer } from './JobDetail.ts'
import { BUFFER_CAPS } from '~/store/index.ts'

function openJob(id: string): JobDetailPage {
  window.history.pushState({}, '', `/app/jobs/${id}`)
  syncRouteFromLocation()
  const page = document.createElement(tag) as JobDetailPage
  page.routeName = 'job'
  return mount(page)
}

describe('JobDetail', () => {
  beforeEach(() => {
    status = 'running'
    resetStore()
  })
  afterEach(() => {
    vi.clearAllMocks()
    resetStore()
  })

  it('keeps the log viewer, its lines and its scroll position across a job refresh', async () => {
    const page = openJob('j1')
    await settle()

    const viewer = page.querySelector('rc-log-viewer') as LogViewer
    expect(viewer).toBeTruthy()
    expect(viewer.lineCount).toBe(2)
    const viewport = viewer.viewport
    const firstLine = viewport.querySelector('.log-line')
    expect(firstLine?.textContent).toContain('first')
    viewport.scrollTop = 7
    const scrolled = viewport.scrollTop

    // The job advances. Cancel refreshes the job through the store, the same
    // path a job_update takes.
    status = 'completed'
    const { api } = await import('~/api/client.ts')
    const getJob = api.getJob as ReturnType<typeof vi.fn>
    const callsBefore = getJob.mock.calls.length
    const cancel = [...page.querySelectorAll('push-button')].find((b) => b.textContent === 'Cancel') as HTMLElement
    cancel.click()
    await settle()
    expect(getJob.mock.calls.length).toBeGreaterThan(callsBefore)
    // The refreshed job is terminal: the page shows it.
    expect(page.querySelector('.status-text')?.textContent).toBe('completed')

    expect(page.querySelector('rc-log-viewer')).toBe(viewer)
    expect(viewer.viewport).toBe(viewport)
    expect(viewport.querySelector('.log-line')).toBe(firstLine)
    expect(viewport.scrollTop).toBe(scrolled)
  })

  it('appends new lines without making the old ones again, and bounds the buffer', async () => {
    const page = openJob('j2')
    await settle()
    const viewer = page.querySelector('rc-log-viewer') as LogViewer
    const firstLine = viewer.viewport.querySelector('.log-line')

    viewer.appendLines([{ timestamp: 't3', stream: 'stdout', message: 'third' }])
    expect(viewer.viewport.querySelector('.log-line')).toBe(firstLine)
    expect(viewer.lineCount).toBe(3)

    const flood = Array.from({ length: BUFFER_CAPS.logLines + 50 }, (_, i) => ({ timestamp: `f${i}`, stream: 'stdout', message: `line ${i}` }))
    viewer.appendLines(flood)
    expect(viewer.lineCount).toBe(BUFFER_CAPS.logLines)
    expect(viewer.viewport.querySelectorAll('.log-line')).toHaveLength(BUFFER_CAPS.logLines)
    expect(viewer.viewport.lastElementChild?.textContent).toContain(`line ${BUFFER_CAPS.logLines + 49}`)
  })

  it('stops its poll timer and live topic when it leaves the page', async () => {
    const { eventSocket } = await import('~/store/events.ts')
    const page = openJob('j3')
    await settle()
    expect(eventSocket.stats().topics).toBeGreaterThan(0)
    page.remove()
    resetStore()
    expect(eventSocket.stats().topics).toBe(0)
    expect(eventSocket.stats().listeners).toBe(0)
  })

  it('makes the History panel only when its tab is opened, then keeps it', async () => {
    const { api } = await import('~/api/client.ts')
    const page = openJob('j4')
    await settle()
    expect(page.querySelector('rc-resource-history')).toBeNull()
    expect(api.getJobResourceHistory).not.toHaveBeenCalled()

    const tabs = [...page.querySelectorAll<HTMLButtonElement>('.tab')]
    expect(tabs.map((t) => t.textContent)).toEqual(['Logs', 'Resources', 'History'])
    tabs[2].click()
    await settle()
    const history = page.querySelector<HTMLElement>('rc-resource-history')!
    expect(history).toBeTruthy()
    expect(history.hidden).toBe(false)
    expect(tabs[2].getAttribute('aria-selected')).toBe('true')
    expect(api.getJobResourceHistory).toHaveBeenCalledWith({ jobId: 'j4', limit: 200 })

    tabs[0].click()
    await settle()
    expect(history.hidden).toBe(true)
    tabs[2].click()
    await settle()
    expect(page.querySelector('rc-resource-history')).toBe(history)
  })
})
