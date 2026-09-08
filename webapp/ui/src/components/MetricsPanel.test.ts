import { describe, expect, it } from 'vitest'
import { describeUnavailable, humanReason } from './MetricsPanel.tsx'

/**
 * The unavailable lines say what the coordinator decided, in plain words.
 * The old text called every metrics-server hiccup "the cluster metrics API
 * is not installed", on clusters where it plainly was.
 */
describe('describeUnavailable', () => {
  it('names the family by its friendly name and the reason plainly', () => {
    expect(describeUnavailable({ metricPrefix: 'cpu.utilization', reason: 'permission_denied' })).toBe(
      'CPU used: not collected (the worker is not permitted to read it)',
    )
    expect(describeUnavailable({ metricPrefix: 'memory.usage', reason: 'metric_api_not_installed' })).toBe(
      'Memory used: not collected (the cluster metrics API is not installed)',
    )
  })

  it('describes a temporary gap as no sample before the job ended', () => {
    expect(describeUnavailable({ metricPrefix: 'storage.used', reason: 'temporarily_unavailable' })).toBe(
      'Storage used: no sample was available before the job ended',
    )
  })

  it('keeps buffer gaps visible and distinct from collection failures', () => {
    expect(describeUnavailable({ metricPrefix: 'telemetry.buffer', reason: 'buffer_gap' })).toBe(
      'Telemetry: some samples were lost to a gap in the telemetry buffer',
    )
  })

  it('falls back to the raw prefix and reason for unknown values', () => {
    expect(describeUnavailable({ metricPrefix: 'gpu.usage', reason: 'something_new' })).toBe(
      'gpu.usage: not collected (something_new)',
    )
    expect(humanReason('something_new')).toBe('something_new')
  })
})
