import type { CIDecisionReason, PriorityLaneMatch, PriorityShift } from '~/api/csilapi/types.gen.ts'

/** Texts of priority decisions (docs/priority.md). No API client here, so any view can import it. */

/** A number with its sign: "+20", "-100", "0". */
export function signed(value: number): string {
  return value > 0 ? `+${value}` : String(value)
}

/** One shift as text: "group release +20". */
export function shiftText(shift: PriorityShift): string {
  const label = shift.group ? `${shift.source} ${shift.group}` : shift.source
  return `${label} ${signed(shift.value)}${shift.applied ? '' : ' (not used)'}`
}

/** A priority reason as one sentence: "Priority -90: lane pr-ready (builtin), base -100; group dev +10". */
export function priorityText(reason: CIDecisionReason | undefined, priority?: number): string {
  if (!reason || reason.kind !== 'priority') return priority === undefined ? '' : `Priority ${priority}`
  const scope = reason.value ? ` (${reason.value})` : ''
  const shifts = (reason.shifts ?? []).filter((s) => s.applied).map(shiftText)
  const final = reason.final ?? priority ?? 0
  return `Priority ${final}: lane ${reason.lane ?? 'unknown'}${scope}, base ${reason.base ?? 0}${shifts.length ? `; ${shifts.join(', ')}` : ''}`
}

/** The match facts of a lane as one short text. */
export function laneMatchText(match: PriorityLaneMatch): string {
  const parts: string[] = []
  if (match.jobKind) parts.push(`${match.jobKind} jobs`)
  if (match.classesAny?.length) parts.push(`classes any of ${match.classesAny.join(', ')}`)
  if (match.classesNone?.length) parts.push(`classes none of ${match.classesNone.join(', ')}`)
  if (match.eventTypes?.length) parts.push(`events ${match.eventTypes.join(', ')}`)
  if (match.draft !== undefined) parts.push(match.draft ? 'draft' : 'not a draft')
  if (match.retry) parts.push('retries only')
  return parts.length ? parts.join('; ') : 'every job'
}

