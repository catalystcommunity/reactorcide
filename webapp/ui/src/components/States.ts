import type { ResourceState } from '~/store/index.ts'
import { h, setText, show, type Child } from '~/ui/dom.ts'

/**
 * Loading, empty and error presentation, in one place.
 *
 * The rule these encode: a refresh that fails must never blank the page. An
 * operator watching a running job wants the last known state with a warning
 * beside it, not an error screen where their data was.
 */

export function skeleton(rows = 5): HTMLElement {
  return h(
    'div',
    { class: 'stack', 'aria-hidden': 'true' },
    Array.from({ length: rows }, (_, index) =>
      h('div', { class: 'skeleton', style: { width: `${100 - (index % 3) * 12}%`, height: '1.6rem' } }),
    ),
  )
}

export function emptyState(title: string, ...children: Child[]): HTMLElement {
  return h('div', { class: 'empty' }, h('div', { class: 'empty-title' }, title), children.length ? h('div', null, children) : null)
}

/** An error box whose text can change in place. */
export class ErrorNotice {
  readonly el: HTMLDivElement
  readonly #text: HTMLSpanElement

  constructor() {
    this.#text = h('span')
    this.el = h('div', { class: 'alert alert-error', role: 'alert', hidden: true }, this.#text)
  }

  /**
   * `stale` means data is on screen but no longer fresh: say exactly that,
   * rather than implying the visible data is wrong.
   */
  set(error: Error | string | undefined, stale = false): void {
    show(this.el, error)
    if (!error) return
    const message = typeof error === 'string' ? error : error.message
    setText(this.#text, stale ? `Showing the last known state. ${message}` : message)
  }
}

/** The part of a page that shows a resource's data. Built once, updated many times. */
export interface ContentView<T> {
  node: Node
  update(data: T): void
}

/**
 * One of loading / error / empty / content for a store resource.
 *
 * The CONTENT IS BUILT ONCE -- when the resource first has data -- and every
 * later refresh calls `update(data)` on the same nodes. That is the whole
 * point. The first Solid version rebuilt the subtree on each refresh, and on a
 * live page that fires constantly: a `project_update` while somebody was
 * halfway through the settings form threw their edits away, and every
 * `job_update` reset the log viewer's buffer and scroll position.
 *
 * When the data becomes empty the content is hidden, not destroyed, so it
 * comes back with its state if the data does.
 */
export class ResourceView<T> {
  readonly el: HTMLDivElement
  readonly #error = new ErrorNotice()
  readonly #skeleton = skeleton()
  readonly #empty: HTMLElement
  readonly #slot: HTMLDivElement
  #content: ContentView<T> | null = null
  readonly #build: (data: T) => ContentView<T>
  readonly #isEmpty?: (data: T) => boolean

  constructor(options: { content: (data: T) => ContentView<T>; isEmpty?: (data: T) => boolean; empty?: HTMLElement }) {
    this.#build = options.content
    this.#isEmpty = options.isEmpty
    this.#empty = options.empty ?? emptyState('Nothing here yet.')
    this.#slot = h('div', { class: 'resource-content' })
    this.el = h('div', { class: 'resource-view' }, this.#error.el, this.#skeleton, this.#slot, this.#empty)
    show(this.#empty, false)
    show(this.#slot, false)
  }

  update(state: ResourceState<T>): void {
    this.#error.set(state.error, state.loaded)
    show(this.#skeleton, !state.loaded && !state.error)

    const data = state.loaded ? state.data : undefined
    const hasContent = data !== undefined && !this.#isEmpty?.(data)
    if (hasContent) {
      if (!this.#content) {
        this.#content = this.#build(data)
        this.#slot.append(this.#content.node)
      }
      this.#content.update(data)
    }
    show(this.#slot, hasContent)
    show(this.#empty, state.loaded && !hasContent)
  }
}

/** A relative time that stays readable in a dense table. */
export function relativeTime(iso: string | undefined): string {
  if (!iso) return '—'
  const then = Date.parse(iso)
  if (Number.isNaN(then)) return '—'
  const seconds = Math.round((Date.now() - then) / 1000)
  if (seconds < 45) return 'just now'
  if (seconds < 90) return 'a minute ago'
  const minutes = Math.round(seconds / 60)
  if (minutes < 60) return `${minutes} minutes ago`
  const hours = Math.round(minutes / 60)
  if (hours < 24) return `${hours} hour${hours === 1 ? '' : 's'} ago`
  const days = Math.round(hours / 24)
  if (days < 30) return `${days} day${days === 1 ? '' : 's'} ago`
  return new Date(then).toLocaleDateString()
}

/** A duration between two timestamps, or from a start to now. */
export function duration(startedAt?: string, completedAt?: string): string {
  if (!startedAt) return '—'
  const start = Date.parse(startedAt)
  if (Number.isNaN(start)) return '—'
  const end = completedAt ? Date.parse(completedAt) : Date.now()
  const seconds = Math.max(0, Math.round((end - start) / 1000))
  if (seconds < 60) return `${seconds}s`
  const minutes = Math.floor(seconds / 60)
  const remainder = seconds % 60
  if (minutes < 60) return `${minutes}m ${remainder}s`
  const hours = Math.floor(minutes / 60)
  return `${hours}h ${minutes % 60}m`
}
