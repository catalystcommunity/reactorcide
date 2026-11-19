import { afterEach, describe, expect, it, vi } from 'vitest'
import { waitFor } from '@testing-library/dom'
import type { ListGitHubAppsResponse } from '~/api/csilapi/types.gen.ts'

let response: ListGitHubAppsResponse

vi.mock('~/api/client.ts', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/client.ts')>()
  return {
    ...actual,
    api: {
      listGithubApps: vi.fn(async () => response),
      setGithubAppEnabled: vi.fn(async (req: { appId: number; enabled: boolean }) => {
        response = { ...response, apps: response.apps.map((app) => (app.appId === req.appId ? { ...app, enabled: req.enabled } : app)) }
        return { app: response.apps.find((app) => app.appId === req.appId) }
      }),
      syncGithubApp: vi.fn(async () => ({ installations: 2, created: 1, disabled: 0, errors: [] })),
    },
  }
})

vi.mock('~/ui/controls.ts', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/ui/controls.ts')>()
  return { ...actual, confirmAction: vi.fn(async () => true) }
})

import { api } from '~/api/client.ts'
import { mount, settle, unmount } from '~/test/mount.ts'
import { githubAppsCard, installationState, rateLimitText, repositoriesText } from './GitHubApps.ts'

let mounted: Element | null = null

afterEach(() => {
  if (mounted) unmount(mounted)
  mounted = null
  vi.clearAllMocks()
})

const twoApps = (): ListGitHubAppsResponse => ({
  apps: [
    { appId: 1, slug: 'rc-a', name: 'A', enabled: true, openRegistration: false, headroomPercent: 12, activeInstallations: 1, repositories: 30, recommended: false,
      installUrl: 'https://github.com/apps/rc-a/installations/new', ratelimitLimit: 5000, ratelimitRemaining: 600, createdAt: '2026-11-16T00:00:00Z' },
    { appId: 2, slug: 'rc-b', name: 'B', enabled: true, openRegistration: true, headroomPercent: 91, activeInstallations: 2, repositories: 4, recommended: true,
      installUrl: 'https://github.com/apps/rc-b/installations/new', createdAt: '2026-11-16T00:00:00Z' },
  ],
  installations: [
    { installationId: 77, appId: 2, accountLogin: 'acme', accountType: 'Organization', repositorySelection: 'selected', suspended: false,
      deleted: false, headroomPercent: 91, ratelimitLimit: 5000, ratelimitRemaining: 4550, lastSeenAt: '2026-11-16T00:00:00Z', repositories: ['acme/app', 'acme/lib'],
      pendingRepositories: ['acme/lib'] },
  ],
})

describe('GitHub Apps card', () => {
  it('lists Apps with headroom, the recommended App, and installations', async () => {
    response = twoApps()
    const el = githubAppsCard()
    mounted = mount(el)
    await settle()
    await waitFor(() => expect(el.textContent).toContain('rc-b (2) — recommended'))
    expect(el.textContent).toContain('12%')
    expect(el.textContent).toContain('600 of 5,000 left')
    expect(el.textContent).toContain('acme/lib (pending approval)')
    expect(el.textContent).toContain('enabled, open registration')
    expect(el.textContent).toContain('acme/app, acme/lib')
    expect(el.textContent).toContain('acme (Organization, selected)')
    const none = [...el.querySelectorAll('p.empty')].find((p) => p.textContent?.includes('No GitHub App')) as HTMLElement
    expect(none.hidden).toBe(true)
    const install = el.querySelector('a.app-install') as HTMLAnchorElement
    expect(install.href).toBe('https://github.com/apps/rc-a/installations/new')
  })

  it('disables an App after a confirmation and keeps the row element', async () => {
    response = twoApps()
    const el = githubAppsCard()
    mounted = mount(el)
    await settle()
    await waitFor(() => expect(el.querySelectorAll('tbody tr').length).toBeGreaterThan(1))
    const row = el.querySelector('tbody tr') as HTMLTableRowElement
    const toggle = row.querySelector('push-button') as HTMLElement
    toggle.click()
    await waitFor(() => expect(api.setGithubAppEnabled).toHaveBeenCalledWith({ appId: 1, enabled: false }))
    await waitFor(() => expect(row.cells[1].textContent).toBe('disabled'))
    expect(el.querySelector('tbody tr')).toBe(row)
    expect(toggle.textContent).toBe('Enable')
  })

  it('starts a sync and shows the result', async () => {
    response = twoApps()
    const el = githubAppsCard()
    mounted = mount(el)
    await settle()
    await waitFor(() => expect(el.querySelectorAll('tbody tr').length).toBeGreaterThan(1))
    const sync = el.querySelectorAll('tbody tr')[1].querySelectorAll('push-button')[1] as HTMLElement
    sync.click()
    await waitFor(() => expect(api.syncGithubApp).toHaveBeenCalledWith({ appId: 2 }))
    await waitFor(() => expect(el.textContent).toContain('2 installations, 1 new projects'))
  })

  it('says when no App is registered', async () => {
    response = { apps: [], installations: [] }
    const el = githubAppsCard()
    mounted = mount(el)
    await settle()
    await waitFor(() => {
      const none = [...el.querySelectorAll('p.empty')].find((p) => p.textContent?.includes('No GitHub App')) as HTMLElement
      expect(none.hidden).toBe(false)
    })
  })

  it('formats rate limits and states', () => {
    expect(rateLimitText()).toBe('no data yet')
    expect(rateLimitText(1, 5000)).toBe('1 of 5,000 left')
    expect(installationState({ deleted: true, suspended: true } as never)).toBe('deleted')
    expect(installationState({ deleted: false, suspended: true } as never)).toBe('suspended')
  })
})

describe('repositoriesText', () => {
  it('marks the repositories that wait for approval', () => {
    const installation = twoApps().installations[0]
    expect(repositoriesText(installation)).toBe('acme/app, acme/lib (pending approval)')
    expect(repositoriesText({ ...installation, repositories: [], pendingRepositories: [] })).toBe('none')
  })
})
