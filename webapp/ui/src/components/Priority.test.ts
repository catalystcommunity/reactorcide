import { afterEach, describe, expect, it, vi } from 'vitest'
import { waitFor } from '@testing-library/dom'
import type { CheckBox, TextField } from '@catalystcommunity/controls'
import type { ListPriorityLanesResponse, PriorityLane } from '~/api/csilapi/types.gen.ts'

let response: ListPriorityLanesResponse

vi.mock('~/api/client.ts', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/client.ts')>()
  return {
    ...actual,
    api: {
      listPriorityLanes: vi.fn(async () => response),
      putPriorityLane: vi.fn(async (req: { name: string; base: number }) => ({
        lane: { name: req.name, base: req.base, position: 0, match: {}, matchText: 'every job', enabled: true },
      })),
      deletePriorityLane: vi.fn(async () => ({ deleted: true })),
    },
  }
})

import { api } from '~/api/client.ts'
import { mount, settle, unmount } from '~/test/mount.ts'
import { laneMatchText, priorityLaneSettings, priorityText, type PriorityLaneSettings } from './Priority.ts'
import { reasonSummary } from './Reasons.ts'

const lane = (name: string, base: number, position: number, scope: string, extra: Partial<PriorityLane> = {}): PriorityLane => ({
  name,
  base,
  position,
  scope,
  match: {},
  matchText: 'every job',
  enabled: true,
  ...extra,
})

const BOUNDS = { minBase: -500, maxBase: 500, minGroupShift: -50, maxGroupShift: 50, maxRequested: 50, shedPrShift: -100 }

let mounted: Element | null = null

async function settings(organization: string, projectId?: string): Promise<PriorityLaneSettings> {
  const el = priorityLaneSettings(organization, projectId)
  mounted = mount(el)
  await settle()
  await waitFor(() => expect(api.listPriorityLanes).toHaveBeenCalled())
  return el
}

afterEach(() => {
  if (mounted) unmount(mounted)
  mounted = null
  vi.clearAllMocks()
})

