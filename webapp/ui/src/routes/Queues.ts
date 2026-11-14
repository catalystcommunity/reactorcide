import type { PushButton, TextField } from '@catalystcommunity/controls'
import { api } from '~/api/client.ts'
import type { ListQueuesResponse, QueueSummary } from '~/api/csilapi/types.gen.ts'
import { RcPage } from '~/app/page.ts'
import { Action, ActionError, notPermitted } from '~/components/ManagementPage.ts'
import { SESSION, session } from '~/lib/session.ts'
import { button, confirmAction, setButtonText, textField } from '~/ui/controls.ts'
import { h, setText, show, syncKeyed } from '~/ui/dom.ts'
import { define } from '~/ui/element.ts'
import { Loader } from '~/ui/load.ts'
import { ROUTE, href } from '~/ui/router.ts'

/**
 * Characteristic queues.
 *
 * Global-admin only, and deliberately so: a queue is instance-wide routing, not
 * an org-scoped resource, and deleting one cancels every non-terminal job on it.
 */
export class QueuesPage extends RcPage {
  static watches = [ROUTE, SESSION]

  #refused!: HTMLElement
  #body!: HTMLElement
  #rows!: HTMLTableSectionElement
  #table!: HTMLElement
  #empty!: HTMLElement
  #error = new ActionError()
  #action = new Action(() => this.invalidate())
  #queues = new Loader<string, ListQueuesResponse>(this, () => api.listQueues({}))
  #views = new WeakMap<Element, QueueRow>()

  protected build(): void {
    this.#refused = notPermitted('queues')
    this.#rows = h('tbody')
    this.#table = h(
      'div',
      { class: 'scroll-x' },
      h(
        'table',
        { class: 'table' },
        h('thead', null, h('tr', null, h('th', null, 'Name'), h('th', null, 'Characteristics'), h('th', null, 'Backlog'), h('th', null, 'Default'), h('th'))),
        this.#rows,
      ),
    )
    this.#empty = h('div', { class: 'empty' }, 'No queues yet.')
    this.#body = h(
      'div',
      { class: 'page' },
      h(
        'div',
        { class: 'page-header' },
        h(
          'div',
          null,
          h('h1', null, 'Queues'),
          h(
            'p',
            { class: 'meta' },
            'Jobs route to a queue by their characteristics. A queue is created automatically the first time a job needs one, so this page is mostly for naming and cleanup.',
          ),
        ),
        h('a', { href: href('/workers'), class: 'btn btn-sm' }, 'Back to workers'),
      ),
      h('div', { class: 'card' }, this.#error.el, this.#table, this.#empty),
    )
    this.append(this.#refused, this.#body)
  }

  render(): void {
    const allowed = Boolean(session()?.is_global_admin)
    show(this.#refused, !allowed)
    show(this.#body, allowed)
    if (!allowed) return
    this.#queues.sync('all')

    this.#error.set(this.#action.error)
    const queues = this.#queues.state.data?.queues ?? []
    show(this.#table, queues.length > 0)
    show(this.#empty, queues.length === 0)
    syncKeyed(
      this.#rows,
      queues,
      (queue) => queue.queueId,
      () => {
        const view = new QueueRow(
          () => this.invalidate(),
          () => void this.#queues.reload(),
          (queue) => void this.#delete(queue),
        )
        this.#views.set(view.el, view)
        return view.el
      },
      (el, queue) => this.#views.get(el)!.update(queue, this.#action.busy),
    )
  }

  async #delete(queue: QueueSummary): Promise<void> {
    const ok = await confirmAction(
      `Delete queue "${queue.displayName}"? Every job still waiting on it (${queue.backlogCount}) will be cancelled.`,
      { okLabel: 'Delete queue' },
    )
    if (!ok) return
    await this.#action.run('The queue could not be deleted.', () => api.deleteQueue({ queueId: queue.queueId }))
    void this.#queues.reload()
  }
}

/** One queue, with an inline rename, since naming is the main thing this page is for. */
class QueueRow {
  readonly el: HTMLTableRowElement
  #queue!: QueueSummary
  #editing = false
  readonly #action: Action
  readonly #error = new ActionError()
  readonly #nameButton: PushButton
  readonly #form: HTMLFormElement
  readonly #field: TextField
  readonly #save: PushButton
  readonly #characteristics: HTMLTableCellElement
  readonly #backlog: HTMLTableCellElement
  readonly #isDefault: HTMLTableCellElement
  readonly #delete: PushButton

  constructor(invalidate: () => void, onRenamed: () => void, onDelete: (queue: QueueSummary) => void) {
    this.#action = new Action(invalidate)
    this.#nameButton = button('', {
      small: true,
      variant: 'flat',
      onClick: () => {
        this.#editing = true
        this.#field.value = this.#queue.displayName
        invalidate()
        requestAnimationFrame(() => this.#field.focus())
      },
    })
    this.#field = textField({ ariaLabel: 'Queue name' })
    this.#save = button('Save', { small: true, variant: 'primary', type: 'submit' })
    this.#form = h(
      'form',
      { class: 'row' },
      this.#field,
      this.#save,
      button('Cancel', {
        small: true,
        variant: 'flat',
        onClick: () => {
          this.#editing = false
          invalidate()
        },
      }),
      this.#error.el,
    )
    this.#form.addEventListener('submit', async (event) => {
      event.preventDefault()
      const ok = await this.#action.run('The queue could not be renamed.', () =>
        api.renameQueue({ queueId: this.#queue.queueId, displayName: this.#field.value }),
      )
      if (ok) {
        this.#editing = false
        onRenamed()
      }
    })
    this.#characteristics = h('td', { class: 'meta mono' })
    this.#backlog = h('td', { class: 'numeric' })
    this.#isDefault = h('td')
    this.#delete = button('Delete', { small: true, variant: 'danger', onClick: () => onDelete(this.#queue) })
    this.el = h('tr', null, h('td', null, this.#nameButton, this.#form), this.#characteristics, this.#backlog, this.#isDefault, h('td', null, this.#delete))
  }

  update(queue: QueueSummary, pageBusy: boolean): void {
    this.#queue = queue
    show(this.#nameButton, !this.#editing)
    show(this.#form, this.#editing)
    setButtonText(this.#nameButton, queue.displayName || 'unnamed')
    this.#save.disabled = this.#action.busy
    this.#error.set(this.#action.error)
    setText(this.#characteristics, queue.characteristics.map((c) => `${c.key}=${c.value}`).join(', ') || '—')
    setText(this.#backlog, String(queue.backlogCount))
    setText(this.#isDefault, queue.isDefault ? 'Yes' : 'No')
    show(this.#delete, !queue.isDefault)
    this.#delete.disabled = pageBusy
  }
}

export const tag = 'rc-queues-page'
define(tag, QueuesPage)
