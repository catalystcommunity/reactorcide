import type { CheckBox, PushButton, TextField } from '@catalystcommunity/controls'
import { api, ServiceError } from '~/api/client.ts'
import type { DescribeFormMetadataResponse, OrgSummary } from '~/api/csilapi/types.gen.ts'
import { RcPage } from '~/app/page.ts'
import { ChoiceMultiSelect, ChoiceSelect } from '~/components/ChoiceInput.ts'
import { field, type Field } from '~/components/Field.ts'
import { ActionError, notPermitted } from '~/components/ManagementPage.ts'
import { OrgPicker, pickInitialOrg } from '~/components/OrgPicker.ts'
import { SESSION, adminOrgIds, canManageSomeOrg, session } from '~/lib/session.ts'
import type { ResourceHandle } from '~/store/index.ts'
import { useFormMetadata, useOrgs } from '~/store/resources.ts'
import { button, checkBox, setButtonText, textField } from '~/ui/controls.ts'
import { h, setAttr, show, uid } from '~/ui/dom.ts'
import { define } from '~/ui/element.ts'
import { ROUTE, navigate } from '~/ui/router.ts'

/**
 * The New Project form.
 *
 * The version this replaces was a column of bare text inputs. Nothing marked
 * which fields were required. "Allowed event types" was free text with a
 * placeholder reading "push, pull_request" -- and `pull_request` is not even a
 * valid value; the real ones are `pull_request_opened` and friends. So the
 * example in the form was wrong, and there was no way to discover the right
 * answer short of reading the Go source.
 *
 * Everything offered below comes from `describe-form-metadata`, which derives
 * it from the same constants the coordinator validates against. The form cannot
 * suggest a value the server would reject.
 *
 * The organization is a real choice, not the caller's user id. Projects live
 * in organization rows with their own ids; the caller's user id is not one of
 * them, and a project created there would belong to an org nobody administers.
 * The picker offers the orgs the caller administers (every org, for a global
 * admin) and is shown only when there is more than one.
 */
export class ProjectNewPage extends RcPage {
  static watches = [ROUTE, SESSION]

  #refused!: HTMLElement
  #page!: HTMLElement
  // Acquired only once the caller may create a project, so a refused visitor
  // sends no requests.
  #metadata: ResourceHandle<DescribeFormMetadataResponse> | null = null
  #orgs: ResourceHandle<{ orgs: OrgSummary[] }> | null = null

  #orgId = ''
  #eventTypes: string[] = []
  #checkoutMode = ''
  #busy = false

  #orgField!: HTMLElement
  #orgPicker!: OrgPicker
  #name!: TextField
  #nameField!: Field
  #repoUrl!: TextField
  #repoField!: Field
  #description!: TextField
  #branches!: TextField
  #eventSelect!: ChoiceMultiSelect
  #checkoutSelect!: ChoiceSelect
  #private!: CheckBox
  #submit!: PushButton
  #submitError = new ActionError()

