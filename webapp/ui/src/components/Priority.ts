import type { CheckBox, PushButton, TextField } from '@catalystcommunity/controls'
import { api } from '~/api/client.ts'
import type { EnumChoice, ListPriorityLanesResponse, PriorityLane, PriorityLaneMatch } from '~/api/csilapi/types.gen.ts'
import { ChoiceMultiSelect, ChoiceSelect } from '~/components/ChoiceInput.ts'
import { field, type Field } from '~/components/Field.ts'
import { Action, ActionError, describeError } from '~/components/ManagementPage.ts'
import { EVENT_TYPE_CHOICES, classListError, splitList } from '~/components/RefClasses.ts'
import { skeleton } from '~/components/States.ts'
import { button, checkBox, confirmAction, setButtonText, textField } from '~/ui/controls.ts'
import { h, setText, show, syncKeyed } from '~/ui/dom.ts'
import { RcElement, define } from '~/ui/element.ts'
import { Loader } from '~/ui/load.ts'
import { laneMatchText, signed } from '~/components/priorityText.ts'
import './priority.css'

export { laneMatchText, priorityText, shiftText } from '~/components/priorityText.ts'

/**
 * Priority lanes and group shifts (docs/priority.md). A lane maps the
 * trusted facts of a job (ref classes, event type, draft, job kind, retry) to
 * a base priority. The largest shift of the actor's groups moves it. Higher
 * runs sooner.
 */

const TRI_CHOICES: EnumChoice[] = [
  { value: 'any', label: 'Any', description: 'No condition.' },
  { value: 'true', label: 'Draft', description: 'Only draft pull requests.' },
  { value: 'false', label: 'Not a draft', description: 'Only work that is not a draft.' },
]

const KIND_CHOICES: EnumChoice[] = [
  { value: '', label: 'Any', description: 'Eval jobs and work jobs.' },
  { value: 'eval', label: 'Eval', description: 'Only the eval job of an event.' },
  { value: 'work', label: 'Work', description: 'Only jobs that are not an eval.' },
]

const NAME_PATTERN = /^[a-z0-9][a-z0-9-]{0,62}$/

type Key = { organization: string; projectId?: string }

/**
 * The lanes of an organization (no project) or of a project: the effective
 * lanes in evaluation order, and a form to add, replace or reset a lane.
 * Set `organization` (the org NAME) before it connects, or call `setScope()`.
 */
export class PriorityLaneSettings extends RcElement {
  organization = ''
  projectId: string | undefined = undefined

  #load = new Loader<Key, ListPriorityLanesResponse>(this, (key) => api.listPriorityLanes(key))
  #save = new Action(() => this.invalidate())
  #saveError = new ActionError()
  #touched = false
  #eventTypes: string[] = []
  #draft = 'any'
  #kind = ''

  #loading!: HTMLElement
  #loadError!: HTMLDivElement
  #body!: HTMLDivElement
  #tbody!: HTMLTableSectionElement
  #readonly!: HTMLDivElement
  #form!: HTMLFormElement
  #saved!: HTMLDivElement
  #submit!: PushButton
  #name!: TextField
  #nameField!: Field
  #base!: TextField
  #baseField!: Field
  #position!: TextField
  #description!: TextField
  #setMatch!: CheckBox
  #matchBox!: HTMLDivElement
  #classes!: TextField
  #classesField!: Field
  #notClasses!: TextField
  #events!: ChoiceMultiSelect
  #draftSelect!: ChoiceSelect
  #kindSelect!: ChoiceSelect
  #retry!: CheckBox
  #enabled!: CheckBox

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
    this.#tbody = h('tbody')
    const table = h(
      'div',
      { class: 'scroll-x' },
      h(
        'table',
        { class: 'table priority-lanes' },
        h(
          'thead',
          null,
          h('tr', null, h('th', null, 'Lane'), h('th', null, 'Base'), h('th', null, 'Position'), h('th', null, 'Matches'), h('th', null, 'Scope'), h('th')),
        ),
        this.#tbody,
      ),
    )
    this.#readonly = h('div', { class: 'alert alert-info', hidden: true })

