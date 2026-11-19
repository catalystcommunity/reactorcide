import type { CheckBox, ComboBox, PushButton, TextField } from '@catalystcommunity/controls'
import { api } from '~/api/client.ts'
import type { AlertRuleInput, ListAlertRulesResponse, ObsAlertRule } from '~/api/csilapi/types.gen.ts'
import { formatDuration, formatPercent } from '~/components/Charts.ts'
import { field, type Field } from '~/components/Field.ts'
import { Action, ActionError } from '~/components/ManagementPage.ts'
import { SESSION, session } from '~/lib/session.ts'
import { button, checkBox, clearField, comboBox, confirmAction, setButtonText, setComboValue, setOptions, textField } from '~/ui/controls.ts'
import { h, setText, show, syncKeyed } from '~/ui/dom.ts'
import { RcElement, define } from '~/ui/element.ts'
import { Loader } from '~/ui/load.ts'

/**
 * The alert rules of an organization (docs/observability.md "Alerts"), with
 * a form to make, change and delete them.
 *
 * A project owner manages a rule with a project; an org admin manages an
 * owner_group rule with no project. The coordinator decides each call; the
 * list says which rules the caller can change (`canManage`), and whether
 * the caller can make a rule with no project (`canCreateOrgRules`).
 */

const ALL_SCOPES = [
  { value: 'workflow', label: 'Workflow', description: 'One workflow security ID of a project' },
  { value: 'project', label: 'Project', description: 'Every workflow of a project' },
  { value: 'owner_group', label: 'Owner group', description: 'The workflows an owner group owns (an org admin)' },
]

export const CONDITIONS = [
  { value: 'pass_rate_below', label: 'Pass rate below', description: 'The pass rate over the window is less than the threshold' },
  { value: 'duration_regression', label: 'Duration regression', description: 'The p95 duration is more than the baseline p95 × factor' },
]

/** models.RefClassBuckets, in precedence order. "" counts every ref class. */
export const REF_CLASS_BUCKETS = [
  { value: 'default', label: 'default (the default branch)' },
  { value: 'tag', label: 'tag' },
  { value: 'pr', label: 'pr' },
  { value: 'schedule', label: 'schedule' },
  { value: 'manual', label: 'manual' },
  { value: 'other', label: 'other' },
  { value: '', label: 'All ref classes' },
]

export function alertStateText(state: string): string {
  switch (state) {
    case 'firing':
      return '▲ Firing'
    case 'ok':
      return '✓ OK'
    case 'no_data':
      return '– No data'
  }
  return state
}

export function alertConditionText(rule: ObsAlertRule): string {
  return rule.condition === 'pass_rate_below' ? `pass rate < ${formatPercent(rule.threshold, 0)}` : `p95 > baseline × ${rule.factor}`
}

export function alertScopeText(rule: ObsAlertRule): string {
  if (rule.scope === 'workflow') return `workflow ${rule.workflowSecurityId ?? ''}`
  if (rule.scope === 'owner_group') return `owner group ${rule.ownerGroup ?? ''}`
  return 'project'
}

/** A number from a text field: undefined when empty, NaN when not a number. */
export function parseNumber(text: string): number | undefined {
  const trimmed = text.trim()
  if (trimmed === '') return undefined
  return Number(trimmed)
}

export interface AlertScope {
  orgId: string
  /** Set when Insights shows one project: the list and the form use it. */
  projectId?: string
}

type NumberKey = 'threshold' | 'factor' | 'windowHours' | 'baselineDays' | 'minRuns' | 'resolveMargin' | 'fireAfter' | 'resolveAfter'

interface NumberInput {
  key: NumberKey
  label: string
  hint: string
  integer: boolean
  min: number
  max: number
  control: TextField
  field: Field
}

