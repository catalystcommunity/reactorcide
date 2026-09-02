import { Show, createMemo, createSignal, type JSX } from 'solid-js'
import { A, useParams } from '@solidjs/router'
import { useProject, useFormMetadata } from '~/store/resources.ts'
import { api, ServiceError } from '~/api/client.ts'
import { ResourceView } from '~/components/States.tsx'
import { VisibilityBadge } from './Projects.tsx'
import { Field } from '~/components/Field.tsx'
import { ChoiceSelect, ChoiceMultiSelect } from '~/components/ChoiceInput.tsx'
import { useSession } from '~/lib/session.tsx'

export function ProjectDetail(): JSX.Element {
  const params = useParams<{ id: string }>()
  const { state, refresh } = useProject(() => params.id)
  const { session } = useSession()
  const canManage = () => Boolean(session()?.capabilities?.manageProjectSettings)

  return (
    <div class="page">
      <div style={{ 'margin-bottom': 'var(--space-4)' }}>
        <A href="/projects" class="meta">
          ← All projects
        </A>
      </div>

      <ResourceView state={state()}>
        {(data) => (
          <>
            <div class="page-header">
              <div>
                <div class="row">
                  <h1>{data().project.name}</h1>
                  <VisibilityBadge isPrivate={data().project.isPrivate} />
                  <Show when={!data().project.enabled}>
                    <span class="status status-neutral">
                      <span class="status-glyph" aria-hidden="true">
                        ⊘
                      </span>
                      Disabled
                    </span>
                  </Show>
                </div>
                <p class="meta">{data().project.repoUrl}</p>
              </div>
            </div>

            <Show when={canManage()}>
              <VisibilityCard project={data().project} onChanged={refresh} />
            </Show>

            <div class="card">
              <dl class="grid-dl">
                <div>
                  <dt>Project ID</dt>
                  <dd class="mono truncate">{data().project.projectId}</dd>
                </div>
                <div>
                  <dt>Organization</dt>
                  <dd class="mono truncate">{data().project.orgId}</dd>
                </div>
                <div>
                  <dt>Target branches</dt>
                  <dd>{data().project.targetBranches.join(', ') || 'Every branch'}</dd>
                </div>
                <div>
                  <dt>Allowed events</dt>
                  <dd>{data().project.allowedEventTypes.join(', ') || 'Default set'}</dd>
                </div>
              </dl>
            </div>

            <Show when={canManage()}>
              <SettingsCard project={data().project} onSaved={refresh} />
            </Show>
          </>
        )}
      </ResourceView>
    </div>
  )
}

/**
 * Changing a project between public and private.
 *
 * Deliberately its own card with its own confirmed action, rather than a
 * checkbox at the bottom of a long settings form. Making a project public
 * exposes every job, workflow and log it owns to anyone who can reach the
 * instance -- that is not a change to make by accident while editing a runner
 * image, and it is not one whose consequence should have to be remembered.
 */
function VisibilityCard(props: {
  project: { projectId: string; isPrivate: boolean; name: string }
  onChanged: () => Promise<void>
}): JSX.Element {
  const [busy, setBusy] = createSignal(false)
  const [error, setError] = createSignal<string>()

  const goingPublic = () => props.project.isPrivate

  const change = async () => {
    const target = !props.project.isPrivate
    const warning = target
      ? `Make "${props.project.name}" private? Its jobs, workflows and logs will become visible only to its members and administrators.`
      : `Make "${props.project.name}" PUBLIC? Everyone who can reach this instance will be able to read its jobs, workflows and logs, including anyone not signed in.`

    if (!confirm(warning)) return

    setBusy(true)
    setError(undefined)
    try {
      await api.updateProject({ projectId: props.project.projectId, isPrivate: target })
      await props.onChanged()
    } catch (cause) {
      setError(
        cause instanceof ServiceError ? cause.message : 'The visibility could not be changed.',
      )
    } finally {
      setBusy(false)
    }
  }

  return (
    <div class="card">
      <div class="card-header">
        <div>
          <h3>Visibility</h3>
          <p class="meta">
            <Show
              when={props.project.isPrivate}
              fallback="Anyone who can reach this instance can read this project's jobs, workflows and logs — including signed-out visitors."
            >
              Only this project's members, its organization's administrators, and global
              administrators can see its jobs, workflows and logs.
            </Show>
          </p>
        </div>
        <button
          type="button"
          class={`btn btn-sm ${goingPublic() ? 'btn-danger' : ''}`}
          disabled={busy()}
          onClick={() => void change()}
        >
          {goingPublic() ? 'Make public' : 'Make private'}
        </button>
      </div>
      <Show when={error()}>
        <div class="alert alert-error" role="alert">
          {error()}
        </div>
      </Show>
    </div>
  )
}

