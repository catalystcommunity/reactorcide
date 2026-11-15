import type { CheckBox, PushButton, TextField } from '@catalystcommunity/controls'
import { api } from '~/api/client.ts'
import type { EnumChoice, ProjectDetail, ScheduleFireSummary, ScheduleSummary } from '~/api/csilapi/types.gen.ts'
import { ChoiceSelect } from '~/components/ChoiceInput.ts'
import { field, type Field } from '~/components/Field.ts'
import { Action, ActionError, describeError } from '~/components/ManagementPage.ts'
import { ProjectCard } from '~/components/ProjectCard.ts'
import { setStatus, statusBadge } from '~/components/StatusBadge.ts'
import { button, checkBox, clearField, confirmAction, setButtonText, textField } from '~/ui/controls.ts'
import { h, setAttr, setText, show, syncKeyed } from '~/ui/dom.ts'
import { define } from '~/ui/element.ts'
import { Loader } from '~/ui/load.ts'
import { href } from '~/ui/router.ts'
import '~/components/reasons.css'

/** A timestamp as a local date and time, past or future. */
export function when(iso: string | undefined): string {
  if (!iso) return '—'
  const t = Date.parse(iso)
  return Number.isNaN(t) ? '—' : new Date(t).toLocaleString()
}

/**
 * Scheduled builds of this project.
 *
 * A schedule lives in the coordinator and names a workflow in the repository
 * on one branch. The coordinator fires it: it reads the branch head, records
 * a schedule event, and evaluates only that workflow. A workflow that is gone
 * from the branch shows here as the schedule's last error; the schedule stays
 * until somebody deletes it.
 */
