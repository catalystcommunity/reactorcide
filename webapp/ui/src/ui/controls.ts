/**
 * Makers for the Zest controls the UI uses, with the properties set the way
 * the controls want them. Each returns the element; keep it and change its
 * properties in render(), never make it again.
 *
 * Zest controls send `input` while a value changes and `change` when the user
 * commits it. A change from code sends no event. Both events bubble, so a
 * listener on a WRAPPER must check `event.target === event.currentTarget`; the
 * makers below listen on the control itself.
 */
import type { CheckBox, ComboBox, ControlOptionInput, ListBox, PushButton, TextArea, TextField } from '@catalystcommunity/controls'
import { askConfirm } from '@catalystcommunity/containers'
import { setAttr } from './dom.ts'

type Variant = 'default' | 'primary' | 'danger' | 'flat'

export interface ButtonOptions {
  variant?: Variant
  type?: 'button' | 'submit'
  /** A smaller button for tables and toolbars. */
  small?: boolean
  disabled?: boolean
  /** The accessible name, when the text alone does not say which row it acts on. */
  ariaLabel?: string
  title?: string
  onClick?: (event: MouseEvent) => void
}

export function button(text: string, options: ButtonOptions = {}): PushButton {
  const el = document.createElement('push-button') as PushButton
  el.variant = options.variant ?? 'default'
  el.type = options.type ?? 'button'
  el.disabled = Boolean(options.disabled)
  if (options.small) el.classList.add('rc-small')
  if (options.ariaLabel) el.setAttribute('aria-label', options.ariaLabel)
  if (options.title) el.title = options.title
  el.textContent = text
  if (options.onClick) el.addEventListener('click', options.onClick)
  return el
}

/** Sets a button's text only when it changes. */
export function setButtonText(el: PushButton, text: string): void {
  if (el.textContent !== text) el.textContent = text
}

export interface TextOptions {
  /** The visible label. Leave empty inside a `field()`, which draws its own label. */
  label?: string
  ariaLabel?: string
  value?: string
  placeholder?: string
  required?: boolean
  name?: string
  helper?: string
  autocomplete?: string
  /** Monospace text, for ids, URLs, paths and patterns. */
  mono?: boolean
  onInput?: (value: string) => void
  onChange?: (value: string) => void
}

export function textField(options: TextOptions & { type?: 'text' | 'email' | 'password' | 'search' | 'url' } = {}): TextField {
  const el = document.createElement('text-field') as TextField
  el.type = options.type ?? 'text'
  applyText(el, options)
  return el
}

export function textArea(options: TextOptions & { rows?: number } = {}): TextArea {
  const el = document.createElement('text-area') as TextArea
  el.rows = options.rows ?? 6
  applyText(el, options)
  return el
}

function applyText(el: TextField | TextArea, options: TextOptions): void {
  if (options.label) el.label = options.label
  if (options.ariaLabel) el.setAttribute('aria-label', options.ariaLabel)
  if (options.placeholder) el.placeholder = options.placeholder
  if (options.required) el.required = true
  // The attribute, not the property: a native form POST reads a form-associated
  // element's field name from the attribute, and Zest does not reflect it.
  if (options.name) el.setAttribute('name', options.name)
  if (options.helper) el.helper = options.helper
  if (options.autocomplete) el.autocomplete = options.autocomplete
  if (options.mono) el.classList.add('rc-mono')
  el.value = options.value ?? ''
  if (options.onInput) el.addEventListener('input', () => options.onInput!(el.value))
  if (options.onChange) el.addEventListener('change', () => options.onChange!(el.value))
  // Enter in a text field submits its form (implicit submission). That is
  // what a native input does, and every form here wants it.
}

/**
 * Sets a field's value from data, unless the person is editing it.
 *
 * A live refresh must not overwrite what somebody is typing. Pages that keep a
 * draft do not call this at all; this is for read-mostly fields.
 */
export function setFieldValue(el: TextField | TextArea, value: string): void {
  if (el.value === value) return
  if (el.contains(document.activeElement)) return
  el.value = value
}

/**
 * Clears a field after a successful submit, without the "Please fill out this
 * field" message a required, touched field otherwise shows at once.
 */
