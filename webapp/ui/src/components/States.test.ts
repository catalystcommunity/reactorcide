import { describe, expect, it } from 'vitest'
import { ResourceView } from './States.ts'
import type { ResourceState } from '~/store/index.ts'
import { h, setText, syncKeyed } from '~/ui/dom.ts'

/**
 * ResourceView's contract, pinned.
 *
 * The bug these guard against was subtle and expensive. The first Solid
 * version rebuilt the content on every refresh: on a live page, which fires
 * constantly, a `project_update` arriving while somebody was halfway through
 * the settings form silently discarded their edits, and every `job_update`
 * reset the log viewer's scroll position.
 *
 * Nothing looked broken. The data was right, the page re-rendered, and the loss
 * only showed up if you happened to be typing at the moment an event arrived.
 */

interface Row {
  id: string
  name: string
}

function stateOf(rows: Row[] | undefined, overrides: Partial<ResourceState<Row[]>> = {}): ResourceState<Row[]> {
  return {
    data: rows,
    error: undefined,
    loading: false,
    loaded: rows !== undefined,
    ...overrides,
  }
}

function nameView(): { node: Node; update(data: Row[]): void } {
  const name = h('span', { class: 'name' })
  const node = h('form', { class: 'content' }, name, h('input', { class: 'draft', type: 'text' }))
  return { node, update: (data) => setText(name, data[0].name) }
}

function mounted(view: ResourceView<Row[]>): HTMLElement {
  document.body.append(view.el)
  return view.el
}

describe('ResourceView', () => {
  it('builds the content once and reuses its DOM across refreshes', () => {
    let builds = 0
    const view = new ResourceView<Row[]>({ content: () => (builds++, nameView()) })
    const root = mounted(view)
    view.update(stateOf([{ id: 'a', name: 'first' }]))
    const before = root.querySelector('.content')
    expect(before).toBeTruthy()

    // A refresh that returns an equal-but-new value, exactly as a poll does.
    view.update(stateOf([{ id: 'a', name: 'first' }]))

    expect(root.querySelector('.content')).toBe(before)
    expect(builds).toBe(1)
  })

  it('preserves form input across a refresh, and still updates the data', () => {
    const view = new ResourceView<Row[]>({ content: nameView })
    const root = mounted(view)
    view.update(stateOf([{ id: 'a', name: 'first' }]))

    const input = root.querySelector('.draft') as HTMLInputElement
    input.value = 'half-typed edit'

    view.update(stateOf([{ id: 'a', name: 'renamed by somebody else' }]))

    const after = root.querySelector('.draft') as HTMLInputElement
    expect(after).toBe(input)
    expect(after.value).toBe('half-typed edit')
    expect(root.querySelector('.name')?.textContent).toBe('renamed by somebody else')
  })

  it('updates a keyed list in place on refresh', () => {
    const view = new ResourceView<Row[]>({
      isEmpty: (rows) => rows.length === 0,
      content: () => {
        const list = h('ul')
        return {
          node: list,
          update: (rows) =>
            syncKeyed(list, rows, (row) => row.id, () => h('li', { class: 'row' }), (li, row) => setText(li, row.name)),
        }
      },
    })
    const root = mounted(view)
    view.update(stateOf([{ id: 'a', name: 'alpha' }, { id: 'b', name: 'beta' }]))
    const listBefore = root.querySelector('ul')
    const firstRow = root.querySelector('.row')
    expect(root.querySelectorAll('.row')).toHaveLength(2)

    view.update(stateOf([{ id: 'a', name: 'alpha' }, { id: 'b', name: 'beta' }, { id: 'c', name: 'gamma' }]))

    // The <ul> and the existing rows are the same nodes; only a row was added.
    expect(root.querySelector('ul')).toBe(listBefore)
    expect(root.querySelector('.row')).toBe(firstRow)
    expect(root.querySelectorAll('.row')).toHaveLength(3)
  })

  it('shows a skeleton before the first load and never builds with undefined', () => {
    let sawUndefined = false
    const view = new ResourceView<Row[]>({
      content: (data) => {
        if (data === undefined) sawUndefined = true
        return nameView()
      },
    })
    const root = mounted(view)
    view.update(stateOf(undefined, { loading: true }))

    expect(root.querySelector<HTMLElement>('.skeleton')?.closest('[hidden]')).toBeNull()
    expect(root.querySelector('.content')).toBeNull()

    view.update(stateOf([{ id: 'a', name: 'first' }]))

    expect(root.querySelector('.content')).toBeTruthy()
    expect(root.querySelector<HTMLElement>('.skeleton')?.closest('[hidden]')).not.toBeNull()
    expect(sawUndefined).toBe(false)
  })

  it('keeps the previous content visible when a refresh fails', () => {
    const view = new ResourceView<Row[]>({ content: nameView })
    const root = mounted(view)
    view.update(stateOf([{ id: 'a', name: 'first' }]))
    const before = root.querySelector('.content')

    view.update(stateOf([{ id: 'a', name: 'first' }], { error: new Error('coordinator unreachable') }))

    // Error beside the data, not instead of it: an operator watching a running
    // job needs the last known state more than an error screen.
    const alert = root.querySelector<HTMLElement>('.alert-error')!
    expect(alert.hidden).toBe(false)
    expect(alert.textContent).toContain('Showing the last known state. coordinator unreachable')
    expect(root.querySelector('.content')).toBe(before)
    expect(root.querySelector<HTMLElement>('.resource-content')!.hidden).toBe(false)
  })

  it('shows the empty state, then the content when data arrives, then hides it without destroying it', () => {
    const view = new ResourceView<Row[]>({
      isEmpty: (rows) => rows.length === 0,
      empty: h('div', { class: 'nothing' }, 'No rows.'),
      content: nameView,
    })
    const root = mounted(view)
    const nothing = (): HTMLElement => root.querySelector('.nothing')!

    view.update(stateOf([]))
    expect(nothing().hidden).toBe(false)
    expect(root.querySelector('.content')).toBeNull()

    view.update(stateOf([{ id: 'a', name: 'first' }]))
    expect(nothing().hidden).toBe(true)
    const content = root.querySelector('.content')
    expect(content?.textContent).toBe('first')

    view.update(stateOf([]))
    expect(nothing().hidden).toBe(false)
    expect(root.querySelector<HTMLElement>('.resource-content')!.hidden).toBe(true)
    view.update(stateOf([{ id: 'a', name: 'back' }]))
    expect(root.querySelector('.content')).toBe(content)
  })
})
