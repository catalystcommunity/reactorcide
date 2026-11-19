import { api } from '~/api/client.ts'
import type { DiagnosisSummary } from '~/api/csilapi/types.gen.ts'
import { relativeTime } from '~/components/States.ts'
import { h, setAttr, setText, show, syncKeyed } from '~/ui/dom.ts'
import { RcElement, define } from '~/ui/element.ts'
import { Loader } from '~/ui/load.ts'
import { SESSION, session } from '~/lib/session.ts'
import { Action, ActionError } from '~/components/ManagementPage.ts'
import { button, confirmAction } from '~/ui/controls.ts'
import type { PushButton } from '@catalystcommunity/controls'
import { safeLinkURL } from './JobLinks.ts'
import './diagnosis.css'

/** The target of a diagnosis card: a job, or a workflow (with its jobs). */
export type DiagnosisTarget = { jobId: string } | { workflowId: string }

function targetKey(target: DiagnosisTarget | undefined): string {
  if (!target) return ''
  return 'jobId' in target ? `job:${target.jobId}` : `workflow:${target.workflowId}`
}

/** The author label: the display name and the principal kind. */
export function authorLabel(entry: DiagnosisSummary): string {
  const name = entry.author.displayName || entry.author.principalId
  return `${name} (${entry.author.kind || 'unknown'})`
}

/**
 * The diagnosis entries that principals (for example a diagnosis tool, which
 * is a service principal) posted for a job or a workflow. Hidden when there
 * are none. The body is shown as plain text, never as HTML: a principal
 * wrote it, not Reactorcide. A new `stamp` (for example the status) loads
 * again.
 */
/** True when the signed-in person wrote the entry (a human principal is the user). */
export function isOwnEntry(entry: DiagnosisSummary, userId: string | undefined): boolean {
  return Boolean(userId) && entry.author.principalId === userId
}

/** The target of an entry, as delete-diagnosis names it (the caller's own entry there). */
export function deleteTarget(entry: DiagnosisSummary): { jobId: string } | { workflowId: string; nodeName?: string } {
  if (entry.jobId) return { jobId: entry.jobId }
  return entry.nodeName ? { workflowId: entry.workflowId ?? '', nodeName: entry.nodeName } : { workflowId: entry.workflowId ?? '' }
}

type EntryRow = HTMLElement & { entry?: DiagnosisSummary }

