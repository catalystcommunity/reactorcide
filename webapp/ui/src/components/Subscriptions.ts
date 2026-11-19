import type { ComboBox, PushButton, TextField } from '@catalystcommunity/controls'
import { api } from '~/api/client.ts'
import type { EventDeliverySummary, EventSubscriptionSummary } from '~/api/csilapi/types.gen.ts'
import { field, type Field } from '~/components/Field.ts'
import { Action, ActionError, oneTimeSecret } from '~/components/ManagementPage.ts'
import { button, clearField, comboBox, confirmAction, setButtonText, setComboValue, setOptions, textField } from '~/ui/controls.ts'
import { h, setText, show, syncKeyed } from '~/ui/dom.ts'
import { RcElement, define } from '~/ui/element.ts'
import { Loader } from '~/ui/load.ts'

/**
 * Outbound event subscriptions (docs/outbound-events.md), shared by the org
 * page and the project page.
 *
 * A subscription sends phase events (a node starts, a node finishes, a
 * workflow completes, an event is evaluated) to a webhook. Each request is
 * signed with the signing secret of the subscription; the secret is shown one
 * time, when it is made. The delivery log shows each delivery and its
 * attempts, and a delivered or dead-lettered delivery can be sent again.
 *
 * An org admin manages org and owner_group subscriptions; a project owner
 * manages project and workflow subscriptions. The coordinator decides each
 * call (uiapi/ui_subscriptions.go).
 */

export const SCOPES = [
  { value: 'org', label: 'Organization', description: 'Every event of the organization' },
  { value: 'project', label: 'Project', description: 'The events of one project' },
  { value: 'workflow', label: 'Workflow', description: 'The events of one workflow security ID in a project' },
  { value: 'owner_group', label: 'Owner group', description: 'The events of workflows that an owner group owns' },
]

/** The scopes a project page offers. */
export const PROJECT_SCOPES = SCOPES.filter((scope) => scope.value === 'project' || scope.value === 'workflow')

export function splitList(value: string): string[] {
  return value
    .split(',')
    .map((part) => part.trim())
    .filter(Boolean)
}

function listOrAll(values: readonly string[]): string {
  return values.length ? values.join(', ') : 'all'
}

/** What the manager lists and where it creates. */
export interface SubscriptionScope {
  orgId: string
  /** Set on the project page: the list shows the project, and the form creates for it. */
  projectId?: string
}

/**
 * The create form and the list of subscriptions of one scope. Set the scope
 * with `setScope`; the element loads when it has an org.
 */