describe('priority lane settings', () => {
  it('lists the effective lanes in order with their scope', async () => {
    response = {
      lanes: [lane('pr-ready', -50, 400, 'org', { match: { classesAny: ['pr'], draft: false }, matchText: 'classes any of pr; not a draft' })],
      inherited: [],
      builtin: [lane('eval-pr', 10, 20, 'builtin'), lane('pr-ready', -100, 400, 'builtin')],
      effective: [
        lane('eval-pr', 10, 20, 'builtin', { matchText: 'eval jobs; classes any of pr' }),
        lane('pr-ready', -50, 400, 'org', { replaces: 'builtin', matchText: 'classes any of pr; not a draft' }),
      ],
      bounds: BOUNDS,
      canManage: true,
    }
    const el = await settings('acme')
    expect(api.listPriorityLanes).toHaveBeenCalledWith({ organization: 'acme' })
    await waitFor(() => expect(el.querySelectorAll('table.priority-lanes tbody tr').length).toBe(2))
    const rows = [...el.querySelectorAll('table.priority-lanes tbody tr')]
    expect(rows[0].querySelector('.lane-name')!.textContent).toBe('eval-pr')
    expect(rows[0].querySelector('.lane-base')!.textContent).toBe('+10')
    expect(rows[1].querySelector('.lane-scope')!.textContent).toBe('org (replaces builtin)')
    expect(rows[1].querySelector('.lane-match')!.textContent).toBe('classes any of pr; not a draft')
    // Only a lane of this scope can be removed.
    const removeButtons = rows.map((row) => row.querySelectorAll('push-button')[1] as HTMLElement)
    expect(removeButtons[0].hidden).toBe(true)
    expect(removeButtons[1].hidden).toBe(false)
  })

  it('changes only the base of a built-in lane when no match is set', async () => {
    response = { lanes: [], inherited: [], builtin: [], effective: [lane('schedule', -300, 600, 'builtin')], bounds: BOUNDS, canManage: true }
    const el = await settings('acme')
    await waitFor(() => expect(el.querySelector<HTMLFormElement>('form.priority-lane-form')!.hidden).toBe(false))
    const fields = [...el.querySelectorAll('form.priority-lane-form text-field')] as TextField[]
    // Name, base, position, description, classes, not classes.
    fields[0].value = 'schedule'
    fields[1].value = '-350'
    el.querySelector('form.priority-lane-form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await waitFor(() => expect(api.putPriorityLane).toHaveBeenCalled())
    expect(vi.mocked(api.putPriorityLane).mock.calls[0][0]).toEqual({ organization: 'acme', name: 'schedule', base: -350, enabled: true })
  })

  it('sends the match facts when the person sets them', async () => {
    response = { lanes: [], inherited: [], builtin: [], effective: [], bounds: BOUNDS, canManage: true }
    const el = await settings('acme', 'proj')
    await waitFor(() => expect(el.querySelector<HTMLFormElement>('form.priority-lane-form')!.hidden).toBe(false))
    const fields = [...el.querySelectorAll('form.priority-lane-form text-field')] as TextField[]
    fields[0].value = 'hotfix'
    fields[1].value = '200'
    fields[2].value = '5'
    const setMatch = el.querySelector('form.priority-lane-form check-box') as CheckBox
    setMatch.checked = true
    setMatch.dispatchEvent(new Event('change', { bubbles: true }))
    fields[4].value = 'hotfix, release'
    el.querySelector('form.priority-lane-form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await waitFor(() => expect(api.putPriorityLane).toHaveBeenCalled())
    expect(vi.mocked(api.putPriorityLane).mock.calls[0][0]).toEqual({
      organization: 'acme',
      projectId: 'proj',
      name: 'hotfix',
      base: 200,
      position: 5,
      enabled: true,
      match: { classesAny: ['hotfix', 'release'], classesNone: [], eventTypes: [] },
    })
  })

  it('refuses a base out of bounds, and sends nothing', async () => {
    response = { lanes: [], inherited: [], builtin: [], effective: [], bounds: BOUNDS, canManage: true }
    const el = await settings('acme')
    await waitFor(() => expect(el.querySelector<HTMLFormElement>('form.priority-lane-form')!.hidden).toBe(false))
    const fields = [...el.querySelectorAll('form.priority-lane-form text-field')] as TextField[]
    fields[0].value = 'big'
    fields[1].value = '9000'
    el.querySelector('form.priority-lane-form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await settle()
    expect(api.putPriorityLane).not.toHaveBeenCalled()
    expect(el.textContent).toContain('Use a value from -500 to 500.')
  })

  it('hides editing when the caller cannot manage the scope', async () => {
    response = { lanes: [], inherited: [], builtin: [], effective: [lane('standard', 0, 1000, 'builtin')], bounds: BOUNDS, canManage: false }
    const el = await settings('acme')
    await waitFor(() => expect(el.querySelectorAll('table.priority-lanes tbody tr').length).toBe(1))
    expect(el.querySelector<HTMLFormElement>('form.priority-lane-form')!.hidden).toBe(true)
    expect(el.textContent).toContain('Only an organization administrator can change the organization lanes.')
    expect(el.querySelector('table.priority-lanes push-button')).toBeNull()
  })
})

describe('priority texts', () => {
  it('says a priority reason', () => {
    const reason = {
      kind: 'priority',
      lane: 'pr-ready',
      value: 'builtin',
      base: -100,
      final: -80,
      shifts: [
        { source: 'group', group: 'bots', value: -10, applied: false },
        { source: 'group', group: 'release', value: 20, applied: true },
      ],
    }
    expect(priorityText(reason)).toBe('Priority -80: lane pr-ready (builtin), base -100; group release +20')
    expect(priorityText(undefined, 7)).toBe('Priority 7')
    expect(priorityText(undefined)).toBe('')
  })

  it('says the match of a lane', () => {
    expect(laneMatchText({})).toBe('every job')
    expect(laneMatchText({ jobKind: 'eval', classesAny: ['pr'], draft: true, retry: true })).toBe('eval jobs; classes any of pr; draft; retries only')
  })

  it('keeps the priority reason out of the summary of a node', () => {
    const reasons = [
      { kind: 'dependency', matched: true, condition: 'all_success', deps: [{ name: 'build', status: 'completed' }] },
      { kind: 'priority', lane: 'standard', base: 0, final: 0, shifts: [] },
    ]
    expect(reasonSummary(reasons)).toBe('Condition all_success: build completed')
  })
})
