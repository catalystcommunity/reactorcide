import type { CheckBox, PushButton, TextField } from '@catalystcommunity/controls'
import { api, ServiceError } from '~/api/client.ts'
import type {
  DescribeFormMetadataResponse,
  EnumChoice,
  GetCapabilitiesResponse,
  GroupSummary,
  ProjectDetail,
  RoleAssignment,
  UserSummary,
} from '~/api/csilapi/types.gen.ts'
import { RcPage } from '~/app/page.ts'
import { ChoiceMultiSelect, ChoiceSelect } from '~/components/ChoiceInput.ts'
import { field, type Field } from '~/components/Field.ts'
import { ProjectCard, makeCard } from '~/components/ProjectCard.ts'
import { Action, ActionError, describeError } from '~/components/ManagementPage.ts'
import { ResourceView, relativeTime } from '~/components/States.ts'
import { setVisibility, visibilityBadge } from '~/components/StatusBadge.ts'
import { userLabel, userPicker, type UserPicker } from '~/components/UserPicker.ts'
import type { ResourceHandle } from '~/store/index.ts'
import { useCapabilities, useFormMetadata, useProject } from '~/store/resources.ts'
import { button, checkBox, clearField, confirmAction, setButtonText, textField } from '~/ui/controls.ts'
import { h, setAttr, setText, show, syncKeyed, uid } from '~/ui/dom.ts'
import { define } from '~/ui/element.ts'
import { href } from '~/ui/router.ts'
import { Loader } from '~/ui/load.ts'
import type { EventSettingsCard, SchedulesCard } from './ProjectEvents.ts'
import './ProjectEvents.ts'

type ProjectData = { project: ProjectDetail }
type Capability = keyof GetCapabilitiesResponse

/** A small "Disabled" / "Active" style badge with a glyph, so colour is never the only signal. */
function plainStatus(tone: string, glyph: string, text: string): HTMLSpanElement {
  return h('span', { class: `status status-${tone}` }, h('span', { class: 'status-glyph', 'aria-hidden': 'true' }, glyph), h('span', { class: 'status-text' }, text))
}

export class ProjectDetailPage extends RcPage {
  #project!: ResourceHandle<ProjectData>
  #caps!: ResourceHandle<GetCapabilitiesResponse>
  #view!: ResourceView<ProjectData>

  // The management cards. Each is made the first time its capability is
  // granted, and only hidden afterwards, so a card that holds a half-typed
  // form keeps it across a capability refresh.
  #visibility: VisibilityCard | null = null
  #settings: SettingsCard | null = null
  #access: AccessCard | null = null
  #webhooks: RotationCard | null = null
  #vcs: RotationCard | null = null
  #events: EventSettingsCard | null = null
  #schedules: SchedulesCard | null = null
  #anchors: Record<'visibility' | 'settings' | 'events' | 'schedules' | 'access' | 'webhooks' | 'vcs', Comment> | null = null

