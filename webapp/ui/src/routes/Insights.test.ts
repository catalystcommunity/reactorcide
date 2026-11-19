import { describe, expect, it, vi } from 'vitest'
import { waitFor } from '@testing-library/dom'
import type { GetInfraDashboardResponse, GetPipelineDashboardResponse, ObsSummary, OrgSummary } from '~/api/csilapi/types.gen.ts'

const ORGS: OrgSummary[] = [{ orgId: 'org-a', name: 'alpha', displayName: 'Alpha', status: 'active', isDefault: true, isPrivate: false }]

const SUMMARY: ObsSummary = {
  runs: 10,
  successes: 7,
  pipelineFailures: 2,
  infraFailures: 1,
  cancelled: 0,
  passRate: 7 / 9,
  infraFailureRate: 0.1,
  durationP50Seconds: 60,
  durationP95Seconds: 300,
  queueWaitP50Seconds: 5,
  queueWaitP95Seconds: 30,
  byClass: [
    { failureClass: 'infra.worker_lost', count: 1 },
    { failureClass: 'job', count: 2 },
  ],
  measuredRuns: 5,
  cpuCoreHours: 1.5,
  memoryGibHours: 2,
  cpuReservedCoreHours: 3,
  memoryReservedGibHours: 4,
}

const TREND = [
  { bucketStart: '2026-11-14T00:00:00Z', runs: 4, passRate: 0.75, durationP95Seconds: 200, infraFailures: 0, cpuCoreHours: 1, evalEvents: 0 },
  { bucketStart: '2026-11-15T00:00:00Z', runs: 0, infraFailures: 0, cpuCoreHours: 0, evalEvents: 0 },
  { bucketStart: '2026-11-16T00:00:00Z', runs: 6, passRate: 0.8, durationP95Seconds: 320, infraFailures: 1, cpuCoreHours: 0.5, evalEvents: 3, evalP95Seconds: 12 },
]

const PIPELINE: GetPipelineDashboardResponse = {
  rangeFrom: '2026-11-14T00:00:00Z',
  rangeTo: '2026-11-17T00:00:00Z',
  days: 7,
  totals: SUMMARY,
  trend: TREND,
  workflows: [
    { projectId: 'p1', projectName: 'app', workflowSecurityId: 'ci', ownerGroup: 'team-a', summary: SUMMARY, defaultBranch: SUMMARY, trend: TREND },
  ],
  flakyTests: [],
  alerts: [
    {
      ruleId: 'r1',
      name: 'ci-pass',
      scope: 'workflow',
      workflowSecurityId: 'ci',
      condition: 'pass_rate_below',
      refClassBucket: 'default',
      threshold: 0.9,
      factor: 1.5,
      windowHours: 24,
      state: 'firing',
      enabled: true,
      lastValue: 0.5,
      lastRuns: 10,
    },
  ],
}

const INFRA: GetInfraDashboardResponse = {
  rangeFrom: '2026-11-14T00:00:00Z',
  rangeTo: '2026-11-17T00:00:00Z',
  days: 30,
  totals: SUMMARY,
  lanes: [{ lane: 'default', summary: SUMMARY }],
  evalEvents: 3,
  evalP95Seconds: 12,
  trend: TREND,
  slos: [
    {
      sli: 'availability',
      lane: '',
      objective: 0.995,
      thresholdSeconds: 0,
      windowDays: 30,
      total: 100,
      good: 99,
      compliance: 0.99,
      errorBudgetRemaining: -1,
      budgetState: 'exhausted',
      measures: '',
    },
  ],
  capacity: {
    workersTotal: 2,
    workersActive: 2,
    workersQuarantined: 0,
    workersDisabled: 0,
    workersSeen: 1,
    backlogJobs: 0,
    backlogOldestAgeSeconds: 0,
    queues: [],
  },
}

vi.mock('~/api/client.ts', () => ({
  ServiceError: class extends Error {},
  TransportFailure: class extends Error {},
  api: {
    listOrgs: vi.fn(async () => ({ orgs: ORGS })),
    getPipelineDashboard: vi.fn(async () => PIPELINE),
    listAlertRules: vi.fn(async () => ({ rules: PIPELINE.alerts, canCreateOrgRules: false })),
    getInfraDashboard: vi.fn(async () => INFRA),
    getUsageDashboard: vi.fn(async () => ({ rangeFrom: '', rangeTo: '', days: 30, attribution: 'owner', groups: [] })),
  },
}))

