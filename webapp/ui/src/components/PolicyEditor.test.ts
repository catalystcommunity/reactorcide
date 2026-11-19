import { afterEach, describe, expect, it, vi } from 'vitest'
import { waitFor } from '@testing-library/dom'
import type { ComboBox, PushButton, TextField } from '@catalystcommunity/controls'
import type { CiPolicyDetail, CiPolicyDocument } from '~/api/csilapi/types.gen.ts'

let stored: CiPolicyDetail | undefined
let builtIn = false

vi.mock('~/api/client.ts', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/client.ts')>()
  return {
    ...actual,
    api: {
      getCiPolicy: vi.fn(async () => {
        if (!stored) throw new actual.ServiceError('not_found', 'no policy')
        if (builtIn) return { policy: stored, builtIn: true, builtInReason: 'the organization has the secure defaults profile' }
        return { policy: stored }
      }),
      putCiPolicy: vi.fn(async (req: { document: CiPolicyDocument }) => ({ policy: { ...(stored ?? DETAIL), revision: 'r2', document: req.document } })),
      deleteCiPolicy: vi.fn(async () => ({ deleted: true })),
    },
  }
})

vi.mock('~/ui/controls.ts', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/ui/controls.ts')>()
  return { ...actual, confirmAction: vi.fn(async () => true) }
})

import { ServiceError, api } from '~/api/client.ts'
import { mount, settle, unmount } from '~/test/mount.ts'
import { ciPolicyEditor, type CiPolicyEditor } from './PolicyEditor.ts'

const DOC: CiPolicyDocument = {
  version: 1,
  defaults: { ciSource: 'base', profile: 'standard' },
  headCi: [
    {
      id: 'docs-team',
      actors: { any: ['reactorcide_group:docs'] },
      workflows: ['docs'],
      paths: ['.reactorcide/workflows/docs.yaml'],
      baseBranches: [],
      use: { ciSource: 'head', profile: 'pr-untrusted', workers: 'default' },
    },
    {
      id: 'release',
      workflows: ['release', 'publish'],
      paths: ['.reactorcide/**'],
      events: ['pull_request_opened'],
      headRepository: 'any',
      approval: { any: ['project_owner'] },
      use: {
        ciSource: 'head',
        profile: 'standard',
        workers: 'default',
        baseNodes: [{ nodes: ['deploy'], ciSource: 'base', profile: 'deploy', workers: 'deployers' }],
      },
    },
  ],
}

const DETAIL: CiPolicyDetail = {
  policyId: 'pol1',
  projectId: 'p1',
  revision: 'r1',
  document: DOC,
  createdAt: '2026-11-01T00:00:00Z',
  updatedAt: '2026-11-02T00:00:00Z',
  updatedBy: 'alice',
}

let mounted: Element | null = null

async function editor(): Promise<CiPolicyEditor> {
  const el = ciPolicyEditor('p1')
  mounted = mount(el)
  await settle()
  await waitFor(() => expect((el.lastElementChild as HTMLElement).hidden).toBe(false))
  return el
}

