import { api } from '~/api/client.ts'
import type { GetWorkerImageStateResponse, ImageFailureSummary, WorkerImageSetEntrySummary } from '~/api/csilapi/types.gen.ts'
import { ActionError, describeError } from '~/components/ManagementPage.ts'
import { h, setAttr, setText, show, syncKeyed } from '~/ui/dom.ts'
import { RcElement, define } from '~/ui/element.ts'
import { Loader } from '~/ui/load.ts'
import { imageFailureText, imageStatusClass, imageStatusText, imageVersionText, shortDigest } from './imageText.ts'
import './images.css'

const REFRESH_MS = 15_000

/**
 * The image state of one worker (get-worker-image-state): the image status,
 * the wanted and the reported image set version, and the set that the
 * coordinator computes now, with the alias names and whether each image is
 * ready on the worker. Ready digests that are not in the set are listed
 * too: the worker can remove those images.
 */
export class WorkerImageStateCard extends RcElement {
  workerId = ''

  #load = new Loader<string, GetWorkerImageStateResponse>(this, (workerId) => api.getWorkerImageState({ workerId }))
  #error = new ActionError()
  #notAware!: HTMLParagraphElement
  #facts!: HTMLElement
  #status!: HTMLSpanElement
  #detail!: HTMLElement
  #runtime!: HTMLElement
  #state!: HTMLElement
  #version!: HTMLElement
  #tableWrap!: HTMLElement
  #tbody!: HTMLTableSectionElement
  #none!: HTMLElement
  #extra!: HTMLParagraphElement
  #failedWrap!: HTMLElement
  #failedList!: HTMLUListElement

  setWorker(workerId: string): void {
    if (this.workerId === workerId) return
    this.workerId = workerId
    this.invalidate()
  }

