import { afterEach, describe, expect, it, vi } from 'vitest'
import { waitFor } from '@testing-library/dom'
import type { ComboBox, TextField } from '@catalystcommunity/controls'
import type {
  DiagnosisSummary,
  GetOrgReportingSettingsResponse,
  GetRequiredSetOrgSettingsResponse,
  ListOidcKeysResponse,
  OidcSettingsResponse,
  ProjectDetail,
} from '~/api/csilapi/types.gen.ts'
import type { SessionSummary } from '~/api/auth.ts'

let reporting: GetOrgReportingSettingsResponse
let requiredSet: GetRequiredSetOrgSettingsResponse
let oidc: OidcSettingsResponse
let keys: ListOidcKeysResponse
let diagnoses: DiagnosisSummary[] = []
const artifactBytes = [new Uint8Array([1, 2, 3]), new Uint8Array([4, 5])]

vi.mock('~/api/client.ts', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/client.ts')>()
  return {
    ...actual,
    api: {
      getOrgReportingSettings: vi.fn(async () => reporting),
      putOrgReportingSettings: vi.fn(async (req: { settings: GetOrgReportingSettingsResponse['settings'] }) => ({ settings: req.settings, canManage: true })),
      getRequiredSetOrgSettings: vi.fn(async () => requiredSet),
      putRequiredSetOrgSettings: vi.fn(async (req: Omit<GetRequiredSetOrgSettingsResponse, 'canManage'>) => ({ ...req, canManage: true })),
      getOidcSettings: vi.fn(async () => oidc),
      putOidcSettings: vi.fn(async (req: { issuer: string; audienceClaims: OidcSettingsResponse['audienceClaims'] }) => ({ ...oidc, issuer: req.issuer, audienceClaims: req.audienceClaims })),
      listOidcKeys: vi.fn(async () => keys),
      rotateOidcKey: vi.fn(async () => keys),
      retireOidcKey: vi.fn(async () => keys),
      listDiagnoses: vi.fn(async () => ({ diagnoses })),
      deleteDiagnosis: vi.fn(async () => ({ deleted: true })),
      readJobArtifact: vi.fn(async (req: { offset: number }) => {
        const index = req.offset === 0 ? 0 : 1
        return { data: artifactBytes[index], sizeBytes: 5, sha256: '', eof: index === 1 }
      }),
    },
  }
})

vi.mock('@catalystcommunity/containers', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@catalystcommunity/containers')>()
  return { ...actual, askConfirm: vi.fn(async () => true) }
})

import { api } from '~/api/client.ts'
import { setSessionForTest } from '~/lib/session.ts'
import { mount, settle, unmount } from '~/test/mount.ts'
import { deleteTarget, diagnosisCard, isOwnEntry } from './Diagnoses.ts'
import { readArtifactParts } from './JobArtifacts.ts'
import { actorShort, actorText, ownerGroupText, requiredStateBadge, setRequiredState } from './Labels.ts'
import { oidcKeysCard, oidcSettingsCard, parseClaims } from './Oidc.ts'
import { orgReportingCard, requiredSetOrgCard } from './OrgSettings.ts'
import { ownerSubjectError } from './PathOwners.ts'
import { limitsText } from './TrustSettings.ts'
import { executionRequest } from '~/routes/ProjectExecution.ts'

let mounted: Element | null = null

afterEach(() => {
  if (mounted) unmount(mounted)
  mounted = null
  vi.clearAllMocks()
})

function buttonByText(root: Element, text: string): HTMLElement {
  const found = [...root.querySelectorAll('push-button')].find((b) => b.textContent === text)
  if (!found) throw new Error(`no button "${text}"`)
  return found as HTMLElement
}

function addChip(input: TextField, value: string): void {
  input.value = value
  input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
}

