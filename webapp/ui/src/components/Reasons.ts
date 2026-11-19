import type { CIDecisionReason } from '~/api/csilapi/types.gen.ts'
import { priorityText } from '~/components/priorityText.ts'
import { fill, h } from '~/ui/dom.ts'
import { href } from '~/ui/router.ts'
import './reasons.css'

/**
 * Why a workflow or a job runs, or does not run.
 *
 * The eval and the coordinator record structured reasons: one per check, in
 * the order the checks ran (event, branch, paths, schedule, policy,
 * dependency, admission). This renders them as one sentence each. The same
 * sentence is used everywhere a reason shows: the event page, the workflow
 * "why" section and the node table.
 *
 * A kind this file does not know renders as "kind: value", so a newer
 * coordinator never breaks an older page.
 */

/** How many matched files show before "+N more". The eval sends at most 50. */
export const FILES_SHOWN = 10

function list(values: readonly string[] | undefined): string {
  return values && values.length ? values.join(', ') : 'none'
}

function quoted(value: string | undefined): string {
  return value ? `"${value}"` : 'unknown'
}

/** One reason as one plain sentence. */
export function reasonSentence(r: CIDecisionReason): string {
  const detail = r.detail ? ` (${r.detail})` : ''
  switch (r.kind) {
    case 'event':
      if (r.matched === false) {
        return r.patterns?.length
          ? `Event ${quoted(r.value)} is not one of: ${list(r.patterns)}${detail}`
          : `Event ${quoted(r.value)} does not match${detail}`
      }
      return r.patterns?.length ? `Event ${quoted(r.value)} matches: ${list(r.patterns)}${detail}` : `Event ${quoted(r.value)}${detail}`
    case 'branch':
      if (!r.value) return `Branch filter ${list(r.patterns)}${detail}`
      return r.matched === false
        ? `Branch ${quoted(r.value)} does not match: ${list(r.patterns)}${detail}`
        : `Branch ${quoted(r.value)} matches: ${list(r.patterns)}${detail}`
    case 'paths': {
      const filter = `include ${list(r.pathsInclude)}${r.pathsExclude?.length ? `, exclude ${list(r.pathsExclude)}` : ''}`
      if (r.detail) return `Paths ${filter} (${r.detail})`
      if (r.matched === false) return `No changed file matches paths ${filter}`
      const count = r.matchedCount ?? r.matchedFiles?.length ?? 0
      return `${count} changed file${count === 1 ? '' : 's'} match paths ${filter}`
    }
    case 'schedule':
      if (r.required !== undefined) {
        // A job-level `if: {schedule: ...}`.
        if (r.required) {
          return r.matched === false ? `Runs only on schedule events; this event is ${quoted(r.value)}` : 'Runs only on schedule events'
        }
        return r.matched === false ? 'Does not run on schedule events' : `Not a schedule event, so it runs`
      }
      return `${r.detail ? capitalize(r.detail) : r.fireTime ? `Schedule fired at ${r.fireTime}` : 'Schedule fired'}${r.value ? `, cron ${r.value}` : ''}`
    case 'policy': {
      const parts = [r.ciOrigin && `CI code from ${r.ciOrigin}`, r.profile && `profile ${r.profile}`, r.workerClass && `worker class ${r.workerClass}`]
      const rule = r.ruleId ? `rule ${r.ruleId}` : 'default policy'
      return `Policy (${rule}${r.policyRevision ? `, revision ${r.policyRevision}` : ''}): ${parts.filter(Boolean).join(', ') || 'no change'}${detail}`
    }
    case 'manual':
      return `Started manually${r.value ? ` by ${r.value}` : ''}${detail}`
    case 'dependency': {
      const deps = (r.deps ?? []).map((d) => `${d.name} ${d.status}`).join(', ')
      const condition = r.condition || 'all_success'
      if (!deps) return `Condition ${condition}: no dependencies`
      return r.matched === false ? `Condition ${condition} is not met: ${deps}` : `Condition ${condition}: ${deps}`
    }
    case 'admission':
      return `Not admitted${r.value ? ` (${r.value})` : ''}${r.detail ? `: ${r.detail}` : ''}`
    case 'invalid':
      return `Invalid workflow${r.detail ? `: ${r.detail}` : ''}`
    case 'priority':
      return priorityText(r)
    case 'ref_class': {
      const rules = r.ruleIds?.length ? ` (from rules ${r.ruleIds.join(', ')})` : ''
      return `Ref classes: ${list(r.classes)}${rules}`
    }
    default:
      return `${r.kind || 'reason'}${r.value ? `: ${r.value}` : ''}${detail}`
  }
}

