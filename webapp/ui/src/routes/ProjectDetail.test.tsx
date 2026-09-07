import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@solidjs/testing-library'
import { MemoryRouter, Route, createMemoryHistory } from '@solidjs/router'
import type { GetCapabilitiesResponse, ProjectDetail as ProjectDetailData } from '~/api/csilapi/types.gen.ts'
import type { ResourceState } from '~/store/index.ts'

/**
 * The project page's management cards.
 *
 * What these pin: the cards are gated on PROJECT-scoped capabilities, not the
 * session's global set; and the webhook-secret form sends exactly what was
 * typed and then forgets the value. The second matters because the value is
 * write-only on the server, so a form that kept it would be the only place it
 * survived.
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

function ready<T>(data: T): ResourceState<T> {
  return { data, error: undefined, loading: false, loaded: true }
}

vi.mock('~/store/resources.ts', () => ({
  useProject: () => ({ state: () => ready({ project: PROJECT }), refresh: async () => {} }),
  useCapabilities: () => ({ state: () => ready(capabilities), refresh: async () => {} }),
  useFormMetadata: () => ({
    state: () =>
      ready({
        eventTypes: [],
        checkoutModes: [],
        vcsProviders: [
          { value: 'github', label: 'GitHub', description: '' },
          { value: 'gitlab', label: 'GitLab', description: '' },
        ],
      }),
    refresh: async () => {},
  }),
}))

import { api } from '~/api/client.ts'
import { ProjectDetail } from './ProjectDetail.tsx'

function renderProject() {
  const history = createMemoryHistory()
  history.set({ value: '/projects/p1' })
  return render(() => (
    <MemoryRouter history={history}>
      <Route path="/projects/:id" component={ProjectDetail} />
    </MemoryRouter>
  ))
}

const CARDS = ['Access', 'Webhook secrets', 'VCS credentials', 'Settings', 'Visibility']

describe('ProjectDetail management cards', () => {
  beforeEach(() => {
    capabilities = NONE
  })
  afterEach(() => {
    vi.clearAllMocks()
  })

  it('renders none of the management cards without project-scoped capabilities', async () => {
    renderProject()
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
    renderProject()
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
    renderProject()
    const heading = await screen.findByRole('heading', { name: 'Webhook secrets' })
    const card = heading.closest('.card') as HTMLElement

    const provider = within(card).getByLabelText(/^Provider/) as HTMLSelectElement
    const name = within(card).getByLabelText(/^Name/) as HTMLInputElement
    const value = within(card).getByLabelText(/^Secret/) as HTMLInputElement
    expect(value.type).toBe('password')

    fireEvent.change(provider, { target: { value: 'gitlab' } })
    fireEvent.input(name, { target: { value: 'rotation 2026-09' } })
    fireEvent.input(value, { target: { value: 'hunter2' } })

    const submit = within(card).getByRole('button', { name: 'Add webhook secret' }) as HTMLButtonElement
    expect(submit.disabled).toBe(false)
    fireEvent.click(submit)

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
})
