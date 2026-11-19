import type { CheckBox, PushButton, TextField } from '@catalystcommunity/controls'
import { ServiceError, api } from '~/api/client.ts'
import type {
  EnumChoice,
  GetImageRolloutStatusResponse,
  ImageAliasRollout,
  ImageAliasSummary,
  ImageRolloutWorker,
  ListImageAliasesResponse,
  SetImageAliasRequest,
} from '~/api/csilapi/types.gen.ts'
import { ChoiceSelect } from '~/components/ChoiceInput.ts'
import { field } from '~/components/Field.ts'
import { Action, ActionError, describeError } from '~/components/ManagementPage.ts'
import { relativeTime } from '~/components/States.ts'
import { button, checkBox, clearField, confirmAction, setButtonText, textField } from '~/ui/controls.ts'
import { h, setAttr, setText, show, syncKeyed } from '~/ui/dom.ts'
import { RcElement, define } from '~/ui/element.ts'
import { Loader } from '~/ui/load.ts'
import { href } from '~/ui/router.ts'
import {
  aliasNameError,
  aliasRefError,
  characteristicsText,
  imageStatusClass,
  imageStatusText,
  imageVersionText,
  outcomeText,
  parseCharacteristics,
  imageFailureText,
  rolloutPoolText,
  rolloutText,
  shortDigest,
} from './imageText.ts'
import './images.css'

/**
 * The image aliases of one scope (docs/image-aliases.md): an organization,
 * or a project of it.
 *
 * Every alias operation needs the manage permission of the scope
 * (organization admin, or project owner). list-image-aliases answers
 * `forbidden` to anybody else, so a refusal of the list hides the edit
 * controls and says why; nothing else is drawn for that caller.
 *
 * The form is built one time. A refresh (each 15 seconds, and after a
 * change) changes the table rows in place and never touches the form, so a
 * half-typed reference stays.
 */

const KIND_CHOICES: EnumChoice[] = [
  { value: '', label: 'From the os', description: 'vm for macos and windows, container for other values.' },
  { value: 'vm', label: 'vm', description: 'A VM image for vm workers.' },
  { value: 'container', label: 'container', description: 'A container image for docker, containerd and kubernetes workers.' },
]

const REFRESH_MS = 15_000

interface Scope {
  organization: string
  projectId?: string
}

interface AliasItem {
  kind: 'alias' | 'rollout'
  alias: ImageAliasSummary
}

interface AliasRowState {
  data: ImageAliasSummary
  action: Action
  error: ActionError
  rollout: PushButton
  edit: PushButton
  override: PushButton
  remove: PushButton
}

/** True when the error is the coordinator's permission refusal. */
export function isRefusal(error: unknown): boolean {
  return error instanceof ServiceError && (error.code === 'forbidden' || error.code === 'permission_denied' || error.code === 'unauthorized')
}

export class ImageAliasesPanel extends RcElement {
  organization = ''
  projectId = ''

  #aliases = new Loader<Scope, ListImageAliasesResponse>(this, (scope) => api.listImageAliases(scope))
  #rollouts = new Loader<Scope, GetImageRolloutStatusResponse>(this, (scope) => api.getImageRolloutStatus(scope))
  #save = new Action(() => this.invalidate())
  /** The alias the form edits, or null for a new alias. */
  #editing: ImageAliasSummary | null = null
  #kind = ''
  #expanded = new Set<string>()
  #result: string | undefined

  #refused!: HTMLElement
  #loadError!: ActionError
  #rolloutError!: HTMLParagraphElement
  #content!: HTMLElement
  #tableWrap!: HTMLElement
  #none!: HTMLElement
  #tbody!: HTMLTableSectionElement
  #rows = new WeakMap<Element, AliasRowState>()
  #rolloutRows = new WeakMap<Element, RolloutView>()

