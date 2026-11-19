import { afterEach, describe, expect, it, vi } from 'vitest'
import { waitFor } from '@testing-library/dom'
import type { PushButton, TextField } from '@catalystcommunity/controls'
import type {
  GetJobProvenanceResponse,
  GetJobTestResultsResponse,
  JobArtifactSummary,
  ListFlakyTestsResponse,
  ListTestQuarantinesResponse,
} from '~/api/csilapi/types.gen.ts'

let tests: GetJobTestResultsResponse = { results: [], canQuarantine: false }
let artifacts: JobArtifactSummary[] = []
let provenance: GetJobProvenanceResponse = { present: false, verified: false }
let flaky: ListFlakyTestsResponse = { tests: [], windowDays: 30 }
let quarantines: ListTestQuarantinesResponse = { quarantines: [], canManage: false }
const chunks: Uint8Array[] = []

vi.mock('~/api/client.ts', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/client.ts')>()
  return {
    ...actual,
    api: {
      getJobTestResults: vi.fn(async () => structuredClone(tests)),
      quarantineTest: vi.fn(async () => ({ quarantine: {} })),
      listJobArtifacts: vi.fn(async () => ({ artifacts })),
      getJobProvenance: vi.fn(async () => provenance),
      readJobArtifact: vi.fn(async (req: { offset: number }) => {
        const index = chunks.findIndex((_, i) => chunks.slice(0, i).reduce((n, c) => n + c.length, 0) === req.offset)
        const data = chunks[index] ?? new Uint8Array()
        return { data, sizeBytes: 6, sha256: 'x', eof: index === chunks.length - 1 }
      }),
      listFlakyTests: vi.fn(async () => flaky),
      listTestQuarantines: vi.fn(async () => quarantines),
      unquarantineTest: vi.fn(async () => ({ done: true })),
    },
  }
})

import { api } from '~/api/client.ts'
import { mount, settle, unmount } from '~/test/mount.ts'
import { firstLines, jobTestsCard, summaryText, testName } from './JobTests.ts'
import { artifactFileName, formatBytes, jobArtifactsCard, readArtifact } from './JobArtifacts.ts'
import { projectTestsCard } from './ProjectTests.ts'

let mounted: Element | null = null

afterEach(() => {
  if (mounted) unmount(mounted)
  mounted = null
  vi.clearAllMocks()
})

function type(el: TextField, value: string): void {
  el.value = value
  el.dispatchEvent(new Event('input', { bubbles: true }))
}

const summary = {
  reports: 1, total: 10, passed: 7, failed: 2, errored: 0, skipped: 1, quarantinedFailures: 1, flaky: 1,
  truncated: false, parseErrors: [], quarantinePolicy: '', quarantineOverride: false,
}

describe('test helpers', () => {
  it('formats names, lines, and counts', () => {
    expect(testName({ suite: 's', classname: 'pkg.A', name: 'b' })).toBe('pkg.A › b')
    expect(testName({ suite: 's', classname: '', name: '' })).toBe('s')
    expect(firstLines('\n a\n\nb\nc\nd')).toBe(' a\nb\nc')
    expect(summaryText(summary)).toBe('10 tests: 7 passed, 2 failed (1 quarantined), 1 skipped, 1 flaky')
    expect(formatBytes(512)).toBe('512 B')
    expect(formatBytes(5 * 1024 * 1024)).toBe('5.0 MiB')
    expect(artifactFileName('dist/app.tar.gz')).toBe('app.tar.gz')
  })
})

