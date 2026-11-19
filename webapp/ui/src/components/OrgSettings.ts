import type { ComboBox, PushButton, TextArea, TextField } from '@catalystcommunity/controls'
import { api } from '~/api/client.ts'
import type { GetOrgReportingSettingsResponse, GetRequiredSetOrgSettingsResponse, OrgReportingSettingsView, RequiredSetApproverRule } from '~/api/csilapi/types.gen.ts'
import { ChipList, subjectError, subjectHint } from '~/components/ChipList.ts'
import { field } from '~/components/Field.ts'
import { Action, ActionError } from '~/components/ManagementPage.ts'
import { PathOwnersEditor } from '~/components/PathOwners.ts'
import { REPORT_MODES, parseContexts } from '~/components/Reporting.ts'
import { button, comboBox, setComboValue, textArea, textField } from '~/ui/controls.ts'
import { h, setText, show, syncKeyed, uid } from '~/ui/dom.ts'
import { RcElement, define } from '~/ui/element.ts'
import { Loader } from '~/ui/load.ts'
import './chips.css'

/**
 * Organization defaults that projects follow: the reporting defaults
 * (docs/reporting-and-ownership.md) and the required set defaults
 * (docs/required-set.md). Each card names the org by NAME, loads one time for
 * each org, and seeds its form one time for each stored state, so a refresh
 * keeps a draft. The coordinator decides each change (an org admin).
 */

const CODEOWNERS = [
  { value: 'off', label: 'Off: use the path owner registry below' },
  { value: 'on', label: 'On: import the code owners file of the trusted base commit' },
]

const PATH_ACCEPTANCE = [
  { value: 'off', label: 'Off: any workflow can claim any path' },
  { value: 'warn', label: 'Warn: flag path matches with no owner acceptance' },
  { value: 'enforce', label: 'Enforce: drop path matches with no owner acceptance' },
]

/** A base for an org card: an org name scope and a keyed load. */
abstract class OrgCard<T> extends RcElement {
  organization = ''
  protected abstract readonly load: Loader<string, T>

  setScope(organization: string): void {
    if (this.organization === organization) return
    this.organization = organization
    this.invalidate()
  }

  connectedCallback(): void {
    this.classList.add('card')
    super.connectedCallback()
  }
}

export class OrgReportingCard extends OrgCard<GetOrgReportingSettingsResponse> {
  protected readonly load = new Loader<string, GetOrgReportingSettingsResponse>(this, (organization) => api.getOrgReportingSettings({ organization }))
  #action = new Action(() => this.invalidate())
  #error = new ActionError()
  #loading!: HTMLParagraphElement
  #body!: HTMLDivElement
  #readonly!: HTMLParagraphElement
  #mode!: ComboBox
  #contexts!: TextArea
  #codeowners!: ComboBox
  #ttl!: TextField
  #pathAcceptance!: ComboBox
  #owners!: PathOwnersEditor
  #save!: PushButton
  #saved!: HTMLDivElement
  #seeded = ''