  protected build(): void {
    this.classList.add('card')
    this.#notAware = h('p', { class: 'meta worker-image-not-aware', hidden: true }, 'This worker does not use image sets. It cannot claim a job that names an image alias.')
    this.#status = h('span')
    this.#detail = h('dd', { class: 'worker-image-detail' })
    this.#runtime = h('dd', { class: 'mono worker-image-runtime' })
    this.#state = h('dd', { class: 'worker-image-report' })
    this.#version = h('dd', { class: 'worker-image-version' })
    this.#facts = h(
      'dl',
      { class: 'worker-image-facts' },
      h('dt', null, 'Image status'),
      h('dd', null, this.#status),
      h('dt', null, 'Detail'),
      this.#detail,
      h('dt', null, 'Runtime'),
      this.#runtime,
      h('dt', null, 'Newest report'),
      this.#state,
      h('dt', null, 'Image set'),
      this.#version,
    )
    this.#tbody = h('tbody')
    this.#tableWrap = h(
      'div',
      { class: 'scroll-x' },
      h(
        'table',
        { class: 'table worker-images' },
        h('thead', null, h('tr', null, h('th', null, 'Aliases'), h('th', null, 'Reference'), h('th', null, 'Digest'), h('th', null, 'Kind'), h('th', null, 'Ready'))),
        this.#tbody,
      ),
    )
    this.#none = h('p', { class: 'meta' }, 'The image set of this worker is empty: no alias serves it.')
    this.#extra = h('p', { class: 'meta worker-image-extra', hidden: true })
    this.#failedList = h('ul', { class: 'image-failure-list' })
    this.#failedWrap = h(
      'div',
      { class: 'worker-image-failed', hidden: true },
      h('h4', { class: 'section-title' }, 'Images that failed'),
      h('p', { class: 'meta' }, 'The worker does not claim jobs for these images. A permanent failure needs a fix of the reference, the registry, or the credentials.'),
      this.#failedList,
    )
    this.append(
      h('h3', { class: 'section-title' }, 'Images'),
      this.#error.el,
      this.#notAware,
      this.#facts,
      this.#tableWrap,
      this.#none,
      this.#failedWrap,
      this.#extra,
    )
  }

  protected connected(): void {
    const timer = setInterval(() => {
      if (document.visibilityState === 'visible' && this.workerId) void this.#load.reload()
    }, REFRESH_MS)
    this.own(() => clearInterval(timer))
  }

  render(): void {
    this.#load.sync(this.workerId || undefined)
    const { data, error, loaded } = this.#load.state
    this.#error.set(error ? describeError(error, 'The image state could not be loaded.') : undefined)
    const aware = Boolean(data?.imageAware)
    show(this.#notAware, loaded && data && !aware)
    show(this.#facts, aware)
    show(this.#tableWrap, aware && (data?.images.length ?? 0) > 0)
    show(this.#none, aware && data?.images.length === 0)
    if (!data || !aware) {
      show(this.#extra, false)
      show(this.#failedWrap, false)
      return
    }
    setText(this.#status, imageStatusText(data.imageStatus))
    setAttr(this.#status, 'class', imageStatusClass(data.imageStatus))
    setText(this.#detail, data.imageStatusDetail || '—')
    setText(this.#runtime, data.imageRuntime || '—')
    setText(this.#state, data.state || 'no report')
    setText(this.#version, imageVersionText(data.imageSetVersion, data.imageReportedVersion))
    syncKeyed(
      this.#tbody,
      data.images,
      (entry) => entry.digest,
      () =>
        h(
          'tr',
          null,
          h('td', { class: 'mono image-aliases' }),
          h('td', { class: 'mono image-ref image-entry-ref' }),
          h('td', { class: 'mono meta image-entry-digest' }),
          h('td', { class: 'meta image-entry-kind' }),
          h(
            'td',
            null,
            h('span', { class: 'image-entry-ready' }),
            ' ',
            h('span', { class: 'image-entry-pending status status-info', hidden: true, title: 'The pending pin of a rollout. The worker does not wait for it before it is ready.' }, 'pending'),
            h('div', { class: 'meta image-failure image-entry-failure', hidden: true }),
          ),
        ),
      (row, entry) => updateEntry(row, entry),
    )
    const failed = data.failed ?? []
    show(this.#failedWrap, failed.length > 0)
    syncKeyed(
      this.#failedList,
      failed,
      (f) => f.digest,
      () => h('li', null, h('span', { class: 'mono image-failure-digest' }), ' ', h('span', { class: 'image-failure-text' })),
      (li, f) => updateFailureItem(li, f),
    )
    const inSet = new Set(data.images.map((e) => e.digest))
    const extra = data.readyDigests.filter((d) => !inSet.has(d))
    show(this.#extra, extra.length > 0)
    setText(this.#extra, extra.length ? `Ready on the worker but not in the set (the worker can remove them): ${extra.map(shortDigest).join(', ')}` : '')
  }
}

function updateEntry(row: Element, entry: WorkerImageSetEntrySummary): void {
  const q = (cls: string): HTMLElement => row.querySelector(cls)!
  setText(q('.image-aliases'), entry.aliases.join(', ') || '—')
  setText(q('.image-entry-ref'), entry.ref)
  setText(q('.image-entry-digest'), shortDigest(entry.digest))
  setAttr(q('.image-entry-digest'), 'title', entry.digest)
  setText(q('.image-entry-kind'), entry.kind)
  const ready = q('.image-entry-ready')
  setText(ready, entry.ready ? 'ready' : 'not ready')
  setAttr(ready, 'class', `image-entry-ready status ${entry.ready ? 'status-ok' : 'status-info'}`)
  show(q('.image-entry-pending'), Boolean(entry.pending))
  const failure = q('.image-entry-failure')
  setText(failure, entry.failure ? imageFailureText(entry.failure) : '')
  setAttr(failure, 'class', `meta image-failure image-entry-failure${entry.failure?.permanent ? ' image-failure-permanent' : ''}`)
  show(failure, Boolean(entry.failure))
}

/** One failed image of a list: the short digest and the failure text. */
export function updateFailureItem(li: Element, f: ImageFailureSummary): void {
  const digest = li.querySelector('.image-failure-digest')!
  setText(digest, shortDigest(f.digest))
  setAttr(digest, 'title', f.digest)
  setText(li.querySelector('.image-failure-text')!, imageFailureText(f))
  setAttr(li, 'class', f.permanent ? 'image-failure-permanent' : '')
}

export const workerImageStateTag = 'rc-worker-image-state'
define(workerImageStateTag, WorkerImageStateCard)

export function workerImageState(workerId: string): WorkerImageStateCard {
  const el = document.createElement(workerImageStateTag) as WorkerImageStateCard
  el.workerId = workerId
  return el
}
