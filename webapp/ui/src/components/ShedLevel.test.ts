import { afterEach, describe, expect, it, vi } from 'vitest'
import { waitFor } from '@testing-library/dom'
import type { ComboBox } from '@catalystcommunity/controls'
import type { ShedActions, ShedState } from '~/api/csilapi/types.gen.ts'

const none: ShedActions = { skipDrafts: false, pauseSchedules: false, lowerPrPriority: false, skipAdvisory: false, refuseManualRuns: false }
const degraded: ShedActions = { ...none, skipDrafts: true, pauseSchedules: true, lowerPrPriority: true }
const critical: ShedActions = { ...degraded, skipAdvisory: true, refuseManualRuns: true }

let state: ShedState

vi.mock('~/api/client.ts', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/client.ts')>()
  return {
    ...actual,
    api: {
      getShedState: vi.fn(async () => ({ state })),
      setShedActions: vi.fn(async (req: { orgId: string; degraded?: ShedActions; critical?: ShedActions; reset?: boolean }) => {
        state = { ...state, degradedActions: req.degraded ?? degraded, criticalActions: req.critical ?? critical }
        return { state }
      }),
      setShedLevel: vi.fn(async (req: { level: string; orgId?: string }) => {
        state = req.orgId ? { ...state, orgLevel: req.level, level: req.level, actions: degraded } : { ...state, globalLevel: req.level, level: req.level }
        return { state }
      }),
    },
  }
})

import { api } from '~/api/client.ts'
import { mount, settle, unmount } from '~/test/mount.ts'
import { shedActionTexts, shedLevelCard } from './ShedLevel.ts'

let mounted: Element | null = null

afterEach(() => {
  if (mounted) unmount(mounted)
  mounted = null
  vi.clearAllMocks()
})

describe('shed level card', () => {
  it('loads the global level and saves a change without an organization', async () => {
    state = { globalLevel: 'normal', level: 'normal', actions: none, degradedActions: degraded, criticalActions: critical }
    const el = shedLevelCard()
    mounted = mount(el)
    await settle()
    await waitFor(() => expect(el.textContent).toContain('In force now: Normal.'))
    expect(api.getShedState).toHaveBeenCalledWith({})
    expect(el.textContent).toContain('No work is skipped.')
    const combo = el.querySelector('combo-box') as ComboBox
    combo.value = 'critical'
    combo.dispatchEvent(new Event('change', { bubbles: true }))
    await waitFor(() => expect(api.setShedLevel).toHaveBeenCalledWith({ level: 'critical' }))
  })

  it('shows the level in force of an organization and its source', async () => {
    state = { globalLevel: 'degraded', orgLevel: 'normal', level: 'degraded', source: 'global', actions: degraded, degradedActions: degraded, criticalActions: critical }
    const el = shedLevelCard('org-1')
    mounted = mount(el)
    await settle()
    await waitFor(() => expect(el.textContent).toContain('In force now: Degraded (from the global level).'))
    expect(api.getShedState).toHaveBeenCalledWith({ orgId: 'org-1' })
    expect(el.textContent).toContain('Pauses schedules')
    expect(el.textContent).not.toContain('Skips advisory workflows')
  })

  it('loads nothing for an organization scope with no organization', async () => {
    const el = shedLevelCard('')
    mounted = mount(el)
    await settle()
    expect(api.getShedState).not.toHaveBeenCalled()
  })

  it('edits the actions of each level of an organization, and keeps a draft through a refresh', async () => {
    state = { globalLevel: 'normal', orgLevel: 'normal', level: 'normal', actions: none, degradedActions: degraded, criticalActions: critical }
    const el = shedLevelCard('org-1')
    mounted = mount(el)
    await settle()
    await waitFor(() => expect(el.querySelector('.shed-actions-editor')?.hasAttribute('hidden')).toBe(false))
    const boxes = [...el.querySelectorAll<HTMLElement & { checked: boolean }>('.shed-actions-table check-box')]
    expect(boxes).toHaveLength(10)
    // Row 0 is skip drafts: degraded on, critical on. Turn degraded off.
    expect(boxes[0].checked).toBe(true)
    boxes[0].checked = false
    // Row 3 is skip advisory: turn it on for degraded.
    boxes[6].checked = true
    el.invalidate()
    await settle()
    expect(boxes[0].checked).toBe(false)
    const save = [...el.querySelectorAll('push-button')].find((b) => b.textContent === 'Save actions') as HTMLElement
    save.click()
    await waitFor(() =>
      expect(api.setShedActions).toHaveBeenCalledWith({
        orgId: 'org-1',
        degraded: { skipDrafts: false, pauseSchedules: true, lowerPrPriority: true, skipAdvisory: true, refuseManualRuns: false },
        critical,
      }),
    )
  })

  it('has no actions editor for the global level', async () => {
    state = { globalLevel: 'normal', level: 'normal', actions: none, degradedActions: degraded, criticalActions: critical }
    const el = shedLevelCard()
    mounted = mount(el)
    await settle()
    await waitFor(() => expect(el.textContent).toContain('In force now'))
    expect(el.querySelector('.shed-actions-editor')?.hasAttribute('hidden')).toBe(true)
  })

  it('names each action that is on', () => {
    expect(shedActionTexts(none)).toEqual([])
    expect(shedActionTexts(critical)).toHaveLength(5)
  })
})
