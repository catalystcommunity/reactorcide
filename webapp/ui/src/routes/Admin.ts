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
import { DRAFT_CHOICES, shedLevelCard, type ShedLevelCard } from '~/components/ShedLevel.ts'
import { githubAppsCard, type GitHubAppsCard } from '~/components/GitHubApps.ts'
import { oidcKeysCard, type OidcKeysCard } from '~/components/Oidc.ts'
import { ChipList } from '~/components/ChipList.ts'
import { DUPLICATE_CHOICES, DYNAMIC_CHOICES } from './ProjectEvents.ts'
import { EGRESS_CHOICES, RETRY_CHOICES } from '~/components/executionChoices.ts'

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
  #shed!: ShedLevelCard
  #githubApps!: GitHubAppsCard
  #oidcKeys!: OidcKeysCard

  protected build(): void {
    this.#refused = notPermitted('this instance')
    this.#cards = [new GlobalSettingsCard(this), new OrgEventSettingsCard(this), new VcsOwnerAllowlistCard(this), new TrustedIdentitiesCard(this), new TrustedDomainsCard(this)]
    // The global shed level. It is an element with its own load, so it is
    // added only when the gate is open (it requests nothing before).
    this.#shed = shedLevelCard()
    // The GitHub App pool (docs/github-app.md), added like the shed card.
    this.#githubApps = githubAppsCard()
    // The OIDC signing keys (docs/oidc.md), added like the shed card.
    this.#oidcKeys = oidcKeysCard()
    this.#body = h(
      'div',
      { class: 'page' },
      h(
        'div',
        { class: 'page-header' },
        h('div', null, h('h1', null, 'Administration'), h('p', { class: 'meta' }, 'Settings and access rules for the whole instance.')),
        h('div', { class: 'row' }, h('a', { href: href('/org/groups'), class: 'btn btn-sm' }, 'Groups'), h('a', { href: href('/org/roles'), class: 'btn btn-sm' }, 'Roles'), h('a', { href: href('/org/principals'), class: 'btn btn-sm' }, 'Principals'), h('a', { href: href('/org/subscriptions'), class: 'btn btn-sm' }, 'Event subscriptions')),
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
    if (allowed) {
      if (!this.#shed.isConnected) this.#cards[0].el.after(this.#shed)
      if (!this.#githubApps.isConnected) this.#body.append(this.#githubApps)
      if (!this.#oidcKeys.isConnected) this.#body.append(this.#oidcKeys)
      for (const card of this.#cards) card.render()
    }
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
  readonly #selects = new Map<string, { duplicate: ChoiceSelect; dynamic: ChoiceSelect; draft: ChoiceSelect; retries: ChoiceSelect; egress: ChoiceSelect }>()

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
        h('thead', null, h('tr', null, h('th', null, 'Organization'), h('th', null, 'Repeated deliveries'), h('th', null, 'Dynamic jobs'), h('th', null, 'Draft PRs'), h('th', null, 'Infra retries'), h('th', null, 'Egress control'))),
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
        'Repeated deliveries: what a new delivery for a change already seen does. Dynamic jobs: whether running jobs may start more jobs. Draft PRs: whether a draft pull request runs. Infra retries: how many times a job that failed for an infrastructure cause (the main container never started) runs again. Egress control: all job traffic goes through the egress proxy. A project can override each one; a project can only turn egress control on.',
      ),
      this.#error.el,
      this.#loading,
      this.#table,
    )
  }

  async #save(org: OrgSummary, change: { duplicateEvents?: string; dynamicJobs?: string; draftPrs?: string; infraRetries?: number; egressControl?: boolean }): Promise<void> {
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
        const draft = new ChoiceSelect({
          ariaLabel: `Draft PRs for ${o.displayName || o.name}`,
          choices: DRAFT_CHOICES,
          value: o.draftPrs || 'run',
          onChange: (value) => void this.#save(current(), { draftPrs: value }),
        })
        const retries = new ChoiceSelect({
          ariaLabel: `Infra retries for ${o.displayName || o.name}`,
          choices: RETRY_CHOICES,
          value: String(o.infraRetries ?? 1),
          onChange: (value) => void this.#save(current(), { infraRetries: Number(value) }),
        })
        const egress = new ChoiceSelect({
          ariaLabel: `Egress control for ${o.displayName || o.name}`,
          choices: EGRESS_CHOICES,
          value: o.egressControl ? 'on' : 'off',
          onChange: (value) => void this.#save(current(), { egressControl: value === 'on' }),
        })
        this.#selects.set(o.orgId, { duplicate, dynamic, draft, retries, egress })
        return h('tr', null, h('td', { class: 'org-name' }), h('td', null, duplicate.el), h('td', null, dynamic.el), h('td', null, draft.el), h('td', null, retries.el), h('td', null, egress.el))
      },
      (row, o) => {
        setText(row.querySelector('.org-name')!, o.displayName || o.name)
        const selects = this.#selects.get(o.orgId)!
        selects.duplicate.el.disabled = this.#action.busy
        selects.dynamic.el.disabled = this.#action.busy
        selects.draft.el.disabled = this.#action.busy
        selects.retries.el.disabled = this.#action.busy
        selects.egress.el.disabled = this.#action.busy
        if (!this.#action.busy) {
          selects.duplicate.update(DUPLICATE_CHOICES, o.duplicateEvents || 'run')
          selects.dynamic.update(DYNAMIC_CHOICES, o.dynamicJobs || 'allow')
          selects.draft.update(DRAFT_CHOICES, o.draftPrs || 'run')
          selects.retries.update(RETRY_CHOICES, String(o.infraRetries ?? 1))
          selects.egress.update(EGRESS_CHOICES, o.egressControl ? 'on' : 'off')
        }
      },
    )
  }
}

