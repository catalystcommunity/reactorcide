import type { CheckBox, PushButton, TextField } from '@catalystcommunity/controls'
import { api } from '~/api/client.ts'
import type {
  GetGlobalSettingsResponse,
  OrgSummary,
  ListTrustedDomainPatternsResponse,
  ListTrustedIdentitiesResponse,
  TrustedDomainPattern,
  TrustedIdentity,
} from '~/api/csilapi/types.gen.ts'
import { RcPage } from '~/app/page.ts'
import { ChoiceSelect } from '~/components/ChoiceInput.ts'
import { field } from '~/components/Field.ts'
import { Action, ActionError, notPermitted } from '~/components/ManagementPage.ts'
import { relativeTime } from '~/components/States.ts'
import { SESSION, session } from '~/lib/session.ts'
import { button, checkBox, clearField, confirmAction, describeControl, textField } from '~/ui/controls.ts'
import { h, setText, show, syncKeyed, uid } from '~/ui/dom.ts'
import { define } from '~/ui/element.ts'
import { Loader, type LoadHost } from '~/ui/load.ts'
import { ROUTE, href } from '~/ui/router.ts'
import { DUPLICATE_CHOICES, DYNAMIC_CHOICES } from './ProjectEvents.ts'

/**
 * Instance administration: global settings, and who may sign in.
 *
 * Global-admin only. Everything here affects the whole deployment rather than
 * one organization, which is why it is a separate page from the org management
 * screens and why each control says what it changes for everyone.
 */
export class AdminPage extends RcPage {
  static watches = [ROUTE, SESSION]

  #refused!: HTMLElement
  #body!: HTMLElement
  #cards: Card[] = []

  protected build(): void {
    this.#refused = notPermitted('this instance')
    this.#cards = [new GlobalSettingsCard(this), new OrgEventSettingsCard(this), new TrustedIdentitiesCard(this), new TrustedDomainsCard(this)]
    this.#body = h(
      'div',
      { class: 'page' },
      h(
        'div',
        { class: 'page-header' },
        h('div', null, h('h1', null, 'Administration'), h('p', { class: 'meta' }, 'Settings and access rules for the whole instance.')),
        h('div', { class: 'row' }, h('a', { href: href('/org/groups'), class: 'btn btn-sm' }, 'Groups'), h('a', { href: href('/org/roles'), class: 'btn btn-sm' }, 'Roles')),
      ),
      this.#cards.map((card) => card.el),
    )
    this.append(this.#refused, this.#body)
  }

