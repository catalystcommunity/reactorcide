import { type JSX } from 'solid-js'

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
  running: { tone: 'info', glyph: '●', label: 'running' },
  queued: { tone: 'warn', glyph: '◔', label: 'queued' },
  submitted: { tone: 'warn', glyph: '◔', label: 'submitted' },
  cancelling: { tone: 'warn', glyph: '◑', label: 'cancelling' },
  cancelled: { tone: 'neutral', glyph: '⊘', label: 'cancelled' },

  // Workflows
  evaluating: { tone: 'info', glyph: '◍', label: 'evaluating' },
  success: { tone: 'ok', glyph: '✓', label: 'succeeded' },
  skipped: { tone: 'neutral', glyph: '⊝', label: 'skipped' },

  // Nodes
  pending: { tone: 'neutral', glyph: '○', label: 'pending' },
  waiting: { tone: 'neutral', glyph: '○', label: 'waiting' },
}

const UNKNOWN: Presentation = { tone: 'neutral', glyph: '?', label: 'unknown state' }

export function presentationFor(status: string): Presentation {
  return PRESENTATION[status] ?? UNKNOWN
}

/** True while a status is still moving, so callers can keep polling. */
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

export function StatusBadge(props: { status: string; class?: string }): JSX.Element {
  const p = () => presentationFor(props.status)
  return (
    <span
      class={`status status-${p().tone} ${props.status === 'running' ? 'status-running' : ''} ${props.class ?? ''}`}
      // The glyph is decorative; the text beside it already says the state.
      // Without this, a screen reader announces a meaningless symbol first.
      title={p().label}
    >
      <span class="status-glyph" aria-hidden="true">
        {p().glyph}
      </span>
      {props.status}
    </span>
  )
}