describe('organization reporting defaults', () => {
  it('sends a full replace with the path owner registry, and keeps a draft through a refresh', async () => {
    reporting = {
      settings: { reportMode: 'full', agreementContexts: ['ci/build'], codeownersImport: 'off', codeownersCacheTtlSeconds: 600, pathAcceptance: 'warn', pathOwners: [{ pattern: '/payments/', owners: ['vcs_team:acme/payments'] }] },
      canManage: true,
    }
    const el = orgReportingCard('acme')
    mounted = mount(el)
    await settle()
    await waitFor(() => expect(api.getOrgReportingSettings).toHaveBeenCalledWith({ organization: 'acme' }))
    await waitFor(() => expect(el.querySelector('.path-owner-row')).not.toBeNull())
    const mode = el.querySelector<ComboBox>('combo-box[aria-label^="Report mode"]')!
    mode.value = 'silent'
    // A refresh with the same stored state keeps the draft.
    el.invalidate()
    await settle()
    expect(mode.value).toBe('silent')
    // Add a second rule.
    buttonByText(el, 'Add rule').click()
    await settle()
    const rows = el.querySelectorAll('.path-owner-row')
    expect(rows).toHaveLength(2)
    const pattern = rows[1].querySelector<TextField>('text-field')!
    pattern.value = '*.sql'
    addChip(rows[1].querySelectorAll<TextField>('text-field')[1], 'reactorcide_group:dba')
    buttonByText(el, 'Save reporting defaults').click()
    await waitFor(() =>
      expect(api.putOrgReportingSettings).toHaveBeenCalledWith({
        organization: 'acme',
        settings: {
          reportMode: 'silent',
          agreementContexts: ['ci/build'],
          codeownersImport: 'off',
          codeownersCacheTtlSeconds: 600,
          pathAcceptance: 'warn',
          pathOwners: [
            { pattern: '/payments/', owners: ['vcs_team:acme/payments'] },
            { pattern: '*.sql', owners: ['reactorcide_group:dba'] },
          ],
        },
      }),
    )
  })

  it('refuses a rule with no owner before the round trip, and is read-only for a member', async () => {
    reporting = { settings: { reportMode: 'full', agreementContexts: [], codeownersImport: 'off', codeownersCacheTtlSeconds: 600, pathAcceptance: 'off', pathOwners: [] }, canManage: true }
    const el = orgReportingCard('acme')
    mounted = mount(el)
    await settle()
    await waitFor(() => expect(el.querySelector('.path-owners-editor')).not.toBeNull())
    buttonByText(el, 'Add rule').click()
    await settle()
    el.querySelector<TextField>('.path-owner-row text-field')!.value = '/docs/'
    buttonByText(el, 'Save reporting defaults').click()
    await settle()
    expect(api.putOrgReportingSettings).not.toHaveBeenCalled()
    expect(el.textContent).toContain('Add an owner to the rule for /docs/')
    unmount(el)

    reporting = { ...reporting, canManage: false }
    const ro = orgReportingCard('acme')
    mounted = mount(ro)
    await settle()
    await waitFor(() => expect(ro.textContent).toContain('Only an organization administrator'))
    expect(buttonByText(ro, 'Save reporting defaults').hidden).toBe(true)
  })

  it('checks an owner subject', () => {
    expect(ownerSubjectError('vcs_team:acme/x')).toBeUndefined()
    expect(ownerSubjectError('no-colon')).toBeDefined()
    expect(ownerSubjectError('a b:c')).toBeDefined()
  })
})

describe('required set defaults', () => {
  it('saves the approvers, the rules and the longest quarantine', async () => {
    requiredSet = { approvers: ['reactorcide_group:platform'], approverRules: [{ branches: ['release/*'], approvers: ['reactorcide_group:release'] }], maxQuarantineDays: 30, canManage: true }
    const el = requiredSetOrgCard('acme')
    mounted = mount(el)
    await settle()
    await waitFor(() => expect(el.querySelector('.approver-rule-row')).not.toBeNull())
    const days = [...el.querySelectorAll<TextField>('text-field')].find((f) => f.getAttribute('aria-label')?.startsWith('Longest quarantine'))!
    days.value = '14'
    buttonByText(el, 'Save required set defaults').click()
    await waitFor(() =>
      expect(api.putRequiredSetOrgSettings).toHaveBeenCalledWith({
        organization: 'acme',
        approvers: ['reactorcide_group:platform'],
        approverRules: [{ branches: ['release/*'], approvers: ['reactorcide_group:release'] }],
        maxQuarantineDays: 14,
      }),
    )
  })

  it('refuses a longest quarantine out of range', async () => {
    requiredSet = { approvers: [], approverRules: [], maxQuarantineDays: 30, canManage: true }
    const el = requiredSetOrgCard('acme')
    mounted = mount(el)
    await settle()
    await waitFor(() => expect(el.textContent).toContain('Longest quarantine'))
    const days = [...el.querySelectorAll<TextField>('text-field')].find((f) => f.getAttribute('aria-label')?.startsWith('Longest quarantine'))!
    days.value = '900'
    buttonByText(el, 'Save required set defaults').click()
    await settle()
    expect(api.putRequiredSetOrgSettings).not.toHaveBeenCalled()
    expect(el.textContent).toContain('from 1 to 365')
  })
})

