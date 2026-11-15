import { afterEach, describe, expect, it, vi } from 'vitest'
import { waitFor } from '@testing-library/dom'
import type { ComboBox, ListBox, TextField } from '@catalystcommunity/controls'
import type { GetCiTrustSettingsResponse } from '~/api/csilapi/types.gen.ts'

let response: GetCiTrustSettingsResponse

vi.mock('~/api/client.ts', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/client.ts')>()
  return {
    ...actual,
    api: {
      getCiTrustSettings: vi.fn(async () => response),
      putCiTrustSettings: vi.fn(async (req: { settings: unknown }) => ({ settings: req.settings, effective: response.effective })),
    },
  }
})

import { ServiceError, api } from '~/api/client.ts'
import { mount, settle, unmount } from '~/test/mount.ts'
import { ciTrustSettingsForm, type CiTrustSettingsForm } from './TrustSettings.ts'
import { subjectError } from './ChipList.ts'

const EFFECTIVE = {
  runGate: ['forks'],
  gatedRunAuthority: 'no_secrets',
  runApprovers: ['project_owner', 'repository_write'],
  overrideApprovers: ['project_owner', 'repository_write'],
  overrideProfile: 'pr-untrusted',
  requiredReviews: [{ id: 'security', accept: ['reactorcide_group:security'], scope: 'org' }],
}

let mounted: Element | null = null

async function form(organization: string, projectId?: string): Promise<CiTrustSettingsForm> {
  const el = ciTrustSettingsForm(organization, projectId)
  mounted = mount(el)
  await settle()
  await waitFor(() => expect(el.querySelector<HTMLFormElement>('form')!.hidden).toBe(false))
  return el
}

function byLabel<T extends Element>(root: Element, selector: string, label: RegExp): T {
  const match = [...root.querySelectorAll(selector)].find((el) => label.test(el.getAttribute('aria-label') ?? ''))
  if (!match) throw new Error(`no ${selector} labelled ${label}`)
  return match as T
}

function choose(combo: ComboBox, value: string): void {
  combo.value = value
  combo.dispatchEvent(new Event('change', { bubbles: true }))
}

function addChip(root: Element, label: RegExp, value: string): void {
  const input = byLabel<TextField>(root, 'text-field', label)
  input.value = value
  input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
}

function submit(el: Element): void {
  el.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
}

afterEach(() => {
  if (mounted) unmount(mounted)
  mounted = null
  vi.clearAllMocks()
})

