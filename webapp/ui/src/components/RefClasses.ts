import type { CheckBox, PushButton, TextField } from '@catalystcommunity/controls'
import { api } from '~/api/client.ts'
import type {
  EnumChoice,
  ExplainRefClassesResponse,
  ListRefClassRulesResponse,
  RefClassFacts,
  RefClassReason,
  RefClassRule,
} from '~/api/csilapi/types.gen.ts'
import { ChoiceMultiSelect, ChoiceSelect } from '~/components/ChoiceInput.ts'
import { field, type Field } from '~/components/Field.ts'
import { Action, ActionError, describeError } from '~/components/ManagementPage.ts'
import { skeleton } from '~/components/States.ts'
import { button, checkBox, clearField, confirmAction, setButtonText, textField } from '~/ui/controls.ts'
import { fill, h, setText, show, syncKeyed } from '~/ui/dom.ts'
import { RcElement, define } from '~/ui/element.ts'
import { Loader } from '~/ui/load.ts'
import './refclasses.css'

/**
 * Ref classes (docs/ref-classes.md): labels that the coordinator gives the
 * ref of an event, for example default, tag, pr, fork, schedule, manual, and
 * the classes that operator rules add. Workflows and jobs inherit them.
 */

/** A list of class labels. Make it one time; `setRefClasses` changes it. */
export function refClassLabels(): HTMLUListElement {
  return h('ul', { class: 'ref-class-list', 'aria-label': 'Ref classes' })
}

/** Shows `classes` in a list from `refClassLabels`. An empty list says "None". */
export function setRefClasses(el: HTMLUListElement, classes: readonly string[] | undefined): void {
  const values = classes ?? []
  const signature = JSON.stringify(values)
  if (el.dataset.signature === signature) return
  el.dataset.signature = signature
  if (values.length === 0) {
    fill(el, h('li', { class: 'meta' }, 'None'))
    return
  }
  fill(el, values.map((value) => h('li', { class: 'ref-class mono' }, value)))
}

/** One sentence for a class reason: "pr from built-in rule pull-request". */
export function refClassReasonText(reason: RefClassReason): string {
  const name = reason.ruleName || reason.ruleId
  if (reason.scope === 'builtin') return `${reason.class} from built-in rule ${name}`
  return `${reason.class} from ${reason.scope === 'project' ? 'project' : 'organization'} rule ${name}`
}

/** The event facts that gave the classes, as label and text pairs. Empty values are left out. */
export function refClassFactRows(facts: RefClassFacts | undefined): Array<[string, string]> {
  if (!facts) return []
  const rows: Array<[string, string | undefined]> = [
    ['Source', facts.source],
    ['Event type', facts.eventType],
    ['Branch', facts.branch],
    ['Head branch (pull request)', facts.headBranch],
    ['Tag', facts.tag],
    ['Base branch', facts.baseBranch],
    ['Default branch', facts.defaultBranch],
    ['Pull request', facts.isPr ? 'yes' : facts.isPr === false ? 'no' : undefined],
    ['Fork', facts.isFork ? 'yes' : facts.isFork === false ? 'no' : undefined],
    ['On the default branch', facts.isDefaultBranch ? 'yes' : facts.isDefaultBranch === false ? 'no' : undefined],
  ]
  return rows.filter((row): row is [string, string] => Boolean(row[1]))
}

/** Splits a comma or space list into trimmed, non-empty values. */
export function splitList(value: string): string[] {
  return value
    .split(/[\s,]+/)
    .map((v) => v.trim())
    .filter(Boolean)
}

const CLASS_PATTERN = /^[a-z0-9][a-z0-9._-]{0,62}$/
const RULE_NAME_PATTERN = /^[a-z0-9][a-z0-9-]{0,62}$/

/** An error for a class list, or undefined. */
export function classListError(values: readonly string[], required: boolean): string | undefined {
  if (required && values.length === 0) return 'Add at least one class.'
  const bad = values.find((v) => !CLASS_PATTERN.test(v))
  return bad ? `"${bad}" is not a valid class. Use lowercase letters, digits, ".", "_" and "-".` : undefined
}

