import { h, setAttr, setText } from '~/ui/dom.ts'

/**
 * A job or workflow status.
 *
 * Colour is never the only signal. Each state carries a glyph too, so the badge
 * survives a monochrome print, a colour-vision deficiency, and the several
 * status colours that are otherwise easy to confuse at a glance in a long list.
 */

type Tone = 'ok' | 'danger' | 'warn' | 'info' | 'neutral'

interface Presentation {
  tone: Tone
  glyph: string
  /** Spoken by assistive technology in place of the glyph. */
  label: string
}

const PRESENTATION: Record<string, Presentation> = {
  // Jobs
  completed: { tone: 'ok', glyph: '✓', label: 'completed' },
  failed: { tone: 'danger', glyph: '✕', label: 'failed' },
  timeout: { tone: 'danger', glyph: '⧗', label: 'timed out' },
  scheduling: { tone: 'warn', glyph: '◷', label: 'scheduling' },
  running: { tone: 'info', glyph: '●', label: 'running' },
  queued: { tone: 'warn', glyph: '◔', label: 'queued' },
  submitted: { tone: 'warn', glyph: '◔', label: 'submitted' },
  cancelling: { tone: 'warn', glyph: '◑', label: 'cancelling' },
  cancelled: { tone: 'neutral', glyph: '⊘', label: 'cancelled' },

  // Workflows
  evaluating: { tone: 'info', glyph: '◍', label: 'evaluating' },
  success: { tone: 'ok', glyph: '✓', label: 'succeeded' },
  skipped: { tone: 'neutral', glyph: '⊝', label: 'skipped' },

  // Events
  received: { tone: 'warn', glyph: '◔', label: 'received' },
  evaluated: { tone: 'ok', glyph: '✓', label: 'evaluated' },
  duplicate: { tone: 'neutral', glyph: '⧉', label: 'duplicate' },

  // Nodes
  pending: { tone: 'neutral', glyph: '○', label: 'pending' },
  waiting: { tone: 'neutral', glyph: '○', label: 'waiting' },
}

const UNKNOWN: Presentation = { tone: 'neutral', glyph: '?', label: 'unknown state' }

export function presentationFor(status: string): Presentation {
  return PRESENTATION[status] ?? UNKNOWN
}

/** True once a status stops moving, so callers can stop polling. */
export function isTerminal(status: string): boolean {
  return (
    status === 'completed' ||
    status === 'failed' ||
    status === 'cancelled' ||
    status === 'timeout' ||
    status === 'success' ||
    status === 'skipped'
  )
}

/** A status badge. Change it with `setStatus`; do not make a new one per refresh. */
export function statusBadge(status = '', extraClass = ''): HTMLSpanElement {
  // The glyph is decorative; the text beside it already says the state.
  // Without aria-hidden, a screen reader announces a meaningless symbol first.
  const el = h('span', { class: 'status' }, h('span', { class: 'status-glyph', 'aria-hidden': 'true' }), h('span', { class: 'status-text' }))
  if (extraClass) el.dataset.extraClass = extraClass
  setStatus(el, status)
  return el
}

export function setStatus(el: HTMLElement, status: string): void {
  const p = presentationFor(status)
  const extra = el.dataset.extraClass ?? ''
  setAttr(el, 'class', `status status-${p.tone}${status === 'running' ? ' status-running' : ''}${extra ? ` ${extra}` : ''}`)
  setAttr(el, 'title', p.label)
  setText(el.querySelector('.status-glyph')!, p.glyph)
  setText(el.querySelector('.status-text')!, status)
}

/**
 * Public or private, stated plainly.
 *
 * Visibility decides who can see a project's jobs and logs, so it gets a badge
 * of its own rather than being inferred from a checkbox buried in a settings
 * form.
 */
export function visibilityBadge(isPrivate: boolean): HTMLSpanElement {
  const el = h('span', { class: 'status' }, h('span', { class: 'status-glyph', 'aria-hidden': 'true' }), h('span', { class: 'status-text' }))
  setVisibility(el, isPrivate)
  return el
}

export function setVisibility(el: HTMLElement, isPrivate: boolean): void {
  setAttr(el, 'class', `status ${isPrivate ? 'status-ok' : 'status-warn'}`)
  setText(el.querySelector('.status-glyph')!, isPrivate ? '⚿' : '◍')
  setText(el.querySelector('.status-text')!, isPrivate ? 'Private' : 'Public')
}
