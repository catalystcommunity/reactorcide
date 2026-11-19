import type { PushButton, TextField } from '@catalystcommunity/controls'
import { api } from '~/api/client.ts'
import type { SecretGrant, SecretPathEntry } from '~/api/csilapi/types.gen.ts'
import { ChoiceSelect } from '~/components/ChoiceInput.ts'
import { field } from '~/components/Field.ts'
import { ManagedOrgPage } from '~/components/ManagedOrg.ts'
import { Action, ActionError } from '~/components/ManagementPage.ts'
import { button, clearField, confirmAction, setButtonText, textField } from '~/ui/controls.ts'
import { h, setAttr, setText, show, syncKeyed } from '~/ui/dom.ts'
import { define } from '~/ui/element.ts'
import { Loader } from '~/ui/load.ts'
import { classListError, splitList } from '~/components/RefClasses.ts'

/**
 * Secrets and the grants that let jobs read them.
 *
 * THIS PAGE IS WRITE-ONLY BY DESIGN. It lists secret PATHS and KEY NAMES and
 * never a value: the UI service has no operation that returns one, so there is
 * nothing here to accidentally render, log, or screenshot. A value can be set
 * and replaced; it cannot be read back.
 *
 * A grant is what makes a secret reachable from a job. Without one, a secret
 * exists and no job can resolve it.
 *
 * Scoped to the organization chosen in the header (`?org=`). A secret belongs
 * to the org that owns the projects whose jobs resolve it, so managing one in
 * the caller's own user id (the old behaviour) stored values no job could
 * ever reach.
 */

const MATCH_MODES = [
  { value: 'exact', label: 'Exact', description: 'Matches one literal value.' },
  { value: 'prefix', label: 'Prefix', description: 'Matches anything starting with the pattern.' },
  { value: 'glob', label: 'Glob', description: 'Matches a shell-style pattern, such as build-*.' },
]

/** Every match mode, for editing a grant that may use any of them. */
const EDIT_MATCH_MODES = [
  { value: 'any', label: 'Any', description: 'Matches every value.' },
  ...MATCH_MODES,
  { value: 'regex', label: 'Regular expression', description: 'Matches a regular expression.' },
]

const DELIVERY_MODES = [
  { value: 'env', label: 'Environment', description: 'The job gets the value in its environment.' },
  {
    value: 'proxy',
    label: 'Egress proxy',
    description: 'The job gets a placeholder. The egress proxy puts the value in a header, for the proxy hosts only.',
  },
]

const HEADER_FORMAT_HINT = 'Use {} one time for the secret, for example Bearer {}. Empty: the value alone.'

/** The delivery fields of a grant request, from the form values. */
export function deliveryRequest(delivery: string, hosts: string, header: string, format: string): {
  delivery: string
  proxyHosts: string[]
  proxyHeader: string
  proxyHeaderFormat: string
} {
  if (delivery !== 'proxy') return { delivery: 'env', proxyHosts: [], proxyHeader: '', proxyHeaderFormat: '' }
  return { delivery: 'proxy', proxyHosts: splitList(hosts), proxyHeader: header.trim(), proxyHeaderFormat: format }
}

/** An error for a header format, or undefined. "{}" must occur one time. */
export function headerFormatError(format: string): string | undefined {
  if (!format) return undefined
  return format.split('{}').length === 2 ? undefined : 'The header format must contain {} exactly one time.'
}

/** How a grant delivers its secrets, as one short text. */
export function grantDeliveryText(grant: SecretGrant): string {
  if (grant.delivery !== 'proxy') return 'env'
  const header = grant.proxyHeader ? ` · ${grant.proxyHeader}` : ''
  return `proxy → ${(grant.proxyHosts ?? []).join(', ') || '(no host)'}${header}`
}

interface PathRowState {
  key: string
  select: ChoiceSelect
  remove: PushButton
  action: Action
  error: ActionError
}

