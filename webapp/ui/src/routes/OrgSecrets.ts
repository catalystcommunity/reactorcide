import type { PushButton, TextField } from '@catalystcommunity/controls'
import { api } from '~/api/client.ts'
import type { SecretGrant, SecretPathEntry } from '~/api/csilapi/types.gen.ts'
import { ChoiceSelect } from '~/components/ChoiceInput.ts'
import { field } from '~/components/Field.ts'
import { ManagedOrgPage } from '~/components/ManagedOrg.ts'
import { Action, ActionError } from '~/components/ManagementPage.ts'
import { button, clearField, confirmAction, setButtonText, textField } from '~/ui/controls.ts'
import { h, setText, show, syncKeyed } from '~/ui/dom.ts'
import { define } from '~/ui/element.ts'
import { Loader } from '~/ui/load.ts'

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
  #grantSubmit!: PushButton

  #pathsWrap!: HTMLElement
  #noPaths!: HTMLElement
  #pathsBody!: HTMLTableSectionElement
  #pathRows = new WeakMap<Element, PathRowState>()

  #grantsWrap!: HTMLElement
  #noGrants!: HTMLElement
  #grantsBody!: HTMLTableSectionElement
  #grantRows = new WeakMap<Element, { action: Action; error: ActionError; remove: PushButton }>()

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
          h('tr', null, h('th', null, 'Name'), h('th', null, 'Secret paths'), h('th', null, 'Job names'), h('th', null, 'Project'), h('th')),
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
    const pathMatch = new ChoiceSelect({ value: this.#pathMatch, choices: MATCH_MODES, onChange: (v) => (this.#pathMatch = v) })
    const jobMatch = new ChoiceSelect({ value: this.#jobMatch, choices: MATCH_MODES, onChange: (v) => (this.#jobMatch = v) })
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
        this.#grantSubmit,
      ),
    )
  }

  async #onGrant(event: Event): Promise<void> {
    event.preventDefault()
    const name = this.#grantName.trim()
    const pattern = this.#pathPattern.trim()
    if (!name || !pattern) return
    const ok = await this.#grant.run('The grant could not be created.', () =>
      api.createSecretGrant({
        orgId: this.managed.orgId(),
        name,
        secretPathMatch: this.#pathMatch,
        secretPathPattern: pattern,
        jobNameMatch: this.#jobMatch,
        jobNamePattern: this.#jobPattern.trim() || undefined,
      }),
    )
    if (ok && this.isActive()) {
      this.#grantName = ''
      this.#pathPattern = ''
      this.#jobPattern = ''
      clearField(this.#grantNameInput)
      clearField(this.#pathPatternInput)
      clearField(this.#jobPatternInput)
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
    this.#grantSubmit.disabled = this.#grant.busy || !this.#grantName.trim() || !this.#pathPattern.trim()
    setButtonText(this.#grantSubmit, this.#grant.busy ? 'Creating…' : 'Create grant')

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
      h('td', { class: 'mono meta truncate grant-project' }),
      h('td', null, remove, error.el),
    )
    this.#grantRows.set(row, { action, error, remove })
    return row
  }

  #updateGrantRow(row: HTMLTableRowElement, grant: SecretGrant): void {
    setText(row.querySelector('.grant-name')!, grant.name)
    setText(row.querySelector('.grant-paths')!, `${grant.secretPathMatch} ${grant.secretPathPattern}`)
    setText(row.querySelector('.grant-jobs')!, `${grant.jobNameMatch} ${grant.jobNamePattern || '(any)'}`)
    setText(row.querySelector('.grant-project')!, grant.projectId || 'whole org')
    const state = this.#grantRows.get(row)!
    state.remove.disabled = state.action.busy
    state.error.set(state.action.error)
  }
}

export const tag = 'rc-org-secrets-page'
define(tag, OrgSecretsPage)
