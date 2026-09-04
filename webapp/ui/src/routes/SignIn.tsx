import { Show, createSignal, type JSX } from 'solid-js'
import { useSearchParams } from '@solidjs/router'
import { useSession } from '~/lib/session.tsx'
import { beginLogin, bootstrapAdmin } from '~/api/auth.ts'
import { Field } from '~/components/Field.tsx'

/**
 * Sign in, and the one-time bootstrap.
 *
 * Neither form posts through the CSIL bridge: both mint a session cookie, which
 * a CSIL operation cannot do. `login_error` arrives in the query string because
 * the identity provider's callback is a top-level navigation, so a failure has
 * to survive a redirect rather than land in a fetch response.
 */
export function SignIn(): JSX.Element {
  const { config } = useSession()
  const [params] = useSearchParams<{ login_error?: string }>()
  const [identity, setIdentity] = createSignal('')
  const [error, setError] = createSignal<string>()
  const [busy, setBusy] = createSignal(false)

  const submit = async (event: Event) => {
    event.preventDefault()
    if (!identity().trim()) {
      setError('Enter your identity to continue.')
      return
    }
    setBusy(true)
    setError(undefined)
    const failure = await beginLogin(identity().trim())
    setBusy(false)
    if (failure) setError(failure)
  }

  return (
    <div class="page" style={{ 'max-width': '32rem' }}>
      <h1 style={{ 'margin-bottom': 'var(--space-4)' }}>Sign in</h1>

      <Show when={params.login_error}>
        <div class="alert alert-error" role="alert">
          {params.login_error}
        </div>
      </Show>

      <Show
        when={config()?.login_enabled}
        fallback={
          <div class="card">
            <p>
              This deployment does not require sign-in. Everything you can see is available
              without an account.
            </p>
          </div>
        }
      >
        <div class="card">
          <form onSubmit={submit}>
            <Show when={error()}>
              <div class="alert alert-error" role="alert">
                {error()}
              </div>
            </Show>

            <Field
              label="Identity"
              required
              hint="Your LinkKeys identity, as handle@domain."
              tooltip="This is the identity your organization issued you. The sign-in itself happens on your identity provider; Reactorcide never sees your credentials."
            >
              {(ids) => (
                <input
                  id={ids.id}
                  class="input"
                  type="text"
                  autocomplete="username"
                  placeholder="you@example.com"
                  aria-describedby={ids.describedBy}
                  value={identity()}
                  onInput={(event) => setIdentity(event.currentTarget.value)}
                />
              )}
            </Field>

            <button type="submit" class="btn btn-primary" disabled={busy()}>
              {busy() ? 'Redirecting…' : 'Continue'}
            </button>
          </form>
        </div>
      </Show>

      <Show when={config()?.bootstrap_available}>
        <BootstrapCard />
      </Show>
    </div>
  )
}

/**
 * Shown only while the deployment has no global admin yet.
 *
 * The token is never echoed back into the form on failure: it is a one-time
 * administrative credential, and leaving it in a field that a screenshot or a
 * password manager might capture is a needless risk.
 */
function BootstrapCard(): JSX.Element {
  const [token, setToken] = createSignal('')
  const [error, setError] = createSignal<string>()
  const [busy, setBusy] = createSignal(false)

  const submit = async (event: Event) => {
    event.preventDefault()
    setBusy(true)
    setError(undefined)
    const failure = await bootstrapAdmin(token())
    setBusy(false)
    if (failure) {
      setError(failure)
      setToken('')
    }
  }

  return (
    <div class="card">
      <h2 style={{ 'margin-bottom': 'var(--space-2)' }}>First administrator</h2>
      <p class="meta" style={{ 'margin-bottom': 'var(--space-4)' }}>
        This deployment has no administrator yet. Redeem the bootstrap token to become one.
        This form disappears once an administrator exists.
      </p>

      <form onSubmit={submit}>
        <Show when={error()}>
          <div class="alert alert-error" role="alert">
            {error()}
          </div>
        </Show>

        <Field label="Bootstrap token" required hint="From REACTORCIDE_BOOTSTRAP_ADMIN_TOKEN.">
          {(ids) => (
            <input
              id={ids.id}
              class="input"
              type="password"
              autocomplete="off"
              aria-describedby={ids.describedBy}
              value={token()}
              onInput={(event) => setToken(event.currentTarget.value)}
            />
          )}
        </Field>

        <button type="submit" class="btn btn-primary" disabled={busy() || !token()}>
          {busy() ? 'Redeeming…' : 'Become administrator'}
        </button>
      </form>
    </div>
  )
}
