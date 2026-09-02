import { Show, createUniqueId, type JSX } from 'solid-js'
import { Tooltip } from '@kobalte/core/tooltip'

/**
 * A labelled form control.
 *
 * The New Project form used to be a column of bare inputs: nothing said which
 * fields were required, nothing said what a valid event type looked like, and
 * nothing validated before the round trip. This component is where that is
 * fixed once for every form.
 *
 * Four things every field gets:
 *
 *  - An explicit required/optional marker. Not a convention the reader has to
 *    infer from an asterisk they might not notice.
 *  - A hint line under the label for the short version.
 *  - A tooltip for the longer explanation, so the hint stays one line.
 *  - An error slot wired to aria-describedby and aria-invalid, so a screen
 *    reader hears the problem rather than only seeing a red border.
 */

export interface FieldProps {
  label: string
  /** Marked required, and announced as such. Defaults to optional. */
  required?: boolean
  /** One short line under the label. */
  hint?: string
  /** The longer explanation, behind an info affordance. */
  tooltip?: string
  /** Validation message. Presence switches the control to the invalid state. */
  error?: string
  /**
   * Renders the control. Receives the ids to wire up, so the caller cannot
   * forget the accessibility plumbing.
   */
  children: (ids: { id: string; describedBy: string | undefined; invalid: boolean }) => JSX.Element
}

export function Field(props: FieldProps): JSX.Element {
  const id = createUniqueId()
  const hintId = `${id}-hint`
  const errorId = `${id}-error`

  const describedBy = () => {
    const parts: string[] = []
    if (props.hint) parts.push(hintId)
    if (props.error) parts.push(errorId)
    return parts.length ? parts.join(' ') : undefined
  }

  return (
    <div class="field">
      <label class="field-label" for={id}>
        {props.label}
        <Show
          when={props.required}
          fallback={<span class="field-optional">(optional)</span>}
        >
          {/* The asterisk is decorative; "required" is what gets announced. */}
          <span class="field-required" aria-hidden="true">
            *
          </span>
          <span class="sr-only">required</span>
        </Show>
        <Show when={props.tooltip}>
          <InfoTooltip text={props.tooltip!} label={`About ${props.label}`} />
        </Show>
      </label>

      <Show when={props.hint}>
        <span class="field-hint" id={hintId}>
          {props.hint}
        </span>
      </Show>

      {props.children({ id, describedBy: describedBy(), invalid: Boolean(props.error) })}

      <Show when={props.error}>
        {/* role=alert so a validation failure is announced when it appears,
            rather than only when the field is next focused. */}
        <span class="field-error" id={errorId} role="alert">
          {props.error}
        </span>
      </Show>
    </div>
  )
}

/**
 * The info affordance beside a label.
 *
 * Kobalte rather than a hand-rolled tooltip: getting the focus, hover, escape
 * and screen-reader behaviour right is genuinely fiddly, and a tooltip that
 * only opens on hover is invisible to a keyboard user.
 */
export function InfoTooltip(props: { text: string; label: string }): JSX.Element {
  return (
    <Tooltip openDelay={150}>
      <Tooltip.Trigger class="tooltip-trigger" aria-label={props.label}>
        i
      </Tooltip.Trigger>
      <Tooltip.Portal>
        <Tooltip.Content class="tooltip-content">{props.text}</Tooltip.Content>
      </Tooltip.Portal>
    </Tooltip>
  )
}