export const EVENT_TYPE_CHOICES: EnumChoice[] = [
  { value: 'push', label: 'push', description: 'A push to a branch.' },
  { value: 'tag_created', label: 'tag_created', description: 'A new tag.' },
  { value: 'pull_request_opened', label: 'pull_request_opened', description: 'A pull request was opened.' },
  { value: 'pull_request_updated', label: 'pull_request_updated', description: 'A pull request got new commits.' },
  { value: 'pull_request_merged', label: 'pull_request_merged', description: 'A pull request was merged.' },
  { value: 'pull_request_closed', label: 'pull_request_closed', description: 'A pull request was closed.' },
  { value: 'schedule', label: 'schedule', description: 'A schedule fired.' },
  { value: 'manual', label: 'manual', description: 'An API submit. The submitter states the ref.' },
]

const TRI_CHOICES: EnumChoice[] = [
  { value: 'any', label: 'Any', description: 'No condition.' },
  { value: 'true', label: 'Yes', description: 'Only when this is true.' },
  { value: 'false', label: 'No', description: 'Only when this is false.' },
]

function triValue(value: boolean | undefined): string {
  return value === undefined ? 'any' : value ? 'true' : 'false'
}

function triParse(value: string): boolean | undefined {
  return value === 'true' ? true : value === 'false' ? false : undefined
}

/** The match facts of a rule as one short text. */
export function ruleMatchText(rule: RefClassRule): string {
  const parts: string[] = []
  if (rule.eventTypes?.length) parts.push(`events ${rule.eventTypes.join(', ')}`)
  if (rule.branchGlob) parts.push(`branch ${rule.branchGlob}`)
  if (rule.tagGlob) parts.push(`tag ${rule.tagGlob}`)
  if (rule.baseBranchGlob) parts.push(`base ${rule.baseBranchGlob}`)
  if (rule.isPr !== undefined) parts.push(rule.isPr ? 'pull request' : 'not a pull request')
  if (rule.isFork !== undefined) parts.push(rule.isFork ? 'fork' : 'not a fork')
  if (rule.isDefaultBranch !== undefined) parts.push(rule.isDefaultBranch ? 'default branch' : 'not the default branch')
  return parts.length ? parts.join('; ') : 'every event'
}

function createRuleRow(): HTMLLIElement {
  return h(
    'li',
    { class: 'sub-card ref-class-rule' },
    h('div', { class: 'sub-card-header' }, h('span', { class: 'mono rule-name' }), h('span', { class: 'rule-actions row' })),
    h('div', { class: 'meta rule-match' }),
    h('div', { class: 'rule-classes' }),
  )
}

type RuleRow = HTMLLIElement & { rule?: RefClassRule }

function updateRuleRow(li: RuleRow, rule: RefClassRule, actions?: (current: () => RefClassRule) => HTMLElement[]): void {
  setText(li.querySelector('.rule-name')!, `${rule.name}${rule.enabled ? '' : ' (disabled)'}`)
  setText(li.querySelector('.rule-match')!, `Position ${rule.position}. Matches ${ruleMatchText(rule)}.`)
  setText(li.querySelector('.rule-classes')!, `Adds: ${rule.classes.join(', ')}`)
  // The buttons are made one time; they act on the current rule of the row.
  li.rule = rule
  const holder = li.querySelector<HTMLElement>('.rule-actions')!
  if (actions && holder.childElementCount === 0) holder.append(...actions(() => li.rule!))
  show(holder, Boolean(actions))
}

type Key = { organization: string; projectId?: string }

/**
 * The ref class rules of an organization (no project) or of a project. Set
 * `organization` (the org NAME) and `projectId` before it connects, or call
 * `setScope()`.
 *
 * Rules only add classes. A project rule cannot remove a class that an
 * organization rule or a built-in rule gives.
 */
export class RefClassSettings extends RcElement {
  organization = ''
  projectId: string | undefined = undefined

  #load = new Loader<Key, ListRefClassRulesResponse>(this, (key) => api.listRefClassRules(key))
  #save = new Action(() => this.invalidate())
  #saveError = new ActionError()
  #explainAction = new Action(() => this.invalidate())
  #explainError = new ActionError()
  #explained: ExplainRefClassesResponse | undefined
  #touched = false
  #eventTypes: string[] = []
  #isPr = 'any'
  #isFork = 'any'
  #isDefault = 'any'

