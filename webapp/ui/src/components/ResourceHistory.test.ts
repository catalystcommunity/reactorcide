import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { GetJobResourceHistoryResponse, JobRunResourceSummary } from '~/api/csilapi/types.gen.ts'

let response: GetJobResourceHistoryResponse
const getJobResourceHistory = vi.fn(async () => structuredClone(response))

vi.mock('~/api/client.ts', () => ({
  api: { getJobResourceHistory: (...args: unknown[]) => getJobResourceHistory(...(args as [])) },
  ServiceError: class extends Error {},
  TransportFailure: class extends Error {},
}))

import { flushRenders } from '@catalystcommunity/base-components'
import { mount, settle } from '~/test/mount.ts'
import { currentRoute } from '~/ui/router.ts'
import {
  PENDING_MAX_TRIES,
  PENDING_RETRY_MS,
  analyze,
  anomalyThreshold,
  formatMetric,
  formatSeconds,
  mad,
  median,
  niceTicks,
  quantile,
  resourceHistory,
  unusedReservation,
  type ResourceHistory,
} from './ResourceHistory.ts'

function run(id: string, minute: number, cpuMax: number, extra: Partial<JobRunResourceSummary> = {}): JobRunResourceSummary {
  return {
    jobId: id,
    status: 'completed',
    createdAt: new Date(Date.UTC(2026, 10, 1, 0, minute)).toISOString(),
    durationSeconds: 120,
    sampleCount: 30,
    cpuMaxMillicores: cpuMax,
    cpuP95Millicores: Math.round(cpuMax * 0.8),
    cpuAvgMillicores: Math.round(cpuMax * 0.5),
    cpuRequestMillicores: 1000,
    cpuLimitMillicores: 2000,
    ...extra,
  }
}

function runs(): JobRunResourceSummary[] {
  return [run('a', 0, 500), run('b', 10, 520), run('c', 20, 480), run('d', 30, 510), run('e', 40, 1900), run('f', 50, 495)]
}

describe('history statistics', () => {
  it('computes median, quantiles and MAD', () => {
    expect(median([3, 1, 2])).toBe(2)
    expect(median([1, 2, 3, 4])).toBe(2.5)
    expect(quantile([0, 10], 0.95)).toBeCloseTo(9.5)
    expect(mad([1, 1, 2, 2, 4, 6, 9])).toBe(1)
    expect(Number.isNaN(median([]))).toBe(true)
  })

  it('flags a run above median + 3×MAD, and falls back to 2× median when MAD is 0', () => {
    expect(anomalyThreshold([500, 520, 480, 510, 1900, 495])).toBeCloseTo(505 + 3 * 12.5)
    expect(anomalyThreshold([100, 100, 100, 100, 100, 450])).toBe(200)
    // Too few runs, or nothing but zeros: no verdict.
    expect(anomalyThreshold([1, 2, 100])).toBe(Infinity)
    expect(anomalyThreshold([0, 0, 0, 0, 0, 0])).toBe(Infinity)
    const result = analyze(runs(), 'cpu', 'all', true)
    expect(result.anomalies.map((v) => v.run.jobId)).toEqual(['e'])
  })

  it('reports the unused share of the request from the p95 of per-run p95', () => {
    expect(unusedReservation(1000, [400, 400, 400])).toBeCloseTo(0.6)
    expect(unusedReservation(1000, [1500])).toBeCloseTo(-0.5)
    expect(unusedReservation(undefined, [400])).toBeUndefined()
    expect(unusedReservation(1000, [])).toBeUndefined()
    // The typical run ignores the spike that the p95 figure reports.
    expect(unusedReservation(1000, [400, 400, 400, 400, 2000], 0.5)).toBeCloseTo(0.6)
    expect(unusedReservation(1000, [400, 400, 400, 400, 2000])).toBeLessThan(0)
    expect(analyze(runs(), 'cpu', 'all', true).request).toBe(1000)
    // Memory is limit-only: the limit is the reservation the unused share is measured against.
    const memRuns = [0, 1, 2].map((i) => run(`m${i}`, i, 500, { memoryP95Bytes: 2 ** 30, memoryMaxBytes: 2 ** 30, memoryLimitBytes: 4 * 2 ** 30 }))
    const memory = analyze(memRuns, 'memory', 'all', true)
    expect(memory.request).toBeUndefined()
    expect(memory.unusedTypical).toBeCloseTo(0.75)
  })

  it('filters by status and window, and counts unmeasured runs', () => {
    const list = [...runs(), run('g', 60, 0, { status: 'failed' }), run('h', 70, 0, { cpuMaxMillicores: undefined })]
    expect(analyze(list, 'cpu', 'all', true).shown).toHaveLength(7)
    expect(analyze(list, 'cpu', 'all', false).shown).toHaveLength(8)
    expect(analyze(list, 'cpu', 'all', true).unmeasured).toBe(1)
    expect(analyze(list, 'cpu', '25', true).shown).toHaveLength(7)
  })
})