function pushButton(root: Element, text: string): PushButton {
  const match = [...root.querySelectorAll('push-button')].find((b) => b.textContent === text)
  if (!match) throw new Error(`no button ${text}`)
  return match as PushButton
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

function addChip(root: Element, label: RegExp, value: string): void {
  const input = byLabel<TextField>(root, 'text-field', label)
  input.value = value
  input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
}

afterEach(() => {
  if (mounted) unmount(mounted)
  mounted = null
  stored = undefined
  vi.clearAllMocks()
})

describe('CI policy configurator', () => {
  it('shows each rule as a card in plain words', async () => {
    stored = DETAIL
    const el = await editor()
    const cards = el.querySelectorAll('.rule-card')
    expect(cards).toHaveLength(2)
    expect(cards[0].querySelector('h4')?.textContent).toBe('Rule 1: docs-team')
    expect(cards[0].textContent).toContain('Who can change CI files without approval')
    expect(cards[0].textContent).toContain('reactorcide_group:docs')
    expect(byLabel<ComboBox>(cards[1], 'combo-box', /^Pull requests from/).value).toBe('any')
    expect(cards[1].textContent).toContain('These steps run from the trusted base CI')
    expect(cards[1].textContent).toContain('deploy')
    expect(el.textContent).toContain('Revision r1.')
  })

  it('saves a loaded document unchanged, with the revision it loaded', async () => {
    stored = DETAIL
    const el = await editor()
    pushButton(el, 'Save policy').click()
    await waitFor(() => expect(api.putCiPolicy).toHaveBeenCalled())
    const sent = vi.mocked(api.putCiPolicy).mock.calls[0][0]
    expect(sent.projectId).toBe('p1')
    expect(sent.expectedRevision).toBe('r1')
    expect(sent.document).toStrictEqual(DOC)
    await waitFor(() => expect(el.textContent).toContain('Policy saved. Revision r2.'))
  })

  it('reorders rules', async () => {
    stored = DETAIL
    const el = await editor()
    pushButton(el.querySelectorAll('.rule-card')[0], 'Move down').click()
    await settle()
    expect(el.querySelectorAll('.rule-card')[0].querySelector('h4')?.textContent).toBe('Rule 1: release')
    pushButton(el, 'Save policy').click()
    await waitFor(() => expect(api.putCiPolicy).toHaveBeenCalled())
    expect(vi.mocked(api.putCiPolicy).mock.calls[0][0].document.headCi.map((r) => r.id)).toEqual(['release', 'docs-team'])
  })

  it('tells the person to reload after a conflict, and shows a server validation message as it is', async () => {
    stored = DETAIL
    const el = await editor()
    vi.mocked(api.putCiPolicy).mockRejectedValueOnce(new ServiceError('conflict', 'revision mismatch'))
    pushButton(el, 'Save policy').click()
    await waitFor(() => expect(el.textContent).toContain('Someone changed the policy after you loaded it.'))
    expect(pushButton(el, 'Reload').hidden).toBe(false)

    vi.mocked(api.putCiPolicy).mockRejectedValueOnce(new ServiceError('invalid_argument', 'rule docs-team must select a profile and worker class'))
    pushButton(el, 'Save policy').click()
    await waitFor(() => expect(el.textContent).toContain('rule docs-team must select a profile and worker class'))
    expect(pushButton(el, 'Reload').hidden).toBe(true)
  })

  it('starts empty when no policy is stored, checks a new rule, and saves it with no revision', async () => {
    stored = undefined
    const el = await editor()
    expect(el.textContent).toContain('No policy is stored for this project.')
    pushButton(el, 'Add rule').click()
    await settle()
    const card = el.querySelector('.rule-card')!
    type(byLabel<TextField>(card, 'text-field', /^Rule name/), 'Docs Team')
    pushButton(el, 'Save policy').click()
    await settle()
    expect(api.putCiPolicy).not.toHaveBeenCalled()
    expect(el.textContent).toContain('A rule name can have only lowercase letters, digits and hyphens.')

    type(byLabel<TextField>(card, 'text-field', /^Rule name/), 'docs-team')
    addChip(card, /^Which workflows/, 'docs')
    addChip(card, /^Which CI files/, '.reactorcide/workflows/docs.yaml')
    type(byLabel<TextField>(card, 'text-field', /^Profile/), 'pr-untrusted')
    type(byLabel<TextField>(card, 'text-field', /^Worker class/), 'default')
    await settle()
    pushButton(el, 'Save policy').click()
    await waitFor(() => expect(api.putCiPolicy).toHaveBeenCalled())
    const sent = vi.mocked(api.putCiPolicy).mock.calls[0][0]
    expect(sent.expectedRevision).toBeUndefined()
    expect(sent.document).toStrictEqual({
      version: 1,
      headCi: [
        {
          id: 'docs-team',
          workflows: ['docs'],
          paths: ['.reactorcide/workflows/docs.yaml'],
          use: { ciSource: 'head', profile: 'pr-untrusted', workers: 'default' },
        },
      ],
    })
  })

  it('deletes the policy with the loaded revision', async () => {
    stored = DETAIL
    const el = await editor()
    pushButton(el, 'Delete policy').click()
    await waitFor(() => expect(api.deleteCiPolicy).toHaveBeenCalledWith({ projectId: 'p1', expectedRevision: 'r1' }))
    await waitFor(() => expect(el.querySelectorAll('.rule-card')).toHaveLength(0))
  })
})

describe('built-in policy', () => {
  it('starts the draft from the built-in policy and saves with no expected revision', async () => {
    stored = { ...DETAIL, policyId: '', revision: 'builtin-r1', document: DOC }
    builtIn = true
    const el = ciPolicyEditor('p1')
    document.body.append(el)
    await settle()
    await waitFor(() => expect(el.textContent).toContain('The built-in policy (revision builtin-r1) applies'))
    expect(el.querySelectorAll('.rule-card').length).toBe(2)
    const save = [...el.querySelectorAll('push-button')].find((b) => b.textContent === 'Save policy') as HTMLElement
    save.click()
    await waitFor(() => expect(api.putCiPolicy).toHaveBeenCalled())
    expect(vi.mocked(api.putCiPolicy).mock.calls[0][0].expectedRevision).toBeUndefined()
    el.remove()
    builtIn = false
    stored = undefined
  })
})
