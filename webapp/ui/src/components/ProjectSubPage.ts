import type { OrgSummary, ProjectDetail } from '~/api/csilapi/types.gen.ts'
import { RcPage } from '~/app/page.ts'
import type { ResourceHandle } from '~/store/index.ts'
import { useOrgs, useProject } from '~/store/resources.ts'
import { h, setAttr, setText, show } from '~/ui/dom.ts'
import { href } from '~/ui/router.ts'
import { describeError } from './ManagementPage.ts'
import { skeleton } from './States.ts'

/** The project sub-pages, in the order of their tabs. */
export const PROJECT_TABS = [
  { suffix: '', label: 'Project' },
  { suffix: 'trust', label: 'CI trust' },
  { suffix: 'policy', label: 'CI policy' },
] as const

/** The app path of a project page or one of its sub-pages. */
export function projectPath(projectId: string, suffix = ''): string {
  const base = `/projects/${encodeURIComponent(projectId)}`
  return suffix ? `${base}/${suffix}` : base
}

/** The tab row that joins a project page to its sub-pages. */
export class ProjectTabs {
  readonly el: HTMLElement
  readonly #links: HTMLAnchorElement[]
  readonly #current: string

  constructor(current: string) {
    this.#current = current
    this.#links = PROJECT_TABS.map((tab) => h('a', { class: 'tab', role: 'tab', 'aria-selected': String(tab.suffix === current) }, tab.label))
    this.el = h('nav', { class: 'tabs', 'aria-label': 'Project pages' }, this.#links)
  }

  update(projectId: string): void {
    PROJECT_TABS.forEach((tab, i) => {
      setAttr(this.#links[i], 'href', href(projectPath(projectId, tab.suffix)))
      setAttr(this.#links[i], 'aria-current', tab.suffix === this.#current ? 'page' : null)
    })
  }
}

/**
 * A page about one project that is not the project page itself (CI trust,
 * CI policy). It loads the project, and the organization name when the page
 * needs it, then builds its body one time.
 */
export abstract class ProjectSubPage extends RcPage {
  protected abstract readonly suffix: string
  protected abstract readonly heading: string
  protected abstract readonly summary: string
  /** True when the body needs the organization NAME. */
  protected readonly needsOrg: boolean = false

  #project!: ResourceHandle<{ project: ProjectDetail }>
  #orgs!: ResourceHandle<{ orgs: OrgSummary[] }>
  #tabs!: ProjectTabs
  #back!: HTMLAnchorElement
  #title!: HTMLHeadingElement
  #loading!: HTMLElement
  #error!: HTMLDivElement
  #slot!: HTMLDivElement
  #built = false

  protected projectId(): string {
    return this.route().id ?? ''
  }

  protected build(): void {
    this.#tabs = new ProjectTabs(this.suffix)
    this.#back = h('a', { class: 'meta' }, '← Project')
    this.#title = h('h1')
    this.#loading = skeleton(3)
    this.#error = h('div', { class: 'alert alert-error', role: 'alert', hidden: true })
    this.#slot = h('div')
    this.append(
      h(
        'div',
        { class: 'page' },
        h('div', { style: { marginBottom: 'var(--space-4)' } }, this.#back),
        h('div', { class: 'page-header' }, h('div', null, this.#title, h('p', { class: 'meta' }, this.summary))),
        this.#tabs.el,
        this.#error,
        this.#loading,
        this.#slot,
      ),
    )
  }

  protected connected(): void {
    this.#project = useProject(this, () => this.projectId())
    if (this.needsOrg) this.#orgs = useOrgs(this)
  }

  /** Makes the body. Runs one time, when the project (and org) are known. */
  protected abstract buildBody(project: ProjectDetail, organization: string): HTMLElement

  /** Updates the body on each render after it is built. */
  protected renderBody(_project: ProjectDetail): void {}

  render(): void {
    this.#project.sync()
    this.#orgs?.sync()
    const id = this.projectId()
    this.#tabs.update(id)
    setAttr(this.#back, 'href', href(projectPath(id)))

    const projectState = this.#project.state()
    const project = projectState.data?.project
    setText(this.#title, project ? `${this.heading}: ${project.name}` : this.heading)

    let error: string | undefined
    let organization = ''
    if (projectState.error && !project) error = describeError(projectState.error, 'The project could not be loaded.')
    if (project && this.needsOrg) {
      const orgs = this.#orgs.state()
      organization = orgs.data?.orgs.find((org) => org.orgId === project.orgId)?.name ?? ''
      if (!organization && orgs.error) error = describeError(orgs.error, 'The organizations could not be loaded.')
      else if (!organization && orgs.loaded) error = "This project's organization is not visible to you. Ask an organization administrator for access."
    }
    const ready = Boolean(project) && (!this.needsOrg || Boolean(organization))
    show(this.#error, error)
    setText(this.#error, error ?? '')
    show(this.#loading, !ready && !error)

    if (ready && !this.#built) {
      this.#built = true
      this.#slot.append(this.buildBody(project!, organization))
    }
    show(this.#slot, ready)
    if (ready && this.#built) this.renderBody(project!)
  }
}
