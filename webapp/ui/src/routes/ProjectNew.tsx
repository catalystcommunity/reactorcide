import { Show, createSignal, type JSX } from 'solid-js'
import { useNavigate } from '@solidjs/router'
import { api, ServiceError } from '~/api/client.ts'
import { useFormMetadata } from '~/store/resources.ts'
import { useSession } from '~/lib/session.tsx'
import { Field } from '~/components/Field.tsx'
import { ChoiceSelect, ChoiceMultiSelect } from '~/components/ChoiceInput.tsx'

/**
 * The New Project form.
 *
 * The version this replaces was a column of bare text inputs. Nothing marked
 * which fields were required. "Allowed event types" was free text with a
 * placeholder reading "push, pull_request" -- and `pull_request` is not even a
 * valid value; the real ones are `pull_request_opened` and friends. So the
 * example in the form was wrong, and there was no way to discover the right
 * answer short of reading the Go source.
 *
 * Everything offered below comes from `describe-form-metadata`, which derives
 * it from the same constants the coordinator validates against. The form cannot
 * suggest a value the server would reject.
 */

interface Errors {
  name?: string
  repoUrl?: string
}

export function ProjectNew(): JSX.Element {
  const navigate = useNavigate()
  const { session } = useSession()
  const metadata = useFormMetadata()

  const [name, setName] = createSignal('')
  const [repoUrl, setRepoUrl] = createSignal('')
  const [description, setDescription] = createSignal('')
  const [branches, setBranches] = createSignal('')
  const [eventTypes, setEventTypes] = createSignal<string[]>([])
  const [checkoutMode, setCheckoutMode] = createSignal('')
  const [isPrivate, setIsPrivate] = createSignal(false)

  const [errors, setErrors] = createSignal<Errors>({})
  const [submitError, setSubmitError] = createSignal<string>()
  const [busy, setBusy] = createSignal(false)

  /**
   * Client-side validation, for feedback only.
   *
   * The server validates independently and is the authority. This exists so a
   * mistake is caught before a round trip, not so the server can trust it.
   */
  const validate = (): boolean => {
    const next: Errors = {}
    if (!name().trim()) next.name = 'A project needs a name.'
    if (!repoUrl().trim()) {
      next.repoUrl = 'A repository URL is required.'
    } else if (!/^(https?:\/\/|git@|ssh:\/\/)/.test(repoUrl().trim())) {
      next.repoUrl = 'This should start with https://, ssh:// or git@.'
    }
    setErrors(next)
    return Object.keys(next).length === 0
  }

  const submit = async (event: Event) => {
    event.preventDefault()
    if (!validate()) return

    setBusy(true)
    setSubmitError(undefined)
    try {
      const response = await api.createProject({
        orgId: session()?.user_id ?? '',
        name: name().trim(),
        repoUrl: repoUrl().trim(),
        description: description().trim() || undefined,
        targetBranches: splitList(branches()),
        allowedEventTypes: eventTypes(),
        checkoutMode: checkoutMode() || undefined,
        // Only sent when explicitly chosen, so leaving it alone lets the
        // deployment's new_projects_private default apply.
        isPrivate: isPrivate() ? true : undefined,
      })
      navigate(`/projects/${response.project.projectId}`)
    } catch (cause) {
      setSubmitError(
        cause instanceof ServiceError ? cause.message : 'The project could not be created.',
      )
    } finally {
      setBusy(false)
    }
  }

  return (
    <div class="page" style={{ 'max-width': '42rem' }}>
      <h1 style={{ 'margin-bottom': 'var(--space-4)' }}>New project</h1>

      <div class="card">
        <form onSubmit={submit} novalidate>
          <Show when={submitError()}>
            <div class="alert alert-error" role="alert">
              {submitError()}
            </div>
          </Show>

          <Field
            label="Name"
            required
            hint="How this project appears throughout the interface."
            error={errors().name}
          >
            {(ids) => (
              <input
                id={ids.id}
                class="input"
                type="text"
                maxlength="255"
                aria-describedby={ids.describedBy}
                aria-invalid={ids.invalid ? 'true' : undefined}
                value={name()}
                onInput={(event) => setName(event.currentTarget.value)}
              />
            )}
          </Field>

          <Field
            label="Repository URL"
            required
            hint="The repository this project builds."
            tooltip="Reactorcide clones this for the source it tests. Trusted CI content is configured separately, so a fork's pull request can never supply its own pipeline definition."
            error={errors().repoUrl}
          >
            {(ids) => (
              <input
                id={ids.id}
                class="input"
                type="text"
                maxlength="2048"
                placeholder="https://github.com/org/repo.git"
                aria-describedby={ids.describedBy}
                aria-invalid={ids.invalid ? 'true' : undefined}
                value={repoUrl()}
                onInput={(event) => setRepoUrl(event.currentTarget.value)}
              />
            )}
          </Field>

          <Field label="Description" hint="One line, shown in the project list.">
            {(ids) => (
              <input
                id={ids.id}
                class="input"
                type="text"
                maxlength="255"
                aria-describedby={ids.describedBy}
                value={description()}
                onInput={(event) => setDescription(event.currentTarget.value)}
              />
            )}
          </Field>

          <Field
            label="Target branches"
            hint="Comma separated. Leave empty to accept every branch."
            tooltip="Only events targeting one of these branches start CI. An empty list means no branch filtering at all."
          >
            {(ids) => (
              <input
                id={ids.id}
                class="input"
                type="text"
                placeholder="main, develop"
                aria-describedby={ids.describedBy}
                value={branches()}
                onInput={(event) => setBranches(event.currentTarget.value)}
              />
            )}
          </Field>

          <Field
            label="Allowed event types"
            hint="Which repository events start CI. Leave empty for the deployment default."
            tooltip="These are the events Reactorcide understands from a webhook. Choosing none applies the default set: push, pull request opened, pull request updated, and tag created."
          >
            {(ids) => (
              <ChoiceMultiSelect
                id={ids.id}
                values={eventTypes()}
                choices={metadata.state().data?.eventTypes ?? []}
                describedBy={ids.describedBy}
                placeholder="Search event types…"
                onChange={setEventTypes}
              />
            )}
          </Field>

          <Field
            label="Pull-request checkout mode"
            hint="How pull-request jobs obtain the repository."
            tooltip="Isolated gives each job its own clone: slower, fully independent. Shared uses a common git object store: much faster checkouts on a large repository."
          >
            {(ids) => (
              <ChoiceSelect
                id={ids.id}
                value={checkoutMode()}
                placeholder="Use the coordinator default"
                choices={metadata.state().data?.checkoutModes ?? []}
                describedBy={ids.describedBy}
                onChange={setCheckoutMode}
              />
            )}
          </Field>

          <div class="checkbox-row">
            <input
              id="is-private"
              type="checkbox"
              checked={isPrivate()}
              onChange={(event) => setIsPrivate(event.currentTarget.checked)}
            />
            <label for="is-private">
              <strong>Private</strong>
              <div class="field-hint">
                A private project's jobs, workflows and logs are visible only to its members, its
                organization's administrators, and global administrators. Leave this unchecked to
                use the organization's default.
              </div>
            </label>
          </div>

          <div class="row">
            <button type="submit" class="btn btn-primary" disabled={busy()}>
              {busy() ? 'Creating…' : 'Create project'}
            </button>
            <button type="button" class="btn" onClick={() => navigate('/projects')}>
              Cancel
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}

function splitList(value: string): string[] {
  return value
    .split(',')
    .map((item) => item.trim())
    .filter(Boolean)
}
