import type { CharacteristicEntry, ImageAliasRollout, ImageAliasSummary, ImageFailureSummary, ImageRolloutPoolSummary, JobSummary } from '~/api/csilapi/types.gen.ts'

/**
 * Text helpers for image aliases and worker image sets
 * (docs/image-aliases.md). They import no API client, so a test can use them
 * without a mock.
 */

/** The image of a job (VM_IMAGES_PLAN I2): `alias (ref)` when the job used an alias, else the ref. */
export function jobImageText(job: Pick<JobSummary, 'runnerImage' | 'imageAlias' | 'imageDisplay'>): string {
  if (job.imageDisplay) return job.imageDisplay
  if (job.imageAlias && job.runnerImage) return `${job.imageAlias} (${job.runnerImage})`
  return job.imageAlias || job.runnerImage || ''
}

/** The status class of a worker image status. */
export function imageStatusClass(status: string | undefined): string {
  switch (status) {
    case 'healthy':
      return 'status status-ok'
    case 'updating images':
      return 'status status-info'
    case 'image update failed':
      return 'status status-danger'
    default:
      return 'status status-neutral'
  }
}

/** The image status of a worker, or the text for a worker that does not use image sets. */
export function imageStatusText(status: string | undefined): string {
  return status || 'no image set'
}

/** Wanted against reported image set version: "v4 (reported v3)". */
export function imageVersionText(wanted: number | undefined, reported: number | undefined): string {
  if (wanted === undefined && reported === undefined) return '—'
  const want = wanted === undefined ? '—' : `v${wanted}`
  const got = reported === undefined ? 'no report' : `reported v${reported}`
  return `${want} (${got})`
}

/** Characteristics as `key=value, key=value`. */
export function characteristicsText(entries: readonly CharacteristicEntry[] | undefined): string {
  return (entries ?? []).map((c) => `${c.key}=${String(c.value)}`).join(', ')
}

/** Keys the coordinator refuses as alias characteristics: os and arch are alias fields. */
export const RESERVED_CHARACTERISTIC_KEYS = ['os', 'arch', 'image_digest']

/**
 * Parses `key=value, key=value`. Values are text, the same as the CLI
 * (`--characteristic key=value`). Returns an error text for a bad entry.
 */
export function parseCharacteristics(text: string): { entries: CharacteristicEntry[] } | { error: string } {
  const entries: CharacteristicEntry[] = []
  const seen = new Set<string>()
  for (const part of text.split(',')) {
    const item = part.trim()
    if (!item) continue
    const cut = item.indexOf('=')
    if (cut <= 0) return { error: `"${item}" is not key=value.` }
    const key = item.slice(0, cut).trim()
    const value = item.slice(cut + 1).trim()
    if (!key) return { error: `"${item}" has no key.` }
    if (!value) return { error: `Characteristic ${key} has no value.` }
    if (RESERVED_CHARACTERISTIC_KEYS.includes(key)) return { error: `Characteristic ${key} is reserved. Use the os and arch fields.` }
    if (seen.has(key)) return { error: `Characteristic ${key} is given two times.` }
    seen.add(key)
    entries.push({ key, value })
  }
  return { entries }
}

/** The alias name rule of the coordinator (imageref.ValidateAliasName). */
export function aliasNameError(name: string): string | undefined {
  if (!name) return 'Give the alias a name.'
  if (name.length > 63) return 'An alias name has 63 characters or fewer.'
  if (!/^[a-z0-9][a-z0-9._-]*$/.test(name)) return 'Use a-z, 0-9, ".", "_" and "-". The first character is a letter or a digit.'
  return undefined
}

/** A short check of a pinned reference. The coordinator does the full check. */
export function aliasRefError(ref: string): string | undefined {
  if (!ref) return 'Give a pinned reference.'
  if (!/@sha256:[0-9a-f]{64}$/.test(ref)) return 'The reference must end with @sha256: and a 64-digit hex digest.'
  return undefined
}

/** The text of a set-image-alias outcome. */
export function outcomeText(name: string, outcome: string): string {
  switch (outcome) {
    case 'created':
      return `Alias ${name} created.`
    case 'pending':
      return `The new pin of ${name} is pending. New jobs use it when each online serving worker has the image.`
    case 'promoted':
      return `Alias ${name} uses the new pin now.`
    case 'updated':
      return `Alias ${name} updated.`
    case 'unchanged':
      return `Nothing changed for ${name}.`
    default:
      return `Alias ${name}: ${outcome}.`
  }
}

/** A short digest: `sha256:0123456789ab`. */
export function shortDigest(digest: string | undefined): string {
  if (!digest) return ''
  const [algo, hex] = digest.includes(':') ? digest.split(':', 2) : ['sha256', digest]
  return `${algo}:${hex.slice(0, 12)}`
}

/** The rollout progress of a pending pin, or '' when the alias has no pending pin. */
export function rolloutText(alias: ImageAliasSummary, rollout: ImageAliasRollout | undefined): string {
  if (!alias.pendingRef) return ''
  if (!rollout) return 'Rollout state not loaded.'
  const progress = `${rollout.readyWorkers} of ${rollout.onlineWorkers} online ${rollout.onlineWorkers === 1 ? 'worker' : 'workers'} ready`
  if (rollout.promotable) return `${progress}. Promotes at the next check.`
  return rollout.blocked ? `${progress}. ${sentence(rollout.blocked)}` : `${progress}.`
}

/** A coordinator reason with an end mark. The first word can be a host name, so the case stays. */
export function sentence(text: string): string {
  const t = text.trim()
  if (!t) return t
  return /[.!?]$/.test(t) ? t : `${t}.`
}

/** The class of an image pull failure: a permanent one needs a fix, a transient one is tried again. */
export function imageFailureClass(failure: Pick<ImageFailureSummary, 'permanent'>): string {
  return failure.permanent ? 'failed (permanent)' : 'failed (will retry)'
}

/** An image pull failure: "failed (permanent): <detail>". */
export function imageFailureText(failure: Pick<ImageFailureSummary, 'permanent' | 'detail'>): string {
  const detail = failure.detail.trim()
  return detail ? `${imageFailureClass(failure)}: ${detail}` : imageFailureClass(failure)
}

/** The counts of the serving workers of a pool that the caller cannot manage. */
export function rolloutPoolText(pool: ImageRolloutPoolSummary): string {
  const noun = pool.workers === 1 ? 'worker' : 'workers'
  return `${pool.poolName}: ${pool.workers} ${noun}, ${pool.onlineWorkers} online, ${pool.readyWorkers} ready, ${pool.failedWorkers} failed`
}
