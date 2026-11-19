import type { PushButton, TextField } from '@catalystcommunity/controls'
import { api } from '~/api/client.ts'
import type { GroupSummary, RoleAssignment, UserSummary } from '~/api/csilapi/types.gen.ts'
import { ChoiceSelect } from '~/components/ChoiceInput.ts'
import { field, type Field } from '~/components/Field.ts'
import { ManagedOrgPage, orgHref } from '~/components/ManagedOrg.ts'
import { Action, ActionError } from '~/components/ManagementPage.ts'
import { relativeTime } from '~/components/States.ts'
import { userLabel, userPicker, type UserPicker } from '~/components/UserPicker.ts'
import { button, clearField, confirmAction, setButtonText, textField } from '~/ui/controls.ts'
import { h, setAttr, setText, show, syncKeyed } from '~/ui/dom.ts'
import { define } from '~/ui/element.ts'
import { Loader } from '~/ui/load.ts'
import { href } from '~/ui/router.ts'

/**
 * Role assignments.
 *
 * A role binds a PRINCIPAL (a user, or a group) to a SCOPE (the organization,
 * one project, or one workflow of a project). The scope is what makes the
 * same role name mean different things: `admin` on an org administers that
 * org; `owner` on a project owns only that project; `owner` on a workflow can
 * cancel, retry and re-run, quarantine, and propose required-set changes for
 * that one workflow (docs/organizations-and-ci-policy.md).
 */

const PRINCIPAL_TYPES = [
  { value: 'user', label: 'User', description: 'One person.' },
  { value: 'group', label: 'Group', description: 'Everyone in a group, now and in future.' },
]

const SCOPE_TYPES = [
  { value: 'org', label: 'Organization', description: 'Applies across the whole organization.' },
  { value: 'project', label: 'Project', description: 'Applies to one project only.' },
  { value: 'workflow', label: 'Workflow', description: 'Owner of one workflow of a project (by its YAML id).' },
]

const ROLES = [
  { value: 'admin', label: 'Admin', description: 'Full administration of the scope, including who else may access it.' },
  { value: 'owner', label: 'Owner', description: 'Owns the scope and its settings, but does not administer access.' },
  { value: 'member', label: 'Member', description: 'Can see the scope, including its private resources.' },
]

type Assignment = RoleAssignment

export class OrgRolesPage extends ManagedOrgPage {
  protected readonly what = 'roles'

  #assignments = new Loader(this, (scopeId: string) => api.listRoleAssignments({ scopeType: 'org', scopeId }))

