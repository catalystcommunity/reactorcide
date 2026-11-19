import { describe, expect, it, vi } from 'vitest'
import type { WorkerSummary } from '~/api/csilapi/types.gen.ts'

vi.mock('~/api/client.ts', () => ({
  api: {},
  ServiceError: class extends Error {},
  TransportFailure: class extends Error {},
}))

import { requested } from './Queues.ts'
import { workerHeadroom, workerLocation } from './Workers.ts'

const base: WorkerSummary = {
  workerId: 'w1',
  poolId: 'p1',
  workerKey: 'k1',
  os: 'linux',
  arch: 'amd64',
  characteristics: [],
  status: 'active',
  activeLeaseCount: 0,
}

describe('worker capacity columns', () => {
  it('shows the standard location characteristics', () => {
    expect(workerLocation(base)).toBe('—')
    expect(
      workerLocation({
        ...base,
        characteristics: [
          { key: 'zone', value: 'us-east-1a' },
          { key: 'region', value: 'us-east-1' },
          { key: 'cloud', value: 'aws' },
          { key: 'gpu', value: true },
        ],
      }),
    ).toBe('aws / us-east-1 / us-east-1a')
  })

  it('shows headroom only for resources admission', () => {
    expect(workerHeadroom(base)).toBe('fixed')
    const text = workerHeadroom({
      ...base,
      admission: {
        mode: 'resources',
        capacityCpuMillicores: 8000,
        capacityMemoryBytes: 32 * 1024 ** 3,
        headroomCpuMillicores: -500,
        headroomMemoryBytes: 4 * 1024 ** 3,
        reportedAt: '2026-11-16T12:00:00Z',
      },
    })
    expect(text).toContain('of')
    expect(text).not.toContain('-')
  })

  it('shows the requested resources of a backlog', () => {
    expect(requested(undefined, undefined)).toBe('—')
    expect(requested(0, 0)).toBe('—')
    expect(requested(2500, 3 * 1024 ** 3)).toContain('/')
  })
})