export class OrgSecretsPage extends ManagedOrgPage {
  protected readonly what = 'secrets'

  #paths = new Loader(this, (orgId: string) => api.listSecretPaths({ orgId }))
  #grants = new Loader(this, (orgId: string) => api.listSecretGrants({ orgId }))

  // Set a secret.
  #set = new Action(() => this.invalidate())
  #path = ''
  #key = ''
  /** Held only until the write returns, then dropped on every outcome. */
  #value = ''
  #saved = false
  #setError!: ActionError
  #savedNotice!: HTMLElement
  #pathInput!: TextField
  #keyInput!: TextField
  #valueInput!: TextField
  #setSubmit!: PushButton

  // New grant.
  #grant = new Action(() => this.invalidate())
  #grantName = ''
  #pathMatch = 'prefix'
  #pathPattern = ''
  #jobMatch = 'glob'
  #jobPattern = ''
  #grantError!: ActionError
  #grantNameInput!: TextField
  #pathPatternInput!: TextField
  #jobPatternInput!: TextField
  #refAnyInput!: TextField
  #refNoneInput!: TextField
  #delivery = 'env'
  #deliverySelect!: ChoiceSelect
  #proxyBox!: HTMLElement
  #proxyHostsInput!: TextField
  #proxyHeaderInput!: TextField
  #proxyFormatInput!: TextField
  #grantSubmit!: PushButton

  // Edit a grant. The form is made one time and filled from the chosen grant.
  #edit = new Action(() => this.invalidate())
  #editing: SecretGrant | null = null
  #editCard!: HTMLElement
  #editTitle!: HTMLHeadingElement
  #editError!: ActionError
  #editName!: TextField
  #editPathMatch!: ChoiceSelect
  #editPathPattern!: TextField
  #editJobMatch!: ChoiceSelect
  #editJobPattern!: TextField
  #editDescription!: TextField
  #editRefAny!: TextField
  #editRefNone!: TextField
  #editDelivery!: ChoiceSelect
  #editProxyBox!: HTMLElement
  #editProxyHosts!: TextField
  #editProxyHeader!: TextField
  #editProxyFormat!: TextField
  #editValues = { pathMatch: 'prefix', jobMatch: 'any', delivery: 'env' }
  #editSubmit!: PushButton

  #pathsWrap!: HTMLElement
  #noPaths!: HTMLElement
  #pathsBody!: HTMLTableSectionElement
  #pathRows = new WeakMap<Element, PathRowState>()

  #grantsWrap!: HTMLElement
  #noGrants!: HTMLElement
  #grantsBody!: HTMLTableSectionElement
  #grantRows = new WeakMap<Element, { action: Action; error: ActionError; remove: PushButton; edit: PushButton; data: SecretGrant }>()

  protected buildBody(): HTMLElement {
    this.#pathsBody = h('tbody')
    this.#pathsWrap = h(
      'div',
      { class: 'scroll-x' },
      h('table', { class: 'table' }, h('thead', null, h('tr', null, h('th', null, 'Path'), h('th', null, 'Keys'), h('th'))), this.#pathsBody),
    )
    this.#noPaths = h('div', { class: 'empty' }, 'No secrets stored for this organization.')

    this.#grantsBody = h('tbody')
    this.#grantsWrap = h(
      'div',
      { class: 'scroll-x' },
      h(
        'table',
        { class: 'table' },
        h(
          'thead',
          null,
          h('tr', null, h('th', null, 'Name'), h('th', null, 'Secret paths'), h('th', null, 'Job names'), h('th', null, 'Ref classes'), h('th', null, 'Delivery'), h('th', null, 'Project'), h('th')),
        ),
        this.#grantsBody,
      ),
    )
    this.#noGrants = h('div', { class: 'empty' }, 'No grants. Without one, a job cannot resolve any secret in this organization.')

