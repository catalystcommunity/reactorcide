import type { PushButton, TextField } from '@catalystcommunity/controls'
import { api } from '~/api/client.ts'
import type { EnumChoice, ListOidcKeysResponse, OidcAudienceClaims, OidcSettingsResponse, OidcSigningKey } from '~/api/csilapi/types.gen.ts'
import { ChoiceSelect } from '~/components/ChoiceInput.ts'
import { field } from '~/components/Field.ts'
import { Action, ActionError } from '~/components/ManagementPage.ts'
import { relativeTime } from '~/components/States.ts'
import { button, confirmAction, textField } from '~/ui/controls.ts'
import { h, setText, show, syncKeyed, uid } from '~/ui/dom.ts'
import { RcElement, define } from '~/ui/element.ts'
import { Loader } from '~/ui/load.ts'

/**
 * The job OIDC issuer (docs/oidc.md). A job asks the coordinator for a
 * short-lived OIDC token for one audience; a cloud provider trusts the
 * issuer and gives the job a role. The settings turn the issuer on or off
 * for an organization or a project, and add claims for an audience. The
 * signing keys are instance-wide: a global admin rotates and retires them.
 */

const ORG_ISSUER: EnumChoice[] = [
  { value: 'allow', label: 'Allow', description: 'Jobs of this organization can get OIDC tokens.' },
  { value: 'deny', label: 'Deny', description: 'No job of this organization gets an OIDC token.' },
]
const PROJECT_ISSUER: EnumChoice[] = [
  { value: 'inherit', label: 'Inherit from organization', description: 'Use the value of the organization.' },
  ...ORG_ISSUER.map((c) => ({ ...c, description: c.description.replace('this organization', 'this project') })),
]

/** One claim line: "name=value". */
export function parseClaims(text: string): { claims: { name: string; value: string }[]; error?: string } {
  const claims: { name: string; value: string }[] = []
  for (const raw of text.split('\n')) {
    const line = raw.trim()
    if (!line) continue
    const cut = line.indexOf('=')
    if (cut <= 0) return { claims, error: `"${line}" is not NAME=VALUE.` }
    claims.push({ name: line.slice(0, cut).trim(), value: line.slice(cut + 1).trim() })
  }
  return { claims }
}

class AudienceRow {
  readonly key = uid('audience')
  readonly el: HTMLDivElement
  readonly audience: TextField
  readonly claims: ReturnType<typeof textField>

  constructor(onRemove: (row: AudienceRow) => void) {
    this.audience = textField({ mono: true, placeholder: 'sts.amazonaws.com' })
    this.claims = textField({ mono: true, placeholder: 'team=payments' })
    this.el = h(
      'div',
      { class: 'sub-card audience-row' },
      h('div', { class: 'sub-card-header' }, h('h4', { class: 'section-title' }, 'Audience'), button('Remove audience', { small: true, variant: 'danger', onClick: () => onRemove(this) })),
      h(
        'div',
        { class: 'form-row' },
        field({ label: 'Audience', required: true, hint: 'The aud claim that a job asks for.', control: this.audience }).el,
        field({ label: 'Extra claims', hint: 'NAME=VALUE, separated by commas. Reserved claims (sub, iss, aud, ...) cannot be set.', control: this.claims }).el,
      ),
    )
  }

  set(entry: OidcAudienceClaims): void {
    this.audience.value = entry.audience
    this.claims.value = entry.claims.map((c) => `${c.name}=${c.value}`).join(', ')
  }

  value(): { entry: OidcAudienceClaims; error?: string } {
    const parsed = parseClaims(this.claims.value.split(',').join('\n'))
    return { entry: { audience: this.audience.value.trim(), claims: parsed.claims }, error: parsed.error }
  }
}

/** The OIDC settings of an organization (no projectId) or a project. */
export class OidcSettingsCard extends RcElement {
  organization = ''
  projectId: string | undefined

