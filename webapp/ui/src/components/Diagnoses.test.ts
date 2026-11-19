import { afterEach, describe, expect, it, vi } from 'vitest'
import { waitFor } from '@testing-library/dom'
import type { DiagnosisSummary } from '~/api/csilapi/types.gen.ts'

let diagnoses: DiagnosisSummary[] = []

vi.mock('~/api/client.ts', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/client.ts')>()
  return {
    ...actual,
    api: {
      listDiagnoses: vi.fn(async () => ({ diagnoses })),
    },
  }
})

import { api } from '~/api/client.ts'
import { mount, settle, unmount } from '~/test/mount.ts'
import { authorLabel, diagnosisCard } from './Diagnoses.ts'

let mounted: Element | null = null

afterEach(() => {
  if (mounted) unmount(mounted)
  mounted = null
  vi.clearAllMocks()
})

function entry(over: Partial<DiagnosisSummary>): DiagnosisSummary {
  return {
    diagnosisId: 'd-1',
    targetType: 'job',
    jobId: 'job-1',
    author: { principalId: 'p-1', kind: 'service', displayName: 'Diag Bot' },
    title: 'Out of memory',
    body: '<img src=x onerror="window.pwned=1"> The test used too much memory.',
    confidence: 'high',
    category: 'oom',
    links: [
      { name: 'Dashboard', url: 'https://dash.example/1' },
      { name: 'Bad', url: 'javascript:alert(1)' },
    ],
    suggestedShiftsText: 'memory=8Gi',
    revision: 2,
    createdAt: '2026-11-16T00:00:00Z',
    updatedAt: '2026-11-16T00:00:00Z',
    ...over,
  }
}

describe('diagnosis card', () => {
  it('shows each entry with its author and kind, and the body as text', async () => {
    diagnoses = [entry({})]
    const el = diagnosisCard()
    el.setTarget({ jobId: 'job-1' }, 'failed')
    mounted = mount(el)
    await settle()
    await waitFor(() => expect(el.hidden).toBe(false))
    expect(api.listDiagnoses).toHaveBeenCalledWith({ jobId: 'job-1' })
    expect(el.textContent).toContain('Out of memory')
    expect(el.textContent).toContain('by Diag Bot (service)')
    expect(el.querySelector('.diagnosis-kind')?.textContent).toBe('service')
    expect(el.textContent).toContain('Suggested re-run shifts: memory=8Gi')
    expect(el.textContent).toContain('confidence high')
    // The body is text: the markup is not an element.
    expect(el.querySelector('img')).toBeNull()
    expect(el.querySelector('.diagnosis-body')?.textContent).toContain('<img src=x')
    const anchors = [...el.querySelectorAll('.diagnosis-links a')]
    expect(anchors).toHaveLength(1)
    expect(anchors[0].getAttribute('href')).toBe('https://dash.example/1')
    expect(anchors[0].getAttribute('rel')).toContain('noopener')
  })

  it('lists the entries of a workflow with the node name, and loads again on a new status', async () => {
    diagnoses = [entry({ nodeName: 'build' })]
    const el = diagnosisCard()
    el.setTarget({ workflowId: 'wf-1' }, 'running')
    mounted = mount(el)
    await settle()
    await waitFor(() => expect(el.hidden).toBe(false))
    expect(api.listDiagnoses).toHaveBeenCalledWith({ workflowId: 'wf-1' })
    expect(el.querySelector('.diagnosis-title')?.textContent).toBe('build: Out of memory')
    const row = el.querySelector('.diagnosis')

    diagnoses = [entry({ nodeName: 'build', title: 'A real bug', revision: 3 })]
    el.setTarget({ workflowId: 'wf-1' }, 'failed')
    await settle()
    await waitFor(() => expect(el.querySelector('.diagnosis-title')?.textContent).toBe('build: A real bug'))
    expect(api.listDiagnoses).toHaveBeenCalledTimes(2)
    expect(el.querySelector('.diagnosis')).toBe(row)
  })

  it('stays hidden with no entries', async () => {
    diagnoses = []
    const el = diagnosisCard()
    el.setTarget({ jobId: 'job-2' }, 'completed')
    mounted = mount(el)
    await settle()
    await waitFor(() => expect(api.listDiagnoses).toHaveBeenCalled())
    await settle()
    expect(el.hidden).toBe(true)
    expect(authorLabel(entry({ author: { principalId: 'p-9', kind: '', displayName: '' } }))).toBe('p-9 (unknown)')
  })
})
