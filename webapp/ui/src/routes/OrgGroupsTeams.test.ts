import { describe, expect, it, vi } from 'vitest'
import { waitFor } from '@testing-library/dom'
import type { PushButton, TextField } from '@catalystcommunity/controls'
import type { GroupMemberDetail, GroupVcsLink, OrgSummary } from '~/api/csilapi/types.gen.ts'
import type { SessionSummary } from '~/api/auth.ts'

/** Groups linked to VCS teams on the Groups page (docs/principals.md). */

const ORGS: OrgSummary[] = [{ orgId: 'org-a', name: 'alpha', displayName: 'Alpha', status: 'active', isDefault: true, isPrivate: false }]
let links: GroupVcsLink[] = []
const MEMBERS: GroupMemberDetail[] = [
  { principalId: 'u1', kind: 'human', name: 'u1', displayName: 'Alice', userId: 'u1', username: 'alice', source: 'manual', manual: true, teams: [], vcsLogins: [], addedAt: '' },
  { principalId: 'p2', kind: 'human', name: 'github/bob', displayName: 'bob', source: 'vcs_sync', manual: false, teams: ['vcs_team:acme/platform'], vcsLogins: ['bob'], addedAt: '' },
]

vi.mock('~/api/client.ts', () => {
  class ServiceError extends Error {}
  class TransportFailure extends Error {}
  return {
    ServiceError,
    TransportFailure,
    api: {
      listGroups: vi.fn(async () => ({ groups: [{ groupId: 'g1', orgId: 'org-a', name: 'platform', createdAt: '', updatedAt: '', priorityShift: 0 }] })),
      listOrgs: vi.fn(async () => ({ orgs: ORGS })),
      listUsers: vi.fn(async () => ({ users: [] })),
      listGroupMemberDetails: vi.fn(async () => ({ members: MEMBERS })),
      listGroupVcsLinks: vi.fn(async () => ({ links })),
      linkGroupVcsTeam: vi.fn(async (req: { groupId: string; team: string }) => {
        const link: GroupVcsLink = { linkId: 'l1', groupId: req.groupId, provider: 'github', team: req.team, subject: `vcs_team:${req.team}`,
          lastError: '', failureCount: 0, memberCount: 0, createdAt: '' }
        links = [link]
        return { link }
      }),
      syncGroupVcsTeams: vi.fn(async () => {
        links = links.map((l) => ({ ...l, memberCount: 1, lastSyncedAt: '2026-11-16T00:00:00Z' }))
        return { results: [{ linkId: 'l1', team: 'acme/platform', members: 1, added: 1, removed: 0 }] }
      }),
      unlinkGroupVcsTeam: vi.fn(async () => {
        links = []
        return { removed: true }
      }),
    },
  }
})

vi.mock('~/store/resources.ts', () => ({
  useOrgs: () => ({
    state: () => ({ data: { orgs: ORGS }, error: undefined, loading: false, loaded: true }),
    sync: () => {},
    refresh: async () => {},
    release: () => {},
  }),
}))

vi.mock('~/ui/controls.ts', async (importOriginal) => ({
  ...(await importOriginal<typeof import('~/ui/controls.ts')>()),
  confirmAction: vi.fn(async () => true),
}))

import { api } from '~/api/client.ts'
import { setSessionForTest } from '~/lib/session.ts'
import { syncRouteFromLocation } from '~/ui/router.ts'
import { mount, settle } from '~/test/mount.ts'
import { memberSource, tag, type OrgGroupsPage } from './OrgGroups.ts'

function session(): SessionSummary {
  return {
    logged_in: true,
    user_id: 'user-1',
    display_name: 'Someone',
    is_global_admin: false,
    capabilities: {} as SessionSummary['capabilities'],
    roles: [{ scope_type: 'org', scope_id: 'org-a', role: 'admin' }],
  }
}

async function openGroup(): Promise<HTMLElement> {
  window.history.pushState({}, '', '/app/org/groups?org=org-a')
  syncRouteFromLocation()
  const page = document.createElement(tag) as OrgGroupsPage
  page.routeName = 'orgGroups'
  mount(page)
  await settle()
  await waitFor(() => expect(page.querySelector('.group-toggle')).not.toBeNull())
  ;(page.querySelector('.group-toggle') as PushButton).click()
  await settle()
  await waitFor(() => expect(page.querySelector('rc-group-teams')).not.toBeNull())
  return page
}

describe('OrgGroups linked VCS teams', () => {
  it('shows the source of each member, and no remove for a synced member', async () => {
    links = []
    setSessionForTest(session())
    const page = await openGroup()
    await waitFor(() => expect(page.querySelectorAll('.member-row').length).toBe(2))
    const rows = [...page.querySelectorAll('.member-row')]
    expect(rows[0].querySelector('.member-source')!.textContent).toBe('Manual')
    expect(rows[1].querySelector('.member-source')!.textContent).toBe('Linked team')
    expect(rows[1].querySelector('.member-teams')!.textContent).toBe('vcs_team:acme/platform')
    expect((rows[0].querySelector('push-button') as HTMLElement).hidden).toBe(false)
    expect((rows[1].querySelector('push-button') as HTMLElement).hidden).toBe(true)
    expect(memberSource({ ...MEMBERS[0], teams: ['vcs_team:a/b'] })).toBe('Manual and linked team')
  })

  it('links, syncs and unlinks a team', async () => {
    links = []
    setSessionForTest(session())
    const page = await openGroup()
    const teams = page.querySelector('rc-group-teams') as HTMLElement
    await waitFor(() => expect(teams.textContent).toContain('No VCS team is linked'))
    const input = teams.querySelector('text-field') as TextField
    input.value = 'acme/platform'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await settle()
    teams.querySelector('form')!.requestSubmit()
    await waitFor(() => expect(api.linkGroupVcsTeam).toHaveBeenCalledWith({ groupId: 'g1', team: 'acme/platform' }))
    await waitFor(() => expect(teams.querySelector('.team-name')?.textContent).toBe('vcs_team:acme/platform'))
    expect(teams.querySelector('.team-last')!.textContent).toBe('Never')

    const sync = teams.querySelector('.group-teams-sync') as PushButton
    await waitFor(() => expect(sync.disabled).toBe(false))
    sync.click()
    await waitFor(() => expect(teams.querySelector('.group-teams-result')!.textContent).toContain('1 added'))
    await waitFor(() => expect(teams.querySelector('.team-members')!.textContent).toBe('1'))

    ;(teams.querySelector('.team-row push-button') as PushButton).click()
    await waitFor(() => expect(api.unlinkGroupVcsTeam).toHaveBeenCalledWith({ groupId: 'g1', linkId: 'l1' }))
    await waitFor(() => expect(teams.querySelector('.team-row')).toBeNull())
  })
})
