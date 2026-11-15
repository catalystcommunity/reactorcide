import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/dom'
import type { ComboBox, PushButton, TextField } from '@catalystcommunity/controls'
import type { GetCapabilitiesResponse, ProjectDetail as ProjectDetailData } from '~/api/csilapi/types.gen.ts'
import type { ResourceState } from '~/store/index.ts'

/**
 * The project page's management cards.
 *
 * What these pin: the cards are gated on PROJECT-scoped capabilities, not the
 * session's global set; the webhook-secret form sends exactly what was typed
 * and then forgets the value; and the settings form keeps a half-typed edit
 * when a live project_update refreshes the page. The second matters because
 * the value is write-only on the server, so a form that kept it would be the
 * only place it survived.
 */

vi.mock('~/api/client.ts', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/client.ts')>()
  return {
    ...actual,
    api: {
      listRoleAssignments: vi.fn(async () => ({ assignments: [] })),
      assignRole: vi.fn(),
      revokeRole: vi.fn(),
      listUsers: vi.fn(async () => ({ users: [] })),
      listGroups: vi.fn(async () => ({ groups: [] })),
      listWebhookSecrets: vi.fn(async () => ({ secrets: [] })),
      addWebhookSecret: vi.fn(async () => ({ secret: {} })),
      deactivateWebhookSecret: vi.fn(),
      deleteWebhookSecret: vi.fn(),
      listVcsCredentials: vi.fn(async () => ({ credentials: [] })),
      addVcsCredential: vi.fn(),
      deactivateVcsCredential: vi.fn(),
      deleteVcsCredential: vi.fn(),
      updateProject: vi.fn(),
      listSchedules: vi.fn(async () => ({ schedules: [] })),
    },
  }
})

const NONE: GetCapabilitiesResponse = {
  viewPrivate: false,
  cancelJob: false,
  killJob: false,
  retryJob: false,
  createProject: false,
  deleteProject: false,
  manageWebhookSecrets: false,
  manageVcsCredentials: false,
  manageSecrets: false,
  manageGroups: false,
  manageWorkers: false,
  manageProjectSettings: false,
  manageTrustedIdentities: false,
  manageGlobalSettings: false,
  isGlobalAdmin: false,
  isOrgAdmin: false,
  isProjectOwner: false,
}

const ALL: GetCapabilitiesResponse = Object.fromEntries(
  Object.keys(NONE).map((key) => [key, true]),
) as unknown as GetCapabilitiesResponse

/** What the mocked capabilities hook answers; set per test. */
let capabilities: GetCapabilitiesResponse = NONE

const PROJECT: ProjectDetailData = {
  projectId: 'p1',
  orgId: 'org1',
  name: 'Widget',
  description: '',
  repoUrl: 'https://example.invalid/widget.git',
  isPrivate: true,
  enabled: true,
  targetBranches: [],
  allowedEventTypes: [],
  defaultCiSourceType: '',
  defaultCiSourceUrl: '',
  defaultCiSourceRef: '',
  defaultRunnerImage: '',
  defaultJobCommand: '',
  defaultTimeoutSeconds: 0,
  defaultQueueName: '',
} as unknown as ProjectDetailData

/** What the mocked project hook answers; a test replaces it to simulate a live refresh. */
let project: ProjectDetailData = PROJECT

function ready<T>(data: T): ResourceState<T> {
  return { data, error: undefined, loading: false, loaded: true }
}

vi.mock('~/store/resources.ts', () => {
  const handle = <T>(state: () => ResourceState<T>) => ({ state, refresh: async () => {}, sync: () => {}, release: () => {} })
  return {
    useProject: () => handle(() => ready({ project })),
    useCapabilities: () => handle(() => ready(capabilities)),
    useFormMetadata: () =>
      handle(() =>
        ready({
          eventTypes: [],
          checkoutModes: [],
          vcsProviders: [
            { value: 'github', label: 'GitHub', description: '' },
            { value: 'gitlab', label: 'GitLab', description: '' },
          ],
        }),
      ),
  }
})


import { api } from '~/api/client.ts'
import { mount, settle } from '~/test/mount.ts'
import { ProjectDetailPage, tag } from './ProjectDetail.ts'

async function renderProject(): Promise<ProjectDetailPage> {
  window.history.pushState({}, '', '/app/projects/p1')
  const { syncRouteFromLocation } = await import('~/ui/router.ts')
  syncRouteFromLocation()
  const page = document.createElement(tag) as ProjectDetailPage
  page.routeName = 'project'
  mount(page)
  await settle()
  return page
}

