import type { ComboBox, PushButton, TextArea, TextField } from '@catalystcommunity/controls'
import { api } from '~/api/client.ts'
import type {
  AgreementRow,
  GetAgreementReportResponse,
  GetReportingSettingsResponse,
  ListPathAcceptancesResponse,
  PathAcceptanceSummary,
} from '~/api/csilapi/types.gen.ts'
import { field } from '~/components/Field.ts'
import { Action, ActionError } from '~/components/ManagementPage.ts'
import { ProjectCard } from '~/components/ProjectCard.ts'
import { relativeTime } from '~/components/States.ts'
import { button, comboBox, setComboValue, setFieldValue, textArea, textField } from '~/ui/controls.ts'
import { h, setText, show, syncKeyed } from '~/ui/dom.ts'
import { define } from '~/ui/element.ts'
import { Loader } from '~/ui/load.ts'
import { ChoiceSelect } from '~/components/ChoiceInput.ts'
import { PathOwnersEditor } from '~/components/PathOwners.ts'

/**
 * Reporting and ownership of a project (docs/reporting-and-ownership.md):
 * the report mode, the agreement report with another CI system, the code
 * owners import, and owner acceptance of path dependencies.
 */

export const REPORT_MODES = [
  { value: 'full', label: 'Full: statuses and the pull request comment' },
  { value: 'statuses_only', label: 'Statuses only: no pull request comment' },
  { value: 'silent', label: 'Silent: nothing in the VCS, results in Reactorcide only' },
]

const CODEOWNERS = [
  { value: 'off', label: 'Off: VCS native code owners only' },
  { value: 'on', label: 'On: import the code owners file into required reviews' },
]

const OWNERS_MODES = [
  { value: 'inherit', label: 'Use the organization registry', description: 'The path owner rules of the organization.' },
  { value: 'set', label: 'Set for this project', description: 'Use the rules below for this project only.' },
]

const PATH_ACCEPTANCE = [
  { value: 'off', label: 'Off: any workflow can claim any path' },
  { value: 'warn', label: 'Warn: flag path matches with no owner acceptance' },
  { value: 'enforce', label: 'Enforce: drop path matches with no owner acceptance' },
]

/** "97.5 %" from basis points, or "—". */
export function agreementRateText(points: number | undefined): string {
  if (points === undefined || points === null) return '—'
  return `${(points / 100).toFixed(1)} %`
}

/** One line for an agreement row: the two results. */
export function agreementRowText(row: AgreementRow): string {
  const other = row.otherResult || 'no result'
  const pr = row.prNumber ? ` · PR #${row.prNumber}` : ''
  return `Reactorcide ${row.reactorcideResult}, other CI ${other}${pr}`
}

/** The source label of a setting: "(inherited)" unless the project sets it. */
export function sourceText(source: string): string {
  return source === 'project' ? '' : source === 'org' ? ' (from the organization)' : ' (default)'
}

/** Splits the agreement contexts text: one name on each line. */
export function parseContexts(text: string): string[] {
  return text
    .split('\n')
    .map((line) => line.trim())
    .filter((line) => line !== '')
}

type AcceptanceRow = HTMLTableRowElement & { data?: PathAcceptanceSummary }
type DisagreementRow = HTMLLIElement & { data?: AgreementRow }

interface ReportingData {
  settings: GetReportingSettingsResponse
  agreement: GetAgreementReportResponse | null
  acceptances: ListPathAcceptancesResponse | null
}

