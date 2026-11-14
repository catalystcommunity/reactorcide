import type { CheckBox, PushButton, TextField } from '@catalystcommunity/controls'
import { api } from '~/api/client.ts'
import type { WorkerPoolSummary } from '~/api/csilapi/types.gen.ts'
import { field } from '~/components/Field.ts'
import { ManagedOrgPage } from '~/components/ManagedOrg.ts'
import { Action, ActionError } from '~/components/ManagementPage.ts'
import { button, checkBox, clearField, confirmAction, setButtonText, textField } from '~/ui/controls.ts'
import { h, setText, show, syncKeyed } from '~/ui/dom.ts'
import { define } from '~/ui/element.ts'
import { Loader } from '~/ui/load.ts'
import { href } from '~/ui/router.ts'

/**
 * Worker classes.
 *
 * A class names a set of pools a job may run on. A PROTECTED class is the point
 * of the feature: only coordinator policy can route a job to one, so untrusted
 * pull-request CI cannot reach the pools that hold deployment credentials.
 *
 * Scoped to the organization chosen in the header (`?org=`). The worker-class
 * operations name their org by NAME (`organization`), not by id: the
 * coordinator resolves it with a lookup by name. The previous version passed
 * the caller's user id here, which matched no organization at all.
 */
export class WorkerClassesPage extends ManagedOrgPage {
  protected readonly what = 'worker classes'

  #classes = new Loader(this, (organization: string) => api.listWorkerClasses({ organization }))
  // Pool names are decoration for the table. The org's own pools are what an
  // org admin may list; a failure here shows ids rather than taking the page down.
  #pools = new Loader(this, async (orgId: string) => {
    try {
      return (await api.listPools({ orgId })).pools
    } catch {
      return [] as WorkerPoolSummary[]
    }
  })

  #action = new Action(() => this.invalidate())
  #name = ''
  #error!: ActionError
  #nameInput!: TextField
  #protected!: CheckBox
  #submit!: PushButton
  #tableWrap!: HTMLElement
  #none!: HTMLElement
  #tbody!: HTMLTableSectionElement

  #orgName(): string {
    return this.managed.org()?.name ?? ''
  }

  protected buildBody(): HTMLElement {
    this.#error = new ActionError()
    this.#nameInput = textField({
      mono: true,
      onInput: (value) => {
        this.#name = value
        this.invalidate()
      },
    })
    this.#protected = checkBox('Protected')
    this.#submit = button('Create class', { variant: 'primary', type: 'submit' })
    this.#tbody = h('tbody')
    this.#tableWrap = h(
      'div',
      { class: 'scroll-x' },
      h(
        'table',
        { class: 'table' },
        h('thead', null, h('tr', null, h('th', null, 'Class'), h('th', null, 'Protected'), h('th', null, 'Pools'), h('th'))),
        this.#tbody,
      ),
    )
    this.#none = h('div', { class: 'empty' }, 'No worker classes defined.')

    return h(
      'div',
      { class: 'page' },
      h(
        'div',
        { class: 'page-header' },
        h(
          'div',
          null,
          h('h1', null, 'Worker classes'),
          h(
            'p',
            { class: 'meta' },
            'A class names the pools a job may run on. Protected classes are reachable only through coordinator policy, which is what keeps untrusted CI away from privileged workers.',
          ),
        ),
        h('div', { class: 'row', style: { alignItems: 'center' } }, this.orgPicker(), h('a', { href: href('/workers'), class: 'btn btn-sm' }, 'Back to workers')),
      ),
      h(
        'div',
        { class: 'card' },
        h('h3', { class: 'section-title' }, 'New class'),
        h(
          'form',
          { onSubmit: (event: Event) => void this.#onCreate(event) },
          this.#error.el,
          field({ label: 'Name', required: true, hint: "Referenced from a job's worker class.", control: this.#nameInput }).el,
          h(
            'div',
            { class: 'checkbox-row' },
            h(
              'div',
              null,
              this.#protected,
              h(
                'div',
                { class: 'field-hint' },
                'Only coordinator policy can route a job to this class. Use it for any pool that holds credentials untrusted CI must not reach.',
              ),
            ),
          ),
          this.#submit,
        ),
      ),
      h('div', { class: 'card' }, h('h3', { class: 'section-title' }, 'Classes'), this.#tableWrap, this.#none),
    )
  }

  async #onCreate(event: Event): Promise<void> {
    event.preventDefault()
    const name = this.#name.trim()
    if (!name) return
    const organization = this.#orgName()
    const ok = await this.#action.run('The worker class could not be saved.', () =>
      api.putWorkerClass({ organization, workerClass: { name, protected: this.#protected.checked, poolIds: [] } }),
    )
    if (ok && this.isActive()) {
      this.#name = ''
      clearField(this.#nameInput)
      this.#protected.checked = false
      void this.#classes.reload()
    }
  }

  protected renderBody(): void {
    this.#classes.sync(this.#orgName() || undefined)
    this.#pools.sync(this.managed.orgId() || undefined)

    this.#error.set(this.#action.error)
    this.#submit.disabled = this.#action.busy || !this.#name.trim()
    setButtonText(this.#submit, this.#action.busy ? 'Saving…' : 'Create class')

    const pools = this.#pools.state.data ?? []
    const poolName = (poolId: string): string => pools.find((pool) => pool.poolId === poolId)?.name ?? poolId
    const classes = this.#classes.state.data?.workerClasses ?? []
    show(this.#tableWrap, classes.length > 0)
    show(this.#none, classes.length === 0)
    syncKeyed(
      this.#tbody,
      classes,
      (workerClass) => workerClass.name,
      (workerClass) => {
        const remove = button('Delete', {
          small: true,
          variant: 'danger',
          ariaLabel: `Delete worker class ${workerClass.name}`,
          onClick: async () => {
            const confirmed = await confirmAction(
              `Delete worker class "${workerClass.name}"? Jobs that name it will fail to route.`,
              { okLabel: 'Delete' },
            )
            if (!confirmed) return
            const organization = this.#orgName()
            await this.#action.run('The class could not be deleted.', () => api.deleteWorkerClass({ organization, name: workerClass.name }))
            if (this.isActive()) void this.#classes.reload()
          },
        })
        return h(
          'tr',
          null,
          h('td', { class: 'mono class-name' }),
          h(
            'td',
            null,
            h('span', { class: 'status status-ok class-protected' }, h('span', { class: 'status-glyph', 'aria-hidden': 'true' }, '⚿'), 'Protected'),
            h('span', { class: 'meta class-unprotected' }, 'No'),
          ),
          h('td', { class: 'meta class-pools' }),
          h('td', null, remove),
        )
      },
      (row, workerClass) => {
        setText(row.querySelector('.class-name')!, workerClass.name)
        show(row.querySelector<HTMLElement>('.class-protected')!, workerClass.protected)
        show(row.querySelector<HTMLElement>('.class-unprotected')!, !workerClass.protected)
        setText(row.querySelector('.class-pools')!, workerClass.poolIds.map(poolName).join(', ') || 'no pools assigned')
        ;(row.querySelector('push-button') as PushButton).disabled = this.#action.busy
      },
    )
  }
}

export const tag = 'rc-worker-classes-page'
define(tag, WorkerClassesPage)
