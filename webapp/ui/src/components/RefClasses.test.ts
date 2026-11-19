import { afterEach, describe, expect, it, vi } from 'vitest'
import { waitFor } from '@testing-library/dom'
import type { TextField } from '@catalystcommunity/controls'
import type { ListRefClassRulesResponse } from '~/api/csilapi/types.gen.ts'

let response: ListRefClassRulesResponse

vi.mock('~/api/client.ts', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/client.ts')>()
  return {
    ...actual,
    api: {
      listRefClassRules: vi.fn(async () => response),
      putRefClassRule: vi.fn(async (req: { rule: unknown }) => ({ rule: { ruleId: 'new', ...(req.rule as object) } })),
      deleteRefClassRule: vi.fn(async () => ({ deleted: true })),
      explainRefClasses: vi.fn(async () => ({
        facts: { eventType: 'push', branch: 'main' },
        classes: ['default', 'production'],
        reasons: [{ class: 'production', ruleId: 'r1', ruleName: 'prod', scope: 'org' }],
        ruleIds: ['r1'],
        rules: [{ ruleId: 'r2', name: 'release', scope: 'org', enabled: true, matched: false, classes: ['release'] }],
      })),
    },
  }
})

import { api } from '~/api/client.ts'
import { mount, settle, unmount } from '~/test/mount.ts'
import { classListError, refClassSettings, ruleMatchText, splitList, type RefClassSettings } from './RefClasses.ts'

const BUILTIN = [
  { ruleId: 'builtin:default-branch', classes: ['default'] },
  { ruleId: 'builtin:fork', classes: ['fork'] },
]

let mounted: Element | null = null

async function settings(organization: string, projectId?: string): Promise<RefClassSettings> {
  const el = refClassSettings(organization, projectId)
  mounted = mount(el)
  await settle()
  await waitFor(() => expect(api.listRefClassRules).toHaveBeenCalled())
  return el
}

afterEach(() => {
  if (mounted) unmount(mounted)
  mounted = null
  vi.clearAllMocks()
})

describe('ref class settings', () => {
  it('lists the built-in, organization and project rules of a project', async () => {
    response = {
      rules: [{ ruleId: 'p1', projectId: 'proj', name: 'trusted', position: 0, isFork: false, classes: ['test'], enabled: true }],
      inherited: [{ ruleId: 'o1', name: 'prod', position: 0, eventTypes: ['push'], isDefaultBranch: true, classes: ['production'], enabled: true }],
      builtin: BUILTIN,
      canManage: true,
    }
    const el = await settings('acme', 'proj')
    expect(api.listRefClassRules).toHaveBeenCalledWith({ organization: 'acme', projectId: 'proj' })
    await waitFor(() => expect(el.textContent).toContain('default-branch: default'))
    expect(el.textContent).toContain('Organization rules')
    expect(el.textContent).toContain('Adds: production')
    expect(el.textContent).toContain('Project rules')
    expect(el.textContent).toContain('Adds: test')
    expect(el.textContent).toContain('Matches not a fork.')
  })

  it('saves a rule with the facts that the form sets', async () => {
    response = { rules: [], inherited: [], builtin: BUILTIN, canManage: true }
    const el = await settings('acme')
    await waitFor(() => expect(el.querySelector<HTMLFormElement>('form.ref-class-form')!.hidden).toBe(false))
    const fields = [...el.querySelectorAll('form.ref-class-form text-field')] as TextField[]
    // Name, position, classes, description, branch, tag, base branch.
    fields[0].value = 'prod'
    fields[2].value = 'production, release'
    fields[4].value = 'main'
    el.querySelector('form.ref-class-form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await waitFor(() => expect(api.putRefClassRule).toHaveBeenCalled())
    expect(vi.mocked(api.putRefClassRule).mock.calls[0][0]).toEqual({
      organization: 'acme',
      rule: { name: 'prod', position: 0, classes: ['production', 'release'], enabled: true, branchGlob: 'main' },
    })
    await waitFor(() => expect(el.textContent).toContain('Adds: production, release'))
  })

  it('refuses a rule with no classes, and sends nothing', async () => {
    response = { rules: [], inherited: [], builtin: BUILTIN, canManage: true }
    const el = await settings('acme')
    await waitFor(() => expect(el.querySelector<HTMLFormElement>('form.ref-class-form')!.hidden).toBe(false))
    const fields = [...el.querySelectorAll('form.ref-class-form text-field')] as TextField[]
    fields[0].value = 'prod'
    el.querySelector('form.ref-class-form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await settle()
    expect(api.putRefClassRule).not.toHaveBeenCalled()
    expect(el.textContent).toContain('Add at least one class.')
  })

  it('hides editing when the caller cannot manage the scope', async () => {
    response = { rules: [{ ruleId: 'o1', name: 'prod', position: 0, classes: ['production'], enabled: true }], inherited: [], builtin: BUILTIN, canManage: false }
    const el = await settings('acme')
    await waitFor(() => expect(el.textContent).toContain('Adds: production'))
    expect(el.querySelector<HTMLFormElement>('form.ref-class-form')!.hidden).toBe(true)
    expect(el.textContent).toContain('Only an organization administrator can change the organization rules.')
    expect(el.querySelector('.ref-class-rule push-button')).toBeNull()
  })

  it('explains the classes of the facts that you give', async () => {
    response = { rules: [], inherited: [], builtin: BUILTIN, canManage: false }
    const el = await settings('acme')
    await waitFor(() => expect(el.querySelector<HTMLFormElement>('form.ref-class-explain')).not.toBeNull())
    // Event ID, event type, branch, tag, base branch, default branch.
    const fields = [...el.querySelectorAll('form.ref-class-explain text-field')] as TextField[]
    fields[1].value = 'push'
    fields[2].value = 'main'
    el.querySelector('form.ref-class-explain')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await waitFor(() => expect(api.explainRefClasses).toHaveBeenCalled())
    expect(vi.mocked(api.explainRefClasses).mock.calls[0][0]).toEqual({ organization: 'acme', facts: { source: 'webhook', eventType: 'push', branch: 'main' } })
    await waitFor(() => expect(el.querySelector<HTMLElement>('.explain-result')!.hidden).toBe(false))
    expect([...el.querySelectorAll('.explain-result .ref-class')].map((li) => li.textContent)).toEqual(['default', 'production'])
    expect(el.textContent).toContain('production from organization rule prod')
    expect(el.textContent).toContain('org rule release did not match')
  })
})

describe('ref class helpers', () => {
  it('splits and checks class lists', () => {
    expect(splitList(' a, b c ,, ')).toEqual(['a', 'b', 'c'])
    expect(classListError([], true)).toBe('Add at least one class.')
    expect(classListError([], false)).toBeUndefined()
    expect(classListError(['Prod'], false)).toContain('"Prod" is not a valid class')
  })

  it('says the match facts of a rule', () => {
    expect(ruleMatchText({ name: 'x', position: 0, classes: ['a'], enabled: true })).toBe('every event')
    expect(ruleMatchText({ name: 'x', position: 0, classes: ['a'], enabled: true, tagGlob: 'v*', isPr: false })).toBe('tag v*; not a pull request')
  })
})
