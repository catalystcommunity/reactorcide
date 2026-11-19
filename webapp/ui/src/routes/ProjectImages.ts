import type { ProjectDetail } from '~/api/csilapi/types.gen.ts'
import { imageAliasesPanel } from '~/components/ImageAliases.ts'
import { ProjectSubPage } from '~/components/ProjectSubPage.ts'
import { define } from '~/ui/element.ts'

/**
 * The image aliases of one project. A project alias replaces the
 * organization alias with the same name for the jobs of this project; the
 * organization aliases the project uses show as inherited. Needs the
 * project owner role.
 */
export class ProjectImagesPage extends ProjectSubPage {
  protected readonly suffix = 'images'
  protected readonly heading = 'Images'
  protected readonly summary = 'Image aliases of this project, and the organization aliases it uses.'
  protected readonly needsOrg = true

  protected buildBody(project: ProjectDetail, organization: string): HTMLElement {
    return imageAliasesPanel(organization, project.projectId)
  }
}

export const tag = 'rc-project-images-page'
define(tag, ProjectImagesPage)
