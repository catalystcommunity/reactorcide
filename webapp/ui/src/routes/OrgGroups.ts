import type { PushButton, TextField } from '@catalystcommunity/controls'
import { api } from '~/api/client.ts'
import type { GroupMemberDetail, GroupSummary, GroupVcsLink } from '~/api/csilapi/types.gen.ts'
import { field } from '~/components/Field.ts'
import { ManagedOrgPage, orgHref } from '~/components/ManagedOrg.ts'
import { Action, ActionError } from '~/components/ManagementPage.ts'
import '~/components/priority.css'
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

const PRIORITY_SHIFT_HINT =
  'From -50 to 50. The largest shift of the groups of a person moves the priority of the work that the person starts. 0 is no shift.'

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
  #principalsLink!: HTMLAnchorElement
  #createError!: ActionError
  #nameInput!: TextField
  #descriptionInput!: TextField
  #submit!: PushButton
  #tableWrap!: HTMLElement
  #noGroups!: HTMLElement
  #tbody!: HTMLTableSectionElement
  #rowActions = new WeakMap<Element, { action: Action; error: ActionError }>()
  #shiftStates = new WeakMap<Element, { dirty: boolean }>()

  protected buildBody(): HTMLElement {
    this.#rolesLink = h('a', { class: 'btn btn-sm' }, 'Manage roles')
    this.#principalsLink = h('a', { class: 'btn btn-sm' }, 'Principals')
    this.#tbody = h('tbody')
    this.#tableWrap = h(
      'div',
      { class: 'scroll-x' },
      h(
        'table',
        { class: 'table' },
        h('thead', null, h('tr', null, h('th', null, 'Group'), h('th', null, 'Description'), h('th', { title: PRIORITY_SHIFT_HINT }, 'Priority shift'), h('th'))),
        this.#tbody,
      ),
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
        h('div', { class: 'row', style: { alignItems: 'center' } }, this.orgPicker(), this.#rolesLink, this.#principalsLink),
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
    setAttr(this.#principalsLink, 'href', href(orgHref('/org/principals', orgId)))

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
    // The priority shift (docs/priority.md): made one time; render() changes
    // the value only while the person does not edit it.
    const shift = textField({ mono: true, ariaLabel: `Priority shift of ${group.name}`, placeholder: '0' })
    shift.classList.add('group-shift-input')
    const shiftState = { dirty: false }
    shift.addEventListener('input', () => {
      shiftState.dirty = true
      this.invalidate()
    })
    const saveShift = button('Save', {
      small: true,
      ariaLabel: `Save the priority shift of ${group.name}`,
      onClick: async () => {
        const value = Number(shift.value.trim() || '0')
        const invalid = !Number.isInteger(value) || value < -50 || value > 50
        const message = invalid ? 'The priority shift must be a whole number from -50 to 50.' : 'The priority shift could not be saved.'
        const ok = await action.run(message, async () => {
          if (invalid) throw new Error(message)
          await api.setGroupPriorityShift({ groupId: group.groupId, shift: value })
        })
        if (ok && this.isActive()) {
          shiftState.dirty = false
          void this.#groups.reload()
        }
      },
    })
    saveShift.classList.add('group-shift-save')
    const row = h(
      'tr',
      null,
      h('td', null, toggle),
      h('td', { class: 'meta group-description' }),
      h('td', null, h('div', { class: 'group-shift' }, shift, saveShift)),
      h('td', null, remove, error.el),
    )
    this.#rowActions.set(row, { action, error })
    this.#shiftStates.set(row, shiftState)
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
    const shift = row.querySelector('.group-shift-input') as TextField
    const shiftState = this.#shiftStates.get(row)!
    if (!shiftState.dirty && document.activeElement !== shift) shift.value = String(group.priorityShift ?? 0)
    ;(row.querySelector('.group-shift-save') as PushButton).disabled = state.action.busy || !shiftState.dirty
  }

  #createMembersRow(group: GroupSummary, orgId: string): HTMLTableRowElement {
    const panel = document.createElement('rc-group-members') as GroupMembers
    panel.groupId = group.groupId
    panel.orgId = orgId
    return h('tr', null, h('td', { colspan: '4' }, panel))
  }
}

