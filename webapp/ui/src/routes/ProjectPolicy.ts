import type { ProjectDetail } from '~/api/csilapi/types.gen.ts'
import { ciPolicyEditor } from '~/components/PolicyEditor.ts'
import { ProjectSubPage } from '~/components/ProjectSubPage.ts'
import { define } from '~/ui/element.ts'

/** The CI policy of one project: when a pull request may run its own CI files. */
export class ProjectPolicyPage extends ProjectSubPage {
  protected readonly suffix = 'policy'
  protected readonly heading = 'CI policy'
  protected readonly summary = 'Decide when a pull request may run its own CI files, and what it runs with.'

  protected buildBody(project: ProjectDetail): HTMLElement {
    return ciPolicyEditor(project.projectId)
  }
}

export const tag = 'rc-project-policy-page'
define(tag, ProjectPolicyPage)