/**
 * The VCS owner allowlist of each organization (docs/principals.md). The
 * group VCS sync of an organization uses the GLOBAL VCS credential only for
 * these owners, so one tenant cannot read the teams of another VCS owner
 * with the shared credential. Global admin only.
 */
class VcsOwnerAllowlistCard implements Card {
  readonly el: HTMLElement
  readonly #orgs: Loader<string, OrgSummary[]>
  readonly #list: Loader<string, string[]>
  readonly #action: Action
  readonly #error = new ActionError()
  readonly #picker: ChoiceSelect
  readonly #owners: ChipList
  readonly #save: PushButton
  readonly #body: HTMLElement
  #orgName = ''
  #seeded = ''

  constructor(host: LoadHost) {
    this.#orgs = new Loader(host, async () => (await api.listOrgs({})).orgs)
    this.#list = new Loader(host, async (organization: string) => (await api.getVcsOwnerAllowlist({ organization })).owners)
    this.#action = new Action(() => host.invalidate())
    this.#picker = new ChoiceSelect({ ariaLabel: 'Organization', onChange: (value) => { this.#orgName = value; host.invalidate() } })
    this.#owners = new ChipList({
      ariaLabel: 'Add a VCS owner',
      placeholder: 'acme',
      mono: true,
      validate: (v) => (/^[a-z0-9][a-z0-9-]{0,38}$/.test(v.toLowerCase()) ? undefined : 'A VCS owner is an organization login: letters, digits and -.'),
      onChange: () => host.invalidate(),
    })
    this.#save = button('Save allowlist', { variant: 'primary', onClick: () => void this.#store() })
    this.#body = h(
      'div',
      { hidden: true },
      field({ label: 'Organization', control: this.#picker.el }).el,
      field({ label: 'VCS owners', hint: 'GitHub organization logins whose teams the group sync may read with the global VCS credential.', control: this.#owners.el, labelTarget: this.#owners.input }).el,
      h('div', { class: 'row' }, this.#save),
    )
    this.el = h(
      'div',
      { class: 'card' },
      h('h3', { class: 'section-title' }, 'VCS owner allowlist'),
      h('p', { class: 'meta' }, 'A group linked to a VCS team syncs with the credential of a project of its organization. With no such credential, it uses the global VCS credential, but only for the owners listed here.'),
      this.#error.el,
      this.#body,
    )
  }

  async #store(): Promise<void> {
    const organization = this.#orgName
    const owners = this.#owners.values().map((v) => v.toLowerCase())
    let stored: string[] | undefined
    const ok = await this.#action.run('The allowlist could not be saved.', async () => {
      stored = (await api.putVcsOwnerAllowlist({ organization, owners })).owners
    })
    if (ok && stored) {
      this.#seeded = ''
      this.#list.mutate(stored)
    }
  }

  render(): void {
    this.#orgs.sync('all')
    const orgs = this.#orgs.state.data ?? []
    if (!this.#orgName && orgs.length) this.#orgName = (orgs.find((o) => o.isDefault) ?? orgs[0]).name
    this.#picker.update(orgs.map((o) => ({ value: o.name, label: o.displayName || o.name, description: '' })), this.#orgName)
    this.#list.sync(this.#orgName || undefined)
    const owners = this.#list.state.data
    const key = owners ? JSON.stringify([this.#orgName, owners]) : ''
    if (owners && key !== this.#seeded) {
      this.#seeded = key
      this.#owners.set(owners)
    }
    this.#error.set(this.#action.error ?? this.#list.state.error?.message ?? this.#orgs.state.error?.message)
    show(this.#body, orgs.length > 0)
    this.#save.disabled = this.#action.busy || !owners
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
