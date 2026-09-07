import { Show, type JSX } from 'solid-js'
import type { OrgSummary } from '~/api/csilapi/types.gen.ts'
import { ChoiceSelect } from './ChoiceInput.tsx'

/**
 * Which organization a management page is about.
 *
 * Projects, groups, roles and secrets all belong to an organization row with
 * its own id, and a person may administer several. The pages that used to
 * assume "the caller's own org" (their user id) managed an org that owns
 * nothing. This picker makes the org an explicit, visible choice, and hides
 * itself when there is only one so the common case costs no attention.
 */
export function OrgPicker(props: {
  id?: string
  orgs: OrgSummary[]
  value: string
  onChange: (orgId: string) => void
  describedBy?: string
}): JSX.Element {
  const choices = () =>
    props.orgs.map((org) => ({
      value: org.orgId,
      label: orgLabel(org),
      description: org.isPrivate ? 'Private organization.' : 'Public organization.',
    }))

  return (
    <Show
      when={props.orgs.length > 1}
      fallback={
        <span id={props.id} class="truncate">
          {orgLabel(props.orgs[0])}
        </span>
      }
    >
      <ChoiceSelect
        id={props.id}
        value={props.value}
        choices={choices()}
        describedBy={props.describedBy}
        onChange={props.onChange}
      />
    </Show>
  )
}

export function orgLabel(org: OrgSummary | undefined): string {
  if (!org) return ''
  const name = org.displayName || org.name
  return org.isDefault ? `${name} (default)` : name
}

/**
 * The org a page should open on: the default org when the caller may manage
 * it, else the first one they may. Stable across renders for a given list, so
 * a page does not jump organizations because the list refreshed.
 */
export function pickInitialOrg(orgs: OrgSummary[], allowed: (org: OrgSummary) => boolean): string {
  const usable = orgs.filter(allowed)
  return usable.find((org) => org.isDefault)?.orgId ?? usable[0]?.orgId ?? ''
}
