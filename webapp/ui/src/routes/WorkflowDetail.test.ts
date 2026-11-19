import { describe, expect, it, vi } from 'vitest'
import type { GetEventResponse, GetWorkflowResponse } from '~/api/csilapi/types.gen.ts'
import type { ResourceState } from '~/store/index.ts'

let workflow: GetWorkflowResponse
let event: GetEventResponse | Error

vi.mock('~/api/client.ts', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/client.ts')>()
  return {
    ...actual,
    api: {
      getEvent: vi.fn(async () => {
        if (event instanceof Error) throw event
        return event
      }),
    },
  }
})

vi.mock('~/store/resources.ts', () => {
  const ready = <T>(data: T): ResourceState<T> => ({ data, error: undefined, loading: false, loaded: true })
  const handle = <T>(state: () => ResourceState<T>) => ({ state, refresh: async () => {}, sync: () => {}, release: () => {} })
  return {
    useWorkflow: () => handle(() => ready(workflow)),
    useCapabilities: () => handle(() => ready({})),
  }
})

import { mount, settle } from '~/test/mount.ts'
import { WorkflowDetailPage, tag } from './WorkflowDetail.ts'

function makeWorkflow(overrides: Partial<GetWorkflowResponse> = {}): GetWorkflowResponse {
  return {
    workflow: {
      workflowId: 'wf-1',
      kind: 'workflow',
      name: 'Build',
      status: 'success',
      projectId: 'p1',
      createdAt: '2026-11-14T10:00:00Z',
      updatedAt: '2026-11-14T10:05:00Z',
      queueName: 'q',
      vcsRepo: 'acme/widget',
      commitSha: 'abc',
      jobCount: 3,
      runningCount: 0,
      completedCount: 1,
      failedCount: 0,
      skippedCount: 2,
      decisionSummary: '',
      originType: 'push',
      triggerType: 'runnerlib_eval',
      ciOrigin: 'base',
      executionProfile: 'standard',
      workerClass: 'default',
      eventId: 'e1',
    },
    nodes: [
      {
        nodeId: 'n1',
        name: 'build',
        displayName: 'build',
        status: 'completed',
        dependsOn: [],
        condition: 'all_success',
        jobId: 'j1',
        decisionReason: 'dependencies satisfied and condition true',
        ciOrigin: 'base',
        executionProfile: 'standard',
        workerClass: 'default',
      },
      {
        nodeId: 'n2',
        name: 'docs',
        displayName: 'docs',
        status: 'skipped',
        dependsOn: [],
        condition: 'all_success',
        decisionReason: 'skipped by if: no changed file matches paths include [docs/**]',
        decisionReasons: [{ kind: 'paths', matched: false, pathsInclude: ['docs/**'] }],
        ciOrigin: 'base',
        executionProfile: 'standard',
        workerClass: 'default',
      },
      {
        nodeId: 'n3',
        name: 'publish-docs',
        displayName: 'publish-docs',
        status: 'skipped',
        dependsOn: ['docs'],
        condition: 'all_success',
        decisionReason: '',
        decisionReasons: [{ kind: 'dependency', matched: false, condition: 'all_success', deps: [{ name: 'docs', status: 'skipped' }] }],
        ciOrigin: 'base',
        executionProfile: 'standard',
        workerClass: 'default',
      },
    ],
    jobs: [],
    why: {
      workflowSecurityId: 'build',
      workflowName: 'Build',
      decision: 'run',
      reasons: [
        { kind: 'event', matched: true, value: 'push', patterns: ['push'] },
        { kind: 'paths', matched: true, pathsInclude: ['api/**'], matchedFiles: ['api/a.go'], matchedCount: 1 },
      ],
    },
    events: [
      { createdAt: '2026-11-14T10:00:01Z', eventType: 'node_submitted', nodeId: 'n1', reason: 'dependencies satisfied' },
      { createdAt: '2026-11-14T10:00:02Z', eventType: 'node_skipped', nodeId: 'n2', reason: 'skipped by if' },
    ],
    ...overrides,
  }
}

function makeEvent(workflowIds: string[]): GetEventResponse {
  return {
    event: {
      eventId: 'e1',
      source: 'webhook',
      provider: 'github',
      eventType: 'push',
      repo: 'acme/widget',
      commitSha: 'abc',
      receivedAt: '2026-11-14T10:00:00Z',
      status: 'evaluated',
      evalJobId: 'job-eval',
      changedFilesCount: 0,
      changedFilesPreview: [],
    },
    decisions: [],
    workflowIds,
  }
}

