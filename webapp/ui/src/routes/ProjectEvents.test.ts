import { afterEach, describe, expect, it, vi } from 'vitest'
import { waitFor } from '@testing-library/dom'
import type { CheckBox, ComboBox, PushButton, TextField } from '@catalystcommunity/controls'
import type { ProjectDetail, ScheduleSummary } from '~/api/csilapi/types.gen.ts'

const SCHEDULE: ScheduleSummary = {
  scheduleId: 's1',
  projectId: 'p1',
  name: 'nightly',
  branch: 'main',
  workflowRef: 'nightly',
  cron: '0 3 * * *',
  timezone: 'UTC',
  enabled: true,
  nextFireAt: '2026-11-15T03:00:00Z',
  lastFireAt: '2026-11-14T03:00:00Z',
  lastEventId: 'e9',
  lastError: '',
  createdAt: '2026-11-01T00:00:00Z',
  updatedAt: '2026-11-01T00:00:00Z',
}

let schedules: ScheduleSummary[] = []

vi.mock('~/api/client.ts', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/client.ts')>()
  return {
    ...actual,
    api: {
      listSchedules: vi.fn(async () => ({ schedules })),
      createSchedule: vi.fn(async () => ({ schedule: SCHEDULE })),
      updateSchedule: vi.fn(async () => ({ schedule: SCHEDULE })),
      deleteSchedule: vi.fn(async () => ({ ok: true })),
      listScheduleFires: vi.fn(async () => ({
        fires: [{ fireId: 'f1', scheduleId: 's1', fireTime: '2026-11-14T03:00:00Z', status: 'failed', error: 'workflow not found' }],
      })),
      updateProject: vi.fn(async () => ({})),
    },
  }
})

vi.mock('~/ui/controls.ts', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/ui/controls.ts')>()
  return { ...actual, confirmAction: vi.fn(async () => true) }
})

import { api } from '~/api/client.ts'
import { makeCard, type ProjectCard } from '~/components/ProjectCard.ts'
import { mount, settle } from '~/test/mount.ts'
import { confirmAction } from '~/ui/controls.ts'
import './ProjectEvents.ts'

const PROJECT = {
  projectId: 'p1',
  orgId: 'org1',
  name: 'Widget',
  duplicateEvents: 'inherit',
  effectiveDuplicateEvents: 'run',
  dynamicJobs: 'deny',
  effectiveDynamicJobs: 'deny',
} as unknown as ProjectDetail

async function card(tag: string, props: Record<string, unknown> = {}): Promise<ProjectCard> {
  const el = makeCard<ProjectCard>(tag, props as Partial<ProjectCard>)
  el.setProject(PROJECT)
  mount(el)
  await settle()
  await settle()
  return el
}

function byLabel<T extends Element>(root: Element, selector: string, label: RegExp): T {
  const match = [...root.querySelectorAll(selector)].find((el) => label.test(el.getAttribute('aria-label') ?? ''))
  if (!match) throw new Error(`no ${selector} labelled ${label}`)
  return match as T
}

function type(field: TextField, value: string): void {
  field.value = value
  field.dispatchEvent(new Event('input', { bubbles: true }))
}

function pushButton(root: Element, text: string): PushButton {
  const match = [...root.querySelectorAll('push-button')].find((b) => b.textContent === text)
  if (!match) throw new Error(`no button ${text}`)
  return match as PushButton
}

afterEach(() => {
  vi.clearAllMocks()
  schedules = []
})