export class SubscriptionsManager extends RcElement {
  #scopeKey: SubscriptionScope | undefined
  #subscriptions = new Loader(this, (key: SubscriptionScope) =>
    api.listEventSubscriptions(key.projectId ? { orgId: key.orgId, projectId: key.projectId } : { orgId: key.orgId }),
  )
  #expanded: string | undefined
  #create = new Action(() => this.invalidate())
  #secret: string | undefined

  #createError!: ActionError
  #name!: TextField
  #scope!: ComboBox
  #project!: TextField
  #projectField!: Field
  #workflow!: TextField
  #ownerGroup!: TextField
  #ownerGroupField!: Field
  #url!: TextField
  #eventTypes!: TextField
  #results!: TextField
  #refClasses!: TextField
  #actorKinds!: TextField
  #failureClasses!: TextField
  #submit!: PushButton
  #secretBox!: HTMLElement
  #tableWrap!: HTMLElement
  #none!: HTMLElement
  #tbody!: HTMLTableSectionElement

  setScope(scope: SubscriptionScope): void {
    const next = scope.orgId ? { ...scope } : undefined
    if (JSON.stringify(next) === JSON.stringify(this.#scopeKey)) return
    this.#scopeKey = next
    if (this.isConnected) this.invalidate()
  }

  #fixedProject(): string {
    return this.#scopeKey?.projectId ?? ''
  }

  protected build(): void {
    this.#tbody = h('tbody')
    this.#tableWrap = h(
      'div',
      { class: 'scroll-x' },
      h(
        'table',
        { class: 'table subscriptions-table' },
        h('thead', null, h('tr', null, h('th', null, 'Subscription'), h('th', null, 'Scope'), h('th', null, 'Events'), h('th', null, 'Status'))),
        this.#tbody,
      ),
    )
    this.#none = h('div', { class: 'empty' }, 'No subscriptions yet.')
    this.#secretBox = h('div')
    this.append(this.#buildCreateCard(), h('div', { class: 'card' }, h('h3', { class: 'section-title' }, 'Subscriptions'), this.#tableWrap, this.#none))
  }

  #buildCreateCard(): HTMLElement {
    this.#createError = new ActionError()
    const changed = () => this.invalidate()
    this.#name = textField({ mono: true, onInput: changed })
    this.#scope = comboBox({ ariaLabel: 'Scope', options: SCOPES, value: 'org', onChange: changed })
    this.#project = textField({ mono: true, placeholder: 'project ID', onInput: changed })
    this.#workflow = textField({ mono: true, placeholder: 'workflow security ID', onInput: changed })
    this.#ownerGroup = textField({ mono: true, onInput: changed })
    this.#url = textField({ mono: true, type: 'url', placeholder: 'https://hooks.example.com/reactorcide', onInput: changed })
    this.#eventTypes = textField({ mono: true, placeholder: 'node.post_complete, workflow.completed' })
    this.#results = textField({ mono: true, placeholder: 'failed, timeout' })
    this.#refClasses = textField({ mono: true, placeholder: 'default_branch' })
    this.#actorKinds = textField({ mono: true, placeholder: 'human, service' })
    this.#failureClasses = textField({ mono: true, placeholder: 'job, oom, timeout' })
    this.#submit = button('Create subscription', { variant: 'primary', type: 'submit' })
    this.#projectField = field({ label: 'Project', hint: 'Project and workflow scopes.', control: this.#project })
    this.#ownerGroupField = field({ label: 'Owner group', hint: 'Owner group scope.', control: this.#ownerGroup })
    return h(
      'div',
      { class: 'card' },
      h('h3', { class: 'section-title' }, 'New subscription'),
      this.#secretBox,
      h(
        'form',
        { class: 'subscription-form', onSubmit: (event: Event) => void this.#onCreate(event) },
        this.#createError.el,
        h(
          'div',
          { class: 'form-row' },
          field({ label: 'Name', required: true, hint: 'Lowercase letters, digits and dashes.', control: this.#name }).el,
          field({ label: 'Scope', required: true, control: this.#scope, labelTarget: this.#scope }).el,
          field({ label: 'Webhook URL', required: true, hint: 'HTTPS. Private and loopback addresses are refused.', control: this.#url }).el,
        ),
        h(
          'div',
          { class: 'form-row' },
          this.#projectField.el,
          field({ label: 'Workflow', hint: 'Workflow scope.', control: this.#workflow }).el,
          this.#ownerGroupField.el,
        ),
        h(
          'div',
          { class: 'form-row' },
          field({ label: 'Event types', hint: 'Comma-separated. Empty: every type.', control: this.#eventTypes }).el,
          field({ label: 'Results', hint: 'Only events with one of these results.', control: this.#results }).el,
          field({ label: 'Ref classes', hint: 'Only events with one of these ref classes.', control: this.#refClasses }).el,
          field({ label: 'Actor kinds', hint: 'human, service, system.', control: this.#actorKinds }).el,
          field({ label: 'Failure classes', hint: 'Only node events with one of these failure classes. A diagnosis tool uses job, oom, timeout.', control: this.#failureClasses }).el,
        ),
        this.#submit,
      ),
    )
  }

  #projectValue(): string {
    return this.#fixedProject() || this.#project.value.trim()
  }

  #canSubmit(): boolean {
    const scope = this.#scope.value
    if (!this.#scopeKey) return false
    if (!this.#name.value.trim() || !this.#url.value.trim()) return false
    if ((scope === 'project' || scope === 'workflow') && !this.#projectValue()) return false
    if (scope === 'workflow' && !this.#workflow.value.trim()) return false
    if (scope === 'owner_group' && !this.#ownerGroup.value.trim()) return false
    return true
  }

  async #onCreate(event: Event): Promise<void> {
    event.preventDefault()
    if (!this.#canSubmit()) return
    const scope = this.#scope.value
    const project = this.#projectValue()
    let secret = ''
    const ok = await this.#create.run('The subscription could not be created.', async () => {
      const response = await api.createEventSubscription({
        orgId: this.#scopeKey!.orgId,
        name: this.#name.value.trim(),
        scope,
        projectId: scope === 'project' || scope === 'workflow' || (scope === 'owner_group' && project) ? project : undefined,
        workflowSecurityId: scope === 'workflow' ? this.#workflow.value.trim() : undefined,
        ownerGroup: scope === 'owner_group' ? this.#ownerGroup.value.trim() : undefined,
        webhookUrl: this.#url.value.trim(),
        eventTypes: splitList(this.#eventTypes.value),
        results: splitList(this.#results.value),
        refClassesAny: splitList(this.#refClasses.value),
        actorKinds: splitList(this.#actorKinds.value),
        failureClasses: splitList(this.#failureClasses.value),
      })
      secret = response.signingSecret
    })
    if (ok && this.isActive()) {
      this.#secret = secret
      for (const control of [this.#name, this.#url, this.#project, this.#workflow, this.#ownerGroup, this.#eventTypes, this.#results, this.#refClasses, this.#actorKinds, this.#failureClasses]) {
        clearField(control)
      }
      void this.#subscriptions.reload()
      this.invalidate()
    }
  }

  reloadList(): void {
    void this.#subscriptions.reload()
  }

  render(): void {
    this.#subscriptions.sync(this.#scopeKey)
    const fixed = Boolean(this.#fixedProject())
    // On a project page only project and workflow scopes apply, and the
    // project is the page's own.
    setOptions(this.#scope, fixed ? PROJECT_SCOPES : SCOPES)
    if (fixed && this.#scope.value !== 'project' && this.#scope.value !== 'workflow') setComboValue(this.#scope, 'project')
    show(this.#projectField.el, !fixed)
    show(this.#ownerGroupField.el, !fixed)
    this.#createError.set(this.#create.error ?? this.#subscriptions.state.error?.message)
    this.#submit.disabled = this.#create.busy || !this.#canSubmit()
    setButtonText(this.#submit, this.#create.busy ? 'Creating…' : 'Create subscription')
    if (this.#secret && this.#secretBox.childElementCount === 0) {
      this.#secretBox.append(
        oneTimeSecret('Signing secret', this.#secret, () => {
          this.#secret = undefined
          this.#secretBox.replaceChildren()
        }),
      )
    }

    const subs = this.#subscriptions.state.data?.subscriptions ?? []
    show(this.#tableWrap, subs.length > 0)
    show(this.#none, this.#subscriptions.state.loaded && subs.length === 0)
    type Entry = { kind: 'row' | 'detail'; sub: EventSubscriptionSummary }
    const entries: Entry[] = []
    for (const sub of subs) {
      entries.push({ kind: 'row', sub })
      if (this.#expanded === sub.subscriptionId) entries.push({ kind: 'detail', sub })
    }
    syncKeyed(
      this.#tbody,
      entries,
      (entry) => `${entry.kind}:${entry.sub.subscriptionId}`,
      (entry) => (entry.kind === 'row' ? this.#createRow(entry.sub) : this.#createDetailRow(entry.sub)),
      (row, entry) => {
        if (entry.kind === 'row') this.#updateRow(row as HTMLTableRowElement, entry.sub)
      },
    )
  }

  #createRow(sub: EventSubscriptionSummary): HTMLTableRowElement {
    const toggle = button('', {
      small: true,
      variant: 'flat',
      onClick: () => {
        this.#expanded = this.#expanded === sub.subscriptionId ? undefined : sub.subscriptionId
        this.invalidate()
      },
    })
    toggle.classList.add('subscription-toggle')
    return h(
      'tr',
      null,
      h('td', null, toggle, h('div', { class: 'meta mono truncate subscription-url' })),
      h('td', { class: 'subscription-scope' }),
      h('td', { class: 'subscription-events mono' }),
      h('td', { class: 'subscription-status' }),
    )
  }

  #updateRow(row: HTMLTableRowElement, sub: EventSubscriptionSummary): void {
    const expanded = this.#expanded === sub.subscriptionId
    const toggle = row.querySelector('.subscription-toggle') as PushButton
    setButtonText(toggle, `${expanded ? '▾' : '▸'} ${sub.name}`)
    toggle.setAttribute('aria-label', `${expanded ? 'Hide' : 'Show'} details of ${sub.name}`)
    setText(row.querySelector('.subscription-url')!, sub.webhookUrl)
    const scope = sub.scope === 'workflow' ? `workflow ${sub.workflowSecurityId}` : sub.scope === 'owner_group' ? `owner group ${sub.ownerGroup}` : sub.scope
    setText(row.querySelector('.subscription-scope')!, scope)
    setText(row.querySelector('.subscription-events')!, listOrAll(sub.eventTypes))
    setText(row.querySelector('.subscription-status')!, !sub.enabled ? 'disabled' : sub.ownerActive ? 'enabled' : 'owner disabled')
  }

  #createDetailRow(sub: EventSubscriptionSummary): HTMLTableRowElement {
    const panel = document.createElement('rc-subscription-detail') as SubscriptionPanel
    panel.subscriptionId = sub.subscriptionId
    panel.onChanged = () => this.reloadList()
    return h('tr', null, h('td', { colspan: '4' }, panel))
  }
}
define('rc-subscriptions-manager', SubscriptionsManager)

export function subscriptionsManager(scope?: SubscriptionScope): SubscriptionsManager {
  const el = document.createElement('rc-subscriptions-manager') as SubscriptionsManager
  if (scope) el.setScope(scope)
  return el
}

/**
 * One subscription: its filters, enable and disable, a test send, delete,
 * and the delivery log with replay.
 */
export class SubscriptionPanel extends RcElement {
  subscriptionId = ''
  onChanged: () => void = () => {}

  #detail = new Loader(this, (id: string) => api.getEventSubscription({ subscriptionId: id }))
  #deliveries = new Loader(this, (id: string) => api.listEventDeliveries({ subscriptionId: id, limit: 50 }))
  #action = new Action(() => this.invalidate())
  #testResult = ''

  #error!: ActionError
  #filters!: HTMLElement
  #toggle!: PushButton
  #test!: PushButton
  #replayAll!: PushButton
  #remove!: PushButton
  #testLine!: HTMLElement
  #log!: HTMLTableSectionElement
  #noDeliveries!: HTMLElement

  protected build(): void {
    this.classList.add('stack')
    this.#error = new ActionError()
    this.#filters = h('dl', { class: 'subscription-filters' })
    this.#toggle = button('', { small: true, onClick: () => void this.#setEnabled() })
    this.#test = button('Send test event', { small: true, onClick: () => void this.#sendTest() })
    this.#replayAll = button('Replay dead letters', { small: true, onClick: () => void this.#replayDeadLetters() })
    this.#remove = button('Delete', { small: true, variant: 'danger', onClick: () => void this.#delete() })
    this.#testLine = h('p', { class: 'meta subscription-test-result', hidden: true })
    this.#log = h('tbody')
    this.#noDeliveries = h('div', { class: 'empty' }, 'No deliveries yet.')
    this.append(
      this.#error.el,
      this.#filters,
      h('div', { class: 'row' }, this.#toggle, this.#test, this.#replayAll, this.#remove),
      this.#testLine,
      h('h4', null, 'Delivery log'),
      h(
        'div',
        { class: 'scroll-x' },
        h(
          'table',
          { class: 'table delivery-log' },
          h(
            'thead',
            null,
            h('tr', null, h('th', null, 'Event'), h('th', null, 'Order'), h('th', null, 'Status'), h('th', null, 'Attempts'), h('th', null, 'Last result'), h('th', null, '')),
          ),
          this.#log,
        ),
      ),
      this.#noDeliveries,
    )
  }

  async #run(message: string, work: () => Promise<unknown>): Promise<boolean> {
    const ok = await this.#action.run(message, work)
    if (ok && this.isActive()) {
      void this.#detail.reload()
      void this.#deliveries.reload()
      this.onChanged()
    }
    return ok
  }

  async #setEnabled(): Promise<void> {
    const sub = this.#detail.state.data?.subscription
    if (!sub) return
    if (sub.enabled) {
      const confirmed = await confirmAction(`Disable ${sub.name}? New events are not sent, and open deliveries go to dead letter.`, { okLabel: 'Disable', danger: true })
      if (!confirmed) return
    }
    await this.#run('The subscription could not be changed.', () => api.updateEventSubscription({ subscriptionId: this.subscriptionId, enabled: !sub.enabled }))
  }

  async #sendTest(): Promise<void> {
    let line = ''
    await this.#action.run('The test event could not be sent.', async () => {
      const r = await api.testEventSubscription({ subscriptionId: this.subscriptionId })
      line = r.delivered ? `Delivered: HTTP ${r.httpStatus ?? ''} in ${r.latencyMs} ms.` : `Not delivered: ${r.error || `HTTP ${r.httpStatus ?? ''}`}`
    })
    if (this.isActive() && line) {
      this.#testResult = line
      this.invalidate()
    }
  }

  async #replayDeadLetters(): Promise<void> {
    await this.#run('The deliveries could not be replayed.', () => api.replayEventDelivery({ subscriptionId: this.subscriptionId, allDeadLetters: true }))
  }

  async #replay(deliveryId: string): Promise<void> {
    await this.#run('The delivery could not be replayed.', () => api.replayEventDelivery({ subscriptionId: this.subscriptionId, deliveryId }))
  }

  async #delete(): Promise<void> {
    const sub = this.#detail.state.data?.subscription
    if (!sub) return
    const confirmed = await confirmAction(`Delete ${sub.name} and its delivery log?`, { okLabel: 'Delete', danger: true })
    if (!confirmed) return
    await this.#run('The subscription could not be deleted.', () => api.deleteEventSubscription({ subscriptionId: this.subscriptionId }))
  }

  render(): void {
    this.#detail.sync(this.subscriptionId || undefined)
    this.#deliveries.sync(this.subscriptionId || undefined)
    this.#error.set(this.#action.error)
    const sub = this.#detail.state.data?.subscription
    const busy = this.#action.busy
    for (const control of [this.#toggle, this.#test, this.#replayAll, this.#remove]) control.disabled = busy || !sub
    setButtonText(this.#toggle, sub?.enabled ? 'Disable' : 'Enable')
    show(this.#testLine, Boolean(this.#testResult))
    setText(this.#testLine, this.#testResult)

    const rows: [string, string][] = sub
      ? [
          ['Owner', `${sub.ownerPrincipalId}${sub.ownerActive ? '' : ' (disabled)'}`],
          ['Event types', listOrAll(sub.eventTypes)],
          ['Results', listOrAll(sub.results)],
          ['Ref classes', listOrAll(sub.refClassesAny)],
          ['Actor kinds', listOrAll(sub.actorKinds)],
          ['Failure classes', listOrAll(sub.failureClasses ?? [])],
          ['Max attempts', sub.maxAttempts ? String(sub.maxAttempts) : 'coordinator default'],
        ]
      : []
    syncKeyed(
      this.#filters,
      rows,
      (row) => row[0],
      (row) => h('div', { class: 'row' }, h('dt', null, row[0]), h('dd', { class: 'mono' })),
      (el, row) => setText(el.querySelector('dd')!, row[1]),
    )

    const deliveries = this.#deliveries.state.data?.deliveries ?? []
    show(this.#noDeliveries, this.#deliveries.state.loaded && deliveries.length === 0)
    syncKeyed(
      this.#log,
      deliveries,
      (d) => d.deliveryId,
      (d) => {
        const replay = button('Replay', { small: true, ariaLabel: `Replay ${d.eventType} ${d.eventId}`, onClick: () => void this.#replay(d.deliveryId) })
        replay.classList.add('delivery-replay')
        return h(
          'tr',
          null,
          h('td', null, h('div', null, d.eventType), h('div', { class: 'meta mono truncate' }, d.eventId)),
          h('td', { class: 'meta mono' }, `${d.orderKey} #${d.sequence}`),
          h('td', { class: 'delivery-status' }),
          h('td', { class: 'delivery-attempts' }),
          h('td', { class: 'meta delivery-last' }),
          h('td', null, replay),
        )
      },
      (row, d: EventDeliverySummary) => {
        setText(row.querySelector('.delivery-status')!, d.status === 'dead_letter' ? 'dead letter' : d.status)
        setText(row.querySelector('.delivery-attempts')!, String(d.attempts))
        const last = [d.lastHttpStatus ? `HTTP ${d.lastHttpStatus}` : '', d.lastLatencyMs !== undefined ? `${d.lastLatencyMs} ms` : '', d.lastError].filter(Boolean).join(' · ')
        setText(row.querySelector('.delivery-last')!, last || '—')
        const replay = row.querySelector('.delivery-replay') as PushButton
        show(replay, d.status === 'delivered' || d.status === 'dead_letter')
        replay.disabled = busy
      },
    )
  }
}
define('rc-subscription-detail', SubscriptionPanel)