async function renderWorkflow(): Promise<WorkflowDetailPage> {
  window.history.pushState({}, '', '/app/workflows/wf-1')
  const { syncRouteFromLocation } = await import('~/ui/router.ts')
  syncRouteFromLocation()
  const page = document.createElement(tag) as WorkflowDetailPage
  page.routeName = 'workflow'
  mount(page)
  await settle()
  await settle()
  return page
}

describe('WorkflowDetail: why it runs', () => {
  it('has a closed "Why is this workflow running?" section with the reasons and the event link', async () => {
    workflow = makeWorkflow()
    event = makeEvent(['wf-1'])
    const page = await renderWorkflow()
    const why = page.querySelector<HTMLDetailsElement>('details.why')!
    expect(why.open).toBe(false)
    expect(why.querySelector('summary')?.textContent).toBe('Why is this workflow running?')
    expect(why.textContent).toContain('Event "push" matches: push')
    expect(why.textContent).toContain('1 changed file match paths include api/**')
    expect(why.querySelector('a[href="/app/events/e1"]')).toBeTruthy()
  })

  it('shows the trusted owner and contact in the "why" section', async () => {
    workflow = makeWorkflow()
    workflow.workflow.owner = { group: 'payments-ci', channel: '#payments-ci', source: 'ci', ciSha: 'abc123' }
    event = makeEvent(['wf-1'])
    const page = await renderWorkflow()
    const owner = page.querySelector<HTMLElement>('details.why [data-testid="workflow-owner"]')!
    expect(owner.textContent).toContain('payments-ci')
    expect(owner.textContent).toContain('#payments-ci')
    expect(owner.textContent).toContain('Read from the trusted CI definition at abc123.')
  })

  it('says so plainly when no reason was recorded', async () => {
    workflow = makeWorkflow({ why: undefined })
    workflow.workflow.eventId = undefined
    event = makeEvent([])
    const page = await renderWorkflow()
    const why = page.querySelector<HTMLDetailsElement>('details.why')!
    expect(why.textContent).toContain('No reason was recorded for this workflow.')
    expect((why.querySelector('a') as HTMLElement).parentElement!.hidden).toBe(true)
  })

  it('shows a reason for every node, including skipped ones', async () => {
    workflow = makeWorkflow()
    event = makeEvent(['wf-1'])
    const page = await renderWorkflow()
    const rows = [...page.querySelectorAll('tbody:not(.eval-row) tr')]
    expect(rows).toHaveLength(3)
    expect(rows[1].querySelector('.node-reason')?.textContent).toContain('skipped by if')
    // A node with only structured reasons gets a summary from them.
    expect(rows[2].querySelector('.node-reason')?.textContent).toBe('Condition all_success is not met: docs skipped')
    expect((rows[1].querySelector('details.node-why') as HTMLElement).hidden).toBe(false)
    expect((rows[0].querySelector('details.node-why') as HTMLElement).hidden).toBe(true)
  })

  it('puts "Evaluate event" first when the event made only this workflow', async () => {
    workflow = makeWorkflow()
    event = makeEvent(['wf-1'])
    const page = await renderWorkflow()
    const evalRow = page.querySelector<HTMLElement>('tbody.eval-row')!
    expect(evalRow.hidden).toBe(false)
    expect(evalRow.querySelector('a[href="/app/jobs/job-eval"]')?.textContent).toBe('Evaluate event')
  })

  it('leaves the eval out when the event made several workflows, or cannot be read', async () => {
    workflow = makeWorkflow()
    event = makeEvent(['wf-1', 'wf-2'])
    let page = await renderWorkflow()
    expect(page.querySelector<HTMLElement>('tbody.eval-row')!.hidden).toBe(true)
    page.remove()

    event = new Error('forbidden')
    page = await renderWorkflow()
    expect(page.querySelector<HTMLElement>('tbody.eval-row')!.hidden).toBe(true)
  })

  it('lists the workflow events in a closed section', async () => {
    workflow = makeWorkflow()
    event = makeEvent(['wf-1'])
    const page = await renderWorkflow()
    const events = page.querySelector<HTMLDetailsElement>('details.workflow-events')!
    expect(events.open).toBe(false)
    const items = [...events.querySelectorAll('.timeline-item')]
    expect(items).toHaveLength(2)
    expect(items[1].textContent).toContain('node skipped')
    expect(items[1].textContent).toContain('docs')
  })
})