class ReportingCard extends ProjectCard {
  #load = new Loader<string, ReportingData>(this, async (projectId) => {
    const settings = await api.getReportingSettings({ projectId })
    const [agreement, acceptances] = await Promise.all([
      api.getAgreementReport({ projectId, since: '30d' }).catch(() => null),
      api.listPathAcceptances({ projectId }).catch(() => null),
    ])
    return { settings, agreement, acceptances }
  })
  #action = new Action(() => this.invalidate())
  #error = new ActionError()
  #loading!: HTMLParagraphElement
  #body!: HTMLDivElement
  #summary!: HTMLParagraphElement
  #mode!: ComboBox
  #contexts!: TextArea
  #codeowners!: ComboBox
  #pathAcceptance!: ComboBox
  #save!: PushButton
  #seeded = ''
  #ownersMode!: ChoiceSelect
  #ownersModeValue = 'inherit'
  #owners!: PathOwnersEditor
  #ownersBox!: HTMLDivElement
  #orgOwners!: HTMLParagraphElement
  #agreementPanel!: HTMLDivElement
  #rate!: HTMLParagraphElement
  #disagreements!: HTMLUListElement
  #noDisagreements!: HTMLParagraphElement
  #acceptances!: HTMLTableSectionElement
  #acceptanceMode!: HTMLParagraphElement
  #requestWorkflow!: TextField
  #requestPattern!: TextField
  #requestOwner!: TextField
  #requestReason!: TextField
  #request!: PushButton

  protected build(): void {
    this.#loading = h('p', { class: 'meta' }, 'Loading…')
    this.#summary = h('p', { class: 'meta reporting-summary' })
    this.#mode = comboBox({ ariaLabel: 'Report mode', options: REPORT_MODES, value: 'full' })
    this.#contexts = textArea({ rows: 3, placeholder: 'One status context or check name on each line, for example ci/build' })
    this.#codeowners = comboBox({ ariaLabel: 'Code owners import', options: CODEOWNERS, value: 'off' })
    this.#pathAcceptance = comboBox({ ariaLabel: 'Path acceptance', options: PATH_ACCEPTANCE, value: 'off' })
    this.#save = button('Save', { variant: 'primary', onClick: () => void this.#saveSettings() })
    this.#mode.classList.add('rp-mode')
    this.#contexts.classList.add('rp-contexts')
    this.#codeowners.classList.add('rp-codeowners')
    this.#pathAcceptance.classList.add('rp-path-acceptance')

    this.#ownersMode = new ChoiceSelect({ ariaLabel: 'Path owner registry', choices: OWNERS_MODES, value: 'inherit', onChange: (v) => { this.#ownersModeValue = v; this.invalidate() } })
    this.#owners = new PathOwnersEditor(() => this.invalidate())
    this.#ownersBox = h('div', { hidden: true }, this.#owners.el)
    this.#orgOwners = h('p', { class: 'meta' })

    this.#rate = h('p', { class: 'agreement-rate' })
    this.#disagreements = h('ul', { class: 'agreement-disagreements' })
    this.#noDisagreements = h('p', { class: 'empty', hidden: true }, 'No disagreement.')
    this.#agreementPanel = h(
      'div',
      { class: 'agreement-panel', hidden: true },
      h('h4', { class: 'section-title' }, 'Agreement with the other CI (last 30 days)'),
      this.#rate,
      this.#noDisagreements,
      this.#disagreements,
    )

    this.#acceptanceMode = h('p', { class: 'meta' })
    this.#acceptances = h('tbody')
    this.#requestWorkflow = textField({ placeholder: 'build', mono: true })
    this.#requestPattern = textField({ placeholder: '/payments/', mono: true })
    this.#requestOwner = textField({ placeholder: 'vcs_team:acme/payments', mono: true })
    this.#requestReason = textField({ placeholder: 'Why the workflow needs these paths' })
    this.#request = button('Request acceptance', { onClick: () => void this.#requestAcceptance() })

    this.#body = h(
      'div',
      { hidden: true },
      this.#summary,
      h(
        'div',
        { class: 'reporting-form' },
        field({ label: 'Report mode', hint: 'Use silent or statuses only to run next to another CI system.', control: this.#mode }).el,
        field({ label: 'Agreement contexts', hint: 'The commit statuses and check names of the other CI system. Empty: no agreement report.', control: this.#contexts }).el,
        field({ label: 'Code owners import', hint: 'Reads the code owners file of the trusted base commit. An org admin changes it.', control: this.#codeowners }).el,
        field({ label: 'Path acceptance', hint: 'Owners accept path dependencies of other workflows. An org admin changes it.', control: this.#pathAcceptance }).el,
        field({ label: 'Path owner registry', hint: 'The owners of each path when the code owners import is off. The last matching rule wins. An org admin changes it.', control: this.#ownersMode.el }).el,
        this.#orgOwners,
        this.#ownersBox,
        h('div', { class: 'row' }, this.#save),
      ),
      this.#agreementPanel,
      h('h4', { class: 'section-title' }, 'Path acceptances'),
      this.#acceptanceMode,
      h(
        'div',
        { class: 'scroll-x' },
        h(
          'table',
          { class: 'table path-acceptances-table' },
          h('thead', null, h('tr', null, h('th', null, 'Workflow'), h('th', null, 'Pattern'), h('th', null, 'Owner'), h('th', null, 'State'), h('th', null, 'Who'), h('th', null, h('span', { class: 'sr-only' }, 'Actions')))),
          this.#acceptances,
        ),
      ),
      h(
        'div',
        { class: 'path-acceptance-form' },
        field({ label: 'Workflow ID', required: true, control: this.#requestWorkflow }).el,
        field({ label: 'Path pattern', required: true, hint: 'Code owners syntax.', control: this.#requestPattern }).el,
        field({ label: 'Owner', required: true, hint: 'The owner subject of the area.', control: this.#requestOwner }).el,
        field({ label: 'Reason', control: this.#requestReason }).el,
        h('div', { class: 'row' }, this.#request),
      ),
    )
    this.append(
      h('h3', { class: 'section-title' }, 'Reporting and ownership'),
      h('p', { class: 'meta' }, 'What Reactorcide writes to the VCS, how it compares with another CI system, and who owns which paths.'),
      this.#error.el,
      this.#loading,
      this.#body,
    )
  }

  async #reload(): Promise<void> {
    if (this.isActive()) await this.#load.reload()
  }

  async #saveSettings(): Promise<void> {
    const data = this.#load.state.data
    if (!data || !this.project) return
    const projectId = this.project.projectId
    const request: Parameters<typeof api.updateReportingSettings>[0] = { projectId }
    const e = data.settings.effective
    if (data.settings.canManageReporting) {
      if (this.#mode.value !== e.reportMode) request.reportMode = this.#mode.value
      const contexts = parseContexts(this.#contexts.value)
      if (contexts.join('\n') !== e.agreementContexts.join('\n')) request.agreementContexts = contexts
    }
    if (data.settings.canManageOwnership) {
      if (this.#codeowners.value !== e.codeownersImport) request.codeownersImport = this.#codeowners.value
      if (this.#pathAcceptance.value !== e.pathAcceptance) request.pathAcceptance = this.#pathAcceptance.value
      const stored = data.settings.projectPathOwners
      if (this.#ownersModeValue === 'set') {
        this.#owners.touched = true
        const problem = this.#owners.error()
        if (problem) {
          this.#action.error = problem
          this.invalidate()
          return
        }
        const owners = this.#owners.values()
        if (JSON.stringify(owners) !== JSON.stringify(stored ?? null)) request.pathOwners = owners
      } else if (stored !== undefined) {
        request.inherit = ['path_owners']
      }
    }
    if (Object.keys(request).length === 1) {
      this.#error.set('Nothing changed.')
      return
    }
    if (await this.#action.run('The settings could not be saved.', () => api.updateReportingSettings(request))) {
      this.#seeded = ''
      await this.#reload()
    }
  }

  async #requestAcceptance(): Promise<void> {
    if (!this.project) return
    const workflowSecurityId = this.#requestWorkflow.value.trim()
    const pathPattern = this.#requestPattern.value.trim()
    const ownerSubject = this.#requestOwner.value.trim()
    if (!workflowSecurityId || !pathPattern || !ownerSubject) {
      this.#error.set('Give the workflow ID, the path pattern, and the owner.')
      return
    }
    const projectId = this.project.projectId
    const ok = await this.#action.run('The request could not be stored.', () =>
      api.requestPathAcceptance({ projectId, workflowSecurityId, pathPattern, ownerSubject, reason: this.#requestReason.value.trim() || undefined }),
    )
    if (ok) {
      this.#requestWorkflow.value = ''
      this.#requestPattern.value = ''
      await this.#reload()
    }
  }

  async #decide(row: PathAcceptanceSummary, decision: 'accept' | 'reject' | 'revoke'): Promise<void> {
    if (!this.project) return
    const projectId = this.project.projectId
    if (await this.#action.run(`The acceptance could not be changed (${decision}).`, () => api.decidePathAcceptance({ projectId, acceptanceId: row.acceptanceId, decision }))) {
      await this.#reload()
    }
  }

  #createAcceptanceRow(): AcceptanceRow {
    const tr = h('tr') as AcceptanceRow
    const current = (): PathAcceptanceSummary => tr.data!
    const accept = button('Accept', { small: true, variant: 'primary', onClick: () => void this.#decide(current(), 'accept') })
    accept.classList.add('act-accept')
    const reject = button('Reject', { small: true, onClick: () => void this.#decide(current(), 'reject') })
    reject.classList.add('act-reject')
    const revoke = button('Revoke', { small: true, onClick: () => void this.#decide(current(), 'revoke') })
    revoke.classList.add('act-revoke')
    tr.append(
      h('td', { class: 'mono pa-workflow' }),
      h('td', { class: 'mono pa-pattern' }),
      h('td', { class: 'mono pa-owner' }),
      h('td', { class: 'pa-state' }),
      h('td', { class: 'pa-who' }),
      h('td', { class: 'row' }, accept, reject, revoke),
    )
    return tr
  }

  #updateAcceptanceRow(tr: AcceptanceRow, row: PathAcceptanceSummary): void {
    tr.data = row
    setText(tr.querySelector('.pa-workflow')!, row.workflowSecurityId)
    setText(tr.querySelector('.pa-pattern')!, row.pathPattern)
    setText(tr.querySelector('.pa-owner')!, row.ownerSubject)
    setText(tr.querySelector('.pa-state')!, row.state)
    const who = row.state === 'accepted' ? `${row.acceptedByName ?? row.acceptedBy ?? '—'} · ${relativeTime(row.acceptedAt)}` : `${row.requestedByName ?? row.requestedBy ?? '—'} · ${relativeTime(row.requestedAt)}`
    setText(tr.querySelector('.pa-who')!, who)
    // The coordinator decides who may act; the buttons show for open rows.
    show(tr.querySelector('.act-accept')!, row.state === 'requested')
    show(tr.querySelector('.act-reject')!, row.state === 'requested')
    show(tr.querySelector('.act-revoke')!, row.state === 'requested' || row.state === 'accepted')
  }

  #createDisagreement(): DisagreementRow {
    const li = h('li') as DisagreementRow
    li.append(h('a', { class: 'mono ag-commit', target: '_blank', rel: 'noopener noreferrer' }), h('span', { class: 'ag-text' }))
    return li
  }

  #updateDisagreement(li: DisagreementRow, row: AgreementRow): void {
    li.data = row
    const link = li.querySelector<HTMLAnchorElement>('.ag-commit')!
    setText(link, row.commitSha.slice(0, 12))
    if (row.commitUrl) link.href = row.commitUrl
    else link.removeAttribute('href')
    setText(li.querySelector('.ag-text')!, ` — ${agreementRowText(row)}`)
  }

  #seed(settings: GetReportingSettingsResponse): void {
    // Seed the form one time for each stored state, so a refresh does not
    // throw away a draft.
    const e = settings.effective
    const key = JSON.stringify([e.reportMode, e.agreementContexts, e.codeownersImport, e.pathAcceptance, settings.projectPathOwners ?? null])
    if (key === this.#seeded) return
    this.#seeded = key
    this.#ownersModeValue = settings.projectPathOwners !== undefined ? 'set' : 'inherit'
    this.#owners.set(settings.projectPathOwners ?? e.pathOwners)
    setComboValue(this.#mode, e.reportMode)
    setFieldValue(this.#contexts, e.agreementContexts.join('\n'))
    setComboValue(this.#codeowners, e.codeownersImport)
    setComboValue(this.#pathAcceptance, e.pathAcceptance)
  }

  render(): void {
    this.#load.sync(this.project?.projectId)
    const data = this.#load.state.data
    this.#error.set(this.#action.error ?? this.#load.state.error?.message)
    show(this.#loading, !data && !this.#load.state.error)
    show(this.#body, data)
    if (!data) return
    const s = data.settings
    const e = s.effective
    this.#seed(s)
    setText(
      this.#summary,
      `Report mode ${e.reportMode}${sourceText(e.reportModeSource)}. Code owners import ${e.codeownersImport}${sourceText(e.codeownersImportSource)}. Path acceptance ${e.pathAcceptance}${sourceText(e.pathAcceptanceSource)}.`,
    )
    this.#mode.disabled = !s.canManageReporting || this.#action.busy
    this.#contexts.disabled = !s.canManageReporting || this.#action.busy
    this.#codeowners.disabled = !s.canManageOwnership || this.#action.busy
    this.#pathAcceptance.disabled = !s.canManageOwnership || this.#action.busy
    this.#ownersMode.update(OWNERS_MODES, this.#ownersModeValue)
    this.#ownersMode.el.disabled = !s.canManageOwnership || this.#action.busy
    this.#owners.setDisabled(!s.canManageOwnership || this.#action.busy)
    show(this.#ownersBox, this.#ownersModeValue === 'set')
    this.#owners.render()
    const ownerRules = e.pathOwners.map((rule) => `${rule.pattern} → ${rule.owners.join(', ')}`).join('; ')
    setText(this.#orgOwners, `In force now${sourceText(e.pathOwnersSource)}: ${ownerRules || 'no rules'}.`)
    show(this.#save, s.canManageReporting || s.canManageOwnership)
    this.#save.disabled = this.#action.busy

    const agreement = data.agreement
    show(this.#agreementPanel, agreement !== null && agreement.contexts.length > 0)
    if (agreement) {
      setText(
        this.#rate,
        `Agreement ${agreementRateText(agreement.rateBasisPoints)}: ${agreement.agreed} of ${agreement.compared} commits. Reactorcide failed and the other CI passed: ${agreement.reactorcideFailedOtherPassed}. Reactorcide passed and the other CI failed: ${agreement.reactorcidePassedOtherFailed}. Waiting: ${agreement.pending}. No other result: ${agreement.noOther}.`,
      )
      show(this.#noDisagreements, agreement.disagreements.length === 0)
      syncKeyed(this.#disagreements, agreement.disagreements, (row) => row.commitSha, () => this.#createDisagreement(), (li, row) => this.#updateDisagreement(li, row))
    }

    const acceptances = data.acceptances
    const rows = acceptances?.rows.filter((row) => row.state === 'requested' || row.state === 'accepted') ?? []
    setText(this.#acceptanceMode, acceptances ? `Path acceptance is ${acceptances.pathAcceptance}. Owners come from the ${acceptances.ownershipSource === 'codeowners' ? 'code owners file' : 'path owner registry'}.` : '')
    syncKeyed(this.#acceptances, rows, (row) => row.acceptanceId, () => this.#createAcceptanceRow(), (tr, row) => this.#updateAcceptanceRow(tr, row))
    this.#request.disabled = this.#action.busy
  }
}

export const reportingCardTag = 'rc-project-reporting-card'
define(reportingCardTag, ReportingCard)
export type { ReportingCard }