  #load = new Loader<string, OidcSettingsResponse>(this, (key) => {
    const [organization, projectId] = JSON.parse(key) as [string, string | null]
    return api.getOidcSettings(projectId ? { organization, projectId } : { organization })
  })
  #action = new Action(() => this.invalidate())
  #error = new ActionError()
  #loading!: HTMLParagraphElement
  #body!: HTMLDivElement
  #state!: HTMLParagraphElement
  #notConfigured!: HTMLDivElement
  #issuer!: ChoiceSelect
  #issuerValue = 'allow'
  #rows: AudienceRow[] = []
  #rowList!: HTMLDivElement
  #noRows!: HTMLParagraphElement
  #add!: PushButton
  #save!: PushButton
  #saved!: HTMLDivElement
  #readonly!: HTMLParagraphElement
  #seeded = ''

  setScope(organization: string, projectId?: string): void {
    if (this.organization === organization && this.projectId === projectId) return
    this.organization = organization
    this.projectId = projectId
    this.invalidate()
  }

  #key(): string | undefined {
    return this.organization ? JSON.stringify([this.organization, this.projectId ?? null]) : undefined
  }

  connectedCallback(): void {
    this.classList.add('card')
    super.connectedCallback()
  }

  protected build(): void {
    const project = Boolean(this.projectId)
    this.#loading = h('p', { class: 'meta' }, 'Loading…')
    this.#state = h('p', { class: 'oidc-state' })
    this.#notConfigured = h('div', { class: 'alert alert-info', hidden: true }, 'This coordinator has no OIDC issuer (REACTORCIDE_OIDC_ISSUER_URL is not set). The settings are kept, but no job gets a token.')
    this.#readonly = h('p', { class: 'meta', hidden: true }, 'Only an organization administrator can change these settings.')
    this.#issuer = new ChoiceSelect({ choices: project ? PROJECT_ISSUER : ORG_ISSUER, value: project ? 'inherit' : 'allow', onChange: (v) => { this.#issuerValue = v; this.invalidate() } })
    this.#rowList = h('div')
    this.#noRows = h('p', { class: 'meta' }, 'No extra claims.')
    this.#add = button('Add audience', { small: true, onClick: () => this.#addRow() })
    this.#add.classList.add('rc-add-row')
    this.#save = button('Save OIDC settings', { variant: 'primary', onClick: () => void this.#store() })
    this.#saved = h('div', { class: 'alert alert-info', role: 'status', hidden: true }, 'OIDC settings saved.')
    this.#body = h(
      'div',
      { hidden: true },
      this.#notConfigured,
      this.#readonly,
      this.#saved,
      this.#state,
      field({ label: 'OIDC tokens for jobs', hint: project ? 'A project can turn tokens off, or follow the organization.' : 'Projects follow this value unless they set their own.', control: this.#issuer.el }).el,
      h('h4', { class: 'section-title' }, 'Extra claims for each audience'),
      this.#noRows,
      this.#rowList,
      this.#add,
      h('div', { class: 'row' }, this.#save),
    )
    this.append(
      h('h3', { class: 'section-title' }, 'OIDC tokens'),
      h('p', { class: 'meta' }, 'A job can get a short-lived OIDC token from Reactorcide and use it to get cloud credentials, with no static secret.'),
      this.#error.el,
      this.#loading,
      this.#body,
    )
  }

  #newRow(): AudienceRow {
    return new AudienceRow((row) => {
      this.#rows = this.#rows.filter((r) => r !== row)
      this.invalidate()
    })
  }

  #addRow(): void {
    const row = this.#newRow()
    this.#rows = [...this.#rows, row]
    this.invalidate()
    row.audience.focus()
  }

  async #store(): Promise<void> {
    show(this.#saved, false)
    const entries: OidcAudienceClaims[] = []
    for (const row of this.#rows) {
      const { entry, error } = row.value()
      if (error || !entry.audience) {
        this.#action.error = error ?? 'Each audience needs a name.'
        this.invalidate()
        return
      }
      entries.push(entry)
    }
    const request = { organization: this.organization, ...(this.projectId ? { projectId: this.projectId } : {}), issuer: this.#issuerValue, audienceClaims: entries }
    let response: OidcSettingsResponse | undefined
    const ok = await this.#action.run('The OIDC settings could not be saved.', async () => {
      response = await api.putOidcSettings(request)
    })
    if (!ok || !response || !this.isActive()) return
    this.#seeded = ''
    this.#load.mutate(response)
    show(this.#saved, true)
  }

  #seed(data: OidcSettingsResponse): void {
    const key = JSON.stringify([this.#key(), data.issuer, data.audienceClaims])
    if (key === this.#seeded) return
    this.#seeded = key
    this.#issuerValue = data.issuer
    this.#rows = data.audienceClaims.map((entry) => {
      const row = this.#newRow()
      row.set(entry)
      return row
    })
  }

  render(): void {
    this.#load.sync(this.#key())
    const data = this.#load.state.data
    this.#error.set(this.#action.error ?? this.#load.state.error?.message)
    show(this.#loading, !data && !this.#load.state.error)
    show(this.#body, data)
    if (!data) return
    this.#seed(data)
    this.#issuer.update(this.projectId ? PROJECT_ISSUER : ORG_ISSUER, this.#issuerValue)
    this.#issuer.el.disabled = !data.canManage || this.#action.busy
    show(this.#notConfigured, !data.issuerConfigured)
    show(this.#readonly, !data.canManage)
    show(this.#save, data.canManage)
    show(this.#add, data.canManage)
    this.#save.disabled = this.#action.busy
    setText(this.#state, `In force now: ${data.effectiveEnabled ? 'jobs can get tokens' : 'no tokens'}${data.issuerUrl ? ` (issuer ${data.issuerUrl})` : ''}.`)
    show(this.#noRows, this.#rows.length === 0)
    syncKeyed(this.#rowList, this.#rows, (r) => r.key, (r) => r.el)
  }
}

type KeyRow = HTMLTableRowElement & { key?: OidcSigningKey }

/** The signing keys of the issuer, for a global admin. */
export class OidcKeysCard extends RcElement {
  #load = new Loader<string, ListOidcKeysResponse>(this, () => api.listOidcKeys({}))
  #action = new Action(() => this.invalidate())
  #error = new ActionError()
  #loading!: HTMLParagraphElement
  #body!: HTMLDivElement
  #none!: HTMLDivElement
  #issuer!: HTMLParagraphElement
  #rows!: HTMLTableSectionElement
  #rotate!: PushButton
  #rotateNow!: PushButton

  connectedCallback(): void {
    this.classList.add('card')
    super.connectedCallback()
  }

  protected build(): void {
    this.#loading = h('p', { class: 'meta' }, 'Loading…')
    this.#none = h('div', { class: 'alert alert-info', hidden: true }, 'This coordinator has no OIDC issuer. Set REACTORCIDE_OIDC_ISSUER_URL to turn it on.')
    this.#issuer = h('p', { class: 'meta mono' })
    this.#rows = h('tbody')
    this.#rotate = button('Rotate', { onClick: () => void this.#rotateKey(false) })
    this.#rotateNow = button('Rotate now', { variant: 'danger', onClick: () => void this.#rotateKey(true) })
    this.#body = h(
      'div',
      { hidden: true },
      this.#issuer,
      h(
        'div',
        { class: 'scroll-x' },
        h('table', { class: 'table oidc-keys-table' }, h('thead', null, h('tr', null, h('th', null, 'Key ID'), h('th', null, 'Algorithm'), h('th', null, 'State'), h('th', null, 'Signs from'), h('th', null, 'Retires'), h('th', null, h('span', { class: 'sr-only' }, 'Actions')))), this.#rows),
      ),
      h('p', { class: 'meta' }, 'Rotate publishes a new key first and signs with it after the publish delay, so verifiers see it before it is used. Rotate now signs with the new key at once.'),
      h('div', { class: 'row' }, this.#rotate, this.#rotateNow),
    )
    this.append(h('h3', { class: 'section-title' }, 'OIDC signing keys'), this.#error.el, this.#loading, this.#none, this.#body)
  }

  async #rotateKey(immediate: boolean): Promise<void> {
    if (immediate && !(await confirmAction('Sign with a new key at once? A verifier that has not read the new key yet refuses tokens until it reads the JWKS again.', { okLabel: 'Rotate now' }))) return
    let response: ListOidcKeysResponse | undefined
    const ok = await this.#action.run('The key could not be rotated.', async () => {
      response = await api.rotateOidcKey(immediate ? { immediate: true } : {})
    })
    if (ok && response && this.isActive()) this.#load.mutate(response)
  }

  async #retire(key: OidcSigningKey): Promise<void> {
    if (!(await confirmAction(`Retire key ${key.kid}? A token it signed fails verification after this.`, { okLabel: 'Retire' }))) return
    let response: ListOidcKeysResponse | undefined
    const ok = await this.#action.run('The key could not be retired.', async () => {
      response = await api.retireOidcKey({ kid: key.kid })
    })
    if (ok && response && this.isActive()) this.#load.mutate(response)
  }

  #createRow(): KeyRow {
    const tr = h('tr') as KeyRow
    const retire = button('Retire', { small: true, variant: 'danger', onClick: () => void this.#retire(tr.key!) })
    retire.classList.add('act-retire')
    tr.append(h('td', { class: 'mono k-kid' }), h('td', { class: 'k-alg' }), h('td', { class: 'k-state' }), h('td', { class: 'meta k-from' }), h('td', { class: 'meta k-retires' }), h('td', null, retire))
    return tr
  }

  #updateRow(tr: KeyRow, key: OidcSigningKey): void {
    tr.key = key
    setText(tr.querySelector('.k-kid')!, key.kid)
    setText(tr.querySelector('.k-alg')!, key.algorithm)
    setText(tr.querySelector('.k-state')!, key.state)
    setText(tr.querySelector('.k-from')!, relativeTime(key.activatesAt))
    setText(tr.querySelector('.k-retires')!, key.retiresAt ? relativeTime(key.retiresAt) : '—')
    const retire = tr.querySelector<PushButton>('.act-retire')!
    show(retire, key.state !== 'retired' && key.state !== 'signing')
    retire.disabled = this.#action.busy
  }

  render(): void {
    this.#load.sync('keys')
    const data = this.#load.state.data
    this.#error.set(this.#action.error ?? this.#load.state.error?.message)
    show(this.#loading, !data && !this.#load.state.error)
    show(this.#none, data && !data.issuerConfigured)
    show(this.#body, data?.issuerConfigured)
    if (!data) return
    setText(this.#issuer, data.issuer ? `Issuer: ${data.issuer}` : '')
    syncKeyed(this.#rows, data.keys, (k) => k.kid, () => this.#createRow(), (tr, k) => this.#updateRow(tr, k))
    this.#rotate.disabled = this.#action.busy
    this.#rotateNow.disabled = this.#action.busy
  }
}

export const oidcSettingsCardTag = 'rc-oidc-settings-card'
define(oidcSettingsCardTag, OidcSettingsCard)
export const oidcKeysCardTag = 'rc-oidc-keys-card'
define(oidcKeysCardTag, OidcKeysCard)

export function oidcSettingsCard(organization: string, projectId?: string): OidcSettingsCard {
  const el = document.createElement(oidcSettingsCardTag) as OidcSettingsCard
  el.organization = organization
  el.projectId = projectId
  return el
}

export function oidcKeysCard(): OidcKeysCard {
  return document.createElement(oidcKeysCardTag) as OidcKeysCard
}
