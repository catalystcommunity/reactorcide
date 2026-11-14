import type { PushButton, TextField } from '@catalystcommunity/controls'
import { api } from '~/api/client.ts'
import type { GroupMemberEntry as GroupMember, GroupSummary } from '~/api/csilapi/types.gen.ts'
import { field } from '~/components/Field.ts'
import { ManagedOrgPage, orgHref } from '~/components/ManagedOrg.ts'
import { Action, ActionError } from '~/components/ManagementPage.ts'
import { userPicker, type UserPicker } from '~/components/UserPicker.ts'
import { button, clearField, confirmAction, setButtonText, textField } from '~/ui/controls.ts'
import { h, setAttr, setText, show, syncKeyed } from '~/ui/dom.ts'
import { RcElement, define } from '~/ui/element.ts'
import { Loader } from '~/ui/load.ts'
import { href } from '~/ui/router.ts'

/**
 * Groups and their members.
 *
 * A group is how a role reaches more than one person: assign a role to a group
 * once, and membership does the rest. Roles themselves live on the Roles page.
 *
 * Scoped to the organization chosen in the header (`?org=`), which is a real
 * `organizations` row the caller administers. See ManagedOrg.ts for why the
 * caller's own user id was never the right thing to put here.
 */

/** One table body row: a group, or the member panel of the expanded group. */
type Entry = { kind: 'group'; group: GroupSummary } | { kind: 'members'; group: GroupSummary }

export class OrgGroupsPage extends ManagedOrgPage {
  protected readonly what = 'groups'

  #groups = new Loader(this, (orgId: string) => api.listGroups({ orgId }))
  #expanded: string | undefined

  #create = new Action(() => this.invalidate())
  #name = ''
  #description = ''

  #rolesLink!: HTMLAnchorElement
  #createError!: ActionError
  #nameInput!: TextField
  #descriptionInput!: TextField
  #submit!: PushButton
  #tableWrap!: HTMLElement
  #noGroups!: HTMLElement
  #tbody!: HTMLTableSectionElement
  #rowActions = new WeakMap<Element, { action: Action; error: ActionError }>()

