import { ServiceError, TransportFailure } from '~/api/client.ts'
import { button } from '~/ui/controls.ts'
import { h, setText, show } from '~/ui/dom.ts'

/**
 * Shared scaffolding for the management pages.
 *
 * These pages are all the same shape: read a list, mutate it, read it again.
 * The pieces below exist so each page spends its lines on WHAT it manages
 * rather than on re-implementing error handling and pending state.
 *
 * The capability gate is a rendering convenience, not a control. The
 * coordinator authorizes every operation independently, so a page rendered for
 * somebody who should not see it still cannot do anything — it would simply
 * show a column of failures instead of a clean refusal.
 */

/** Turns any thrown value into something worth showing a person. */
export function describeError(cause: unknown, fallback: string): string {
  if (cause instanceof ServiceError) return cause.message
  if (cause instanceof TransportFailure) return cause.message
  return fallback
}

/**
 * Wraps a mutation with pending state and error capture.
 *
 * Every management action goes through this so none of them can silently do
 * nothing: a failure always surfaces, and a double-click cannot fire twice.
 * `onChange` runs after each change of `busy` or `error`; give it the host's
 * `invalidate`.
 */
export class Action {
  busy = false
  error: string | undefined = undefined
  readonly #onChange: () => void

  constructor(onChange: () => void) {
    this.#onChange = onChange
  }

  clearError(): void {
    if (this.error === undefined) return
    this.error = undefined
    this.#onChange()
  }

  async run(fallbackMessage: string, work: () => Promise<unknown>): Promise<boolean> {
    if (this.busy) return false
    this.busy = true
    this.error = undefined
    this.#onChange()
    try {
      await work()
      return true
    } catch (cause) {
      this.error = describeError(cause, fallbackMessage)
      return false
    } finally {
      this.busy = false
      this.#onChange()
    }
  }
}

/** An error box for an Action. Call `.set(action.error)` from render(). */
export class ActionError {
  readonly el: HTMLDivElement

  constructor() {
    this.el = h('div', { class: 'alert alert-error', role: 'alert', hidden: true })
  }

  set(error: string | undefined): void {
    show(this.el, error)
    setText(this.el, error ?? '')
  }
}

/** The page shown when a caller lacks the capability a whole page is about. */
export function notPermitted(what: string): HTMLElement {
  return h(
    'div',
    { class: 'page' },
    h(
      'div',
      { class: 'empty' },
      h('div', { class: 'empty-title' }, `You do not have permission to manage ${what}.`),
      h('p', null, 'Ask an administrator of your organization if you need access.'),
    ),
  )
}

/**
 * A value shown exactly once and never again.
 *
 * Enrollment tokens are the case this exists for: create-enrollment-token is
 * the only operation that ever returns the token itself, no later call can
 * retrieve it, and it must never be logged. Saying so where it is displayed is
 * the difference between an operator copying it and an operator having to
 * revoke and recreate one.
 */
export function oneTimeSecret(label: string, value: string, onDismiss: () => void): HTMLElement {
  return h(
    'div',
    { class: 'alert alert-info' },
    h('div', { style: { fontWeight: '600', marginBottom: 'var(--space-2)' } }, `${label} — copy this now.`),
    h(
      'p',
      { class: 'meta', style: { marginBottom: 'var(--space-2)' } },
      'This is the only time it is shown. Nothing can retrieve it again; if it is lost, revoke it and create another.',
    ),
    h(
      'div',
      { class: 'row' },
      h('code', { class: 'one-time-secret' }, value),
      button('Copy', { small: true, onClick: () => void navigator.clipboard?.writeText(value) }),
      button('Dismiss', { small: true, variant: 'flat', onClick: onDismiss }),
    ),
  )
}
