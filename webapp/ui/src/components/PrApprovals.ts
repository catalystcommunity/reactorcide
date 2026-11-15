import type { PushButton } from '@catalystcommunity/controls'
import { ServiceError, api } from '~/api/client.ts'
import type { CiApprovalAction, GetPrApprovalsResponse, RequiredReviewState } from '~/api/csilapi/types.gen.ts'
import { ChoiceSelect } from '~/components/ChoiceInput.ts'
import { describeError } from '~/components/ManagementPage.ts'
import { button } from '~/ui/controls.ts'
import { h, setAttr, setClass, setText, show, syncKeyed } from '~/ui/dom.ts'
import { RcElement, define } from '~/ui/element.ts'
import { Loader } from '~/ui/load.ts'
import './chips.css'

/**
 * The approvals of one pull request, on its event page.
 *
 * It shows the current head, the required reviews and every approval action
 * on that head, and it runs the same commands as a PR comment. Each command
 * carries the head shown here, so a command from a stale page is refused
 * ("conflict") and does not apply to a head nobody looked at.
 */

/** The command words, as a PR comment writes them after /reactorcide. */
export const PR_COMMANDS = [
  { command: 'approve-ci', label: 'Approve CI', hint: 'Run the trusted base CI for this head.' },
  { command: 'override-ci', label: 'Override CI', hint: 'Run the CI files of this pull request.' },
  { command: 'accept', label: 'Accept', hint: 'Count toward the required reviews.' },
  { command: 'revoke', label: 'Revoke', hint: 'Take back your approvals on this head.' },
  { command: 'retry', label: 'Retry', hint: 'Run the failed jobs again.' },
  { command: 'rerun', label: 'Rerun', hint: 'Run all the workflows again.' },
] as const

/** An approval kind in the words of its command. */
export function kindLabel(kind: string): string {
  if (kind === 'run') return 'approve-ci'
  if (kind === 'head_ci') return 'override-ci'
  if (kind === 'accept') return 'accept'
  return kind
}

type Key = { projectId: string; prNumber: number }
type Loaded = { data?: GetPrApprovalsResponse; unavailable?: boolean }

export class PrApprovalsPanel extends RcElement {
  projectId = ''
  prNumber = 0

