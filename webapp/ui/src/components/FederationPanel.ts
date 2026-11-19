import type { ComboBox, PushButton, TextArea, TextField } from '@catalystcommunity/controls'
import { api } from '~/api/client.ts'
import type { FederationConditionEntry, FederationIssuerEntry, FederationMappingEntry } from '~/api/csilapi/types.gen.ts'
import { field } from '~/components/Field.ts'
import { Action, ActionError } from '~/components/ManagementPage.ts'
import { button, clearField, comboBox, confirmAction, setButtonText, setComboValue, setOptions, textArea, textField } from '~/ui/controls.ts'
import { h, setText, show, syncKeyed } from '~/ui/dom.ts'
import { RcElement, define } from '~/ui/element.ts'
import { Loader } from '~/ui/load.ts'

/**
 * Inbound federation of an organization (docs/federation.md).
 *
 * An org admin registers a trusted external OIDC issuer (for example the
 * OIDC issuer of another CI system or a cloud workload identity), and maps
 * claim conditions to a service principal. A workload of that issuer then
 * exchanges its own token for a short-lived Reactorcide token: it needs no
 * static Reactorcide token. The issued token acts with the capabilities of
 * the mapping intersected with the roles of the principal.
 */

/** Parses one condition line: "claim=value" (equals) or "claim~glob" (glob). */
export function parseCondition(line: string): FederationConditionEntry | undefined {
  const text = line.trim()
  if (!text) return undefined
  const eq = text.indexOf('=')
  const glob = text.indexOf('~')
  if (eq > 0 && (glob < 0 || eq < glob)) return { claim: text.slice(0, eq).trim(), op: 'equals', value: text.slice(eq + 1).trim() }
  if (glob > 0) return { claim: text.slice(0, glob).trim(), op: 'glob', value: text.slice(glob + 1).trim() }
  return undefined
}

export function conditionText(condition: FederationConditionEntry): string {
  return `${condition.claim}${condition.op === 'glob' ? '~' : '='}${condition.value}`
}

function splitList(text: string): string[] {
  return text
    .split(/[\s,]+/)
    .map((value) => value.trim())
    .filter(Boolean)
}

export class FederationPanel extends RcElement {
  orgId = ''

  #data = new Loader(this, (orgId: string) => api.listFederationIssuers({ orgId }))
  #principals = new Loader(this, (orgId: string) => api.listPrincipals({ orgId, kind: 'service' }))
  #action = new Action(() => this.invalidate())
  #formError: string | undefined

  #error!: ActionError
  #issuerWrap!: HTMLElement
  #issuerBody!: HTMLTableSectionElement
  #noIssuers!: HTMLElement
  #mappingWrap!: HTMLElement
  #mappingBody!: HTMLTableSectionElement
  #issuerName!: TextField
  #issuerURL!: TextField
  #jwksURL!: TextField
  #audiences!: TextField
  #algorithms!: TextField
  #addIssuer!: PushButton
  #mappingSection!: HTMLElement
  #mappingIssuer!: ComboBox
  #mappingName!: TextField
  #mappingPrincipal!: ComboBox
  #conditions!: TextArea
  #capabilities!: TextField
  #ttl!: TextField
  #addMapping!: PushButton

