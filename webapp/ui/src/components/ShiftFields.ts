import type { TextField } from '@catalystcommunity/controls'
import type { RunShifts } from '~/api/csilapi/types.gen.ts'
import { field } from '~/components/Field.ts'
import { clearField, textField } from '~/ui/controls.ts'
import { h } from '~/ui/dom.ts'
import './shifts.css'

/**
 * The optional shifts of a retry or a re-run: CPU, memory, storage and
 * timeout. These are the only changes a retry or a re-run can make. The new
 * run keeps the authority of the original run. A change to anything else
 * (image, command, environment) needs a new run.
 *
 * The fields are made one time. The page reads `value()` when the person
 * clicks the action, and calls `clear()` after a successful action.
 */
type ShiftKey = 'cpu' | 'memory' | 'storage' | 'timeout'

export class ShiftFields {
  readonly el: HTMLDetailsElement
  readonly #fields: Record<ShiftKey, TextField>

  constructor(action: string) {
    const make = (placeholder: string): TextField => textField({ placeholder, mono: true })
    this.#fields = {
      cpu: make('4'),
      memory: make('8Gi'),
      storage: make('20Gi'),
      timeout: make('2h'),
    }
    this.el = h(
      'details',
      { class: 'shift-fields' },
      h('summary', null, `Change resources for the ${action} (optional)`),
      h(
        'p',
        { class: 'meta' },
        'The new run keeps the authority of the original run. Only these values can change. Empty keeps the original value.',
      ),
      h(
        'div',
        { class: 'shift-grid' },
        field({ label: 'CPU limit', hint: 'Cores, for example 4 or 2500m', control: this.#fields.cpu }).el,
        field({ label: 'Memory limit', hint: 'For example 8Gi', control: this.#fields.memory }).el,
        field({ label: 'Storage limit', hint: 'Ephemeral disk, for example 20Gi', control: this.#fields.storage }).el,
        field({ label: 'Timeout', hint: 'For example 2h or 90m', control: this.#fields.timeout }).el,
      ),
    ) as HTMLDetailsElement
  }

  /** The shifts typed in, or undefined when every field is empty. */
  value(): RunShifts | undefined {
    const shifts: RunShifts = {}
    let any = false
    for (const [key, el] of Object.entries(this.#fields) as [ShiftKey, TextField][]) {
      const text = (el.value ?? '').trim()
      if (text) {
        shifts[key] = text
        any = true
      }
    }
    return any ? shifts : undefined
  }

  /** Empties the fields and closes the section. */
  clear(): void {
    for (const el of Object.values(this.#fields)) clearField(el)
    this.el.open = false
  }
}

/** A short text of recorded shifts, for example "memory 8Gi, timeout 2h0m0s". */
export function describeShifts(shifts: RunShifts | undefined): string {
  if (!shifts) return ''
  const parts: string[] = []
  if (shifts.cpu) parts.push(`CPU ${shifts.cpu}`)
  if (shifts.cpuRequest) parts.push(`CPU request ${shifts.cpuRequest}`)
  if (shifts.memory) parts.push(`memory ${shifts.memory}`)
  if (shifts.storage) parts.push(`storage ${shifts.storage}`)
  if (shifts.storageRequest) parts.push(`storage request ${shifts.storageRequest}`)
  if (shifts.timeout) parts.push(`timeout ${shifts.timeout}`)
  return parts.join(', ')
}
