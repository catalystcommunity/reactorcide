import { describe, expect, it, vi } from 'vitest'

// The pages load data on connect; nothing here is about the data, so every
// call answers with an empty, never-resolving promise.
vi.mock('~/api/client.ts', () => {
  const pending = () => new Promise(() => {})
  return {
    api: new Proxy({}, { get: () => pending }),
    authApi: new Proxy({}, { get: () => pending }),
    ServiceError: class extends Error {},
    TransportFailure: class extends Error {},
  }
})

import './App.ts'
import { setSessionForTest } from '~/lib/session.ts'
import { navigate, setQuery } from '~/ui/router.ts'
import { mount, settle } from '~/test/mount.ts'

async function waitForPage(tag: string): Promise<Element> {
  await vi.waitFor(() => expect(document.querySelector(tag)).toBeTruthy())
  return document.querySelector(tag)!
}

function navLabels(): string[] {
  return [...document.querySelectorAll<HTMLAnchorElement>('.app-nav-link')].filter((a) => !a.hidden).map((a) => a.textContent ?? '')
}

describe('application shell', () => {
  it('keeps the page element when only the query changes, so an open control stays open', async () => {
    mount(document.createElement('rc-app'))
    navigate('/jobs')
    const page = await waitForPage('rc-jobs-page')

    setQuery({ status: 'failed' })
    await settle()

    expect(document.querySelector('rc-jobs-page')).toBe(page)
  })

  it('makes a new page element for a different id', async () => {
    mount(document.createElement('rc-app'))
    navigate('/jobs/job-a')
    const first = await waitForPage('rc-job-detail-page')
    navigate('/jobs/job-b')
    await vi.waitFor(() => expect(document.querySelector('rc-job-detail-page')).not.toBe(first))
    expect(first.isConnected).toBe(false)
    expect(document.querySelectorAll('rc-job-detail-page')).toHaveLength(1)
  })

  it('shows NotFound for an unknown path', async () => {
    mount(document.createElement('rc-app'))
    navigate('/no-such-page')
    await waitForPage('rc-not-found-page')
  })

  it('shows management links only to somebody who administers an org', async () => {
    setSessionForTest({ logged_in: true, is_global_admin: false, capabilities: {} as never, roles: [] })
    mount(document.createElement('rc-app'))
    await settle()
    expect(navLabels()).toEqual(['Workflows', 'Jobs', 'Projects', 'Insights'])

    setSessionForTest({
      logged_in: true,
      is_global_admin: false,
      capabilities: {} as never,
      roles: [{ scope_type: 'org', scope_id: 'org-1', role: 'admin' }],
    })
    await settle()
    expect(navLabels()).toEqual(['Workflows', 'Jobs', 'Projects', 'Insights', 'Access', 'Secrets', 'Images', 'Worker classes'])

    setSessionForTest({ logged_in: true, is_global_admin: true, capabilities: {} as never, roles: [] })
    await settle()
    expect(navLabels()).toEqual(['Workflows', 'Jobs', 'Projects', 'Insights', 'Access', 'Secrets', 'Images', 'Workers', 'Admin'])
  })

  it('marks the current section in the navigation', async () => {
    mount(document.createElement('rc-app'))
    navigate('/jobs/01ABC')
    await settle()
    const active = [...document.querySelectorAll('.app-nav-link-active')].map((a) => a.textContent)
    expect(active).toEqual(['Jobs'])
    expect(document.querySelector('.app-nav-link-active')?.getAttribute('aria-current')).toBe('page')
  })
})
