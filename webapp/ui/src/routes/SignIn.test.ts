import { describe, expect, it } from 'vitest'
import { getByRole } from '@testing-library/dom'
import type { TextField } from '@catalystcommunity/controls'
import { tag } from './SignIn.ts'
import { setSessionForTest } from '~/lib/session.ts'
import { syncRouteFromLocation } from '~/ui/router.ts'
import { mount, settle } from '~/test/mount.ts'

/**
 * The sign-in form must be a native POST.
 *
 * The webapp answers it with a 302 to the identity provider. A fetch follows
 * that redirect itself, requests the provider's page cross-origin without CORS
 * headers, and rejects; the first version did that and showed "Redirecting…"
 * forever. These tests pin the form's method, action and field name so the
 * browser, not script, carries the identity off this origin.
 */

async function open(path: string): Promise<HTMLElement> {
  window.history.pushState({}, '', path)
  syncRouteFromLocation()
  setSessionForTest(
    { logged_in: false, is_global_admin: false, capabilities: {} as never, roles: [] },
    { auth_mode: 'rp', login_enabled: true, bootstrap_available: false, has_global_admin: false },
  )
  const page = mount(document.createElement(tag))
  await settle()
  return page
}

function submit(form: HTMLFormElement): Event {
  // happy-dom connects no custom element to its form, so the push-button
  // cannot submit there. Dispatch the submit event the browser would send.
  const event = new Event('submit', { cancelable: true, bubbles: true })
  form.dispatchEvent(event)
  return event
}

describe('SignIn', () => {
  it('posts the identity natively to the webapp login endpoint', async () => {
    const page = await open('/app/signin')
    const form = page.querySelector('form') as HTMLFormElement
    expect(form).not.toBeNull()
    expect(form.getAttribute('method')?.toLowerCase()).toBe('post')
    expect(form.getAttribute('action')).toBe('/app/auth/login')

    // The form-associated field carries the name the server reads, and the
    // inner input tells a password manager it is the username.
    const field = form.querySelector('text-field[name="identity"]') as TextField
    expect(field).not.toBeNull()
    expect(field.querySelector('input')?.getAttribute('autocomplete')).toBe('username')
  })

  it('blocks an empty submission client-side and says so', async () => {
    const page = await open('/app/signin')
    const event = submit(page.querySelector('form') as HTMLFormElement)
    expect(event.defaultPrevented).toBe(true)
    expect(getByRole(page, 'alert').textContent).toContain('Enter your identity')
  })

  it('lets a non-empty submission through and prefills from the query string', async () => {
    const page = await open('/app/signin?login_error=login%20failed&identity=tod%40example.com')
    const field = page.querySelector('text-field[name="identity"]') as TextField
    expect(field.value).toBe('tod@example.com')
    expect(page.textContent).toContain('login failed')

    const event = submit(page.querySelector('form') as HTMLFormElement)
    expect(event.defaultPrevented).toBe(false)
  })
})
