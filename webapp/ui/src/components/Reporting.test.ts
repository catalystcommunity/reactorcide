import { afterEach, describe, expect, it, vi } from 'vitest'
import { waitFor } from '@testing-library/dom'
import type { ComboBox, PushButton, TextArea } from '@catalystcommunity/controls'
import type {
  GetAgreementReportResponse,
  GetReportingSettingsResponse,
  ListPathAcceptancesResponse,
  ProjectDetail,
} from '~/api/csilapi/types.gen.ts'

let settings: GetReportingSettingsResponse
let agreement: GetAgreementReportResponse
let acceptances: ListPathAcceptancesResponse

function baseSettings(overrides: Partial<GetReportingSettingsResponse> = {}): GetReportingSettingsResponse {
  return {
    effective: {
      reportMode: 'silent',
      reportModeSource: 'project',
      agreementContexts: ['ci/build'],
      agreementContextsSource: 'project',
      codeownersImport: 'off',
      codeownersImportSource: 'default',
      codeownersCacheTtlSeconds: 600,
      pathAcceptance: 'warn',
      pathAcceptanceSource: 'org',
      pathOwners: [],
      pathOwnersSource: 'default',
    },
    projectReportMode: 'silent',
    canManageReporting: true,
    canManageOwnership: true,
    ...overrides,
  }
}

function baseAgreement(): GetAgreementReportResponse {
  return {
    contexts: ['ci/build'],
    total: 4,
    compared: 3,
    agreed: 2,
    disagreed: 1,
    pending: 1,
    noOther: 0,
    rateBasisPoints: 6667,
    reactorcideFailedOtherPassed: 1,
    reactorcidePassedOtherFailed: 0,
    disagreements: [
      {
        commitSha: 'abcdef1234567890',
        prNumber: 7,
        reactorcideResult: 'failure',
        otherResult: 'success',
        agree: false,
        status: 'complete',
        otherResults: [],
        reactorcideCompletedAt: '2026-11-15T00:00:00Z',
        commitUrl: 'https://github.com/acme/app/commit/abcdef1234567890',
      },
    ],
    rows: [],
  }
}

function baseAcceptances(): ListPathAcceptancesResponse {
  return {
    pathAcceptance: 'warn',
    ownershipSource: 'registry',
    canManageOwnership: true,
    rows: [
      { acceptanceId: 'a-1', workflowSecurityId: 'build', pathPattern: '/payments/', ownerSubject: 'reactorcide_group:payments', state: 'requested', requestedByName: 'bob', requestedAt: '2026-11-15T00:00:00Z' },
      { acceptanceId: 'a-2', workflowSecurityId: 'lint', pathPattern: '/docs/', ownerSubject: 'reactorcide_group:docs', state: 'accepted', acceptedByName: 'alice', acceptedAt: '2026-11-15T00:00:00Z' },
      { acceptanceId: 'a-3', workflowSecurityId: 'old', pathPattern: '/x/', ownerSubject: 'reactorcide_group:x', state: 'revoked' },
    ],
  }
}

vi.mock('~/api/client.ts', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/client.ts')>()
  return {
    ...actual,
    api: {
      getReportingSettings: vi.fn(async () => settings),
      updateReportingSettings: vi.fn(async () => settings),
      getAgreementReport: vi.fn(async () => agreement),
      listPathAcceptances: vi.fn(async () => acceptances),
      requestPathAcceptance: vi.fn(async () => ({ acceptance: acceptances.rows[0] })),
      decidePathAcceptance: vi.fn(async () => ({ acceptance: acceptances.rows[0] })),
    },
  }
})

import { api } from '~/api/client.ts'
import { makeCard, type ProjectCard } from '~/components/ProjectCard.ts'
import { mount, settle } from '~/test/mount.ts'
import { agreementRateText, parseContexts, reportingCardTag, sourceText } from './Reporting.ts'

