import { afterEach, describe, expect, it, vi } from 'vitest'
import { waitFor } from '@testing-library/dom'
import type { PushButton, TextField } from '@catalystcommunity/controls'
import type { GetRequiredSetResponse, ProjectDetail } from '~/api/csilapi/types.gen.ts'

let response: GetRequiredSetResponse

function base(overrides: Partial<GetRequiredSetResponse> = {}): GetRequiredSetResponse {
  return {
    enabled: true,
    workflows: [
      { workflowSecurityId: 'build', branchPattern: '', state: 'required', inSet: true, source: 'seed', addedAt: '2026-11-01T00:00:00Z' },
      { workflowSecurityId: 'lint', branchPattern: '', state: 'advisory', inSet: false },
      {
        workflowSecurityId: 'e2e', branchPattern: '', state: 'quarantined', inSet: true, quarantinedUntil: '2026-12-01T10:00:00Z',
        quarantineReason: 'flaky', quarantinedByName: 'alice',
      },
    ],
    proposals: [
      { proposalId: 'p-1', workflowSecurityId: 'lint', branchPattern: '', action: 'promote', state: 'pending', proposedAt: '2026-11-15T00:00:00Z', proposedByName: 'bob' },
    ],
    approvers: [],
    approversSource: 'org_admins',
    approverRules: [{ branches: ['release/*'], approvers: ['reactorcide_group:release'] }],
    approversInherit: true,
    approverRulesInherit: true,
    maxQuarantineDays: 30,
    canPropose: true,
    canManage: true,
    canApproveAllBranches: true,
    ...overrides,
  }
}

vi.mock('~/api/client.ts', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/client.ts')>()
  return {
    ...actual,
    api: {
      getRequiredSet: vi.fn(async () => response),
      setRequiredSetEnabled: vi.fn(async () => ({ enabled: false, seeded: [] })),
      proposeRequiredSetChange: vi.fn(async () => ({ proposal: response.proposals[0] })),
      decideRequiredSetProposal: vi.fn(async () => ({ proposal: response.proposals[0] })),
      quarantineRequiredWorkflow: vi.fn(async () => ({ workflow: response.workflows[0] })),
      unquarantineRequiredWorkflow: vi.fn(async () => ({ done: true })),
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
import { branchLabel, formatExpiry, requiredSetCardTag, requiredStateText } from './RequiredSet.ts'

function row(el: Element, workflow: string): HTMLTableRowElement {
  const match = [...el.querySelectorAll('tr')].find((tr) => tr.querySelector('.wf-id')?.textContent === workflow)
  if (!match) throw new Error(`no row ${workflow}`)
  return match as HTMLTableRowElement
}

function visibleButtons(root: Element): string[] {
  return [...root.querySelectorAll('push-button')].filter((b) => !b.closest('[hidden]')).map((b) => b.textContent ?? '')
}

async function card(): Promise<ProjectCard> {
  const el = makeCard<ProjectCard>(requiredSetCardTag, {})
  el.setProject({ projectId: 'p1', name: 'Widget', enabled: true } as unknown as ProjectDetail)
  mount(el)
  await settle()
  await waitFor(() => expect(el.textContent).toContain('The required set is on.'))
  return el
}

afterEach(() => {
  vi.clearAllMocks()
})

describe('required set helpers', () => {
  it('names states, expiries and branch patterns', () => {
    expect(requiredStateText({ workflowSecurityId: 'x', branchPattern: '', state: 'advisory', inSet: false })).toBe('Advisory — not required')
    expect(requiredStateText({ workflowSecurityId: 'x', branchPattern: '', state: 'quarantined', inSet: true, quarantinedUntil: '2026-12-01T10:00:00Z' })).toBe(
      'Quarantined until 2026-12-01 10:00 UTC',
    )
    expect(formatExpiry('2026-12-01T10:00:00Z')).toBe('2026-12-01 10:00 UTC')
    expect(branchLabel('')).toBe('every branch')
    expect(branchLabel('release/*')).toBe('release/*')
  })
})

describe('required set card', () => {
  it('shows each workflow with its state and the right actions', async () => {
    response = base()
    const el = await card()
    expect(api.getRequiredSet).toHaveBeenCalledWith({ projectId: 'p1' })
    expect(row(el, 'build').textContent).toContain('Required')
    expect(row(el, 'lint').textContent).toContain('Advisory — not required')
    expect(row(el, 'e2e').textContent).toContain('Quarantined until 2026-12-01 10:00 UTC')
    expect(row(el, 'e2e').textContent).toContain('alice')
    // A pending promotion hides the promote button of its row.
    expect(visibleButtons(row(el, 'lint'))).toEqual([])
    expect(visibleButtons(row(el, 'build'))).toEqual(['Propose removal', 'Quarantine'])
    expect(visibleButtons(row(el, 'e2e'))).toEqual(['Propose removal', 'End quarantine'])
    expect(el.textContent).toContain('Pending proposals')
    expect(el.textContent).toContain('release/*: reactorcide_group:release')
  })

  it('approves a proposal and keeps the row elements on refresh', async () => {
    response = base()
    const el = await card()
    const before = row(el, 'build')
    const approve = [...el.querySelectorAll('push-button')].find((b) => b.textContent === 'Approve') as PushButton
    approve.click()
    await waitFor(() => expect(api.decideRequiredSetProposal).toHaveBeenCalledWith({ projectId: 'p1', proposalId: 'p-1', decision: 'approve' }))
    await waitFor(() => expect(api.getRequiredSet).toHaveBeenCalledTimes(2))
    await settle()
    expect(row(el, 'build')).toBe(before)
  })

  it('needs a reason for a quarantine, then sends the expiry', async () => {
    response = base()
    const el = await card()
    const quarantine = [...row(el, 'build').querySelectorAll('push-button')].find((b) => b.textContent === 'Quarantine') as PushButton
    quarantine.click()
    await settle()
    expect(api.quarantineRequiredWorkflow).not.toHaveBeenCalled()
    expect(el.textContent).toContain('A quarantine needs the workflow ID and a reason.')
    const reason = [...el.querySelectorAll('text-field')].find((f) => /^Reason/.test(f.getAttribute('aria-label') ?? '')) as TextField
    reason.value = 'flaky runner'
    quarantine.click()
    await waitFor(() =>
      expect(api.quarantineRequiredWorkflow).toHaveBeenCalledWith({ projectId: 'p1', workflowSecurityId: 'build', branchPattern: '', until: '7d', reason: 'flaky runner' }),
    )
  })

  it('hides the changes for a caller who may only read, and shows the set off', async () => {
    response = base({ enabled: false, canPropose: false, canManage: false, canApproveAllBranches: false, proposals: [] })
    const el = makeCard<ProjectCard>(requiredSetCardTag, {})
    el.setProject({ projectId: 'p1', name: 'Widget', enabled: true } as unknown as ProjectDetail)
    mount(el)
    await settle()
    await waitFor(() => expect(el.textContent).toContain('The required set is off: every workflow blocks a merge.'))
    expect(visibleButtons(el)).toEqual([])
  })

  it('turns the set off after a confirm', async () => {
    response = base()
    const el = await card()
    const toggle = [...el.querySelectorAll('push-button')].find((b) => b.textContent === 'Turn off') as PushButton
    toggle.click()
    await waitFor(() => expect(api.setRequiredSetEnabled).toHaveBeenCalledWith({ projectId: 'p1', enabled: false }))
  })
})