  protected build(): void {
    this.#refused = notPermitted('projects')

    this.#orgPicker = new OrgPicker({ ariaLabel: 'Organization', onChange: (orgId) => ((this.#orgId = orgId), this.invalidate()) })
    this.#orgField = field({
      label: 'Organization',
      required: true,
      hint: 'The organization that will own this project.',
      control: this.#orgPicker.el,
      labelTarget: this.#orgPicker.combo,
    }).el

    this.#name = textField()
    this.#name.maxlength = 255
    this.#nameField = field({ label: 'Name', required: true, hint: 'How this project appears throughout the interface.', control: this.#name })

    this.#repoUrl = textField({ placeholder: 'https://github.com/org/repo.git' })
    this.#repoUrl.maxlength = 2048
    this.#repoField = field({
      label: 'Repository URL',
      required: true,
      hint: 'The repository this project builds.',
      tooltip:
        "Reactorcide clones this for the source it tests. Trusted CI content is configured separately, so a fork's pull request can never supply its own pipeline definition.",
      control: this.#repoUrl,
    })

    this.#description = textField()
    this.#description.maxlength = 255
    this.#branches = textField({ placeholder: 'main, develop' })

    this.#eventSelect = new ChoiceMultiSelect({ onChange: (values) => (this.#eventTypes = values) })
    this.#checkoutSelect = new ChoiceSelect({ placeholder: 'Use the coordinator default', onChange: (value) => (this.#checkoutMode = value) })

    const privateHint = uid('hint')
    this.#private = checkBox('Private')
    setAttr(this.#private, 'aria-describedby', privateHint)

    this.#submit = button('Create project', { type: 'submit', variant: 'primary' })

    const form = h(
      'form',
      { novalidate: true, onSubmit: (event: Event) => void this.#submitForm(event) },
      this.#submitError.el,
      this.#orgField,
      this.#nameField.el,
      this.#repoField.el,
      field({ label: 'Description', hint: 'One line, shown in the project list.', control: this.#description }).el,
      field({
        label: 'Target branches',
        hint: 'Comma separated. Leave empty to accept every branch.',
        tooltip: 'Only events targeting one of these branches start CI. An empty list means no branch filtering at all.',
        control: this.#branches,
      }).el,
      field({
        label: 'Allowed event types',
        hint: 'Which repository events start CI. Leave empty for the deployment default.',
        tooltip:
          'These are the events Reactorcide understands from a webhook. Choosing none applies the default set: push, pull request opened, pull request updated, and tag created.',
        control: this.#eventSelect.el,
      }).el,
      field({
        label: 'Pull-request checkout mode',
        hint: 'How pull-request jobs obtain the repository.',
        tooltip:
          'Isolated gives each job its own clone: slower, fully independent. Shared uses a common git object store: much faster checkouts on a large repository.',
        control: this.#checkoutSelect.el,
      }).el,
      h(
        'div',
        { class: 'checkbox-row' },
        h(
          'div',
          null,
          this.#private,
          h(
            'div',
            { class: 'field-hint', id: privateHint },
            "A private project's jobs, workflows and logs are visible only to its members, its organization's administrators, and global administrators. Leave this unchecked to use the organization's default.",
          ),
        ),
      ),
      h('div', { class: 'row' }, this.#submit, button('Cancel', { onClick: () => navigate('/projects') })),
    )

    this.#page = h('div', { class: 'page', style: { maxWidth: '42rem' } }, h('h1', { style: { marginBottom: 'var(--space-4)' } }, 'New project'), h('div', { class: 'card' }, form))
    this.append(this.#refused, this.#page)
  }

  protected connected(): void {
    // A reconnect must acquire again; the old handles were released on disconnect.
    this.#metadata = null
    this.#orgs = null
  }

  #allowedOrgs(): OrgSummary[] {
    const current = session()
    const allowed = (org: OrgSummary): boolean => Boolean(current?.is_global_admin) || adminOrgIds(current).includes(org.orgId)
    return (this.#orgs?.state().data?.orgs ?? []).filter(allowed)
  }

  /**
   * Client-side validation, for feedback only.
   *
   * The server validates independently and is the authority. This exists so a
   * mistake is caught before a round trip, not so the server can trust it.
   */
  #validate(): boolean {
    const name = this.#name.value.trim()
    const repo = this.#repoUrl.value.trim()
    const nameError = name ? undefined : 'A project needs a name.'
    let repoError: string | undefined
    if (!repo) repoError = 'A repository URL is required.'
    else if (!/^(https?:\/\/|git@|ssh:\/\/)/.test(repo)) repoError = 'This should start with https://, ssh:// or git@.'
    this.#nameField.setError(nameError)
    this.#repoField.setError(repoError)
    return !nameError && !repoError
  }

  async #submitForm(event: Event): Promise<void> {
    event.preventDefault()
    if (!this.#validate() || !this.#orgId || this.#busy) return

    this.#busy = true
    this.#submitError.set(undefined)
    this.invalidate()
    try {
      const response = await api.createProject({
        orgId: this.#orgId,
        name: this.#name.value.trim(),
        repoUrl: this.#repoUrl.value.trim(),
        description: this.#description.value.trim() || undefined,
        targetBranches: splitList(this.#branches.value),
        allowedEventTypes: this.#eventTypes,
        checkoutMode: this.#checkoutMode || undefined,
        // Only sent when explicitly chosen, so leaving it alone lets the
        // deployment's new_projects_private default apply.
        isPrivate: this.#private.checked ? true : undefined,
      })
      navigate(`/projects/${response.project.projectId}`)
    } catch (cause) {
      this.#submitError.set(cause instanceof ServiceError ? cause.message : 'The project could not be created.')
    } finally {
      this.#busy = false
      if (this.isActive()) this.invalidate()
    }
  }

  render(): void {
    const permitted = canManageSomeOrg(session())
    show(this.#refused, !permitted)
    show(this.#page, permitted)
    if (!permitted) return

    this.#metadata ??= useFormMetadata(this)
    this.#orgs ??= useOrgs(this)

    const orgs = this.#allowedOrgs()
    // The initial org is chosen once the list arrives, and only while nothing
    // has been picked: a refresh must not move the form off a chosen org.
    if (!this.#orgId && orgs.length > 0) this.#orgId = pickInitialOrg(orgs, () => true)
    show(this.#orgField, orgs.length > 1)
    this.#orgPicker.update(orgs, this.#orgId)

    const metadata = this.#metadata.state().data
    this.#eventSelect.update(metadata?.eventTypes ?? [], this.#eventTypes)
    this.#checkoutSelect.update(metadata?.checkoutModes ?? [], this.#checkoutMode)

    this.#submit.disabled = this.#busy || !this.#orgId
    setButtonText(this.#submit, this.#busy ? 'Creating…' : 'Create project')
  }
}

function splitList(value: string): string[] {
  return value
    .split(',')
    .map((item) => item.trim())
    .filter(Boolean)
}

export const tag = 'rc-project-new-page'
define(tag, ProjectNewPage)