const NUMBER_FIELDS: Omit<NumberInput, 'control' | 'field'>[] = [
  { key: 'threshold', label: 'Threshold', hint: 'Pass rate below: a value from 0 to 1 (0.9 is 90%).', integer: false, min: 0, max: 1 },
  { key: 'factor', label: 'Factor', hint: 'Duration regression: 1 to 100. Default 1.5.', integer: false, min: 1, max: 100 },
  { key: 'windowHours', label: 'Window hours', hint: 'The window that the rule measures. Default 24.', integer: true, min: 1, max: 24 * 90 },
  { key: 'baselineDays', label: 'Baseline days', hint: 'Duration regression: whole days before the window. Default 7.', integer: true, min: 1, max: 90 },
  { key: 'minRuns', label: 'Minimum runs', hint: 'Fewer pipeline runs is "no data". Default 5.', integer: true, min: 1, max: 100000 },
  { key: 'resolveMargin', label: 'Resolve margin', hint: 'Hysteresis before a firing rule resolves. Default 0.05.', integer: false, min: 0, max: 1 },
  { key: 'fireAfter', label: 'Fire after', hint: 'Evaluations in a row before it fires. Default 2.', integer: true, min: 1, max: 100 },
  { key: 'resolveAfter', label: 'Resolve after', hint: 'Evaluations in a row before it resolves. Default 2.', integer: true, min: 1, max: 100 },
]

type RuleRow = HTMLTableRowElement & { rule?: ObsAlertRule }

export class AlertRulesEditor extends RcElement {
  static watches = [SESSION]

