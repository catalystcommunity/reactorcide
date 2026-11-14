import type { TextField } from '@catalystcommunity/controls'
import { api } from '~/api/client.ts'
import type { UserSummary } from '~/api/csilapi/types.gen.ts'
import { textField } from '~/ui/controls.ts'
import { h, setAttr, setClass, setText, show, syncKeyed } from '~/ui/dom.ts'
import { RcElement, define } from '~/ui/element.ts'
import { describeError } from './ManagementPage.ts'

/**
 * Choosing a person by name, not by pasting a UUID.
 *
 * Roles and group membership are keyed by user id, and until `list-users`
 * existed nothing in the UI showed one: an administrator had to read the id
 * out of the database. This asks the coordinator for the accounts it knows,
 * filtered as the administrator types, and hands back the chosen id.
 *
 * The list is authorized by the org the caller administers (`orgId`), which
 * is how the coordinator decides whether they may see accounts at all.
 *
 * Set `orgId`, `value` and `onChange` before it connects. Set `orgId` or
 * `value` later and it follows.
 */
export class UserPicker extends RcElement {
  #orgId = ''
  #value = ''
  onChange: (userId: string) => void = () => {}

  #search!: TextField
  #error!: HTMLDivElement
  #none!: HTMLDivElement
  #list!: HTMLDivElement
  #users: UserSummary[] = []
  #loaded = false
  /** Only the newest request may write; an older response that lands late is dropped. */
  #request = 0
  #loadedFor = ''

  get orgId(): string {
    return this.#orgId
  }
  set orgId(next: string) {
    if (next === this.#orgId) return
    this.#orgId = next
    if (this.isActive()) void this.#load()
  }

  get value(): string {
    return this.#value
  }
  set value(next: string) {
    if (next === this.#value) return
    this.#value = next
    this.invalidate()
  }

  /** The search box, which takes the field's accessible name. */
  get searchField(): TextField {
    return this.#search
  }

  protected build(): void {
    this.classList.add('user-picker')
    this.setAttribute('role', 'group')
    this.#search = textField({
      type: 'search',
      placeholder: 'Search by name or sign-in',
      onInput: () => void this.#load(),
    })
    this.#error = h('div', { class: 'field-error', role: 'alert', hidden: true })
    this.#none = h('div', { class: 'field-hint', hidden: true }, 'No matching accounts. A person appears here after their first sign-in.')
    this.#list = h('div', { class: 'user-picker-list', role: 'listbox', 'aria-label': 'Accounts', hidden: true })
    this.append(this.#search, this.#error, this.#none, this.#list)
    if (!this.hasAttribute('aria-label')) this.setAttribute('aria-label', 'Search accounts')
  }

  /**
   * A `Field` names and describes the picker by setting `aria-label` and
   * `aria-describedby` on this host. The host is a labelled group, and the
   * search field gets the same name and description, because the field is
   * what has the focus. Mirrored now and on each change, removals included
   * (an error that clears must stop being read).
   */
  #mirrorAria(): void {
    for (const name of ['aria-label', 'aria-describedby']) setAttr(this.#search, name, this.getAttribute(name))
  }

  protected connected(): void {
    this.#mirrorAria()
    const observer = new MutationObserver(() => this.#mirrorAria())
    observer.observe(this, { attributes: true, attributeFilter: ['aria-label', 'aria-describedby'] })
    this.own(() => observer.disconnect())
    const key = `${this.#orgId}\u0000${this.#search.value}`
    if (key !== this.#loadedFor) void this.#load()
  }

  async #load(): Promise<void> {
    const orgId = this.#orgId
    const query = this.#search?.value.trim() ?? ''
    const request = ++this.#request
    this.#loadedFor = `${orgId}\u0000${this.#search?.value ?? ''}`
    let users: UserSummary[] = []
    let error: string | undefined
    try {
      users = (await api.listUsers({ orgId, query: query || undefined })).users
    } catch (cause) {
      error = describeError(cause, 'The user list could not be loaded.')
    }
    if (request !== this.#request || !this.isActive()) return
    this.#users = users
    this.#loaded = true
    show(this.#error, error)
    setText(this.#error, error ?? '')
    this.invalidate()
  }

  render(): void {
    const hasUsers = this.#users.length > 0
    show(this.#list, hasUsers)
    show(this.#none, this.#loaded && !hasUsers && this.#error.hidden)
    syncKeyed(
      this.#list,
      this.#users,
      (user) => user.userId,
      (user) => {
        const option = h(
          'button',
          { type: 'button', role: 'option', class: 'user-picker-option' },
          h('span', { class: 'user-picker-name' }),
          h('span', { class: 'meta mono user-picker-subject' }),
        )
        option.addEventListener('click', () => {
          this.#value = user.userId
          this.invalidate()
          this.onChange(user.userId)
        })
        return option
      },
      (option, user) => {
        const selected = this.#value === user.userId
        setAttr(option, 'aria-selected', String(selected))
        setClass(option, 'user-picker-option-selected', selected)
        setText(option.querySelector('.user-picker-name')!, userLabel(user))
        const subject = option.querySelector<HTMLElement>('.user-picker-subject')!
        setText(subject, user.subject ?? '')
        show(subject, user.subject)
      },
    )
  }
}
define('rc-user-picker', UserPicker)

export function userPicker(orgId: string, value: string, onChange: (userId: string) => void): UserPicker {
  const el = document.createElement('rc-user-picker') as UserPicker
  el.orgId = orgId
  el.value = value
  el.onChange = onChange
  return el
}

export function userLabel(user: UserSummary): string {
  return user.displayName || user.username || user.userId
}
