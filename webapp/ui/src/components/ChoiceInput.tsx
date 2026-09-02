import { For, Show, createMemo, createSignal, type JSX } from 'solid-js'
import type { EnumChoice } from '~/api/csilapi/types.gen.ts'
import './choice.css'

/**
 * Controls bound to a server-supplied vocabulary.
 *
 * The values come from `describe-form-metadata`, which derives them from the
 * same Go constants the coordinator validates against. That is the point: a
 * copied enum drifts, and the "allowed event types" field was free text
 * precisely because nothing served the list. These controls cannot offer a
 * value the server would reject, and cannot omit one it accepts.
 *
 * THE RENDERING RULE, which this file exists to demonstrate:
 * a control must never be rebuilt from scratch when its data refreshes. The old
 * metrics dialog called `innerHTML = ''` and recreated its <select> elements on
 * every refresh, and a browser closes an open dropdown the moment its element
 * is replaced -- so the picker slammed shut every few seconds. Everything here
 * uses <For> with stable keys and binds values to signals, so a data update
 * mutates in place and an open menu stays open.
 */

/** Single-select. Options may change while the menu is open. */
export function ChoiceSelect(props: {
  id?: string
  value: string
  choices: EnumChoice[]
  /** Shown as the first entry when no value is chosen. */
  placeholder?: string
  describedBy?: string
  invalid?: boolean
  disabled?: boolean
  onChange: (value: string) => void
}): JSX.Element {
  return (
    <select
      id={props.id}
      class="select"
      // Bound to the signal rather than set once. Solid patches the property,
      // so a refresh that does not change the value touches nothing.
      value={props.value}
      aria-describedby={props.describedBy}
      aria-invalid={props.invalid ? 'true' : undefined}
      disabled={props.disabled}
      onChange={(event) => props.onChange(event.currentTarget.value)}
    >
      <Show when={props.placeholder}>
        <option value="">{props.placeholder}</option>
      </Show>
      {/*
        Keyed by the option's own value. Refreshing a list whose contents are
        unchanged reuses every existing <option> node, which is what keeps an
        open dropdown open.
      */}
      <For each={props.choices}>
        {(choice) => (
          <option value={choice.value} title={choice.description}>
            {choice.label}
          </option>
        )}
      </For>
    </select>
  )
}

/**
 * Multi-select with typeahead, for fields like a project's allowed event types.
 *
 * Chosen values render as removable chips; typing filters the remaining
 * options. Every option shows its description, so the operator does not have to
 * already know what `pull_request_updated` means.
 */
export function ChoiceMultiSelect(props: {
  id?: string
  values: string[]
  choices: EnumChoice[]
  placeholder?: string
  describedBy?: string
  invalid?: boolean
  onChange: (values: string[]) => void
}): JSX.Element {
  const [query, setQuery] = createSignal('')
  const [open, setOpen] = createSignal(false)

  const chosen = createMemo(() => new Set(props.values))

  const available = createMemo(() => {
    const needle = query().trim().toLowerCase()
    return props.choices.filter((choice) => {
      if (chosen().has(choice.value)) return false
      if (!needle) return true
      return (
        choice.value.toLowerCase().includes(needle) ||
        choice.label.toLowerCase().includes(needle)
      )
    })
  })

  const labelFor = (value: string) =>
    props.choices.find((choice) => choice.value === value)?.label ?? value

  const add = (value: string) => {
    if (!chosen().has(value)) props.onChange([...props.values, value])
    setQuery('')
    setOpen(false)
  }

  const remove = (value: string) => {
    props.onChange(props.values.filter((v) => v !== value))
  }

  return (
    <div class="choice-multi" aria-invalid={props.invalid ? 'true' : undefined}>
      <div class="choice-chips">
        <For each={props.values}>
          {(value) => (
            <span class="choice-chip">
              {labelFor(value)}
              <button
                type="button"
                class="choice-chip-remove"
                aria-label={`Remove ${labelFor(value)}`}
                onClick={() => remove(value)}
              >
                ×
              </button>
            </span>
          )}
        </For>
      </div>

      <div class="choice-input-wrap">
        <input
          id={props.id}
          class="input"
          type="text"
          role="combobox"
          aria-expanded={open()}
          aria-autocomplete="list"
          aria-describedby={props.describedBy}
          placeholder={props.placeholder ?? 'Type to search…'}
          value={query()}
          onInput={(event) => {
            setQuery(event.currentTarget.value)
            setOpen(true)
          }}
          onFocus={() => setOpen(true)}
          // A blur closes the list, but only after a click on an option has had
          // a chance to register. Closing synchronously would swallow the
          // click that caused the blur.
          onBlur={() => window.setTimeout(() => setOpen(false), 120)}
          onKeyDown={(event) => {
            if (event.key === 'Escape') setOpen(false)
            if (event.key === 'Enter' && available().length > 0) {
              event.preventDefault()
              add(available()[0].value)
            }
            // Backspace on an empty query removes the last chip, which is what
            // every other chip input does.
            if (event.key === 'Backspace' && query() === '' && props.values.length > 0) {
              remove(props.values[props.values.length - 1])
            }
          }}
        />

        <Show when={open() && available().length > 0}>
          <ul class="choice-list" role="listbox">
            <For each={available()}>
              {(choice) => (
                <li>
                  <button
                    type="button"
                    class="choice-option"
                    role="option"
                    aria-selected="false"
                    // onMouseDown rather than onClick: mousedown fires before
                    // the input's blur, so the option is chosen instead of the
                    // list vanishing out from under the pointer.
                    onMouseDown={(event) => {
                      event.preventDefault()
                      add(choice.value)
                    }}
                  >
                    <span class="choice-option-label">{choice.label}</span>
                    <Show when={choice.description}>
                      <span class="choice-option-description">{choice.description}</span>
                    </Show>
                  </button>
                </li>
              )}
            </For>
          </ul>
        </Show>
      </div>
    </div>
  )
}
