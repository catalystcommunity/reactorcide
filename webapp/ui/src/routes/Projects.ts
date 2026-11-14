import type { ProjectSummary } from '~/api/csilapi/types.gen.ts'
import { RcPage } from '~/app/page.ts'
import { ResourceView, emptyState } from '~/components/States.ts'
import { setVisibility, visibilityBadge } from '~/components/StatusBadge.ts'
import { SESSION, canManageSomeOrg, session } from '~/lib/session.ts'
import type { ResourceHandle } from '~/store/index.ts'
import { useProjects } from '~/store/resources.ts'
import { h, setAttr, setText, show, syncKeyed } from '~/ui/dom.ts'
import { define } from '~/ui/element.ts'
import { ROUTE, href } from '~/ui/router.ts'

type ProjectList = { projects: ProjectSummary[] }

export class ProjectsPage extends RcPage {
  static watches = [ROUTE, SESSION]

  #projects!: ResourceHandle<ProjectList>
  #newLink!: HTMLAnchorElement
  #view!: ResourceView<ProjectList>

  protected build(): void {
    this.#newLink = h('a', { href: href('/projects/new'), class: 'btn btn-primary btn-sm' }, 'New project')
    this.#view = new ResourceView<ProjectList>({
      isEmpty: (data) => data.projects.length === 0,
      empty: emptyState('No projects yet.'),
      content: () => projectTable(),
    })
    this.append(
      h(
        'div',
        { class: 'page' },
        h(
          'div',
          { class: 'page-header' },
          h('div', null, h('h1', null, 'Projects'), h('p', { class: 'meta' }, 'Repositories Reactorcide builds.')),
          this.#newLink,
        ),
        h('div', { class: 'card' }, this.#view.el),
      ),
    )
  }

  protected connected(): void {
    this.#projects = useProjects(this)
  }

  render(): void {
    // Anyone who administers an organization may create a project in it. The
    // session's capability set is the GLOBAL scope, so gating on
    // createProject there hid the link from every org admin.
    show(this.#newLink, canManageSomeOrg(session()))
    this.#view.update(this.#projects.state())
  }
}

function projectTable(): { node: Node; update(data: ProjectList): void } {
  const body = h('tbody')
  const node = h(
    'div',
    { class: 'scroll-x' },
    h(
      'table',
      { class: 'table' },
      h('thead', null, h('tr', null, h('th', null, 'Project'), h('th', null, 'Visibility'), h('th', null, 'Repository'), h('th', null, 'Enabled'))),
      body,
    ),
  )
  return {
    node,
    update: (data) =>
      syncKeyed(
        body,
        data.projects,
        (project) => project.projectId,
        () =>
          h(
            'tr',
            null,
            h('td', null, h('a', { class: 'project-link' }), h('div', { class: 'meta project-description' })),
            h('td', null, visibilityBadge(false)),
            h('td', { class: 'truncate meta project-repo' }),
            h('td', { class: 'project-enabled' }),
          ),
        (row, project) => {
          const link = row.querySelector<HTMLAnchorElement>('.project-link')!
          setAttr(link, 'href', href(`/projects/${project.projectId}`))
          setText(link, project.name)
          const description = row.querySelector<HTMLElement>('.project-description')!
          show(description, project.description)
          setText(description, project.description ?? '')
          setVisibility(row.querySelector('.status')!, project.isPrivate)
          setText(row.querySelector('.project-repo')!, project.repoUrl ?? '')
          setText(row.querySelector('.project-enabled')!, project.enabled ? 'Yes' : 'No')
        },
      ),
  }
}

export const tag = 'rc-projects-page'
define(tag, ProjectsPage)
