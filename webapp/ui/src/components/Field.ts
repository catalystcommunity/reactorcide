import { attachToolTip } from '@catalystcommunity/menus'
import { RcElement, define } from '~/ui/element.ts'
import { h, setAttr, setText, show, uid } from '~/ui/dom.ts'

/**
 * A labelled form control.
 *
 * The New Project form used to be a column of bare inputs: nothing said which
 * fields were required, nothing said what a valid event type looked like, and
 * nothing validated before the round trip. This is where that is fixed once
 * for every form.
 *
 * Four things every field gets:
 *
 *  - An explicit required/optional marker. Not a convention the reader has to
 *    infer from an asterisk they might not notice.
 *  - A hint line under the label for the short version.
 *  - A tooltip for the longer explanation, so the hint stays one line.
 *  - An error slot wired to aria-describedby, so a screen reader hears the
 *    problem rather than only seeing a red border.
 *
 * The control is a Zest control (or any element). Zest draws no label here:
 * the field draws the visible label, and the control gets the same text as
 * its `aria-label`. Zest forwards `aria-label` and `aria-describedby` from the
 * host to the inner native control, which is what a screen reader reads.
 */
export interface FieldOptions {
  label: string
  /** Marked required, and announced as such. Defaults to optional. */
  required?: boolean
  /** One short line under the label. */
  hint?: string
  /** The longer explanation, behind an info affordance. */
  tooltip?: string
  /** The control. */
  control: Element
  /**
   * Where the accessible name and description go, when that is not `control`
   * itself (a picker that wraps a Zest control).
   */
  labelTarget?: Element
}

export class Field {
  readonly el: HTMLDivElement
  readonly control: Element
  readonly #error: HTMLSpanElement
  readonly #errorId: string
  readonly #hintId: string | undefined
  readonly #target: Element
  #message: string | undefined

  constructor(options: FieldOptions) {
    const id = uid('field')
    this.#errorId = `${id}-error`
    this.#hintId = options.hint ? `${id}-hint` : undefined
    this.control = options.control
    this.#target = options.labelTarget ?? options.control

    // The visible label is hidden from assistive technology because the
    // control's own name already says it; reading both would say it twice.
    const marker = options.required
      ? h('span', { class: 'field-required', 'aria-hidden': 'true' }, '*')
      : h('span', { class: 'field-optional', 'aria-hidden': 'true' }, '(optional)')

    const labelRow = h(
      'div',
      { class: 'field-label' },
      h('span', { 'aria-hidden': 'true' }, options.label),
      marker,
      options.tooltip ? infoTip(options.tooltip, `About ${options.label}`) : null,
    )

    // role=alert so a validation failure is announced when it appears, rather
    // than only when the field is next focused.
    this.#error = h('span', { class: 'field-error', id: this.#errorId, role: 'alert', hidden: true })

    this.el = h(
      'div',
      { class: 'field' },
      labelRow,
      options.hint ? h('span', { class: 'field-hint', id: this.#hintId }, options.hint) : null,
      options.control,
      this.#error,
    )

    setAttr(this.#target, 'aria-label', options.required ? options.label : `${options.label} (optional)`)
    if (options.required && 'required' in this.#target) (this.#target as { required: boolean }).required = true
    this.#describe()
  }

  /** Shows or clears the validation message. */
  setError(message: string | undefined): void {
    if (message === this.#message) return
    this.#message = message
    show(this.#error, message)
    setText(this.#error, message ?? '')
    setAttr(this.control, 'data-invalid', message ? 'true' : null)
    this.#describe()
  }

  #describe(): void {
    const ids = [this.#hintId, this.#message ? this.#errorId : undefined].filter(Boolean)
    setAttr(this.#target, 'aria-describedby', ids.length ? ids.join(' ') : null)
  }
}

export function field(options: FieldOptions): Field {
  return new Field(options)
}

/**
 * The info affordance beside a label: a real button, so a keyboard user can
 * reach it. The Zest tooltip shows on hover and at once on keyboard focus,
 * stays while the pointer is on it, and closes on Escape (WCAG 1.4.13).
 */
export class InfoTip extends RcElement {
  text = ''
  tipLabel = ''
  #button: HTMLButtonElement | null = null

  protected build(): void {
    this.#button = h('button', { type: 'button', class: 'tooltip-trigger', 'aria-label': this.tipLabel }, 'i')
    this.append(this.#button)
  }

  protected connected(): void {
    // The tooltip goes on document.body so no card's overflow can clip it,
    // and is removed when this element leaves the page.
    this.own(attachToolTip(this.#button!, () => this.text, { delay: 150, container: document.body }))
  }
}
define('rc-info-tip', InfoTip)

export function infoTip(text: string, label: string): InfoTip {
  const el = document.createElement('rc-info-tip') as InfoTip
  el.text = text
  el.tipLabel = label
  return el
}
