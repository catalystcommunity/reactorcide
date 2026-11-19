import type { CheckBox, PushButton, TextField } from '@catalystcommunity/controls'
import { api } from '~/api/client.ts'
import type { EnumChoice, ExecutionProfileSummary } from '~/api/csilapi/types.gen.ts'
import { ChoiceMultiSelect } from '~/components/ChoiceInput.ts'
import { field } from '~/components/Field.ts'
import { ManagedOrgPage, orgHref } from '~/components/ManagedOrg.ts'
import { Action, ActionError } from '~/components/ManagementPage.ts'
import { classListError, splitList } from '~/components/RefClasses.ts'
import { button, checkBox, clearField, confirmAction, setButtonText, textField } from '~/ui/controls.ts'
import { h, setAttr, setText, show, syncKeyed } from '~/ui/dom.ts'
import { define } from '~/ui/element.ts'
import { Loader } from '~/ui/load.ts'
import { href } from '~/ui/router.ts'

/**
 * Execution profiles of an organization (docs/organizations-and-ci-policy.md,
 * docs/builds-and-caches.md, docs/egress-proxy.md).
 *
 * A profile is the authority a job runs with: secrets, root, runtime
 * capabilities, worker classes, ceilings, the cache namespace and write mode,
 * and egress control. The CI policy selects a profile for each job.
 *
 * The form is a full put: Save sends every field. Edit loads the values of a
 * profile into the form ONE time; a refresh of the list does not touch the
 * form, so a draft stays. The resource ceilings have no control here, so an
 * edit sends them back as they were loaded.
 */

/** Built-in profiles. The coordinator refuses to delete them. */
export const BUILT_IN_PROFILES = ['standard', 'pr-untrusted']

/** The timeout ceiling text as a number, undefined when empty, or null when not valid. */
export function parseCeiling(text: string): number | undefined | null {
  const value = text.trim()
  if (!value) return undefined
  if (!/^\d+$/.test(value)) return null
  const n = Number(value)
  return n > 0 ? n : null
}

/** One short text of a profile's authority, for the list. */
export function profileText(p: ExecutionProfileSummary): string {
  const parts = [p.denySecrets ? 'no secrets' : 'secrets', p.mayRunAsRoot ? 'root allowed' : 'no root']
  if (p.trustedCacheWrites) parts.push(p.cacheWriteClasses?.length ? `cache writes for ${p.cacheWriteClasses.join(', ')}` : 'cache writes')
  else parts.push('read-only cache')
  if (p.egressControl) parts.push('egress control')
  if (p.timeoutCeilingSeconds) parts.push(`timeout ≤ ${p.timeoutCeilingSeconds} s`)
  return parts.join(' · ')
}

type RowState = { remove: PushButton; edit: PushButton; action: Action; error: ActionError; data: ExecutionProfileSummary }

export class OrgProfilesPage extends ManagedOrgPage {
  protected readonly what = 'execution profiles'

  #profiles = new Loader(this, (organization: string) => api.listExecutionProfiles({ organization }))
  #classes = new Loader(this, async (organization: string) => (await api.listWorkerClasses({ organization })).workerClasses)
  #save = new Action(() => this.invalidate())
  /** The profile the form edits, or null for a new profile. */
  #editing: ExecutionProfileSummary | null = null
  #workerClasses: string[] = []

  #trustLink!: HTMLAnchorElement
  #classesLink!: HTMLAnchorElement
  #formTitle!: HTMLHeadingElement
  #formError!: ActionError
  #name!: TextField
  #denySecrets!: CheckBox
  #secretPaths!: TextField
  #capabilities!: TextField
  #root!: CheckBox
  #classPicker!: ChoiceMultiSelect
  #timeout!: TextField
  #cacheNamespace!: TextField
  #artifactNamespace!: TextField
  #trustedWrites!: CheckBox
  #namespaceClasses!: TextField
  #writeClasses!: TextField
  #egress!: CheckBox
  #submit!: PushButton
  #cancel!: PushButton
  #tableWrap!: HTMLElement
  #none!: HTMLElement
  #tbody!: HTMLTableSectionElement
  #rows = new WeakMap<Element, RowState>()

