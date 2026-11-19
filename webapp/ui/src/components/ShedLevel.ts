import { api } from '~/api/client.ts'
import type { EnumChoice, ShedActions, ShedState } from '~/api/csilapi/types.gen.ts'
import { ChoiceSelect } from '~/components/ChoiceInput.ts'
import { field } from '~/components/Field.ts'
import { Action, ActionError } from '~/components/ManagementPage.ts'
import type { CheckBox, PushButton } from '@catalystcommunity/controls'
import { button, checkBox, confirmAction } from '~/ui/controls.ts'
import { fill, h, setText, show } from '~/ui/dom.ts'
import { RcElement, define } from '~/ui/element.ts'
import { Loader } from '~/ui/load.ts'

export const SHED_CHOICES: EnumChoice[] = [
  { value: 'normal', label: 'Normal', description: 'All work runs.' },
  {
    value: 'degraded',
    label: 'Degraded',
    description: 'Skips drafts (where draft PRs is "skip when shedding"), pauses schedules, and lowers the priority of pull requests.',
  },
  { value: 'critical', label: 'Critical', description: 'Degraded, and also skips advisory workflows and refuses manual runs from non-administrators.' },
]

export const DRAFT_CHOICES: EnumChoice[] = [
  { value: 'run', label: 'Run', description: 'A draft pull request runs like any other pull request.' },
  { value: 'skip', label: 'Skip', description: 'A draft runs nothing. It runs when it becomes ready for review.' },
  { value: 'skip_when_shedding', label: 'Skip when shedding', description: 'A draft runs, except while the shed level skips drafts.' },
]

/** The texts of the actions that are on, in a fixed order. */
export function shedActionTexts(actions: ShedActions): string[] {
  const texts: string[] = []
  if (actions.skipDrafts) texts.push('Skips drafts where draft PRs is "skip when shedding"')
  if (actions.pauseSchedules) texts.push('Pauses schedules')
  if (actions.lowerPrPriority) texts.push('Lowers the priority of pull requests')
  if (actions.skipAdvisory) texts.push('Skips advisory workflows')
  if (actions.refuseManualRuns) texts.push('Refuses manual runs from non-administrators')
  return texts
}

/** The shed actions, in the order the editor shows them. */
export const SHED_ACTIONS: { key: keyof ShedActions; label: string }[] = [
  { key: 'skipDrafts', label: 'Skip drafts (projects with draft PRs "skip when shedding")' },
  { key: 'pauseSchedules', label: 'Pause schedules' },
  { key: 'lowerPrPriority', label: 'Lower the priority of pull requests' },
  { key: 'skipAdvisory', label: 'Skip advisory workflows' },
  { key: 'refuseManualRuns', label: 'Refuse manual runs from non-administrators' },
]

function levelLabel(level: string): string {
  return SHED_CHOICES.find((c) => c.value === level)?.label ?? level
}

/**
 * The shed level control: the global level (no orgId) on the Admin page, or
 * the level of one organization on its settings page. The level in force is
 * the higher of the global and the organization level. A change saves at
 * once, and the coordinator records it in the audit log.
 */
export class ShedLevelCard extends RcElement {
  /** True for the global level. Else the level of the organization orgId. */
  global = false
  /** The organization ID (organization scope). Nothing loads while it is empty. */
  orgId = ''

  #load = new Loader<string, ShedState>(this, async (key) => (await api.getShedState(key === 'global' ? {} : { orgId: key })).state)
  #save = new Action(() => this.invalidate())
  #error = new ActionError()
  #select!: ChoiceSelect
  #intro!: HTMLParagraphElement
  #inForce!: HTMLParagraphElement
  #actions!: HTMLUListElement
  #loading!: HTMLParagraphElement
  #body!: HTMLDivElement
  // The actions editor (organization scope only).
  #editor!: HTMLDivElement
  #boxes: { degraded: CheckBox; critical: CheckBox }[] = []
  #saveActions!: PushButton
  #resetActions!: PushButton
  #seeded = ''

  setScope(orgId: string): void {
    if (this.orgId === orgId) return
    this.orgId = orgId
    this.invalidate()
  }

