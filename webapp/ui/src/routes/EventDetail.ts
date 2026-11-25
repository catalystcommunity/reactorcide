import type { CIEventWorkflowDecision, GetEventResponse } from '~/api/csilapi/types.gen.ts'
import { RcPage } from '~/app/page.ts'
import { OwnerView } from '~/components/Owner.ts'
import { requiredStateBadge, setRequiredState } from '~/components/Labels.ts'
import { prApprovalsPanel, type PrApprovalsPanel } from '~/components/PrApprovals.ts'
import { ReasonList, decisionBadge, setDecision } from '~/components/Reasons.ts'
import { refClassFactRows, refClassLabels, refClassReasonText, setRefClasses } from '~/components/RefClasses.ts'
import { ResourceView, emptyState, relativeTime } from '~/components/States.ts'
import { setStatus, statusBadge } from '~/components/StatusBadge.ts'
import type { ResourceHandle } from '~/store/index.ts'
import { useEvent } from '~/store/resources.ts'
import { fill, h, setAttr, setText, show, syncKeyed } from '~/ui/dom.ts'
import { define } from '~/ui/element.ts'
import { href } from '~/ui/router.ts'

/**
 * One CI event: what arrived, and what Reactorcide decided to run for it.
 *
 * The "Reactorcide Checks" commit status links here when an event created
 * zero or several workflows. Every workflow file the eval looked at is listed
 * with its decision and the reasons for it, so "why did nothing run?" and
 * "why did that run?" both have an answer on one page.
 */
export class EventDetailPage extends RcPage {
  #event!: ResourceHandle<GetEventResponse>
  #view!: ResourceView<GetEventResponse>