export function clearField(el: TextField | TextArea): void {
  el.value = ''
  el.touched = false
  el.invalidate()
}

export function checkBox(text: string, options: { checked?: boolean; disabled?: boolean; onChange?: (checked: boolean) => void } = {}): CheckBox {
  const el = document.createElement('check-box') as CheckBox
  el.checked = Boolean(options.checked)
  el.disabled = Boolean(options.disabled)
  el.textContent = text
  if (options.onChange) el.addEventListener('change', () => options.onChange!(el.checked))
  return el
}

export interface ChoiceOption {
  value: string
  label: string
  description?: string
  disabled?: boolean
}

export interface ComboOptions {
  label?: string
  ariaLabel?: string
  placeholder?: string
  options?: ChoiceOption[]
  value?: string
  required?: boolean
  disabled?: boolean
  onChange?: (value: string) => void
}

export function comboBox(options: ComboOptions = {}): ComboBox {
  const el = document.createElement('combo-box') as ComboBox
  if (options.label) el.label = options.label
  if (options.ariaLabel) el.setAttribute('aria-label', options.ariaLabel)
  if (options.placeholder) el.placeholder = options.placeholder
  if (options.required) el.required = true
  el.disabled = Boolean(options.disabled)
  // Options before the value: the box reads the value against its options.
  setOptions(el, options.options ?? [])
  el.value = options.value ?? ''
  if (options.onChange) el.addEventListener('change', () => options.onChange!(el.value))
  return el
}

/**
 * Sets the options of a combo box or a list box only when they differ.
 *
 * A metrics or form-metadata refresh hands back equal data in a new array.
 * Setting it would re-render the list under an open popup, which is the
 * "picker slams shut every few seconds" bug the old metrics dialog had.
 */
export function setOptions(el: ComboBox | ListBox, options: readonly ChoiceOption[]): void {
  const next = options.map(toControlOption)
  const signature = JSON.stringify(next)
  const holder = el as unknown as { __rcOptions?: string }
  if (holder.__rcOptions === signature) return
  holder.__rcOptions = signature
  el.options = next
}

function toControlOption(option: ChoiceOption): ControlOptionInput {
  const out: Record<string, unknown> = { value: option.value, label: option.label }
  if (option.description) out.description = option.description
  if (option.disabled) out.disabled = true
  return out as ControlOptionInput
}

/** Sets a combo box value from code only when it changes (no event is sent). */
export function setComboValue(el: ComboBox, value: string): void {
  if (el.value !== value) el.value = value
}

export function listBox(options: {
  ariaLabel?: string
  label?: string
  multiple?: boolean
  options?: ChoiceOption[]
  values?: string[]
  onChange?: (values: string[]) => void
} = {}): ListBox {
  const el = document.createElement('list-box') as ListBox
  if (options.label) el.label = options.label
  if (options.ariaLabel) el.setAttribute('aria-label', options.ariaLabel)
  el.multiple = Boolean(options.multiple)
  setOptions(el, options.options ?? [])
  el.values = options.values ?? []
  if (options.onChange) el.addEventListener('change', () => options.onChange!(el.values))
  return el
}

/** Sets the selection of a list box from code only when it changes. */
export function setListValues(el: ListBox, values: readonly string[]): void {
  const current = el.values ?? []
  if (current.length === values.length && current.every((v, i) => v === values[i])) return
  el.values = [...values]
}

/**
 * Asks before something that cannot be undone. Replaces `window.confirm`:
 * the Zest dialog is modal, has the danger look, and takes the focus back to
 * where it was when it closes.
 */
export function confirmAction(text: string, options: { okLabel?: string; title?: string; danger?: boolean } = {}): Promise<boolean> {
  return askConfirm({
    title: options.title ?? 'Are you sure?',
    text,
    okLabel: options.okLabel ?? 'Continue',
    cancelLabel: 'Cancel',
    danger: options.danger ?? true,
  })
}

/** Sets or clears `aria-describedby` on a control host (Zest forwards it to the inner control). */
export function describeControl(el: Element, ids: (string | undefined | false)[]): void {
  const value = ids.filter(Boolean).join(' ')
  setAttr(el, 'aria-describedby', value || null)
}
