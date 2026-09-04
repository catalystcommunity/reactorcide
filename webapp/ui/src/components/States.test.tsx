import { describe, expect, it } from 'vitest'
import { render } from '@solidjs/testing-library'
import { createSignal, For } from 'solid-js'
import { ResourceView } from './States.tsx'
import type { ResourceState } from '~/store/index.ts'

/**
 * ResourceView's contract, pinned.
 *
 * The bug these guard against was subtle and expensive. The first version took
 * `children: (data: T)` and called it inside the JSX, which made the call site
 * itself reactive: every refresh destroyed the whole subtree and built a new
 * one. On a live page that fires constantly, so a `project_update` arriving
 * while somebody was halfway through the settings form silently discarded their
 * edits, and every `job_update` reset the log viewer's scroll position.
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

describe('ResourceView', () => {
  it('builds the subtree once and reuses its DOM across refreshes', () => {
    const [state, setState] = createSignal(stateOf([{ id: 'a', name: 'first' }]))

    const { container } = render(() => (
      <ResourceView state={state()}>
        {(data) => (
          <div class="content">
            <span class="marker">{data()[0].name}</span>
          </div>
        )}
      </ResourceView>
    ))

    const contentBefore = container.querySelector('.content')
    expect(contentBefore).toBeTruthy()

    // A refresh that returns an equal-but-new value, exactly as a poll does.
    setState(stateOf([{ id: 'a', name: 'first' }]))

    expect(container.querySelector('.content')).toBe(contentBefore)
  })

  it('preserves component state across a refresh', () => {
    // The failure this reproduces: an operator typing into a form loses their
    // input the moment an unrelated live event arrives.
    const [state, setState] = createSignal(stateOf([{ id: 'a', name: 'first' }]))

    const { container } = render(() => (
      <ResourceView state={state()}>
        {(data) => (
          <form>
            <span class="name">{data()[0].name}</span>
            <input class="draft" type="text" />
          </form>
        )}
      </ResourceView>
    ))

    const input = container.querySelector('.draft') as HTMLInputElement
    input.value = 'half-typed edit'

    setState(stateOf([{ id: 'a', name: 'renamed by somebody else' }]))

    const after = container.querySelector('.draft') as HTMLInputElement
    expect(after).toBe(input)
    expect(after.value).toBe('half-typed edit')
    // And the data still updated.
    expect(container.querySelector('.name')?.textContent).toBe('renamed by somebody else')
  })

  it('updates a list in place on refresh', () => {
    const [state, setState] = createSignal(
      stateOf([
        { id: 'a', name: 'alpha' },
        { id: 'b', name: 'beta' },
      ]),
    )

    const { container } = render(() => (
      <ResourceView state={state()} isEmpty={(rows) => rows.length === 0}>
        {(data) => (
          <ul>
            <For each={data()}>{(row) => <li class="row">{row.name}</li>}</For>
          </ul>
        )}
      </ResourceView>
    ))

    const listBefore = container.querySelector('ul')
    expect(container.querySelectorAll('.row')).toHaveLength(2)

    setState(
      stateOf([
        { id: 'a', name: 'alpha' },
        { id: 'b', name: 'beta' },
        { id: 'c', name: 'gamma' },
      ]),
    )

    // The <ul> is the same node; only a row was added.
    expect(container.querySelector('ul')).toBe(listBefore)
    expect(container.querySelectorAll('.row')).toHaveLength(3)
  })

  it('shows a skeleton before the first load and never calls children with undefined', () => {
    let sawUndefined = false
    const [state, setState] = createSignal(stateOf(undefined, { loading: true }))

    const { container } = render(() => (
      <ResourceView state={state()}>
        {(data) => {
          // Calling children before data exists would throw here in every real
          // route, which all dereference the value immediately.
          if (data() === undefined) sawUndefined = true
          return <div class="content">{data()?.length ?? 0}</div>
        }}
      </ResourceView>
    ))

    expect(container.querySelector('.skeleton')).toBeTruthy()
    expect(container.querySelector('.content')).toBeNull()

    setState(stateOf([{ id: 'a', name: 'first' }]))

    expect(container.querySelector('.content')).toBeTruthy()
    expect(sawUndefined).toBe(false)
  })

  it('keeps the previous content visible when a refresh fails', () => {
    const [state, setState] = createSignal(stateOf([{ id: 'a', name: 'first' }]))

    const { container } = render(() => (
      <ResourceView state={state()}>
        {(data) => <div class="content">{data()[0].name}</div>}
      </ResourceView>
    ))

    const contentBefore = container.querySelector('.content')

    setState(
      stateOf([{ id: 'a', name: 'first' }], { error: new Error('coordinator unreachable') }),
    )

    // Error beside the data, not instead of it: an operator watching a running
    // job needs the last known state more than an error screen.
    expect(container.querySelector('.alert-error')?.textContent).toContain('coordinator unreachable')
    expect(container.querySelector('.content')).toBe(contentBefore)
  })

  it('renders the empty state and swaps to content when data arrives', () => {
    const [state, setState] = createSignal(stateOf([] as Row[]))

    const { container } = render(() => (
      <ResourceView
        state={state()}
        isEmpty={(rows) => rows.length === 0}
        empty={<div class="nothing">No rows.</div>}
      >
        {(data) => <div class="content">{data().length}</div>}
      </ResourceView>
    ))

    expect(container.querySelector('.nothing')).toBeTruthy()
    expect(container.querySelector('.content')).toBeNull()

    setState(stateOf([{ id: 'a', name: 'first' }]))

    expect(container.querySelector('.nothing')).toBeNull()
    expect(container.querySelector('.content')?.textContent).toBe('1')
  })
})