  #id(): string {
    return this.route().id ?? ''
  }

  protected build(): void {
    this.#view = new ResourceView<GetEventResponse>({ content: () => this.#content() })
    this.append(
      h(
        'div',
        { class: 'page' },
        h('div', { style: { marginBottom: 'var(--space-4)' } }, h('a', { href: href('/'), class: 'meta' }, '← All workflows')),
        this.#view.el,
      ),
    )
  }

  protected connected(): void {
    this.#event = useEvent(
      this,
      () => this.#id(),
      () => this.#event?.state().data,
    )
  }

  render(): void {
    this.#event.sync()
    this.#view.update(this.#event.state())
  }

  #content(): { node: Node; update(data: GetEventResponse): void } {
    const title = h('h1')
    const badge = statusBadge()
    const subtitle = h('p', { class: 'meta' })

    // Facts. Each row hides when the event has no value for it.
    const facts = new Map<string, { item: HTMLElement; value: HTMLElement }>()
    const fact = (key: string, label: string, mono = false): HTMLElement => {
      const value = h('dd', { class: mono ? 'mono truncate' : '' })
      const item = h('div', null, h('dt', null, label), value)
      facts.set(key, { item, value })
      return item
    }
    const setFact = (key: string, text: string | undefined, link?: string): void => {
      const f = facts.get(key)!
      show(f.item, text)
      if (!text) return
      if (link) {
        let a = f.value.querySelector('a')
        if (!a) {
          a = h('a')
          f.value.replaceChildren(a)
        }
        setAttr(a, 'href', link)
        setText(a, text)
      } else {
        setText(f.value, text)
      }
    }

    const decisionsEmpty = emptyState('No workflow decisions yet.', 'The eval records them when it finishes.')
    const decisionsList = h('ul', { class: 'decision-list' })
    const decisionsCount = h('span', { class: 'meta' })

    // Ref classes: the labels the coordinator gave the ref, the facts it
    // used, and the rule that gave each class.
    const classLabels = refClassLabels()
    const classFacts = h('dl', { class: 'grid-dl ref-class-facts' })
    const classReasons = h('ul', { class: 'reason-list' })
    const classesCard = h(
      'div',
      { class: 'card event-ref-classes' },
      h('h3', { class: 'section-title' }, 'Ref classes'),
      classLabels,
      classFacts,
      classReasons,
    )

    const filesCount = h('p', { class: 'meta' })
    const filesList = h('ul', { class: 'changed-files' })
    const filesCard = h('div', { class: 'card' }, h('h3', { class: 'section-title' }, 'Changed files'), filesCount, filesList)

    // The approvals of the pull request, made the first time the event has
    // one. It hides itself when the coordinator cannot serve approvals.
    const approvalsAnchor = document.createComment('approvals')
    let approvals: PrApprovalsPanel | null = null

    const node = h(
      'div',
      null,
      h('div', { class: 'page-header' }, h('div', null, h('div', { class: 'row' }, title, badge), subtitle)),
      h(
        'div',
        { class: 'card' },
        h(
          'dl',
          { class: 'grid-dl' },
          fact('id', 'Event ID', true),
          fact('source', 'Source'),
          fact('type', 'Event type'),
          fact('repo', 'Repository', true),
          fact('branch', 'Branch'),
          fact('ref', 'Ref', true),
          fact('commit', 'Commit', true),
          fact('base', 'Base commit', true),
          fact('pr', 'Pull request'),
          fact('actor', 'Actor'),
          fact('principal', 'Principal'),
          fact('schedule', 'Schedule', true),
          fact('received', 'Received'),
          fact('delivery', 'Delivery ID', true),
          fact('checks', 'Reactorcide Checks'),
          fact('eval', 'Eval job', true),
          fact('duplicate', 'Duplicate of', true),
        ),
      ),
      classesCard,
      approvalsAnchor,
      h(
        'div',
        { class: 'card' },
        h('div', { class: 'card-header' }, h('h3', null, 'Workflows'), decisionsCount),
        decisionsEmpty,
        decisionsList,
      ),
      filesCard,
    )

    return {
      node,
      update: ({ event, decisions }) => {
        const subject = event.prNumber ? `#${event.prNumber}` : event.branch || event.ref || ''
        setText(title, `Event ${event.eventType}${event.action ? ` ${event.action}` : ''}${subject ? ` ${subject}` : ''}`)
        setStatus(badge, event.status)
        setText(subtitle, `${event.repo || 'No repository'} · received ${relativeTime(event.receivedAt)}`)

        setFact('id', event.eventId)
        setFact('source', event.source)
        setFact('type', event.action ? `${event.eventType} (${event.action})` : event.eventType)
        setFact('repo', event.repo)
        setFact('branch', event.branch)
        setFact('ref', event.ref && event.ref !== event.branch ? event.ref : undefined)
        setFact('commit', event.commitSha ? event.commitSha.slice(0, 12) : undefined)
        setFact('base', event.baseSha ? event.baseSha.slice(0, 12) : undefined)
        setFact('pr', event.prNumber ? `#${event.prNumber}` : undefined)
        setFact('actor', event.actor)
        // The principal of the actor: who started the event (a person, a
        // service, or the system principal "Reactorcide" for a schedule).
        const principal = event.actorPrincipal
        setFact(
          'principal',
          principal ? `${principal.displayName || principal.principalId} (${principal.kind || 'principal'})${principal.identity ? ` via ${principal.identity}` : ''}` : undefined,
        )
        setFact('schedule', event.scheduleId, event.projectId ? href(`/projects/${event.projectId}`) : undefined)
        setFact('received', event.receivedAt ? new Date(event.receivedAt).toLocaleString() : undefined)
        setFact('delivery', event.deliveryId)
        setFact('checks', event.checksState ? `${event.checksState}${event.checksDescription ? `: ${event.checksDescription}` : ''}` : undefined)
        setFact('eval', event.evalJobId, event.evalJobId ? href(`/jobs/${event.evalJobId}`) : undefined)
        setFact('duplicate', event.duplicateOfEventId, event.duplicateOfEventId ? href(`/events/${event.duplicateOfEventId}`) : undefined)

        setRefClasses(classLabels, event.refClasses)
        const factRows = refClassFactRows(event.refClassFacts)
        const factSignature = JSON.stringify(factRows)
        if (classFacts.dataset.signature !== factSignature) {
          classFacts.dataset.signature = factSignature
          fill(classFacts, factRows.map(([label, value]) => h('div', null, h('dt', null, label), h('dd', null, value))))
        }
        const reasonLines = (event.refClassReasons ?? []).map(refClassReasonText)
        const reasonSignature = JSON.stringify(reasonLines)
        if (classReasons.dataset.signature !== reasonSignature) {
          classReasons.dataset.signature = reasonSignature
          fill(classReasons, reasonLines.map((line) => h('li', { class: 'reason reason-note' }, h('span', { class: 'reason-text' }, line))))
        }

        if (event.prNumber && event.projectId) {
          if (!approvals) {
            approvals = prApprovalsPanel(event.projectId, event.prNumber)
            approvalsAnchor.before(approvals)
          } else {
            approvals.setPullRequest(event.projectId, event.prNumber)
          }
        }

        const runs = decisions.filter((d) => d.decision === 'run').length
        setText(decisionsCount, decisions.length ? `${runs} of ${decisions.length} run` : '')
        show(decisionsEmpty, decisions.length === 0)
        show(decisionsList, decisions.length > 0)
        syncKeyed(decisionsList, sortDecisions(decisions), (d) => d.workflowSecurityId, createDecision, (el, d) => updateDecision(el, d, event.projectId))

        show(filesCard, event.changedFilesCount > 0 || event.changedFilesPreview.length > 0)
        const more = event.changedFilesCount - event.changedFilesPreview.length
        setText(filesCount, `${event.changedFilesCount} file${event.changedFilesCount === 1 ? '' : 's'} changed${more > 0 ? `; the first ${event.changedFilesPreview.length} are shown` : ''}.`)
        const signature = JSON.stringify(event.changedFilesPreview)
        if (filesList.dataset.signature !== signature) {
          filesList.dataset.signature = signature
          fill(
            filesList,
            event.changedFilesPreview.map((file) => h('li', { class: 'mono' }, file)),
            more > 0 ? h('li', { class: 'meta' }, `+${more} more`) : null,
          )
        }
      },
    }
  }
}