  #load = new Loader<Key, Loaded>(this, async (key) => {
    try {
      return { data: await api.getPrApprovals(key) }
    } catch (cause) {
      // The coordinator has no approval service, or cannot read the pull
      // request. The rest of the event page is still useful: hide quietly.
      if (cause instanceof ServiceError && cause.code === 'unavailable') return { unavailable: true }
      throw cause
    }
  })
  #label = ''
  #busy = false
  #message = ''
  #error = ''

  #head!: HTMLElement
  #summary!: HTMLParagraphElement
  #loadError!: HTMLDivElement
  #notice!: HTMLDivElement
  #errorBox!: HTMLDivElement
  #reviewsEmpty!: HTMLParagraphElement
  #reviewsBody!: HTMLTableSectionElement
  #reviewsTable!: HTMLDivElement
  #actionsEmpty!: HTMLParagraphElement
  #actionsBody!: HTMLTableSectionElement
  #actionsTable!: HTMLDivElement
  #labelSelect!: ChoiceSelect
  #buttons: PushButton[] = []

  setPullRequest(projectId: string, prNumber: number): void {
    if (this.projectId === projectId && this.prNumber === prNumber) return
    this.projectId = projectId
    this.prNumber = prNumber
    this.invalidate()
  }

  protected build(): void {
    this.classList.add('card')
    this.#head = h('code', { class: 'mono pr-head' })
    this.#summary = h('p', { class: 'meta pr-reviews-summary', hidden: true })
    this.#loadError = h('div', { class: 'alert alert-error', role: 'alert', hidden: true })
    this.#notice = h('div', { class: 'alert alert-info', role: 'status', hidden: true })
    this.#errorBox = h('div', { class: 'alert alert-error', role: 'alert', hidden: true })

    this.#reviewsEmpty = h('p', { class: 'meta' }, 'This pull request needs no reviews.')
    this.#reviewsBody = h('tbody')
    this.#reviewsTable = h(
      'div',
      { class: 'scroll-x' },
      h(
        'table',
        { class: 'table' },
        h('thead', null, h('tr', null, h('th', null, 'Review'), h('th', null, 'Scope'), h('th', null, 'Applies'), h('th', null, 'Accepted'), h('th', null, 'State'))),
        this.#reviewsBody,
      ),
    )

    this.#actionsEmpty = h('p', { class: 'meta' }, 'Nobody has acted on this head.')
    this.#actionsBody = h('tbody')
    this.#actionsTable = h(
      'div',
      { class: 'scroll-x' },
      h(
        'table',
        { class: 'table' },
        h(
          'thead',
          null,
          h('tr', null, h('th', null, 'Time'), h('th', null, 'Command'), h('th', null, 'Action'), h('th', null, 'Label'), h('th', null, 'Person'), h('th', null, 'Channel'), h('th', null, 'Subjects')),
        ),
        this.#actionsBody,
      ),
    )

    this.#labelSelect = new ChoiceSelect({
      ariaLabel: 'Accept label',
      placeholder: 'No label: accept for every review',
      onChange: (value) => (this.#label = value),
    })
    this.#buttons = PR_COMMANDS.map((c) =>
      button(c.label, { small: true, variant: c.command === 'revoke' ? 'danger' : 'default', title: c.hint, onClick: () => void this.#run(c.command) }),
    )

    this.append(
      h('div', { class: 'card-header' }, h('div', null, h('h3', null, 'Pull request approvals'), h('p', { class: 'meta' }, 'Head ', this.#head))),
      this.#loadError,
      this.#notice,
      this.#errorBox,
      h('h4', { class: 'section-title' }, 'Required reviews'),
      this.#summary,
      this.#reviewsEmpty,
      this.#reviewsTable,
      h('h4', { class: 'section-title' }, 'Actions on this head'),
      this.#actionsEmpty,
      this.#actionsTable,
      h(
        'div',
        { class: 'pr-command-bar' },
        h('span', { class: 'field-label' }, 'Accept label'),
        this.#labelSelect.el,
        this.#buttons,
      ),
      h('p', { class: 'meta' }, 'A command here is the same as a comment /reactorcide COMMAND on the pull request. The label applies only to Accept.'),
    )
  }

  #key(): Key | undefined {
    return this.projectId && this.prNumber > 0 ? { projectId: this.projectId, prNumber: this.prNumber } : undefined
  }

  async #run(command: string): Promise<void> {
    const data = this.#load.state.data?.data
    if (this.#busy || !data) return
    this.#busy = true
    this.#message = ''
    this.#error = ''
    this.invalidate()
    try {
      const request: Parameters<typeof api.prCommand>[0] = { projectId: this.projectId, prNumber: this.prNumber, command, headSha: data.headSha }
      if (command === 'accept' && this.#label) request.label = this.#label
      const response = await api.prCommand(request)
      this.#message = response.message
    } catch (cause) {
      if (cause instanceof ServiceError && cause.code === 'conflict') {
        this.#error = 'The pull request has a new head. The panel now shows it. Check the new head, then try again.'
      } else if (cause instanceof ServiceError && cause.isForbidden) {
        this.#error = `You cannot do this. ${cause.message}`
      } else {
        this.#error = describeError(cause, 'The command failed.')
      }
    } finally {
      this.#busy = false
    }
    if (!this.isActive()) return
    await this.#load.reload()
  }

  render(): void {
    this.#load.sync(this.#key())
    const state = this.#load.state
    const loaded = state.data
    // Hide quietly when the coordinator cannot serve approvals.
    // It shows after the first answer, so it does not flash before it hides.
    show(this, this.#key() && ((loaded && !loaded.unavailable) || state.error))
    const data = loaded?.data

    show(this.#loadError, state.error)
    setText(this.#loadError, state.error ? describeError(state.error, 'The approvals could not be loaded.') : '')
    show(this.#notice, this.#message)
    setText(this.#notice, this.#message)
    show(this.#errorBox, this.#error)
    setText(this.#errorBox, this.#error)

    setText(this.#head, data?.headSha ? data.headSha.slice(0, 12) : '—')
    setAttr(this.#head, 'title', data?.headSha ?? null)

    const reviews = data?.reviews ?? []
    const summary = data?.reviewsState ? `${data.reviewsState}${data.reviewsSummary ? `: ${data.reviewsSummary}` : ''}` : ''
    show(this.#summary, summary)
    setText(this.#summary, summary)
    show(this.#reviewsEmpty, data && reviews.length === 0)
    show(this.#reviewsTable, reviews.length > 0)
    syncKeyed(this.#reviewsBody, reviews, (r) => `${r.scope}:${r.id}`, createReviewRow, updateReviewRow)

    const actions = data?.actions ?? []
    show(this.#actionsEmpty, data && actions.length === 0)
    show(this.#actionsTable, actions.length > 0)
    syncKeyed(this.#actionsBody, actions, (a) => a.approvalId, createActionRow, updateActionRow)

    const ids = [...new Set(reviews.map((r) => r.id))]
    if (this.#label && !ids.includes(this.#label)) this.#label = ''
    this.#labelSelect.update(
      ids.map((id) => ({ value: id, label: id, description: reviews.find((r) => r.id === id)?.description ?? '' })),
      this.#label,
    )
    for (const b of this.#buttons) b.disabled = this.#busy || !data
  }
}

function createReviewRow(): HTMLTableRowElement {
  return h(
    'tr',
    null,
    h('td', null, h('div', { class: 'mono rv-id' }), h('div', { class: 'meta rv-desc' })),
    h('td', { class: 'rv-scope' }),
    h('td', { class: 'rv-applies' }),
    h('td', null, h('div', { class: 'rv-count' }), h('div', { class: 'meta mono rv-people' })),
    h('td', { class: 'rv-state' }),
  )
}

function updateReviewRow(row: HTMLTableRowElement, r: RequiredReviewState): void {
  setText(row.querySelector('.rv-id')!, r.id)
  const desc = row.querySelector<HTMLElement>('.rv-desc')!
  show(desc, r.description)
  setText(desc, r.description ?? '')
  setText(row.querySelector('.rv-scope')!, r.scope === 'org' ? 'Organization' : r.scope === 'project' ? 'Project' : r.scope)
  setText(row.querySelector('.rv-applies')!, r.applies ? 'Yes' : 'No: no matching file changed')
  setText(row.querySelector('.rv-count')!, `${r.accepted.length} of ${r.required} accepted`)
  setText(row.querySelector('.rv-people')!, r.accepted.join(', '))
  const stateCell = row.querySelector<HTMLElement>('.rv-state')!
  const done = r.satisfied || !r.applies
  setText(stateCell, !r.applies ? 'Not needed' : r.satisfied ? '✓ Satisfied' : '… Waiting')
  setClass(stateCell, 'review-state-satisfied', done)
  setClass(stateCell, 'review-state-open', !done)
}

function createActionRow(): HTMLTableRowElement {
  return h(
    'tr',
    null,
    h('td', { class: 'meta ac-time' }),
    h('td', { class: 'mono ac-kind' }),
    h('td', { class: 'ac-action' }),
    h('td', { class: 'mono ac-label' }),
    h('td', { class: 'mono ac-person' }),
    h('td', { class: 'ac-channel' }),
    h('td', { class: 'meta mono ac-subjects' }),
  )
}

function updateActionRow(row: HTMLTableRowElement, a: CiApprovalAction): void {
  const t = Date.parse(a.createdAt)
  setText(row.querySelector('.ac-time')!, Number.isNaN(t) ? a.createdAt : new Date(t).toLocaleString())
  setText(row.querySelector('.ac-kind')!, kindLabel(a.kind))
  setText(row.querySelector('.ac-action')!, a.action === 'approved' ? 'Approved' : a.action === 'revoked' ? 'Revoked' : a.action)
  setText(row.querySelector('.ac-label')!, a.label ?? '—')
  setText(row.querySelector('.ac-person')!, a.person)
  setText(row.querySelector('.ac-channel')!, a.channel)
  setText(row.querySelector('.ac-subjects')!, a.subjects.join(', '))
}

export const prApprovalsTag = 'rc-pr-approvals-panel'
define(prApprovalsTag, PrApprovalsPanel)

export function prApprovalsPanel(projectId: string, prNumber: number): PrApprovalsPanel {
  const el = document.createElement(prApprovalsTag) as PrApprovalsPanel
  el.projectId = projectId
  el.prNumber = prNumber
  return el
}