function SettingsCard(props: {
  project: {
    projectId: string
    name: string
    description: string
    enabled: boolean
    targetBranches: string[]
    allowedEventTypes: string[]
    checkoutMode: string
    defaultRunnerImage: string
    defaultQueueName: string
  }
  onSaved: () => Promise<void>
}): JSX.Element {
  const metadata = useFormMetadata()
  const [name, setName] = createSignal(props.project.name)
  const [description, setDescription] = createSignal(props.project.description)
  const [branches, setBranches] = createSignal(props.project.targetBranches.join(', '))
  const [eventTypes, setEventTypes] = createSignal([...props.project.allowedEventTypes])
  const [checkoutMode, setCheckoutMode] = createSignal(props.project.checkoutMode)
  const [enabled, setEnabled] = createSignal(props.project.enabled)
  const [runnerImage, setRunnerImage] = createSignal(props.project.defaultRunnerImage)

  const [busy, setBusy] = createSignal(false)
  const [error, setError] = createSignal<string>()
  const [saved, setSaved] = createSignal(false)
  const nameError = createMemo(() => (name().trim() ? undefined : 'A project needs a name.'))

  const submit = async (event: Event) => {
    event.preventDefault()
    if (nameError()) return

    setBusy(true)
    setError(undefined)
    setSaved(false)
    try {
      await api.updateProject({
        projectId: props.project.projectId,
        name: name().trim(),
        description: description().trim(),
        enabled: enabled(),
        targetBranches: branches()
          .split(',')
          .map((b) => b.trim())
          .filter(Boolean),
        allowedEventTypes: eventTypes(),
        checkoutMode: checkoutMode() || undefined,
        defaultRunnerImage: runnerImage().trim() || undefined,
      })
      await props.onSaved()
      setSaved(true)
    } catch (cause) {
      setError(cause instanceof ServiceError ? cause.message : 'The settings could not be saved.')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div class="card">
      <h3 class="section-title">Settings</h3>
      <form onSubmit={submit} novalidate>
        <Show when={error()}>
          <div class="alert alert-error" role="alert">
            {error()}
          </div>
        </Show>
        <Show when={saved()}>
          <div class="alert alert-info" role="status">
            Settings saved.
          </div>
        </Show>

        <Field label="Name" required error={nameError()}>
          {(ids) => (
            <input
              id={ids.id}
              class="input"
              type="text"
              maxlength="255"
              aria-invalid={ids.invalid ? 'true' : undefined}
              aria-describedby={ids.describedBy}
              value={name()}
              onInput={(event) => setName(event.currentTarget.value)}
            />
          )}
        </Field>

        <Field label="Description">
          {(ids) => (
            <input
              id={ids.id}
              class="input"
              type="text"
              maxlength="255"
              value={description()}
              onInput={(event) => setDescription(event.currentTarget.value)}
            />
          )}
        </Field>

        <Field label="Target branches" hint="Comma separated. Empty accepts every branch.">
          {(ids) => (
            <input
              id={ids.id}
              class="input"
              type="text"
              aria-describedby={ids.describedBy}
              value={branches()}
              onInput={(event) => setBranches(event.currentTarget.value)}
            />
          )}
        </Field>

        <Field
          label="Allowed event types"
          hint="Which repository events start CI."
          tooltip="Choosing none applies the default set: push, pull request opened, pull request updated, and tag created."
        >
          {(ids) => (
            <ChoiceMultiSelect
              id={ids.id}
              values={eventTypes()}
              choices={metadata.state().data?.eventTypes ?? []}
              describedBy={ids.describedBy}
              onChange={setEventTypes}
            />
          )}
        </Field>

        <Field label="Pull-request checkout mode">
          {(ids) => (
            <ChoiceSelect
              id={ids.id}
              value={checkoutMode()}
              placeholder="Use the coordinator default"
              choices={metadata.state().data?.checkoutModes ?? []}
              onChange={setCheckoutMode}
            />
          )}
        </Field>

        <Field label="Default runner image" hint="Used when a job does not name its own.">
          {(ids) => (
            <input
              id={ids.id}
              class="input mono"
              type="text"
              aria-describedby={ids.describedBy}
              value={runnerImage()}
              onInput={(event) => setRunnerImage(event.currentTarget.value)}
            />
          )}
        </Field>

        <div class="checkbox-row">
          <input
            id="project-enabled"
            type="checkbox"
            checked={enabled()}
            onChange={(event) => setEnabled(event.currentTarget.checked)}
          />
          <label for="project-enabled">
            <strong>Enabled</strong>
            <div class="field-hint">A disabled project ignores incoming webhook events.</div>
          </label>
        </div>

        <button type="submit" class="btn btn-primary" disabled={busy() || Boolean(nameError())}>
          {busy() ? 'Saving…' : 'Save settings'}
        </button>
      </form>
    </div>
  )
}
