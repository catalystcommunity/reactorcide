import type { ComboBox, ListBox } from '@catalystcommunity/controls'
import type { EnumChoice } from '~/api/csilapi/types.gen.ts'
import { comboBox, listBox, setComboValue, setListValues, setOptions, type ChoiceOption } from '~/ui/controls.ts'

/**
 * Controls bound to a server-supplied vocabulary.
 *
 * The values come from `describe-form-metadata`, which derives them from the
 * same Go constants the coordinator validates against. That is the point: a
 * copied enum drifts, and the "allowed event types" field was free text
 * precisely because nothing served the list. These controls cannot offer a
 * value the server would reject, and cannot omit one it accepts.
 *
 * THE RENDERING RULE, which this file exists to demonstrate: a control must
 * never be rebuilt from scratch when its data refreshes. The old metrics
 * dialog called `innerHTML = ''` and recreated its <select> elements on every
 * refresh, and a browser closes an open dropdown the moment its element is
 * replaced -- so the picker slammed shut every few seconds. Make the control
 * once, then call `update` from render(): it changes the options only when
 * they differ, and the value only when it differs.
 */

function toOptions(choices: readonly EnumChoice[], placeholder?: string): ChoiceOption[] {
  const out: ChoiceOption[] = placeholder ? [{ value: '', label: placeholder }] : []
  for (const choice of choices) out.push({ value: choice.value, label: choice.label, description: choice.description || undefined })
  return out
}

/** Single-select. Options may change while the list is open. */
export class ChoiceSelect {
  readonly el: ComboBox
  readonly #placeholder?: string

  constructor(options: {
    ariaLabel?: string
    value?: string
    choices?: readonly EnumChoice[]
    /** The first entry, with the value '', meaning "no choice" (for example "All statuses"). */
    placeholder?: string
    disabled?: boolean
    onChange: (value: string) => void
  }) {
    this.#placeholder = options.placeholder
    this.el = comboBox({
      ariaLabel: options.ariaLabel,
      placeholder: options.placeholder,
      options: toOptions(options.choices ?? [], options.placeholder),
      value: options.value ?? '',
      disabled: options.disabled,
      onChange: options.onChange,
    })
  }

  update(choices: readonly EnumChoice[], value: string): void {
    setOptions(this.el, toOptions(choices, this.#placeholder))
    setComboValue(this.el, value)
  }
}

/**
 * Multi-select, for fields like a project's allowed event types.
 *
 * A Zest list box with `multiple`: every option shows its description, so the
 * operator does not have to already know what `pull_request_updated` means,
 * and a check mark shows each chosen value.
 */
export class ChoiceMultiSelect {
  readonly el: ListBox

  constructor(options: {
    ariaLabel?: string
    values?: readonly string[]
    choices?: readonly EnumChoice[]
    onChange: (values: string[]) => void
  }) {
    this.el = listBox({
      ariaLabel: options.ariaLabel,
      multiple: true,
      options: toOptions(options.choices ?? []),
      values: [...(options.values ?? [])],
      onChange: options.onChange,
    })
  }

  update(choices: readonly EnumChoice[], values: readonly string[]): void {
    setOptions(this.el, toOptions(choices))
    setListValues(this.el, values)
  }
}