  #scope: AlertScope | undefined
  #load = new Loader<AlertScope, ListAlertRulesResponse>(this, (key) =>
    api.listAlertRules(key.projectId ? { orgId: key.orgId, projectId: key.projectId } : { orgId: key.orgId }),
  )
  #action = new Action(() => this.invalidate())
  /** The rule in the form, or undefined for a new rule. */
  #editing: ObsAlertRule | undefined

  #error!: ActionError
  #tbody!: HTMLTableSectionElement
  #tableWrap!: HTMLElement
  #none!: HTMLElement
  #formCard!: HTMLElement
  #formTitle!: HTMLElement
  #name!: TextField
  #scopeBox!: ComboBox
  #project!: TextField
  #workflow!: TextField
  #ownerGroup!: TextField
  #condition!: ComboBox
  #bucket!: ComboBox
  #description!: TextField
  #enabled!: CheckBox
  #numbers: NumberInput[] = []
  #submit!: PushButton
  #cancel!: PushButton
  #fields!: { name: Field; project: Field; workflow: Field; ownerGroup: Field }

  setScope(scope: AlertScope): void {
    const next = scope.orgId ? { ...scope } : undefined
    if (next && !next.projectId) delete next.projectId
    if (JSON.stringify(next) === JSON.stringify(this.#scope)) return
    this.#scope = next
    if (this.isConnected) this.invalidate()
  }

  protected build(): void {
    this.#error = new ActionError()
    this.#tbody = h('tbody')
    this.#tableWrap = h(
      'div',
      { class: 'scroll-x' },
      h(
        'table',
        { class: 'table alert-rules-table' },
        h(
          'thead',
          null,
          h(
            'tr',
            null,
            h('th', null, 'Rule'),
            h('th', null, 'Scope'),
            h('th', null, 'Condition'),
            h('th', null, 'State'),
            h('th', { class: 'numeric' }, 'Value'),
            h('th', { class: 'numeric' }, 'Runs'),
            h('th', null, h('span', { class: 'sr-only' }, 'Actions')),
          ),
        ),
        this.#tbody,
      ),
    )
    this.#none = h('div', { class: 'meta' }, 'No alert rules.')

    const changed = (): void => this.invalidate()
    this.#name = textField({ mono: true, placeholder: 'ci-pass', onInput: changed })
    this.#scopeBox = comboBox({ ariaLabel: 'Scope', options: ALL_SCOPES.slice(0, 2), value: 'workflow', onChange: changed })
    this.#project = textField({ mono: true, placeholder: 'project ID', onInput: changed })
    this.#workflow = textField({ mono: true, placeholder: 'ci', onInput: changed })
    this.#ownerGroup = textField({ mono: true, placeholder: 'team-a', onInput: changed })
    this.#condition = comboBox({ ariaLabel: 'Condition', options: CONDITIONS, value: 'pass_rate_below', onChange: changed })
    this.#bucket = comboBox({ ariaLabel: 'Ref class bucket', options: REF_CLASS_BUCKETS, value: 'default' })
    this.#description = textField({ placeholder: 'What the owner does when it fires' })
    this.#enabled = checkBox('Enabled', { checked: true })
    this.#numbers = NUMBER_FIELDS.map((spec) => {
      const control = textField({ mono: true, onInput: changed })
      control.classList.add(`alert-${spec.key}`)
      return { ...spec, control, field: field({ label: spec.label, hint: spec.hint, control }) }
    })
    this.#fields = {
      name: field({ label: 'Name', required: true, hint: 'Lowercase letters, digits and hyphens.', control: this.#name }),
      project: field({ label: 'Project', hint: 'Workflow and project scopes. Optional for an owner group.', control: this.#project }),
      workflow: field({ label: 'Workflow', hint: 'The workflow security ID.', control: this.#workflow }),
      ownerGroup: field({ label: 'Owner group', control: this.#ownerGroup }),
    }
    this.#submit = button('Create rule', { variant: 'primary', type: 'submit' })
    this.#cancel = button('Cancel', { onClick: () => this.#resetForm() })
    this.#formTitle = h('h3', { class: 'section-title' }, 'New alert rule')
    const form = h(
      'form',
      { class: 'alert-rule-form', onSubmit: (event: Event) => void this.#onSubmit(event) },
      h(
        'div',
        { class: 'form-row' },
        this.#fields.name.el,
        field({ label: 'Scope', required: true, control: this.#scopeBox, labelTarget: this.#scopeBox }).el,
        field({ label: 'Condition', required: true, control: this.#condition, labelTarget: this.#condition }).el,
      ),
      h('div', { class: 'form-row' }, this.#fields.project.el, this.#fields.workflow.el, this.#fields.ownerGroup.el),
      h(
        'div',
        { class: 'form-row' },
        field({ label: 'Ref class bucket', hint: 'Which runs count. "All" counts every ref class.', control: this.#bucket, labelTarget: this.#bucket }).el,
        field({ label: 'Description', control: this.#description }).el,
      ),
      h('div', { class: 'form-row' }, this.#numbers.map((n) => n.field.el)),
      h('div', { class: 'checkbox-row' }, this.#enabled),
      h('div', { class: 'row' }, this.#submit, this.#cancel),
    )
    this.#formCard = h('div', { class: 'alert-rule-editor' }, this.#formTitle, form)
    this.append(this.#error.el, this.#tableWrap, this.#none, this.#formCard)
  }

  #numberErrors(): boolean {
    let bad = false
    const condition = this.#condition.value
    for (const n of this.#numbers) {
      const value = parseNumber(n.control.value)
      let message: string | undefined
      if (value !== undefined) {
        if (Number.isNaN(value) || (n.integer && !Number.isInteger(value))) message = n.integer ? 'Give a whole number.' : 'Give a number.'
        else if (value < n.min || value > n.max || (n.key === 'threshold' && value === 0)) message = `Give a value from ${n.min} to ${n.max}.`
      }
      n.field.setError(message)
      bad ||= Boolean(message)
      // The threshold is for a pass rate rule, the factor and the baseline for a duration rule.
      if (n.key === 'threshold') show(n.field.el, condition === 'pass_rate_below')
      if (n.key === 'factor' || n.key === 'baselineDays') show(n.field.el, condition === 'duration_regression')
    }
    return bad
  }

  #canSubmit(): boolean {
    if (!this.#scope) return false
    if (this.#numberErrors()) return false
    if (this.#editing) return true
    const scope = this.#scopeBox.value
    if (!this.#name.value.trim()) return false
    if ((scope === 'workflow' || scope === 'project') && !this.#project.value.trim()) return false
    if (scope === 'workflow' && !this.#workflow.value.trim()) return false
    if (scope === 'owner_group' && !this.#ownerGroup.value.trim()) return false
    return true
  }

  #input(): AlertRuleInput {
    const out: AlertRuleInput = {
      description: this.#description.value.trim(),
      condition: this.#condition.value,
      refClassBucket: this.#bucket.value,
      enabled: this.#enabled.checked,
    }
    for (const n of this.#numbers) {
      const value = parseNumber(n.control.value)
      if (value === undefined) continue
      if (n.key === 'threshold' && out.condition !== 'pass_rate_below') continue
      if ((n.key === 'factor' || n.key === 'baselineDays') && out.condition !== 'duration_regression') continue
      out[n.key] = value
    }
    return out
  }

  async #onSubmit(event: Event): Promise<void> {
    event.preventDefault()
    if (!this.#canSubmit() || !this.#scope) return
    const orgId = this.#scope.orgId
    const rule = this.#input()
    const editing = this.#editing
    let ok: boolean
    if (editing) {
      ok = await this.#action.run('The alert rule could not be saved.', () => api.updateAlertRule({ orgId, ruleId: editing.ruleId, rule }))
    } else {
      const scope = this.#scopeBox.value
      const project = this.#project.value.trim()
      ok = await this.#action.run('The alert rule could not be made.', () =>
        api.createAlertRule({
          orgId,
          name: this.#name.value.trim(),
          scope,
          projectId: scope !== 'owner_group' || project ? project : undefined,
          workflowSecurityId: scope === 'workflow' ? this.#workflow.value.trim() : undefined,
          ownerGroup: scope === 'owner_group' ? this.#ownerGroup.value.trim() : undefined,
          rule,
        }),
      )
    }
    if (ok && this.isActive()) {
      this.#resetForm()
      void this.#load.reload()
    }
  }

  /** Loads a rule into the form one time. A refresh does not change it again. */
  #edit(rule: ObsAlertRule): void {
    this.#editing = rule
    this.#name.value = rule.name
    // Options before the value: an owner_group rule needs its option.
    setOptions(this.#scopeBox, ALL_SCOPES)
    setComboValue(this.#scopeBox, rule.scope)
    this.#project.value = rule.projectId ?? ''
    this.#workflow.value = rule.workflowSecurityId ?? ''
    this.#ownerGroup.value = rule.ownerGroup ?? ''
    setComboValue(this.#condition, rule.condition)
    setComboValue(this.#bucket, rule.refClassBucket)
    this.#description.value = rule.description ?? ''
    this.#enabled.checked = rule.enabled
    const values: Record<NumberKey, number | undefined> = {
      threshold: rule.threshold,
      factor: rule.factor,
      windowHours: rule.windowHours,
      baselineDays: rule.baselineDays,
      minRuns: rule.minRuns,
      resolveMargin: rule.resolveMargin,
      fireAfter: rule.fireAfter,
      resolveAfter: rule.resolveAfter,
    }
    for (const n of this.#numbers) n.control.value = values[n.key] === undefined ? '' : String(values[n.key])
    this.#action.clearError()
    this.invalidate()
    this.#name.scrollIntoView?.({ block: 'nearest' })
  }

  #resetForm(): void {
    this.#editing = undefined
    clearField(this.#name)
    clearField(this.#workflow)
    clearField(this.#ownerGroup)
    clearField(this.#description)
    this.#project.value = this.#scope?.projectId ?? ''
    setComboValue(this.#condition, 'pass_rate_below')
    setComboValue(this.#bucket, 'default')
    this.#enabled.checked = true
    for (const n of this.#numbers) clearField(n.control)
    this.invalidate()
  }

  async #delete(rule: ObsAlertRule): Promise<void> {
    if (!this.#scope) return
    const confirmed = await confirmAction(`Delete the alert rule ${rule.name}? Its transitions are deleted too.`, { okLabel: 'Delete' })
    if (!confirmed) return
    const orgId = this.#scope.orgId
    if (await this.#action.run('The alert rule could not be deleted.', () => api.deleteAlertRule({ orgId, ruleId: rule.ruleId }))) {
      if (this.#editing?.ruleId === rule.ruleId) this.#resetForm()
      void this.#load.reload()
    }
  }

  #createRow(): RuleRow {
    const tr = h('tr') as RuleRow
    const edit = button('Edit', { small: true, onClick: () => tr.rule && this.#edit(tr.rule) })
    edit.classList.add('alert-edit')
    const remove = button('Delete', { small: true, variant: 'danger', onClick: () => tr.rule && void this.#delete(tr.rule) })
    remove.classList.add('alert-delete')
    tr.append(
      h('td', null, h('div', { class: 'alert-name mono' }), h('div', { class: 'meta alert-description' })),
      h('td', { class: 'alert-scope' }),
      h('td', { class: 'alert-condition' }),
      h('td', { class: 'alert-state' }),
      h('td', { class: 'numeric alert-value' }),
      h('td', { class: 'numeric alert-runs' }),
      h('td', null, h('div', { class: 'row' }, edit, remove)),
    )
    return tr
  }

  #updateRow(tr: RuleRow, rule: ObsAlertRule): void {
    tr.rule = rule
    setText(tr.querySelector('.alert-name')!, rule.name)
    const description = tr.querySelector<HTMLElement>('.alert-description')!
    setText(description, rule.description ?? '')
    show(description, rule.description)
    setText(tr.querySelector('.alert-scope')!, alertScopeText(rule))
    setText(tr.querySelector('.alert-condition')!, alertConditionText(rule))
    setText(tr.querySelector('.alert-state')!, rule.enabled ? alertStateText(rule.state) : 'Disabled')
    setText(tr.querySelector('.alert-value')!, rule.condition === 'pass_rate_below' ? formatPercent(rule.lastValue) : formatDuration(rule.lastValue))
    setText(tr.querySelector('.alert-runs')!, String(rule.lastRuns))
    const manage = Boolean(rule.canManage)
    for (const sel of ['.alert-edit', '.alert-delete']) {
      const b = tr.querySelector(sel) as PushButton
      show(b, manage)
      b.disabled = this.#action.busy
    }
  }

  render(): void {
    this.#load.sync(this.#scope)
    const data = this.#load.state.data
    this.#error.set(this.#action.error ?? this.#load.state.error?.message)
    const rules = data?.rules ?? []
    show(this.#tableWrap, rules.length > 0)
    show(this.#none, this.#load.state.loaded && rules.length === 0)
    syncKeyed(this.#tbody, rules, (r) => r.ruleId, () => this.#createRow(), (tr, r) => this.#updateRow(tr as RuleRow, r))

    // The form is for a signed-in caller; the coordinator decides each change.
    show(this.#formCard, Boolean(session()?.logged_in) && Boolean(data))
    const editing = Boolean(this.#editing)
    setText(this.#formTitle, editing ? `Change alert rule ${this.#editing!.name}` : 'New alert rule')
    setOptions(this.#scopeBox, data?.canCreateOrgRules || this.#editing?.scope === 'owner_group' ? ALL_SCOPES : ALL_SCOPES.slice(0, 2))
    const scope = this.#scopeBox.value
    // The scope of a rule cannot change.
    for (const control of [this.#name, this.#scopeBox, this.#project, this.#workflow, this.#ownerGroup]) control.disabled = editing || this.#action.busy
    show(this.#fields.workflow.el, scope === 'workflow')
    show(this.#fields.ownerGroup.el, scope === 'owner_group')
    if (!editing && this.#scope?.projectId && !this.#project.value) this.#project.value = this.#scope.projectId
    this.#submit.disabled = this.#action.busy || !this.#canSubmit()
    setButtonText(this.#submit, editing ? 'Save rule' : 'Create rule')
    show(this.#cancel, editing)
  }
}

export const alertRulesEditorTag = 'rc-alert-rules-editor'
define(alertRulesEditorTag, AlertRulesEditor)

export function alertRulesEditor(): AlertRulesEditor {
  return document.createElement(alertRulesEditorTag) as AlertRulesEditor
}