/** A Zest control host in `card` by the accessible name its Field gave it. */
function control<T extends Element>(card: HTMLElement, selector: string, label: RegExp): T {
  const match = [...card.querySelectorAll(selector)].find((el) => label.test(el.getAttribute('aria-label') ?? ''))
  if (!match) throw new Error(`no ${selector} labelled ${label}`)
  return match as T
}

const CARDS = ['Access', 'Webhook secrets', 'VCS credentials', 'Settings', 'Visibility', 'Events', 'Schedules']

describe('ProjectDetail management cards', () => {
  beforeEach(() => {
    capabilities = NONE
    project = PROJECT
  })
  afterEach(() => {
    vi.clearAllMocks()
  })

  it('renders none of the management cards without project-scoped capabilities', async () => {
    await renderProject()
    await screen.findByRole('heading', { name: 'Widget' })
    for (const heading of CARDS) {
      expect(screen.queryByRole('heading', { name: heading }), heading).toBeNull()
    }
    expect(api.listRoleAssignments).not.toHaveBeenCalled()
    expect(api.listWebhookSecrets).not.toHaveBeenCalled()
    expect(api.listVcsCredentials).not.toHaveBeenCalled()
  })

  it('renders the Access, Webhook secrets and VCS credentials cards when the project grants them', async () => {
    capabilities = ALL
    await renderProject()
    await screen.findByRole('heading', { name: 'Widget' })
    for (const heading of ['Access', 'Webhook secrets', 'VCS credentials']) {
      expect(screen.getByRole('heading', { name: heading })).toBeTruthy()
    }
    await waitFor(() => {
      expect(api.listRoleAssignments).toHaveBeenCalledWith({ scopeType: 'project', scopeId: 'p1' })
      expect(api.listWebhookSecrets).toHaveBeenCalledWith({ projectId: 'p1' })
      expect(api.listVcsCredentials).toHaveBeenCalledWith({ projectId: 'p1' })
    })
  })

  it('submits a webhook secret with what was typed and then clears the value', async () => {
    capabilities = ALL
    await renderProject()
    const heading = await screen.findByRole('heading', { name: 'Webhook secrets' })
    const card = heading.closest('.card') as HTMLElement

    const provider = control<ComboBox>(card, 'combo-box', /^Provider/)
    const name = control<TextField>(card, 'text-field', /^Name/)
    const value = control<TextField>(card, 'text-field', /^Secret/)
    expect(value.type).toBe('password')

    // As the person does it: the control changes, then sends its event.
    provider.value = 'gitlab'
    provider.dispatchEvent(new Event('change', { bubbles: true }))
    name.value = 'rotation 2026-09'
    name.dispatchEvent(new Event('input', { bubbles: true }))
    value.value = 'hunter2'
    value.dispatchEvent(new Event('input', { bubbles: true }))
    await settle()

    const submit = within(card).getByText('Add webhook secret').closest('push-button') as PushButton
    expect(submit.disabled).toBe(false)
    // happy-dom does not connect a custom element to its form, so the
    // push-button cannot submit it there; submit the form as the browser would.
    card.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))

    await waitFor(() => {
      expect(api.addWebhookSecret).toHaveBeenCalledWith({
        projectId: 'p1',
        provider: 'gitlab',
        name: 'rotation 2026-09',
        value: 'hunter2',
      })
    })
    await waitFor(() => expect(value.value).toBe(''))
    // The value never appears anywhere on the page after submit.
    expect(card.textContent).not.toContain('hunter2')
    expect(api.addVcsCredential).not.toHaveBeenCalled()
  })

  it('keeps a half-typed settings edit when a live update refreshes the project', async () => {
    capabilities = ALL
    const page = await renderProject()
    const heading = await screen.findByRole('heading', { name: 'Settings' })
    const card = heading.closest('.card') as HTMLElement
    const name = control<TextField>(card, 'text-field', /^Name/)
    expect(name.value).toBe('Widget')

    name.value = 'Widget, half ren'
    name.dispatchEvent(new Event('input', { bubbles: true }))

    // A project_update from somebody else lands mid-edit.
    project = { ...PROJECT, description: 'changed elsewhere' }
    page.invalidate()
    await settle()

    expect(control<TextField>(card, 'text-field', /^Name/)).toBe(name)
    expect(name.value).toBe('Widget, half ren')
    expect(screen.getByRole('heading', { name: 'Settings' })).toBe(heading)
  })
})