async function card(): Promise<ProjectCard> {
  const el = makeCard<ProjectCard>(reportingCardTag, {})
  el.setProject({ projectId: 'p1', name: 'Widget', enabled: true } as unknown as ProjectDetail)
  mount(el)
  await settle()
  await waitFor(() => expect(el.textContent).toContain('Report mode silent'))
  return el
}

function buttonNamed(root: Element, text: string, within?: Element): PushButton {
  const scope = within ?? root
  const match = [...scope.querySelectorAll('push-button')].find((b) => b.textContent === text && !b.closest('[hidden]'))
  if (!match) throw new Error(`no visible button ${text}`)
  return match as PushButton
}

afterEach(() => {
  vi.clearAllMocks()
})

describe('reporting helpers', () => {
  it('formats the rate and the sources', () => {
    expect(agreementRateText(9750)).toBe('97.5 %')
    expect(agreementRateText(undefined)).toBe('—')
    expect(sourceText('project')).toBe('')
    expect(sourceText('org')).toBe(' (from the organization)')
    expect(sourceText('default')).toBe(' (default)')
    expect(parseContexts(' ci/build \n\n lint\n')).toEqual(['ci/build', 'lint'])
  })
})

describe('reporting card', () => {
  it('shows the settings, the agreement summary, and the open acceptances', async () => {
    settings = baseSettings()
    agreement = baseAgreement()
    acceptances = baseAcceptances()
    const el = await card()
    expect(el.textContent).toContain('Path acceptance warn (from the organization)')
    expect(el.textContent).toContain('Agreement 66.7 %: 2 of 3 commits')
    const link = el.querySelector<HTMLAnchorElement>('.ag-commit')!
    expect(link.textContent).toBe('abcdef123456')
    expect(link.href).toBe('https://github.com/acme/app/commit/abcdef1234567890')
    const rows = el.querySelectorAll('.path-acceptances-table tbody tr')
    expect(rows.length).toBe(2)
    expect(rows[0].querySelector('.pa-workflow')?.textContent).toBe('build')
    expect(rows[0].querySelector('.act-accept')?.closest('[hidden]')).toBeNull()
    expect(rows[1].querySelector<HTMLElement>('.act-accept')!.hidden).toBe(true)
    expect(rows[1].querySelector<HTMLElement>('.act-revoke')!.hidden).toBe(false)
  })

  it('saves only the changed values', async () => {
    settings = baseSettings()
    agreement = baseAgreement()
    acceptances = baseAcceptances()
    const el = await card()
    const mode = el.querySelector<ComboBox>('.rp-mode')!
    mode.value = 'full'
    const contexts = el.querySelector<TextArea>('.rp-contexts')!
    contexts.value = 'ci/build\nci/test'
    buttonNamed(el, 'Save').click()
    await waitFor(() => expect(api.updateReportingSettings).toHaveBeenCalled())
    expect(api.updateReportingSettings).toHaveBeenCalledWith({ projectId: 'p1', reportMode: 'full', agreementContexts: ['ci/build', 'ci/test'] })
  })

  it('accepts a requested row', async () => {
    settings = baseSettings()
    agreement = baseAgreement()
    acceptances = baseAcceptances()
    const el = await card()
    const row = el.querySelector('.path-acceptances-table tbody tr')!
    buttonNamed(el, 'Accept', row).click()
    await waitFor(() => expect(api.decidePathAcceptance).toHaveBeenCalledWith({ projectId: 'p1', acceptanceId: 'a-1', decision: 'accept' }))
  })

  it('hides the agreement panel with no contexts and disables fields without permission', async () => {
    settings = baseSettings({ canManageReporting: false, canManageOwnership: false })
    settings.effective.agreementContexts = []
    agreement = { ...baseAgreement(), contexts: [] }
    acceptances = baseAcceptances()
    const el = await card()
    expect(el.querySelector<HTMLElement>('.agreement-panel')!.hidden).toBe(true)
    expect(el.querySelector<ComboBox>('.rp-mode')!.disabled).toBe(true)
    const save = [...el.querySelectorAll<HTMLElement>('push-button')].find((b) => b.textContent === 'Save')!
    expect(save.hidden).toBe(true)
  })
})
