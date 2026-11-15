import type { ProjectDetail } from '~/api/csilapi/types.gen.ts'
import { RcElement } from '~/ui/element.ts'

/** A card on the project page. The page hands it the current project on each refresh. */
export abstract class ProjectCard extends RcElement {
  protected project: ProjectDetail | null = null

  setProject(project: ProjectDetail): void {
    if (this.project === project) return
    this.project = project
    this.invalidate()
  }

  connectedCallback(): void {
    this.classList.add('card')
    super.connectedCallback()
  }
}

/** Makes a card element with its properties set before it connects. */
export function makeCard<T extends HTMLElement>(tag: string, props: Partial<T>): T {
  const el = document.createElement(tag) as T
  Object.assign(el, props)
  return el
}