  #loading!: HTMLElement
  #loadError!: HTMLDivElement
  #body!: HTMLDivElement
  #intro!: HTMLParagraphElement
  #builtinList!: HTMLUListElement
  #inheritedBox!: HTMLDivElement
  #inheritedList!: HTMLUListElement
  #inheritedNone!: HTMLParagraphElement
  #ruleList!: HTMLUListElement
  #rulesNone!: HTMLParagraphElement
  #readonly!: HTMLDivElement
  #form!: HTMLFormElement
  #saved!: HTMLDivElement
  #submit!: PushButton
  #name!: TextField
  #nameField!: Field
  #position!: TextField
  #classes!: TextField
  #classesField!: Field
  #description!: TextField
  #events!: ChoiceMultiSelect
  #branch!: TextField
  #tag!: TextField
  #base!: TextField
  #pr!: ChoiceSelect
  #fork!: ChoiceSelect
  #default!: ChoiceSelect
  #enabled!: CheckBox

  #explainEvent!: TextField
  #explainBranch!: TextField
  #explainTag!: TextField
  #explainType!: TextField
  #explainBase!: TextField
  #explainDefault!: TextField
  #explainFork!: CheckBox
  #explainResult!: HTMLDivElement
  #explainClasses!: HTMLUListElement
  #explainRules!: HTMLUListElement
  #explainSubmit!: PushButton

  setScope(organization: string, projectId?: string): void {
    if (this.organization === organization && this.projectId === projectId) return
    this.organization = organization
    this.projectId = projectId
    this.invalidate()
  }