  #formTitle!: HTMLHeadingElement
  #formError!: ActionError
  #formResult!: HTMLDivElement
  #name!: TextField
  #ref!: TextField
  #kindSelect!: ChoiceSelect
  #os!: TextField
  #arch!: TextField
  #chars!: TextField
  #force!: CheckBox
  #submit!: PushButton
  #cancel!: PushButton

  setScope(organization: string, projectId = ''): void {
    if (this.organization === organization && this.projectId === projectId) return
    this.organization = organization
    this.projectId = projectId
    this.invalidate()
  }

  #scope(): Scope | undefined {
    if (!this.organization) return undefined
    return this.projectId ? { organization: this.organization, projectId: this.projectId } : { organization: this.organization }
  }

  #isProject(): boolean {
    return Boolean(this.projectId)
  }

  /** True for a row of the scope of this panel (not an organization alias in a project view). */
  #own(alias: ImageAliasSummary): boolean {
    return this.#isProject() ? alias.scope === 'project' : alias.scope !== 'project'
  }

  protected build(): void {
    this.#refused = h(
      'div',
      { class: 'empty image-aliases-refused', hidden: true },
      h('div', { class: 'empty-title' }, 'You do not have permission to manage these image aliases.'),
      h('p', null, 'Image aliases need the organization admin role, or the project owner role for a project. Ask an administrator if you need access.'),
    )
    this.#loadError = new ActionError()
    this.#rolloutError = h('p', { class: 'meta image-rollout-error', hidden: true })

    this.#tbody = h('tbody')
    this.#tableWrap = h(
      'div',
      { class: 'scroll-x' },
      h(
        'table',
        { class: 'table image-aliases-table' },
        h(
          'thead',
          null,
          h(
            'tr',
            null,
            h('th', null, 'Alias'),
            h('th', null, 'Kind'),
            h('th', null, 'Platform'),
            h('th', null, 'Characteristics'),
            h('th', null, 'Active pin'),
            h('th', null, 'Pending pin'),
            h('th', null, h('span', { class: 'sr-only' }, 'Actions')),
          ),
        ),
        this.#tbody,
      ),
    )
    this.#none = h('div', { class: 'empty' }, 'No image aliases yet. Create one below.')

    this.#formTitle = h('h3', { class: 'section-title' }, 'Set an alias')
    this.#formError = new ActionError()
    this.#formResult = h('div', { class: 'alert alert-info image-alias-result', role: 'status', hidden: true })
    const changed = (): void => this.invalidate()
    this.#name = textField({ mono: true, placeholder: 'windows-vs2022', onInput: changed })
    this.#ref = textField({ mono: true, placeholder: 'oci://registry.example.com/vm/windows-vs2022:2026.11@sha256:…', onInput: changed })
    this.#kindSelect = new ChoiceSelect({ ariaLabel: 'Kind', choices: KIND_CHOICES, value: '', onChange: (value) => {
      this.#kind = value
      this.invalidate()
    } })
    this.#os = textField({ mono: true, placeholder: 'linux', onInput: changed })
    this.#arch = textField({ mono: true, placeholder: 'amd64', onInput: changed })
    this.#chars = textField({ mono: true, placeholder: 'worker_class=mac-xcode', onInput: changed })
    this.#force = checkBox('Promote at once (force)', { onChange: changed })
    this.#submit = button('Set alias', { variant: 'primary', type: 'submit' })
    this.#cancel = button('Cancel edit', { onClick: () => this.#reset() })
    show(this.#cancel, false)

    const form = h(
      'form',
      { class: 'image-alias-form', onSubmit: (event: Event) => void this.#onSubmit(event) },
      this.#formError.el,
      this.#formResult,
      h(
        'div',
        { class: 'form-row' },
        field({ label: 'Name', required: true, hint: '1 to 63 characters: a-z, 0-9, ".", "_", "-".', control: this.#name }).el,
        field({ label: 'Kind', hint: 'Empty: vm for macos and windows, else container.', control: this.#kindSelect.el }).el,
      ),
      field({
        label: 'Pinned reference',
        required: true,
        hint: 'oci://host/repo:tag@sha256:<digest>, host/repo:tag@sha256:<digest>, or s3://bucket/prefix/name:tag@sha256:<digest>.',
        tooltip: 'A new digest for an alias that exists becomes the pending pin. The coordinator promotes it when each online serving worker has the image.',
        control: this.#ref,
      }).el,
      h(
        'div',
        { class: 'form-row' },
        field({ label: 'Os', hint: 'linux, macos, or windows. Necessary for a new alias.', control: this.#os }).el,
        field({ label: 'Arch', hint: 'amd64 or arm64. Necessary for a new alias.', control: this.#arch }).el,
      ),
      field({
        label: 'Characteristics',
        hint: 'More worker characteristics, key=value, comma separated. worker_class=NAME limits the alias to pools that serve that class. Kind, os, arch, and characteristics cannot change after the create: use a new alias name.',
        control: this.#chars,
      }).el,
      h('div', { class: 'checkbox-row' }, this.#force),
      h('p', { class: 'field-hint' }, 'Force makes the new pin active at once and does not wait for the workers. Jobs then wait until a worker has the new image.'),
      h('div', { class: 'row' }, this.#submit, this.#cancel),
    )

    this.#content = h(
      'div',
      null,
      h('div', { class: 'card' }, h('h3', { class: 'section-title' }, 'Aliases'), this.#rolloutError, this.#tableWrap, this.#none),
      h('div', { class: 'card' }, this.#formTitle, form),
    )
    this.append(this.#loadError.el, this.#refused, this.#content)
  }

  protected connected(): void {
    const timer = setInterval(() => {
      if (document.visibilityState !== 'visible' || !this.#scope()) return
      void this.#aliases.reload()
      void this.#rollouts.reload()
    }, REFRESH_MS)
    this.own(() => clearInterval(timer))
  }

  /** Loads an alias into the form one time. */
  #edit(alias: ImageAliasSummary, override = false): void {
    this.#editing = override ? null : alias
    this.#save.clearError()
    this.#result = undefined
    this.#name.value = alias.name
    this.#name.disabled = !override
    this.#ref.value = override ? alias.activeRef : alias.pendingRef ?? alias.activeRef
    this.#kind = alias.kind
    this.#os.value = alias.os
    this.#arch.value = alias.arch
    this.#chars.value = characteristicsText(alias.characteristics)
    // Kind, os, arch, and characteristics cannot change after the create.
    this.#lockMetadata(!override)
    this.#force.checked = false
    this.invalidate()
    this.#ref.scrollIntoView?.({ block: 'nearest' })
  }

  /** Locks the fields that cannot change for an alias that exists. */
  #lockMetadata(locked: boolean): void {
    this.#os.disabled = locked
    this.#arch.disabled = locked
    this.#chars.disabled = locked
    this.#kindSelect.el.disabled = locked
  }

  #reset(): void {
    this.#editing = null
    this.#name.disabled = false
    for (const control of [this.#name, this.#ref, this.#os, this.#arch, this.#chars]) clearField(control)
    this.#kind = ''
    this.#lockMetadata(false)
    this.#force.checked = false
    this.invalidate()
  }

  /** The validation problem of the form, or undefined. */
  #problem(): string | undefined {
    const name = this.#name.value.trim()
    const nameError = aliasNameError(name)
    if (nameError) return nameError
    const refError = aliasRefError(this.#ref.value.trim())
    if (refError) return refError
    if (!this.#editing && (!this.#os.value.trim() || !this.#arch.value.trim())) return 'A new alias needs an os and an arch.'
    const parsed = parseCharacteristics(this.#chars.value)
    if ('error' in parsed) return parsed.error
    return undefined
  }

  /** The request the form describes. */
  requestFromForm(): SetImageAliasRequest {
    const req: SetImageAliasRequest = { organization: this.organization, name: this.#name.value.trim(), ref: this.#ref.value.trim() }
    if (this.projectId) req.projectId = this.projectId
    if (this.#force.checked) req.force = true
    // An edit changes only the pin: kind, os, arch, and characteristics
    // cannot change after the create (the coordinator refuses a change).
    if (this.#editing) return req
    if (this.#kind) req.kind = this.#kind
    const os = this.#os.value.trim()
    if (os) req.os = os
    const arch = this.#arch.value.trim()
    if (arch) req.arch = arch
    const parsed = parseCharacteristics(this.#chars.value)
    if ('entries' in parsed) req.characteristics = parsed.entries
    return req
  }

  async #onSubmit(event: Event): Promise<void> {
    event.preventDefault()
    if (this.#problem()) return
    const req = this.requestFromForm()
    if (req.force) {
      const ok = await confirmAction(
        `Make ${req.ref} the active pin of ${req.name} at once? New jobs then use it before the workers have the image, and wait until a worker has pulled it.`,
        { title: 'Promote without waiting?', okLabel: 'Promote now' },
      )
      if (!ok) return
    }
    let outcome = ''
    const ok = await this.#save.run('The image alias could not be set.', async () => {
      outcome = (await api.setImageAlias(req)).outcome
    })
    if (!ok || !this.isActive()) return
    this.#result = outcomeText(req.name, outcome)
    this.#reset()
    void this.#aliases.reload()
    void this.#rollouts.reload()
  }

  render(): void {
    const scope = this.#scope()
    this.#aliases.sync(scope)
    this.#rollouts.sync(scope)

    const listError = this.#aliases.state.error
    const refused = isRefusal(listError)
    show(this.#refused, refused)
    show(this.#content, !refused && this.#aliases.state.loaded)
    this.#loadError.set(!refused && listError ? describeError(listError, 'The image aliases could not be loaded.') : undefined)
    const rolloutError = this.#rollouts.state.error
    show(this.#rolloutError, rolloutError && !refused)
    setText(this.#rolloutError, rolloutError ? `Rollout state: ${describeError(rolloutError, 'it could not be loaded.')}` : '')
    if (refused || !this.#aliases.state.loaded) return

    // Form.
    const editing = this.#editing
    setText(this.#formTitle, editing ? `Change alias ${editing.name}` : this.#isProject() ? 'Set a project alias' : 'Set an alias')
    show(this.#cancel, editing)
    this.#kindSelect.update(KIND_CHOICES, this.#kind)
    const problem = this.#problem()
    const typed = Boolean(this.#name.value.trim() || this.#ref.value.trim())
    this.#formError.set(this.#save.error ?? (typed ? problem : undefined))
    show(this.#formResult, this.#result && !this.#save.error && !typed)
    setText(this.#formResult, this.#result ?? '')
    this.#submit.disabled = this.#save.busy || Boolean(problem)
    setButtonText(this.#submit, this.#save.busy ? 'Saving…' : editing ? 'Save alias' : 'Set alias')

    // Table.
    const aliases = this.#aliases.state.data?.aliases ?? []
    const rollouts = new Map<string, ImageAliasRollout>()
    for (const r of this.#rollouts.state.data?.rollouts ?? []) rollouts.set(r.alias.aliasId, r)
    show(this.#tableWrap, aliases.length > 0)
    show(this.#none, aliases.length === 0)
    const items: AliasItem[] = []
    for (const alias of aliases) {
      items.push({ kind: 'alias', alias })
      if (this.#expanded.has(alias.aliasId)) items.push({ kind: 'rollout', alias })
    }
    syncKeyed(
      this.#tbody,
      items,
      (item) => `${item.kind}:${item.alias.aliasId}`,
      (item) => (item.kind === 'alias' ? this.#createRow(item.alias) : this.#createRolloutRow()),
      (row, item) => {
        if (item.kind === 'alias') this.#updateRow(row as HTMLTableRowElement, item.alias, rollouts.get(item.alias.aliasId))
        else this.#rolloutRows.get(row)!.update(item.alias, rollouts.get(item.alias.aliasId), this.#rollouts.state.loaded)
      },
    )
  }

  #createRow(alias: ImageAliasSummary): HTMLTableRowElement {
    const action = new Action(() => this.invalidate())
    const error = new ActionError()
    const state: AliasRowState = { data: alias, action, error, rollout: null!, edit: null!, override: null!, remove: null! }
    state.rollout = button('Rollout', {
      small: true,
      variant: 'flat',
      ariaLabel: `Rollout of alias ${alias.name}`,
      onClick: () => {
        const id = state.data.aliasId
        if (this.#expanded.has(id)) this.#expanded.delete(id)
        else this.#expanded.add(id)
        this.invalidate()
      },
    })
    state.edit = button('Change', { small: true, ariaLabel: `Change alias ${alias.name}`, onClick: () => this.#edit(state.data) })
    state.override = button('Override', {
      small: true,
      ariaLabel: `Override alias ${alias.name} for this project`,
      title: 'Make a project alias with the same name. It replaces the organization alias for the jobs of this project.',
      onClick: () => this.#edit(state.data, true),
    })
    state.remove = button('Delete', {
      small: true,
      variant: 'danger',
      ariaLabel: `Delete alias ${alias.name}`,
      onClick: async () => {
        const name = state.data.name
        const ok = await confirmAction(
          `Delete image alias "${name}"? Its images leave the worker image sets at once. A job that waits and uses the alias can then wait without a worker.`,
          { okLabel: 'Delete alias' },
        )
        if (!ok) return
        const req = this.projectId ? { organization: this.organization, projectId: this.projectId, name } : { organization: this.organization, name }
        const done = await action.run('The image alias could not be deleted.', () => api.deleteImageAlias(req))
        if (done && this.isActive()) {
          if (this.#editing?.aliasId === state.data.aliasId) this.#reset()
          this.#expanded.delete(state.data.aliasId)
          void this.#aliases.reload()
          void this.#rollouts.reload()
        }
      },
    })
    const row = h(
      'tr',
      { class: 'image-alias-row' },
      h('td', null, h('div', { class: 'mono alias-name' }), h('div', { class: 'meta alias-scope' })),
      h('td', { class: 'meta alias-kind' }),
      h('td', { class: 'meta mono alias-platform' }),
      h('td', { class: 'meta mono alias-chars' }),
      h('td', null, h('div', { class: 'mono image-ref alias-active' }), h('div', { class: 'meta mono alias-active-digest' })),
      h('td', null, h('div', { class: 'mono image-ref alias-pending' }), h('div', { class: 'meta alias-progress' })),
      h('td', null, h('div', { class: 'row' }, state.rollout, state.edit, state.override, state.remove), error.el),
    )
    this.#rows.set(row, state)
    return row
  }

  #updateRow(row: HTMLTableRowElement, alias: ImageAliasSummary, rollout: ImageAliasRollout | undefined): void {
    const state = this.#rows.get(row)!
    state.data = alias
    const own = this.#own(alias)
    const q = (cls: string): HTMLElement => row.querySelector(cls)!
    setText(q('.alias-name'), alias.name)
    setText(q('.alias-scope'), own ? alias.scope : `${alias.scope} (inherited)`)
    setText(q('.alias-kind'), alias.kind)
    setText(q('.alias-platform'), `${alias.os}/${alias.arch}`)
    setText(q('.alias-chars'), characteristicsText(alias.characteristics) || '—')
    setText(q('.alias-active'), alias.activeRef)
    setAttr(q('.alias-active'), 'title', alias.activeRef)
    setText(q('.alias-active-digest'), `${shortDigest(alias.activeDigest)} · ${relativeTime(alias.updatedAt)}`)
    setText(q('.alias-pending'), alias.pendingRef ?? '—')
    setAttr(q('.alias-pending'), 'title', alias.pendingRef ?? null)
    const progress = rolloutText(alias, rollout)
    const since = alias.pendingSince ? ` Pending since ${relativeTime(alias.pendingSince)}.` : ''
    setText(q('.alias-progress'), progress ? `${progress}${since}` : '')
    show(q('.alias-progress'), progress)
    // A project view has no rollout of an inherited organization alias.
    show(state.rollout, own)
    setButtonText(state.rollout, this.#expanded.has(alias.aliasId) ? 'Hide rollout' : 'Rollout')
    setAttr(state.rollout, 'aria-expanded', String(this.#expanded.has(alias.aliasId)))
    show(state.edit, own)
    show(state.remove, own)
    show(state.override, !own && this.#isProject())
    for (const b of [state.edit, state.remove, state.override]) b.disabled = state.action.busy
    state.error.set(state.action.error)
  }

  #createRolloutRow(): HTMLTableRowElement {
    const view = new RolloutView()
    this.#rolloutRows.set(view.el, view)
    return view.el
  }
}

/** The serving workers of one alias and their image state. */
class RolloutView {
  readonly el: HTMLTableRowElement
  readonly #summary: HTMLParagraphElement
  readonly #tbody: HTMLTableSectionElement
  readonly #table: HTMLElement
  readonly #none: HTMLElement
  readonly #problemsWrap: HTMLElement
  readonly #problems: HTMLUListElement
  readonly #poolsWrap: HTMLElement
  readonly #pools: HTMLUListElement

  constructor() {
    this.#summary = h('p', { class: 'meta rollout-summary' })
    this.#problems = h('ul', { class: 'rollout-problem-list' })
    this.#problemsWrap = h(
      'div',
      { class: 'rollout-problems', role: 'alert', hidden: true },
      h('strong', null, 'Problems that the alias owner must fix:'),
      this.#problems,
    )
    this.#pools = h('ul', { class: 'rollout-pool-list' })
    this.#poolsWrap = h(
      'div',
      { class: 'rollout-other-pools', hidden: true },
      h('p', { class: 'meta' }, 'Other pools that serve this alias (counts only):'),
      this.#pools,
    )
    this.#tbody = h('tbody')
    this.#table = h(
      'table',
      { class: 'table rollout-workers' },
      h(
        'thead',
        null,
        h(
          'tr',
          null,
          h('th', null, 'Worker'),
          h('th', null, 'Online'),
          h('th', null, 'Image status'),
          h('th', null, 'Image set'),
          h('th', null, 'Active image'),
          h('th', null, 'Pending image'),
          h('th', null, 'Ready'),
        ),
      ),
      this.#tbody,
    )
    this.#none = h('p', { class: 'meta' }, 'No image-aware worker serves this alias.')
    this.el = h('tr', { class: 'rollout-row' }, h('td', { colspan: '7' }, h('div', { class: 'rollout-panel' }, this.#summary, this.#problemsWrap, this.#table, this.#none, this.#poolsWrap)))
  }

  update(alias: ImageAliasSummary, rollout: ImageAliasRollout | undefined, loaded: boolean): void {
    if (!rollout) {
      setText(this.#summary, loaded ? 'No rollout state for this alias.' : 'Loading the rollout state…')
      show(this.#table, false)
      show(this.#none, false)
      show(this.#problemsWrap, false)
      show(this.#poolsWrap, false)
      return
    }
    const workers = rollout.workers
    const otherPools = rollout.otherPools ?? []
    const problems = [...new Set(rollout.problems ?? [])]
    show(this.#problemsWrap, problems.length > 0)
    syncKeyed(
      this.#problems,
      problems,
      (p) => p,
      () => h('li'),
      (li, p) => setText(li, p),
    )
    show(this.#poolsWrap, otherPools.length > 0)
    syncKeyed(
      this.#pools,
      otherPools,
      (p) => p.poolName,
      () => h('li', { class: 'rollout-pool' }),
      (li, p) => setText(li, rolloutPoolText(p)),
    )
    const head = alias.pendingRef
      ? rolloutText(alias, rollout)
      : `No pending pin. ${rollout.onlineWorkers} online ${rollout.onlineWorkers === 1 ? 'worker serves' : 'workers serve'} this alias.`
    setText(this.#summary, head)
    show(this.#table, workers.length > 0)
    show(this.#none, workers.length === 0 && otherPools.length === 0)
    syncKeyed(
      this.#tbody,
      workers,
      (w) => w.workerId,
      () =>
        h(
          'tr',
          null,
          h('td', null, h('a', { class: 'rollout-worker' }), h('div', { class: 'meta mono truncate rollout-worker-id' })),
          h('td', { class: 'rollout-online' }),
          h('td', null, h('span', { class: 'rollout-status' }), h('div', { class: 'meta rollout-detail' }), h('ul', { class: 'meta rollout-failures', hidden: true })),
          h('td', { class: 'meta rollout-version' }),
          h('td', { class: 'rollout-has-active' }),
          h('td', { class: 'rollout-has-pending' }),
          h('td', { class: 'rollout-ready' }),
        ),
      (row, w) => updateRolloutWorker(row, w, Boolean(alias.pendingRef)),
    )
  }
}

function yesNo(value: boolean): string {
  return value ? 'yes' : 'no'
}

function updateRolloutWorker(row: Element, w: ImageRolloutWorker, pending: boolean): void {
  const q = (cls: string): HTMLElement => row.querySelector(cls)!
  const link = q('.rollout-worker') as HTMLAnchorElement
  setText(link, w.hostname || w.workerId)
  setAttr(link, 'href', href(`/workers/${encodeURIComponent(w.workerId)}`))
  setText(q('.rollout-worker-id'), w.workerId)
  setText(q('.rollout-online'), w.online ? 'online' : 'offline')
  const status = q('.rollout-status')
  setText(status, imageStatusText(w.imageStatus))
  setAttr(status, 'class', `rollout-status ${imageStatusClass(w.imageStatus)}`)
  setText(q('.rollout-detail'), w.imageStatusDetail ?? '')
  show(q('.rollout-detail'), w.imageStatusDetail)
  setText(q('.rollout-version'), imageVersionText(w.imageSetVersion, w.imageReportedVersion))
  setText(q('.rollout-has-active'), yesNo(w.hasActive))
  setText(q('.rollout-has-pending'), pending ? yesNo(w.hasPending) : '—')
  setText(q('.rollout-ready'), pending ? yesNo(w.ready) : '—')
  const failures = w.failed ?? []
  const list = q('.rollout-failures')
  show(list, failures.length > 0)
  syncKeyed(
    list,
    failures,
    (f) => f.digest,
    () => h('li', null, h('span', { class: 'mono image-failure-digest' }), ' ', h('span', { class: 'image-failure-text' })),
    (li, f) => {
      const digest = li.querySelector('.image-failure-digest')!
      setText(digest, shortDigest(f.digest))
      setAttr(digest, 'title', f.digest)
      setText(li.querySelector('.image-failure-text')!, imageFailureText(f))
      setAttr(li, 'class', f.permanent ? 'image-failure-permanent' : '')
    },
  )
}

export const imageAliasesTag = 'rc-image-aliases'
define(imageAliasesTag, ImageAliasesPanel)

export function imageAliasesPanel(organization: string, projectId = ''): ImageAliasesPanel {
  const el = document.createElement(imageAliasesTag) as ImageAliasesPanel
  el.organization = organization
  el.projectId = projectId
  return el
}