  render(): void {
    const allowed = Boolean(session()?.is_global_admin)
    show(this.#refused, !allowed)
    show(this.#body, allowed)
    // Nothing is requested until the gate is open.
    if (allowed) for (const card of this.#cards) card.render()
  }
}

interface Card {
  readonly el: HTMLElement
  render(): void
}

class GlobalSettingsCard implements Card {
  readonly el: HTMLElement
  readonly #settings: Loader<string, GetGlobalSettingsResponse>
  readonly #action: Action
  readonly #error = new ActionError()
  readonly #loading: HTMLElement
  readonly #row: HTMLElement
  readonly #checkbox: CheckBox

  constructor(host: LoadHost) {
    this.#settings = new Loader(host, () => api.getGlobalSettings({}))
    this.#action = new Action(() => host.invalidate())
    const hintId = uid('hint')
    this.#checkbox = checkBox('New projects are private by default', {
      onChange: async (next) => {
        await this.#action.run('The setting could not be saved.', () => api.updateGlobalSettings({ newProjectsPrivate: next }))
        void this.#settings.reload()
      },
    })
    describeControl(this.#checkbox, [hintId])
    this.#loading = h('p', { class: 'meta' }, 'Loading…')
    this.#row = h(
      'div',
      { class: 'checkbox-row' },
      h(
        'div',
        null,
        this.#checkbox,
        h(
          'div',
          { class: 'field-hint', id: hintId },
          "Applies when a project is created without an explicit choice. A private project's jobs and logs are visible only to its members and administrators. Changing this does not alter projects that already exist.",
        ),
      ),
    )
    this.el = h('div', { class: 'card' }, h('h3', { class: 'section-title' }, 'Defaults for new projects'), this.#error.el, this.#loading, this.#row)
  }

  render(): void {
    this.#settings.sync('all')
    this.#error.set(this.#action.error)
    const settings = this.#settings.state.data
    show(this.#loading, !settings)
    show(this.#row, settings)
    this.#checkbox.disabled = this.#action.busy
    if (settings && !this.#action.busy && this.#checkbox.checked !== settings.newProjectsPrivate) {
      this.#checkbox.checked = settings.newProjectsPrivate
    }
  }
}

/**
 * Per-organization event defaults: repeated deliveries and dynamic jobs. A
 * project follows its organization unless it overrides the setting. A change
 * here saves at once; the list then reloads, so the value shown is the stored
 * one.
 */
class OrgEventSettingsCard implements Card {
  readonly el: HTMLElement
  readonly #orgs: Loader<string, OrgSummary[]>
  readonly #action: Action
  readonly #error = new ActionError()
  readonly #loading: HTMLElement
  readonly #table: HTMLElement
  readonly #body: HTMLTableSectionElement
  readonly #selects = new Map<string, { duplicate: ChoiceSelect; dynamic: ChoiceSelect }>()

  constructor(host: LoadHost) {
    this.#orgs = new Loader(host, async () => (await api.listOrgs({})).orgs)
    this.#action = new Action(() => host.invalidate())
    this.#loading = h('p', { class: 'meta' }, 'Loading…')
    this.#body = h('tbody')
    this.#table = h(
      'div',
      { class: 'scroll-x' },
      h(
        'table',
        { class: 'table' },
        h('thead', null, h('tr', null, h('th', null, 'Organization'), h('th', null, 'Repeated deliveries'), h('th', null, 'Dynamic jobs'))),
        this.#body,
      ),
    )
    this.el = h(
      'div',
      { class: 'card' },
      h('h3', { class: 'section-title' }, 'Organization event defaults'),
      h(
        'p',
        { class: 'meta' },
        'Repeated deliveries: what a new delivery for a change already seen does. Dynamic jobs: whether running jobs may start more jobs. A project can override each one.',
      ),
      this.#error.el,
      this.#loading,
      this.#table,
    )
  }

  async #save(org: OrgSummary, change: { duplicateEvents?: string; dynamicJobs?: string }): Promise<void> {
    await this.#action.run('The organization setting could not be saved.', () => api.updateOrg({ name: org.name, ...change }))
    void this.#orgs.reload()
  }

  render(): void {
    this.#orgs.sync('all')
    this.#error.set(this.#action.error ?? this.#orgs.state.error?.message)
    const orgs = this.#orgs.state.data
    show(this.#loading, !orgs && !this.#orgs.state.error)
    show(this.#table, orgs?.length)
    syncKeyed(
      this.#body,
      orgs ?? [],
      (o) => o.orgId,
      (o) => {
        const current = (): OrgSummary => this.#orgs.state.data?.find((x) => x.orgId === o.orgId) ?? o
        const duplicate = new ChoiceSelect({
          ariaLabel: `Repeated deliveries for ${o.displayName || o.name}`,
          choices: DUPLICATE_CHOICES,
          value: o.duplicateEvents || 'run',
          onChange: (value) => void this.#save(current(), { duplicateEvents: value }),
        })
        const dynamic = new ChoiceSelect({
          ariaLabel: `Dynamic jobs for ${o.displayName || o.name}`,
          choices: DYNAMIC_CHOICES,
          value: o.dynamicJobs || 'allow',
          onChange: (value) => void this.#save(current(), { dynamicJobs: value }),
        })
        this.#selects.set(o.orgId, { duplicate, dynamic })
        return h('tr', null, h('td', { class: 'org-name' }), h('td', null, duplicate.el), h('td', null, dynamic.el))
      },
      (row, o) => {
        setText(row.querySelector('.org-name')!, o.displayName || o.name)
        const selects = this.#selects.get(o.orgId)!
        selects.duplicate.el.disabled = this.#action.busy
        selects.dynamic.el.disabled = this.#action.busy
        if (!this.#action.busy) {
          selects.duplicate.update(DUPLICATE_CHOICES, o.duplicateEvents || 'run')
          selects.dynamic.update(DYNAMIC_CHOICES, o.dynamicJobs || 'allow')
        }
      },
    )
  }
}

/** A two-field add form, a list, and a Remove per row: the shape both trust lists share. */
abstract class TrustListCard<T> implements Card {
  readonly el: HTMLElement
  protected readonly action: Action
  protected readonly list: Loader<string, T[]>
  readonly #error = new ActionError()
  readonly #submit: PushButton
  readonly #tableWrap: HTMLElement
  readonly #rows: HTMLTableSectionElement
  protected readonly first: TextField
  protected readonly second: TextField

  constructor(
    host: LoadHost,
    options: {
      title: string
      intro: string
      headers: string[]
      submitLabel: string
      fields: [ReturnType<typeof field>, ReturnType<typeof field>]
      load: () => Promise<T[]>
    },
  ) {
    this.action = new Action(() => host.invalidate())
    this.list = new Loader(host, options.load)
    this.first = options.fields[0].control as TextField
    this.second = options.fields[1].control as TextField
    for (const control of [this.first, this.second]) control.addEventListener('input', () => host.invalidate())
    this.#submit = button(options.submitLabel, { variant: 'primary', type: 'submit' })
    const form = h('form', null, this.#error.el, h('div', { class: 'form-row' }, options.fields.map((f) => f.el)), this.#submit)
    form.addEventListener('submit', (event) => {
      event.preventDefault()
      void this.#add()
    })
    this.#rows = h('tbody')
    this.#tableWrap = h(
      'div',
      { class: 'scroll-x', style: { marginTop: 'var(--space-4)' } },
      h('table', { class: 'table' }, h('thead', null, h('tr', null, options.headers.map((label) => h('th', null, label)), h('th'))), this.#rows),
    )
    this.el = h(
      'div',
      { class: 'card' },
      h('h3', { class: 'section-title' }, options.title),
      h('p', { class: 'meta', style: { marginBottom: 'var(--space-4)' } }, options.intro),
      form,
      this.#tableWrap,
    )
  }

  /** True when the form may be submitted. */
  protected abstract ready(): boolean
  protected abstract add(): Promise<unknown>
  protected abstract keyOf(item: T): string
  protected abstract cells(item: T): string[]
  protected abstract cellClasses(): string[]
  protected abstract remove(item: T): Promise<void>

  async #add(): Promise<void> {
    if (!this.ready()) return
    const ok = await this.action.run(this.addFailure, () => this.add())
    if (ok) {
      clearField(this.first)
      clearField(this.second)
      void this.list.reload()
    }
  }

  protected abstract readonly addFailure: string

  render(): void {
    this.list.sync('all')
    this.#error.set(this.action.error)
    this.#submit.disabled = this.action.busy || !this.ready()
    const items = this.list.state.data ?? []
    show(this.#tableWrap, items.length > 0)
    const classes = this.cellClasses()
    syncKeyed(
      this.#rows,
      items,
      (item) => this.keyOf(item),
      (item) => {
        const remove = button('Remove', { small: true, variant: 'danger' })
        remove.addEventListener('click', () => void this.remove(rowItem.get(row) as T))
        const row = h('tr', null, classes.map((cls) => h('td', { class: cls })), h('td', null, remove))
        rowItem.set(row, item)
        return row
      },
      (row, item) => {
        rowItem.set(row, item)
        const tds = row.querySelectorAll('td')
        this.cells(item).forEach((text, i) => setText(tds[i], text))
        ;(row.querySelector('push-button') as PushButton).disabled = this.action.busy
      },
    )
  }
}

/** The item each row shows now, so a row's Remove acts on its current data. */
const rowItem = new WeakMap<Element, unknown>()

/**
 * Individually trusted identities.
 *
 * An identity listed here may sign in regardless of the domain patterns below.
 * Removing one does not end a session it already holds.
 */
class TrustedIdentitiesCard extends TrustListCard<TrustedIdentity> {
  protected readonly addFailure = 'The identity could not be added.'

  constructor(host: LoadHost) {
    super(host, {
      title: 'Trusted identities',
      intro: 'People allowed to sign in by name, whatever the domain rules say.',
      headers: ['Identity', 'Source', 'Added'],
      submitLabel: 'Add identity',
      fields: [
        field({ label: 'Handle', required: true, control: textField({ placeholder: 'alice', mono: true }) }),
        field({ label: 'Domain', required: true, control: textField({ placeholder: 'example.com', mono: true }) }),
      ],
      load: async () => ((await api.listTrustedIdentities({})) as ListTrustedIdentitiesResponse).identities,
    })
  }

  protected ready(): boolean {
    return Boolean(this.first.value.trim() && this.second.value.trim())
  }
  protected add(): Promise<unknown> {
    return api.addTrustedIdentity({ handle: this.first.value.trim(), domain: this.second.value.trim() })
  }
  protected keyOf(identity: TrustedIdentity): string {
    return `${identity.handle}@${identity.domain}`
  }
  protected cells(identity: TrustedIdentity): string[] {
    return [`${identity.handle}@${identity.domain}`, identity.source, relativeTime(identity.createdAt)]
  }
  protected cellClasses(): string[] {
    return ['mono', 'meta', 'meta']
  }
  protected async remove(identity: TrustedIdentity): Promise<void> {
    const ok = await confirmAction(
      `Remove ${identity.handle}@${identity.domain}? They will not be able to sign in again, but any session they already hold continues until it expires.`,
      { okLabel: 'Remove' },
    )
    if (!ok) return
    await this.action.run('The identity could not be removed.', () =>
      api.removeTrustedIdentity({ handle: identity.handle, domain: identity.domain }),
    )
    void this.list.reload()
  }
}

class TrustedDomainsCard extends TrustListCard<TrustedDomainPattern> {
  protected readonly addFailure = 'The pattern could not be added.'

  constructor(host: LoadHost) {
    super(host, {
      title: 'Trusted domain patterns',
      intro:
        'Anyone whose identity domain matches a pattern here may sign in. A pattern is far broader than a single identity — check it says what you mean before adding it.',
      headers: ['Pattern', 'Description', 'Added'],
      submitLabel: 'Add pattern',
      fields: [
        field({ label: 'Pattern', required: true, hint: 'For example *.example.com', control: textField({ mono: true }) }),
        field({ label: 'Description', hint: 'Why this domain is trusted.', control: textField() }),
      ],
      load: async () => ((await api.listTrustedDomainPatterns({})) as ListTrustedDomainPatternsResponse).patterns,
    })
  }

  protected ready(): boolean {
    return Boolean(this.first.value.trim())
  }
  protected add(): Promise<unknown> {
    return api.addTrustedDomainPattern({ pattern: this.first.value.trim(), description: this.second.value.trim() || undefined })
  }
  protected keyOf(entry: TrustedDomainPattern): string {
    return entry.patternId
  }
  protected cells(entry: TrustedDomainPattern): string[] {
    return [entry.pattern, entry.description || '—', relativeTime(entry.createdAt)]
  }
  protected cellClasses(): string[] {
    return ['mono', 'meta', 'meta']
  }
  protected async remove(entry: TrustedDomainPattern): Promise<void> {
    const ok = await confirmAction(`Remove pattern "${entry.pattern}"?`, { okLabel: 'Remove' })
    if (!ok) return
    await this.action.run('The pattern could not be removed.', () => api.removeTrustedDomainPattern({ patternId: entry.patternId }))
    void this.list.reload()
  }
}

export const tag = 'rc-admin-page'
define(tag, AdminPage)
