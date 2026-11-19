import type { WorkflowOwnership } from '~/api/csilapi/types.gen.ts'
import { h, setAttr, setText, show } from '~/ui/dom.ts'

/**
 * The owner and contact of a workflow (docs/workflow-design.md "Owners and
 * contact"). The coordinator reads them from the TRUSTED CI definition when
 * the run is created and keeps that snapshot, so a pull request cannot change
 * what this shows for its own run.
 *
 * Build it one time; `update` changes the text in place.
 */
export class OwnerView {
  readonly el: HTMLDivElement
  readonly #owner: HTMLSpanElement
  readonly #contactRow: HTMLDivElement
  readonly #channel: HTMLSpanElement
  readonly #email: HTMLAnchorElement
  readonly #help: HTMLAnchorElement
  readonly #source: HTMLParagraphElement
  readonly #trust: HTMLSpanElement

  constructor() {
    this.#owner = h('span', { class: 'mono' })
    this.#trust = h('span', { class: 'meta owner-trust', hidden: true })
    this.#channel = h('span', { class: 'mono owner-channel' })
    this.#email = h('a', { class: 'mono owner-email' })
    this.#help = h('a', { class: 'owner-help', rel: 'noopener noreferrer', target: '_blank' }, 'Help')
    this.#contactRow = h('div', { class: 'owner-contact' }, h('span', { class: 'field-label' }, 'Contact '), this.#channel, ' ', this.#email, ' ', this.#help)
    this.#source = h('p', { class: 'meta owner-source' })
    this.el = h(
      'div',
      { class: 'owner', 'data-testid': 'workflow-owner' },
      h('div', null, h('span', { class: 'field-label owner-label' }, 'Owner '), this.#owner, ' ', this.#trust),
      this.#contactRow,
      this.#source,
    )
  }

  update(owner: WorkflowOwnership | undefined): void {
    // Only a snapshot from a trusted ref gives the owner authority (owner
    // approvals, workflow roles). A job copy does not record it.
    show(this.#trust, owner?.trusted !== undefined)
    setText(this.#trust, owner?.trusted ? '(trusted: from a trusted ref)' : owner?.trusted === false ? '(not trusted: gives no authority)' : '')
    if (!owner) {
      setText(this.#owner, 'Unknown')
      show(this.#contactRow, false)
      setText(this.#source, 'No trusted owner was recorded for this run.')
      return
    }
    const names = [owner.group, ...(owner.subjects ?? [])].filter((value): value is string => Boolean(value))
    setText(this.#owner, names.length > 0 ? names.join(', ') : 'None')
    const hasContact = Boolean(owner.channel || owner.email || owner.helpUrl)
    show(this.#contactRow, hasContact)
    show(this.#channel, owner.channel)
    setText(this.#channel, owner.channel ?? '')
    show(this.#email, owner.email)
    setText(this.#email, owner.email ?? '')
    setAttr(this.#email, 'href', owner.email ? `mailto:${owner.email}` : null)
    const helpUrl = owner.helpUrl && /^https?:\/\//.test(owner.helpUrl) ? owner.helpUrl : undefined
    show(this.#help, helpUrl)
    setAttr(this.#help, 'href', helpUrl ?? null)
    setText(this.#source, sourceText(owner))
  }
}

function sourceText(owner: WorkflowOwnership): string {
  const sha = owner.ciSha ? owner.ciSha.slice(0, 12) : ''
  switch (owner.source) {
    case 'ci':
      return sha ? `Read from the trusted CI definition at ${sha}.` : 'Read from the trusted CI definition.'
    case 'prior':
      return 'Copied from an earlier trusted run: the CI of this run was the change itself.'
    case 'parent':
      return 'Copied from the parent workflow.'
    case 'retry':
      return 'Copied from the run that this run retries.'
    default:
      return ''
  }
}