describe('OIDC settings and keys', () => {
  it('sends the issuer choice and the claims of each audience for a project', async () => {
    oidc = { issuer: 'inherit', audienceClaims: [{ audience: 'sts.amazonaws.com', claims: [{ name: 'team', value: 'payments' }] }], effectiveEnabled: true, issuerConfigured: true, issuerUrl: 'https://ci.example', canManage: true }
    const el = oidcSettingsCard('acme', 'p1')
    mounted = mount(el)
    await settle()
    await waitFor(() => expect(api.getOidcSettings).toHaveBeenCalledWith({ organization: 'acme', projectId: 'p1' }))
    await waitFor(() => expect(el.textContent).toContain('jobs can get tokens'))
    const issuer = el.querySelector<ComboBox>('combo-box')!
    issuer.value = 'deny'
    issuer.dispatchEvent(new Event('change', { bubbles: true }))
    const claims = el.querySelectorAll<TextField>('.audience-row text-field')[1]
    claims.value = 'team=payments, env=prod'
    buttonByText(el, 'Save OIDC settings').click()
    await waitFor(() =>
      expect(api.putOidcSettings).toHaveBeenCalledWith({
        organization: 'acme',
        projectId: 'p1',
        issuer: 'deny',
        audienceClaims: [{ audience: 'sts.amazonaws.com', claims: [{ name: 'team', value: 'payments' }, { name: 'env', value: 'prod' }] }],
      }),
    )
  })

  it('says when the coordinator has no issuer', async () => {
    oidc = { issuer: 'allow', audienceClaims: [], effectiveEnabled: false, issuerConfigured: false, canManage: false }
    const el = oidcSettingsCard('acme')
    mounted = mount(el)
    await settle()
    await waitFor(() => expect(el.textContent).toContain('no tokens'))
    expect(el.textContent).toContain('This coordinator has no OIDC issuer')
    expect(buttonByText(el, 'Save OIDC settings').hidden).toBe(true)
  })

  it('lists the signing keys and retires one that does not sign', async () => {
    keys = {
      issuerConfigured: true,
      issuer: 'https://ci.example',
      keys: [
        { kid: 'k-new', algorithm: 'ES256', createdAt: '2026-11-16T00:00:00Z', activatesAt: '2026-11-16T00:00:00Z', state: 'signing' },
        { kid: 'k-old', algorithm: 'ES256', createdAt: '2026-10-16T00:00:00Z', activatesAt: '2026-10-16T00:00:00Z', state: 'published' },
      ],
    }
    const el = oidcKeysCard()
    mounted = mount(el)
    await settle()
    await waitFor(() => expect(el.querySelectorAll('.oidc-keys-table tbody tr')).toHaveLength(2))
    const rows = el.querySelectorAll('.oidc-keys-table tbody tr')
    expect((rows[0].querySelector('.act-retire') as HTMLElement).hidden).toBe(true)
    ;(rows[1].querySelector('.act-retire') as HTMLElement).click()
    await waitFor(() => expect(api.retireOidcKey).toHaveBeenCalledWith({ kid: 'k-old' }))
    await settle()
    buttonByText(el, 'Rotate').click()
    await waitFor(() => expect(api.rotateOidcKey).toHaveBeenCalledWith({}))
  })

  it('parses claim lines', () => {
    expect(parseClaims('a=1\n b = 2 ')).toEqual({ claims: [{ name: 'a', value: '1' }, { name: 'b', value: '2' }] })
    expect(parseClaims('nope').error).toBeDefined()
  })
})

describe('project execution settings', () => {
  const project = { projectId: 'p1', infraRetries: undefined, egressControl: false } as unknown as ProjectDetail

  it('sends only what changed, and inherit as its own flag', () => {
    expect(executionRequest(project, 'inherit', 'off')).toEqual({})
    expect(executionRequest(project, '3', 'on')).toEqual({ infraRetries: 3, egressControl: true })
    expect(executionRequest({ ...project, infraRetries: 2 }, 'inherit', 'off')).toEqual({ infraRetriesInherit: true })
    expect(executionRequest({ ...project, infraRetries: 0 }, '0', 'off')).toEqual({})
  })
})

