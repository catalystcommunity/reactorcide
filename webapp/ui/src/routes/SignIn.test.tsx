import { describe, expect, it, vi } from 'vitest'
import { render, fireEvent } from '@solidjs/testing-library'
import { MemoryRouter, Route, createMemoryHistory } from '@solidjs/router'
import { SignIn } from './SignIn.tsx'

/**
 * The sign-in form must be a native POST.
 *
 * The webapp answers it with a 302 to the identity provider. A fetch follows
 * that redirect itself, requests the provider's page cross-origin without CORS
 * headers, and rejects; the first version did that and showed "Redirecting…"
 * forever. These tests pin the form's method, action and field name so the
 * browser, not script, carries the identity off this origin.
 */

vi.mock('~/lib/session.tsx', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/lib/session.tsx')>()
  return {
    ...actual,
    useSession: () => ({
      session: () => ({ logged_in: false, is_global_admin: false, capabilities: {}, roles: [] }),
      config: () => ({ auth_mode: 'rp', login_enabled: true, bootstrap_available: false, has_global_admin: false }),
      refetch: () => undefined,
    }),
  }
})

function mount(path: string) {
  const history = createMemoryHistory()
  history.set({ value: path })
  return render(() => (
    <MemoryRouter history={history}>
      <Route path="/signin" component={SignIn} />
    </MemoryRouter>
  ))
}

describe('SignIn', () => {
  it('posts the identity natively to the webapp login endpoint', () => {
    const { container } = mount('/signin')
    const form = container.querySelector('form') as HTMLFormElement
    expect(form).not.toBeNull()
    expect(form.getAttribute('method')?.toLowerCase()).toBe('post')
    expect(form.getAttribute('action')).toBe('/app/auth/login')

    const input = container.querySelector('input[name="identity"]') as HTMLInputElement
    expect(input).not.toBeNull()
  })

  it('blocks an empty submission client-side and says so', () => {
    const { container, getByRole } = mount('/signin')
    const form = container.querySelector('form') as HTMLFormElement
    const event = new Event('submit', { cancelable: true, bubbles: true })
    form.dispatchEvent(event)
    expect(event.defaultPrevented).toBe(true)
    expect(getByRole('alert').textContent).toContain('Enter your identity')
  })

  it('lets a non-empty submission through and prefills from the query string', () => {
    const { container } = mount('/signin?login_error=login%20failed&identity=tod%40example.com')
    const input = container.querySelector('input[name="identity"]') as HTMLInputElement
    expect(input.value).toBe('tod@example.com')
    expect(container.textContent).toContain('login failed')

    fireEvent.input(input, { target: { value: 'tod@example.com' } })
    const form = container.querySelector('form') as HTMLFormElement
    const event = new Event('submit', { cancelable: true, bubbles: true })
    form.dispatchEvent(event)
    expect(event.defaultPrevented).toBe(false)
  })
})
