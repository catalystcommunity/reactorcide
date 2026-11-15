import type { PushButton, TextField } from '@catalystcommunity/controls'
import { button, textField } from '~/ui/controls.ts'
import { h, setText, show, syncKeyed, uid } from '~/ui/dom.ts'
import './chips.css'

/**
 * A list of short values shown as chips: subjects, paths, workflow ids.
 *
 * A person types one value and adds it. A value that is not valid is refused
 * with the reason, so the server never sees it. Each chip has its own remove
 * button. The controls are made one time; `set()` changes the chips in place.
 */
export interface ChipListOptions {
  /** The accessible name of the text field, for example "Add a subject". */
  ariaLabel: string
  placeholder?: string
  /** Returns the reason a value is not valid, or undefined. */
  validate?: (value: string) => string | undefined
  /** Monospace chips and input. */
  mono?: boolean
  onChange?: (values: string[]) => void
}

export class ChipList {
  readonly el: HTMLDivElement
  readonly input: TextField
  readonly #list: HTMLUListElement
  readonly #error: HTMLSpanElement
  readonly #add: PushButton
  readonly #options: ChipListOptions
  #values: string[] = []
  #disabled = false

  constructor(options: ChipListOptions) {
    this.#options = options
    const errorId = uid('chips-error')
    this.#list = h('ul', { class: 'chip-list' })
    this.#error = h('span', { class: 'field-error', id: errorId, role: 'alert', hidden: true })
    this.input = textField({ ariaLabel: options.ariaLabel, placeholder: options.placeholder, mono: options.mono })
    // Enter adds the value. It must not submit the form around the list.
    this.input.addEventListener('keydown', (event: KeyboardEvent) => {
      if (event.key !== 'Enter') return
      event.preventDefault()
      event.stopPropagation()
      this.#addTyped()
    })
    this.#add = button('Add', { small: true, ariaLabel: options.ariaLabel, onClick: () => this.#addTyped() })
    this.el = h('div', { class: 'chips' }, this.#list, h('div', { class: 'row chip-input' }, this.input, this.#add), this.#error)
    this.#sync()
  }

  values(): string[] {
    return [...this.#values]
  }

  /** Replaces the values from code. Sends no change. */
  set(values: readonly string[]): void {
    this.#values = [...new Set(values)]
    this.#sync()
  }

  setDisabled(disabled: boolean): void {
    if (this.#disabled === disabled) return
    this.#disabled = disabled
    this.input.disabled = disabled
    this.#add.disabled = disabled
    this.#sync()
  }

  /** Adds a value as a person would. Returns the reason when it is refused. */
  add(raw: string): string | undefined {
    const value = raw.trim()
    let reason: string | undefined
    if (!value) reason = 'Type a value first.'
    else if (this.#values.includes(value)) reason = `"${value}" is already in the list.`
    else reason = this.#options.validate?.(value)
    this.#showError(reason)
    if (reason) return reason
    this.#values = [...this.#values, value]
    this.#sync()
    this.#options.onChange?.(this.values())
    return undefined
  }

  #addTyped(): void {
    if (this.#disabled) return
    if (this.add(this.input.value) === undefined) this.input.value = ''
  }

  #remove(value: string): void {
    if (this.#disabled) return
    this.#values = this.#values.filter((v) => v !== value)
    this.#sync()
    this.#options.onChange?.(this.values())
  }

  #showError(reason: string | undefined): void {
    show(this.#error, reason)
    setText(this.#error, reason ?? '')
  }

  #sync(): void {
    show(this.#list, this.#values.length > 0)
    syncKeyed(
      this.#list,
      this.#values,
      (v) => v,
      (v) =>
        h(
          'li',
          { class: this.#options.mono ? 'chip mono' : 'chip' },
          h('span', { class: 'chip-text' }, v),
          button('×', { small: true, variant: 'flat', ariaLabel: `Remove ${v}`, onClick: () => this.#remove(v) }),
        ),
      (li) => {
        const remove = li.querySelector<PushButton>('push-button')
        if (remove && remove.disabled !== this.#disabled) remove.disabled = this.#disabled
      },
    )
  }
}

/** The approver subjects the coordinator accepts. */
export interface SubjectRules {
  /** `non_author`: only a required review accepts it. */
  nonAuthor?: boolean
  /** `project_owner`: a policy `actors` list does not accept it. */
  projectOwner?: boolean
}

const PREFIXES = ['vcs_user:', 'vcs_team:', 'reactorcide_group:']

/** The reason a subject is not valid, or undefined. */
export function subjectError(subject: string, rules: SubjectRules = {}): string | undefined {
  const projectOwner = rules.projectOwner ?? true
  if (subject === 'repository_write') return undefined
  if (subject === 'project_owner') return projectOwner ? undefined : 'project_owner is not allowed here.'
  if (subject === 'non_author') return rules.nonAuthor ? undefined : 'non_author is allowed only in a required review.'
  const prefix = PREFIXES.find((p) => subject.startsWith(p))
  if (!prefix) return `"${subject}" is not a subject. ${subjectHint(rules)}`
  const rest = subject.slice(prefix.length)
  if (!rest || /\s/.test(rest)) return `Put a name after ${prefix} with no spaces.`
  if (prefix === 'vcs_user:' && !/^[^/]+\/[^/]+$/.test(rest)) return 'Write a VCS user as vcs_user:github/LOGIN.'
  if (prefix === 'vcs_team:' && !/^[^/]+\/[^/]+$/.test(rest)) return 'Write a VCS team as vcs_team:OWNER/TEAM.'
  return undefined
}

/** The subject forms, in one line, for a hint. */
export function subjectHint(rules: SubjectRules = {}): string {
  const forms = ['repository_write']
  if (rules.projectOwner ?? true) forms.push('project_owner')
  forms.push('vcs_user:github/LOGIN', 'vcs_team:OWNER/TEAM', 'reactorcide_group:NAME')
  if (rules.nonAuthor) forms.push('non_author')
  return `Use ${forms.join(', ')}.`
}

/** Lowercase letters, digits and hyphens, starting with a letter or digit (a rule id, a workflow id, a review id). */
export const SECURITY_ID = /^[a-z0-9][a-z0-9-]{0,62}$/

export function securityIdError(value: string, what = 'An id'): string | undefined {
  return SECURITY_ID.test(value) ? undefined : `${what} can have only lowercase letters, digits and hyphens. It must start with a letter or digit.`
}

/** A repository-relative glob pattern (`*`, `**`). */
export function pathPatternError(value: string): string | undefined {
  if (value.startsWith('/')) return 'Write the path from the repository root, with no leading /.'
  if (value.includes('\\')) return 'Use / in a path, not \\.'
  if (value.split('/').some((part) => part === '..' || part === '.' || part === '')) return 'A path cannot have an empty, . or .. part.'
  return undefined
}
