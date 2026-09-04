import { Show, createSignal, type JSX } from 'solid-js'
import { ServiceError, TransportFailure } from '~/api/client.ts'

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
 */
export function useAction(): {
  busy: () => boolean
  error: () => string | undefined
  clearError: () => void
  run: (fallbackMessage: string, work: () => Promise<unknown>) => Promise<boolean>
} {
  const [busy, setBusy] = createSignal(false)
  const [error, setError] = createSignal<string>()

  return {
    busy,
    error,
    clearError: () => setError(undefined),
    run: async (fallbackMessage, work) => {
      if (busy()) return false
      setBusy(true)
      setError(undefined)
      try {
        await work()
        return true
      } catch (cause) {
        setError(describeError(cause, fallbackMessage))
        return false
      } finally {
        setBusy(false)
      }
    },
  }
}

export function ActionError(props: { error: string | undefined }): JSX.Element {
  return (
    <Show when={props.error}>
      <div class="alert alert-error" role="alert">
        {props.error}
      </div>
    </Show>
  )
}

/** The page shown when a caller lacks the capability a whole page is about. */
export function NotPermitted(props: { what: string }): JSX.Element {
  return (
    <div class="page">
      <div class="empty">
        <div class="empty-title">You do not have permission to manage {props.what}.</div>
        <p>Ask an administrator of your organization if you need access.</p>
      </div>
    </div>
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
export function OneTimeSecret(props: { label: string; value: string; onDismiss: () => void }): JSX.Element {
  return (
    <div class="alert alert-info">
      <div style={{ 'font-weight': '600', 'margin-bottom': 'var(--space-2)' }}>
        {props.label} — copy this now.
      </div>
      <p class="meta" style={{ 'margin-bottom': 'var(--space-2)' }}>
        This is the only time it is shown. Nothing can retrieve it again; if it is lost, revoke
        it and create another.
      </p>
      <div class="row">
        <code class="one-time-secret">{props.value}</code>
        <button
          type="button"
          class="btn btn-sm"
          onClick={() => void navigator.clipboard?.writeText(props.value)}
        >
          Copy
        </button>
        <button type="button" class="btn btn-sm btn-ghost" onClick={props.onDismiss}>
          Dismiss
        </button>
      </div>
    </div>
  )
}
