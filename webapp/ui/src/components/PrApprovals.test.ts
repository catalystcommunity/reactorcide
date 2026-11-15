import { afterEach, describe, expect, it, vi } from 'vitest'
import { waitFor } from '@testing-library/dom'
import type { ComboBox, PushButton } from '@catalystcommunity/controls'
import type { GetPrApprovalsResponse } from '~/api/csilapi/types.gen.ts'

let approvals: GetPrApprovalsResponse

vi.mock('~/api/client.ts', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/client.ts')>()
  return {
    ...actual,
    api: {
      getPrApprovals: vi.fn(async () => approvals),
      prCommand: vi.fn(async () => ({ message: 'Accepted for review security.', recorded: [] })),
    },
  }
})

import { ServiceError, api } from '~/api/client.ts'
import { mount, settle, unmount } from '~/test/mount.ts'
import { kindLabel, prApprovalsPanel, type PrApprovalsPanel } from './PrApprovals.ts'

const HEAD = 'abcdef0123456789abcdef0123456789abcdef01'

const APPROVALS: GetPrApprovalsResponse = {
  headSha: HEAD,
  actions: [
    {
      approvalId: 'a1',
      kind: 'run',
      action: 'approved',
      headSha: HEAD,
      person: 'github/alice',
      subjects: ['repository_write'],
      channel: 'comment',
      createdAt: '2026-11-14T10:00:00Z',
    },
  ],
  reviews: [
    { id: 'security', description: 'Security review', scope: 'org', applies: true, required: 2, accepted: ['github/bob'], satisfied: false, accept: ['reactorcide_group:security'] },
    { id: 'docs', scope: 'project', applies: false, required: 1, accepted: [], satisfied: false, accept: ['non_author'] },
  ],
  reviewsState: 'pending',
  reviewsSummary: '1 of 2 reviews satisfied',
}

let mounted: PrApprovalsPanel | null = null

async function panel(): Promise<PrApprovalsPanel> {
  const el = prApprovalsPanel('p1', 7)
  mounted = mount(el)
  await settle()
  await waitFor(() => expect(el.querySelector('.pr-head')?.textContent).toBe(HEAD.slice(0, 12)))
  return el
}

function pushButton(root: Element, text: string): PushButton {
  const match = [...root.querySelectorAll('push-button')].find((b) => b.textContent === text)
  if (!match) throw new Error(`no button ${text}`)
  return match as PushButton
}

afterEach(() => {
  if (mounted) unmount(mounted)
  mounted = null
  vi.clearAllMocks()
})

describe('pull request approvals panel', () => {
  it('shows the head, the required reviews, the summary and the actions', async () => {
    approvals = APPROVALS
    const el = await panel()
    expect(api.getPrApprovals).toHaveBeenCalledWith({ projectId: 'p1', prNumber: 7 })
    expect(el.textContent).toContain('pending: 1 of 2 reviews satisfied')
    const reviews = [...el.querySelectorAll('tbody')[0].querySelectorAll('tr')]
    expect(reviews[0].textContent).toContain('security')
    expect(reviews[0].textContent).toContain('Organization')
    expect(reviews[0].textContent).toContain('1 of 2 accepted')
    expect(reviews[0].textContent).toContain('Waiting')
    expect(reviews[1].textContent).toContain('Not needed')
    const action = el.querySelectorAll('tbody')[1].querySelector('tr')!
    expect(action.querySelector('.ac-kind')?.textContent).toBe('approve-ci')
    expect(action.querySelector('.ac-person')?.textContent).toBe('github/alice')
    expect(kindLabel('head_ci')).toBe('override-ci')
  })

  it('sends the head shown and the chosen label, shows the message and refreshes', async () => {
    approvals = APPROVALS
    const el = await panel()
    const label = el.querySelector<ComboBox>('combo-box')!
    label.value = 'security'
    label.dispatchEvent(new Event('change', { bubbles: true }))
    pushButton(el, 'Accept').click()
    await waitFor(() =>
      expect(api.prCommand).toHaveBeenCalledWith({ projectId: 'p1', prNumber: 7, command: 'accept', label: 'security', headSha: HEAD }),
    )
    await waitFor(() => expect(el.textContent).toContain('Accepted for review security.'))
    await waitFor(() => expect(api.getPrApprovals).toHaveBeenCalledTimes(2))

    // The label is only for accept.
    pushButton(el, 'Approve CI').click()
    await waitFor(() => expect(api.prCommand).toHaveBeenLastCalledWith({ projectId: 'p1', prNumber: 7, command: 'approve-ci', headSha: HEAD }))
  })

  it('says the head changed on a conflict, and shows a refusal plainly', async () => {
    approvals = APPROVALS
    const el = await panel()
    vi.mocked(api.prCommand).mockRejectedValueOnce(new ServiceError('conflict', 'the head changed'))
    pushButton(el, 'Override CI').click()
    await waitFor(() => expect(el.textContent).toContain('The pull request has a new head.'))
    await waitFor(() => expect(api.getPrApprovals).toHaveBeenCalledTimes(2))

    vi.mocked(api.prCommand).mockRejectedValueOnce(new ServiceError('forbidden', 'you have no override approver subject'))
    pushButton(el, 'Override CI').click()
    await waitFor(() => expect(el.textContent).toContain('You cannot do this. you have no override approver subject'))
  })

  it('hides itself when approvals are unavailable', async () => {
    vi.mocked(api.getPrApprovals).mockRejectedValueOnce(new ServiceError('unavailable', 'pull request approvals are not available'))
    const el = prApprovalsPanel('p1', 7)
    mounted = mount(el)
    await settle()
    await waitFor(() => expect(el.hidden).toBe(true))
    expect(el.textContent).not.toContain('not available')
  })
})