describe('labels', () => {
  it('shows advisory and quarantined, and hides required', () => {
    const el = requiredStateBadge()
    setRequiredState(el, 'required', undefined)
    expect(el.hidden).toBe(true)
    setRequiredState(el, 'advisory', 'advisory — not required')
    expect(el.hidden).toBe(false)
    expect(el.textContent).toContain('Advisory')
    expect(el.getAttribute('title')).toBe('advisory — not required')
    setRequiredState(el, 'quarantined', undefined)
    expect(el.textContent).toContain('Quarantined')
    expect(el.className).toContain('status-warn')
  })

  it('formats the actor and the owner group', () => {
    const actor = { principalId: 'p-1', kind: 'human', displayName: 'Alice', identity: 'vcs:github/alice' }
    expect(actorText(actor)).toBe('Alice (human) via vcs:github/alice')
    expect(actorShort(actor)).toBe('Alice')
    expect(actorShort({ principalId: 'p-2', kind: '', displayName: '' })).toBe('p-2')
    expect(actorText(undefined)).toBe('')
    expect(ownerGroupText({ group: 'payments' })).toBe('payments')
    expect(ownerGroupText({ subjects: ['vcs_user:github/bob'] })).toBe('vcs_user:github/bob')
    expect(ownerGroupText(undefined)).toBe('')
  })

  it('formats re-run shift limits', () => {
    expect(limitsText(undefined)).toBe('No limits')
    expect(limitsText({ maxCpu: '8', maxTimeoutSeconds: 3600 })).toBe('cpu ≤ 8, timeout ≤ 3600 s')
  })
})

describe('diagnosis delete', () => {
  function session(userId: string): SessionSummary {
    return { logged_in: true, user_id: userId, display_name: 'U', is_global_admin: false, capabilities: {} as SessionSummary['capabilities'], roles: [] }
  }
  const base: DiagnosisSummary = {
    diagnosisId: 'd-1',
    targetType: 'job',
    jobId: 'job-1',
    author: { principalId: 'user-1', kind: 'human', displayName: 'Me' },
    title: 'Mine',
    body: '',
    confidence: '',
    category: '',
    links: [],
    suggestedShiftsText: '',
    revision: 1,
    createdAt: '2026-11-16T00:00:00Z',
    updatedAt: '2026-11-16T00:00:00Z',
  }

  it('offers Delete on the entries of the signed-in person only', async () => {
    setSessionForTest(session('user-1'))
    diagnoses = [base, { ...base, diagnosisId: 'd-2', title: 'Theirs', author: { principalId: 'svc-1', kind: 'service', displayName: 'Bot' } }]
    const el = diagnosisCard()
    el.setTarget({ jobId: 'job-1' }, 'failed')
    mounted = mount(el)
    await settle()
    await waitFor(() => expect(el.querySelectorAll('.diagnosis')).toHaveLength(2))
    const buttons = [...el.querySelectorAll<HTMLElement>('.diagnosis-delete')]
    expect(buttons.map((b) => b.hidden)).toEqual([false, true])
    buttons[0].click()
    await waitFor(() => expect(api.deleteDiagnosis).toHaveBeenCalledWith({ jobId: 'job-1' }))
    expect(isOwnEntry(base, undefined)).toBe(false)
    expect(deleteTarget({ ...base, jobId: undefined, targetType: 'workflow', workflowId: 'w-1' })).toEqual({ workflowId: 'w-1' })
  })
})

describe('artifact download', () => {
  it('gives each part to the writer as it arrives, with the progress', async () => {
    const written: number[][] = []
    const progress: number[] = []
    await readArtifactParts(
      'job-1',
      { artifactId: 'a1', path: 'out/x.bin', kind: 'file', mediaType: '', sizeBytes: 5, sha256: '', status: 'complete', createdAt: '' },
      (part) => {
        written.push([...part])
      },
      (done) => progress.push(done),
    )
    expect(written).toEqual([[1, 2, 3], [4, 5]])
    expect(progress).toEqual([3, 5])
    expect(api.readJobArtifact).toHaveBeenNthCalledWith(1, { jobId: 'job-1', artifactId: 'a1', offset: 0 })
    expect(api.readJobArtifact).toHaveBeenNthCalledWith(2, { jobId: 'job-1', artifactId: 'a1', offset: 3 })
  })
})