describe('Schedules card', () => {
  it('lists the schedules with their state and last fire', async () => {
    schedules = [{ ...SCHEDULE, lastError: 'workflow not found' }]
    const el = await card('rc-project-schedules-card')
    expect(api.listSchedules).toHaveBeenCalledWith({ projectId: 'p1' })
    const row = el.querySelector('tbody tr')!
    expect(row.querySelector('.sch-name')?.textContent).toBe('nightly')
    expect(row.querySelector('.sch-cron')?.textContent).toBe('0 3 * * *')
    expect(row.querySelector<CheckBox>('check-box')!.checked).toBe(true)
    expect(row.querySelector('.sch-last')?.getAttribute('href')).toBe('/app/events/e9')
    expect(row.querySelector('.sch-error')?.textContent).toBe('workflow not found')
  })

  it('creates a schedule from the form, and refuses an incomplete one', async () => {
    const el = await card('rc-project-schedules-card')
    const form = el.querySelector('form')!
    form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await settle()
    expect(api.createSchedule).not.toHaveBeenCalled()
    expect(el.textContent).toContain('A schedule needs a name.')

    type(byLabel<TextField>(el, 'text-field', /^Name/), 'nightly')
    type(byLabel<TextField>(el, 'text-field', /^Branch/), 'main')
    type(byLabel<TextField>(el, 'text-field', /^Workflow/), 'nightly')
    type(byLabel<TextField>(el, 'text-field', /^Cron/), '0 3 * * *')
    await settle()
    form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await waitFor(() =>
      expect(api.createSchedule).toHaveBeenCalledWith({
        projectId: 'p1',
        name: 'nightly',
        branch: 'main',
        workflowRef: 'nightly',
        cron: '0 3 * * *',
        timezone: 'UTC',
        enabled: true,
      }),
    )
    await waitFor(() => expect(api.listSchedules).toHaveBeenCalledTimes(2))
  })

  it('edits, disables and deletes a schedule', async () => {
    schedules = [SCHEDULE]
    const el = await card('rc-project-schedules-card')
    const row = el.querySelector('tbody tr')!

    const enabled = row.querySelector<CheckBox>('check-box')!
    enabled.checked = false
    enabled.dispatchEvent(new Event('change', { bubbles: true }))
    await waitFor(() => expect(api.updateSchedule).toHaveBeenCalledWith({ scheduleId: 's1', enabled: false }))

    pushButton(row, 'Edit').click()
    await settle()
    const cron = byLabel<TextField>(el, 'text-field', /^Cron/)
    expect(cron.value).toBe('0 3 * * *')
    type(cron, '@daily')
    await settle()
    el.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await waitFor(() => expect(api.updateSchedule).toHaveBeenCalledWith(expect.objectContaining({ scheduleId: 's1', cron: '@daily' })))

    pushButton(el.querySelector('tbody tr')!, 'Delete').click()
    await waitFor(() => expect(api.deleteSchedule).toHaveBeenCalledWith({ scheduleId: 's1' }))
    expect(confirmAction).toHaveBeenCalled()
  })

  it('shows the recent fires of a schedule', async () => {
    schedules = [SCHEDULE]
    const el = await card('rc-project-schedules-card')
    pushButton(el.querySelector('tbody tr')!, 'Fires').click()
    await settle()
    await waitFor(() => expect(api.listScheduleFires).toHaveBeenCalledWith({ scheduleId: 's1', limit: 20 }))
    await settle()
    const panel = el.querySelector<HTMLElement>('.schedule-fires')!
    expect(panel.hidden).toBe(false)
    await waitFor(() => expect(panel.querySelector('.fire-error')?.textContent).toBe('workflow not found'))
  })
})

describe('Event settings card', () => {
  it('shows the value in force and saves the chosen settings', async () => {
    const onSaved = vi.fn(async () => {})
    const el = await card('rc-project-event-settings-card', { initial: PROJECT, onSaved })
    expect(el.textContent).toContain('In force now: Run again.')
    expect(el.textContent).toContain('In force now: Deny.')

    const combos = [...el.querySelectorAll<ComboBox>('combo-box')]
    expect(combos.map((c) => c.value)).toEqual(['inherit', 'deny'])
    combos[0].value = 'skip'
    combos[0].dispatchEvent(new Event('change', { bubbles: true }))
    el.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await waitFor(() => expect(api.updateProject).toHaveBeenCalledWith({ projectId: 'p1', duplicateEvents: 'skip', dynamicJobs: 'deny' }))
    await waitFor(() => expect(onSaved).toHaveBeenCalled())
  })
})
