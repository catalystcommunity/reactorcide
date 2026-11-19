import { afterEach, describe, expect, it, vi } from 'vitest'
import { waitFor } from '@testing-library/dom'
import type { PushButton, TextField } from '@catalystcommunity/controls'
import type { ProjectDetail } from '~/api/csilapi/types.gen.ts'

/**
 * Re-run shifts and manual runs.
 *
 * A retry or a re-run can change only CPU, memory, storage and timeout; the
 * UI sends those as `shifts` and nothing else. A manual run names a branch,
 * a tag or a pull request, and an optional workflow.
 */

let jobStatus = 'failed'

vi.mock('~/api/client.ts', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/client.ts')>()
  return {
    ...actual,
    api: {
      getJob: vi.fn(async ({ jobId }: { jobId: string }) => ({
        job: {
          jobId, name: 'build', status: jobStatus, projectId: 'p1', queueName: 'q', updatedAt: new Date().toISOString(),
          shifts: jobStatus === 'failed' ? { memory: '8Gi', timeout: '2h0m0s' } : undefined,
        },
      })),
      retryJob: vi.fn(async () => ({ jobId: 'j2', status: 'submitted' })),
      getCapabilities: vi.fn(async () => ({ cancelJob: true, killJob: true, retryJob: true })),
      getJobLogs: vi.fn(async () => ({ entries: [], nextCursor: '' })),
      getJobResourceHistory: vi.fn(async () => ({ name: 'build', runs: [], pending: 0 })),
      startRun: vi.fn(async () => ({ eventId: 'e1', status: 'evaluating', ref: 'refs/heads/main', commitSha: 'abcdef1234' })),
    },
  }
})

import { api } from '~/api/client.ts'
import { ShiftFields, describeShifts } from '~/components/ShiftFields.ts'
import { makeCard, type ProjectCard } from '~/components/ProjectCard.ts'
import { resetStore } from '~/store/index.ts'
import { mount, settle } from '~/test/mount.ts'
import { syncRouteFromLocation } from '~/ui/router.ts'
import { tag, type JobDetailPage } from './JobDetail.ts'
import './ProjectEvents.ts'

function type(field: TextField, value: string): void {
  field.value = value
  field.dispatchEvent(new Event('input', { bubbles: true }))
}

function byLabel<T extends Element>(root: Element, selector: string, label: RegExp): T {
  const match = [...root.querySelectorAll(selector)].find((el) => label.test(el.getAttribute('aria-label') ?? ''))
  if (!match) throw new Error(`no ${selector} labelled ${label}`)
  return match as T
}

function pushButton(root: Element, text: string): PushButton {
  const match = [...root.querySelectorAll('push-button')].find((b) => b.textContent === text)
  if (!match) throw new Error(`no button ${text}`)
  return match as PushButton
}

afterEach(() => {
  vi.clearAllMocks()
  jobStatus = 'failed'
})

describe('ShiftFields', () => {
  it('returns only the values typed in, and clears them', () => {
    const shifts = new ShiftFields('retry')
    mount(shifts.el)
    expect(shifts.value()).toBeUndefined()
    type(byLabel<TextField>(shifts.el, 'text-field', /^Memory limit/), ' 8Gi ')
    type(byLabel<TextField>(shifts.el, 'text-field', /^Timeout/), '2h')
    expect(shifts.value()).toEqual({ memory: '8Gi', timeout: '2h' })
    shifts.clear()
    expect(shifts.value()).toBeUndefined()
    expect(describeShifts({ cpu: '4', storage: '20Gi' })).toBe('CPU 4, storage 20Gi')
  })
})

describe('Job retry with shifts', () => {
  it('sends the shifts with the retry and shows the recorded shifts', async () => {
    resetStore()
    window.history.pushState({}, '', '/app/jobs/j1')
    syncRouteFromLocation()
    const page = document.createElement(tag) as JobDetailPage
    page.routeName = 'job'
    mount(page)
    await settle()
    await settle()
    expect(page.textContent).toContain('memory 8Gi, timeout 2h0m0s')
    const fields = page.querySelector('details.shift-fields') as HTMLDetailsElement
    expect(fields.hidden).toBe(false)
    const memory = byLabel<TextField>(fields, 'text-field', /^Memory limit/)
    type(memory, '16Gi')
    pushButton(page, 'Retry').click()
    await waitFor(() => expect(api.retryJob).toHaveBeenCalledWith({ jobId: 'j1', shifts: { memory: '16Gi' } }))
    await waitFor(() => expect(memory.value).toBe(''))
  })

  it('sends no shifts for a plain retry', async () => {
    resetStore()
    window.history.pushState({}, '', '/app/jobs/j3')
    syncRouteFromLocation()
    const page = document.createElement(tag) as JobDetailPage
    page.routeName = 'job'
    mount(page)
    await settle()
    await settle()
    pushButton(page, 'Retry').click()
    await waitFor(() => expect(api.retryJob).toHaveBeenCalledWith({ jobId: 'j3', shifts: undefined }))
  })
})

describe('Run card', () => {
  async function runCard(): Promise<ProjectCard> {
    const el = makeCard<ProjectCard>('rc-project-run-card', {})
    el.setProject({ projectId: 'p1', name: 'Widget', enabled: true } as unknown as ProjectDetail)
    mount(el)
    await settle()
    return el
  }

  it('starts a run on a ref with an optional workflow', async () => {
    const el = await runCard()
    const form = el.querySelector('form')!
    form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await settle()
    expect(api.startRun).not.toHaveBeenCalled()
    expect(el.textContent).toContain('Give a branch or tag, or a pull request.')

    type(byLabel<TextField>(el, 'text-field', /^Branch or tag/), 'main')
    type(byLabel<TextField>(el, 'text-field', /^Workflow/), 'build')
    await settle()
    form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await waitFor(() =>
      expect(api.startRun).toHaveBeenCalledWith({ projectId: 'p1', ref: 'main', prNumber: undefined, workflow: 'build' }),
    )
    await waitFor(() => expect(el.textContent).toContain('Started on refs/heads/main (abcdef1).'))
    expect(el.querySelector('a[href="/app/events/e1"]')).toBeTruthy()
  })

  it('starts a run on a pull request head, and refuses both or a bad number', async () => {
    const el = await runCard()
    const form = el.querySelector('form')!
    type(byLabel<TextField>(el, 'text-field', /^Branch or tag/), 'main')
    type(byLabel<TextField>(el, 'text-field', /^Pull request/), '12')
    form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await settle()
    expect(el.textContent).toContain('not both')
    type(byLabel<TextField>(el, 'text-field', /^Branch or tag/), '')
    type(byLabel<TextField>(el, 'text-field', /^Pull request/), 'x')
    form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await settle()
    expect(el.textContent).toContain('positive whole number')
    type(byLabel<TextField>(el, 'text-field', /^Pull request/), '12')
    form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await waitFor(() =>
      expect(api.startRun).toHaveBeenCalledWith({ projectId: 'p1', ref: undefined, prNumber: 12, workflow: undefined }),
    )
  })
})