  /*
   * The people and groups of an org, for turning a principal id into a name.
   *
   * Fetched once per org and looked up by id. Both lists are decorative here --
   * the assignments table works without them -- so a failure to load either
   * degrades to showing the raw id rather than taking the page down.
   */
  #users = new Loader(this, async (orgId: string) => {
    try {
      return (await api.listUsers({ orgId })).users
    } catch {
      return [] as UserSummary[]
    }
  })
  #groups = new Loader(this, async (orgId: string) => {
    try {
      return (await api.listGroups({ orgId })).groups
    } catch {
      return [] as GroupSummary[]
    }
  })

  // The grant form's state.
  #principalType = 'user'
  #principalId = ''
  #scopeType = 'org'
  #scopeId = ''
  #scopeWorkflow = ''
  #role = 'member'
  #grant = new Action(() => this.invalidate())

  #groupsLink!: HTMLAnchorElement
  #trustLink!: HTMLAnchorElement
  #grantError!: ActionError
  #principalTypeSelect!: ChoiceSelect
  #userField!: Field
  #picker!: UserPicker
  #groupField!: Field
  #groupSelect!: ChoiceSelect
  #groupHint!: HTMLElement
  #scopeSelect!: ChoiceSelect
  #projectField!: Field
  #projectInput!: TextField
  #workflowField!: Field
  #workflowInput!: TextField
  #roleField!: Field
  #roleSelect!: ChoiceSelect
  #submit!: PushButton
  #tbody!: HTMLTableSectionElement
  #tableWrap!: HTMLElement
  #noAssignments!: HTMLElement
  #rowActions = new WeakMap<Element, { action: Action; error: ActionError }>()

  // Project and workflow roles of one project of the org.
  #projects = new Loader(this, async (orgId: string) => {
    try {
      return (await api.listProjects({ orgId })).projects
    } catch {
      return []
    }
  })
  #projectRoles = new Loader(this, async (projectId: string) => {
    const [project, workflow] = await Promise.all([
      api.listRoleAssignments({ scopeType: 'project', scopeId: projectId }),
      api.listRoleAssignments({ scopeType: 'workflow', scopeId: projectId }),
    ])
    return [...project.assignments, ...workflow.assignments]
  })
  #rolesProject = ''
  #projectPicker!: ChoiceSelect
  #projectTbody!: HTMLTableSectionElement
  #projectTableWrap!: HTMLElement
  #noProjectRoles!: HTMLElement

  protected buildBody(): HTMLElement {
    this.#groupsLink = h('a', { class: 'btn btn-sm' }, 'Manage groups')
    this.#trustLink = h('a', { class: 'btn btn-sm' }, 'CI trust settings')
    const header = h(
      'div',
      { class: 'page-header' },
      h(
        'div',
        null,
        h('h1', null, 'Roles'),
        h('p', { class: 'meta' }, 'Who can do what, and where. A role granted to a group reaches everyone in it.'),
      ),
      h('div', { class: 'row', style: { alignItems: 'center' } }, this.orgPicker(), this.#groupsLink, this.#trustLink),
    )

    this.#tbody = h('tbody')
    this.#tableWrap = h(
      'div',
      { class: 'scroll-x' },
      h(
        'table',
        { class: 'table' },
        h('thead', null, h('tr', null, h('th', null, 'Principal'), h('th', null, 'Role'), h('th', null, 'Scope'), h('th', null, 'Granted'), h('th'))),
        this.#tbody,
      ),
    )
    this.#noAssignments = h('div', { class: 'empty' }, 'No roles have been assigned in this organization.')

    this.#projectPicker = new ChoiceSelect({
      ariaLabel: 'Project',
      placeholder: 'Choose a project…',
      value: '',
      onChange: (next) => {
        this.#rolesProject = next
        this.invalidate()
      },
    })
    this.#projectTbody = h('tbody')
    this.#projectTableWrap = h(
      'div',
      { class: 'scroll-x', hidden: true },
      h(
        'table',
        { class: 'table project-roles-table' },
        h('thead', null, h('tr', null, h('th', null, 'Principal'), h('th', null, 'Role'), h('th', null, 'Scope'), h('th', null, 'Granted'), h('th'))),
        this.#projectTbody,
      ),
    )
    this.#noProjectRoles = h('div', { class: 'empty', hidden: true }, 'No project or workflow roles in this project.')

    return h(
      'div',
      { class: 'page' },
      header,
      this.#buildGrantCard(),
      h('div', { class: 'card' }, h('h3', { class: 'section-title' }, 'Assignments in this organization'), this.#tableWrap, this.#noAssignments),
      h(
        'div',
        { class: 'card' },
        h('h3', { class: 'section-title' }, 'Project and workflow roles'),
        h('p', { class: 'meta' }, 'The roles of one project, and the owner roles of its workflows. A workflow owner can cancel, retry and re-run that workflow, quarantine it, and propose required set changes.'),
        field({ label: 'Project', control: this.#projectPicker.el }).el,
        this.#projectTableWrap,
        this.#noProjectRoles,
      ),
    )
  }

  #buildGrantCard(): HTMLElement {
    this.#grantError = new ActionError()
    this.#principalTypeSelect = new ChoiceSelect({
      value: this.#principalType,
      choices: PRINCIPAL_TYPES,
      onChange: (next) => {
        // A user id is not a group id; a change of kind clears the choice.
        this.#principalType = next
        this.#principalId = ''
        this.#picker.value = ''
        this.invalidate()
      },
    })
    this.#picker = userPicker('', '', (userId) => {
      this.#principalId = userId
      this.invalidate()
    })
    this.#userField = field({
      label: 'User',
      required: true,
      hint: 'Search the accounts that have signed in.',
      control: this.#picker,
    })
    this.#groupSelect = new ChoiceSelect({
      placeholder: 'Choose a group…',
      value: '',
      onChange: (next) => {
        this.#principalId = next
        this.invalidate()
      },
    })
    this.#groupField = field({
      label: 'Group',
      required: true,
      hint: 'A group from the Groups page.',
      control: this.#groupSelect.el,
    })
    this.#groupHint = this.#groupField.el.querySelector('.field-hint')!

    this.#scopeSelect = new ChoiceSelect({
      value: this.#scopeType,
      choices: SCOPE_TYPES,
      onChange: (next) => {
        this.#scopeType = next
        this.invalidate()
      },
    })
    this.#projectInput = textField({
      mono: true,
      onInput: (value) => {
        this.#scopeId = value
      },
    })
    this.#projectField = field({ label: 'Project ID', required: true, hint: "From the project's detail page.", control: this.#projectInput })
    this.#workflowInput = textField({
      mono: true,
      onInput: (value) => {
        this.#scopeWorkflow = value
        this.invalidate()
      },
    })
    this.#workflowField = field({
      label: 'Workflow ID',
      required: true,
      hint: 'The workflow security ID: the `id:` of the workflow YAML. A workflow role is always owner.',
      control: this.#workflowInput,
    })
    this.#roleSelect = new ChoiceSelect({
      value: this.#role,
      choices: ROLES,
      onChange: (next) => {
        this.#role = next
      },
    })
    this.#submit = button('Grant role', { variant: 'primary', type: 'submit' })

    const form = h(
      'form',
      { onSubmit: (event: Event) => void this.#onGrant(event) },
      this.#grantError.el,
      h(
        'div',
        { class: 'form-row' },
        field({ label: 'Principal type', required: true, control: this.#principalTypeSelect.el }).el,
        this.#userField.el,
        this.#groupField.el,
      ),
      h(
        'div',
        { class: 'form-row' },
        field({ label: 'Scope', required: true, control: this.#scopeSelect.el }).el,
        this.#projectField.el,
        this.#workflowField.el,
      ),
      (this.#roleField = field({
        label: 'Role',
        required: true,
        tooltip:
          'Admin administers access as well as settings. Owner controls the scope but not who else may reach it. Member can see private resources in the scope.',
        control: this.#roleSelect.el,
      })).el,
      this.#submit,
    )
    return h('div', { class: 'card' }, h('h3', { class: 'section-title' }, 'Grant a role'), form)
  }

  async #onGrant(event: Event): Promise<void> {
    event.preventDefault()
    const principalId = this.#principalId.trim()
    if (!principalId) return
    const orgId = this.managed.orgId()
    const ok = await this.#grant.run('The role could not be granted.', () =>
      api.assignRole({
        principalType: this.#principalType,
        principalId,
        scopeType: this.#scopeType,
        // An org-scoped grant defaults to THIS org, so the common case needs
        // no id typed at all.
        scopeId: this.#scopeType === 'org' ? orgId : this.#scopeId.trim() || undefined,
        scopeWorkflow: this.#scopeType === 'workflow' ? this.#scopeWorkflow.trim() || undefined : undefined,
        role: this.#scopeType === 'workflow' ? 'owner' : this.#role,
      }),
    )
    if (!this.isActive()) return
    if (ok) {
      this.#principalId = ''
      this.#scopeId = ''
      this.#scopeWorkflow = ''
      this.#picker.value = ''
      this.#groupSelect.update(this.#groupChoices(), '')
      clearField(this.#projectInput)
      clearField(this.#workflowInput)
      void this.#assignments.reload()
      void this.#projectRoles.reload()
    }
  }

  #groupChoices(): { value: string; label: string; description: string }[] {
    return (this.#groups.state.data ?? []).map((group) => ({
      value: group.groupId,
      label: group.name,
      description: group.description ?? '',
    }))
  }

  #principalName(type: string, id: string): string | undefined {
    if (type === 'group') return this.#groups.state.data?.find((group) => group.groupId === id)?.name
    const user = this.#users.state.data?.find((candidate) => candidate.userId === id)
    return user ? userLabel(user) : undefined
  }

  protected renderBody(): void {
    const orgId = this.managed.orgId()
    this.#assignments.sync(orgId || undefined)
    this.#users.sync(orgId || undefined)
    this.#groups.sync(orgId || undefined)

    setAttr(this.#groupsLink, 'href', href(orgHref('/org/groups', orgId)))
    setAttr(this.#trustLink, 'href', href(orgHref('/org/ci-trust', orgId)))

    // The grant form.
    this.#grantError.set(this.#grant.error)
    const isGroup = this.#principalType === 'group'
    show(this.#userField.el, !isGroup)
    show(this.#groupField.el, isGroup)
    this.#picker.orgId = orgId
    const groups = this.#groups.state.data ?? []
    this.#groupSelect.update(this.#groupChoices(), isGroup ? this.#principalId : '')
    this.#groupSelect.el.disabled = groups.length === 0
    setText(this.#groupHint, groups.length ? 'A group from the Groups page.' : 'No groups yet. Create one on the Groups page.')
    const isWorkflow = this.#scopeType === 'workflow'
    show(this.#projectField.el, this.#scopeType === 'project' || isWorkflow)
    show(this.#workflowField.el, isWorkflow)
    show(this.#roleField.el, !isWorkflow)
    this.#submit.disabled = this.#grant.busy || !this.#principalId.trim() || (isWorkflow && !this.#scopeWorkflow.trim())
    setButtonText(this.#submit, this.#grant.busy ? 'Granting…' : 'Grant role')

    // The assignments.
    const assignments = this.#assignments.state.data?.assignments ?? []
    show(this.#tableWrap, assignments.length > 0)
    show(this.#noAssignments, assignments.length === 0)
    syncKeyed(
      this.#tbody,
      assignments,
      (assignment) => assignment.assignmentId,
      (assignment) => this.#createRow(assignment, () => this.#assignments.reload()),
      (row, assignment) => this.#updateRow(row, assignment),
    )

    // The project and workflow roles of the chosen project.
    this.#projects.sync(orgId || undefined)
    const projects = this.#projects.state.data ?? []
    if (this.#rolesProject && !projects.some((p) => p.projectId === this.#rolesProject)) this.#rolesProject = ''
    this.#projectPicker.update(
      projects.map((p) => ({ value: p.projectId, label: p.name, description: p.repoUrl })),
      this.#rolesProject,
    )
    this.#projectRoles.sync(this.#rolesProject || undefined)
    const projectRoles = this.#rolesProject ? (this.#projectRoles.state.data ?? []) : []
    const loaded = Boolean(this.#rolesProject) && this.#projectRoles.state.loaded
    show(this.#projectTableWrap, projectRoles.length > 0)
    show(this.#noProjectRoles, loaded && projectRoles.length === 0)
    syncKeyed(
      this.#projectTbody,
      projectRoles,
      (assignment) => assignment.assignmentId,
      (assignment) => this.#createRow(assignment, () => this.#projectRoles.reload()),
      (row, assignment) => this.#updateRow(row, assignment),
    )
  }

  #createRow(assignment: Assignment, reload: () => Promise<void>): HTMLTableRowElement {
    const action = new Action(() => this.invalidate())
    const error = new ActionError()
    const revoke = button('Revoke', {
      small: true,
      variant: 'danger',
      onClick: async () => {
        if (!(await confirmAction('Revoke this role? Access it granted is removed immediately.', { okLabel: 'Revoke' }))) return
        const ok = await action.run('The role could not be revoked.', () => api.revokeRole({ assignmentId: assignment.assignmentId }))
        if (ok && this.isActive()) void reload()
      },
    })
    const row = h(
      'tr',
      null,
      h(
        'td',
        null,
        h('div', null, h('span', { class: 'meta principal-type' }), ' ', h('span', { class: 'principal-name' })),
        h('div', { class: 'meta mono truncate principal-id' }),
      ),
      h('td', { class: 'assignment-role' }),
      h('td', { class: 'meta' }, h('span', { class: 'scope-type' }), h('div', { class: 'mono truncate scope-id' })),
      h('td', { class: 'meta assignment-granted' }),
      h('td', null, revoke, error.el),
    )
    this.#rowActions.set(row, { action, error })
    return row
  }

  #updateRow(row: HTMLTableRowElement, assignment: Assignment): void {
    setText(row.querySelector('.principal-type')!, assignment.principalType)
    const name = this.#principalName(assignment.principalType, assignment.principalId)
    const nameEl = row.querySelector<HTMLElement>('.principal-name')!
    show(nameEl, name)
    setText(nameEl, name ?? '')
    setText(row.querySelector('.principal-id')!, assignment.principalId)
    setText(row.querySelector('.assignment-role')!, assignment.role)
    setText(row.querySelector('.scope-type')!, assignment.scopeType)
    const scopeId = row.querySelector<HTMLElement>('.scope-id')!
    show(scopeId, assignment.scopeId)
    setText(scopeId, assignment.scopeWorkflow ? `${assignment.scopeId ?? ''} / ${assignment.scopeWorkflow}` : (assignment.scopeId ?? ''))
    setText(row.querySelector('.assignment-granted')!, relativeTime(assignment.createdAt))
    const state = this.#rowActions.get(row)!
    ;(row.querySelector('push-button') as PushButton).disabled = state.action.busy
    state.error.set(state.action.error)
  }
}

export const tag = 'rc-org-roles-page'
define(tag, OrgRolesPage)