    this.#name = textField({ mono: true, placeholder: 'pr-ready', onInput: rerender })
    this.#nameField = field({ label: 'Lane name', required: true, hint: 'A built-in name replaces that lane. Lowercase letters, digits and hyphens.', control: this.#name })
    this.#base = textField({ mono: true, placeholder: '-100', onInput: rerender })
    this.#baseField = field({ label: 'Base priority', required: true, hint: 'From -500 to 500. Higher runs sooner.', control: this.#base })
    this.#position = textField({ mono: true, placeholder: 'Keep' })
    this.#description = textField({ placeholder: 'Pull requests that are ready for review.' })
    this.#setMatch = checkBox('Set the match facts', { checked: false })
    this.#setMatch.addEventListener('change', rerender)
    this.#classes = textField({ mono: true, placeholder: 'pr, release', onInput: rerender })
    this.#classesField = field({ label: 'Ref classes (any of)', hint: 'A comma list. Empty is no condition.', control: this.#classes })
    this.#notClasses = textField({ mono: true, placeholder: 'fork' })
    this.#events = new ChoiceMultiSelect({ ariaLabel: 'Event types', choices: EVENT_TYPE_CHOICES, onChange: (values) => { this.#eventTypes = values } })
    this.#draftSelect = new ChoiceSelect({ ariaLabel: 'Draft', value: 'any', choices: TRI_CHOICES, onChange: (v) => { this.#draft = v } })
    this.#kindSelect = new ChoiceSelect({ ariaLabel: 'Job kind', value: '', choices: KIND_CHOICES, onChange: (v) => { this.#kind = v } })
    this.#retry = checkBox('Retries only')
    this.#matchBox = h(
      'div',
      { hidden: true },
      h('div', { class: 'form-row' }, this.#classesField.el, field({ label: 'Ref classes (none of)', control: this.#notClasses }).el),
      field({ label: 'Event types', hint: 'Select none for every event type.', control: this.#events.el }).el,
      h('div', { class: 'form-row' }, field({ label: 'Draft', control: this.#draftSelect.el }).el, field({ label: 'Job kind', control: this.#kindSelect.el }).el),
      this.#retry,
    )
    this.#enabled = checkBox('Enabled', { checked: true })
    this.#saved = h('div', { class: 'alert alert-info', role: 'status', hidden: true }, 'Lane saved.')
    this.#submit = button('Save lane', { type: 'submit', variant: 'primary' })
    this.#form = h(
      'form',
      { novalidate: true, class: 'priority-lane-form', onSubmit: (event: Event) => void this.#submitForm(event) },
      h('h4', { class: 'section-title' }, 'Add or replace a lane'),
      h('p', { class: 'meta' }, 'Without match facts, a lane with a built-in or organization name keeps that match and position. Only the base changes.'),
      this.#saveError.el,
      this.#saved,
      h('div', { class: 'form-row' }, this.#nameField.el, this.#baseField.el, field({ label: 'Position', hint: 'Lower is evaluated first. Empty keeps it.', control: this.#position }).el),
      field({ label: 'Description', control: this.#description }).el,
      this.#setMatch,
      this.#matchBox,
      h('div', { class: 'row' }, this.#enabled, this.#submit),
    )

    this.#loading = skeleton(3)
    this.#loadError = h('div', { class: 'alert alert-error', role: 'alert', hidden: true })
    this.#body = h(
      'div',
      { hidden: true },
      h('p', { class: 'meta' }, 'The first lane that matches gives the base priority. The largest shift of the groups of the actor moves it (Groups page). A priority that a job asks for moves it at most 50 in its lane. While the shed level lowers pull requests, pull request work moves 100 down.'),
      table,
      this.#readonly,
      this.#form,
    )
    this.classList.add('card', 'priority-lane-settings')
    this.append(h('h3', { class: 'section-title' }, 'Priority lanes'), this.#loadError, this.#loading, this.#body)
  }

  /** Puts a lane into the form, to change it. */
  edit(lane: PriorityLane): void {
    this.#name.value = lane.name
    this.#base.value = String(lane.base)
    this.#position.value = String(lane.position)
    this.#description.value = lane.description ?? ''
    this.#classes.value = (lane.match.classesAny ?? []).join(', ')
    this.#notClasses.value = (lane.match.classesNone ?? []).join(', ')
    this.#eventTypes = [...(lane.match.eventTypes ?? [])]
    this.#draft = lane.match.draft === undefined ? 'any' : lane.match.draft ? 'true' : 'false'
    this.#kind = lane.match.jobKind ?? ''
    this.#retry.checked = Boolean(lane.match.retry)
    this.#enabled.checked = lane.enabled
    this.#setMatch.checked = false
    this.invalidate()
    this.#base.focus()
  }

  #errors(): { name?: string; base?: string; classes?: string } {
    const name = this.#name.value.trim()
    const baseText = this.#base.value.trim()
    const base = Number(baseText)
    return {
      name: !name ? 'A lane needs a name.' : NAME_PATTERN.test(name) ? undefined : 'Use lowercase letters, digits and hyphens.',
      base: !baseText || !Number.isInteger(base) ? 'Give a whole number.' : base < -500 || base > 500 ? 'Use a value from -500 to 500.' : undefined,
      classes: this.#setMatch.checked ? classListError(splitList(this.#classes.value), false) : undefined,
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
    if (errors.name || errors.base || errors.classes) {
      this.invalidate()
      return
    }
    const request: Parameters<typeof api.putPriorityLane>[0] = {
      ...key,
      name: this.#name.value.trim(),
      base: Number(this.#base.value.trim()),
      enabled: this.#enabled.checked,
    }
    const position = this.#position.value.trim()
    if (position && Number.isInteger(Number(position))) request.position = Number(position)
    const description = this.#description.value.trim()
    if (description) request.description = description
    if (this.#setMatch.checked) {
      const match: PriorityLaneMatch = {
        classesAny: splitList(this.#classes.value),
        classesNone: splitList(this.#notClasses.value),
        eventTypes: [...this.#eventTypes],
      }
      if (this.#draft !== 'any') match.draft = this.#draft === 'true'
      if (this.#kind) match.jobKind = this.#kind
      if (this.#retry.checked) match.retry = true
      request.match = match
    }
    const ok = await this.#save.run('The lane could not be saved.', () => api.putPriorityLane(request))
    if (!ok || !this.isActive()) return
    this.#touched = false
    show(this.#saved, true)
    await this.#load.reload()
  }

  async #delete(lane: PriorityLane): Promise<void> {
    const key = this.#key()
    if (!key) return
    const builtin = this.#load.state.data?.builtin.some((b) => b.name === lane.name)
    const text = builtin
      ? `Reset the lane "${lane.name}"? The built-in lane applies again to new jobs.`
      : `Delete the lane "${lane.name}"? New jobs no longer get it.`
    if (!(await confirmAction(text, { okLabel: builtin ? 'Reset lane' : 'Delete lane', danger: true }))) return
    const ok = await this.#save.run('The lane could not be deleted.', () => api.deletePriorityLane({ ...key, name: lane.name }))
    if (ok && this.isActive()) await this.#load.reload()
  }

  render(): void {
    const key = this.#key()
    this.#load.sync(key)
    const state = this.#load.state
    const data = state.data
    const project = Boolean(this.projectId)
    show(this.#loading, !data && !state.error)
    show(this.#loadError, state.error && !data)
    setText(this.#loadError, state.error ? describeError(state.error, 'The priority lanes could not be loaded.') : '')
    show(this.#body, data)
    if (!data) return

    const ownScope = project ? 'project' : 'org'
    syncKeyed(
      this.#tbody,
      data.effective,
      (lane) => lane.name,
      () => {
        const row = h('tr', null, h('td', { class: 'mono lane-name' }), h('td', { class: 'mono lane-base' }), h('td', { class: 'mono lane-position' }),
          h('td', { class: 'meta lane-match' }), h('td', { class: 'lane-scope' }), h('td', { class: 'row lane-actions' })) as HTMLTableRowElement & { lane?: PriorityLane }
        return row
      },
      (row, lane) => {
        const r = row as HTMLTableRowElement & { lane?: PriorityLane }
        r.lane = lane
        setText(r.querySelector('.lane-name')!, `${lane.name}${lane.enabled ? '' : ' (off)'}`)
        setText(r.querySelector('.lane-base')!, signed(lane.base))
        setText(r.querySelector('.lane-position')!, String(lane.position))
        setText(r.querySelector('.lane-match')!, lane.matchText || laneMatchText(lane.match))
        setText(r.querySelector('.lane-scope')!, `${lane.scope ?? ''}${lane.replaces ? ` (replaces ${lane.replaces})` : ''}`)
        const holder = r.querySelector<HTMLElement>('.lane-actions')!
        // The buttons are made one time; they act on the current lane of the row.
        if (data.canManage && holder.childElementCount === 0) {
          holder.append(
            button('Edit', { small: true, ariaLabel: `Edit lane ${lane.name}`, onClick: () => this.edit(r.lane!) }),
            button('Remove', { small: true, variant: 'danger', ariaLabel: `Remove lane ${lane.name}`, onClick: () => void this.#delete(r.lane!) }),
          )
        }
        const remove = holder.children[1] as HTMLElement | undefined
        if (remove) show(remove, r.lane.scope === ownScope)
        show(holder, data.canManage)
      },
    )

    show(this.#form, data.canManage)
    show(this.#readonly, !data.canManage)
    setText(this.#readonly, project ? 'Only a project owner can change the project lanes.' : 'Only an organization administrator can change the organization lanes.')
    show(this.#matchBox, this.#setMatch.checked)
    const errors = this.#errors()
    this.#nameField.setError(this.#touched ? errors.name : undefined)
    this.#baseField.setError(this.#touched ? errors.base : undefined)
    this.#classesField.setError(this.#touched ? errors.classes : undefined)
    this.#events.update(EVENT_TYPE_CHOICES, this.#eventTypes)
    this.#draftSelect.update(TRI_CHOICES, this.#draft)
    this.#kindSelect.update(KIND_CHOICES, this.#kind)
    this.#saveError.set(this.#save.error)
    this.#submit.disabled = this.#save.busy
    setButtonText(this.#submit, this.#save.busy ? 'Saving…' : 'Save lane')
  }
}

export const priorityLaneSettingsTag = 'rc-priority-lane-settings'
define(priorityLaneSettingsTag, PriorityLaneSettings)

/** Makes the lane settings for an organization (no project) or a project. */
export function priorityLaneSettings(organization: string, projectId?: string): PriorityLaneSettings {
  const el = document.createElement(priorityLaneSettingsTag) as PriorityLaneSettings
  el.organization = organization
  el.projectId = projectId
  return el
}