  #id(): string {
    return this.route().id ?? ''
  }

  protected build(): void {
    this.#view = new ResourceView<ProjectData>({ content: () => this.#content() })
    this.append(
      h(
        'div',
        { class: 'page' },
        h('div', { style: { marginBottom: 'var(--space-4)' } }, h('a', { href: href('/projects'), class: 'meta' }, '← All projects')),
        this.#view.el,
      ),
    )
  }

  protected connected(): void {
    this.#project = useProject(this, () => this.#id())
    // PROJECT-scoped capabilities, asked of the coordinator for this project.
    //
    // The session's capability set is the global scope, which is all-false for
    // everyone but a global admin. A project owner and the owning org's admins
    // may manage this project and show up nowhere globally, so the old gate
    // (`session.capabilities.manageProjectSettings`) hid every card from exactly
    // the people meant to use them. Nothing here is a security control: the
    // coordinator authorizes each operation again.
    this.#caps = useCapabilities(this, () => ({ projectId: this.#id() }))
  }

  #can(capability: Capability): boolean {
    return Boolean(this.#caps.state().data?.[capability])
  }

  render(): void {
    this.#project.sync()
    this.#caps.sync()
    this.#view.update(this.#project.state())
  }

  #content(): { node: Node; update(data: ProjectData): void } {
    const title = h('h1')
    const visibility = visibilityBadge(false)
    const disabled = plainStatus('neutral', '⊘', 'Disabled')
    const repo = h('p', { class: 'meta' })
    const projectId = h('dd', { class: 'mono truncate' })
    const orgId = h('dd', { class: 'mono truncate' })
    const branches = h('dd')
    const events = h('dd')

    const anchors = {
      visibility: document.createComment('visibility'),
      settings: document.createComment('settings'),
      events: document.createComment('events'),
      schedules: document.createComment('schedules'),
      access: document.createComment('access'),
      webhooks: document.createComment('webhooks'),
      vcs: document.createComment('vcs'),
    }
    this.#anchors = anchors

    const node = h(
      'div',
      { class: 'project-detail' },
      h('div', { class: 'page-header' }, h('div', null, h('div', { class: 'row' }, title, visibility, disabled), repo)),
      anchors.visibility,
      h(
        'div',
        { class: 'card' },
        h(
          'dl',
          { class: 'grid-dl' },
          h('div', null, h('dt', null, 'Project ID'), projectId),
          h('div', null, h('dt', null, 'Organization'), orgId),
          h('div', null, h('dt', null, 'Target branches'), branches),
          h('div', null, h('dt', null, 'Allowed events'), events),
        ),
      ),
      anchors.settings,
      anchors.events,
      anchors.schedules,
      anchors.access,
      anchors.webhooks,
      anchors.vcs,
    )

    return {
      node,
      update: ({ project }) => {
        setText(title, project.name)
        setVisibility(visibility, project.isPrivate)
        show(disabled, !project.enabled)
        setText(repo, project.repoUrl)
        setText(projectId, project.projectId)
        setText(orgId, project.orgId)
        setText(branches, project.targetBranches.join(', ') || 'Every branch')
        setText(events, project.allowedEventTypes.join(', ') || 'Default set')
        this.#syncCards(project)
      },
    }
  }

  #syncCards(project: ProjectDetail): void {
    const anchors = this.#anchors!
    const refresh = (): Promise<void> => this.#project.refresh()

    const canManage = this.#can('manageProjectSettings')
    if (canManage && !this.#visibility) {
      this.#visibility = make<VisibilityCard>('rc-project-visibility-card', { onChanged: refresh })
      anchors.visibility.before(this.#visibility)
    }
    if (canManage && !this.#settings) {
      // The settings form takes its first values from the project HERE, once.
      // Later refreshes do not reach the form's fields, so a project_update
      // arriving mid-edit cannot throw away what somebody is typing.
      this.#settings = make<SettingsCard>('rc-project-settings-card', { initial: project, onSaved: refresh })
      anchors.settings.before(this.#settings)
    }
    if (canManage && !this.#events) {
      // Seeded once, like the settings form, so a refresh keeps a draft.
      this.#events = make<EventSettingsCard>('rc-project-event-settings-card', { initial: project, onSaved: refresh })
      anchors.events.before(this.#events)
    }
    if (canManage && !this.#schedules) {
      this.#schedules = make<SchedulesCard>('rc-project-schedules-card', {})
      anchors.schedules.before(this.#schedules)
    }
    if (this.#can('manageGroups') && !this.#access) {
      this.#access = make<AccessCard>('rc-project-access-card', {})
      anchors.access.before(this.#access)
    }
    if (this.#can('manageWebhookSecrets') && !this.#webhooks) {
      this.#webhooks = make<RotationCard>('rc-project-rotation-card', {
        config: {
          title: 'Webhook secrets',
          noun: 'webhook secret',
          purpose:
            'A webhook secret is what this project uses to verify the signature on each incoming webhook from that provider. An event whose signature matches no active secret is rejected.',
          valueLabel: 'Secret',
          list: async (projectId) => (await api.listWebhookSecrets({ projectId })).secrets as RotationRow[],
          add: (request) => api.addWebhookSecret(request),
          deactivate: (id) => api.deactivateWebhookSecret({ id }),
          remove: (id) => api.deleteWebhookSecret({ id }),
        },
      })
      anchors.webhooks.before(this.#webhooks)
    }
    if (this.#can('manageVcsCredentials') && !this.#vcs) {
      this.#vcs = make<RotationCard>('rc-project-rotation-card', {
        config: {
          title: 'VCS credentials',
          noun: 'VCS credential',
          purpose:
            'A VCS credential is the token this project uses to clone the repository and to post commit statuses back to the provider.',
          valueLabel: 'Token',
          list: async (projectId) => (await api.listVcsCredentials({ projectId })).credentials as RotationRow[],
          add: (request) => api.addVcsCredential(request),
          deactivate: (id) => api.deactivateVcsCredential({ id }),
          remove: (id) => api.deleteVcsCredential({ id }),
        },
      })
      anchors.vcs.before(this.#vcs)
    }

    const cards: [ProjectCard | null, boolean][] = [
      [this.#visibility, canManage],
      [this.#settings, canManage],
      [this.#events, canManage],
      [this.#schedules, canManage],
      [this.#access, this.#can('manageGroups')],
      [this.#webhooks, this.#can('manageWebhookSecrets')],
      [this.#vcs, this.#can('manageVcsCredentials')],
    ]
    for (const [card, allowed] of cards) {
      if (!card) continue
      show(card, allowed)
      card.setProject(project)
    }
  }
}

const make = makeCard

/**
 * Changing a project between public and private.
 *
 * Deliberately its own card with its own confirmed action, rather than a
 * checkbox at the bottom of a long settings form. Making a project public
 * exposes every job, workflow and log it owns to anyone who can reach the
 * instance -- that is not a change to make by accident while editing a runner
 * image, and it is not one whose consequence should have to be remembered.
 */
class VisibilityCard extends ProjectCard {
  onChanged: () => Promise<void> = async () => {}
  #text!: HTMLParagraphElement
  #button!: PushButton
  #error = new ActionError()
  #busy = false

  protected build(): void {
    this.#text = h('p', { class: 'meta' })
    this.#button = button('', { small: true, onClick: () => void this.#change() })
    this.append(h('div', { class: 'card-header' }, h('div', null, h('h3', null, 'Visibility'), this.#text), this.#button), this.#error.el)
  }

  async #change(): Promise<void> {
    const project = this.project
    if (!project) return
    const target = !project.isPrivate
    const warning = target
      ? `Make "${project.name}" private? Its jobs, workflows and logs will become visible only to its members and administrators.`
      : `Make "${project.name}" PUBLIC? Everyone who can reach this instance will be able to read its jobs, workflows and logs, including anyone not signed in.`

    if (!(await confirmAction(warning, { okLabel: target ? 'Make private' : 'Make public', danger: !target }))) return
    if (!this.isActive()) return

    this.#busy = true
    this.#error.set(undefined)
    this.invalidate()
    try {
      await api.updateProject({ projectId: project.projectId, isPrivate: target })
      await this.onChanged()
    } catch (cause) {
      this.#error.set(cause instanceof ServiceError ? cause.message : 'The visibility could not be changed.')
    } finally {
      this.#busy = false
      this.invalidate()
    }
  }

  render(): void {
    const project = this.project
    if (!project) return
    const goingPublic = project.isPrivate
    setText(
      this.#text,
      project.isPrivate
        ? "Only this project's members, its organization's administrators, and global administrators can see its jobs, workflows and logs."
        : "Anyone who can reach this instance can read this project's jobs, workflows and logs — including signed-out visitors.",
    )
    this.#button.variant = goingPublic ? 'danger' : 'default'
    this.#button.disabled = this.#busy
    setButtonText(this.#button, goingPublic ? 'Make public' : 'Make private')
  }
}
define('rc-project-visibility-card', VisibilityCard)

class SettingsCard extends ProjectCard {
  initial!: ProjectDetail
  onSaved: () => Promise<void> = async () => {}

  #metadata!: ResourceHandle<DescribeFormMetadataResponse>
  #name!: TextField
  #nameField!: Field
  #description!: TextField
  #branches!: TextField
  #eventTypes!: ChoiceMultiSelect
  #checkoutMode!: ChoiceSelect
  #runnerImage!: TextField
  #enabled!: CheckBox
  #submit!: PushButton
  #error = new ActionError()
  #saved!: HTMLDivElement

  // The draft. Seeded once from the project; only the person changes it.
  #draftEvents: string[] = []
  #draftCheckout = ''
  #busy = false

  protected build(): void {
    const p = this.initial
    this.#draftEvents = [...p.allowedEventTypes]
    this.#draftCheckout = p.checkoutMode

    const rerender = (): void => this.invalidate()
    this.#name = textField({ value: p.name, onInput: rerender })
    this.#name.maxlength = 255
    this.#nameField = field({ label: 'Name', required: true, control: this.#name })
    this.#description = textField({ value: p.description })
    this.#description.maxlength = 255
    this.#branches = textField({ value: p.targetBranches.join(', ') })
    this.#eventTypes = new ChoiceMultiSelect({ values: this.#draftEvents, onChange: (values) => (this.#draftEvents = values) })
    this.#checkoutMode = new ChoiceSelect({
      value: this.#draftCheckout,
      placeholder: 'Use the coordinator default',
      onChange: (value) => (this.#draftCheckout = value),
    })
    this.#runnerImage = textField({ value: p.defaultRunnerImage, mono: true })

    const enabledHint = uid('hint')
    this.#enabled = checkBox('Enabled', { checked: p.enabled })
    setAttr(this.#enabled, 'aria-describedby', enabledHint)

    this.#submit = button('Save settings', { type: 'submit', variant: 'primary' })
    this.#saved = h('div', { class: 'alert alert-info', role: 'status', hidden: true }, 'Settings saved.')

    const form = h(
      'form',
      { novalidate: true, onSubmit: (event: Event) => void this.#submitForm(event) },
      this.#error.el,
      this.#saved,
      this.#nameField.el,
      field({ label: 'Description', control: this.#description }).el,
      field({ label: 'Target branches', hint: 'Comma separated. Empty accepts every branch.', control: this.#branches }).el,
      field({
        label: 'Allowed event types',
        hint: 'Which repository events start CI.',
        tooltip: 'Choosing none applies the default set: push, pull request opened, pull request updated, and tag created.',
        control: this.#eventTypes.el,
      }).el,
      field({ label: 'Pull-request checkout mode', control: this.#checkoutMode.el }).el,
      field({ label: 'Default runner image', hint: 'Used when a job does not name its own.', control: this.#runnerImage }).el,
      h(
        'div',
        { class: 'checkbox-row' },
        h('div', null, this.#enabled, h('div', { class: 'field-hint', id: enabledHint }, 'A disabled project ignores incoming webhook events.')),
      ),
      this.#submit,
    )
    this.append(h('h3', { class: 'section-title' }, 'Settings'), form)
  }

  protected connected(): void {
    this.#metadata = useFormMetadata(this)
  }

  #nameError(): string | undefined {
    return this.#name.value.trim() ? undefined : 'A project needs a name.'
  }

  async #submitForm(event: Event): Promise<void> {
    event.preventDefault()
    if (this.#nameError() || this.#busy || !this.project) return

    this.#busy = true
    this.#error.set(undefined)
    show(this.#saved, false)
    this.invalidate()
    try {
      await api.updateProject({
        projectId: this.project.projectId,
        name: this.#name.value.trim(),
        description: this.#description.value.trim(),
        enabled: this.#enabled.checked,
        targetBranches: this.#branches.value
          .split(',')
          .map((b) => b.trim())
          .filter(Boolean),
        allowedEventTypes: this.#draftEvents,
        checkoutMode: this.#draftCheckout || undefined,
        defaultRunnerImage: this.#runnerImage.value.trim() || undefined,
      })
      await this.onSaved()
      show(this.#saved, true)
    } catch (cause) {
      this.#error.set(cause instanceof ServiceError ? cause.message : 'The settings could not be saved.')
    } finally {
      this.#busy = false
      this.invalidate()
    }
  }

  render(): void {
    const metadata = this.#metadata.state().data
    this.#eventTypes.update(metadata?.eventTypes ?? [], this.#draftEvents)
    this.#checkoutMode.update(metadata?.checkoutModes ?? [], this.#draftCheckout)
    const nameError = this.#nameError()
    this.#nameField.setError(nameError)
    this.#submit.disabled = this.#busy || Boolean(nameError)
    setButtonText(this.#submit, this.#busy ? 'Saving…' : 'Save settings')
  }
}
define('rc-project-settings-card', SettingsCard)

/**
 * Who may see and manage this project.
 *
 * Roles granted here are PROJECT-scoped: `member` on this project sees this
 * project's private jobs and nothing else in the organization. That is the
 * grant an operator reaches for when "change who sees a private project" is
 * the task, and until this card existed it could only be done from the org
 * Roles page by pasting a project id and a user UUID.
 */

const PRINCIPAL_TYPES: EnumChoice[] = [
  { value: 'user', label: 'User', description: 'One person.' },
  { value: 'group', label: 'Group', description: 'Everyone in a group, now and in future.' },
]

const PROJECT_ROLES: EnumChoice[] = [
  { value: 'member', label: 'Member', description: 'Can see this project, including its private jobs, workflows and logs.' },
  { value: 'owner', label: 'Owner', description: 'Owns this project and its settings, but does not administer access to it.' },
  {
    value: 'admin',
    label: 'Admin',
    description: 'Full administration of this project, including who else may access it. Unusual on a single project; org admins already have this.',
  },
]

function roleLabel(role: string): string {
  return PROJECT_ROLES.find((choice) => choice.value === role)?.label ?? role
}

class AccessCard extends ProjectCard {
  // The list itself. A ServiceError (typically forbidden) is captured as a
  // message rather than thrown, so the card explains itself instead of the
  // whole page erroring.
  #assignments = new Loader<string, { rows: RoleAssignment[]; error: string | undefined }>(this, async (projectId) => {
    try {
      return { rows: (await api.listRoleAssignments({ scopeType: 'project', scopeId: projectId })).assignments, error: undefined }
    } catch (cause) {
      return { rows: [] as RoleAssignment[], error: describeError(cause, 'The access list could not be loaded.') }
    }
  })
  // Names for the ids. Fetched ONCE per org, not per row; a failure here only
  // means ids are shown raw, so it is swallowed.
  #users = new Loader<string, UserSummary[]>(this, async (orgId) => {
    try {
      return (await api.listUsers({ orgId })).users
    } catch {
      return [] as UserSummary[]
    }
  })
  #groups = new Loader<string, GroupSummary[]>(this, async (orgId) => {
    try {
      return (await api.listGroups({ orgId })).groups
    } catch {
      return [] as GroupSummary[]
    }
  })

  // Grant form
  #principalType = 'user'
  #principalId = ''
  #role = 'member'
  #grant = new Action(() => this.invalidate())
  #grantError = new ActionError()
  #typeSelect!: ChoiceSelect
  #roleSelect!: ChoiceSelect
  #userField!: HTMLElement
  #picker!: UserPicker
  #groupField!: HTMLElement
  #groupSelect!: ChoiceSelect
  #noGroups!: HTMLElement
  #grantButton!: PushButton

  // List
  #listError!: HTMLDivElement
  #empty!: HTMLDivElement
  #table!: HTMLDivElement
  #body!: HTMLTableSectionElement
  #rows = new Map<string, AssignmentRowView>()

  protected build(): void {
    this.#typeSelect = new ChoiceSelect({
      value: this.#principalType,
      choices: PRINCIPAL_TYPES,
      onChange: (value) => {
        this.#principalType = value
        // A user id is not a group id; a type change starts over.
        this.#setPrincipal('')
      },
    })
    this.#roleSelect = new ChoiceSelect({ value: this.#role, choices: PROJECT_ROLES, onChange: (value) => (this.#role = value) })

    this.#picker = userPicker(this.project?.orgId ?? '', '', (userId) => this.#setPrincipal(userId))
    this.#picker.setAttribute('role', 'group')
    this.#userField = field({
      label: 'User',
      required: true,
      hint: "Accounts known to this project's organization.",
      control: this.#picker,
    }).el

    this.#groupSelect = new ChoiceSelect({ placeholder: 'Choose a group', onChange: (value) => this.#setPrincipal(value) })
    this.#noGroups = h('div', { class: 'field-hint', hidden: true }, 'This organization has no groups.')
    this.#groupField = field({ label: 'Group', required: true, hint: "Groups belong to this project's organization.", control: this.#groupSelect.el }).el
    this.#groupField.append(this.#noGroups)

    this.#grantButton = button('Grant role', { type: 'submit', variant: 'primary' })

    const form = h(
      'form',
      { style: { marginBottom: 'var(--space-4)' }, onSubmit: (event: Event) => void this.#submitGrant(event) },
      this.#grantError.el,
      h(
        'div',
        { class: 'form-row' },
        field({ label: 'Grant to', required: true, control: this.#typeSelect.el }).el,
        field({
          label: 'Role',
          required: true,
          tooltip:
            "Member can see this project's private resources. Owner controls the project's settings but not who else may reach it. Admin administers access as well.",
          control: this.#roleSelect.el,
        }).el,
      ),
      this.#userField,
      this.#groupField,
      this.#grantButton,
    )

    this.#listError = h('div', { class: 'alert alert-error', role: 'alert', hidden: true })
    this.#empty = h(
      'div',
      { class: 'empty', hidden: true },
      "No roles are granted on this project. Only its organization's administrators and global administrators can see it while it is private.",
    )
    this.#body = h('tbody')
    this.#table = h(
      'div',
      { class: 'scroll-x', hidden: true },
      h(
        'table',
        { class: 'table' },
        h('thead', null, h('tr', null, h('th', null, 'Type'), h('th', null, 'Principal'), h('th', null, 'Role'), h('th', null, 'Granted'), h('th'))),
        this.#body,
      ),
    )

    this.append(
      h('h3', { class: 'section-title' }, 'Access'),
      h(
        'p',
        { class: 'meta', style: { marginBottom: 'var(--space-4)' } },
        "A private project is visible to its members, to the administrators of its organization, and to global administrators. Roles granted here apply to THIS project only; roles on the whole organization are managed on the Roles page.",
      ),
      form,
      this.#listError,
      this.#empty,
      this.#table,
    )
  }

  #setPrincipal(id: string): void {
    this.#principalId = id
    this.#picker.value = this.#principalType === 'user' ? id : ''
    this.invalidate()
  }

  async #submitGrant(event: Event): Promise<void> {
    event.preventDefault()
    const project = this.project
    if (!this.#principalId || !project) return
    const ok = await this.#grant.run('The role could not be granted.', () =>
      api.assignRole({
        principalType: this.#principalType,
        principalId: this.#principalId,
        scopeType: 'project',
        scopeId: project.projectId,
        role: this.#role,
      }),
    )
    if (ok && this.isActive()) {
      this.#setPrincipal('')
      void this.#assignments.reload()
    }
  }

  #principalName(assignment: RoleAssignment): string | undefined {
    if (assignment.principalType === 'group') {
      return (this.#groups.state.data ?? []).find((group) => group.groupId === assignment.principalId)?.name
    }
    const user = (this.#users.state.data ?? []).find((candidate) => candidate.userId === assignment.principalId)
    return user ? userLabel(user) : undefined
  }

  render(): void {
    const project = this.project
    if (!project) return
    this.#assignments.sync(project.projectId)
    this.#users.sync(project.orgId)
    this.#groups.sync(project.orgId)
    this.#picker.orgId = project.orgId

    // Grant form
    const groups = this.#groups.state.data ?? []
    const isGroup = this.#principalType === 'group'
    show(this.#userField, !isGroup)
    show(this.#groupField, isGroup)
    this.#groupSelect.update(
      groups.map((group) => ({ value: group.groupId, label: group.name, description: group.description ?? '' })),
      isGroup ? this.#principalId : '',
    )
    this.#groupSelect.el.disabled = groups.length === 0
    show(this.#noGroups, this.#groups.state.loaded && groups.length === 0)
    this.#grantError.set(this.#grant.error)
    this.#grantButton.disabled = this.#grant.busy || !this.#principalId
    setButtonText(this.#grantButton, this.#grant.busy ? 'Granting…' : 'Grant role')

    // List
    const list = this.#assignments.state.data
    this.#listError.hidden = !list?.error
    setText(this.#listError, list?.error ?? '')
    const ready = Boolean(list && !list.error)
    const rows = list?.rows ?? []
    show(this.#empty, ready && rows.length === 0)
    show(this.#table, ready && rows.length > 0)
    syncKeyed(
      this.#body,
      rows,
      (assignment) => assignment.assignmentId,
      (assignment) => {
        const view = new AssignmentRowView(() => void this.#assignments.reload())
        this.#rows.set(assignment.assignmentId, view)
        return view.el
      },
      (_el, assignment) => this.#rows.get(assignment.assignmentId)?.update(assignment, this.#principalName(assignment)),
    )
    const live = new Set(rows.map((row) => row.assignmentId))
    for (const key of [...this.#rows.keys()]) if (!live.has(key)) this.#rows.delete(key)
  }
}
define('rc-project-access-card', AccessCard)

class AssignmentRowView {
  readonly el: HTMLTableRowElement
  #assignment: RoleAssignment | null = null
  #name: string | undefined
  readonly #action: Action
  readonly #error = new ActionError()
  readonly #type = h('td')
  readonly #principal = h('td')
  readonly #role = h('td')
  readonly #granted = h('td', { class: 'meta' })
  readonly #revoke: PushButton

  constructor(onRevoked: () => void) {
    this.#action = new Action(() => this.#paint())
    this.#revoke = button('Revoke', {
      small: true,
      variant: 'danger',
      onClick: async () => {
        const assignment = this.#assignment
        if (!assignment) return
        if (!(await confirmAction('Revoke this role? Access it granted is removed immediately.', { okLabel: 'Revoke' }))) return
        const ok = await this.#action.run('The role could not be revoked.', () => api.revokeRole({ assignmentId: assignment.assignmentId }))
        if (ok) onRevoked()
      },
    })
    this.el = h('tr', null, this.#type, this.#principal, this.#role, this.#granted, h('td', null, this.#revoke, this.#error.el))
  }

  update(assignment: RoleAssignment, name: string | undefined): void {
    this.#assignment = assignment
    this.#name = name
    this.#paint()
  }

  #paint(): void {
    const assignment = this.#assignment
    if (!assignment) return
    setText(this.#type, assignment.principalType === 'group' ? 'Group' : 'User')
    const key = `${this.#name ?? ''}\u0000${assignment.principalId}`
    if (this.#principal.dataset.shown !== key) {
      this.#principal.dataset.shown = key
      this.#principal.replaceChildren(
        ...(this.#name
          ? [h('div', null, this.#name), h('div', { class: 'meta mono truncate' }, assignment.principalId)]
          : [h('span', { class: 'mono truncate' }, assignment.principalId)]),
      )
    }
    setText(this.#role, roleLabel(assignment.role))
    setText(this.#granted, relativeTime(assignment.createdAt))
    this.#revoke.disabled = this.#action.busy
    this.#error.set(this.#action.error)
  }
}

/**
 * Webhook secrets and VCS credentials.
 *
 * One implementation for both, because the coordinator models them
 * identically: a write-only value with a provider, a name, an active flag and
 * a last-used time. The value is never returned by any operation, and this
 * component never holds it past the request that stores it.
 *
 * Several rows may be active at once, on purpose: rotation is "add the new
 * one, switch the provider over, then deactivate the old one", and a
 * one-active-at-a-time rule would force a window in which webhooks fail.
 */

interface RotationRow {
  id: string
  provider: string
  name: string
  isActive: boolean
  lastUsedAt?: string
  createdAt: string
  deactivatedAt?: string
}

interface RotationConfig {
  title: string
  /** Lower-case singular, for sentences: "webhook secret". */
  noun: string
  /** One or two sentences on what the value does. */
  purpose: string
  /** Label for the write-only value field: "Secret" or "Token". */
  valueLabel: string
  list: (projectId: string) => Promise<RotationRow[]>
  add: (request: { projectId: string; provider: string; name: string; value: string }) => Promise<unknown>
  deactivate: (id: string) => Promise<unknown>
  remove: (id: string) => Promise<unknown>
}

class RotationCard extends ProjectCard {
  config!: RotationConfig

  #metadata!: ResourceHandle<DescribeFormMetadataResponse>
  #rows = new Loader<string, { rows: RotationRow[]; error: string | undefined }>(this, async (projectId) => {
    try {
      return { rows: await this.config.list(projectId), error: undefined }
    } catch (cause) {
      return { rows: [] as RotationRow[], error: describeError(cause, `The ${this.config.noun}s could not be loaded.`) }
    }
  })

  #provider = ''
  #providerSelect!: ChoiceSelect
  #name!: TextField
  #value!: TextField
  #submit!: PushButton
  #action = new Action(() => this.invalidate())
  #actionError = new ActionError()
  #added!: HTMLDivElement

  #listError!: HTMLDivElement
  #empty!: HTMLDivElement
  #table!: HTMLDivElement
  #body!: HTMLTableSectionElement
  #rowViews = new Map<string, RotationRowView>()

  protected build(): void {
    const c = this.config
    const rerender = (): void => this.invalidate()
    this.#providerSelect = new ChoiceSelect({
      placeholder: 'Choose a provider',
      onChange: (value) => {
        this.#provider = value
        this.invalidate()
      },
    })
    this.#name = textField({ onInput: rerender })
    this.#name.maxlength = 255
    this.#value = textField({ type: 'password', autocomplete: 'off', mono: true, onInput: rerender })
    this.#submit = button(`Add ${c.noun}`, { type: 'submit', variant: 'primary' })
    this.#added = h('div', { class: 'alert alert-info', role: 'status', hidden: true }, `The ${c.noun} was stored. Its value is not shown again.`)

    const form = h(
      'form',
      { style: { marginBottom: 'var(--space-4)' }, autocomplete: 'off', onSubmit: (event: Event) => void this.#submitForm(event) },
      this.#actionError.el,
      this.#added,
      h(
        'div',
        { class: 'form-row' },
        field({ label: 'Provider', required: true, control: this.#providerSelect.el }).el,
        field({
          label: 'Name',
          required: true,
          hint: 'Something that tells this one from the next: the date, or the account it belongs to.',
          control: this.#name,
        }).el,
      ),
      field({ label: c.valueLabel, required: true, hint: 'Stored encrypted. It is never shown again.', control: this.#value }).el,
      this.#submit,
    )

    this.#listError = h('div', { class: 'alert alert-error', role: 'alert', hidden: true })
    this.#empty = h('div', { class: 'empty', hidden: true }, `No ${c.noun}s yet.`)
    this.#body = h('tbody')
    this.#table = h(
      'div',
      { class: 'scroll-x', hidden: true },
      h(
        'table',
        { class: 'table' },
        h(
          'thead',
          null,
          h('tr', null, h('th', null, 'Provider'), h('th', null, 'Name'), h('th', null, 'Status'), h('th', null, 'Last used'), h('th', null, 'Created'), h('th')),
        ),
        this.#body,
      ),
    )

    this.append(
      h('h3', { class: 'section-title' }, c.title),
      h(
        'p',
        { class: 'meta', style: { marginBottom: 'var(--space-4)' } },
        `${c.purpose} More than one may be active at a time: to rotate, add the new one, then deactivate the old one once the provider is switched over.`,
      ),
      form,
      this.#listError,
      this.#empty,
      this.#table,
    )
  }

  protected connected(): void {
    this.#metadata = useFormMetadata(this)
  }

  #incomplete(): boolean {
    return !this.#provider || !this.#name.value.trim() || !this.#value.value
  }

  #providerLabel(id: string): string {
    return (this.#metadata.state().data?.vcsProviders ?? []).find((choice) => choice.value === id)?.label ?? id
  }

  async #submitForm(event: Event): Promise<void> {
    event.preventDefault()
    const project = this.project
    if (this.#incomplete() || !project) return
    show(this.#added, false)
    const value = this.#value.value
    const ok = await this.#action.run(`The ${this.config.noun} could not be added.`, () =>
      this.config.add({ projectId: project.projectId, provider: this.#provider, name: this.#name.value.trim(), value }),
    )
    // The value leaves memory whether or not the request succeeded, so a
    // failed submit cannot leave a secret sitting in a form field.
    clearField(this.#value)
    if (ok) {
      clearField(this.#name)
      show(this.#added, true)
      void this.#rows.reload()
    }
    this.invalidate()
  }

  render(): void {
    const project = this.project
    if (!project) return
    this.#rows.sync(project.projectId)

    this.#providerSelect.update(this.#metadata.state().data?.vcsProviders ?? [], this.#provider)
    this.#actionError.set(this.#action.error)
    this.#submit.disabled = this.#action.busy || this.#incomplete()
    setButtonText(this.#submit, this.#action.busy ? 'Adding…' : `Add ${this.config.noun}`)

    const list = this.#rows.state.data
    this.#listError.hidden = !list?.error
    setText(this.#listError, list?.error ?? '')
    const ready = Boolean(list && !list.error)
    const rows = list?.rows ?? []
    show(this.#empty, ready && rows.length === 0)
    show(this.#table, ready && rows.length > 0)
    syncKeyed(
      this.#body,
      rows,
      (row) => row.id,
      (row) => {
        const view = new RotationRowView(this.config, () => void this.#rows.reload())
        this.#rowViews.set(row.id, view)
        return view.el
      },
      (_el, row) => this.#rowViews.get(row.id)?.update(row, this.#providerLabel(row.provider)),
    )
    const live = new Set(rows.map((row) => row.id))
    for (const key of [...this.#rowViews.keys()]) if (!live.has(key)) this.#rowViews.delete(key)
  }
}
define('rc-project-rotation-card', RotationCard)

class RotationRowView {
  readonly el: HTMLTableRowElement
  #row: RotationRow | null = null
  #providerLabel = ''
  readonly #action: Action
  readonly #error = new ActionError()
  readonly #provider = h('td')
  readonly #name = h('td')
  readonly #active = plainStatus('ok', '●', 'Active')
  readonly #inactive = plainStatus('neutral', '⊘', '')
  readonly #lastUsed = h('td', { class: 'meta' })
  readonly #created = h('td', { class: 'meta' })
  readonly #deactivate: PushButton
  readonly #remove: PushButton

  constructor(config: RotationConfig, onChanged: () => void) {
    this.#action = new Action(() => this.#paint())
    this.#deactivate = button('Deactivate', {
      small: true,
      onClick: async () => {
        const row = this.#row
        if (!row) return
        const sure = await confirmAction(
          `Deactivate the ${config.noun} "${row.name}"? Anything still using it stops working. It stays listed and can be deleted later.`,
          { okLabel: 'Deactivate' },
        )
        if (!sure) return
        const ok = await this.#action.run(`The ${config.noun} could not be deactivated.`, () => config.deactivate(row.id))
        if (ok) onChanged()
      },
    })
    this.#remove = button('Delete', {
      small: true,
      variant: 'danger',
      onClick: async () => {
        const row = this.#row
        if (!row) return
        if (!(await confirmAction(`Delete the ${config.noun} "${row.name}"? This cannot be undone.`, { okLabel: 'Delete' }))) return
        const ok = await this.#action.run(`The ${config.noun} could not be deleted.`, () => config.remove(row.id))
        if (ok) onChanged()
      },
    })
    this.el = h(
      'tr',
      null,
      this.#provider,
      this.#name,
      h('td', null, this.#active, this.#inactive),
      this.#lastUsed,
      this.#created,
      h('td', null, h('div', { class: 'row' }, this.#deactivate, this.#remove), this.#error.el),
    )
  }

  update(row: RotationRow, providerLabel: string): void {
    this.#row = row
    this.#providerLabel = providerLabel
    this.#paint()
  }

  #paint(): void {
    const row = this.#row
    if (!row) return
    setText(this.#provider, this.#providerLabel)
    setText(this.#name, row.name)
    show(this.#active, row.isActive)
    show(this.#inactive, !row.isActive)
    setText(this.#inactive.querySelector('.status-text')!, `Deactivated ${relativeTime(row.deactivatedAt)}`)
    setText(this.#lastUsed, row.lastUsedAt ? relativeTime(row.lastUsedAt) : 'Never')
    setText(this.#created, relativeTime(row.createdAt))
    show(this.#deactivate, row.isActive)
    this.#deactivate.disabled = this.#action.busy
    this.#remove.disabled = this.#action.busy
    this.#error.set(this.#action.error)
  }
}

export const tag = 'rc-project-detail-page'
define(tag, ProjectDetailPage)
