import type { ComboBox, PushButton, TextField } from '@catalystcommunity/controls'
import { api } from '~/api/client.ts'
import type { PrincipalDetail, PrincipalSummary } from '~/api/csilapi/types.gen.ts'
import '~/components/FederationPanel.ts'
import type { FederationPanel } from '~/components/FederationPanel.ts'
import { field } from '~/components/Field.ts'
import { ManagedOrgPage, orgHref } from '~/components/ManagedOrg.ts'
import { Action, ActionError, oneTimeSecret } from '~/components/ManagementPage.ts'
import { button, clearField, comboBox, confirmAction, setButtonText, setComboValue, setOptions, textField } from '~/ui/controls.ts'
import { h, setAttr, setText, show, syncKeyed } from '~/ui/dom.ts'
import { RcElement, define } from '~/ui/element.ts'
import { Loader } from '~/ui/load.ts'
import { href } from '~/ui/router.ts'

/**
 * Principals of the organization: the humans that act in it and its service
 * principals.
 *
 * A principal is anything that can act. A service principal belongs to the
 * organization, gets roles through groups (or a direct role), and acts with
 * tokens issued here. A token's authority is its capabilities intersected
 * with the roles of its principal, the same as a user token. An org admin
 * also links identities (a VCS login, a LinkKeys identity, a VCSStar
 * identity) to principals here, so one person has one set of subjects.
 */

type Entry = { kind: 'principal'; principal: PrincipalSummary } | { kind: 'detail'; principal: PrincipalSummary }

const IDENTITY_TYPES = [
  { value: 'vcs', label: 'VCS login', description: 'provider/login, for example github/octocat' },
  { value: 'linkkeys', label: 'LinkKeys identity', description: 'handle@domain or uuid@domain' },
  { value: 'vcsstar', label: 'VCSStar identity', description: 'the VCSStar identity' },
]

export class OrgPrincipalsPage extends ManagedOrgPage {
  protected readonly what = 'principals'

  #principals = new Loader(this, (orgId: string) => api.listPrincipals({ orgId }))
  #expanded: string | undefined

  #create = new Action(() => this.invalidate())
  #name = ''
  #displayName = ''

  #groupsLink!: HTMLAnchorElement
  #createError!: ActionError
  #nameInput!: TextField
  #displayInput!: TextField
  #submit!: PushButton
  #tableWrap!: HTMLElement
  #none!: HTMLElement
  #tbody!: HTMLTableSectionElement
  #federation!: FederationPanel