export class DiagnosisCard extends RcElement {
  static watches = [SESSION]
  #target: DiagnosisTarget | undefined
  #delete = new Action(() => this.invalidate())
  #error = new ActionError()
  #stamp = ''
  #reload = false
  #load = new Loader<string, DiagnosisSummary[]>(this, async (key) => {
    const [kind, id] = key.split(':')
    const response = await api.listDiagnoses(kind === 'job' ? { jobId: id } : { workflowId: id })
    return response.diagnoses
  })
  #list!: HTMLDivElement
  #note!: HTMLParagraphElement

  setTarget(target: DiagnosisTarget | undefined, stamp: string): void {
    const same = targetKey(this.#target) === targetKey(target)
    if (same && this.#stamp === stamp) return
    // A new stamp of the same target loads again and keeps the entries on
    // screen meanwhile, so a row keeps its element.
    this.#reload = same
    this.#target = target
    this.#stamp = stamp
    this.invalidate()
  }

  protected build(): void {
    this.classList.add('card')
    this.hidden = true
    this.#list = h('div', { class: 'diagnoses' })
    this.#note = h(
      'p',
      { class: 'meta' },
      'A principal wrote each entry. Reactorcide does not check the content. A suggested shift is not applied until somebody retries with it.',
    )
    this.append(h('h3', { class: 'section-title' }, 'Diagnosis'), this.#note, this.#error.el, this.#list)
  }

  async #remove(entry: DiagnosisSummary): Promise<void> {
    if (!(await confirmAction(`Delete the diagnosis entry "${entry.title}"? It is also removed from the pull request comment.`, { okLabel: 'Delete' }))) return
    const ok = await this.#delete.run('The diagnosis entry could not be deleted.', () => api.deleteDiagnosis(deleteTarget(entry)))
    if (ok && this.isActive()) await this.#load.reload()
  }

  render(): void {
    const key = targetKey(this.#target)
    this.#load.sync(key || undefined)
    if (this.#reload) {
      this.#reload = false
      void this.#load.reload()
    }
    const entries = this.#load.state.data ?? []
    show(this, entries.length > 0)
    const workflowView = this.#target !== undefined && 'workflowId' in this.#target
    const userId = session()?.user_id
    this.#error.set(this.#delete.error)
    syncKeyed(
      this.#list,
      entries,
      (entry) => entry.diagnosisId,
      () => {
        const row = h('article', { class: 'diagnosis' }) as EntryRow
        const remove = button('Delete', { small: true, variant: 'danger', onClick: () => void this.#remove(row.entry!) })
        remove.classList.add('diagnosis-delete')
        row.append(
          h(
            'div',
            { class: 'diagnosis-head' },
            h('span', { class: 'diagnosis-title' }),
            h('span', { class: 'status status-info diagnosis-kind' }),
            h('span', { class: 'meta diagnosis-author' }),
            h('span', { class: 'meta diagnosis-meta' }),
            remove,
          ),
          h('pre', { class: 'diagnosis-body' }),
          h('p', { class: 'meta diagnosis-shifts' }),
          h('div', { class: 'diagnosis-links' }),
        )
        return row
      },
      (row, entry) => {
        ;(row as EntryRow).entry = entry
        const remove = row.querySelector<PushButton>('.diagnosis-delete')!
        // The coordinator decides; the button shows on the entries of the
        // signed-in person.
        show(remove, isOwnEntry(entry, userId))
        remove.disabled = this.#delete.busy
        const title = row.querySelector<HTMLElement>('.diagnosis-title')!
        const prefix = workflowView && entry.nodeName ? `${entry.nodeName}: ` : ''
        setText(title, prefix + entry.title)
        setText(row.querySelector<HTMLElement>('.diagnosis-kind')!, entry.author.kind || 'unknown')
        setText(row.querySelector<HTMLElement>('.diagnosis-author')!, `by ${authorLabel(entry)}`)
        const meta = [entry.confidence && `confidence ${entry.confidence}`, entry.category, relativeTime(entry.updatedAt)]
        if (entry.revision > 1) meta.push(`revision ${entry.revision}`)
        setText(row.querySelector<HTMLElement>('.diagnosis-meta')!, meta.filter(Boolean).join(' · '))
        const body = row.querySelector<HTMLElement>('.diagnosis-body')!
        setText(body, entry.body)
        show(body, entry.body !== '')
        const shifts = row.querySelector<HTMLElement>('.diagnosis-shifts')!
        setText(shifts, entry.suggestedShiftsText ? `Suggested re-run shifts: ${entry.suggestedShiftsText}` : '')
        show(shifts, entry.suggestedShiftsText !== '')
        const links = row.querySelector<HTMLDivElement>('.diagnosis-links')!
        const safe = entry.links.filter((link) => safeLinkURL(link.url))
        syncKeyed(
          links,
          safe,
          (link) => `${link.name}\n${link.url}`,
          () => h('a', { target: '_blank', rel: 'noopener noreferrer nofollow' }),
          (anchor, link) => {
            setAttr(anchor, 'href', link.url)
            setText(anchor, link.name)
          },
        )
        show(links, safe.length > 0)
      },
    )
  }
}

export const diagnosisCardTag = 'rc-diagnosis-card'
define(diagnosisCardTag, DiagnosisCard)

export function diagnosisCard(): DiagnosisCard {
  return document.createElement(diagnosisCardTag) as DiagnosisCard
}