describe('job tests card', () => {
  it('shows failing tests with flags and quarantines one', async () => {
    tests = {
      summary,
      projectId: 'p-1',
      canQuarantine: true,
      results: [
        { testId: 't1', suite: 'unit', classname: 'pkg.A', name: 'breaks', status: 'fail', durationMs: 5, message: 'expected 1\ngot 2\n<b>x</b>\nline 4', file: 'a_test.go', line: 7, quarantined: false, flaky: true },
        { testId: 't2', suite: 'unit', classname: 'pkg.A', name: 'parked', status: 'fail', durationMs: 5, message: '', file: '', quarantined: true, flaky: false },
      ],
    }
    const el = jobTestsCard('job-1')
    mounted = mount(el)
    await settle()
    await waitFor(() => expect(el.hidden).toBe(false))
    expect(api.getJobTestResults).toHaveBeenCalledWith({ jobId: 'job-1', failedOnly: true, limit: 200 })
    expect(el.textContent).toContain('10 tests: 7 passed, 2 failed (1 quarantined)')
    expect(el.textContent).toContain('(flaky)')
    expect(el.textContent).toContain('(quarantined)')
    expect(el.textContent).toContain('a_test.go:7')
    expect(el.querySelector('b')).toBeNull()
    expect(el.textContent).not.toContain('line 4')
    const rows = [...el.querySelectorAll('.test-row')]
    const buttons = rows.map((row) => row.querySelector('push-button') as PushButton)
    expect(buttons[0].hidden).toBe(false)
    expect(buttons[1].hidden).toBe(true)
    buttons[0].click()
    await settle()
    const form = el.querySelector('form') as HTMLFormElement
    expect(form.hidden).toBe(false)
    const [reason] = [...form.querySelectorAll('text-field')] as TextField[]
    type(reason, 'arm runners')
    await settle()
    form.dispatchEvent(new Event('submit', { cancelable: true }))
    await waitFor(() => expect(api.quarantineTest).toHaveBeenCalledWith({ projectId: 'p-1', testId: 't1', reason: 'arm runners', until: '7d' }))
  })

  it('stays hidden without a report and shows no button without permission', async () => {
    tests = { results: [], canQuarantine: false }
    const el = jobTestsCard('job-2')
    mounted = mount(el)
    await settle()
    await waitFor(() => expect(api.getJobTestResults).toHaveBeenCalled())
    await settle()
    expect(el.hidden).toBe(true)
  })
})

describe('job artifacts card', () => {
  it('lists artifacts and shows the provenance check', async () => {
    artifacts = [
      { artifactId: 'a1', path: 'dist/app.bin', kind: 'file', mediaType: 'application/octet-stream', sizeBytes: 2048, sha256: 'ab'.repeat(32), status: 'complete', createdAt: '' },
      { artifactId: 'a2', path: 'big.bin', kind: 'file', mediaType: 'application/octet-stream', sizeBytes: 1, sha256: '', status: 'failed', statusReason: 'too large', createdAt: '' },
      { artifactId: 'p', path: 'provenance.intoto.dsse.json', kind: 'provenance', mediaType: 'x', sizeBytes: 1, sha256: 'cd'.repeat(32), status: 'complete', createdAt: '' },
    ]
    provenance = { present: true, verified: true, keyId: 'kid-1', statementJson: '{"_type": "https://in-toto.io/Statement/v1"}' }
    const el = jobArtifactsCard('job-3')
    mounted = mount(el)
    await settle()
    await waitFor(() => expect(el.hidden).toBe(false))
    expect(el.textContent).toContain('dist/app.bin')
    expect(el.textContent).toContain('2.0 KiB')
    expect(el.textContent).toContain('failed: too large')
    expect(el.textContent).not.toContain('provenance.intoto.dsse.json')
    expect(el.textContent).toContain('signature verified (key kid-1)')
    const downloads = [...el.querySelectorAll('tbody push-button')] as PushButton[]
    expect(downloads[1].disabled).toBe(true)
  })

  it('reads an artifact in parts', async () => {
    chunks.splice(0, chunks.length, new Uint8Array([1, 2, 3]), new Uint8Array([4, 5, 6]))
    const parts = await readArtifact('job-3', { artifactId: 'a1', path: 'x', kind: 'file', mediaType: '', sizeBytes: 6, sha256: '', status: 'complete', createdAt: '' })
    expect(parts.map((p) => [...p])).toEqual([[1, 2, 3], [4, 5, 6]])
    expect(api.readJobArtifact).toHaveBeenCalledTimes(2)
  })
})

describe('project tests card', () => {
  it('lists flaky tests and quarantines, and ends one', async () => {
    flaky = { windowDays: 14, tests: [{ testId: 'f1', suite: 'unit', classname: 'pkg', name: 'flaky', flakyCount: 3, totalRuns: 20 }] }
    quarantines = {
      canManage: true,
      quarantines: [{ quarantineId: 'q1', testId: 't1', suite: 'unit', classname: 'pkg', name: 'parked', ownerPrincipalId: 'owner-1', reason: 'arm', expiresAt: '2026-12-01T00:00:00Z', createdAt: '' }],
    }
    const el = projectTestsCard('p-1')
    mounted = mount(el)
    await settle()
    await waitFor(() => expect(el.hidden).toBe(false))
    expect(el.textContent).toContain('Flaky tests (last 14 days)')
    expect(el.textContent).toContain('pkg › flaky')
    expect(el.textContent).toContain('pkg › parked')
    expect(el.textContent).toContain('owner-1')
  })
})