  protected buildBody(): HTMLElement {
    this.#federation = document.createElement('rc-federation-panel') as FederationPanel
    this.#groupsLink = h('a', { class: 'btn btn-sm' }, 'Manage groups')
    this.#tbody = h('tbody')
    this.#tableWrap = h(
      'div',
      { class: 'scroll-x' },
      h(
        'table',
        { class: 'table principals-table' },
        h('thead', null, h('tr', null, h('th', null, 'Principal'), h('th', null, 'Kind'), h('th', null, 'Status'))),
        this.#tbody,
      ),
    )
    this.#none = h('div', { class: 'empty' }, 'No principals yet.')
    return h(
      'div',
      { class: 'page' },
      h(
        'div',
        { class: 'page-header' },
        h(
          'div',
          null,
          h('h1', null, 'Principals'),
          h(
            'p',
            { class: 'meta' },
            'People and services that act in this organization. Give a service principal a role through a group, then issue it a token.',
          ),
        ),
        h('div', { class: 'row', style: { alignItems: 'center' } }, this.orgPicker(), this.#groupsLink),
      ),
      this.#buildCreateCard(),
      h('div', { class: 'card' }, h('h3', { class: 'section-title' }, 'Principals'), this.#tableWrap, this.#none),
      this.#federation,
    )
  }

  #buildCreateCard(): HTMLElement {
    this.#createError = new ActionError()
    this.#nameInput = textField({
      mono: true,
      onInput: (value) => {
        this.#name = value
        this.invalidate()
      },
    })
    this.#displayInput = textField({
      onInput: (value) => {
        this.#displayName = value
      },
    })
    this.#submit = button('Create service principal', { variant: 'primary', type: 'submit' })
    return h(
      'div',
      { class: 'card' },
      h('h3', { class: 'section-title' }, 'New service principal'),
      h(
        'form',
        { onSubmit: (event: Event) => void this.#onCreate(event) },
        this.#createError.el,
        h(
          'div',
          { class: 'form-row' },
          field({ label: 'Name', required: true, hint: 'Lowercase letters, digits and dashes. Subject: service:<name>.', control: this.#nameInput }).el,
          field({ label: 'Display name', control: this.#displayInput }).el,
        ),
        this.#submit,
      ),
    )
  }

  async #onCreate(event: Event): Promise<void> {
    event.preventDefault()
    const name = this.#name.trim()
    if (!name) return
    const ok = await this.#create.run('The service principal could not be created.', () =>
      api.createServicePrincipal({ orgId: this.managed.orgId(), name, displayName: this.#displayName.trim() || undefined }),
    )
    if (ok && this.isActive()) {
      this.#name = ''
      this.#displayName = ''
      clearField(this.#nameInput)
      clearField(this.#displayInput)
      void this.#principals.reload()
    }
  }

  /** Reloads the list after a change in a detail panel. */
  reloadList(): void {
    void this.#principals.reload()
  }

  protected renderBody(): void {
    const orgId = this.managed.orgId()
    this.#principals.sync(orgId || undefined)
    if (this.#federation.orgId !== orgId) {
      this.#federation.orgId = orgId
      this.#federation.invalidate()
    }
    setAttr(this.#groupsLink, 'href', href(orgHref('/org/groups', orgId)))
    this.#createError.set(this.#create.error)
    this.#submit.disabled = this.#create.busy || !this.#name.trim()
    setButtonText(this.#submit, this.#create.busy ? 'Creating…' : 'Create service principal')

    const principals = this.#principals.state.data?.principals ?? []
    show(this.#tableWrap, principals.length > 0)
    show(this.#none, this.#principals.state.loaded && principals.length === 0)
    const entries: Entry[] = []
    for (const principal of principals) {
      entries.push({ kind: 'principal', principal })
      if (this.#expanded === principal.principalId) entries.push({ kind: 'detail', principal })
    }
    syncKeyed(
      this.#tbody,
      entries,
      (entry) => `${entry.kind}:${entry.principal.principalId}`,
      (entry) => (entry.kind === 'principal' ? this.#createRow(entry.principal) : this.#createDetailRow(entry.principal, orgId)),
      (row, entry) => {
        if (entry.kind === 'principal') this.#updateRow(row as HTMLTableRowElement, entry.principal)
      },
    )
  }

  #createRow(principal: PrincipalSummary): HTMLTableRowElement {
    const toggle = button('', {
      small: true,
      variant: 'flat',
      onClick: () => {
        this.#expanded = this.#expanded === principal.principalId ? undefined : principal.principalId
        this.invalidate()
      },
    })
    toggle.classList.add('principal-toggle')
    return h(
      'tr',
      null,
      h('td', null, toggle, h('div', { class: 'meta mono truncate principal-id' })),
      h('td', { class: 'principal-kind' }),
      h('td', { class: 'principal-status' }),
    )
  }

  #updateRow(row: HTMLTableRowElement, principal: PrincipalSummary): void {
    const expanded = this.#expanded === principal.principalId
    const toggle = row.querySelector('.principal-toggle') as PushButton
    setButtonText(toggle, `${expanded ? '▾' : '▸'} ${principal.displayName || principal.name}`)
    setAttr(toggle, 'aria-label', `${expanded ? 'Hide' : 'Show'} details of ${principal.displayName || principal.name}`)
    setText(row.querySelector('.principal-id')!, principal.principalId)
    setText(row.querySelector('.principal-kind')!, principal.kind)
    setText(row.querySelector('.principal-status')!, principal.status)
  }

  #createDetailRow(principal: PrincipalSummary, orgId: string): HTMLTableRowElement {
    const panel = document.createElement('rc-principal-detail') as PrincipalPanel
    panel.principalId = principal.principalId
    panel.orgId = orgId
    panel.onChanged = () => this.reloadList()
    return h('tr', null, h('td', { colspan: '3' }, panel))
  }
}

/**
 * One principal: identities, groups, tokens, subjects, and the status of a
 * service principal. An element of its own, so its data lives exactly as long
 * as the row stays expanded.
 */
class PrincipalPanel extends RcElement {
  principalId = ''
  orgId = ''
  onChanged: () => void = () => {}

  #detail = new Loader(this, (key: string) => {
    const [orgId, principalId] = key.split('|')
    return api.getPrincipal({ orgId, principalId })
  })
  #groups = new Loader(this, (orgId: string) => api.listGroups({ orgId }))
  #action = new Action(() => this.invalidate())
  #current: PrincipalDetail | undefined
  #secret: string | undefined

  #error!: ActionError
  #statusButton!: PushButton
  #identityType!: ComboBox
  #identityValue!: TextField
  #linkButton!: PushButton
  #identities!: HTMLUListElement
  #groupPicker!: ComboBox
  #groupButton!: PushButton
  #groupList!: HTMLUListElement
  #tokenSection!: HTMLElement
  #tokenName!: TextField
  #tokenCaps!: TextField
  #issueButton!: PushButton
  #tokens!: HTMLUListElement
  #secretBox!: HTMLElement
  #subjects!: HTMLUListElement

  protected build(): void {
    this.classList.add('stack')
    this.#error = new ActionError()
    this.#statusButton = button('', { small: true, onClick: () => void this.#toggleStatus() })
    this.#identityType = comboBox({ ariaLabel: 'Identity type', options: IDENTITY_TYPES, value: 'vcs' })
    this.#identityValue = textField({ mono: true, onInput: () => this.invalidate() })
    this.#linkButton = button('Link identity', { small: true, variant: 'primary', type: 'submit' })
    this.#identities = h('ul', { class: 'plain-list principal-identities' })
    this.#groupPicker = comboBox({ ariaLabel: 'Group', onChange: () => this.invalidate() })
    this.#groupButton = button('Add to group', { small: true, onClick: () => void this.#addGroup() })
    this.#groupList = h('ul', { class: 'plain-list principal-groups' })
    this.#tokenName = textField({})
    this.#tokenCaps = textField({ mono: true, placeholder: 'jobs:read,jobs:submit' })
    this.#issueButton = button('Issue token', { small: true, variant: 'primary', type: 'submit' })
    this.#tokens = h('ul', { class: 'plain-list principal-tokens' })
    this.#secretBox = h('div')
    this.#subjects = h('ul', { class: 'plain-list mono principal-subjects' })
    this.#tokenSection = h(
      'div',
      { class: 'stack' },
      h('h4', null, 'Tokens'),
      h('p', { class: 'meta' }, 'A token acts with its capabilities intersected with the roles of this principal.'),
      this.#secretBox,
      this.#tokens,
      h(
        'form',
        { class: 'form-row', onSubmit: (event: Event) => void this.#issue(event) },
        field({ label: 'Token name', control: this.#tokenName }).el,
        field({ label: 'Capabilities', hint: 'Comma-separated. Empty gives every capability (admin only).', control: this.#tokenCaps }).el,
        this.#issueButton,
      ),
    )
    this.append(
      this.#error.el,
      h('div', { class: 'row' }, this.#statusButton),
      h('h4', null, 'Identities'),
      this.#identities,
      h(
        'form',
        { class: 'form-row', onSubmit: (event: Event) => void this.#link(event) },
        field({ label: 'Type', control: this.#identityType, labelTarget: this.#identityType }).el,
        field({ label: 'Value', control: this.#identityValue }).el,
        this.#linkButton,
      ),
      h('h4', null, 'Groups'),
      this.#groupList,
      h('div', { class: 'row' }, this.#groupPicker, this.#groupButton),
      this.#tokenSection,
      h('h4', null, 'Subjects'),
      h('p', { class: 'meta' }, 'Policy, approver lists and required reviews can name any of these.'),
      this.#subjects,
    )
  }

  async #run(message: string, work: () => Promise<{ principal: PrincipalDetail } | unknown>): Promise<void> {
    const ok = await this.#action.run(message, work)
    if (ok && this.isActive()) {
      void this.#detail.reload()
      this.onChanged()
    }
  }

  async #toggleStatus(): Promise<void> {
    const detail = this.#current
    if (!detail) return
    const disable = detail.principal.status === 'active'
    if (disable) {
      const confirmed = await confirmAction(`Disable ${detail.principal.name}? Its tokens stop working at once.`, { okLabel: 'Disable', danger: true })
      if (!confirmed) return
    }
    await this.#run('The status could not be changed.', () =>
      api.setPrincipalStatus({ orgId: this.orgId, principalId: this.principalId, status: disable ? 'disabled' : 'active' }),
    )
  }

  async #link(event: Event): Promise<void> {
    event.preventDefault()
    const value = this.#identityValue.value.trim()
    if (!value) return
    await this.#run('The identity could not be linked.', () =>
      api.linkPrincipalIdentity({ orgId: this.orgId, principalId: this.principalId, identityType: this.#identityType.value, identityValue: value }),
    )
    if (this.isActive() && !this.#action.error) clearField(this.#identityValue)
  }

  async #unlink(identityType: string, identityValue: string): Promise<void> {
    const confirmed = await confirmAction(`Remove ${identityType}:${identityValue} from this principal?`, { okLabel: 'Remove' })
    if (!confirmed) return
    await this.#run('The identity could not be removed.', () =>
      api.unlinkPrincipalIdentity({ orgId: this.orgId, principalId: this.principalId, identityType, identityValue }),
    )
  }

  async #addGroup(): Promise<void> {
    const groupId = this.#groupPicker.value
    if (!groupId) return
    await this.#run('The principal could not be added to the group.', () =>
      api.setPrincipalGroup({ orgId: this.orgId, principalId: this.principalId, groupId, member: true }),
    )
  }

  async #removeGroup(name: string): Promise<void> {
    const group = (this.#groups.state.data?.groups ?? []).find((g) => g.name === name)
    if (!group) return
    await this.#run('The principal could not be removed from the group.', () =>
      api.setPrincipalGroup({ orgId: this.orgId, principalId: this.principalId, groupId: group.groupId, member: false }),
    )
  }

  async #issue(event: Event): Promise<void> {
    event.preventDefault()
    const capabilities = this.#tokenCaps.value
      .split(',')
      .map((value) => value.trim())
      .filter(Boolean)
    let token = ''
    await this.#run('The token could not be issued.', async () => {
      const response = await api.issuePrincipalToken({
        orgId: this.orgId,
        principalId: this.principalId,
        name: this.#tokenName.value.trim(),
        capabilities,
      })
      token = response.token
    })
    if (token && this.isActive()) {
      this.#secret = token
      clearField(this.#tokenName)
      clearField(this.#tokenCaps)
      this.invalidate()
    }
  }

  async #revoke(tokenId: string, name: string): Promise<void> {
    const confirmed = await confirmAction(`Revoke token "${name}"? Anything that uses it stops working.`, { okLabel: 'Revoke', danger: true })
    if (!confirmed) return
    await this.#run('The token could not be revoked.', () => api.revokePrincipalToken({ orgId: this.orgId, principalId: this.principalId, tokenId }))
  }

  render(): void {
    this.#detail.sync(this.orgId && this.principalId ? `${this.orgId}|${this.principalId}` : undefined)
    this.#groups.sync(this.orgId || undefined)
    this.#error.set(this.#action.error)
    const detail = this.#detail.state.data?.principal
    this.#current = detail
    const busy = this.#action.busy
    const service = detail?.principal.kind === 'service'

    show(this.#statusButton, service)
    setButtonText(this.#statusButton, detail?.principal.status === 'active' ? 'Disable' : 'Enable')
    this.#statusButton.disabled = busy
    this.#linkButton.disabled = busy || !this.#identityValue.value.trim()
    this.#groupButton.disabled = busy || !this.#groupPicker.value
    this.#issueButton.disabled = busy || detail?.principal.status !== 'active'
    show(this.#tokenSection, service)

    const member = new Set(detail?.groups ?? [])
    const groups = (this.#groups.state.data?.groups ?? []).filter((g) => !member.has(g.name))
    setOptions(
      this.#groupPicker,
      groups.map((g) => ({ value: g.groupId, label: g.name })),
    )
    if (this.#groupPicker.value && !groups.some((g) => g.groupId === this.#groupPicker.value)) setComboValue(this.#groupPicker, '')

    syncKeyed(
      this.#identities,
      detail?.identities ?? [],
      (identity) => `${identity.identityType}:${identity.identityValue}`,
      (identity) => {
        const removable = identity.identityType !== 'user' && identity.identityType !== 'token'
        const remove = button('Remove', {
          small: true,
          ariaLabel: `Remove ${identity.identityType}:${identity.identityValue}`,
          onClick: () => void this.#unlink(identity.identityType, identity.identityValue),
        })
        show(remove, removable)
        return h(
          'li',
          { class: 'row' },
          h('span', { class: 'mono' }, `${identity.identityType}:${identity.identityValue}`),
          h('span', { class: 'meta' }, `verified by ${identity.verifiedBy}`),
          remove,
        )
      },
      (row) => {
        const remove = row.querySelector('push-button') as PushButton | null
        if (remove) remove.disabled = busy
      },
    )
    syncKeyed(
      this.#groupList,
      detail?.groups ?? [],
      (name) => name,
      (name) =>
        h(
          'li',
          { class: 'row' },
          h('span', null, name),
          button('Remove', { small: true, ariaLabel: `Remove from ${name}`, onClick: () => void this.#removeGroup(name) }),
        ),
      () => {},
    )
    syncKeyed(
      this.#tokens,
      detail?.tokens ?? [],
      (token) => token.tokenId,
      (token) => {
        const revoke = button('Revoke', { small: true, variant: 'danger', ariaLabel: `Revoke ${token.name}`, onClick: () => void this.#revoke(token.tokenId, token.name) })
        return h(
          'li',
          { class: 'row' },
          h('span', null, token.name),
          h('span', { class: 'meta mono token-caps' }),
          h('span', { class: 'meta token-state' }),
          revoke,
        )
      },
      (row, token) => {
        setText(row.querySelector('.token-caps')!, token.capabilities.join(', '))
        setText(row.querySelector('.token-state')!, token.isActive ? 'active' : 'revoked')
        show(row.querySelector('push-button') as HTMLElement, token.isActive)
      },
    )
    if (this.#secret && this.#secretBox.childElementCount === 0) {
      this.#secretBox.append(
        oneTimeSecret('Token', this.#secret, () => {
          this.#secret = undefined
          this.#secretBox.replaceChildren()
        }),
      )
    }
    syncKeyed(
      this.#subjects,
      detail?.subjects ?? [],
      (subject) => subject,
      (subject) => h('li', null, subject),
      () => {},
    )
  }
}
define('rc-principal-detail', PrincipalPanel)

export const tag = 'rc-org-principals-page'
define(tag, OrgPrincipalsPage)
