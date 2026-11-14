import type { PushButton, TextField } from '@catalystcommunity/controls'
import { bootstrapAdmin } from '~/api/auth.ts'
import { RcPage } from '~/app/page.ts'
import { field } from '~/components/Field.ts'
import { SESSION, authConfig } from '~/lib/session.ts'
import { button, setButtonText, textField } from '~/ui/controls.ts'
import { h, setText, show } from '~/ui/dom.ts'
import { define } from '~/ui/element.ts'
import { ROUTE } from '~/ui/router.ts'

/**
 * Sign in, and the one-time bootstrap.
 *
 * Neither form posts through the CSIL bridge: both mint a session cookie, which
 * a CSIL operation cannot do. `login_error` arrives in the query string because
 * the identity provider's callback is a top-level navigation, so a failure has
 * to survive a redirect rather than land in a fetch response.
 *
 * The sign-in form is a NATIVE form post, not a fetch. The webapp answers it
 * with a 302 to the identity provider, and a fetch follows that redirect
 * itself: the browser requests the provider's page cross-origin, gets no CORS
 * headers back, and the fetch rejects. The first version did exactly that, so
 * the button said "Redirecting…" forever and nothing else happened. Only a
 * top-level navigation can leave the origin and come back.
 *
 * The identity field is a Zest text-field with `name="identity"`. It is form
 * associated, so the browser puts its value in the native post, and its inner
 * input carries `autocomplete="username"` for password managers.
 */
export class SignInPage extends RcPage {
  static watches = [ROUTE, SESSION]

  #loginError!: HTMLDivElement
  #loginCard!: HTMLElement
  #noLogin!: HTMLElement
  #formError!: HTMLDivElement
  #identity!: TextField
  #continue!: PushButton
  #busy = false
  #bootstrap!: BootstrapCard

  protected build(): void {
    const query = this.route().query
    this.#loginError = h('div', { class: 'alert alert-error', role: 'alert', hidden: true })
    this.#formError = h('div', { class: 'alert alert-error', role: 'alert', hidden: true })

    // A failed attempt comes back with the identity that was typed, so nobody
    // has to type it twice.
    this.#identity = textField({
      autocomplete: 'username',
      placeholder: 'you@example.com',
      value: query.get('identity') ?? '',
    })
    // The ATTRIBUTE, not the property: the browser names a form-associated
    // element's entry in the post from its `name` attribute, and Zest does not
    // reflect the property to it.
    this.#identity.setAttribute('name', 'identity')
    this.#continue = button('Continue', { variant: 'primary', type: 'submit' })

    const form = h(
      'form',
      { method: 'post', action: '/app/auth/login' },
      this.#formError,
      field({
        label: 'Identity',
        required: true,
        hint: 'Your LinkKeys identity, as handle@domain.',
        tooltip:
          'This is the identity your organization issued you. The sign-in itself happens on your identity provider; Reactorcide never sees your credentials.',
        control: this.#identity,
      }).el,
      this.#continue,
    )
    form.addEventListener('submit', (event) => this.#submit(event))

    this.#loginCard = h('div', { class: 'card' }, form)
    this.#noLogin = h(
      'div',
      { class: 'card' },
      h('p', null, 'This deployment does not require sign-in. Everything you can see is available without an account.'),
    )
    this.#bootstrap = new BootstrapCard()

    this.append(
      h(
        'div',
        { class: 'page', style: { maxWidth: '32rem' } },
        h('h1', { style: { marginBottom: 'var(--space-4)' } }, 'Sign in'),
        this.#loginError,
        this.#loginCard,
        this.#noLogin,
        this.#bootstrap.el,
      ),
    )
  }

  /**
   * Runs before the browser submits. It only blocks an empty field; a
   * non-empty one lets the native post go ahead and navigate away.
   */
  #submit(event: Event): void {
    if (!this.#identity.value.trim()) {
      event.preventDefault()
      this.#setFormError('Enter your identity to continue.')
      return
    }
    this.#setFormError(undefined)
    this.#busy = true
    this.invalidate()
  }

  #setFormError(message: string | undefined): void {
    show(this.#formError, message)
    setText(this.#formError, message ?? '')
  }

  render(): void {
    const loginError = this.route().query.get('login_error') ?? ''
    show(this.#loginError, loginError)
    setText(this.#loginError, loginError)

    const config = authConfig()
    const enabled = Boolean(config?.login_enabled)
    show(this.#loginCard, enabled)
    // Nothing until the config is known, so a login deployment never flashes
    // "does not require sign-in".
    show(this.#noLogin, config && !enabled)
    this.#continue.disabled = this.#busy
    setButtonText(this.#continue, this.#busy ? 'Redirecting…' : 'Continue')

    show(this.#bootstrap.el, config?.bootstrap_available)
  }
}

/**
 * Shown only while the deployment has no global admin yet.
 *
 * The token is never echoed back into the form on failure: it is a one-time
 * administrative credential, and leaving it in a field that a screenshot or a
 * password manager might capture is a needless risk.
 */
class BootstrapCard {
  readonly el: HTMLElement
  readonly #token: TextField
  readonly #submit: PushButton
  readonly #error: HTMLDivElement
  #busy = false

  constructor() {
    this.#token = textField({ type: 'password', autocomplete: 'off', onInput: () => this.#update() })
    this.#submit = button('Become administrator', { variant: 'primary', type: 'submit', disabled: true })
    this.#error = h('div', { class: 'alert alert-error', role: 'alert', hidden: true })
    const form = h(
      'form',
      null,
      this.#error,
      field({ label: 'Bootstrap token', required: true, hint: 'From REACTORCIDE_BOOTSTRAP_ADMIN_TOKEN.', control: this.#token }).el,
      this.#submit,
    )
    form.addEventListener('submit', (event) => {
      event.preventDefault()
      void this.#redeem()
    })
    this.el = h(
      'div',
      { class: 'card', hidden: true },
      h('h2', { style: { marginBottom: 'var(--space-2)' } }, 'First administrator'),
      h(
        'p',
        { class: 'meta', style: { marginBottom: 'var(--space-4)' } },
        'This deployment has no administrator yet. Redeem the bootstrap token to become one. This form disappears once an administrator exists.',
      ),
      form,
    )
  }

  async #redeem(): Promise<void> {
    const token = this.#token.value
    if (!token || this.#busy) return
    this.#busy = true
    this.#setError(undefined)
    this.#update()
    const failure = await bootstrapAdmin(token)
    this.#busy = false
    if (failure) {
      this.#setError(failure)
      this.#token.value = ''
      this.#token.touched = false
    }
    this.#update()
  }

  #setError(message: string | undefined): void {
    show(this.#error, message)
    setText(this.#error, message ?? '')
  }

  #update(): void {
    this.#submit.disabled = this.#busy || !this.#token.value
    setButtonText(this.#submit, this.#busy ? 'Redeeming…' : 'Become administrator')
  }
}

export const tag = 'rc-sign-in-page'
define(tag, SignInPage)
