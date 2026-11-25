import type { PushButton, TextField } from '@catalystcommunity/controls'
import type { EnumChoice, ProjectSummary } from '~/api/csilapi/types.gen.ts'
import { ChoiceSelect } from '~/components/ChoiceInput.ts'
import { button, setFieldValue, textField } from '~/ui/controls.ts'
import { h, setText, show } from '~/ui/dom.ts'
import { setQuery } from '~/ui/router.ts'
import './list-controls.css'

/**
 * The name, branch and project filters and the pager of the job and workflow
 * lists. Every value is in the URL query (`name`, `branch`, `project`,
 * `page`), so a filtered list is a link that can be shared, and a reload
 * keeps it.
 */

/** Rows per page of a list. */
export const LIST_PAGE_SIZE = 50

/** Typing waits this long before the list loads again. */
const TYPING_DELAY_MS = 300

export interface ListQuery {
  name: string
  branch: string
  projectId: string
  /** The number of rows before this page. */
  offset: number
}

/** Reads the list filters and the page from a route query. */
export function listQuery(query: URLSearchParams): ListQuery {
  const page = Number.parseInt(query.get('page') ?? '', 10)
  return {
    name: query.get('name') ?? '',
    branch: query.get('branch') ?? '',
    projectId: query.get('project') ?? '',
    offset: Number.isFinite(page) && page > 1 ? (page - 1) * LIST_PAGE_SIZE : 0,
  }
}

/** The project choices of a filter, sorted by name. */
export function projectChoices(projects: readonly ProjectSummary[] | undefined): EnumChoice[] {
  return [...(projects ?? [])]
    .sort((a, b) => a.name.localeCompare(b.name))
    .map((p) => ({ value: p.projectId, label: p.name, description: p.repoUrl }))
}

/**
 * The text and project filters. A filter change goes back to the first page.
 * Make it in build(); call update() from render() and dispose() on disconnect.
 */
export class ListFilterBar {
  readonly el: HTMLElement
  readonly #name: TextField
  readonly #branch: TextField
  readonly #project: ChoiceSelect
  #timer: ReturnType<typeof setTimeout> | null = null

  constructor(options: { nameLabel: string }) {
    this.#name = textField({
      type: 'search',
      ariaLabel: options.nameLabel,
      placeholder: options.nameLabel,
      onInput: () => this.#later(),
      onChange: () => this.#now(),
    })
    this.#branch = textField({
      type: 'search',
      ariaLabel: 'Filter by branch',
      placeholder: 'Branch: main, todpunk/*',
      mono: true,
      onInput: () => this.#later(),
      onChange: () => this.#now(),
    })
    this.#project = new ChoiceSelect({
      ariaLabel: 'Filter by project',
      placeholder: 'All projects',
      onChange: (value) => setQuery({ project: value || undefined, page: undefined }),
    })
    this.#name.classList.add('list-filter-name')
    this.#branch.classList.add('list-filter-branch')
    this.el = h('div', { class: 'row list-filters' }, this.#name, this.#branch, this.#project.el)
  }

  update(query: ListQuery, projects: readonly ProjectSummary[] | undefined): void {
    // A pending change is newer than the URL: do not type over it.
    if (this.#timer === null) {
      setFieldValue(this.#name, query.name)
      setFieldValue(this.#branch, query.branch)
    }
    this.#project.update(projectChoices(projects), query.projectId)
  }

  dispose(): void {
    if (this.#timer !== null) clearTimeout(this.#timer)
    this.#timer = null
  }

  #later(): void {
    this.dispose()
    this.#timer = setTimeout(() => this.#now(), TYPING_DELAY_MS)
  }

  #now(): void {
    this.dispose()
    setQuery({ name: this.#name.value.trim() || undefined, branch: this.#branch.value.trim() || undefined, page: undefined })
  }
}

/** "Newer" and "Older" for a list page, with the range it shows. */
export class Pager {
  readonly el: HTMLElement
  readonly #range: HTMLElement
  readonly #newer: PushButton
  readonly #older: PushButton
  #offset = 0

  constructor() {
    this.#range = h('span', { class: 'meta pager-range', 'aria-live': 'polite' })
    this.#newer = button('Newer', { small: true, onClick: () => this.#go(this.#offset - LIST_PAGE_SIZE) })
    this.#older = button('Older', { small: true, onClick: () => this.#go(this.#offset + LIST_PAGE_SIZE) })
    this.el = h('nav', { class: 'row pager', 'aria-label': 'Pages' }, this.#range, this.#newer, this.#older)
  }

  /** `shown` is the number of rows on this page. */
  update(page: { total: number; offset: number; shown: number } | undefined): void {
    const total = page?.total ?? 0
    const offset = page?.offset ?? 0
    this.#offset = offset
    show(this.el, total > LIST_PAGE_SIZE || offset > 0)
    setText(this.#range, pageRange(total, offset, page?.shown ?? 0))
    this.#newer.disabled = offset <= 0
    this.#older.disabled = offset + LIST_PAGE_SIZE >= total
  }

  #go(offset: number): void {
    const page = Math.floor(Math.max(0, offset) / LIST_PAGE_SIZE) + 1
    setQuery({ page: page > 1 ? String(page) : undefined })
    window.scrollTo?.({ top: 0 })
  }
}

/** "51–100 of 1,320". */
export function pageRange(total: number, offset: number, shown: number): string {
  const count = total.toLocaleString()
  if (shown === 0) return offset > 0 ? `Past the last row (${count} in all)` : `0 of ${count}`
  return `${(offset + 1).toLocaleString()}–${(offset + shown).toLocaleString()} of ${count}`
}
