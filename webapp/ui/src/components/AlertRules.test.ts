import { afterEach, describe, expect, it, vi } from 'vitest'
import { waitFor } from '@testing-library/dom'
import type { ComboBox, TextField } from '@catalystcommunity/controls'
import type { ObsAlertRule } from '~/api/csilapi/types.gen.ts'
import type { SessionSummary } from '~/api/auth.ts'

const RULE: ObsAlertRule = {
  ruleId: 'r1',
  name: 'ci-pass',
  scope: 'workflow',
  projectId: 'p1',
  workflowSecurityId: 'ci',
  condition: 'pass_rate_below',
  refClassBucket: 'default',
  threshold: 0.9,
  factor: 1.5,
  windowHours: 24,
  baselineDays: 7,
  minRuns: 5,
  resolveMargin: 0.05,
  fireAfter: 2,
  resolveAfter: 2,
  state: 'firing',
  enabled: true,
  lastValue: 0.5,
  lastRuns: 10,
  canManage: true,
}
const READ_ONLY: ObsAlertRule = { ...RULE, ruleId: 'r2', name: 'other', canManage: false }

vi.mock('~/api/client.ts', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/client.ts')>()
  return {
    ...actual,
    api: {
      listAlertRules: vi.fn(async () => ({ rules: [RULE, READ_ONLY], canCreateOrgRules: true })),
      createAlertRule: vi.fn(async () => ({ rule: RULE })),
      updateAlertRule: vi.fn(async () => ({ rule: RULE })),
      deleteAlertRule: vi.fn(async () => ({ deleted: true })),
    },
  }
})
vi.mock('@catalystcommunity/containers', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@catalystcommunity/containers')>()),
  askConfirm: vi.fn(async () => true),
}))

import { api } from '~/api/client.ts'
import { setSessionForTest } from '~/lib/session.ts'
import { mount, settle } from '~/test/mount.ts'
import { alertRulesEditor, parseNumber, type AlertRulesEditor } from './AlertRules.ts'

function signedIn(): SessionSummary {
  return { logged_in: true, user_id: 'u1', display_name: 'U', is_global_admin: false, capabilities: {} as SessionSummary['capabilities'], roles: [] }
}

let current: AlertRulesEditor | null = null
afterEach(() => {
  current?.remove()
  current = null
  vi.clearAllMocks()
})

async function editor(projectId?: string): Promise<AlertRulesEditor> {
  setSessionForTest(signedIn())
  const el = (current = alertRulesEditor())
  el.setScope({ orgId: 'org-a', projectId })
  mount(el)
  await settle()
  await waitFor(() => expect(el.querySelectorAll('.alert-rules-table tbody tr').length).toBe(2))
  return el
}

function type(el: TextField, value: string): void {
  el.value = value
  el.dispatchEvent(new Event('input', { bubbles: true }))
}

function input(el: Element, cls: string): TextField {
  return el.querySelector(`.alert-rule-form .${cls}`) as TextField
}

function submit(el: Element): void {
  el.querySelector('form.alert-rule-form')!.dispatchEvent(new Event('submit', { cancelable: true }))
}

describe('alert rules editor', () => {
  it('parses number fields', () => {
    expect(parseNumber('')).toBeUndefined()
    expect(parseNumber(' 0.8 ')).toBe(0.8)
    expect(Number.isNaN(parseNumber('x')!)).toBe(true)
  })

  it('lists the rules of the project and shows Edit only for a rule the caller can change', async () => {
    const el = await editor('p1')
    expect(api.listAlertRules).toHaveBeenCalledWith({ orgId: 'org-a', projectId: 'p1' })
    const rows = el.querySelectorAll<HTMLElement>('.alert-rules-table tbody tr')
    expect(rows[0].querySelector('.alert-state')!.textContent).toBe('▲ Firing')
    expect((rows[0].querySelector('.alert-edit') as HTMLElement).hidden).toBe(false)
    expect((rows[1].querySelector('.alert-edit') as HTMLElement).hidden).toBe(true)
    expect((rows[1].querySelector('.alert-delete') as HTMLElement).hidden).toBe(true)
    // An org admin may make owner group rules.
    const scope = el.querySelector('.alert-rule-form combo-box') as ComboBox
    expect(scope.options.map((o) => (o as { value: string }).value)).toEqual(['workflow', 'project', 'owner_group'])
  })

  it('creates a workflow rule with the exact request, and a refresh keeps the draft', async () => {
    const el = await editor('p1')
    const fields = [...el.querySelectorAll<TextField>('.alert-rule-form text-field')]
    const [name, project, workflow] = fields
    expect(project.value, 'the project of the view').toBe('p1')
    type(name, 'ci-pass-rate')
    type(workflow, 'ci')
    type(input(el, 'alert-threshold'), '0.8')
    type(input(el, 'alert-windowHours'), '48')
    // A refresh of the list keeps the typed values and the elements.
    await (el as unknown as { invalidate(): void }).invalidate()
    await settle()
    expect(name.value).toBe('ci-pass-rate')
    expect(el.querySelector('.alert-rule-form text-field')).toBe(name)
    submit(el)
    await waitFor(() =>
      expect(api.createAlertRule).toHaveBeenCalledWith({
        orgId: 'org-a',
        name: 'ci-pass-rate',
        scope: 'workflow',
        projectId: 'p1',
        workflowSecurityId: 'ci',
        ownerGroup: undefined,
        rule: { description: '', condition: 'pass_rate_below', refClassBucket: 'default', enabled: true, threshold: 0.8, windowHours: 48 },
      }),
    )
    await waitFor(() => expect(name.value).toBe(''))
    expect(api.listAlertRules).toHaveBeenCalledTimes(2)
  })

  it('refuses a threshold outside 0 to 1', async () => {
    const el = await editor('p1')
    const [name, , workflow] = [...el.querySelectorAll<TextField>('.alert-rule-form text-field')]
    type(name, 'x')
    type(workflow, 'ci')
    type(input(el, 'alert-threshold'), '90')
    await settle()
    expect(el.textContent).toContain('Give a value from 0 to 1.')
    submit(el)
    await settle()
    expect(api.createAlertRule).not.toHaveBeenCalled()
  })

  it('loads a rule into the form one time, and saves it with update-alert-rule', async () => {
    const el = await editor()
    ;(el.querySelector('.alert-edit') as HTMLElement).click()
    await settle()
    const name = el.querySelector('.alert-rule-form text-field') as TextField
    expect(name.value).toBe('ci-pass')
    expect(name.disabled, 'the scope of a rule cannot change').toBe(true)
    const threshold = input(el, 'alert-threshold')
    expect(threshold.value).toBe('0.9')
    type(threshold, '0.95')
    await settle()
    expect(threshold.value).toBe('0.95')
    submit(el)
    await waitFor(() =>
      expect(api.updateAlertRule).toHaveBeenCalledWith({
        orgId: 'org-a',
        ruleId: 'r1',
        rule: {
          description: '',
          condition: 'pass_rate_below',
          refClassBucket: 'default',
          enabled: true,
          threshold: 0.95,
          windowHours: 24,
          minRuns: 5,
          resolveMargin: 0.05,
          fireAfter: 2,
          resolveAfter: 2,
        },
      }),
    )
  })

  it('deletes a rule after a confirmation', async () => {
    const el = await editor()
    ;(el.querySelector('.alert-delete') as HTMLElement).click()
    await waitFor(() => expect(api.deleteAlertRule).toHaveBeenCalledWith({ orgId: 'org-a', ruleId: 'r1' }))
  })
})