function capitalize(text: string): string {
  return text.charAt(0).toUpperCase() + text.slice(1)
}

/** The reason a skip or a refusal comes from: the last reason that did not match, else the last one. */
export function reasonSummary(reasons: readonly CIDecisionReason[] | undefined): string {
  if (!reasons?.length) return ''
  const miss = [...reasons].reverse().find((r) => r.matched === false || r.kind === 'admission' || r.kind === 'invalid')
  // The priority reason tells how soon a job runs, not why it runs.
  const why = reasons.filter((r) => r.kind !== 'priority')
  return reasonSentence(miss ?? why[why.length - 1] ?? reasons[reasons.length - 1])
}

function glyph(r: CIDecisionReason): { text: string; label: string; tone: string } {
  if (r.kind === 'invalid' || r.kind === 'admission' || r.matched === false) return { text: '✕', label: 'Did not match', tone: 'miss' }
  if (r.matched === true) return { text: '✓', label: 'Matched', tone: 'ok' }
  return { text: '•', label: 'Note', tone: 'note' }
}

export interface ReasonListOptions {
  /** Where a schedule reason links: the project that owns the schedule. */
  projectId?: string
}

/**
 * A list of reasons. Static content, so `update` replaces the list, but only
 * when the reasons change: a live refresh with equal reasons touches nothing.
 */
export class ReasonList {
  readonly el: HTMLUListElement
  #signature = ''
  readonly #options: ReasonListOptions

  constructor(options: ReasonListOptions = {}) {
    this.#options = options
    this.el = h('ul', { class: 'reason-list' })
  }

  update(reasons: readonly CIDecisionReason[] | undefined, options?: ReasonListOptions): void {
    const opts = { ...this.#options, ...options }
    const signature = JSON.stringify([reasons ?? [], opts.projectId ?? ''])
    if (signature === this.#signature) return
    this.#signature = signature
    fill(this.el, (reasons ?? []).map((r) => reasonItem(r, opts)))
  }
}

function reasonItem(r: CIDecisionReason, options: ReasonListOptions): HTMLLIElement {
  const g = glyph(r)
  const item = h(
    'li',
    { class: `reason reason-${g.tone}` },
    h('span', { class: 'reason-glyph', 'aria-hidden': 'true' }, g.text),
    h('span', { class: 'sr-only' }, `${g.label}: `),
    h('span', { class: 'reason-text' }, reasonSentence(r)),
  )
  if (r.kind === 'paths' && r.matchedFiles?.length) {
    const shown = r.matchedFiles.slice(0, FILES_SHOWN)
    const more = (r.matchedCount ?? r.matchedFiles.length) - shown.length
    item.append(
      h(
        'ul',
        { class: 'reason-files' },
        shown.map((file) => h('li', { class: 'mono' }, file)),
        more > 0 ? h('li', { class: 'meta reason-more' }, `+${more} more`) : null,
      ),
    )
  }
  if (r.kind === 'policy' && r.reasons?.length) {
    item.append(h('ul', { class: 'reason-sub' }, r.reasons.map((text) => h('li', null, text))))
  }
  if (r.kind === 'schedule' && r.scheduleId && options.projectId) {
    item.append(' ', h('a', { href: href(`/projects/${options.projectId}`), class: 'reason-link' }, 'View schedules'))
  }
  return item
}

type Decision = 'run' | 'skipped' | 'invalid' | 'not_admitted'

const DECISIONS: Record<Decision, { tone: string; glyph: string; label: string }> = {
  run: { tone: 'ok', glyph: '▶', label: 'Run' },
  skipped: { tone: 'neutral', glyph: '⊝', label: 'Skipped' },
  invalid: { tone: 'danger', glyph: '✕', label: 'Invalid' },
  not_admitted: { tone: 'warn', glyph: '⊘', label: 'Not admitted' },
}

/** A badge for a workflow decision. Colour is never the only signal: each has a glyph and a word. */
export function decisionBadge(): HTMLSpanElement {
  return h('span', { class: 'status decision' }, h('span', { class: 'status-glyph', 'aria-hidden': 'true' }), h('span', { class: 'status-text' }))
}

export function setDecision(el: HTMLElement, decision: string): void {
  const p = DECISIONS[decision as Decision] ?? { tone: 'neutral', glyph: '?', label: decision || 'Unknown' }
  const cls = `status decision status-${p.tone}`
  if (el.className !== cls) el.className = cls
  const glyphEl = el.querySelector('.status-glyph')!
  if (glyphEl.textContent !== p.glyph) glyphEl.textContent = p.glyph
  const text = el.querySelector('.status-text')!
  if (text.textContent !== p.label) text.textContent = p.label
}