vi.mock('~/store/resources.ts', () => ({
  useOrgs: () => ({
    state: () => ({ data: { orgs: ORGS }, error: undefined, loading: false, loaded: true }),
    sync: () => {},
    refresh: async () => {},
    release: () => {},
  }),
}))

import { api } from '~/api/client.ts'
import { syncRouteFromLocation } from '~/ui/router.ts'
import { mount, settle } from '~/test/mount.ts'
import { axisTicks, formatDuration, formatPercent } from '~/components/Charts.ts'
import { classMixText, foldSeries, parseDays, parseView, tag, type InsightsPage } from './Insights.ts'

async function renderPage(path: string): Promise<HTMLElement> {
  window.history.pushState({}, '', path)
  syncRouteFromLocation()
  const page = document.createElement(tag) as InsightsPage
  page.routeName = 'insights'
  mount(page)
  await settle()
  return page
}

describe('Insights helpers', () => {
  it('parses the view and the look-back range', () => {
    expect(parseView(null)).toBe('pipeline')
    expect(parseView('usage')).toBe('usage')
    expect(parseDays('90')).toBe(90)
    expect(parseDays('12')).toBe(30)
  })

  it('formats values', () => {
    expect(formatPercent(0.5)).toBe('50.0%')
    expect(formatPercent(undefined)).toBe('—')
    expect(formatDuration(30)).toBe('30 s')
    expect(formatDuration(600)).toBe('10.0 min')
    expect(formatDuration(7200)).toBe('2.0 h')
    expect(axisTicks(1, 'percent')).toEqual([0, 0.25, 0.5, 0.75, 1])
    expect(axisTicks(330, 'seconds').at(-1)).toBeGreaterThanOrEqual(330)
    expect(classMixText(SUMMARY.byClass)).toBe('infra.worker_lost 1, job 2')
  })

  it('folds series past four into Other', () => {
    const series = Array.from({ length: 6 }, (_, i) => ({ key: `g${i}`, label: `g${i}`, points: [{ x: 'd', y: i }] }))
    const folded = foldSeries(series)
    expect(folded).toHaveLength(4)
    expect(folded[3].label).toBe('Other (3)')
    expect(folded[3].points[0].y).toBe(3 + 4 + 5)
  })
})

describe('Insights page', () => {
  it('shows the pipeline view with an accessible chart and a table view', async () => {
    const page = await renderPage('/app/insights?view=pipeline&days=7')
    await waitFor(() => expect(api.getPipelineDashboard).toHaveBeenCalledWith({ orgId: 'org-a', days: 7, projectId: undefined }))
    await waitFor(() => expect(page.querySelectorAll('tbody tr').length).toBeGreaterThan(0))
    const svg = page.querySelector('svg.insight-chart')!
    expect(svg.getAttribute('role')).toBe('img')
    expect(document.getElementById(svg.getAttribute('aria-labelledby')!)).not.toBeNull()
    // A gap day has no point; the two other days have one each.
    expect(svg.querySelectorAll('.insight-point').length).toBe(2)
    expect(svg.querySelector('.insight-point title')!.textContent).toContain('75.0%')
    // The alert rules come from list-alert-rules for the org of the view.
    await waitFor(() => expect(api.listAlertRules).toHaveBeenCalledWith({ orgId: 'org-a' }))
    await waitFor(() => expect(page.textContent).toContain('▲ Firing'))
    expect(page.querySelector('.insight-table')).not.toBeNull()

    // A refresh keeps the row elements.
    const row = page.querySelector('.card tbody tr')
    await settle()
    expect(page.querySelector('.card tbody tr')).toBe(row)
  })

  it('shows the infrastructure view with SLO state as text, not color alone', async () => {
    const page = await renderPage('/app/insights?view=infrastructure')
    await waitFor(() => expect(api.getInfraDashboard).toHaveBeenCalledWith({ orgId: 'org-a', days: 30 }))
    await waitFor(() => expect(page.textContent).toContain('✕ Exhausted'))
    expect(page.textContent).toContain('Control-plane availability')
    // Two series (queue wait and eval latency) have a legend.
    const legends = [...page.querySelectorAll<HTMLElement>('.insight-legend')].filter((l) => !l.hidden)
    expect(legends.some((l) => l.textContent!.includes('Eval latency p95'))).toBe(true)
  })
})