  #orgName(): string {
    return this.managed.org()?.name ?? ''
  }

  protected buildBody(): HTMLElement {
    this.#trustLink = h('a', { class: 'btn btn-sm' }, 'CI trust')
    this.#classesLink = h('a', { class: 'btn btn-sm' }, 'Worker classes')
    this.#formTitle = h('h3', { class: 'section-title' }, 'Apply a profile')
    this.#formError = new ActionError()
    const changed = (): void => this.invalidate()
    this.#name = textField({ mono: true, placeholder: 'integration-pr', onInput: changed })
    this.#denySecrets = checkBox('Deny secrets', { onChange: changed })
    this.#secretPaths = textField({ mono: true, placeholder: 'reactorcide/public', onInput: changed })
    this.#capabilities = textField({ mono: true, placeholder: 'gpu', onInput: changed })
    this.#root = checkBox('May run as root', { onChange: changed })
    this.#classPicker = new ChoiceMultiSelect({ ariaLabel: 'Allowed worker classes', onChange: (values) => {
      this.#workerClasses = values
      this.invalidate()
    } })
    this.#timeout = textField({ mono: true, placeholder: '1800', onInput: changed })
    this.#cacheNamespace = textField({ mono: true, placeholder: 'integration-pr', onInput: changed })
    this.#artifactNamespace = textField({ mono: true, placeholder: 'integration-pr', onInput: changed })
    this.#trustedWrites = checkBox('Trusted cache writes', { onChange: changed })
    this.#namespaceClasses = textField({ mono: true, placeholder: 'release, default', onInput: changed })
    this.#writeClasses = textField({ mono: true, placeholder: 'default, tag', onInput: changed })
    this.#egress = checkBox('Egress control', { onChange: changed })
    this.#submit = button('Save profile', { variant: 'primary', type: 'submit' })
    this.#cancel = button('Cancel edit', { onClick: () => this.#reset() })
    show(this.#cancel, false)

    const form = h(
      'form',
      { class: 'profile-form', onSubmit: (event: Event) => void this.#onSubmit(event) },
      this.#formError.el,
      field({ label: 'Name', required: true, hint: 'Lowercase. The CI policy selects a profile by this name.', control: this.#name }).el,
      h('h4', { class: 'section-title' }, 'Secrets and identity'),
      h('div', { class: 'checkbox-row' }, this.#denySecrets),
      h('p', { class: 'field-hint' }, 'A job with this profile resolves no secret, except the paths of the allowlist.'),
      field({ label: 'Secret path allowlist', hint: 'A comma list of secret paths the job may still resolve when secrets are denied.', control: this.#secretPaths }).el,
      h('div', { class: 'checkbox-row' }, this.#root),
      h('p', { class: 'field-hint' }, 'Off: the job cannot use run_as root.'),
      field({ label: 'Runtime capabilities', hint: 'A comma list of capabilities the job may ask for (docker, builder, gpu). Empty: none.', control: this.#capabilities }).el,
      h('h4', { class: 'section-title' }, 'Placement and limits'),
      field({
        label: 'Allowed worker classes',
        hint: 'The worker classes a job with this profile may use. None chosen: every class that is not protected.',
        control: this.#classPicker.el,
      }).el,
      field({ label: 'Timeout ceiling (seconds)', hint: 'The largest timeout a job may ask for. Empty: no ceiling.', control: this.#timeout }).el,
      h('h4', { class: 'section-title' }, 'Caches and artifacts'),
      h(
        'div',
        { class: 'form-row' },
        field({ label: 'Cache namespace', hint: 'The base cache namespace. Empty: the default namespace of the organization.', control: this.#cacheNamespace }).el,
        field({ label: 'Artifact namespace', hint: 'Empty: the cache namespace.', control: this.#artifactNamespace }).el,
      ),
      h('div', { class: 'checkbox-row' }, this.#trustedWrites),
      h('p', { class: 'field-hint' }, 'Off: the job reads the cache and cannot write it. Keep it off for untrusted code.'),
      field({
        label: 'Cache namespace classes',
        hint: 'An ordered comma list of ref classes. The first class the job has is added to the namespace as a suffix. Empty: one namespace.',
        control: this.#namespaceClasses,
      }).el,
      field({
        label: 'Cache write classes',
        hint: 'A comma list of ref classes. With trusted cache writes on, only a job with one of these classes writes the cache. Empty: every class.',
        control: this.#writeClasses,
      }).el,
      h('h4', { class: 'section-title' }, 'Network'),
      h('div', { class: 'checkbox-row' }, this.#egress),
      h('p', { class: 'field-hint' }, 'On: all egress of the job goes through the egress proxy (docs/egress-proxy.md). Another setting cannot turn it off.'),
      h('div', { class: 'row' }, this.#submit, this.#cancel),
    )

    this.#tbody = h('tbody')
    this.#tableWrap = h(
      'div',
      { class: 'scroll-x' },
      h(
        'table',
        { class: 'table profiles-table' },
        h('thead', null, h('tr', null, h('th', null, 'Profile'), h('th', null, 'Authority'), h('th', null, 'Worker classes'), h('th', null, 'Cache namespace'), h('th', null, h('span', { class: 'sr-only' }, 'Actions')))),
        this.#tbody,
      ),
    )
    this.#none = h('div', { class: 'empty' }, 'No execution profiles.')

    return h(
      'div',
      { class: 'page' },
      h(
        'div',
        { class: 'page-header' },
        h(
          'div',
          null,
          h('h1', null, 'Execution profiles'),
          h('p', { class: 'meta' }, 'The authority a job runs with. The CI policy selects a profile for each job. A child profile must have the same or less authority than its parent.'),
        ),
        h('div', { class: 'row', style: { alignItems: 'center' } }, this.orgPicker(), this.#trustLink, this.#classesLink),
      ),
      h('div', { class: 'card' }, h('h3', { class: 'section-title' }, 'Profiles'), this.#tableWrap, this.#none),
      h('div', { class: 'card' }, this.#formTitle, form),
    )
  }

  /** Loads a profile into the form one time. */
  #edit(profile: ExecutionProfileSummary): void {
    this.#editing = profile
    this.#save.clearError()
    this.#name.value = profile.name
    this.#name.disabled = true
    this.#denySecrets.checked = profile.denySecrets
    this.#secretPaths.value = profile.secretPathAllowlist.join(', ')
    this.#capabilities.value = profile.runtimeCapabilities.join(', ')
    this.#root.checked = profile.mayRunAsRoot
    this.#workerClasses = [...profile.allowedWorkerClasses]
    this.#timeout.value = profile.timeoutCeilingSeconds ? String(profile.timeoutCeilingSeconds) : ''
    this.#cacheNamespace.value = profile.cacheNamespace ?? ''
    this.#artifactNamespace.value = profile.artifactNamespace ?? ''
    this.#trustedWrites.checked = profile.trustedCacheWrites
    this.#namespaceClasses.value = (profile.cacheNamespaceClasses ?? []).join(', ')
    this.#writeClasses.value = (profile.cacheWriteClasses ?? []).join(', ')
    this.#egress.checked = Boolean(profile.egressControl)
    this.invalidate()
    this.#name.scrollIntoView?.({ block: 'nearest' })
  }

  /** Empties the form for a new profile. */
  #reset(): void {
    this.#editing = null
    this.#name.disabled = false
    for (const control of [this.#name, this.#secretPaths, this.#capabilities, this.#timeout, this.#cacheNamespace, this.#artifactNamespace, this.#namespaceClasses, this.#writeClasses]) {
      clearField(control)
    }
    for (const box of [this.#denySecrets, this.#root, this.#trustedWrites, this.#egress]) box.checked = false
    this.#workerClasses = []
    this.invalidate()
  }

  /** The validation problem of the form, or undefined. */
  #problem(): string | undefined {
    const name = this.#name.value.trim()
    if (!name) return 'Give the profile a name.'
    if (parseCeiling(this.#timeout.value) === null) return 'The timeout ceiling must be a positive number of seconds.'
    return classListError(splitList(this.#namespaceClasses.value), false) ?? classListError(splitList(this.#writeClasses.value), false)
  }

  /** The profile the form describes. */
  profileFromForm(): ExecutionProfileSummary {
    const text = (control: TextField): string | undefined => control.value.trim() || undefined
    const profile: ExecutionProfileSummary = {
      name: this.#name.value.trim(),
      denySecrets: this.#denySecrets.checked,
      secretPathAllowlist: splitList(this.#secretPaths.value),
      runtimeCapabilities: splitList(this.#capabilities.value),
      mayRunAsRoot: this.#root.checked,
      allowedWorkerClasses: [...this.#workerClasses],
      // No control edits the ceilings: send back what was loaded.
      resourceCeilings: this.#editing?.resourceCeilings ?? {},
      trustedCacheWrites: this.#trustedWrites.checked,
      cacheNamespaceClasses: splitList(this.#namespaceClasses.value),
      cacheWriteClasses: splitList(this.#writeClasses.value),
      egressControl: this.#egress.checked,
    }
    const ceiling = parseCeiling(this.#timeout.value)
    if (typeof ceiling === 'number') profile.timeoutCeilingSeconds = ceiling
    const cache = text(this.#cacheNamespace)
    if (cache) profile.cacheNamespace = cache
    const artifact = text(this.#artifactNamespace)
    if (artifact) profile.artifactNamespace = artifact
    return profile
  }

  async #onSubmit(event: Event): Promise<void> {
    event.preventDefault()
    if (this.#problem()) return
    const organization = this.#orgName()
    const profile = this.profileFromForm()
    const ok = await this.#save.run('The profile could not be saved.', () => api.putExecutionProfile({ organization, profile }))
    if (ok && this.isActive()) {
      this.#reset()
      void this.#profiles.reload()
    }
  }

  protected renderBody(): void {
    const organization = this.#orgName()
    this.#profiles.sync(organization || undefined)
    this.#classes.sync(organization || undefined)
    const orgId = this.managed.orgId()
    setAttr(this.#trustLink, 'href', href(orgHref('/org/ci-trust', orgId)))
    setAttr(this.#classesLink, 'href', href(orgHref('/workers/classes', orgId)))

    const editing = this.#editing
    setText(this.#formTitle, editing ? `Edit profile ${editing.name}` : 'Apply a profile')
    show(this.#cancel, editing)
    const problem = this.#problem()
    this.#formError.set(this.#save.error ?? (this.#name.value.trim() && problem !== 'Give the profile a name.' ? problem : undefined))
    this.#submit.disabled = this.#save.busy || Boolean(problem)
    setButtonText(this.#submit, this.#save.busy ? 'Saving…' : editing ? 'Save changes' : 'Save profile')

    const known = (this.#classes.state.data ?? []).map((c) => c.name)
    // A class the profile names but the org no longer has stays choosable.
    const names = [...new Set([...known, ...this.#workerClasses])]
    const choices: EnumChoice[] = names.map((name) => ({ value: name, label: name, description: '' }))
    this.#classPicker.update(choices, this.#workerClasses)

    const profiles = this.#profiles.state.data?.profiles ?? []
    const loaded = Boolean(this.#profiles.state.data)
    show(this.#tableWrap, profiles.length > 0)
    show(this.#none, loaded && profiles.length === 0)
    syncKeyed(this.#tbody, profiles, (p) => p.name, (p) => this.#createRow(p), (row, p) => this.#updateRow(row, p))
  }

  #createRow(profile: ExecutionProfileSummary): HTMLTableRowElement {
    const action = new Action(() => this.invalidate())
    const error = new ActionError()
    const state: RowState = { action, error, data: profile, edit: null!, remove: null! }
    state.edit = button('Edit', { small: true, ariaLabel: `Edit profile ${profile.name}`, onClick: () => this.#edit(state.data) })
    state.remove = button('Delete', {
      small: true,
      variant: 'danger',
      ariaLabel: `Delete profile ${profile.name}`,
      onClick: async () => {
        const name = state.data.name
        if (!(await confirmAction(`Delete execution profile "${name}"? A CI policy that selects it then fails for the jobs that use it.`, { okLabel: 'Delete' }))) return
        const ok = await action.run('The profile could not be deleted.', () => api.deleteExecutionProfile({ organization: this.#orgName(), name }))
        if (ok && this.isActive()) {
          if (this.#editing?.name === name) this.#reset()
          void this.#profiles.reload()
        }
      },
    })
    const row = h(
      'tr',
      null,
      h('td', { class: 'mono profile-name' }),
      h('td', { class: 'meta profile-authority' }),
      h('td', { class: 'mono meta profile-classes' }),
      h('td', { class: 'mono meta profile-cache' }),
      h('td', null, h('div', { class: 'row' }, state.edit, state.remove), error.el),
    )
    this.#rows.set(row, state)
    return row
  }

  #updateRow(row: HTMLTableRowElement, profile: ExecutionProfileSummary): void {
    const state = this.#rows.get(row)!
    state.data = profile
    const builtIn = BUILT_IN_PROFILES.includes(profile.name)
    setText(row.querySelector('.profile-name')!, builtIn ? `${profile.name} (built-in)` : profile.name)
    setText(row.querySelector('.profile-authority')!, profileText(profile))
    setText(row.querySelector('.profile-classes')!, profile.allowedWorkerClasses.join(', ') || 'any unprotected')
    const split = profile.cacheNamespaceClasses?.length ? ` split by ${profile.cacheNamespaceClasses.join(', ')}` : ''
    setText(row.querySelector('.profile-cache')!, `${profile.cacheNamespace || 'default'}${split}`)
    show(state.remove, !builtIn)
    state.remove.disabled = state.action.busy
    state.edit.disabled = state.action.busy
    state.error.set(state.action.error)
  }
}

export const tag = 'rc-org-profiles-page'
define(tag, OrgProfilesPage)
