import type { ActorSummary, WorkflowOwnership } from '~/api/csilapi/types.gen.ts'
import { h, setAttr, setText, show } from '~/ui/dom.ts'

/**
 * Small labels that several pages show: the required set state of a
 * workflow (docs/required-set.md), the actor of a run (docs/principals.md),
 * and the owner group (docs/workflow-design.md). Each maker builds one
 * element; each setter changes it in place.
 */

/** "advisory" or "quarantined" as a badge; hidden for a required workflow. */
export function requiredStateBadge(): HTMLSpanElement {
  return h('span', { class: 'status status-neutral required-state', hidden: true }, h('span', { class: 'status-glyph', 'aria-hidden': 'true' }), h('span', { class: 'status-text' }))
}

export function setRequiredState(el: HTMLElement, state: string | undefined, label: string | undefined): void {
  const visible = state === 'advisory' || state === 'quarantined'
  show(el, visible)
  if (!visible) return
  setAttr(el, 'class', `status ${state === 'quarantined' ? 'status-warn' : 'status-neutral'} required-state`)
  setText(el.querySelector('.status-glyph')!, state === 'quarantined' ? '⏸' : '○')
  setText(el.querySelector('.status-text')!, state === 'quarantined' ? 'Quarantined' : 'Advisory')
  setAttr(el, 'title', label ?? (state === 'quarantined' ? 'Quarantined: not required' : 'Advisory: not required'))
}

/** The display text of an actor: "Alice (human) via vcs:github/alice". */
export function actorText(actor: ActorSummary | undefined): string {
  if (!actor) return ''
  const name = actor.displayName || actor.principalId
  const kind = actor.kind ? ` (${actor.kind})` : ''
  return `${name}${kind}${actor.identity ? ` via ${actor.identity}` : ''}`
}

/** The short text of an actor for a list row: the name only. */
export function actorShort(actor: ActorSummary | undefined): string {
  if (!actor) return ''
  return actor.displayName || actor.principalId
}

/** The owner group of a run for a list row, or ''. */
export function ownerGroupText(owner: WorkflowOwnership | undefined): string {
  if (!owner) return ''
  return owner.group || (owner.subjects ?? []).join(', ')
}
