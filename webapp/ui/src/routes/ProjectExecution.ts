import type { PushButton } from '@catalystcommunity/controls'
import { api } from '~/api/client.ts'
import type { ProjectDetail } from '~/api/csilapi/types.gen.ts'
import { ChoiceSelect } from '~/components/ChoiceInput.ts'
import { field } from '~/components/Field.ts'
import { Action, ActionError } from '~/components/ManagementPage.ts'
import { oidcSettingsCard } from '~/components/Oidc.ts'
import { priorityLaneSettings } from '~/components/Priority.ts'
import { ProjectSubPage } from '~/components/ProjectSubPage.ts'
import { button } from '~/ui/controls.ts'
import { h, setText, show } from '~/ui/dom.ts'
import { RcElement, define } from '~/ui/element.ts'
import { EGRESS_CHOICES, RETRY_CHOICES } from '~/components/executionChoices.ts'

const PROJECT_RETRY_CHOICES = [{ value: 'inherit', label: 'Inherit from organization', description: 'Use the value of the organization.' }, ...RETRY_CHOICES]

/** The infra_retries and egress_control values a project form sends. */
export function executionRequest(
  project: ProjectDetail,
  retries: string,
  egress: string,
): { infraRetries?: number; infraRetriesInherit?: boolean; egressControl?: boolean } {
  const out: { infraRetries?: number; infraRetriesInherit?: boolean; egressControl?: boolean } = {}
  const stored = project.infraRetries === undefined ? 'inherit' : String(project.infraRetries)
  if (retries !== stored) {
    if (retries === 'inherit') out.infraRetriesInherit = true
    else out.infraRetries = Number(retries)
  }
  const egressOn = egress === 'on'
  if (egressOn !== Boolean(project.egressControl)) out.egressControl = egressOn
  return out
}

/**
 * Infra retries and egress control of one project (docs/runtime-behavior.md,
 * docs/egress-proxy.md). A project owner changes them. Egress control is on
 * when the organization, the project, or the execution profile turns it on;
 * a project cannot turn off the organization value.
 */
export class ExecutionSettingsCard extends RcElement {
  project: ProjectDetail | null = null
  #action = new Action(() => this.invalidate())
  #error = new ActionError()
  #retries!: ChoiceSelect
  #egress!: ChoiceSelect
  #retriesValue = 'inherit'
  #egressValue = 'off'
  #save!: PushButton
  #saved!: HTMLDivElement
  #state!: HTMLParagraphElement
  #seeded = ''

  setProject(project: ProjectDetail): void {
    if (this.project === project) return
    this.project = project
    this.invalidate()
  }

  connectedCallback(): void {
    this.classList.add('card')
    super.connectedCallback()
  }

  protected build(): void {
    this.#retries = new ChoiceSelect({ choices: PROJECT_RETRY_CHOICES, value: 'inherit', onChange: (v) => { this.#retriesValue = v; this.invalidate() } })
    this.#egress = new ChoiceSelect({ choices: EGRESS_CHOICES, value: 'off', onChange: (v) => { this.#egressValue = v; this.invalidate() } })
    this.#save = button('Save execution settings', { variant: 'primary', onClick: () => void this.#store() })
    this.#saved = h('div', { class: 'alert alert-info', role: 'status', hidden: true }, 'Execution settings saved.')
    this.#state = h('p', { class: 'meta execution-state' })
    this.append(
      h('h3', { class: 'section-title' }, 'Retries and egress'),
      this.#error.el,
      this.#saved,
      this.#state,
      h(
        'div',
        { class: 'form-row' },
        field({
          label: 'Infra retries',
          hint: 'How many times a job that failed for an infrastructure cause before its main container started runs again.',
          control: this.#retries.el,
        }).el,
        field({
          label: 'Egress control',
          hint: 'Send all traffic of the jobs of this project through the egress proxy. Grants with proxy delivery need it.',
          control: this.#egress.el,
        }).el,
      ),
      h('div', { class: 'row' }, this.#save),
    )
  }

  async #store(): Promise<void> {
    const project = this.project
    if (!project) return
    show(this.#saved, false)
    const change = executionRequest(project, this.#retriesValue, this.#egressValue)
    if (Object.keys(change).length === 0) {
      this.#action.error = 'Nothing changed.'
      this.invalidate()
      return
    }
    let response: ProjectDetail | undefined
    const ok = await this.#action.run('The execution settings could not be saved.', async () => {
      response = (await api.updateProject({ projectId: project.projectId, ...change })).project
    })
    if (!ok || !response || !this.isActive()) return
    this.#seeded = ''
    this.setProject(response)
    show(this.#saved, true)
  }

  render(): void {
    const project = this.project
    this.#error.set(this.#action.error)
    if (!project) return
    const key = JSON.stringify([project.projectId, project.infraRetries ?? null, project.egressControl ?? false])
    if (key !== this.#seeded) {
      this.#seeded = key
      this.#retriesValue = project.infraRetries === undefined ? 'inherit' : String(project.infraRetries)
      this.#egressValue = project.egressControl ? 'on' : 'off'
    }
    this.#retries.update(PROJECT_RETRY_CHOICES, this.#retriesValue)
    this.#egress.update(EGRESS_CHOICES, this.#egressValue)
    this.#retries.el.disabled = this.#action.busy
    this.#egress.el.disabled = this.#action.busy
    this.#save.disabled = this.#action.busy
    const retries = project.effectiveInfraRetries
    const egress = project.effectiveEgressControl
    setText(
      this.#state,
      `In force now: ${retries === undefined ? 'unknown' : retries} infra ${retries === 1 ? 'retry' : 'retries'}; egress control ${egress === undefined ? 'unknown' : egress ? 'on' : 'off'}${
        egress && !project.egressControl ? ' (from the organization)' : ''
      }.`,
    )
  }
}

export const executionSettingsCardTag = 'rc-project-execution-settings-card'
define(executionSettingsCardTag, ExecutionSettingsCard)

/** Execution settings of one project: retries, egress control, OIDC tokens, priority lanes. */
export class ProjectExecutionPage extends ProjectSubPage {
  protected readonly suffix = 'execution'
  protected readonly heading = 'Execution'
  protected readonly summary = 'Retries, egress control, OIDC tokens and priority lanes of the jobs of this project.'
  protected readonly needsOrg = true

  #settings: ExecutionSettingsCard | null = null

  protected buildBody(project: ProjectDetail, organization: string): HTMLElement {
    this.#settings = document.createElement(executionSettingsCardTag) as ExecutionSettingsCard
    this.#settings.setProject(project)
    return h('div', null, this.#settings, oidcSettingsCard(organization, project.projectId), priorityLaneSettings(organization, project.projectId))
  }

  protected renderBody(project: ProjectDetail): void {
    this.#settings?.setProject(project)
  }
}

export const tag = 'rc-project-execution-page'
define(tag, ProjectExecutionPage)