class SchedulesCard extends ProjectCard {
  #list = new Loader<string, { rows: ScheduleSummary[]; error: string | undefined }>(this, async (projectId) => {
    try {
      return { rows: (await api.listSchedules({ projectId })).schedules, error: undefined }
    } catch (cause) {
      return { rows: [] as ScheduleSummary[], error: describeError(cause, 'The schedules could not be loaded.') }
    }
  })
  #fires = new Loader<string, { rows: ScheduleFireSummary[]; error: string | undefined }>(this, async (scheduleId) => {
    try {
      return { rows: (await api.listScheduleFires({ scheduleId, limit: 20 })).fires, error: undefined }
    } catch (cause) {
      return { rows: [] as ScheduleFireSummary[], error: describeError(cause, 'The fires could not be loaded.') }
    }
  })
  #byId = new Map<string, ScheduleSummary>()
  #firesFor = ''
  #editing = ''

  // Form
  #save = new Action(() => this.invalidate())
  #saveError = new ActionError()
  #rowAction = new Action(() => this.invalidate())
  #rowError = new ActionError()
  #formTitle!: HTMLHeadingElement
  #name!: TextField
  #branch!: TextField
  #ref!: TextField
  #cron!: TextField
  #timezone!: TextField
  #enabled!: CheckBox
  #fields!: Record<'name' | 'branch' | 'ref' | 'cron', Field>
  #submit!: PushButton
  #cancelEdit!: PushButton
  #touched = false

  // List
  #listError!: HTMLDivElement
  #empty!: HTMLDivElement
  #table!: HTMLDivElement
  #body!: HTMLTableSectionElement

  // Fires
  #firesPanel!: HTMLDivElement
  #firesTitle!: HTMLHeadingElement
  #firesError!: HTMLDivElement
  #firesEmpty!: HTMLParagraphElement
  #firesBody!: HTMLTableSectionElement

  protected build(): void {
    const rerender = (): void => this.invalidate()
    this.#name = textField({ placeholder: 'nightly', onInput: rerender })
    this.#branch = textField({ placeholder: 'main', onInput: rerender })
    this.#ref = textField({ placeholder: 'nightly or .reactorcide/workflows/nightly.yaml', mono: true, onInput: rerender })
    this.#cron = textField({ placeholder: '0 3 * * *', mono: true, onInput: rerender })
    this.#timezone = textField({ value: 'UTC', mono: true })
    this.#enabled = checkBox('Enabled', { checked: true })
    this.#fields = {
      name: field({ label: 'Name', required: true, control: this.#name }),
      branch: field({ label: 'Branch', required: true, hint: 'The schedule runs the head of this branch.', control: this.#branch }),
      ref: field({
        label: 'Workflow',
        required: true,
        hint: 'The workflow id, or its file path under .reactorcide/workflows/.',
        control: this.#ref,
      }),
      cron: field({
        label: 'Cron expression',
        required: true,
        hint: 'Five fields (minute hour day month weekday), or @daily, @hourly.',
        control: this.#cron,
      }),
    }
    this.#submit = button('Add schedule', { type: 'submit', variant: 'primary' })
    this.#cancelEdit = button('Cancel edit', { onClick: () => this.#resetForm() })
    this.#formTitle = h('h4', { class: 'section-title' }, 'Add a schedule')

    const form = h(
      'form',
      { class: 'schedule-form', novalidate: true, onSubmit: (event: Event) => void this.#submitForm(event) },
      this.#formTitle,
      this.#saveError.el,
      this.#fields.name.el,
      this.#fields.branch.el,
      this.#fields.ref.el,
      this.#fields.cron.el,
      field({ label: 'Timezone', hint: 'An IANA name, for example Europe/Berlin. Empty means UTC.', control: this.#timezone }).el,
      h('div', { class: 'checkbox-row' }, this.#enabled),
      h('div', { class: 'row' }, this.#submit, this.#cancelEdit),
    )

    this.#listError = h('div', { class: 'alert alert-error', role: 'alert', hidden: true })
    this.#empty = h('div', { class: 'empty', hidden: true }, 'No schedules.')
    this.#body = h('tbody')
    this.#table = h(
      'div',
      { class: 'scroll-x', hidden: true },
      h(
        'table',
        { class: 'table' },
        h(
          'thead',
          null,
          h(
            'tr',
            null,
            h('th', null, 'Schedule'),
            h('th', null, 'Branch'),
            h('th', null, 'When'),
            h('th', null, 'Enabled'),
            h('th', null, 'Next fire'),
            h('th', null, 'Last fire'),
            h('th', null, h('span', { class: 'sr-only' }, 'Actions')),
          ),
        ),
        this.#body,
      ),
    )

    this.#firesTitle = h('h4', { class: 'section-title' })
    this.#firesError = h('div', { class: 'alert alert-error', role: 'alert', hidden: true })
    this.#firesEmpty = h('p', { class: 'meta', hidden: true }, 'This schedule has not fired yet.')
    this.#firesBody = h('tbody')
    this.#firesPanel = h(
      'div',
      { class: 'schedule-fires', hidden: true },
      h('div', { class: 'card-header' }, this.#firesTitle, button('Close', { small: true, variant: 'flat', onClick: () => this.#showFires('') })),
      this.#firesError,
      this.#firesEmpty,
      h(
        'div',
        { class: 'scroll-x' },
        h(
          'table',
          { class: 'table' },
          h('thead', null, h('tr', null, h('th', null, 'Fire time'), h('th', null, 'Status'), h('th', null, 'Event'), h('th', null, 'Error'))),
          this.#firesBody,
        ),
      ),
    )

    this.append(
      h('div', { class: 'card-header' }, h('div', null, h('h3', null, 'Schedules'), h('p', { class: 'meta' }, 'Run a workflow on a branch at fixed times.'))),
      this.#rowError.el,
      this.#listError,
      this.#empty,
      this.#table,
      this.#firesPanel,
      form,
    )
  }

  #values(): { name: string; branch: string; workflowRef: string; cron: string; timezone: string } {
    return {
      name: this.#name.value.trim(),
      branch: this.#branch.value.trim(),
      workflowRef: this.#ref.value.trim(),
      cron: this.#cron.value.trim(),
      timezone: this.#timezone.value.trim() || 'UTC',
    }
  }

  #errors(): Partial<Record<'name' | 'branch' | 'ref' | 'cron', string>> {
    const v = this.#values()
    return {
      name: v.name ? undefined : 'A schedule needs a name.',
      branch: v.branch ? undefined : 'Name the branch to run.',
      ref: v.workflowRef ? undefined : 'Name the workflow to run.',
      cron: v.cron ? undefined : 'A schedule needs a cron expression.',
    }
  }

  async #submitForm(event: Event): Promise<void> {
    event.preventDefault()
    this.#touched = true
    const errors = this.#errors()
    if (Object.values(errors).some(Boolean) || !this.project) {
      this.invalidate()
      return
    }
    const v = this.#values()
    const projectId = this.project.projectId
    const editing = this.#editing
    const ok = await this.#save.run('The schedule could not be saved.', () =>
      editing
        ? api.updateSchedule({ scheduleId: editing, ...v, enabled: this.#enabled.checked })
        : api.createSchedule({ projectId, ...v, enabled: this.#enabled.checked }),
    )
    if (!ok || !this.isActive()) return
    this.#resetForm()
    await this.#list.reload()
  }

  #resetForm(): void {
    this.#editing = ''
    this.#touched = false
    for (const control of [this.#name, this.#branch, this.#ref, this.#cron]) clearField(control)
    this.#timezone.value = 'UTC'
    this.#enabled.checked = true
    this.#save.clearError()
    this.invalidate()
  }

  #edit(id: string): void {
    const s = this.#byId.get(id)
    if (!s) return
    this.#editing = id
    this.#name.value = s.name
    this.#branch.value = s.branch
    this.#ref.value = s.workflowRef
    this.#cron.value = s.cron
    this.#timezone.value = s.timezone
    this.#enabled.checked = s.enabled
    this.invalidate()
    this.#name.focus()
  }

  async #toggle(id: string, enabled: boolean): Promise<void> {
    await this.#rowAction.run('The schedule could not be changed.', () => api.updateSchedule({ scheduleId: id, enabled }))
    if (this.isActive()) await this.#list.reload()
  }

  async #delete(id: string): Promise<void> {
    const s = this.#byId.get(id)
    if (!s) return
    const ok = await confirmAction(`Delete the schedule "${s.name}"? It stops firing. Its past events stay.`, { okLabel: 'Delete schedule' })
    if (!ok || !this.isActive()) return
    const done = await this.#rowAction.run('The schedule could not be deleted.', () => api.deleteSchedule({ scheduleId: id }))
    if (!done || !this.isActive()) return
    if (this.#editing === id) this.#resetForm()
    if (this.#firesFor === id) this.#showFires('')
    await this.#list.reload()
  }

  #showFires(id: string): void {
    this.#firesFor = this.#firesFor === id ? '' : id
    this.invalidate()
  }

  render(): void {
    const project = this.project
    if (!project) return
    this.#list.sync(project.projectId)
    const data = this.#list.state.data
    const rows = data?.rows ?? []
    this.#byId = new Map(rows.map((r) => [r.scheduleId, r]))

    this.#rowError.set(this.#rowAction.error)
    show(this.#listError, data?.error)
    setText(this.#listError, data?.error ?? '')
    show(this.#empty, data && !data.error && rows.length === 0)
    show(this.#table, rows.length > 0)
    syncKeyed(this.#body, rows, (r) => r.scheduleId, (r) => this.#createRow(r.scheduleId), (row, r) => this.#updateRow(row, r))

    // Form
    const errors = this.#errors()
    for (const key of ['name', 'branch', 'ref', 'cron'] as const) this.#fields[key].setError(this.#touched ? errors[key] : undefined)
    this.#saveError.set(this.#save.error)
    this.#submit.disabled = this.#save.busy
    setButtonText(this.#submit, this.#save.busy ? 'Saving…' : this.#editing ? 'Save changes' : 'Add schedule')
    setText(this.#formTitle, this.#editing ? `Edit "${this.#byId.get(this.#editing)?.name ?? ''}"` : 'Add a schedule')
    show(this.#cancelEdit, this.#editing)

    // Fires
    if (this.#firesFor && !this.#byId.has(this.#firesFor) && data) this.#firesFor = ''
    this.#fires.sync(this.#firesFor || undefined)
    show(this.#firesPanel, this.#firesFor)
    if (this.#firesFor) {
      setText(this.#firesTitle, `Recent fires of "${this.#byId.get(this.#firesFor)?.name ?? ''}"`)
      const fires = this.#fires.state.data
      show(this.#firesError, fires?.error)
      setText(this.#firesError, fires?.error ?? '')
      show(this.#firesEmpty, fires && !fires.error && fires.rows.length === 0)
      syncKeyed(this.#firesBody, fires?.rows ?? [], (f) => f.fireId, createFireRow, updateFireRow)
    }
  }

  #createRow(id: string): HTMLTableRowElement {
    const enabled = checkBox('', { onChange: (next) => void this.#toggle(id, next) })
    const name = this.#byId.get(id)?.name ?? ''
    enabled.setAttribute('aria-label', `Enabled: ${name}`)
    return h(
      'tr',
      null,
      h('td', null, h('div', { class: 'sch-name' }), h('div', { class: 'meta mono sch-ref' })),
      h('td', { class: 'mono sch-branch' }),
      h('td', null, h('div', { class: 'mono sch-cron' }), h('div', { class: 'meta sch-tz' })),
      h('td', null, enabled),
      h('td', { class: 'meta sch-next' }),
      h('td', null, h('a', { class: 'meta sch-last' }), h('span', { class: 'meta sch-last-plain' }), h('div', { class: 'schedule-error sch-error' })),
      h(
        'td',
        null,
        h(
          'div',
          { class: 'schedule-actions' },
          button('Fires', { small: true, variant: 'flat', ariaLabel: `Fires of ${name}`, onClick: () => this.#showFires(id) }),
          button('Edit', { small: true, ariaLabel: `Edit ${name}`, onClick: () => this.#edit(id) }),
          button('Delete', { small: true, variant: 'danger', ariaLabel: `Delete ${name}`, onClick: () => void this.#delete(id) }),
        ),
      ),
    )
  }

  #updateRow(row: HTMLTableRowElement, s: ScheduleSummary): void {
    setText(row.querySelector('.sch-name')!, s.name)
    setText(row.querySelector('.sch-ref')!, s.workflowRef)
    setText(row.querySelector('.sch-branch')!, s.branch)
    setText(row.querySelector('.sch-cron')!, s.cron)
    setText(row.querySelector('.sch-tz')!, s.timezone || 'UTC')
    const enabled = row.querySelector<CheckBox>('check-box')!
    enabled.disabled = this.#rowAction.busy
    if (!this.#rowAction.busy && enabled.checked !== s.enabled) enabled.checked = s.enabled
    setText(row.querySelector('.sch-next')!, s.enabled ? when(s.nextFireAt) : 'Disabled')
    const last = row.querySelector<HTMLAnchorElement>('.sch-last')!
    const lastPlain = row.querySelector<HTMLElement>('.sch-last-plain')!
    show(last, s.lastEventId)
    show(lastPlain, !s.lastEventId)
    setAttr(last, 'href', s.lastEventId ? href(`/events/${s.lastEventId}`) : null)
    setText(last, when(s.lastFireAt))
    setText(lastPlain, s.lastFireAt ? when(s.lastFireAt) : 'Never')
    const error = row.querySelector<HTMLElement>('.sch-error')!
    show(error, s.lastError)
    setText(error, s.lastError)
  }
}
define('rc-project-schedules-card', SchedulesCard)

function createFireRow(): HTMLTableRowElement {
  return h(
    'tr',
    null,
    h('td', { class: 'meta fire-time' }),
    h('td', null, statusBadge()),
    h('td', null, h('a', { class: 'mono fire-event' }), h('span', { class: 'meta fire-none' }, '—')),
    h('td', { class: 'schedule-error fire-error' }),
  )
}

function updateFireRow(row: HTMLTableRowElement, f: ScheduleFireSummary): void {
  setText(row.querySelector('.fire-time')!, when(f.fireTime))
  // "fired" is the success word for a fire; show it with the success tone.
  setStatus(row.querySelector('.status')!, f.status === 'fired' ? 'completed' : f.status === 'firing' ? 'running' : f.status)
  setText(row.querySelector('.status-text')!, f.status)
  const link = row.querySelector<HTMLAnchorElement>('.fire-event')!
  show(link, f.eventId)
  show(row.querySelector<HTMLElement>('.fire-none')!, !f.eventId)
  setAttr(link, 'href', f.eventId ? href(`/events/${f.eventId}`) : null)
  setText(link, f.eventId ? f.eventId.slice(0, 12) : '')
  setText(row.querySelector('.fire-error')!, f.error)
}

export const DUPLICATE_CHOICES: EnumChoice[] = [
  { value: 'run', label: 'Run again', description: 'A new delivery of a change already seen starts a new eval.' },
  { value: 'skip', label: 'Skip', description: 'A new delivery of a change already seen is recorded as a duplicate and does not run.' },
]

export const DYNAMIC_CHOICES: EnumChoice[] = [
  { value: 'allow', label: 'Allow', description: 'Running jobs may start more jobs (runnerlib trigger).' },
  { value: 'deny', label: 'Deny', description: 'Only the jobs that workflow files declare run.' },
]

const INHERIT: EnumChoice = { value: 'inherit', label: 'Organization default', description: 'Use the setting of the organization.' }

function choiceLabel(choices: EnumChoice[], value: string | undefined): string {
  return choices.find((c) => c.value === value)?.label ?? value ?? 'unknown'
}

/**
 * How this project treats repeated deliveries and runtime-started jobs. Each
 * setting can follow the organization or override it; the card shows the
 * value in force either way.
 */
class EventSettingsCard extends ProjectCard {
  initial!: ProjectDetail
  onSaved: () => Promise<void> = async () => {}

  #duplicate = 'inherit'
  #dynamic = 'inherit'
  #duplicateSelect!: ChoiceSelect
  #dynamicSelect!: ChoiceSelect
  #duplicateEffective!: HTMLDivElement
  #dynamicEffective!: HTMLDivElement
  #save = new Action(() => this.invalidate())
  #error = new ActionError()
  #saved!: HTMLDivElement
  #submit!: PushButton

  protected build(): void {
    // The draft is seeded once; a live project refresh does not overwrite it.
    this.#duplicate = this.initial.duplicateEvents || 'inherit'
    this.#dynamic = this.initial.dynamicJobs || 'inherit'
    this.#duplicateSelect = new ChoiceSelect({
      value: this.#duplicate,
      choices: [INHERIT, ...DUPLICATE_CHOICES],
      onChange: (value) => (this.#duplicate = value),
    })
    this.#dynamicSelect = new ChoiceSelect({
      value: this.#dynamic,
      choices: [INHERIT, ...DYNAMIC_CHOICES],
      onChange: (value) => (this.#dynamic = value),
    })
    this.#duplicateEffective = h('div', { class: 'field-hint' })
    this.#dynamicEffective = h('div', { class: 'field-hint' })
    this.#saved = h('div', { class: 'alert alert-info', role: 'status', hidden: true }, 'Event settings saved.')
    this.#submit = button('Save event settings', { type: 'submit', variant: 'primary' })
    // The value in force sits inside its field, under the control.
    const duplicateField = field({
      label: 'Repeated deliveries',
      hint: 'A new delivery for the same repository, event type, commit and pull request.',
      control: this.#duplicateSelect.el,
    })
    duplicateField.el.append(this.#duplicateEffective)
    const dynamicField = field({ label: 'Dynamic jobs', hint: 'Jobs that a running job starts, not declared in a workflow file.', control: this.#dynamicSelect.el })
    dynamicField.el.append(this.#dynamicEffective)
    this.append(
      h('h3', { class: 'section-title' }, 'Events'),
      h(
        'form',
        { novalidate: true, onSubmit: (event: Event) => void this.#submitForm(event) },
        this.#error.el,
        this.#saved,
        duplicateField.el,
        dynamicField.el,
        this.#submit,
      ),
    )
  }

  async #submitForm(event: Event): Promise<void> {
    event.preventDefault()
    const project = this.project
    if (!project) return
    show(this.#saved, false)
    const ok = await this.#save.run('The event settings could not be saved.', () =>
      api.updateProject({ projectId: project.projectId, duplicateEvents: this.#duplicate, dynamicJobs: this.#dynamic }),
    )
    if (!ok || !this.isActive()) return
    await this.onSaved()
    show(this.#saved, true)
  }

  render(): void {
    const p = this.project
    this.#duplicateSelect.update([INHERIT, ...DUPLICATE_CHOICES], this.#duplicate)
    this.#dynamicSelect.update([INHERIT, ...DYNAMIC_CHOICES], this.#dynamic)
    setText(this.#duplicateEffective, p?.effectiveDuplicateEvents ? `In force now: ${choiceLabel(DUPLICATE_CHOICES, p.effectiveDuplicateEvents)}.` : '')
    setText(this.#dynamicEffective, p?.effectiveDynamicJobs ? `In force now: ${choiceLabel(DYNAMIC_CHOICES, p.effectiveDynamicJobs)}.` : '')
    this.#error.set(this.#save.error)
    this.#submit.disabled = this.#save.busy
    setButtonText(this.#submit, this.#save.busy ? 'Saving…' : 'Save event settings')
  }
}
define('rc-project-event-settings-card', EventSettingsCard)

export type { SchedulesCard, EventSettingsCard }