  protected buildBody(): HTMLElement {
    this.#rolesLink = h('a', { class: 'btn btn-sm' }, 'Manage roles')
    this.#tbody = h('tbody')
    this.#tableWrap = h(
      'div',
      { class: 'scroll-x' },
      h('table', { class: 'table' }, h('thead', null, h('tr', null, h('th', null, 'Group'), h('th', null, 'Description'), h('th'))), this.#tbody),
    )
    this.#noGroups = h('div', { class: 'empty' }, 'No groups yet.')

    return h(
      'div',
      { class: 'page' },
      h(
        'div',
        { class: 'page-header' },
        h(
          'div',
          null,
          h('h1', null, 'Groups'),
          h(
            'p',
            { class: 'meta' },
            'Collect people into a group, then grant that group a role once instead of granting each person separately.',
          ),
        ),
        h('div', { class: 'row', style: { alignItems: 'center' } }, this.orgPicker(), this.#rolesLink),
      ),
      this.#buildCreateCard(),
      h('div', { class: 'card' }, h('h3', { class: 'section-title' }, 'Groups'), this.#tableWrap, this.#noGroups),
    )
  }

  #buildCreateCard(): HTMLElement {
    this.#createError = new ActionError()
    this.#nameInput = textField({
      onInput: (value) => {
        this.#name = value
        this.invalidate()
      },
    })
    this.#descriptionInput = textField({
      onInput: (value) => {
        this.#description = value
      },
    })
    this.#submit = button('Create group', { variant: 'primary', type: 'submit' })
    return h(
      'div',
      { class: 'card' },
      h('h3', { class: 'section-title' }, 'New group'),
      h(
        'form',
        { onSubmit: (event: Event) => void this.#onCreate(event) },
        this.#createError.el,
        h(
          'div',
          { class: 'form-row' },
          field({ label: 'Name', required: true, control: this.#nameInput }).el,
          field({ label: 'Description', control: this.#descriptionInput }).el,
        ),
        this.#submit,
      ),
    )
  }

  async #onCreate(event: Event): Promise<void> {
    event.preventDefault()
    const name = this.#name.trim()
    if (!name) return
    const ok = await this.#create.run('The group could not be created.', () =>
      api.createGroup({ orgId: this.managed.orgId(), name, description: this.#description.trim() || undefined }),
    )
    if (ok && this.isActive()) {
      this.#name = ''
      this.#description = ''
      clearField(this.#nameInput)
      clearField(this.#descriptionInput)
      void this.#groups.reload()
    }
  }

  protected renderBody(): void {
    const orgId = this.managed.orgId()
    this.#groups.sync(orgId || undefined)
    setAttr(this.#rolesLink, 'href', href(orgHref('/org/roles', orgId)))

    this.#createError.set(this.#create.error)
    this.#submit.disabled = this.#create.busy || !this.#name.trim()
    setButtonText(this.#submit, this.#create.busy ? 'Creating…' : 'Create group')

    const groups = this.#groups.state.data?.groups ?? []
    show(this.#tableWrap, groups.length > 0)
    show(this.#noGroups, groups.length === 0)
    const entries: Entry[] = []
    for (const group of groups) {
      entries.push({ kind: 'group', group })
      if (this.#expanded === group.groupId) entries.push({ kind: 'members', group })
    }
    syncKeyed(
      this.#tbody,
      entries,
      (entry) => `${entry.kind}:${entry.group.groupId}`,
      (entry) => (entry.kind === 'group' ? this.#createGroupRow(entry.group) : this.#createMembersRow(entry.group, orgId)),
      (row, entry) => {
        if (entry.kind === 'group') this.#updateGroupRow(row as HTMLTableRowElement, entry.group)
      },
    )
  }

  #createGroupRow(group: GroupSummary): HTMLTableRowElement {
    const action = new Action(() => this.invalidate())
    const error = new ActionError()
    const toggle = button('', {
      small: true,
      variant: 'flat',
      onClick: () => {
        this.#expanded = this.#expanded === group.groupId ? undefined : group.groupId
        this.invalidate()
      },
    })
    const remove = button('Delete', {
      small: true,
      variant: 'danger',
      onClick: async () => {
        const confirmed = await confirmAction(
          `Delete group "${group.name}"? Any role granted to it stops applying to its members.`,
          { okLabel: 'Delete' },
        )
        if (!confirmed) return
        const ok = await action.run('The group could not be deleted.', () => api.deleteGroup({ groupId: group.groupId }))
        if (ok && this.isActive()) void this.#groups.reload()
      },
    })
    remove.classList.add('group-delete')
    toggle.classList.add('group-toggle')
    const row = h('tr', null, h('td', null, toggle), h('td', { class: 'meta group-description' }), h('td', null, remove, error.el))
    this.#rowActions.set(row, { action, error })
    return row
  }

  #updateGroupRow(row: HTMLTableRowElement, group: GroupSummary): void {
    const expanded = this.#expanded === group.groupId
    const toggle = row.querySelector('.group-toggle') as PushButton
    setButtonText(toggle, `${expanded ? '▾' : '▸'} ${group.name}`)
    setAttr(toggle, 'aria-label', `${expanded ? 'Hide' : 'Show'} members of ${group.name}`)
    setText(row.querySelector('.group-description')!, group.description || '—')
    const state = this.#rowActions.get(row)!
    ;(row.querySelector('.group-delete') as PushButton).disabled = state.action.busy
    state.error.set(state.action.error)
  }

  #createMembersRow(group: GroupSummary, orgId: string): HTMLTableRowElement {
    const panel = document.createElement('rc-group-members') as GroupMembers
    panel.groupId = group.groupId
    panel.orgId = orgId
    return h('tr', null, h('td', { colspan: '3' }, panel))
  }
}

/**
 * The members of one group. An element of its own, so its data and its user
 * picker live exactly as long as the group stays expanded.
 */
class GroupMembers extends RcElement {
  groupId = ''
  orgId = ''

  #members = new Loader(this, (groupId: string) => api.listGroupMembers({ groupId }))
  #action = new Action(() => this.invalidate())
  #userId = ''

  #error!: ActionError
  #picker!: UserPicker
  #add!: PushButton
  #table!: HTMLTableElement
  #tbody!: HTMLTableSectionElement
  #none!: HTMLElement

  protected build(): void {
    this.classList.add('stack')
    this.#error = new ActionError()
    this.#picker = userPicker(this.orgId, '', (userId) => {
      this.#userId = userId
      this.invalidate()
    })
    this.#add = button('Add member', { small: true, variant: 'primary', type: 'submit' })
    this.#tbody = h('tbody')
    this.#table = h('table', { class: 'table' }, h('thead', null, h('tr', null, h('th', null, 'Member'), h('th'))), this.#tbody)
    this.#none = h('p', { class: 'meta' }, 'This group has no members.')
    this.append(
      this.#error.el,
      h(
        'form',
        { class: 'stack', onSubmit: (event: Event) => void this.#onAdd(event) },
        field({
          label: 'Add a member',
          hint: 'Search the accounts that have signed in, then pick one.',
          control: h('div', { style: { maxWidth: '28rem' } }, this.#picker),
          labelTarget: this.#picker,
        }).el,
        h('div', { class: 'row' }, this.#add),
      ),
      this.#table,
      this.#none,
    )
  }

  async #onAdd(event: Event): Promise<void> {
    event.preventDefault()
    const userId = this.#userId.trim()
    if (!userId) return
    const ok = await this.#action.run('The member could not be added.', () => api.addGroupMember({ groupId: this.groupId, userId }))
    if (ok && this.isActive()) {
      this.#userId = ''
      this.#picker.value = ''
      void this.#members.reload()
    }
  }

  render(): void {
    this.#members.sync(this.groupId || undefined)
    this.#picker.orgId = this.orgId
    this.#error.set(this.#action.error)
    this.#add.disabled = this.#action.busy || !this.#userId
    const members = this.#members.state.data?.members ?? []
    show(this.#table, members.length > 0)
    show(this.#none, this.#members.state.loaded && members.length === 0)
    syncKeyed(
      this.#tbody,
      members,
      (member) => member.userId,
      (member) => this.#createMemberRow(member),
      (row, member) => {
        setText(row.querySelector('.member-name')!, member.username)
        setText(row.querySelector('.member-id')!, member.userId)
        ;(row.querySelector('push-button') as PushButton).disabled = this.#action.busy
      },
    )
  }

  #createMemberRow(member: GroupMember): HTMLTableRowElement {
    const remove = button('Remove', {
      small: true,
      ariaLabel: `Remove ${member.username || member.userId}`,
      onClick: async () => {
        await this.#action.run('The member could not be removed.', () =>
          api.removeGroupMember({ groupId: this.groupId, userId: member.userId }),
        )
        if (this.isActive()) void this.#members.reload()
      },
    })
    return h(
      'tr',
      null,
      h('td', null, h('div', { class: 'member-name' }), h('div', { class: 'meta mono truncate member-id' })),
      h('td', null, remove),
    )
  }
}
define('rc-group-members', GroupMembers)

export const tag = 'rc-org-groups-page'
define(tag, OrgGroupsPage)
