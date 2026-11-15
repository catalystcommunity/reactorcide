import { describe, expect, it, vi } from 'vitest'
import type { GetEventResponse } from '~/api/csilapi/types.gen.ts'
import type { ResourceState } from '~/store/index.ts'

/** What the mocked event hook answers; set per test. */
let state: ResourceState<GetEventResponse>

vi.mock('~/store/resources.ts', () => ({
  useEvent: () => ({ state: () => state, refresh: async () => {}, sync: () => {}, release: () => {} }),
}))

vi.mock('~/api/client.ts', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/client.ts')>()
  return {
    ...actual,
    api: { getPrApprovals: vi.fn(async () => ({ headSha: 'feedface00112233', actions: [], reviews: [] })) },
  }
})

import { api } from '~/api/client.ts'
import { mount, settle } from '~/test/mount.ts'
import { EventDetailPage, tag } from './EventDetail.ts'

const EVENT: GetEventResponse = {
  event: {
    eventId: 'e1',
    source: 'webhook',
    provider: 'github',
    eventType: 'push',
    repo: 'acme/widget',
    projectId: 'p1',
    branch: 'main',
    commitSha: '0123456789abcdef0123',
    actor: 'alice',
    receivedAt: '2026-11-14T10:00:00Z',
    status: 'evaluated',
    evalJobId: 'job-eval',
    changedFilesCount: 3,
    changedFilesPreview: ['api/a.go', 'docs/x.md'],
    deliveryId: 'delivery-123',
    checksState: 'success',
    checksDescription: '1 workflow passed, 1 skipped',
  },
  decisions: [
    {
      workflowSecurityId: 'build',
      workflowName: 'Build',
      sourceFile: '.reactorcide/workflows/build.yaml',
      decision: 'run',
      workflowId: 'wf-1',
      reasons: [{ kind: 'event', matched: true, value: 'push', patterns: ['push'] }],
    },
    {
      workflowSecurityId: 'docs',
      workflowName: 'Docs',
      decision: 'skipped',
      reasons: [{ kind: 'paths', matched: false, pathsInclude: ['site/**'] }],
    },
    {
      workflowSecurityId: 'file:.reactorcide/workflows/bad.yaml',
      workflowName: '',
      sourceFile: '.reactorcide/workflows/bad.yaml',
      decision: 'invalid',
      reasons: [{ kind: 'invalid', detail: 'jobs must be a mapping' }],
    },
  ],
  workflowIds: ['wf-1'],
}

async function renderEvent(): Promise<EventDetailPage> {
  window.history.pushState({}, '', '/app/events/e1')
  const { syncRouteFromLocation } = await import('~/ui/router.ts')
  syncRouteFromLocation()
  const page = document.createElement(tag) as EventDetailPage
  page.routeName = 'event'
  mount(page)
  await settle()
  return page
}

describe('EventDetail', () => {
  it('shows the event facts and links its eval job', async () => {
    state = { data: EVENT, error: undefined, loading: false, loaded: true }
    const page = await renderEvent()
    expect(page.querySelector('h1')?.textContent).toBe('Event push main')
    expect(page.textContent).toContain('acme/widget')
    expect(page.textContent).toContain('0123456789ab')
    expect(page.querySelector('a[href="/app/jobs/job-eval"]')?.textContent).toBe('job-eval')
    expect(page.textContent).toContain('delivery-123')
    expect(page.textContent).toContain('success: 1 workflow passed, 1 skipped')
  })

  it('lists every workflow decision with its reasons, and links the workflow it created', async () => {
    state = { data: EVENT, error: undefined, loading: false, loaded: true }
    const page = await renderEvent()
    const items = [...page.querySelectorAll('.decision-item')]
    expect(items).toHaveLength(3)
    expect(items[0].querySelector('a.decision-link')?.getAttribute('href')).toBe('/app/workflows/wf-1')
    expect(items[0].querySelector('.decision')?.textContent).toContain('Run')
    // Sorted: what ran, then what is broken, then what was skipped.
    expect(items[1].querySelector('.decision')?.textContent).toContain('Invalid')
    // An invalid file with no name is named by its path.
    expect(items[1].querySelector('.decision-plain')?.textContent).toBe('.reactorcide/workflows/bad.yaml')
    expect(items[1].textContent).toContain('Invalid workflow: jobs must be a mapping')
    expect(items[2].querySelector('.decision')?.textContent).toContain('Skipped')
    expect(items[2].textContent).toContain('No changed file matches paths include site/**')
    // A skipped workflow made no workflow, so its name is not a link.
    expect((items[2].querySelector('a.decision-link') as HTMLElement).hidden).toBe(true)
    expect(page.textContent).toContain('1 of 3 run')
  })

  it('shows the changed-file preview and how many more there are', async () => {
    state = { data: EVENT, error: undefined, loading: false, loaded: true }
    const page = await renderEvent()
    const files = [...page.querySelectorAll('.changed-files li')].map((li) => li.textContent)
    expect(files).toEqual(['api/a.go', 'docs/x.md', '+1 more'])
  })

  it('shows the refusal when the event cannot be read', async () => {
    state = { data: undefined, error: new Error('event not found'), loading: false, loaded: false }
    const page = await renderEvent()
    const alert = page.querySelector<HTMLElement>('.alert-error')!
    expect(alert.hidden).toBe(false)
    expect(alert.textContent).toBe('event not found')
    expect(page.querySelector('.decision-item')).toBeNull()
  })

  it('shows the pull request approvals panel for a pull request event only', async () => {
    state = { data: EVENT, error: undefined, loading: false, loaded: true }
    let page = await renderEvent()
    expect(page.querySelector('rc-pr-approvals-panel')).toBeNull()
    page.remove()

    state = { data: { ...EVENT, event: { ...EVENT.event, eventType: 'pull_request_updated', prNumber: 12 } }, error: undefined, loading: false, loaded: true }
    page = await renderEvent()
    const panel = page.querySelector<HTMLElement>('rc-pr-approvals-panel')!
    expect(api.getPrApprovals).toHaveBeenCalledWith({ projectId: 'p1', prNumber: 12 })
    await settle()
    expect(panel.hidden).toBe(false)
    expect(panel.querySelector('.pr-head')?.textContent).toBe('feedface0011')
    page.remove()
  })
})