/**
 * The members of one group, and the VCS teams linked to it. An element of its
 * own, so its data and its user picker live exactly as long as the group
 * stays expanded.
 *
 * A member is manual (an admin added it), synced (a linked team gives it), or
 * both. A sync never removes a manual member. A synced-only member cannot be
 * removed here: remove the person from the team, or unlink the team.
 */
class GroupMembers extends RcElement {
  groupId = ''
  orgId = ''

  #members = new Loader(this, (groupId: string) => api.listGroupMemberDetails({ groupId }))
  #action = new Action(() => this.invalidate())
  #userId = ''

  #error!: ActionError
  #picker!: UserPicker
  #add!: PushButton
  #teams!: GroupTeams
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
    this.#teams = document.createElement('rc-group-teams') as GroupTeams
    this.#teams.groupId = this.groupId
    this.#teams.onMembersChanged = () => void this.#members.reload()
    this.#tbody = h('tbody')
    this.#table = h(
      'table',
      { class: 'table' },
      h('thead', null, h('tr', null, h('th', null, 'Member'), h('th', null, 'Source'), h('th', null, 'Teams'), h('th'))),
      this.#tbody,
    )
    this.#none = h('p', { class: 'meta' }, 'This group has no members.')
    this.append(
      this.#teams,
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
      (member) => member.principalId,
      (member) => this.#createMemberRow(member),
      (row, member) => {
        setText(row.querySelector('.member-name')!, member.username || member.displayName || member.name)
        setText(row.querySelector('.member-id')!, member.userId || member.principalId)
        setText(row.querySelector('.member-source')!, memberSource(member))
        setText(row.querySelector('.member-teams')!, member.teams.length ? member.teams.join(', ') : '—')
        const remove = row.querySelector('push-button') as PushButton
        // Only a manual member with a user can be removed here.
        show(remove, member.manual && Boolean(member.userId))
        remove.disabled = this.#action.busy
      },
    )
  }

  #createMemberRow(member: GroupMemberDetail): HTMLTableRowElement {
    const remove = button('Remove', {
      small: true,
      ariaLabel: `Remove ${member.username || member.name}`,
      onClick: async () => {
        if (!member.userId) return
        await this.#action.run('The member could not be removed.', () =>
          api.removeGroupMember({ groupId: this.groupId, userId: member.userId! }),
        )
        if (this.isActive()) void this.#members.reload()
      },
    })
    return h(
      'tr',
      { class: 'member-row' },
      h('td', null, h('div', { class: 'member-name' }), h('div', { class: 'meta mono truncate member-id' })),
      h('td', { class: 'meta member-source' }),
      h('td', { class: 'meta mono member-teams' }),
      h('td', null, remove),
    )
  }
}
define('rc-group-members', GroupMembers)

/** The text of the source of a member. */
export function memberSource(member: GroupMemberDetail): string {
  if (member.manual && member.teams.length > 0) return 'Manual and linked team'
  return member.manual ? 'Manual' : 'Linked team'
}

/**
 * The VCS teams linked to one group: link, unlink, sync now, and the sync
 * status of each team (last sync, next sync, last error).
 */
class GroupTeams extends RcElement {
  groupId = ''
  onMembersChanged: () => void = () => {}

  #links = new Loader(this, (groupId: string) => api.listGroupVcsLinks({ groupId }))
  #action = new Action(() => this.invalidate())
  #team = ''
  #result = ''

  #error!: ActionError
  #teamInput!: TextField
  #link!: PushButton
  #sync!: PushButton
  #resultEl!: HTMLElement
  #table!: HTMLTableElement
  #tbody!: HTMLTableSectionElement
  #none!: HTMLElement