  protected build(): void {
    this.classList.add('card', 'stack', 'federation-panel')
    this.#error = new ActionError()
    this.#issuerBody = h('tbody')
    this.#issuerWrap = h(
      'div',
      { class: 'scroll-x' },
      h(
        'table',
        { class: 'table federation-issuers' },
        h('thead', null, h('tr', null, h('th', null, 'Issuer'), h('th', null, 'Audiences'), h('th', null, 'Algorithms'), h('th', null, 'Status'), h('th', null, ''))),
        this.#issuerBody,
      ),
    )
    this.#noIssuers = h('div', { class: 'empty' }, 'No federated issuers. Workloads use static tokens only.')
    this.#mappingBody = h('tbody')
    this.#mappingWrap = h(
      'div',
      { class: 'scroll-x' },
      h(
        'table',
        { class: 'table federation-mappings' },
        h(
          'thead',
          null,
          h('tr', null, h('th', null, 'Mapping'), h('th', null, 'Principal'), h('th', null, 'Conditions'), h('th', null, 'Capabilities'), h('th', null, 'Status'), h('th', null, '')),
        ),
        this.#mappingBody,
      ),
    )

    this.#issuerName = textField({ mono: true, onInput: () => this.invalidate() })
    this.#issuerURL = textField({ mono: true, type: 'url', placeholder: 'https://token.actions.githubusercontent.com', onInput: () => this.invalidate() })
    this.#jwksURL = textField({ mono: true, type: 'url' })
    this.#audiences = textField({ mono: true, placeholder: 'reactorcide', onInput: () => this.invalidate() })
    this.#algorithms = textField({ mono: true, placeholder: 'RS256, ES256' })
    this.#addIssuer = button('Add issuer', { variant: 'primary', type: 'submit' })

    this.#mappingIssuer = comboBox({ ariaLabel: 'Issuer', onChange: () => this.invalidate() })
    this.#mappingName = textField({ mono: true, onInput: () => this.invalidate() })
    this.#mappingPrincipal = comboBox({ ariaLabel: 'Service principal', onChange: () => this.invalidate() })
    this.#conditions = textArea({ mono: true, rows: 3, placeholder: 'sub~repo:acme/app:ref:refs/heads/*\nrepository=acme/app', onInput: () => this.invalidate() })
    this.#capabilities = textField({ mono: true, placeholder: 'jobs:submit, jobs:read', onInput: () => this.invalidate() })
    this.#ttl = textField({ mono: true, placeholder: '900' })
    this.#addMapping = button('Add mapping', { variant: 'primary', type: 'submit' })

    this.#mappingSection = h(
      'div',
      { class: 'stack' },
      h('h4', null, 'Claim mappings'),
      h(
        'p',
        { class: 'meta' },
        'Exactly one active mapping must match a token. Every condition must match, and one condition must be on sub. A mapping never gives every capability.',
      ),
      this.#mappingWrap,
      h(
        'form',
        { class: 'stack', onSubmit: (event: Event) => void this.#onAddMapping(event) },
        h(
          'div',
          { class: 'form-row' },
          field({ label: 'Issuer', required: true, control: this.#mappingIssuer, labelTarget: this.#mappingIssuer }).el,
          field({ label: 'Name', required: true, hint: 'Lowercase letters, digits and dashes.', control: this.#mappingName }).el,
          field({ label: 'Service principal', required: true, control: this.#mappingPrincipal, labelTarget: this.#mappingPrincipal }).el,
        ),
        h(
          'div',
          { class: 'form-row' },
          field({
            label: 'Conditions',
            required: true,
            hint: 'One per line: claim=value or claim~glob.',
            tooltip: 'In a glob, * matches any run of characters (also / and :), and ? matches one character.',
            control: this.#conditions,
          }).el,
          field({ label: 'Capabilities', required: true, hint: 'Comma-separated, for example jobs:submit.', control: this.#capabilities }).el,
          field({ label: 'Token lifetime (seconds)', hint: '60 to 3600. Default 900.', control: this.#ttl }).el,
        ),
        this.#addMapping,
      ),
    )

    this.append(
      h('h3', { class: 'section-title' }, 'Federated issuers'),
      h(
        'p',
        { class: 'meta' },
        'A workload of a trusted OIDC issuer exchanges its own token at POST /api/v1/federation/token for a short-lived token of a service principal. It needs no static Reactorcide token.',
      ),
      this.#error.el,
      this.#issuerWrap,
      this.#noIssuers,
      h(
        'form',
        { class: 'stack', onSubmit: (event: Event) => void this.#onAddIssuer(event) },
        h(
          'div',
          { class: 'form-row' },
          field({ label: 'Name', required: true, hint: 'Lowercase letters, digits and dashes.', control: this.#issuerName }).el,
          field({ label: 'Issuer URL', required: true, hint: 'The exact iss value. HTTPS.', control: this.#issuerURL }).el,
          field({ label: 'JWKS URL', hint: 'Empty: read it from the discovery document.', control: this.#jwksURL }).el,
        ),
        h(
          'div',
          { class: 'form-row' },
          field({ label: 'Audiences', required: true, hint: 'Comma-separated. A token must name one in aud.', control: this.#audiences }).el,
          field({ label: 'Algorithms', hint: 'RS256, ES256, EdDSA. Default RS256 and ES256.', control: this.#algorithms }).el,
        ),
        this.#addIssuer,
      ),
      this.#mappingSection,
    )
  }

  async #run(message: string, work: () => Promise<unknown>): Promise<boolean> {
    this.#formError = undefined
    const ok = await this.#action.run(message, work)
    if (ok && this.isActive()) void this.#data.reload()
    return ok
  }

  async #onAddIssuer(event: Event): Promise<void> {
    event.preventDefault()
    const name = this.#issuerName.value.trim()
    const issuerUrl = this.#issuerURL.value.trim()
    const audiences = splitList(this.#audiences.value)
    if (!name || !issuerUrl || audiences.length === 0) return
    const algorithms = splitList(this.#algorithms.value)
    const jwksUrl = this.#jwksURL.value.trim()
    const ok = await this.#run('The issuer could not be added.', () =>
      api.saveFederationIssuer({
        orgId: this.orgId,
        name,
        issuerUrl,
        audiences,
        algorithms: algorithms.length ? algorithms : undefined,
        jwksUrl: jwksUrl || undefined,
      }),
    )
    if (ok && this.isActive()) {
      for (const control of [this.#issuerName, this.#issuerURL, this.#jwksURL, this.#audiences, this.#algorithms]) clearField(control)
      this.invalidate()
    }
  }

  async #setIssuerStatus(issuer: FederationIssuerEntry, status: string): Promise<void> {
    if (status === 'disabled') {
      const confirmed = await confirmAction(`Disable issuer ${issuer.name}? Its tokens cannot be exchanged until you enable it.`, { okLabel: 'Disable', danger: true })
      if (!confirmed) return
    }
    await this.#run('The issuer could not be changed.', () =>
      api.saveFederationIssuer({
        orgId: this.orgId,
        issuerId: issuer.issuerId,
        name: issuer.name,
        issuerUrl: issuer.issuerUrl,
        jwksUrl: issuer.jwksUrl,
        audiences: issuer.audiences,
        algorithms: issuer.algorithms,
        clockSkewSeconds: issuer.clockSkewSeconds,
        status,
      }),
    )
  }

  async #deleteIssuer(issuer: FederationIssuerEntry): Promise<void> {
    const confirmed = await confirmAction(`Delete issuer ${issuer.name} and all its mappings?`, { okLabel: 'Delete', danger: true })
    if (!confirmed) return
    await this.#run('The issuer could not be deleted.', () => api.deleteFederationIssuer({ orgId: this.orgId, issuerId: issuer.issuerId }))
  }

  async #onAddMapping(event: Event): Promise<void> {
    event.preventDefault()
    const issuerId = this.#mappingIssuer.value
    const name = this.#mappingName.value.trim()
    const principalId = this.#mappingPrincipal.value
    const lines = this.#conditions.value.split('\n').filter((line) => line.trim())
    const conditions = lines.map(parseCondition)
    const capabilities = splitList(this.#capabilities.value)
    if (!issuerId || !name || !principalId || capabilities.length === 0 || lines.length === 0) return
    if (conditions.some((condition) => !condition)) {
      this.#formError = 'Each condition must be claim=value or claim~glob.'
      this.invalidate()
      return
    }
    const ttlText = this.#ttl.value.trim()
    const ttl = ttlText ? Number(ttlText) : undefined
    if (ttl !== undefined && (!Number.isInteger(ttl) || ttl < 60 || ttl > 3600)) {
      this.#formError = 'The token lifetime must be 60 to 3600 seconds.'
      this.invalidate()
      return
    }
    const ok = await this.#run('The mapping could not be added.', () =>
      api.saveFederationMapping({
        orgId: this.orgId,
        issuerId,
        name,
        principalId,
        conditions: conditions as FederationConditionEntry[],
        capabilities,
        tokenTtlSeconds: ttl,
      }),
    )
    if (ok && this.isActive()) {
      for (const control of [this.#mappingName, this.#conditions, this.#capabilities, this.#ttl]) clearField(control)
      this.invalidate()
    }
  }

  async #setMappingStatus(mapping: FederationMappingEntry, status: string): Promise<void> {
    await this.#run('The mapping could not be changed.', () =>
      api.saveFederationMapping({
        orgId: this.orgId,
        issuerId: mapping.issuerId,
        mappingId: mapping.mappingId,
        name: mapping.name,
        principalId: mapping.principalId,
        conditions: mapping.conditions,
        capabilities: mapping.capabilities,
        tokenTtlSeconds: mapping.tokenTtlSeconds,
        status,
      }),
    )
  }

  async #deleteMapping(mapping: FederationMappingEntry): Promise<void> {
    const confirmed = await confirmAction(`Delete mapping ${mapping.name}?`, { okLabel: 'Delete', danger: true })
    if (!confirmed) return
    await this.#run('The mapping could not be deleted.', () => api.deleteFederationMapping({ orgId: this.orgId, mappingId: mapping.mappingId }))
  }

  render(): void {
    this.#data.sync(this.orgId || undefined)
    this.#principals.sync(this.orgId || undefined)
    this.#error.set(this.#formError ?? this.#action.error ?? (this.#data.state.error ? 'Federation settings could not be loaded.' : undefined))
    const busy = this.#action.busy
    const issuers = this.#data.state.data?.issuers ?? []
    const mappings = this.#data.state.data?.mappings ?? []
    const issuerNames = new Map(issuers.map((issuer) => [issuer.issuerId, issuer.name]))

    show(this.#issuerWrap, issuers.length > 0)
    show(this.#noIssuers, this.#data.state.loaded && issuers.length === 0)
    show(this.#mappingSection, issuers.length > 0)
    show(this.#mappingWrap, mappings.length > 0)
    this.#addIssuer.disabled = busy || !this.#issuerName.value.trim() || !this.#issuerURL.value.trim() || splitList(this.#audiences.value).length === 0
    setButtonText(this.#addIssuer, busy ? 'Saving…' : 'Add issuer')
    this.#addMapping.disabled =
      busy ||
      !this.#mappingIssuer.value ||
      !this.#mappingName.value.trim() ||
      !this.#mappingPrincipal.value ||
      !this.#conditions.value.trim() ||
      splitList(this.#capabilities.value).length === 0

    setOptions(
      this.#mappingIssuer,
      issuers.map((issuer) => ({ value: issuer.issuerId, label: issuer.name, description: issuer.issuerUrl })),
    )
    if (!this.#mappingIssuer.value && issuers.length === 1) setComboValue(this.#mappingIssuer, issuers[0].issuerId)
    const services = (this.#principals.state.data?.principals ?? []).filter((p) => p.kind === 'service')
    setOptions(
      this.#mappingPrincipal,
      services.map((p) => ({ value: p.principalId, label: p.name, description: p.displayName })),
    )

    syncKeyed(
      this.#issuerBody,
      issuers,
      (issuer) => issuer.issuerId,
      (issuer) => {
        const toggle = button('', {
          small: true,
          onClick: () => {
            const current = this.#issuerOf(issuer.issuerId) ?? issuer
            void this.#setIssuerStatus(current, current.status === 'active' ? 'disabled' : 'active')
          },
        })
        toggle.classList.add('issuer-toggle')
        const remove = button('Delete', { small: true, variant: 'danger', ariaLabel: `Delete issuer ${issuer.name}`, onClick: () => void this.#deleteIssuer(issuer) })
        return h(
          'tr',
          null,
          h('td', null, h('div', { class: 'issuer-name' }), h('div', { class: 'meta mono truncate issuer-url' })),
          h('td', { class: 'mono issuer-audiences' }),
          h('td', { class: 'mono issuer-algorithms' }),
          h('td', { class: 'issuer-status' }),
          h('td', { class: 'row' }, toggle, remove),
        )
      },
      (row, issuer) => {
        setText(row.querySelector('.issuer-name')!, issuer.name)
        setText(row.querySelector('.issuer-url')!, issuer.issuerUrl)
        setText(row.querySelector('.issuer-audiences')!, issuer.audiences.join(', '))
        setText(row.querySelector('.issuer-algorithms')!, issuer.algorithms.join(', '))
        setText(row.querySelector('.issuer-status')!, issuer.status)
        const toggle = row.querySelector('.issuer-toggle') as PushButton
        setButtonText(toggle, issuer.status === 'active' ? 'Disable' : 'Enable')
        toggle.disabled = busy
        ;(row.querySelectorAll('push-button')[1] as PushButton).disabled = busy
      },
    )
    syncKeyed(
      this.#mappingBody,
      mappings,
      (mapping) => mapping.mappingId,
      (mapping) => {
        const toggle = button('', {
          small: true,
          onClick: () => {
            const current = this.#mappingOf(mapping.mappingId) ?? mapping
            void this.#setMappingStatus(current, current.status === 'active' ? 'disabled' : 'active')
          },
        })
        toggle.classList.add('mapping-toggle')
        const remove = button('Delete', { small: true, variant: 'danger', ariaLabel: `Delete mapping ${mapping.name}`, onClick: () => void this.#deleteMapping(mapping) })
        return h(
          'tr',
          null,
          h('td', null, h('div', { class: 'mapping-name' }), h('div', { class: 'meta mapping-issuer' })),
          h('td', { class: 'mapping-principal' }),
          h('td', { class: 'mono mapping-conditions' }),
          h('td', { class: 'mono mapping-caps' }),
          h('td', { class: 'mapping-status' }),
          h('td', { class: 'row' }, toggle, remove),
        )
      },
      (row, mapping) => {
        setText(row.querySelector('.mapping-name')!, mapping.name)
        setText(row.querySelector('.mapping-issuer')!, issuerNames.get(mapping.issuerId) ?? '')
        setText(row.querySelector('.mapping-principal')!, mapping.principalName || mapping.principalId)
        setText(row.querySelector('.mapping-conditions')!, mapping.conditions.map(conditionText).join('  '))
        setText(row.querySelector('.mapping-caps')!, `${mapping.capabilities.join(', ')} · ${mapping.tokenTtlSeconds}s`)
        setText(row.querySelector('.mapping-status')!, mapping.status)
        const toggle = row.querySelector('.mapping-toggle') as PushButton
        setButtonText(toggle, mapping.status === 'active' ? 'Disable' : 'Enable')
        toggle.disabled = busy
        ;(row.querySelectorAll('push-button')[1] as PushButton).disabled = busy
      },
    )
  }

  #issuerOf(issuerId: string): FederationIssuerEntry | undefined {
    return (this.#data.state.data?.issuers ?? []).find((issuer) => issuer.issuerId === issuerId)
  }

  #mappingOf(mappingId: string): FederationMappingEntry | undefined {
    return (this.#data.state.data?.mappings ?? []).find((mapping) => mapping.mappingId === mappingId)
  }
}
define('rc-federation-panel', FederationPanel)