  #key(): Key | undefined {
    if (!this.organization) return undefined
    return this.projectId ? { organization: this.organization, projectId: this.projectId } : { organization: this.organization }
  }

  protected build(): void {
    const rerender = (): void => this.invalidate()
    this.#intro = h('p', { class: 'meta' })
    this.#builtinList = h('ul', { class: 'readonly-list' })
    this.#inheritedList = h('ul', { class: 'readonly-list' })
    this.#inheritedNone = h('p', { class: 'meta' }, 'The organization has no rules.')
    this.#inheritedBox = h(
      'div',
      { hidden: true },
      h('h4', { class: 'section-title' }, 'Organization rules'),
      h('p', { class: 'meta' }, 'These apply to every project. Change them on the organization CI trust page.'),
      this.#inheritedNone,
      this.#inheritedList,
    )
    this.#ruleList = h('ul', { class: 'readonly-list' })
    this.#rulesNone = h('p', { class: 'meta' }, 'No rules. Only the built-in classes apply.')
    this.#readonly = h('div', { class: 'alert alert-info', hidden: true })

    this.#name = textField({ mono: true, placeholder: 'production', onInput: rerender })
    this.#nameField = field({ label: 'Rule name', required: true, hint: 'Lowercase letters, digits and hyphens. A rule with the same name is replaced.', control: this.#name })
    this.#position = textField({ mono: true, placeholder: '0' })
    this.#classes = textField({ mono: true, placeholder: 'production, release', onInput: rerender })
    this.#classesField = field({ label: 'Classes to add', required: true, hint: 'A comma list, for example production.', control: this.#classes })
    this.#description = textField({ placeholder: 'Pushes to the default branch deploy to production.' })
    this.#events = new ChoiceMultiSelect({ ariaLabel: 'Event types', choices: EVENT_TYPE_CHOICES, onChange: (values) => { this.#eventTypes = values } })
    this.#branch = textField({ mono: true, placeholder: 'main or release/*' })
    this.#tag = textField({ mono: true, placeholder: 'v*' })
    this.#base = textField({ mono: true, placeholder: 'main' })
    this.#pr = new ChoiceSelect({ ariaLabel: 'Pull request', value: 'any', choices: TRI_CHOICES, onChange: (v) => { this.#isPr = v } })
    this.#fork = new ChoiceSelect({ ariaLabel: 'Fork', value: 'any', choices: TRI_CHOICES, onChange: (v) => { this.#isFork = v } })
    this.#default = new ChoiceSelect({ ariaLabel: 'Default branch', value: 'any', choices: TRI_CHOICES, onChange: (v) => { this.#isDefault = v } })
    this.#enabled = checkBox('Enabled', { checked: true })
    this.#saved = h('div', { class: 'alert alert-info', role: 'status', hidden: true }, 'Ref class rule saved.')
    this.#submit = button('Save rule', { type: 'submit', variant: 'primary' })
    this.#form = h(
      'form',
      { novalidate: true, class: 'ref-class-form', onSubmit: (event: Event) => void this.#submitForm(event) },
      h('h4', { class: 'section-title' }, 'Add or replace a rule'),
      h('p', { class: 'meta' }, 'A rule adds its classes when every condition that you set is true. Use event types for a class that gives authority, so a manual submit cannot get it.'),
      this.#saveError.el,
      this.#saved,
      h('div', { class: 'form-row' }, this.#nameField.el, field({ label: 'Position', hint: 'Lower runs first. The order changes only the order of the reasons.', control: this.#position }).el),
      this.#classesField.el,
      field({ label: 'Description', control: this.#description }).el,
      field({ label: 'Event types', hint: 'Select none for every event type.', control: this.#events.el }).el,
      h(
        'div',
        { class: 'form-row' },
        field({ label: 'Branch', hint: '* stays in one segment; ** crosses segments.', control: this.#branch }).el,
        field({ label: 'Tag', control: this.#tag }).el,
        field({ label: 'Base branch', hint: 'The target of a pull request.', control: this.#base }).el,
      ),
      h(
        'div',
        { class: 'form-row' },
        field({ label: 'Pull request', control: this.#pr.el }).el,
        field({ label: 'Fork', control: this.#fork.el }).el,
        field({ label: 'Default branch', hint: 'A push to, or a pull request into, the default branch.', control: this.#default.el }).el,
      ),
      h('div', { class: 'row' }, this.#enabled, this.#submit),
    )

    this.#explainEvent = textField({ mono: true, ariaLabel: 'Event ID', placeholder: 'An event ID (optional)' })
    this.#explainType = textField({ mono: true, ariaLabel: 'Event type', placeholder: 'push' })
    this.#explainBranch = textField({ mono: true, ariaLabel: 'Branch', placeholder: 'main' })
    this.#explainTag = textField({ mono: true, ariaLabel: 'Tag', placeholder: 'v1.0.0' })
    this.#explainBase = textField({ mono: true, ariaLabel: 'Base branch', placeholder: 'main' })
    this.#explainDefault = textField({ mono: true, ariaLabel: 'Default branch', placeholder: 'main' })
    this.#explainFork = checkBox('From a fork')
    this.#explainSubmit = button('Explain', { type: 'submit' })
    this.#explainClasses = refClassLabels()
    this.#explainRules = h('ul', { class: 'reason-list' })
    this.#explainResult = h(
      'div',
      { class: 'explain-result', hidden: true },
      h('div', { class: 'field-label' }, 'Classes'),
      this.#explainClasses,
      h('div', { class: 'field-label' }, 'Why'),
      this.#explainRules,
    )
    const explainForm = h(
      'form',
      { novalidate: true, class: 'ref-class-explain', onSubmit: (event: Event) => void this.#explain(event) },
      h('h4', { class: 'section-title' }, 'Explain'),
      h('p', { class: 'meta' }, 'Give an event ID to see its stored classes, or give the facts of a ref.'),
      this.#explainError.el,
      h(
        'div',
        { class: 'form-row' },
        field({ label: 'Event ID', control: this.#explainEvent }).el,
        field({ label: 'Event type', control: this.#explainType }).el,
        field({ label: 'Branch', control: this.#explainBranch }).el,
      ),
      h(
        'div',
        { class: 'form-row' },
        field({ label: 'Tag', control: this.#explainTag }).el,
        field({ label: 'Base branch', control: this.#explainBase }).el,
        field({ label: 'Default branch', control: this.#explainDefault }).el,
      ),
      h('div', { class: 'row' }, this.#explainFork, this.#explainSubmit),
      this.#explainResult,
    )

    this.#loading = skeleton(3)
    this.#loadError = h('div', { class: 'alert alert-error', role: 'alert', hidden: true })
    this.#body = h(
      'div',
      { hidden: true },
      h('h4', { class: 'section-title' }, 'Built-in rules'),
      h('p', { class: 'meta' }, 'These always apply.'),
      this.#builtinList,
      this.#inheritedBox,
      h('h4', { class: 'section-title rules-title' }),
      this.#rulesNone,
      this.#ruleList,
      this.#readonly,
      this.#form,
      explainForm,
    )
    this.classList.add('card', 'ref-class-settings')
    this.append(h('div', { class: 'card-header' }, h('div', null, h('h3', null, 'Ref classes'), this.#intro)), this.#loadError, this.#loading, this.#body)
  }

  /** The rule that the form describes. */
  rule(): RefClassRule {
    const out: RefClassRule = {
      name: this.#name.value.trim(),
      position: Number(this.#position.value.trim() || '0') || 0,
      classes: splitList(this.#classes.value),
      enabled: this.#enabled.checked,
    }
    const description = this.#description.value.trim()
    if (description) out.description = description
    if (this.#eventTypes.length) out.eventTypes = [...this.#eventTypes]
    if (this.#branch.value.trim()) out.branchGlob = this.#branch.value.trim()
    if (this.#tag.value.trim()) out.tagGlob = this.#tag.value.trim()
    if (this.#base.value.trim()) out.baseBranchGlob = this.#base.value.trim()
    const pr = triParse(this.#isPr)
    if (pr !== undefined) out.isPr = pr
    const fork = triParse(this.#isFork)
    if (fork !== undefined) out.isFork = fork
    const isDefault = triParse(this.#isDefault)
    if (isDefault !== undefined) out.isDefaultBranch = isDefault
    return out
  }

  /** Puts a stored rule into the form, to change it. */
  edit(rule: RefClassRule): void {
    this.#name.value = rule.name
    this.#position.value = String(rule.position)
    this.#classes.value = rule.classes.join(', ')
    this.#description.value = rule.description ?? ''
    this.#eventTypes = [...(rule.eventTypes ?? [])]
    this.#branch.value = rule.branchGlob ?? ''
    this.#tag.value = rule.tagGlob ?? ''
    this.#base.value = rule.baseBranchGlob ?? ''
    this.#isPr = triValue(rule.isPr)
    this.#isFork = triValue(rule.isFork)
    this.#isDefault = triValue(rule.isDefaultBranch)
    this.#enabled.checked = rule.enabled
    this.invalidate()
    this.#name.focus()
  }

  #errors(): { name?: string; classes?: string } {
    const rule = this.rule()
    return {
      name: !rule.name ? 'A rule needs a name.' : RULE_NAME_PATTERN.test(rule.name) ? undefined : 'Use lowercase letters, digits and hyphens.',
      classes: classListError(rule.classes, true),
    }
  }

  async #submitForm(event: Event): Promise<void> {
    event.preventDefault()
    const key = this.#key()
    const current = this.#load.state.data
    if (!key || !current?.canManage) return
    this.#touched = true
    show(this.#saved, false)
    const errors = this.#errors()
    if (errors.name || errors.classes) {
      this.invalidate()
      return
    }
    const rule = this.rule()
    let saved: RefClassRule | undefined
    const ok = await this.#save.run('The ref class rule could not be saved.', async () => {
      saved = (await api.putRefClassRule({ ...key, rule })).rule
    })
    if (!ok || !saved || !this.isActive()) return
    const rules = [...current.rules.filter((r) => r.name !== saved!.name), saved].sort((a, b) => a.position - b.position || a.name.localeCompare(b.name))
    this.#load.mutate({ ...current, rules })
    this.#touched = false
    clearField(this.#name)
    clearField(this.#classes)
    show(this.#saved, true)
  }

  async #delete(rule: RefClassRule): Promise<void> {
    const key = this.#key()
    const current = this.#load.state.data
    if (!key || !current) return
    if (!(await confirmAction(`Delete the ref class rule "${rule.name}"? New events no longer get its classes.`, { okLabel: 'Delete rule', danger: true }))) return
    const ok = await this.#save.run('The ref class rule could not be deleted.', () => api.deleteRefClassRule({ ...key, name: rule.name }))
    if (!ok || !this.isActive()) return
    this.#load.mutate({ ...current, rules: current.rules.filter((r) => r.name !== rule.name) })
  }

  async #explain(event: Event): Promise<void> {
    event.preventDefault()
    const key = this.#key()
    if (!key) return
    const eventId = this.#explainEvent.value.trim()
    const facts: RefClassFacts = { source: 'webhook' }
    const text = (el: TextField): string | undefined => el.value.trim() || undefined
    if (text(this.#explainType)) facts.eventType = text(this.#explainType)
    if (text(this.#explainBranch)) facts.branch = text(this.#explainBranch)
    if (text(this.#explainTag)) facts.tag = text(this.#explainTag)
    if (text(this.#explainBase)) facts.baseBranch = text(this.#explainBase)
    if (text(this.#explainDefault)) facts.defaultBranch = text(this.#explainDefault)
    if (this.#explainFork.checked) facts.isFork = true
    let result: ExplainRefClassesResponse | undefined
    const ok = await this.#explainAction.run('The ref classes could not be explained.', async () => {
      result = await api.explainRefClasses(eventId ? { ...key, eventId } : { ...key, facts })
    })
    if (!ok || !this.isActive()) return
    this.#explained = result
    this.invalidate()
  }

  render(): void {
    const key = this.#key()
    this.#load.sync(key)
    const state = this.#load.state
    const data = state.data
    const project = Boolean(this.projectId)
    setText(
      this.#intro,
      project
        ? 'Labels that Reactorcide gives the ref of each event of this project. Project rules add classes to the organization classes. Secret grants, CI policy and worker classes can use them.'
        : 'Labels that Reactorcide gives the ref of each event, for example production or release. Secret grants, CI policy and worker classes can use them. Nothing restricts by class until you configure it.',
    )
    show(this.#loading, !data && !state.error)
    show(this.#loadError, state.error && !data)
    setText(this.#loadError, state.error ? describeError(state.error, 'The ref class rules could not be loaded.') : '')
    show(this.#body, data)
    if (!data) return

    syncKeyed(
      this.#builtinList,
      data.builtin,
      (b) => b.ruleId,
      () => h('li', { class: 'meta' }),
      (li, b) => setText(li, `${b.ruleId.replace(/^builtin:/, '')}: ${b.classes.join(', ')}`),
    )
    show(this.#inheritedBox, project)
    show(this.#inheritedNone, project && data.inherited.length === 0)
    syncKeyed(this.#inheritedList, project ? data.inherited : [], (r) => r.ruleId ?? r.name, createRuleRow, (li, r) => updateRuleRow(li, r))

    setText(this.#body.querySelector('.rules-title')!, project ? 'Project rules' : 'Organization rules')
    show(this.#rulesNone, data.rules.length === 0)
    const actions = data.canManage
      ? (current: () => RefClassRule): HTMLElement[] => [
          button('Edit', { small: true, ariaLabel: `Edit rule ${current().name}`, onClick: () => this.edit(current()) }),
          button('Delete', { small: true, variant: 'danger', ariaLabel: `Delete rule ${current().name}`, onClick: () => void this.#delete(current()) }),
        ]
      : undefined
    syncKeyed(this.#ruleList, data.rules, (r) => r.name, createRuleRow, (li, r) => updateRuleRow(li, r, actions))

    show(this.#form, data.canManage)
    show(this.#readonly, !data.canManage)
    setText(this.#readonly, project ? 'Only a project owner can change the project rules.' : 'Only an organization administrator can change the organization rules.')
    const errors = this.#errors()
    this.#nameField.setError(this.#touched ? errors.name : undefined)
    this.#classesField.setError(this.#touched ? errors.classes : undefined)
    this.#events.update(EVENT_TYPE_CHOICES, this.#eventTypes)
    this.#pr.update(TRI_CHOICES, this.#isPr)
    this.#fork.update(TRI_CHOICES, this.#isFork)
    this.#default.update(TRI_CHOICES, this.#isDefault)
    this.#saveError.set(this.#save.error)
    this.#submit.disabled = this.#save.busy
    setButtonText(this.#submit, this.#save.busy ? 'Saving…' : 'Save rule')

    this.#explainError.set(this.#explainAction.error)
    this.#explainSubmit.disabled = this.#explainAction.busy
    const explained = this.#explained
    show(this.#explainResult, explained)
    if (explained) {
      setRefClasses(this.#explainClasses, explained.classes)
      const lines = [
        ...explained.reasons.map(refClassReasonText),
        ...explained.rules.filter((r) => !r.matched).map((r) => `${r.scope} rule ${r.name} did not match${r.enabled ? '' : ' (disabled)'}`),
      ]
      if (explained.storedClasses) lines.push(`Stored classes of the event: ${explained.storedClasses.join(', ') || 'none'}`)
      const signature = JSON.stringify(lines)
      if (this.#explainRules.dataset.signature !== signature) {
        this.#explainRules.dataset.signature = signature
        fill(this.#explainRules, lines.map((line) => h('li', { class: 'reason reason-note' }, line)))
      }
    }
  }
}

export const refClassSettingsTag = 'rc-ref-class-settings'
define(refClassSettingsTag, RefClassSettings)

/** Makes the settings for an organization (no project) or a project. */
export function refClassSettings(organization: string, projectId?: string): RefClassSettings {
  const el = document.createElement(refClassSettingsTag) as RefClassSettings
  el.organization = organization
  el.projectId = projectId
  return el
}
