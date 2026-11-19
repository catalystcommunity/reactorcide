import type { PushButton, TextField } from '@catalystcommunity/controls'
import { api } from '~/api/client.ts'
import type { EnvDefaultEntry } from '~/api/csilapi/types.gen.ts'
import { field } from '~/components/Field.ts'
import { Action, ActionError } from '~/components/ManagementPage.ts'
import { button, clearField, confirmAction, setButtonText, textField } from '~/ui/controls.ts'
import { h, setText, show, syncKeyed } from '~/ui/dom.ts'
import { RcElement, define } from '~/ui/element.ts'
import { Loader } from '~/ui/load.ts'

/** The scope of a set of environment defaults. */
export interface EnvDefaultsScope {
  /** The organization NAME. Nothing loads while it is empty. */
  organization: string
  workerClass?: string
  projectId?: string
}

const KEY_PATTERN = /^[A-Za-z_][A-Za-z0-9_]{0,127}$/

/** The local check of a key; the coordinator checks again. */
export function envKeyError(key: string): string | undefined {
  if (!key) return undefined
  if (!KEY_PATTERN.test(key)) return 'Use letters, digits and "_", and do not start with a digit.'
  const upper = key.toUpperCase()
  if (upper.startsWith('REACTORCIDE_') || upper.startsWith('RC_WF')) return 'REACTORCIDE_* and RC_WF* keys are reserved for the coordinator.'
  return undefined
}

function scopeRequest(scope: EnvDefaultsScope): { organization: string; workerClass?: string; projectId?: string } {
  const out: { organization: string; workerClass?: string; projectId?: string } = { organization: scope.organization }
  if (scope.workerClass) out.workerClass = scope.workerClass
  if (scope.projectId) out.projectId = scope.projectId
  return out
}

/**
 * The non-secret environment defaults of an organization, a worker class, or
 * a project. Precedence in the job environment: job > project > worker class
 * > organization. A save of an existing key replaces its value.
 */
export class EnvDefaultsCard extends RcElement {
  scope: EnvDefaultsScope = { organization: '' }
  heading = 'Environment defaults'

  #load = new Loader<EnvDefaultsScope, EnvDefaultEntry[]>(this, async (scope) => (await api.listEnvDefaults(scopeRequest(scope))).entries)
  #action = new Action(() => this.invalidate())
  #error = new ActionError()
  #key!: TextField
  #value!: TextField
  #keyField!: ReturnType<typeof field>
  #submit!: PushButton
  #tbody!: HTMLTableSectionElement
  #table!: HTMLElement
  #none!: HTMLElement
  #title!: HTMLHeadingElement

  setScope(scope: EnvDefaultsScope): void {
    if (JSON.stringify(scope) === JSON.stringify(this.scope)) return
    this.scope = scope
    this.invalidate()
  }

  protected build(): void {
    this.#key = textField({ mono: true, placeholder: 'GOPROXY', onInput: () => this.invalidate() })
    this.#value = textField({ mono: true, placeholder: 'https://proxy.example/go', onInput: () => this.invalidate() })
    this.#keyField = field({ label: 'Key', required: true, control: this.#key })
    this.#submit = button('Save default', { variant: 'primary', type: 'submit' })
    this.#tbody = h('tbody')
    this.#table = h(
      'div',
      { class: 'scroll-x' },
      h('table', { class: 'table' }, h('thead', null, h('tr', null, h('th', null, 'Key'), h('th', null, 'Value'), h('th'))), this.#tbody),
    )
    this.#none = h('div', { class: 'empty' }, 'No environment defaults.')
    this.#title = h('h3', { class: 'section-title' })
    this.classList.add('card')
    this.append(
      this.#title,
      h(
        'p',
        { class: 'meta' },
        'Non-secret values for every job of this scope, for example a package proxy URL. A job value wins over a project value, a project value over a worker class value, and a worker class value over an organization value. Values are not secret: use a secret grant for a secret. REACTORCIDE_* and RC_WF* keys are reserved.',
      ),
      this.#error.el,
      this.#table,
      this.#none,
      h(
        'form',
        { onSubmit: (event: Event) => void this.#onSave(event) },
        h('div', { class: 'form-row' }, this.#keyField.el, field({ label: 'Value', control: this.#value }).el),
        this.#submit,
      ),
    )
  }

  async #onSave(event: Event): Promise<void> {
    event.preventDefault()
    const key = this.#key.value.trim()
    if (!key || envKeyError(key)) return
    const value = this.#value.value
    const ok = await this.#action.run('The environment default could not be saved.', () => api.putEnvDefault({ ...scopeRequest(this.scope), key, value }))
    if (ok && this.isActive()) {
      clearField(this.#key)
      clearField(this.#value)
      await this.#load.reload()
    }
  }

  async #onDelete(key: string): Promise<void> {
    if (!(await confirmAction(`Remove the environment default "${key}"?`, { okLabel: 'Remove' }))) return
    const ok = await this.#action.run('The environment default could not be removed.', () => api.deleteEnvDefault({ ...scopeRequest(this.scope), key }))
    if (ok && this.isActive()) await this.#load.reload()
  }

  render(): void {
    this.#load.sync(this.scope.organization ? this.scope : undefined)
    setText(this.#title, this.heading)
    this.#error.set(this.#action.error ?? this.#load.state.error?.message)
    const key = this.#key.value.trim()
    const keyError = envKeyError(key)
    this.#keyField.setError(keyError)
    this.#submit.disabled = this.#action.busy || !key || Boolean(keyError)
    setButtonText(this.#submit, this.#action.busy ? 'Saving…' : 'Save default')
    const entries = this.#load.state.data ?? []
    show(this.#table, entries.length > 0)
    show(this.#none, this.#load.state.loaded && entries.length === 0)
    syncKeyed(
      this.#tbody,
      entries,
      (entry) => entry.key,
      (entry) =>
        h(
          'tr',
          null,
          h('td', { class: 'mono env-key' }),
          h('td', { class: 'mono env-value truncate' }),
          h('td', null, button('Remove', { small: true, variant: 'danger', ariaLabel: `Remove ${entry.key}`, onClick: () => void this.#onDelete(entry.key) })),
        ),
      (row, entry) => {
        setText(row.querySelector('.env-key')!, entry.key)
        setText(row.querySelector('.env-value')!, entry.value)
        ;(row.querySelector('push-button') as PushButton).disabled = this.#action.busy
      },
    )
  }
}

export const envDefaultsCardTag = 'rc-env-defaults-card'
define(envDefaultsCardTag, EnvDefaultsCard)

export function envDefaultsCard(scope: EnvDefaultsScope, heading = 'Environment defaults'): EnvDefaultsCard {
  const el = document.createElement(envDefaultsCardTag) as EnvDefaultsCard
  el.scope = scope
  el.heading = heading
  return el
}
