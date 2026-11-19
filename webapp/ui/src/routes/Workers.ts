import type { CheckBox, PushButton, TextField } from '@catalystcommunity/controls'
import { api } from '~/api/client.ts'
import type {
  EnrollmentTokenSummary,
  ListEnrollmentTokensResponse,
  ListPoolsResponse,
  ListWorkersResponse,
  WorkerPoolSummary,
  WorkerSummary,
} from '~/api/csilapi/types.gen.ts'
import { RcPage } from '~/app/page.ts'
import { field } from '~/components/Field.ts'
import { Action, ActionError, describeError, notPermitted, oneTimeSecret } from '~/components/ManagementPage.ts'
import { formatMetric } from '~/components/ResourceHistory.ts'
import { relativeTime } from '~/components/States.ts'
import { imageStatusClass, imageStatusText, imageVersionText } from '~/components/imageText.ts'
import { SESSION, session } from '~/lib/session.ts'
import { button, checkBox, clearField, confirmAction, setButtonText, textField } from '~/ui/controls.ts'
import { h, setAttr, setText, show, syncKeyed } from '~/ui/dom.ts'
import { RcElement, define } from '~/ui/element.ts'
import { Loader } from '~/ui/load.ts'
import { ROUTE, href } from '~/ui/router.ts'

/**
 * Worker pools, their enrollment tokens, and the workers that enrolled.
 *
 * Gated on the ManageWorkers capability. The gate decides what is DRAWN; the
 * coordinator decides what is allowed, and re-checks every operation here.
 */
export class WorkersPage extends RcPage {
  static watches = [ROUTE, SESSION]

  #refused!: HTMLElement
  #body!: HTMLElement
  #queuesLink!: HTMLAnchorElement

  #pools = new Loader<string, ListPoolsResponse>(this, () => api.listPools({}))
  #workers = new Loader<string, ListWorkersResponse>(this, () => api.listWorkers({}))
  #selectedPool: string | undefined
  // Worker filters. They apply to the list on the page: the full list also
  // gives the count for the warning above the table.
  #onlyDeactivated = false
  #tokenFilter: { id: string; label: string } | undefined
  #deactivatedFilter!: CheckBox
  #tokenFilterChip!: HTMLElement
  #tokenFilterText!: HTMLElement
  #deactivatedWarning!: HTMLElement
  #workersFilteredEmpty!: HTMLElement

  #createAction = new Action(() => this.invalidate())
  #createError = new ActionError()
  #name!: TextField
  #description!: TextField
  #create!: PushButton

  #poolTable!: HTMLTableElement
  #poolHead!: HTMLTableSectionElement
  #poolWrap!: HTMLElement
  #poolsEmpty!: HTMLElement
  #poolViews = new Map<string, PoolSection>()

  #workerRows!: HTMLTableSectionElement
  #workerWrap!: HTMLElement
  #workersEmpty!: HTMLElement
  #workersError!: ActionError
  #workerViews = new WeakMap<Element, WorkerRow>()