    return h(
      'div',
      { class: 'page' },
      h(
        'div',
        { class: 'page-header' },
        h(
          'div',
          null,
          h('h1', null, 'Secrets'),
          h(
            'p',
            { class: 'meta' },
            'Values are never shown here and cannot be read back through this interface — only set, replaced, and deleted. Jobs reach a secret through a grant.',
          ),
        ),
        this.orgPicker(),
      ),
      this.#buildSetCard(),
      h(
        'div',
        { class: 'card' },
        h('h3', { class: 'section-title' }, 'Stored secrets'),
        h('p', { class: 'meta', style: { marginBottom: 'var(--space-3)' } }, 'Paths and key names only.'),
        this.#pathsWrap,
        this.#noPaths,
      ),
      this.#buildGrantCard(),
      h('div', { class: 'card' }, h('h3', { class: 'section-title' }, 'Grants'), this.#grantsWrap, this.#noGrants),
      this.#buildEditCard(),
    )
  }

  /**
   * Setting a secret value.
   *
   * The value field is a password input, is never echoed back, and is cleared
   * the moment the write succeeds OR fails. Leaving a secret sitting in a form
   * field after an error is how one ends up in a screenshot or a password
   * manager.
   */
  #buildSetCard(): HTMLElement {
    this.#setError = new ActionError()
    this.#savedNotice = h('div', { class: 'alert alert-info', role: 'status', hidden: true }, 'Secret stored. Its value cannot be displayed again.')
    this.#pathInput = textField({
      mono: true,
      onInput: (value) => {
        this.#path = value
        this.invalidate()
      },
    })
    this.#keyInput = textField({
      mono: true,
      onInput: (value) => {
        this.#key = value
        this.invalidate()
      },
    })
    this.#valueInput = textField({
      type: 'password',
      autocomplete: 'off',
      onInput: (value) => {
        this.#value = value
        this.#saved = false
        this.invalidate()
      },
    })
    this.#setSubmit = button('Store secret', { variant: 'primary', type: 'submit' })
    return h(
      'div',
      { class: 'card' },
      h('h3', { class: 'section-title' }, 'Set a secret'),
      h(
        'form',
        { onSubmit: (event: Event) => void this.#onSet(event) },
        this.#setError.el,
        this.#savedNotice,
        h(
          'div',
          { class: 'form-row' },
          field({ label: 'Path', required: true, hint: 'For example reactorcide/github.', control: this.#pathInput }).el,
          field({ label: 'Key', required: true, hint: 'For example api_token.', control: this.#keyInput }).el,
        ),
        field({
          label: 'Value',
          required: true,
          hint: 'Write-only. This is never displayed again, here or anywhere else.',
          tooltip:
            'Setting a value that already exists replaces it. Jobs reference it as ${secret:path:key} and never see it in a job definition.',
          control: this.#valueInput,
        }).el,
        this.#setSubmit,
      ),
    )
  }

  async #onSet(event: Event): Promise<void> {
    event.preventDefault()
    const path = this.#path.trim()
    const key = this.#key.trim()
    const value = this.#value
    if (!path || !key || !value) return
    const orgId = this.managed.orgId()
    const ok = await this.#set.run('The secret could not be stored.', () => api.setSecret({ orgId, path, key, value }))
    // Cleared on every outcome, not only success.
    this.#value = ''
    clearField(this.#valueInput)
    if (!this.isActive()) return
    this.#saved = ok
    this.invalidate()
    if (ok) void this.#paths.reload()
  }

  #buildGrantCard(): HTMLElement {
    this.#grantError = new ActionError()
    this.#grantNameInput = textField({
      onInput: (value) => {
        this.#grantName = value
        this.invalidate()
      },
    })
    this.#pathPatternInput = textField({
      mono: true,
      placeholder: 'reactorcide/',
      onInput: (value) => {
        this.#pathPattern = value
        this.invalidate()
      },
    })
    this.#jobPatternInput = textField({
      mono: true,
      placeholder: 'deploy-*',
      onInput: (value) => {
        this.#jobPattern = value
      },
    })
    this.#refAnyInput = textField({ mono: true, placeholder: 'production', onInput: () => this.invalidate() })
    this.#refNoneInput = textField({ mono: true, placeholder: 'fork', onInput: () => this.invalidate() })
    const pathMatch = new ChoiceSelect({ value: this.#pathMatch, choices: MATCH_MODES, onChange: (v) => (this.#pathMatch = v) })
    const jobMatch = new ChoiceSelect({ value: this.#jobMatch, choices: MATCH_MODES, onChange: (v) => (this.#jobMatch = v) })
    this.#deliverySelect = new ChoiceSelect({
      ariaLabel: 'Delivery',
      value: this.#delivery,
      choices: DELIVERY_MODES,
      onChange: (v) => {
        this.#delivery = v
        this.invalidate()
      },
    })
    this.#proxyHostsInput = textField({ mono: true, placeholder: 'api.github.com', onInput: () => this.invalidate() })
    this.#proxyHeaderInput = textField({ mono: true, placeholder: 'Authorization' })
    this.#proxyFormatInput = textField({ mono: true, placeholder: 'Bearer {}', onInput: () => this.invalidate() })
    this.#proxyBox = proxyFields(this.#proxyHostsInput, this.#proxyHeaderInput, this.#proxyFormatInput)
    this.#grantSubmit = button('Create grant', { variant: 'primary', type: 'submit' })
    return h(
      'div',
      { class: 'card' },
      h('h3', { class: 'section-title' }, 'New grant'),
      h(
        'p',
        { class: 'meta', style: { marginBottom: 'var(--space-4)' } },
        'A grant says which jobs may resolve which secrets. Keep the patterns as narrow as the work allows — a broad grant hands every matching job every matching secret.',
      ),
      h(
        'form',
        { onSubmit: (event: Event) => void this.#onGrant(event) },
        this.#grantError.el,
        field({ label: 'Name', required: true, hint: 'What this grant is for, in a few words.', control: this.#grantNameInput }).el,
        h(
          'div',
          { class: 'form-row' },
          field({ label: 'Secret path match', required: true, control: pathMatch.el }).el,
          field({ label: 'Secret path pattern', required: true, control: this.#pathPatternInput }).el,
        ),
        h(
          'div',
          { class: 'form-row' },
          field({ label: 'Job name match', required: true, control: jobMatch.el }).el,
          field({ label: 'Job name pattern', hint: 'Leave empty to match every job in the organization.', control: this.#jobPatternInput }).el,
        ),
        h(
          'div',
          { class: 'form-row' },
          field({ label: 'Only for ref classes', hint: 'A comma list. The grant applies only to a job whose ref has one of these classes. Empty is no condition.', control: this.#refAnyInput }).el,
          field({ label: 'Never for ref classes', hint: 'A comma list. The grant never applies to a job whose ref has one of these classes, for example fork.', control: this.#refNoneInput }).el,
        ),
        field({ label: 'Delivery', required: true, hint: 'How a job gets the secrets of this grant (docs/egress-proxy.md).', control: this.#deliverySelect.el }).el,
        this.#proxyBox,
        this.#grantSubmit,
      ),
    )
  }

  async #onGrant(event: Event): Promise<void> {
    event.preventDefault()
    const name = this.#grantName.trim()
    const pattern = this.#pathPattern.trim()
    if (!name || !pattern) return
    const refClassesAny = splitList(this.#refAnyInput.value)
    const refClassesNone = splitList(this.#refNoneInput.value)
    if (classListError(refClassesAny, false) || classListError(refClassesNone, false)) return
    const ok = await this.#grant.run('The grant could not be created.', () =>
      api.createSecretGrant({
        orgId: this.managed.orgId(),
        name,
        secretPathMatch: this.#pathMatch,
        secretPathPattern: pattern,
        jobNameMatch: this.#jobMatch,
        jobNamePattern: this.#jobPattern.trim() || undefined,
        ...(refClassesAny.length ? { refClassesAny } : {}),
        ...(refClassesNone.length ? { refClassesNone } : {}),
        ...(this.#delivery === 'proxy'
          ? deliveryRequest('proxy', this.#proxyHostsInput.value, this.#proxyHeaderInput.value, this.#proxyFormatInput.value)
          : {}),
      }),
    )
    if (ok && this.isActive()) {
      this.#grantName = ''
      this.#pathPattern = ''
      this.#jobPattern = ''
      clearField(this.#grantNameInput)
      clearField(this.#pathPatternInput)
      clearField(this.#jobPatternInput)
      clearField(this.#refAnyInput)
      clearField(this.#refNoneInput)
      clearField(this.#proxyHostsInput)
      clearField(this.#proxyHeaderInput)
      clearField(this.#proxyFormatInput)
      this.#delivery = 'env'
      this.#deliverySelect.update(DELIVERY_MODES, 'env')
      void this.#grants.reload()
    }
  }

  #buildEditCard(): HTMLElement {
    this.#editTitle = h('h3', { class: 'section-title' })
    this.#editError = new ActionError()
    const changed = (): void => this.invalidate()
    this.#editName = textField({ onInput: changed })
    this.#editPathMatch = new ChoiceSelect({ ariaLabel: 'Secret path match', choices: EDIT_MATCH_MODES, value: 'prefix', onChange: (v) => (this.#editValues.pathMatch = v) })
    this.#editPathPattern = textField({ mono: true, onInput: changed })
    this.#editJobMatch = new ChoiceSelect({ ariaLabel: 'Job name match', choices: EDIT_MATCH_MODES, value: 'any', onChange: (v) => (this.#editValues.jobMatch = v) })
    this.#editJobPattern = textField({ mono: true })
    this.#editDescription = textField()
    this.#editRefAny = textField({ mono: true, onInput: changed })
    this.#editRefNone = textField({ mono: true, onInput: changed })
    this.#editDelivery = new ChoiceSelect({
      ariaLabel: 'Delivery',
      choices: DELIVERY_MODES,
      value: 'env',
      onChange: (v) => {
        this.#editValues.delivery = v
        this.invalidate()
      },
    })
    this.#editProxyHosts = textField({ mono: true, placeholder: 'api.github.com', onInput: changed })
    this.#editProxyHeader = textField({ mono: true, placeholder: 'Authorization' })
    this.#editProxyFormat = textField({ mono: true, placeholder: 'Bearer {}', onInput: changed })
    this.#editProxyBox = proxyFields(this.#editProxyHosts, this.#editProxyHeader, this.#editProxyFormat)
    this.#editSubmit = button('Save grant', { variant: 'primary', type: 'submit' })
    const cancel = button('Cancel', { onClick: () => this.#closeEdit() })
    this.#editCard = h(
      'div',
      { class: 'card grant-edit', hidden: true },
      this.#editTitle,
      h(
        'form',
        { onSubmit: (event: Event) => void this.#onEdit(event) },
        this.#editError.el,
        field({ label: 'Name', required: true, control: this.#editName }).el,
        h(
          'div',
          { class: 'form-row' },
          field({ label: 'Secret path match', required: true, control: this.#editPathMatch.el }).el,
          field({ label: 'Secret path pattern', required: true, control: this.#editPathPattern }).el,
        ),
        h(
          'div',
          { class: 'form-row' },
          field({ label: 'Job name match', required: true, control: this.#editJobMatch.el }).el,
          field({ label: 'Job name pattern', hint: 'Empty with match any: every job.', control: this.#editJobPattern }).el,
        ),
        field({ label: 'Description', control: this.#editDescription }).el,
        h(
          'div',
          { class: 'form-row' },
          field({ label: 'Only for ref classes', hint: 'A comma list. Empty is no condition.', control: this.#editRefAny }).el,
          field({ label: 'Never for ref classes', hint: 'A comma list. Empty is no condition.', control: this.#editRefNone }).el,
        ),
        field({ label: 'Delivery', required: true, control: this.#editDelivery.el }).el,
        this.#editProxyBox,
        h('div', { class: 'row' }, this.#editSubmit, cancel),
      ),
    )
    return this.#editCard
  }

  /** Fills the edit form one time from a grant. */
  #openEdit(grant: SecretGrant): void {
    this.#editing = grant
    this.#edit.clearError()
    this.#editName.value = grant.name
    this.#editValues = { pathMatch: grant.secretPathMatch, jobMatch: grant.jobNameMatch, delivery: grant.delivery === 'proxy' ? 'proxy' : 'env' }
    this.#editPathMatch.update(EDIT_MATCH_MODES, this.#editValues.pathMatch)
    this.#editPathPattern.value = grant.secretPathPattern
    this.#editJobMatch.update(EDIT_MATCH_MODES, this.#editValues.jobMatch)
    this.#editJobPattern.value = grant.jobNamePattern ?? ''
    this.#editDescription.value = grant.description ?? ''
    this.#editRefAny.value = (grant.refClassesAny ?? []).join(', ')
    this.#editRefNone.value = (grant.refClassesNone ?? []).join(', ')
    this.#editDelivery.update(DELIVERY_MODES, this.#editValues.delivery)
    this.#editProxyHosts.value = (grant.proxyHosts ?? []).join(', ')
    this.#editProxyHeader.value = grant.proxyHeader ?? ''
    this.#editProxyFormat.value = grant.proxyHeaderFormat ?? ''
    this.invalidate()
    this.#editCard.scrollIntoView?.({ block: 'nearest' })
  }

  #closeEdit(): void {
    this.#editing = null
    this.invalidate()
  }

  #editProblem(): string | undefined {
    if (!this.#editName.value.trim()) return 'Give the grant a name.'
    if (this.#editValues.pathMatch !== 'any' && !this.#editPathPattern.value.trim()) return 'Give the secret path pattern.'
    if (this.#editValues.delivery === 'proxy' && splitList(this.#editProxyHosts.value).length === 0) return 'A proxy grant needs at least one proxy host.'
    if (this.#editValues.delivery === 'proxy' && headerFormatError(this.#editProxyFormat.value)) return headerFormatError(this.#editProxyFormat.value)
    return classListError(splitList(this.#editRefAny.value), false) ?? classListError(splitList(this.#editRefNone.value), false)
  }

  async #onEdit(event: Event): Promise<void> {
    event.preventDefault()
    const grant = this.#editing
    if (!grant || this.#editProblem()) return
    const request = {
      grantId: grant.grantId,
      name: this.#editName.value.trim(),
      secretPathMatch: this.#editValues.pathMatch,
      secretPathPattern: this.#editPathPattern.value.trim(),
      jobNameMatch: this.#editValues.jobMatch,
      jobNamePattern: this.#editJobPattern.value.trim(),
      description: this.#editDescription.value.trim(),
      setRefClasses: true,
      refClassesAny: splitList(this.#editRefAny.value),
      refClassesNone: splitList(this.#editRefNone.value),
      ...deliveryRequest(this.#editValues.delivery, this.#editProxyHosts.value, this.#editProxyHeader.value, this.#editProxyFormat.value),
    }
    const ok = await this.#edit.run('The grant could not be saved.', () => api.updateSecretGrant(request))
    if (ok && this.isActive()) {
      this.#editing = null
      void this.#grants.reload()
    }
  }

  protected renderBody(): void {
    const orgId = this.managed.orgId()
    this.#paths.sync(orgId || undefined)
    this.#grants.sync(orgId || undefined)

    this.#setError.set(this.#set.error)
    show(this.#savedNotice, this.#saved)
    this.#setSubmit.disabled = this.#set.busy || !this.#path.trim() || !this.#key.trim() || !this.#value
    setButtonText(this.#setSubmit, this.#set.busy ? 'Storing…' : 'Store secret')

    this.#grantError.set(this.#grant.error)
    const refError = classListError(splitList(this.#refAnyInput.value), false) ?? classListError(splitList(this.#refNoneInput.value), false)
    this.#grantSubmit.disabled = this.#grant.busy || !this.#grantName.trim() || !this.#pathPattern.trim() || Boolean(refError)
    setButtonText(this.#grantSubmit, this.#grant.busy ? 'Creating…' : 'Create grant')
    show(this.#proxyBox, this.#delivery === 'proxy')
    // A hidden required field blocks the native form submit, so the hosts
    // are required only while they show.
    this.#proxyHostsInput.required = this.#delivery === 'proxy'
    if (this.#delivery === 'proxy' && (splitList(this.#proxyHostsInput.value).length === 0 || headerFormatError(this.#proxyFormatInput.value))) {
      this.#grantSubmit.disabled = true
    }

    const editing = this.#editing
    show(this.#editCard, editing)
    if (editing) {
      setText(this.#editTitle, `Edit grant ${editing.name}`)
      show(this.#editProxyBox, this.#editValues.delivery === 'proxy')
      this.#editProxyHosts.required = this.#editValues.delivery === 'proxy'
      const problem = this.#editProblem()
      this.#editError.set(this.#edit.error ?? problem)
      this.#editSubmit.disabled = this.#edit.busy || Boolean(problem)
      setButtonText(this.#editSubmit, this.#edit.busy ? 'Saving…' : 'Save grant')
    }

    const paths = this.#paths.state.data?.paths ?? []
    show(this.#pathsWrap, paths.length > 0)
    show(this.#noPaths, paths.length === 0)
    syncKeyed(this.#pathsBody, paths, (entry) => entry.path, (entry) => this.#createPathRow(entry), (row, entry) => this.#updatePathRow(row, entry))

    const grants = this.#grants.state.data?.grants ?? []
    show(this.#grantsWrap, grants.length > 0)
    show(this.#noGrants, grants.length === 0)
    syncKeyed(this.#grantsBody, grants, (grant) => grant.grantId, (grant) => this.#createGrantRow(grant), (row, grant) => this.#updateGrantRow(row, grant))
  }

  #createPathRow(entry: SecretPathEntry): HTMLTableRowElement {
    const action = new Action(() => this.invalidate())
    const error = new ActionError()
    const state: PathRowState = {
      key: '',
      action,
      error,
      select: new ChoiceSelect({
        ariaLabel: `Key of ${entry.path} to delete`,
        placeholder: 'Delete a key…',
        value: '',
        onChange: (value) => {
          state.key = value
          this.invalidate()
        },
      }),
      remove: button('Delete', {
        small: true,
        variant: 'danger',
        onClick: async () => {
          const key = state.key
          if (!key) return
          if (!(await confirmAction(`Delete ${entry.path}:${key}? Any job that references it will fail.`, { okLabel: 'Delete' }))) return
          const ok = await action.run('The secret could not be deleted.', () =>
            api.deleteSecret({ orgId: this.managed.orgId(), path: entry.path, key }),
          )
          if (ok && this.isActive()) {
            state.key = ''
            void this.#paths.reload()
          }
        },
      }),
    }
    const row = h(
      'tr',
      null,
      h('td', { class: 'mono secret-path' }),
      h('td', null, h('div', { class: 'row secret-keys' })),
      h('td', null, h('div', { class: 'row' }, state.select.el, state.remove, error.el)),
    )
    this.#pathRows.set(row, state)
    return row
  }

  #updatePathRow(row: HTMLTableRowElement, entry: SecretPathEntry): void {
    setText(row.querySelector('.secret-path')!, entry.path)
    syncKeyed(
      row.querySelector('.secret-keys')!,
      entry.keys,
      (key) => key,
      () => h('code', { class: 'secret-key' }),
      (el, key) => setText(el, key),
    )
    const state = this.#pathRows.get(row)!
    if (state.key && !entry.keys.includes(state.key)) state.key = ''
    state.select.update(
      entry.keys.map((key) => ({ value: key, label: key, description: '' })),
      state.key,
    )
    show(state.remove, state.key)
    state.remove.disabled = state.action.busy
    state.error.set(state.action.error)
  }

  #createGrantRow(grant: SecretGrant): HTMLTableRowElement {
    const action = new Action(() => this.invalidate())
    const error = new ActionError()
    const edit = button('Edit', { small: true, ariaLabel: `Edit grant ${grant.name}`, onClick: () => this.#openEdit(this.#grantRows.get(row)!.data) })
    const remove = button('Delete', {
      small: true,
      variant: 'danger',
      onClick: async () => {
        const confirmed = await confirmAction(
          `Delete grant "${grant.name}"? Jobs relying on it will stop being able to resolve those secrets.`,
          { okLabel: 'Delete' },
        )
        if (!confirmed) return
        const ok = await action.run('The grant could not be deleted.', () => api.deleteSecretGrant({ grantId: grant.grantId }))
        if (ok && this.isActive()) void this.#grants.reload()
      },
    })
    const row = h(
      'tr',
      null,
      h('td', { class: 'grant-name' }),
      h('td', { class: 'mono meta grant-paths' }),
      h('td', { class: 'mono meta grant-jobs' }),
      h('td', { class: 'mono meta grant-ref-classes' }),
      h('td', { class: 'mono meta grant-delivery' }),
      h('td', { class: 'mono meta truncate grant-project' }),
      h('td', null, h('div', { class: 'row' }, edit, remove), error.el),
    )
    this.#grantRows.set(row, { action, error, remove, edit, data: grant })
    return row
  }

  #updateGrantRow(row: HTMLTableRowElement, grant: SecretGrant): void {
    setText(row.querySelector('.grant-name')!, grant.name)
    setText(row.querySelector('.grant-paths')!, `${grant.secretPathMatch} ${grant.secretPathPattern}`)
    setText(row.querySelector('.grant-jobs')!, `${grant.jobNameMatch} ${grant.jobNamePattern || '(any)'}`)
    setText(row.querySelector('.grant-ref-classes')!, grantRefClassText(grant))
    setText(row.querySelector('.grant-delivery')!, grantDeliveryText(grant))
    setText(row.querySelector('.grant-project')!, grant.projectId || 'whole org')
    const state = this.#grantRows.get(row)!
    state.data = grant
    setAttr(state.edit, 'aria-label', `Edit grant ${grant.name}`)
    state.edit.disabled = state.action.busy
    state.remove.disabled = state.action.busy
    state.error.set(state.action.error)
  }
}

/** The proxy delivery fields of a grant form. Shown only for proxy delivery. */
function proxyFields(hosts: TextField, header: TextField, format: TextField): HTMLElement {
  return h(
    'div',
    { hidden: true },
    field({
      label: 'Proxy hosts',
      required: true,
      hint: 'A comma list of host names (a leading *. is a subdomain wildcard). Only requests to these hosts get the value.',
      control: hosts,
    }).el,
    h(
      'div',
      { class: 'form-row' },
      field({ label: 'Header', hint: 'The request header the proxy sets, for example Authorization.', control: header }).el,
      field({ label: 'Header format', hint: HEADER_FORMAT_HINT, control: format }).el,
    ),
  )
}

/** The ref class conditions of a grant, as one short text. */
export function grantRefClassText(grant: SecretGrant): string {
  const parts: string[] = []
  if (grant.refClassesAny?.length) parts.push(`only ${grant.refClassesAny.join(', ')}`)
  if (grant.refClassesNone?.length) parts.push(`never ${grant.refClassesNone.join(', ')}`)
  return parts.join('; ') || 'any'
}

export const tag = 'rc-org-secrets-page'
define(tag, OrgSecretsPage)