describe('CI trust settings form', () => {
  it('asks for the organization by name, and sends nothing for fields left on the default', async () => {
    response = { settings: {}, effective: EFFECTIVE, canLoosen: true }
    const el = await form('acme')
    expect(api.getCiTrustSettings).toHaveBeenCalledWith({ organization: 'acme' })
    submit(el)
    await waitFor(() => expect(api.putCiTrustSettings).toHaveBeenCalled())
    const sent = vi.mocked(api.putCiTrustSettings).mock.calls[0][0]
    expect(sent).toEqual({ organization: 'acme', settings: {} })
    expect(Object.keys(sent.settings)).toEqual([])
  })

  it('sends an empty list for "set" with no values, and undefined for inherit', async () => {
    response = {
      settings: { runApprovers: [] },
      effective: { ...EFFECTIVE, runApprovers: [] },
      organizationEffective: EFFECTIVE,
      canLoosen: false,
    }
    const el = await form('acme', 'p1')
    expect(api.getCiTrustSettings).toHaveBeenCalledWith({ organization: 'acme', projectId: 'p1' })
    // The stored empty list is shown as "set", not as inherit.
    expect(byLabel<ComboBox>(el, 'combo-box', /^Run approvers/).value).toBe('set')
    expect(byLabel<ComboBox>(el, 'combo-box', /^Hold pull requests/).value).toBe('inherit')
    expect(el.textContent).toContain('Organization value: Forks')
    expect(el.textContent).toContain('In force now: Nobody')
    expect(el.textContent).toContain('Only an organization administrator can make them less strict')

    // Hold pull requests: set, with no condition selected = explicitly no gate.
    choose(byLabel<ComboBox>(el, 'combo-box', /^Hold pull requests/), 'set')
    const gate = el.querySelector<ListBox>('list-box')!
    gate.values = []
    gate.dispatchEvent(new Event('change', { bubbles: true }))
    await settle()
    submit(el)
    await waitFor(() => expect(api.putCiTrustSettings).toHaveBeenCalled())
    const sent = vi.mocked(api.putCiTrustSettings).mock.calls[0][0]
    expect(sent.projectId).toBe('p1')
    expect(sent.settings).toStrictEqual({ runGate: [], runApprovers: [] })
  })

  it('switching to "set" starts from the inherited value, and back to inherit drops it', async () => {
    response = { settings: { overrideApprovers: ['repository_write'] }, effective: EFFECTIVE, organizationEffective: EFFECTIVE, canLoosen: true }
    const el = await form('acme', 'p1')
    choose(byLabel<ComboBox>(el, 'combo-box', /^Run approvers/), 'set')
    addChip(el, /^Add a run approver/, 'vcs_user:github/alice')
    choose(byLabel<ComboBox>(el, 'combo-box', /^Override approvers/), 'inherit')
    await settle()
    submit(el)
    await waitFor(() => expect(api.putCiTrustSettings).toHaveBeenCalled())
    expect(vi.mocked(api.putCiTrustSettings).mock.calls[0][0].settings).toStrictEqual({
      runApprovers: ['project_owner', 'repository_write', 'vcs_user:github/alice'],
    })
  })

  it('shows the server message when a looser save is refused', async () => {
    response = { settings: {}, effective: EFFECTIVE, organizationEffective: EFFECTIVE, canLoosen: false }
    const message = 'only an organization administrator can make project settings less strict than the organization: removes run gate condition forks'
    vi.mocked(api.putCiTrustSettings).mockRejectedValueOnce(new ServiceError('forbidden', message))
    const el = await form('acme', 'p1')
    choose(byLabel<ComboBox>(el, 'combo-box', /^Hold pull requests/), 'set')
    const gate = el.querySelector<ListBox>('list-box')!
    gate.values = []
    gate.dispatchEvent(new Event('change', { bubbles: true }))
    submit(el)
    await waitFor(() => expect(el.querySelector<HTMLElement>('.alert-error:not([hidden])')?.textContent).toBe(message))
  })

  it('refuses a subject that is not valid, before any request', async () => {
    response = { settings: {}, effective: EFFECTIVE, canLoosen: true }
    const el = await form('acme')
    choose(byLabel<ComboBox>(el, 'combo-box', /^Run approvers/), 'set')
    addChip(el, /^Add a run approver/, 'alice')
    await settle()
    expect(el.textContent).toContain('"alice" is not a subject.')
    expect(subjectError('non_author')).toContain('only in a required review')
    expect(subjectError('non_author', { nonAuthor: true })).toBeUndefined()
    expect(subjectError('vcs_team:acme/security')).toBeUndefined()
    expect(subjectError('vcs_user:alice')).toContain('vcs_user:github/LOGIN')
  })

  it('edits required reviews, shows org reviews read-only, and sends only the project reviews', async () => {
    response = { settings: {}, effective: EFFECTIVE, organizationEffective: EFFECTIVE, canLoosen: true }
    const el = await form('acme', 'p1')
    expect(el.querySelector('.readonly-list')?.textContent).toContain('security')
    choose(byLabel<ComboBox>(el, 'combo-box', /^Project required reviews/), 'set')
    await settle()
    const add = [...el.querySelectorAll('push-button')].find((b) => b.textContent === 'Add review')!
    ;(add as HTMLElement).click()
    await settle()
    // Incomplete: the save is refused with the reason.
    submit(el)
    await settle()
    expect(api.putCiTrustSettings).not.toHaveBeenCalled()
    expect(el.textContent).toContain('A review needs an id.')

    const id = byLabel<TextField>(el, 'text-field', /^Review id/)
    id.value = 'compliance'
    id.dispatchEvent(new Event('input', { bubbles: true }))
    addChip(el, /^Who can accept/, 'non_author')
    const count = byLabel<TextField>(el, 'text-field', /^People needed/)
    count.value = '2'
    addChip(el, /^Which changes/, 'src/**')
    await settle()
    submit(el)
    await waitFor(() => expect(api.putCiTrustSettings).toHaveBeenCalled())
    expect(vi.mocked(api.putCiTrustSettings).mock.calls[0][0].settings).toStrictEqual({
      requiredReviews: [{ id: 'compliance', accept: ['non_author'], paths: ['src/**'], count: 2 }],
    })
  })
})