  protected build(): void {
    this.classList.add('stack', 'group-teams')
    this.#error = new ActionError()
    this.#teamInput = textField({
      mono: true,
      placeholder: 'owner/team-slug',
      onInput: (value) => {
        this.#team = value
        this.invalidate()
      },
    })
    this.#link = button('Link team', { small: true, variant: 'primary', type: 'submit' })
    this.#sync = button('Sync now', { small: true, onClick: () => void this.#onSync() })
    this.#sync.classList.add('group-teams-sync')
    this.#resultEl = h('p', { class: 'meta group-teams-result' })
    this.#tbody = h('tbody')
    this.#table = h(
      'table',
      { class: 'table' },
      h(
        'thead',
        null,
        h('tr', null, h('th', null, 'Team'), h('th', null, 'Members'), h('th', null, 'Last sync'), h('th', null, 'Next sync'), h('th', null, 'Error'), h('th')),
      ),
      this.#tbody,
    )
    this.#none = h('p', { class: 'meta' }, 'No VCS team is linked. Members come only from manual adds.')
    this.append(
      h('h4', { class: 'section-title' }, 'Linked VCS teams'),
      h(
        'p',
        { class: 'meta' },
        'The members of a linked GitHub team become members of this group. Reactorcide syncs on a schedule and on team webhooks. The organization needs a project with a repository under the team owner.',
      ),
      this.#error.el,
      h(
        'form',
        { class: 'row', onSubmit: (event: Event) => void this.#onLink(event) },
        field({ label: 'Team', required: true, hint: 'OWNER/SLUG, for example acme/platform.', control: this.#teamInput }).el,
        this.#link,
        this.#sync,
      ),
      this.#resultEl,
      this.#table,
      this.#none,
    )
  }

  async #onLink(event: Event): Promise<void> {
    event.preventDefault()
    const team = this.#team.trim()
    if (!team) return
    const ok = await this.#action.run('The team could not be linked.', () => api.linkGroupVcsTeam({ groupId: this.groupId, team }))
    if (ok && this.isActive()) {
      this.#team = ''
      clearField(this.#teamInput)
      void this.#links.reload()
    }
  }

  async #onSync(): Promise<void> {
    let summary = ''
    const ok = await this.#action.run('The teams could not be synced.', async () => {
      const response = await api.syncGroupVcsTeams({ groupId: this.groupId })
      summary = response.results.length
        ? response.results
            .map((r) => (r.error ? `${r.team}: ${r.error}` : `${r.team}: ${r.members} members, ${r.added} added, ${r.removed} removed`))
            .join('; ')
        : 'Sync requested.'
    })
    if (!this.isActive()) return
    this.#result = ok ? summary : ''
    void this.#links.reload()
    this.onMembersChanged()
  }

  render(): void {
    this.#links.sync(this.groupId || undefined)
    this.#error.set(this.#action.error)
    this.#link.disabled = this.#action.busy || !this.#team.trim()
    const links = this.#links.state.data?.links ?? []
    this.#sync.disabled = this.#action.busy || links.length === 0
    setText(this.#resultEl, this.#result)
    show(this.#resultEl, this.#result !== '')
    show(this.#table, links.length > 0)
    show(this.#none, this.#links.state.loaded && links.length === 0)
    syncKeyed(
      this.#tbody,
      links,
      (link) => link.linkId,
      (link) => this.#createLinkRow(link),
      (row, link) => {
        setText(row.querySelector('.team-name')!, link.subject)
        setText(row.querySelector('.team-members')!, String(link.memberCount))
        setText(row.querySelector('.team-last')!, link.lastSyncedAt || 'Never')
        setText(row.querySelector('.team-next')!, link.nextSyncAt || 'Now')
        setText(row.querySelector('.team-error')!, link.lastError || '—')
        ;(row.querySelector('push-button') as PushButton).disabled = this.#action.busy
      },
    )
  }

  #createLinkRow(link: GroupVcsLink): HTMLTableRowElement {
    const unlink = button('Unlink', {
      small: true,
      variant: 'danger',
      ariaLabel: `Unlink ${link.subject}`,
      onClick: async () => {
        const confirmed = await confirmAction(
          `Unlink ${link.subject}? The members that only this team gives leave the group.`,
          { okLabel: 'Unlink' },
        )
        if (!confirmed) return
        const ok = await this.#action.run('The team could not be unlinked.', () =>
          api.unlinkGroupVcsTeam({ groupId: this.groupId, linkId: link.linkId }),
        )
        if (ok && this.isActive()) {
          void this.#links.reload()
          this.onMembersChanged()
        }
      },
    })
    return h(
      'tr',
      { class: 'team-row' },
      h('td', { class: 'mono team-name' }),
      h('td', { class: 'team-members' }),
      h('td', { class: 'meta team-last' }),
      h('td', { class: 'meta team-next' }),
      h('td', { class: 'meta team-error' }),
      h('td', null, unlink),
    )
  }
}
define('rc-group-teams', GroupTeams)

export const tag = 'rc-org-groups-page'
define(tag, OrgGroupsPage)
