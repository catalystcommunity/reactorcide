import type { ProjectDetail } from '~/api/csilapi/types.gen.ts'
import { ProjectSubPage } from '~/components/ProjectSubPage.ts'
import { refClassSettings } from '~/components/RefClasses.ts'
import { ciTrustSettingsForm } from '~/components/TrustSettings.ts'
import { h } from '~/ui/dom.ts'
import { define } from '~/ui/element.ts'

/** The CI trust settings of one project: run gate, approvers, override, required reviews. */
export class ProjectTrustPage extends ProjectSubPage {
  protected readonly suffix = 'trust'
  protected readonly heading = 'CI trust'
  protected readonly summary = 'Decide which pull requests wait for approval, who approves them, and which reviews a change needs.'
  protected readonly needsOrg = true

  protected buildBody(project: ProjectDetail, organization: string): HTMLElement {
    return h('div', null, ciTrustSettingsForm(organization, project.projectId), refClassSettings(organization, project.projectId))
  }
}

export const tag = 'rc-project-trust-page'
define(tag, ProjectTrustPage)