  protected build(): void {
    this.#refused = notPermitted('workers')
    this.#queuesLink = h('a', { href: href('/workers/queues'), class: 'btn btn-sm' }, 'Queues')

    this.#name = textField({ onInput: () => this.invalidate() })
    this.#description = textField()
    this.#create = button('Create pool', { variant: 'primary', type: 'submit' })
    const form = h(
      'form',
      null,
      this.#createError.el,
      h(
        'div',
        { class: 'form-row' },
        field({ label: 'Name', required: true, hint: 'How this pool is identified when a worker enrols.', control: this.#name }).el,
        field({ label: 'Description', control: this.#description }).el,
      ),
      this.#create,
    )
    form.addEventListener('submit', (event) => {
      event.preventDefault()
      void this.#createPool()
    })

    this.#poolHead = h('thead', null, h('tr', null, h('th', null, 'Pool'), h('th', null, 'Description'), h('th', null, 'Created'), h('th')))
    this.#poolTable = h('table', { class: 'table' }, this.#poolHead)
    this.#poolWrap = h('div', { class: 'scroll-x' }, this.#poolTable)
    this.#poolsEmpty = h('div', { class: 'empty' }, 'No pools yet. Create one above, then enrol a worker against it.')

    this.#workerRows = h('tbody')
    this.#workerWrap = h(
      'div',
      { class: 'scroll-x' },
      h(
        'table',
        { class: 'table' },
        h(
          'thead',
          null,
          h(
            'tr',
            null,
            h('th', null, 'Worker'),
            h('th', null, 'Status'),
            h('th', null, 'Images'),
            h('th', null, 'Platform'),
            h('th', null, 'Location'),
            h('th', null, 'Enrollment token'),
            h('th', null, 'Active leases'),
            h('th', null, 'Headroom'),
            h('th', null, 'Last seen'),
            h('th'),
          ),
        ),
        this.#workerRows,
      ),
    )
    this.#workersEmpty = h('div', { class: 'empty' }, 'No workers have enrolled yet.')
    this.#workersError = new ActionError()
    this.#workersFilteredEmpty = h('div', { class: 'empty' }, 'No workers match the filter.')
    this.#deactivatedWarning = h('div', { class: 'alert alert-error', role: 'alert', hidden: true })
    this.#deactivatedFilter = checkBox('Only workers on a deactivated token', {
      onChange: (checked) => {
        this.#onlyDeactivated = checked
        this.invalidate()
      },
    })
    this.#tokenFilterText = h('span')
    this.#tokenFilterChip = h(
      'span',
      { class: 'row', hidden: true },
      this.#tokenFilterText,
      button('Clear', {
        small: true,
        variant: 'flat',
        onClick: () => {
          this.#tokenFilter = undefined
          this.invalidate()
        },
      }),
    )
    this.addEventListener('rc-filter-token', (event) => {
      this.#tokenFilter = (event as CustomEvent<{ id: string; label: string }>).detail
      this.invalidate()
    })

    this.#body = h(
      'div',
      { class: 'page' },
      h(
        'div',
        { class: 'page-header' },
        h('div', null, h('h1', null, 'Workers'), h('p', { class: 'meta' }, 'Pools, their enrollment tokens, and the workers that joined them.')),
        h('div', { class: 'row' }, h('a', { href: href('/workers/classes'), class: 'btn btn-sm' }, 'Worker classes'), this.#queuesLink),
      ),
      h('div', { class: 'card' }, h('h3', { class: 'section-title' }, 'New pool'), form),
      h('div', { class: 'card' }, h('h3', { class: 'section-title' }, 'Pools'), this.#poolWrap, this.#poolsEmpty),
      h(
        'div',
        { class: 'card' },
        h('h3', { class: 'section-title' }, 'Enrolled workers'),
        this.#workersError.el,
        this.#deactivatedWarning,
        h('div', { class: 'row' }, this.#deactivatedFilter, this.#tokenFilterChip),
        this.#workerWrap,
        this.#workersEmpty,
        this.#workersFilteredEmpty,
      ),
    )
    this.append(this.#refused, this.#body)
  }

  async #createPool(): Promise<void> {
    const name = this.#name.value.trim()
    if (!name) return
    const ok = await this.#createAction.run('The pool could not be created.', () =>
      api.createPool({ name, description: this.#description.value.trim() || undefined }),
    )
    if (ok) {
      clearField(this.#name)
      clearField(this.#description)
      void this.#pools.reload()
    }
  }

  render(): void {
    const current = session()
    const allowed = Boolean(current?.capabilities?.manageWorkers)
    show(this.#refused, !allowed)
    show(this.#body, allowed)
    if (!allowed) return
    show(this.#queuesLink, current?.is_global_admin)

    this.#pools.sync('all')
    this.#workers.sync('all')

    this.#createError.set(this.#createAction.error)
    this.#create.disabled = this.#createAction.busy || !this.#name.value.trim()

    this.#renderPools(this.#pools.state.data?.pools ?? [])

    const all = this.#workers.state.data?.workers ?? []
    const onDeactivated = all.filter((w) => w.onDeactivatedToken).length
    show(this.#deactivatedWarning, onDeactivated > 0)
    setText(
      this.#deactivatedWarning,
      `${onDeactivated} ${onDeactivated === 1 ? 'worker runs' : 'workers run'} on a deactivated enrollment token. ` +
        'They work until they stop, but then they cannot register again. Give those hosts an active token.',
    )
    show(this.#tokenFilterChip, Boolean(this.#tokenFilter))
    setText(this.#tokenFilterText, this.#tokenFilter ? `Token: ${this.#tokenFilter.label}` : '')
    const workers = filterWorkers(all, this.#onlyDeactivated, this.#tokenFilter?.id)
    show(this.#workerWrap, workers.length > 0)
    // A failed load is not "no workers": say that it failed.
    const workersError = this.#workers.state.error
    this.#workersError.set(workersError ? describeError(workersError, 'The workers could not be loaded.') : undefined)
    show(this.#workersEmpty, this.#workers.state.loaded && all.length === 0)
    show(this.#workersFilteredEmpty, all.length > 0 && workers.length === 0)
    syncKeyed(
      this.#workerRows,
      workers,
      (worker) => worker.workerId,
      () => {
        const view = new WorkerRow(() => this.invalidate(), () => void this.#workers.reload())
        this.#workerViews.set(view.el, view)
        return view.el
      },
      (el, worker) => this.#workerViews.get(el)!.update(worker),
    )
  }

  /**
   * One <tbody> per pool, holding its row and, when expanded, its tokens. A
   * table may have many bodies, and a body keeps the pair together, so a pool
   * that stays keeps its elements -- and an open token panel keeps its state.
   */
  #renderPools(pools: WorkerPoolSummary[]): void {
    show(this.#poolWrap, pools.length > 0)
    show(this.#poolsEmpty, pools.length === 0)
    const seen = new Set<string>()
    let cursor: ChildNode | null = this.#poolHead.nextSibling
    for (const pool of pools) {
      seen.add(pool.poolId)
      let view = this.#poolViews.get(pool.poolId)
      if (!view) {
        view = new PoolSection(
          () => this.invalidate(),
          (poolId) => {
            this.#selectedPool = this.#selectedPool === poolId ? undefined : poolId
            this.invalidate()
          },
          () => void this.#pools.reload(),
        )
        this.#poolViews.set(pool.poolId, view)
      }
      view.update(pool, this.#selectedPool === pool.poolId)
      if (cursor === view.el) cursor = view.el.nextSibling
      else this.#poolTable.insertBefore(view.el, cursor)
    }
    for (const [poolId, view] of this.#poolViews) {
      if (seen.has(poolId)) continue
      view.el.remove()
      this.#poolViews.delete(poolId)
    }
  }
}

class PoolSection {
  readonly el: HTMLTableSectionElement
  #pool!: WorkerPoolSummary
  readonly #toggle: PushButton
  readonly #description: HTMLTableCellElement
  readonly #created: HTMLTableCellElement
  readonly #delete: PushButton
  readonly #deleteAction: Action
  readonly #deleteError = new ActionError()
  readonly #detail: HTMLTableRowElement
  readonly #detailCell: HTMLTableCellElement
  #tokens: EnrollmentTokens | null = null

  constructor(invalidate: () => void, onToggle: (poolId: string) => void, onDeleted: () => void) {
    this.#deleteAction = new Action(invalidate)
    this.#toggle = button('', { small: true, variant: 'flat', onClick: () => onToggle(this.#pool.poolId) })
    this.#description = h('td', { class: 'meta' })
    this.#created = h('td', { class: 'meta' })
    this.#delete = button('Delete', {
      small: true,
      variant: 'danger',
      onClick: async () => {
        const ok = await confirmAction(
          `Delete pool "${this.#pool.name}"? Workers enrolled through it will no longer be able to authenticate.`,
          { okLabel: 'Delete pool' },
        )
        if (!ok) return
        const done = await this.#deleteAction.run('The pool could not be deleted.', () => api.deletePool({ poolId: this.#pool.poolId }))
        if (done) onDeleted()
      },
    })
    this.#detailCell = h('td', { colspan: '4' })
    this.#detail = h('tr', null, this.#detailCell)
    this.el = h(
      'tbody',
      null,
      h('tr', null, h('td', null, this.#toggle), this.#description, this.#created, h('td', null, this.#delete, this.#deleteError.el)),
      this.#detail,
    )
  }

  update(pool: WorkerPoolSummary, expanded: boolean): void {
    this.#pool = pool
    setButtonText(this.#toggle, `${expanded ? '▾' : '▸'} ${pool.name}`)
    setAttr(this.#toggle, 'aria-expanded', String(expanded))
    setText(this.#description, pool.description || '—')
    setText(this.#created, relativeTime(pool.createdAt))
    this.#delete.disabled = this.#deleteAction.busy
    this.#deleteError.set(this.#deleteAction.error)

    // The token panel exists only while expanded, as before: collapsing it
    // releases its data, and expanding it again loads fresh.
    show(this.#detail, expanded)
    if (expanded && !this.#tokens) {
      this.#tokens = document.createElement('rc-enrollment-tokens') as EnrollmentTokens
      this.#tokens.poolId = pool.poolId
      this.#detailCell.append(this.#tokens)
    } else if (!expanded && this.#tokens) {
      this.#tokens.remove()
      this.#tokens = null
    }
  }
}

/**
 * A pool's enrollment tokens.
 *
 * create-enrollment-token is the ONLY operation that ever returns the token
 * value; the store keeps a hash and no later call can retrieve it. That is why
 * the result goes through oneTimeSecret rather than into a table cell, and why
 * it lives only in this element's memory until dismissed.
 */
export class EnrollmentTokens extends RcElement {
  poolId = ''

  #tokens = new Loader<string, ListEnrollmentTokensResponse>(this, (poolId) => api.listEnrollmentTokens({ poolId }))
  #action = new Action(() => this.invalidate())
  #error = new ActionError()
  #secretSlot!: HTMLElement
  #minted: string | undefined
  #shownSecret: string | undefined
  #name!: TextField
  #create!: PushButton
  #tableWrap!: HTMLElement
  #rows!: HTMLTableSectionElement
  #none!: HTMLElement
  #rowToken = new WeakMap<Element, EnrollmentTokenSummary>()

  protected build(): void {
    this.classList.add('stack')
    this.#secretSlot = h('div')
    this.#name = textField({ ariaLabel: 'Enrollment token name', placeholder: 'Token name (optional)' })
    this.#name.style.maxWidth = '18rem'
    this.#create = button('Create enrollment token', { small: true, variant: 'primary', onClick: () => void this.#mint() })
    this.#rows = h('tbody')
    this.#tableWrap = h(
      'table',
      { class: 'table' },
      h('thead', null, h('tr', null, h('th', null, 'Token'), h('th', null, 'Active'), h('th', null, 'Active workers'), h('th', null, 'Last used'), h('th'))),
      this.#rows,
    )
    this.#none = h('p', { class: 'meta' }, 'No enrollment tokens for this pool.')
    this.append(this.#error.el, this.#secretSlot, h('div', { class: 'row' }, this.#name, this.#create), this.#tableWrap, this.#none)
  }

  async #mint(): Promise<void> {
    const ok = await this.#action.run('The token could not be created.', async () => {
      const response = await api.createEnrollmentToken({ poolId: this.poolId, name: this.#name.value.trim() || undefined })
      this.#minted = response.token
      clearField(this.#name)
    })
    if (ok) void this.#tokens.reload()
  }

  render(): void {
    this.#tokens.sync(this.poolId)
    this.#error.set(this.#action.error)
    this.#create.disabled = this.#action.busy

    if (this.#minted !== this.#shownSecret) {
      this.#shownSecret = this.#minted
      this.#secretSlot.replaceChildren(
        ...(this.#minted
          ? [
              oneTimeSecret('Enrollment token', this.#minted, () => {
                this.#minted = undefined
                this.invalidate()
              }),
            ]
          : []),
      )
    }

    const tokens = this.#tokens.state.data?.tokens ?? []
    show(this.#tableWrap, tokens.length > 0)
    show(this.#none, this.#tokens.state.loaded && tokens.length === 0)
    syncKeyed(
      this.#rows,
      tokens,
      (token) => token.tokenId,
      () => {
        const deactivate = button('Deactivate', {
          small: true,
          variant: 'danger',
          onClick: async () => {
            const token = this.#rowToken.get(row)!
            const ok = await confirmAction(deactivateText(token), { title: 'Deactivate permanently?', okLabel: 'Deactivate permanently' })
            if (!ok) return
            await this.#action.run('The token could not be deactivated.', () =>
              api.deactivateEnrollmentToken({ tokenId: token.tokenId, confirmPermanent: true }),
            )
            void this.#tokens.reload()
          },
        })
        const workers = button('', {
          small: true,
          variant: 'flat',
          onClick: () => {
            const token = this.#rowToken.get(row)!
            this.dispatchEvent(new CustomEvent('rc-filter-token', { bubbles: true, detail: { id: token.tokenId, label: tokenLabel(token) } }))
          },
        })
        const row = h(
          'tr',
          null,
          h('td', null, h('div', { class: 'token-name' }), h('div', { class: 'meta mono truncate token-id' })),
          h('td', { class: 'token-active' }),
          h('td', { class: 'numeric' }, workers, h('span', { class: 'status status-warn token-warn', hidden: true }, 'still in use')),
          h('td', { class: 'meta token-used' }),
          h('td', null, deactivate),
        )
        return row
      },
      (row, token) => {
        this.#rowToken.set(row, token)
        const name = row.querySelector<HTMLElement>('.token-name')!
        setText(name, token.name || 'unnamed')
        name.classList.toggle('meta', !token.name)
        setText(row.querySelector('.token-id')!, token.tokenId)
        setText(row.querySelector('.token-active')!, token.isActive ? 'Yes' : 'No (permanent)')
        const count = token.activeWorkerCount ?? 0
        const [workers, deactivate] = row.querySelectorAll<PushButton>('push-button')
        setButtonText(workers!, String(count))
        workers!.disabled = count === 0
        show(row.querySelector<HTMLElement>('.token-warn')!, !token.isActive && count > 0)
        setText(row.querySelector('.token-used')!, token.lastUsedAt ? relativeTime(token.lastUsedAt) : 'never')
        show(deactivate!, token.isActive)
      },
    )
  }
}
define('rc-enrollment-tokens', EnrollmentTokens)

class WorkerRow {
  readonly el: HTMLTableRowElement
  #worker!: WorkerSummary
  readonly #action: Action
  readonly #error = new ActionError()
  readonly #drain: PushButton
  readonly #toggle: PushButton

  constructor(invalidate: () => void, onChanged: () => void) {
    this.#action = new Action(invalidate)
    // Drain lets a worker finish what it is running and take nothing new.
    // Disabling it is the harder stop, so the two are offered separately
    // rather than as one ambiguous "stop" button.
    this.#drain = button('Drain', {
      small: true,
      onClick: async () => {
        await this.#action.run('The worker could not be drained.', () => api.drainWorker({ workerId: this.#worker.workerId }))
        onChanged()
      },
    })
    this.#toggle = button('', {
      small: true,
      onClick: async () => {
        const next = this.#worker.status === 'disabled' ? 'active' : 'disabled'
        await this.#action.run('The worker status could not be changed.', () =>
          api.setWorkerStatus({ workerId: this.#worker.workerId, status: next }),
        )
        onChanged()
      },
    })
    this.el = h(
      'tr',
      null,
      h('td', null, h('a', { class: 'worker-name' }), h('div', { class: 'meta mono truncate worker-id' })),
      h('td', null, h('div', { class: 'worker-status' }), h('div', { class: 'meta worker-online' })),
      h(
        'td',
        null,
        h('span', { class: 'worker-image-status' }),
        h('div', { class: 'meta worker-image-detail', hidden: true }),
        h('div', { class: 'meta worker-image-version' }),
      ),
      h('td', { class: 'meta worker-platform' }),
      h('td', { class: 'meta worker-location' }),
      h(
        'td',
        null,
        h('div', { class: 'worker-token-name' }),
        h('div', { class: 'meta mono truncate worker-token-id' }),
        h('span', { class: 'status status-danger worker-token-warn', hidden: true }, 'deactivated token'),
      ),
      h('td', { class: 'numeric worker-leases' }),
      h('td', { class: 'meta worker-headroom' }),
      h('td', { class: 'meta worker-seen' }),
      h('td', null, h('div', { class: 'row' }, this.#drain, this.#toggle), this.#error.el),
    )
  }

  update(worker: WorkerSummary): void {
    this.#worker = worker
    const q = (cls: string): Element => this.el.querySelector(cls)!
    setText(q('.worker-name'), worker.hostname || worker.workerKey)
    setAttr(q('.worker-name'), 'href', href(`/workers/${encodeURIComponent(worker.workerId)}`))
    setText(q('.worker-id'), worker.workerId)
    setText(q('.worker-status'), worker.status)
    setText(q('.worker-online'), worker.online === undefined ? '' : worker.online ? 'online' : 'offline')
    const imageStatus = q('.worker-image-status')
    setText(imageStatus, imageStatusText(worker.imageStatus))
    setAttr(imageStatus, 'class', `worker-image-status ${imageStatusClass(worker.imageStatus)}`)
    const imageDetail = q('.worker-image-detail') as HTMLElement
    setText(imageDetail, worker.imageStatusDetail ?? '')
    show(imageDetail, worker.imageStatusDetail)
    setText(q('.worker-image-version'), worker.imageStatus ? imageVersionText(worker.imageSetVersion, worker.imageReportedVersion) : '')
    setText(q('.worker-platform'), `${worker.os}/${worker.arch}`)
    setText(q('.worker-location'), workerLocation(worker))
    const tokenName = q('.worker-token-name') as HTMLElement
    setText(tokenName, worker.enrollmentTokenId ? worker.enrollmentTokenName || 'unnamed' : '—')
    tokenName.classList.toggle('meta', !worker.enrollmentTokenName)
    setText(q('.worker-token-id'), worker.enrollmentTokenId ?? '')
    show(q('.worker-token-warn') as HTMLElement, Boolean(worker.onDeactivatedToken))
    setText(q('.worker-leases'), String(worker.activeLeaseCount))
    setText(q('.worker-headroom'), workerHeadroom(worker))
    setText(q('.worker-seen'), worker.lastSeenAt ? relativeTime(worker.lastSeenAt) : '—')
    setButtonText(this.#toggle, worker.status === 'disabled' ? 'Enable' : 'Disable')
    this.#drain.disabled = this.#action.busy
    this.#toggle.disabled = this.#action.busy
    this.#error.set(this.#action.error)
  }
}

/** The workers that pass the filters of the workers table. */
export function filterWorkers(workers: WorkerSummary[], onlyDeactivated: boolean, tokenId: string | undefined): WorkerSummary[] {
  return workers.filter((w) => (!onlyDeactivated || w.onDeactivatedToken) && (!tokenId || w.enrollmentTokenId === tokenId))
}

/** A token as its name and ID, for labels. The token value is never shown. */
export function tokenLabel(token: EnrollmentTokenSummary): string {
  return token.name ? `${token.name} (${token.tokenId})` : token.tokenId
}

/** The text of the deactivation dialog: permanent, and the workers still on the token. */
export function deactivateText(token: EnrollmentTokenSummary): string {
  const count = token.activeWorkerCount ?? 0
  const inUse =
    count > 0
      ? ` ${count} ${count === 1 ? 'worker runs' : 'workers run'} on this token now. ${count === 1 ? 'It keeps' : 'They keep'} working until ${count === 1 ? 'it stops' : 'they stop'}, then cannot register again.`
      : ' No worker with an active session uses this token.'
  return `Deactivate enrollment token ${tokenLabel(token)}? This is permanent: the token can never be activated again.${inUse}`
}

/** The standard location characteristics (cloud, region, zone) of a worker, or a dash. */
export function workerLocation(worker: WorkerSummary): string {
  const parts: string[] = []
  for (const key of ['cloud', 'region', 'zone']) {
    const entry = worker.characteristics.find((c) => c.key === key)
    if (entry) parts.push(Array.isArray(entry.value) ? entry.value.join(',') : String(entry.value))
  }
  return parts.join(' / ') || '—'
}

/**
 * The headroom of a worker in resources admission mode, as CPU and memory of
 * the capacity; "fixed" for a worker in the default concurrency mode.
 */
export function workerHeadroom(worker: WorkerSummary): string {
  const a = worker.admission
  if (!a) return 'fixed'
  const cpu = `${formatMetric(Math.max(0, a.headroomCpuMillicores), 'millicores')} of ${formatMetric(a.capacityCpuMillicores, 'millicores')}`
  const memory = `${formatMetric(Math.max(0, a.headroomMemoryBytes), 'bytes')} of ${formatMetric(a.capacityMemoryBytes, 'bytes')}`
  return `${cpu}, ${memory}`
}

export const tag = 'rc-workers-page'
define(tag, WorkersPage)
