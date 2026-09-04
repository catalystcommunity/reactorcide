import { For, Show, type JSX } from 'solid-js'
import { A } from '@solidjs/router'
import { useProjects } from '~/store/resources.ts'
import { ResourceView, EmptyState } from '~/components/States.tsx'
import { useSession } from '~/lib/session.tsx'

export function Projects(): JSX.Element {
  const { state } = useProjects()
  const { session } = useSession()

  return (
    <div class="page">
      <div class="page-header">
        <div>
          <h1>Projects</h1>
          <p class="meta">Repositories Reactorcide builds.</p>
        </div>
        <Show when={session()?.capabilities?.createProject}>
          <A href="/projects/new" class="btn btn-primary btn-sm">
            New project
          </A>
        </Show>
      </div>

      <div class="card">
        <ResourceView
          state={state()}
          isEmpty={(data) => data.projects.length === 0}
          empty={<EmptyState title="No projects yet." />}
        >
          {(data) => (
            <div class="scroll-x">
              <table class="table">
                <thead>
                  <tr>
                    <th>Project</th>
                    <th>Visibility</th>
                    <th>Repository</th>
                    <th>Enabled</th>
                  </tr>
                </thead>
                <tbody>
                  <For each={data().projects}>
                    {(project) => (
                      <tr>
                        <td>
                          <A href={`/projects/${project.projectId}`}>{project.name}</A>
                          <Show when={project.description}>
                            <div class="meta">{project.description}</div>
                          </Show>
                        </td>
                        <td>
                          <VisibilityBadge isPrivate={project.isPrivate} />
                        </td>
                        <td class="truncate meta">{project.repoUrl}</td>
                        <td>{project.enabled ? 'Yes' : 'No'}</td>
                      </tr>
                    )}
                  </For>
                </tbody>
              </table>
            </div>
          )}
        </ResourceView>
      </div>
    </div>
  )
}

/**
 * Public or private, stated plainly.
 *
 * Visibility decides who can see a project's jobs and logs, so it gets a badge
 * of its own rather than being inferred from a checkbox buried in a settings
 * form.
 */
export function VisibilityBadge(props: { isPrivate: boolean }): JSX.Element {
  return (
    <Show
      when={props.isPrivate}
      fallback={
        <span class="status status-warn">
          <span class="status-glyph" aria-hidden="true">
            ◍
          </span>
          Public
        </span>
      }
    >
      <span class="status status-ok">
        <span class="status-glyph" aria-hidden="true">
          ⚿
        </span>
        Private
      </span>
    </Show>
  )
}
