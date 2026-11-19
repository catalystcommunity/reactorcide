import type { ProjectDetail } from '~/api/csilapi/types.gen.ts'
import { ProjectSubPage } from '~/components/ProjectSubPage.ts'
import { subscriptionsManager, type SubscriptionsManager } from '~/components/Subscriptions.ts'
import { define } from '~/ui/element.ts'

/**
 * Outbound event subscriptions of one project (docs/outbound-events.md). A
 * project owner manages the project and workflow subscriptions here; the
 * org and owner group scopes stay on the organization page.
 */
export class ProjectSubscriptionsPage extends ProjectSubPage {
  protected readonly suffix = 'subscriptions'
  protected readonly heading = 'Event subscriptions'
  protected readonly summary = 'Send the phase events of this project or one of its workflows to a webhook. Delivery is at least once, in order for each workflow.'

  #manager!: SubscriptionsManager

  protected buildBody(project: ProjectDetail): HTMLElement {
    this.#manager = subscriptionsManager({ orgId: project.orgId, projectId: project.projectId })
    return this.#manager
  }

  protected renderBody(project: ProjectDetail): void {
    this.#manager.setScope({ orgId: project.orgId, projectId: project.projectId })
  }
}

export const tag = 'rc-project-subscriptions-page'
define(tag, ProjectSubscriptionsPage)