describe('history formatting', () => {
  it('formats seconds, cores and bytes for people', () => {
    expect(formatSeconds(65)).toBe('1:05')
    expect(formatSeconds(3725)).toBe('1:02:05')
    expect(formatMetric(1500, 'millicores')).toBe('1.50 CPU')
    expect(formatMetric(250, 'millicores')).toBe('250 mCPU')
    expect(formatMetric(3 * 1024 ** 3, 'bytes')).toBe('3.0 GiB')
    expect(formatMetric(undefined, 'bytes')).toBe('—')
  })

  it('makes clean axis ticks in the natural unit', () => {
    const cpu = niceTicks(1900, 'millicores')
    expect(cpu.top).toBeGreaterThanOrEqual(1900)
    expect(cpu.ticks[0]).toBe(0)
    expect(cpu.label(cpu.ticks[1])).toMatch(/CPU$/)
    const mem = niceTicks(1.5 * 1024 ** 3, 'bytes')
    expect(mem.label(mem.ticks[1])).toMatch(/GiB$/)
    const small = niceTicks(300, 'millicores')
    expect(small.label(small.ticks[1])).toMatch(/mCPU$/)
  })
})

describe('rc-resource-history', () => {
  beforeEach(() => {
    response = { name: 'build', projectId: 'p1', runs: runs(), pending: 0 }
    getJobResourceHistory.mockClear()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('draws one point per measured run, marks the anomaly and the current run, and lists the anomaly', async () => {
    const el = mount(resourceHistory('d'))
    await settle()
    expect(getJobResourceHistory).toHaveBeenCalledWith({ jobId: 'd', limit: 200 })
    const points = el.querySelectorAll('.history-hit')
    expect(points).toHaveLength(6)
    expect(el.querySelectorAll('.history-point-anomaly')).toHaveLength(1)
    const current = el.querySelector<SVGGElement>('.history-hit-current')!
    expect(current.dataset.jobId).toBe('d')
    expect(current.getAttribute('aria-label')).toContain('this run')
    // One tab stop: the current run.
    expect([...points].filter((p) => p.getAttribute('tabindex') === '0')).toEqual([current])
    expect(el.querySelectorAll('.history-anomalies li')).toHaveLength(1)
    expect(el.querySelector('.history-anomalies a')?.getAttribute('href')).toBe('/app/jobs/e')
    expect(el.querySelectorAll('.history-table tbody tr')).toHaveLength(6)
    // A typical run leaves 60% of the 1 CPU request unused; the 1,900 mCPU spike lifts the p95 above it.
    expect(el.querySelector('[data-stat="unused"] dd')?.textContent).toBe('60% unused / 24% over')
  })

  it('opens a run on click and on Enter', async () => {
    const el = mount(resourceHistory('d'))
    await settle()
    el.querySelector<SVGGElement>('[data-job-id="b"]')!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(currentRoute().path).toBe('/jobs/b')

    const svg = el.querySelector('svg')!
    el.querySelector<SVGGElement>('.history-hit-current')!.focus()
    svg.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true }))
    svg.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    expect(currentRoute().path).toBe('/jobs/e')
  })

  it('shows the empty state when no run has data', async () => {
    response = { name: 'build', runs: [run('a', 0, 0, { cpuMaxMillicores: undefined })], pending: 0 }
    const el = mount(resourceHistory('a'))
    await settle()
    const empty = el.querySelector<HTMLElement>('.empty')!
    expect(empty.hidden).toBe(false)
    expect(empty.textContent).toContain('No finished runs of this job have resource data yet.')
  })

  it('asks again while older runs are summarized, and stops at zero', async () => {
    vi.useFakeTimers()
    response = { name: 'build', runs: runs().slice(0, 2), pending: 4 }
    const el = mount(resourceHistory('a')) as ResourceHistory
    const tick = async (ms: number) => {
      await vi.advanceTimersByTimeAsync(ms)
      flushRenders()
      await vi.advanceTimersByTimeAsync(0)
      flushRenders()
    }
    await tick(0)
    expect(getJobResourceHistory).toHaveBeenCalledTimes(1)
    expect(el.querySelector('.history-pending')?.textContent).toBe('Summarizing 4 older runs…')

    await tick(PENDING_RETRY_MS)
    expect(getJobResourceHistory).toHaveBeenCalledTimes(2)

    response = { ...response, runs: runs(), pending: 0 }
    await tick(PENDING_RETRY_MS)
    expect(getJobResourceHistory).toHaveBeenCalledTimes(3)
    expect(el.querySelector<HTMLElement>('.history-pending')!.hidden).toBe(true)

    await tick(PENDING_RETRY_MS * 5)
    expect(getJobResourceHistory).toHaveBeenCalledTimes(3)
  })

  it('gives up after the retry budget, and stops when it leaves the page', async () => {
    vi.useFakeTimers()
    response = { name: 'build', runs: runs(), pending: 9 }
    const el = mount(resourceHistory('a')) as ResourceHistory
    for (let i = 0; i < PENDING_MAX_TRIES + 5; i++) {
      await vi.advanceTimersByTimeAsync(PENDING_RETRY_MS)
      flushRenders()
    }
    expect(getJobResourceHistory).toHaveBeenCalledTimes(PENDING_MAX_TRIES + 1)

    getJobResourceHistory.mockClear()
    const other = mount(resourceHistory('b'))
    await vi.advanceTimersByTimeAsync(0)
    flushRenders()
    other.remove()
    el.remove()
    await vi.advanceTimersByTimeAsync(PENDING_RETRY_MS * 3)
    expect(getJobResourceHistory).toHaveBeenCalledTimes(1)
  })

  it('asks for up to 1,000 runs only when "all" is chosen', async () => {
    const el = mount(resourceHistory('a'))
    await settle()
    const box = el.querySelectorAll('combo-box')[1] as HTMLElement & { value: string }
    box.value = 'all'
    box.dispatchEvent(new Event('change', { bubbles: true }))
    await settle()
    expect(getJobResourceHistory).toHaveBeenLastCalledWith({ jobId: 'a', limit: 1000 })
  })

  it('names the per-run value a duration, not a peak, and does not repeat the duration row', async () => {
    response = { ...response, runs: [...runs(), run('g', 60, 0, { cpuMaxMillicores: undefined, sampleCount: 0 })] }
    const el = mount(resourceHistory('d'))
    await settle()
    const label = (key: string) => el.querySelector(`[data-stat="${key}"] dt`)?.textContent
    expect(label('median')).toBe('Median peak')
    expect(el.querySelector('[data-stat="runs"] dd')?.textContent).toBe('6 of 7')

    const box = el.querySelectorAll('combo-box')[0] as HTMLElement & { value: string }
    box.value = 'duration'
    box.dispatchEvent(new Event('change', { bubbles: true }))
    await settle()
    expect(label('median')).toBe('Median duration')
    expect(label('max')).toBe('Longest run')
    expect(el.querySelector<HTMLElement>('[data-stat="duration"]')!.hidden).toBe(true)
    expect(el.querySelector('.history-legend')?.textContent).toBe('Duration of each run')
  })
})
