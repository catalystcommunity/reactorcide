import { describe, expect, it } from 'vitest'
import { render } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { ChoiceSelect, ChoiceMultiSelect } from './ChoiceInput.tsx'
import type { EnumChoice } from '~/api/csilapi/types.gen.ts'

const CHOICES: EnumChoice[] = [
  { value: 'cpu', label: 'CPU', description: 'Processor usage' },
  { value: 'memory', label: 'Memory', description: 'Memory usage' },
  { value: 'storage', label: 'Storage', description: 'Disk usage' },
]

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
   * open menu survives with it.
   */
  it('keeps the same DOM nodes when its data refreshes', () => {
    const [choices, setChoices] = createSignal(CHOICES)
    const [value, setValue] = createSignal('cpu')

    const { container } = render(() => (
      <ChoiceSelect value={value()} choices={choices()} onChange={setValue} />
    ))

    const select = container.querySelector('select')!
    const optionsBefore = [...select.querySelectorAll('option')]
    expect(optionsBefore).toHaveLength(3)

    // A refresh that returns equal data, exactly as a metrics poll does.
    setChoices([...CHOICES])

    const selectAfter = container.querySelector('select')!
    const optionsAfter = [...selectAfter.querySelectorAll('option')]

    expect(selectAfter).toBe(select)
    for (let i = 0; i < optionsBefore.length; i++) {
      expect(optionsAfter[i]).toBe(optionsBefore[i])
    }
  })

  it('keeps the current selection across a refresh', () => {
    const [choices, setChoices] = createSignal(CHOICES)
    const [value, setValue] = createSignal('memory')

    const { container } = render(() => (
      <ChoiceSelect value={value()} choices={choices()} onChange={setValue} />
    ))

    const select = container.querySelector('select')! as HTMLSelectElement
    expect(select.value).toBe('memory')

    setChoices([...CHOICES, { value: 'network', label: 'Network', description: '' }])

    // A new option appearing mid-job -- a builder sidecar starting, say --
    // must not reset what the operator had selected.
    expect(select.value).toBe('memory')
    expect(select.querySelectorAll('option')).toHaveLength(4)
  })

  it('adds a new option in place rather than rebuilding the list', () => {
    const [choices, setChoices] = createSignal(CHOICES)

    const { container } = render(() => (
      <ChoiceSelect value="cpu" choices={choices()} onChange={() => {}} />
    ))

    const select = container.querySelector('select')!
    const firstOptionBefore = select.querySelector('option')

    setChoices([...CHOICES, { value: 'network', label: 'Network', description: '' }])

    // The existing nodes are reused; only the new one is created.
    expect(select.querySelector('option')).toBe(firstOptionBefore)
    expect(select.querySelectorAll('option')).toHaveLength(4)
  })
})

describe('ChoiceMultiSelect', () => {
  it('renders a chip per chosen value with a labelled remove control', () => {
    const { getByLabelText, getAllByLabelText } = render(() => (
      <ChoiceMultiSelect values={['cpu', 'memory']} choices={CHOICES} onChange={() => {}} />
    ))

    expect(getAllByLabelText(/^Remove /)).toHaveLength(2)
    // The remove control must be reachable and named, not a bare glyph.
    expect(getByLabelText('Remove CPU')).toBeTruthy()
  })

  it('removes a value when its chip is dismissed', () => {
    const [values, setValues] = createSignal(['cpu', 'memory'])
    const { getByLabelText } = render(() => (
      <ChoiceMultiSelect values={values()} choices={CHOICES} onChange={setValues} />
    ))

    getByLabelText('Remove CPU').click()
    expect(values()).toEqual(['memory'])
  })

  it('offers only values that are not already chosen', () => {
    const { container, queryByText } = render(() => (
      <ChoiceMultiSelect values={['cpu']} choices={CHOICES} onChange={() => {}} />
    ))

    const input = container.querySelector('input')! as HTMLInputElement
    input.focus()
    input.dispatchEvent(new Event('focus'))

    // 'CPU' appears as a chip, but must not also appear as a selectable option.
    const options = [...container.querySelectorAll('.choice-option-label')].map((n) => n.textContent)
    expect(options).not.toContain('CPU')
  })

  it('shows each option description so the operator need not already know the vocabulary', () => {
    const { container } = render(() => (
      <ChoiceMultiSelect values={[]} choices={CHOICES} onChange={() => {}} />
    ))

    const input = container.querySelector('input')!
    input.dispatchEvent(new FocusEvent('focus'))

    const descriptions = [...container.querySelectorAll('.choice-option-description')].map(
      (n) => n.textContent,
    )
    expect(descriptions).toContain('Processor usage')
  })

  it('marks itself as a combobox for assistive technology', () => {
    const { container } = render(() => (
      <ChoiceMultiSelect values={[]} choices={CHOICES} onChange={() => {}} />
    ))
    const input = container.querySelector('input')!
    expect(input.getAttribute('role')).toBe('combobox')
    expect(input.getAttribute('aria-autocomplete')).toBe('list')
  })
})