const DECISION_ORDER: Record<string, number> = { run: 0, not_admitted: 1, invalid: 2, skipped: 3 }

/** What ran first, then what was refused or broken, then what was skipped; by name within each. */
export function sortDecisions(decisions: readonly CIEventWorkflowDecision[]): CIEventWorkflowDecision[] {
  const name = (d: CIEventWorkflowDecision): string => d.workflowName || d.sourceFile || d.workflowSecurityId
  return [...decisions].sort((a, b) => (DECISION_ORDER[a.decision] ?? 9) - (DECISION_ORDER[b.decision] ?? 9) || name(a).localeCompare(name(b)))
}

interface DecisionRow extends HTMLLIElement {
  reasons: ReasonList
  owner: OwnerView
}

function createDecision(): DecisionRow {
  const reasons = new ReasonList()
  // The owner of a created workflow (its trusted snapshot). Hidden for a
  // workflow that did not run.
  const owner = new OwnerView()
  const el = h(
    'li',
    { class: 'decision-item' },
    h(
      'div',
      { class: 'decision-head' },
      decisionBadge(),
      h('a', { class: 'decision-name decision-link' }),
      h('span', { class: 'decision-name decision-plain' }),
      requiredStateBadge(),
      h('span', { class: 'meta mono decision-file' }),
    ),
    owner.el,
    reasons.el,
  ) as DecisionRow
  el.reasons = reasons
  el.owner = owner
  return el
}

function updateDecision(el: DecisionRow, d: CIEventWorkflowDecision, projectId: string | undefined): void {
  setDecision(el.querySelector('.decision')!, d.decision)
  const name = d.workflowName || d.sourceFile || d.workflowSecurityId
  const link = el.querySelector<HTMLAnchorElement>('.decision-link')!
  const plain = el.querySelector<HTMLElement>('.decision-plain')!
  show(link, d.workflowId)
  show(plain, !d.workflowId)
  setAttr(link, 'href', d.workflowId ? href(`/workflows/${d.workflowId}`) : null)
  setText(link, name)
  setText(plain, name)
  const file = el.querySelector<HTMLElement>('.decision-file')!
  const showFile = d.sourceFile && d.sourceFile !== name
  show(file, showFile)
  setText(file, showFile ? d.sourceFile! : '')
  setRequiredState(el.querySelector('.required-state')!, d.requiredState, d.requiredLabel)
  el.reasons.update(d.reasons, { projectId })
  show(el.owner.el, d.workflowId && d.owner)
  el.owner.update(d.owner)
}

export const tag = 'rc-event-detail-page'
define(tag, EventDetailPage)
