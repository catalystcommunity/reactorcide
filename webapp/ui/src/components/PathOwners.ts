import type { TextField } from '@catalystcommunity/controls'
import type { PathOwnerRule } from '~/api/csilapi/types.gen.ts'
import { ChipList } from '~/components/ChipList.ts'
import { field, type Field } from '~/components/Field.ts'
import { button, textField } from '~/ui/controls.ts'
import { h, setText, show, syncKeyed, uid } from '~/ui/dom.ts'

/**
 * The path owner registry (docs/reporting-and-ownership.md): rules of a
 * code owners pattern and the owner subjects of the matching paths. The last
 * matching rule wins, as in a code owners file. The registry is used when the
 * code owners import is off.
 *
 * A plain editor (not an element): the card that holds it builds it one time
 * and calls `set()` one time for each stored state, so a refresh keeps a
 * draft.
 */

/** The reason an owner subject is not valid, or undefined. */
export function ownerSubjectError(value: string): string | undefined {
  if (!value || /\s/.test(value)) return 'An owner subject has no spaces.'
  if (!value.includes(':')) return 'Write an owner subject as TYPE:VALUE, for example vcs_team:acme/payments.'
  return undefined
}

class PathOwnerRow {
  readonly key = uid('path-owner')
  readonly el: HTMLDivElement
  readonly pattern: TextField
  readonly owners: ChipList
  readonly #patternField: Field
  readonly #heading: HTMLHeadingElement

  constructor(onRemove: (row: PathOwnerRow) => void, onChange: () => void) {
    this.pattern = textField({ mono: true, placeholder: '/payments/', onInput: onChange })
    this.owners = new ChipList({ ariaLabel: 'Add an owner subject', placeholder: 'vcs_team:acme/payments', mono: true, validate: ownerSubjectError, onChange })
    this.#patternField = field({ label: 'Path pattern', required: true, hint: 'Code owners syntax, for example /payments/ or *.sql.', control: this.pattern })
    this.#heading = h('h4', { class: 'section-title' })
    this.el = h(
      'div',
      { class: 'sub-card path-owner-row' },
      h('div', { class: 'sub-card-header' }, this.#heading, button('Remove rule', { small: true, variant: 'danger', onClick: () => onRemove(this) })),
      this.#patternField.el,
      field({ label: 'Owners', required: true, hint: 'The owner subjects of the paths.', control: this.owners.el, labelTarget: this.owners.input }).el,
    )
  }

  set(rule: PathOwnerRule): void {
    this.pattern.value = rule.pattern
    this.owners.set(rule.owners)
  }

  value(): PathOwnerRule {
    return { pattern: this.pattern.value.trim(), owners: this.owners.values() }
  }

  error(): string | undefined {
    const v = this.value()
    if (!v.pattern) return 'A rule needs a path pattern.'
    if (/\s/.test(v.pattern)) return 'A path pattern has no spaces.'
    if (v.owners.length === 0) return `Add an owner to the rule for ${v.pattern}.`
    return undefined
  }

  render(touched: boolean): void {
    const v = this.value()
    this.#patternField.setError(touched && !v.pattern ? 'Write a path pattern.' : undefined)
    setText(this.#heading, v.pattern ? `Rule for ${v.pattern}` : 'New rule')
  }
}

export class PathOwnersEditor {
  readonly el: HTMLDivElement
  #rows: PathOwnerRow[] = []
  readonly #list: HTMLDivElement
  readonly #empty: HTMLParagraphElement
  readonly #add: ReturnType<typeof button>
  readonly #onChange: () => void
  touched = false

  constructor(onChange: () => void) {
    this.#onChange = onChange
    this.#list = h('div', { class: 'path-owner-list' })
    this.#empty = h('p', { class: 'meta' }, 'No rules. Add one to name the owners of a path.')
    this.#add = button('Add rule', { small: true, onClick: () => this.#addRow() })
    this.#add.classList.add('rc-add-row')
    this.el = h('div', { class: 'path-owners-editor' }, this.#empty, this.#list, this.#add)
  }

  set(rules: readonly PathOwnerRule[]): void {
    this.#rows = rules.map((rule) => {
      const row = this.#newRow()
      row.set(rule)
      return row
    })
    this.touched = false
  }

  values(): PathOwnerRule[] {
    return this.#rows.map((row) => row.value())
  }

  /** The first problem, or undefined. */
  error(): string | undefined {
    for (const row of this.#rows) {
      const error = row.error()
      if (error) return `Fix the path owners: ${error}`
    }
    return undefined
  }

  setDisabled(disabled: boolean): void {
    this.#add.disabled = disabled
  }

  #newRow(): PathOwnerRow {
    return new PathOwnerRow(
      (row) => {
        this.#rows = this.#rows.filter((r) => r !== row)
        this.#onChange()
      },
      () => this.#onChange(),
    )
  }

  #addRow(): void {
    const row = this.#newRow()
    this.#rows = [...this.#rows, row]
    this.#onChange()
    row.pattern.focus()
  }

  render(): void {
    show(this.#empty, this.#rows.length === 0)
    syncKeyed(this.#list, this.#rows, (r) => r.key, (r) => r.el, (_el, r) => r.render(this.touched))
  }
}
