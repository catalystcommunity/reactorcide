import { describe, expect, it } from 'vitest'
import type { ComboBox } from '@catalystcommunity/controls'
import { ChoiceMultiSelect, ChoiceSelect } from './ChoiceInput.ts'
import type { EnumChoice } from '~/api/csilapi/types.gen.ts'
import { settle } from '~/test/mount.ts'

const CHOICES: EnumChoice[] = [
  { value: 'cpu', label: 'CPU', description: 'Processor usage' },
  { value: 'memory', label: 'Memory', description: 'Memory usage' },
  { value: 'storage', label: 'Storage', description: 'Disk usage' },
]

function options(el: Element): Element[] {
  return [...el.querySelectorAll('[role="option"]')]
}

describe('ChoiceSelect', () => {
  /**
   * The reported bug, pinned.
   *
   * The old metrics dialog rebuilt its controls on every refresh --
   * `metricsControls.innerHTML = ''` followed by fresh createElement calls --
   * and a browser closes an open dropdown the instant its element is replaced.
   * With live metrics refreshing every few seconds, the picker was unusable.
   *
   * This asserts the structural property that makes that impossible: a data
   * refresh must reuse the SAME DOM nodes. If the element identity survives, an
   * open list survives with it.
   */
  it('keeps the same DOM nodes when its data refreshes', async () => {
    const select = new ChoiceSelect({ value: 'cpu', choices: CHOICES, onChange: () => {} })
    document.body.append(select.el)
    await settle()
    const control = select.el
    const before = options(control)
    expect(before).toHaveLength(3)

    // A refresh that returns equal data, exactly as a metrics poll does.
    select.update([...CHOICES.map((c) => ({ ...c }))], 'cpu')
    await settle()

    expect(select.el).toBe(control)
    const after = options(control)
    for (let i = 0; i < before.length; i++) expect(after[i]).toBe(before[i])
  })

  it('keeps the selection and an open list when a new option appears', async () => {
    const select = new ChoiceSelect({ value: 'memory', choices: CHOICES, onChange: () => {} })
    document.body.append(select.el)
    await settle()
    const combo = select.el as ComboBox
    combo.show()
    await settle()
    expect(combo.open).toBe(true)

    // A new option appearing mid-job -- a builder sidecar starting, say --
    // must not reset what the operator had selected, nor close the list.
    select.update([...CHOICES, { value: 'network', label: 'Network', description: '' }], 'memory')
    await settle()

    expect(combo.value).toBe('memory')
    expect(combo.open).toBe(true)
    expect(options(combo)).toHaveLength(4)
  })

  it('offers a placeholder entry that means "no choice"', async () => {
    let chosen = 'unset'
    const select = new ChoiceSelect({ placeholder: 'All statuses', value: '', choices: CHOICES, onChange: (v) => (chosen = v) })
    document.body.append(select.el)
    await settle()
    expect(options(select.el)[0].textContent).toBe('All statuses')
    expect(chosen).toBe('unset')
  })
})

describe('ChoiceMultiSelect', () => {
  it('shows each option description so the operator need not already know the vocabulary', async () => {
    const multi = new ChoiceMultiSelect({ values: [], choices: CHOICES, onChange: () => {} })
    document.body.append(multi.el)
    await settle()
    const descriptions = [...multi.el.querySelectorAll('.zest-description')].map((n) => n.textContent)
    expect(descriptions).toContain('Processor usage')
  })

  it('marks the chosen values selected, and keeps them and the nodes across a refresh', async () => {
    const multi = new ChoiceMultiSelect({ values: ['cpu', 'memory'], choices: CHOICES, onChange: () => {} })
    document.body.append(multi.el)
    await settle()
    const selected = (): string[] =>
      options(multi.el)
        .filter((o) => o.getAttribute('aria-selected') === 'true')
        .map((o) => (o as HTMLElement).dataset.value!)
    expect(selected()).toEqual(['cpu', 'memory'])
    const before = options(multi.el)

    multi.update([...CHOICES], ['cpu', 'memory'])
    await settle()
    expect(selected()).toEqual(['cpu', 'memory'])
    expect(options(multi.el)[0]).toBe(before[0])
  })

  it('reports the new values when the person toggles an option', async () => {
    let values: string[] = []
    const multi = new ChoiceMultiSelect({ values: ['cpu'], choices: CHOICES, onChange: (v) => (values = v) })
    document.body.append(multi.el)
    await settle()
    ;(options(multi.el)[1] as HTMLElement).click()
    await settle()
    expect(values).toEqual(['cpu', 'memory'])
  })

  it('is a multi-select list box for assistive technology', async () => {
    const multi = new ChoiceMultiSelect({ ariaLabel: 'Event types', values: [], choices: CHOICES, onChange: () => {} })
    document.body.append(multi.el)
    await settle()
    const list = multi.el.querySelector('[role="listbox"]')!
    expect(list.getAttribute('aria-multiselectable')).toBe('true')
  })
})