  #key(): string | undefined {
    if (this.global) return 'global'
    return this.orgId || undefined
  }

  protected build(): void {
    this.#select = new ChoiceSelect({ choices: SHED_CHOICES, value: 'normal', onChange: (value) => void this.#set(value) })
    this.#intro = h('p', { class: 'meta' })
    this.#inForce = h('p', { class: 'shed-in-force' })
    this.#actions = h('ul', { class: 'readonly-list shed-actions' })
    this.#loading = h('p', { class: 'meta' }, 'Loading…')
    const levelField = field({
      label: 'Shed level',
      hint: 'Overload protection. Use degraded or critical while the coordinator or the workers cannot keep up.',
      control: this.#select.el,
    })
    this.#boxes = SHED_ACTIONS.map(() => ({
      degraded: checkBox('', {}),
      critical: checkBox('', {}),
    }))
    SHED_ACTIONS.forEach((action, i) => {
      this.#boxes[i].degraded.setAttribute('aria-label', `${action.label}: degraded`)
      this.#boxes[i].critical.setAttribute('aria-label', `${action.label}: critical`)
    })
    this.#saveActions = button('Save actions', { variant: 'primary', onClick: () => void this.#storeActions() })
    this.#resetActions = button('Use the built-in actions', { onClick: () => void this.#reset() })
    this.#editor = h(
      'div',
      { class: 'shed-actions-editor', hidden: true },
      h('h4', { class: 'section-title' }, 'Actions of each level'),
      h('p', { class: 'meta' }, 'What each level does in this organization. A change applies to the next event; work that runs now continues.'),
      h(
        'div',
        { class: 'scroll-x' },
        h(
          'table',
          { class: 'table shed-actions-table' },
          h('thead', null, h('tr', null, h('th', null, 'Action'), h('th', null, 'Degraded'), h('th', null, 'Critical'))),
          h(
            'tbody',
            null,
            SHED_ACTIONS.map((action, i) => h('tr', null, h('td', null, action.label), h('td', null, this.#boxes[i].degraded), h('td', null, this.#boxes[i].critical))),
          ),
        ),
      ),
      h('div', { class: 'row' }, this.#saveActions, this.#resetActions),
    )
    this.#body = h('div', { hidden: true }, levelField.el, this.#inForce, this.#actions, this.#editor)
    this.classList.add('card')
    this.append(h('h3', { class: 'section-title' }, 'Shed level'), this.#intro, this.#error.el, this.#loading, this.#body)
  }

  async #set(level: string): Promise<void> {
    const ok = await this.#save.run('The shed level could not be saved.', () => api.setShedLevel(this.global ? { level } : { level, orgId: this.orgId }))
    if (ok && this.isActive()) await this.#load.reload()
  }

  #actionsOf(level: 'degraded' | 'critical'): ShedActions {
    const out = {} as ShedActions
    SHED_ACTIONS.forEach((action, i) => {
      out[action.key] = this.#boxes[i][level].checked
    })
    return out
  }

  async #storeActions(): Promise<void> {
    const request = { orgId: this.orgId, degraded: this.#actionsOf('degraded'), critical: this.#actionsOf('critical') }
    const ok = await this.#save.run('The shed actions could not be saved.', () => api.setShedActions(request))
    if (ok && this.isActive()) {
      this.#seeded = ''
      await this.#load.reload()
    }
  }

  async #reset(): Promise<void> {
    if (!(await confirmAction('Use the built-in actions of each level for this organization?', { okLabel: 'Use built-in actions', danger: false }))) return
    const ok = await this.#save.run('The shed actions could not be reset.', () => api.setShedActions({ orgId: this.orgId, reset: true }))
    if (ok && this.isActive()) {
      this.#seeded = ''
      await this.#load.reload()
    }
  }

  #seed(state: ShedState): void {
    // Seed the checkboxes one time for each stored state, so a refresh does
    // not throw away a change that is not saved yet.
    const key = JSON.stringify([state.degradedActions, state.criticalActions])
    if (key === this.#seeded) return
    this.#seeded = key
    SHED_ACTIONS.forEach((action, i) => {
      this.#boxes[i].degraded.checked = Boolean(state.degradedActions[action.key])
      this.#boxes[i].critical.checked = Boolean(state.criticalActions[action.key])
    })
  }

  render(): void {
    this.#load.sync(this.#key())
    const state = this.#load.state.data
    setText(
      this.#intro,
      !this.global
        ? 'The level of this organization. The level in force is the higher of this level and the global level.'
        : 'The global level applies to every organization. An organization can set a higher level of its own.',
    )
    this.#error.set(this.#save.error ?? this.#load.state.error?.message)
    show(this.#loading, !state && !this.#load.state.error)
    show(this.#body, state)
    if (!state) return
    const own = this.global ? state.globalLevel : (state.orgLevel ?? 'normal')
    this.#select.el.disabled = this.#save.busy
    if (!this.#save.busy) this.#select.update(SHED_CHOICES, own)
    const source = !this.global && state.source === 'global' ? ' (from the global level)' : ''
    setText(this.#inForce, `In force now: ${levelLabel(state.level)}${source}.`)
    show(this.#editor, !this.global)
    if (!this.global) {
      this.#seed(state)
      for (const pair of this.#boxes) {
        pair.degraded.disabled = this.#save.busy
        pair.critical.disabled = this.#save.busy
      }
      this.#saveActions.disabled = this.#save.busy
      this.#resetActions.disabled = this.#save.busy
    }
    const texts = shedActionTexts(state.actions)
    const lines = texts.length ? texts : ['No work is skipped.']
    const signature = lines.join('\n')
    if (this.#actions.dataset.signature !== signature) {
      this.#actions.dataset.signature = signature
      fill(this.#actions, lines.map((line) => h('li', null, line)))
    }
  }
}

export const shedLevelCardTag = 'rc-shed-level-card'
define(shedLevelCardTag, ShedLevelCard)

/** Makes the control for the global level (no argument) or an organization. */
export function shedLevelCard(orgId?: string): ShedLevelCard {
  const el = document.createElement(shedLevelCardTag) as ShedLevelCard
  el.global = orgId === undefined
  el.orgId = orgId ?? ''
  return el
}
