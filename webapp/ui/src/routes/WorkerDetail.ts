import { api } from '~/api/client.ts'
import type { ListWorkersResponse, WorkerSummary } from '~/api/csilapi/types.gen.ts'
import { RcPage } from '~/app/page.ts'
import { describeError, notPermitted } from '~/components/ManagementPage.ts'
import { relativeTime, skeleton } from '~/components/States.ts'
import { workerImageState, type WorkerImageStateCard } from '~/components/WorkerImageState.ts'
import { SESSION, session } from '~/lib/session.ts'
import { h, setAttr, setText, show } from '~/ui/dom.ts'
import { define } from '~/ui/element.ts'
import { Loader } from '~/ui/load.ts'
import { ROUTE, href } from '~/ui/router.ts'
import { workerHeadroom, workerLocation } from './Workers.ts'

/** The online state of a worker: absent for a coordinator that does not send it. */
export function onlineText(worker: Pick<WorkerSummary, 'online'>): string {
  if (worker.online === undefined) return '—'
  return worker.online ? 'online' : 'offline'
}

/**
 * One worker: its state, and its image state and image set
 * (get-worker-image-state, in the Images card).
 *
 * There is no get-worker operation, so the page reads list-workers and picks
 * the worker. The same capability gate as the Workers page applies; the
 * coordinator checks each call again.
 */
export class WorkerDetailPage extends RcPage {
  static watches = [ROUTE, SESSION]

  #workers = new Loader<string, ListWorkersResponse>(this, () => api.listWorkers({}))
  #refused!: HTMLElement
  #body!: HTMLElement
  #loading!: HTMLElement
  #error!: HTMLDivElement
  #missing!: HTMLElement
  #title!: HTMLHeadingElement
  #facts!: HTMLElement
  #images: WorkerImageStateCard | null = null
  #imageSlot!: HTMLElement
  #values: Record<string, HTMLElement> = {}

  #workerId(): string {
    return this.route().id ?? ''
  }

  protected build(): void {
    this.#refused = notPermitted('workers')
    this.#title = h('h1')
    this.#loading = skeleton(3)
    this.#error = h('div', { class: 'alert alert-error', role: 'alert', hidden: true })
    this.#missing = h('div', { class: 'empty', hidden: true }, 'No worker with this id.')
    const fact = (key: string, label: string, cls = ''): HTMLElement[] => {
      const dd = h('dd', { class: `worker-${key} ${cls}`.trim() })
      this.#values[key] = dd
      return [h('dt', null, label), dd]
    }
    this.#facts = h(
      'dl',
      { class: 'worker-image-facts' },
      fact('id', 'Worker id', 'mono'),
      fact('version', 'Worker version', 'mono'),
      fact('status', 'Status'),
      fact('online', 'Online'),
      fact('platform', 'Platform', 'mono'),
      fact('location', 'Location'),
      fact('leases', 'Active leases'),
      fact('headroom', 'Headroom'),
      fact('seen', 'Last seen'),
    )
    this.#imageSlot = h('div')
    this.#body = h(
      'div',
      { class: 'page' },
      h('div', { style: { marginBottom: 'var(--space-4)' } }, h('a', { class: 'meta', href: href('/workers') }, '← Workers')),
      h('div', { class: 'page-header' }, h('div', null, this.#title, h('p', { class: 'meta' }, 'The state of one worker and the images that the coordinator wants it to hold.'))),
      this.#error,
      this.#loading,
      this.#missing,
      h('div', { class: 'card worker-facts-card' }, this.#facts),
      this.#imageSlot,
    )
    this.append(this.#refused, this.#body)
  }

  render(): void {
    const allowed = Boolean(session()?.capabilities?.manageWorkers)
    show(this.#refused, !allowed)
    show(this.#body, allowed)
    if (!allowed) return
    this.#workers.sync('all')
    const id = this.#workerId()
    const { data, error, loaded } = this.#workers.state
    const worker = data?.workers.find((w) => w.workerId === id)
    show(this.#loading, !loaded && !error)
    show(this.#error, error)
    setText(this.#error, error ? describeError(error, 'The workers could not be loaded.') : '')
    show(this.#missing, loaded && !worker)
    show(this.#facts.parentElement as HTMLElement, worker)
    setText(this.#title, worker ? `Worker ${worker.hostname || worker.workerKey}` : 'Worker')
    if (!this.#images && id) {
      this.#images = workerImageState(id)
      this.#imageSlot.append(this.#images)
    }
    this.#images?.setWorker(id)
    if (!worker) return
    const v = this.#values
    setText(v.id, worker.workerId)
    setText(v.status, worker.status)
    setText(v.online, onlineText(worker))
    setText(v.platform, `${worker.os}/${worker.arch}`)
    setText(v.location, workerLocation(worker))
    setText(v.leases, String(worker.activeLeaseCount))
    setText(v.headroom, workerHeadroom(worker))
    setText(v.version, worker.workerVersion || '—')
    setText(v.seen, worker.lastSeenAt ? relativeTime(worker.lastSeenAt) : '—')
    setAttr(this.#title, 'title', worker.workerId)
  }
}

export const tag = 'rc-worker-detail-page'
define(tag, WorkerDetailPage)
