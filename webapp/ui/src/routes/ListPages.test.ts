import { describe, expect, it, vi, beforeEach } from 'vitest'
import { waitFor } from '@testing-library/dom'
import type { PushButton, TextField } from '@catalystcommunity/controls'
import type { JobSummary, WorkflowSummaryDetail } from '~/api/csilapi/types.gen.ts'

function workflow(id: string, extra: Partial<WorkflowSummaryDetail> = {}): WorkflowSummaryDetail {
  return {
    workflowId: id,
    kind: 'workflow',
    name: `wf-${id}`,
    status: 'success',
    createdAt: '2026-11-01T00:00:00Z',
    updatedAt: '2026-11-01T00:00:00Z',
    queueName: 'q',
    vcsRepo: 'github.com/x/y',
    commitSha: 'abcdef0123456789',
    jobCount: 1,
    runningCount: 0,
    completedCount: 1,
    failedCount: 0,
    skippedCount: 0,
    decisionSummary: '',
    originType: '',
    triggerType: '',
    ciOrigin: '',
    executionProfile: '',
    workerClass: '',
    ...extra,
  } as WorkflowSummaryDetail
}

function job(id: string, extra: Partial<JobSummary> = {}): JobSummary {
  return { jobId: id, name: `job-${id}`, description: '', status: 'completed', lastError: '', createdAt: '2026-11-01T00:00:00Z', updatedAt: '2026-11-01T00:00:00Z', ...extra } as JobSummary
}

vi.mock('~/api/client.ts', () => {
  class ServiceError extends Error {}
  class TransportFailure extends Error {}
  return {
    ServiceError,
    TransportFailure,
    api: {
      listWorkflows: vi.fn(async (req: { offset?: number }) => ({
        workflows: [workflow('w1', { branch: 'todpunk/alpha' }), workflow('w2')],
        total: 230,
        limit: 50,
        offset: req.offset ?? 0,
      })),
      listJobs: vi.fn(async (req: { offset?: number }) => ({ jobs: [job('j1', { branch: 'main' })], total: 1, limit: 50, offset: req.offset ?? 0 })),
      listProjects: vi.fn(async () => ({
        projects: [{ projectId: 'p-1', orgId: 'o', name: 'zest', description: '', repoUrl: 'github.com/x/zest', isPrivate: false, enabled: true, createdAt: '' }],
      })),
      describeFormMetadata: vi.fn(async () => ({ workflowStatuses: [], jobStatuses: [] })),
    },
  }
})

import { api } from '~/api/client.ts'
import { resetStore } from '~/store/index.ts'
import { syncRouteFromLocation } from '~/ui/router.ts'
import { mount, settle } from '~/test/mount.ts'
import { pageRange, listQuery } from '~/components/ListControls.ts'
import { tag as workflowsTag } from './Workflows.ts'
import { tag as jobsTag } from './Jobs.ts'

async function open(tagName: string, routeName: string, url: string): Promise<HTMLElement> {
  window.history.pushState({}, '', url)
  syncRouteFromLocation()
  const page = document.createElement(tagName) as HTMLElement & { routeName: string }
  page.routeName = routeName
  mount(page)
  await settle()
  return page
}

function query(): URLSearchParams {
  return new URLSearchParams(window.location.search)
}

beforeEach(() => {
  resetStore()
  vi.mocked(api.listWorkflows).mockClear()
  vi.mocked(api.listJobs).mockClear()
})

describe('list query helpers', () => {
  it('reads the filters and the page', () => {
    expect(listQuery(new URLSearchParams('name=lint&branch=todpunk/*&project=p-1&page=3'))).toEqual({
      name: 'lint',
      branch: 'todpunk/*',
      projectId: 'p-1',
      offset: 100,
    })
    expect(listQuery(new URLSearchParams('page=0')).offset).toBe(0)
    expect(listQuery(new URLSearchParams('page=x')).offset).toBe(0)
  })

  it('tells the range of a page', () => {
    expect(pageRange(230, 50, 50)).toBe('51–100 of 230')
    expect(pageRange(0, 0, 0)).toBe('0 of 0')
    expect(pageRange(10, 50, 0)).toContain('Past the last row')
  })
})

describe('Workflows list', () => {
  it('sends the filters and the page from the URL, and shows the branch', async () => {
    const page = await open(workflowsTag, 'workflows', '/app/?name=build&branch=todpunk%2F*&project=p-1&page=2')
    await waitFor(() => expect(page.querySelectorAll('tbody tr').length).toBe(2))
    expect(api.listWorkflows).toHaveBeenLastCalledWith({
      status: undefined,
      projectId: 'p-1',
      name: 'build',
      branch: 'todpunk/*',
      limit: 50,
      offset: 50,
    })
    expect(page.querySelector('.wf-commit')?.textContent).toBe('todpunk/alpha · abcdef012345')
    expect(page.querySelector('.pager-range')?.textContent).toBe('51–52 of 230')
    expect(page.querySelector<TextField>('.list-filter-branch')?.value).toBe('todpunk/*')
  })

  it('goes to an older page and back', async () => {
    const page = await open(workflowsTag, 'workflows', '/app/')
    await waitFor(() => expect(page.querySelector('.pager')?.hasAttribute('hidden')).toBe(false))
    const [newer, older] = [...page.querySelectorAll<PushButton>('.pager push-button')]
    expect(newer.disabled).toBe(true)
    older.click()
    await settle()
    expect(query().get('page')).toBe('2')
    await waitFor(() => expect(api.listWorkflows).toHaveBeenLastCalledWith(expect.objectContaining({ offset: 50 })))
    newer.click()
    await settle()
    expect(query().get('page')).toBeNull()
  })

  it('a name filter change goes back to the first page', async () => {
    const page = await open(workflowsTag, 'workflows', '/app/?page=3')
    const field = page.querySelector<TextField>('.list-filter-name')!
    field.value = 'deploy'
    field.dispatchEvent(new Event('change', { bubbles: true }))
    await settle()
    expect(query().get('name')).toBe('deploy')
    expect(query().get('page')).toBeNull()
    await waitFor(() => expect(api.listWorkflows).toHaveBeenLastCalledWith(expect.objectContaining({ name: 'deploy', offset: undefined })))
  })
})

describe('Jobs list', () => {
  it('sends the name and branch filters and hides the pager for one page', async () => {
    const page = await open(jobsTag, 'jobs', '/app/jobs?name=lint&branch=main')
    await waitFor(() => expect(page.querySelectorAll('tbody tr').length).toBe(1))
    expect(api.listJobs).toHaveBeenLastCalledWith(expect.objectContaining({ name: 'lint', branch: 'main', limit: 50, offset: undefined }))
    expect(page.querySelector('.job-branch')?.textContent).toBe('main')
    expect(page.querySelector('.pager')?.hasAttribute('hidden')).toBe(true)
  })
})