  protected build(): void {
    this.#loading = h('p', { class: 'meta' }, 'Loading…')
    this.#readonly = h('p', { class: 'meta', hidden: true }, 'Only an organization administrator can change these settings.')
    this.#mode = comboBox({ ariaLabel: 'Report mode', options: REPORT_MODES, value: 'full' })
    this.#contexts = textArea({ rows: 3, placeholder: 'One status context or check name on each line, for example ci/build' })
    this.#codeowners = comboBox({ ariaLabel: 'Code owners import', options: CODEOWNERS, value: 'off' })
    this.#ttl = textField({ mono: true, placeholder: '600' })
    this.#pathAcceptance = comboBox({ ariaLabel: 'Path acceptance', options: PATH_ACCEPTANCE, value: 'off' })
    this.#owners = new PathOwnersEditor(() => this.invalidate())
    this.#save = button('Save reporting defaults', { variant: 'primary', onClick: () => void this.#store() })
    this.#saved = h('div', { class: 'alert alert-info', role: 'status', hidden: true }, 'Reporting defaults saved.')
    this.#body = h(
      'div',
      { hidden: true },
      this.#readonly,
      this.#saved,
      h(
        'div',
        { class: 'reporting-form' },
        field({ label: 'Report mode', hint: 'What Reactorcide writes to the VCS. Silent or statuses only runs next to another CI system.', control: this.#mode }).el,
        field({ label: 'Agreement contexts', hint: 'The commit statuses and check names of the other CI system. Empty: no agreement report.', control: this.#contexts }).el,
        field({ label: 'Code owners import', hint: 'Import the code owners file into the required reviews and the path owners.', control: this.#codeowners }).el,
        field({ label: 'Code owners cache (seconds)', hint: 'How long a read code owners file is kept. From 10 to 86400.', control: this.#ttl }).el,
        field({ label: 'Path acceptance', hint: 'Owners accept path dependencies of workflows that they do not own.', control: this.#pathAcceptance }).el,
      ),
      h('h4', { class: 'section-title' }, 'Path owner registry'),
      h('p', { class: 'meta' }, 'The owners of each path when the code owners import is off. The last rule that matches a path wins. A project can set its own registry.'),
      this.#owners.el,
      h('div', { class: 'row' }, this.#save),
    )
    this.append(
      h('h3', { class: 'section-title' }, 'Reporting defaults'),
      h('p', { class: 'meta' }, 'Projects follow these values unless they set their own on the project page.'),
      this.#error.el,
      this.#loading,
      this.#body,
    )
  }

  /** The settings the form sends (a full replace). */
  settings(): OrgReportingSettingsView {
    const ttl = this.#ttl.value.trim()
    return {
      reportMode: this.#mode.value,
      agreementContexts: parseContexts(this.#contexts.value),
      codeownersImport: this.#codeowners.value,
      codeownersCacheTtlSeconds: ttl ? Number(ttl) : 0,
      pathAcceptance: this.#pathAcceptance.value,
      pathOwners: this.#owners.values(),
    }
  }

  async #store(): Promise<void> {
    show(this.#saved, false)
    this.#owners.touched = true
    const ttl = this.#ttl.value.trim()
    const problem = this.#owners.error() ?? (ttl && !/^\d+$/.test(ttl) ? 'The code owners cache is a number of seconds.' : undefined)
    if (problem) {
      this.#action.error = problem
      this.invalidate()
      return
    }
    const organization = this.organization
    let response: GetOrgReportingSettingsResponse | undefined
    const ok = await this.#action.run('The reporting defaults could not be saved.', async () => {
      response = await api.putOrgReportingSettings({ organization, settings: this.settings() })
    })
    if (!ok || !response || !this.isActive()) return
    this.#seeded = ''
    this.load.mutate(response)
    show(this.#saved, true)
  }

  #seed(data: GetOrgReportingSettingsResponse): void {
    const key = JSON.stringify([this.organization, data.settings])
    if (key === this.#seeded) return
    this.#seeded = key
    const s = data.settings
    setComboValue(this.#mode, s.reportMode)
    this.#contexts.value = s.agreementContexts.join('\n')
    setComboValue(this.#codeowners, s.codeownersImport)
    this.#ttl.value = String(s.codeownersCacheTtlSeconds)
    setComboValue(this.#pathAcceptance, s.pathAcceptance)
    this.#owners.set(s.pathOwners)
  }

  render(): void {
    this.load.sync(this.organization || undefined)
    const data = this.load.state.data
    this.#error.set(this.#action.error ?? this.load.state.error?.message)
    show(this.#loading, !data && !this.load.state.error)
    show(this.#body, data)
    if (!data) return
    this.#seed(data)
    const disabled = !data.canManage || this.#action.busy
    for (const control of [this.#mode, this.#contexts, this.#codeowners, this.#ttl, this.#pathAcceptance]) control.disabled = disabled
    this.#owners.setDisabled(disabled)
    show(this.#readonly, !data.canManage)
    show(this.#save, data.canManage)
    this.#save.disabled = this.#action.busy
    this.#owners.render()
  }
}

/** One approver rule: branch globs and the approvers of those branches. */
class ApproverRuleRow {
  readonly key = uid('approver-rule')
  readonly el: HTMLDivElement
  readonly branches: ChipList
  readonly approvers: ChipList

  constructor(onRemove: (row: ApproverRuleRow) => void, onChange: () => void) {
    this.branches = new ChipList({ ariaLabel: 'Add a branch glob', placeholder: 'release/*', mono: true, validate: (v) => (/\s/.test(v) ? 'A branch glob has no spaces.' : undefined), onChange })
    this.approvers = new ChipList({ ariaLabel: 'Add an approver', placeholder: 'reactorcide_group:release', mono: true, validate: (v) => subjectError(v), onChange })
    this.el = h(
      'div',
      { class: 'sub-card approver-rule-row' },
      h('div', { class: 'sub-card-header' }, h('h4', { class: 'section-title' }, 'Approver rule'), button('Remove rule', { small: true, variant: 'danger', onClick: () => onRemove(this) })),
      field({ label: 'Branches', required: true, hint: 'Branch globs. ** is every branch. The first rule that matches the entry wins.', control: this.branches.el, labelTarget: this.branches.input }).el,
      field({ label: 'Approvers', required: true, hint: subjectHint(), control: this.approvers.el, labelTarget: this.approvers.input }).el,
    )
  }

  set(rule: RequiredSetApproverRule): void {
    this.branches.set(rule.branches)
    this.approvers.set(rule.approvers)
  }

  value(): RequiredSetApproverRule {
    return { branches: this.branches.values(), approvers: this.approvers.values() }
  }
}

export class RequiredSetOrgCard extends OrgCard<GetRequiredSetOrgSettingsResponse> {
  protected readonly load = new Loader<string, GetRequiredSetOrgSettingsResponse>(this, (organization) => api.getRequiredSetOrgSettings({ organization }))
  #action = new Action(() => this.invalidate())
  #error = new ActionError()
  #loading!: HTMLParagraphElement
  #body!: HTMLDivElement
  #readonly!: HTMLParagraphElement
  #approvers!: ChipList
  #rules: ApproverRuleRow[] = []
  #ruleList!: HTMLDivElement
  #noRules!: HTMLParagraphElement
  #addRule!: PushButton
  #days!: TextField
  #save!: PushButton
  #saved!: HTMLDivElement
  #seeded = ''

  protected build(): void {
    this.#loading = h('p', { class: 'meta' }, 'Loading…')
    this.#readonly = h('p', { class: 'meta', hidden: true }, 'Only an organization administrator can change these settings.')
    this.#approvers = new ChipList({ ariaLabel: 'Add a required set approver', placeholder: 'reactorcide_group:platform', mono: true, validate: (v) => subjectError(v), onChange: () => this.invalidate() })
    this.#ruleList = h('div')
    this.#noRules = h('p', { class: 'meta' }, 'No rules. The approvers above approve changes on every branch.')
    this.#addRule = button('Add rule', { small: true, onClick: () => this.#add() })
    this.#addRule.classList.add('rc-add-row')
    this.#days = textField({ mono: true, placeholder: '30' })
    this.#save = button('Save required set defaults', { variant: 'primary', onClick: () => void this.#store() })
    this.#saved = h('div', { class: 'alert alert-info', role: 'status', hidden: true }, 'Required set defaults saved.')
    this.#body = h(
      'div',
      { hidden: true },
      this.#readonly,
      this.#saved,
      field({ label: 'Approvers', hint: `Who approves a change to the required set of a project. Empty: org admins only. ${subjectHint()}`, control: this.#approvers.el, labelTarget: this.#approvers.input }).el,
      h('h4', { class: 'section-title' }, 'Approvers for each branch'),
      this.#noRules,
      this.#ruleList,
      this.#addRule,
      field({ label: 'Longest quarantine (days)', hint: 'The longest time a workflow can be out of the required set. From 1 to 365.', control: this.#days }).el,
      h('div', { class: 'row' }, this.#save),
    )
    this.append(
      h('h3', { class: 'section-title' }, 'Required set defaults'),
      h('p', { class: 'meta' }, 'The approvers of required set changes and the longest quarantine. A project can set its own approvers on the project page.'),
      this.#error.el,
      this.#loading,
      this.#body,
    )
  }

  #newRow(): ApproverRuleRow {
    return new ApproverRuleRow(
      (row) => {
        this.#rules = this.#rules.filter((r) => r !== row)
        this.invalidate()
      },
      () => this.invalidate(),
    )
  }

  #add(): void {
    this.#rules = [...this.#rules, this.#newRow()]
    this.invalidate()
  }

  /** The request body fields the form sends. */
  values(): { approvers: string[]; approverRules: RequiredSetApproverRule[]; maxQuarantineDays: number } {
    const days = this.#days.value.trim()
    return { approvers: this.#approvers.values(), approverRules: this.#rules.map((r) => r.value()), maxQuarantineDays: days ? Number(days) : 0 }
  }

  async #store(): Promise<void> {
    show(this.#saved, false)
    const values = this.values()
    const days = this.#days.value.trim()
    const problem =
      values.approverRules.find((r) => r.branches.length === 0 || r.approvers.length === 0) !== undefined
        ? 'Each approver rule needs a branch and an approver.'
        : days && !(/^\d+$/.test(days) && Number(days) >= 1 && Number(days) <= 365)
          ? 'The longest quarantine is a number of days from 1 to 365.'
          : undefined
    if (problem) {
      this.#action.error = problem
      this.invalidate()
      return
    }
    const organization = this.organization
    let response: GetRequiredSetOrgSettingsResponse | undefined
    const ok = await this.#action.run('The required set defaults could not be saved.', async () => {
      response = await api.putRequiredSetOrgSettings({ organization, ...values })
    })
    if (!ok || !response || !this.isActive()) return
    this.#seeded = ''
    this.load.mutate(response)
    show(this.#saved, true)
  }

  #seed(data: GetRequiredSetOrgSettingsResponse): void {
    const key = JSON.stringify([this.organization, data.approvers, data.approverRules, data.maxQuarantineDays])
    if (key === this.#seeded) return
    this.#seeded = key
    this.#approvers.set(data.approvers)
    this.#rules = data.approverRules.map((rule) => {
      const row = this.#newRow()
      row.set(rule)
      return row
    })
    this.#days.value = String(data.maxQuarantineDays)
  }

  render(): void {
    this.load.sync(this.organization || undefined)
    const data = this.load.state.data
    this.#error.set(this.#action.error ?? this.load.state.error?.message)
    show(this.#loading, !data && !this.load.state.error)
    show(this.#body, data)
    if (!data) return
    this.#seed(data)
    show(this.#readonly, !data.canManage)
    show(this.#save, data.canManage)
    show(this.#addRule, data.canManage)
    this.#days.disabled = !data.canManage || this.#action.busy
    this.#save.disabled = this.#action.busy
    show(this.#noRules, this.#rules.length === 0)
    syncKeyed(this.#ruleList, this.#rules, (r) => r.key, (r) => r.el)
    setText(this.#saved, 'Required set defaults saved.')
  }
}

export const orgReportingCardTag = 'rc-org-reporting-card'
define(orgReportingCardTag, OrgReportingCard)
export const requiredSetOrgCardTag = 'rc-required-set-org-card'
define(requiredSetOrgCardTag, RequiredSetOrgCard)

export function orgReportingCard(organization: string): OrgReportingCard {
  const el = document.createElement(orgReportingCardTag) as OrgReportingCard
  el.organization = organization
  return el
}

export function requiredSetOrgCard(organization: string): RequiredSetOrgCard {
  const el = document.createElement(requiredSetOrgCardTag) as RequiredSetOrgCard
  el.organization = organization
  return el
}
