import { api } from '~/api/client.ts'
import type { JobLinkSummary } from '~/api/csilapi/types.gen.ts'
import { h, setAttr, setText, show, syncKeyed } from '~/ui/dom.ts'
import { RcElement, define } from '~/ui/element.ts'
import { Loader } from '~/ui/load.ts'

/** True for an absolute http or https URL: the only links the page shows. */
export function safeLinkURL(value: string): boolean {
  try {
    const url = new URL(value)
    return url.protocol === 'https:' || url.protocol === 'http:'
  } catch {
    return false
  }
}

/**
 * The named links that a job published in its workflow output (for example
 * an external build results page). Hidden when the job has none. Set `jobId`;
 * change `done` when the job finishes, so the links load again.
 */
export class JobLinksCard extends RcElement {
  jobId = ''
  done = false

  #load = new Loader<string, JobLinkSummary[]>(this, async (key) => (await api.listJobLinks({ jobId: key.split('\n')[0] })).links)
  #list!: HTMLUListElement

  setJob(jobId: string, done: boolean): void {
    if (this.jobId === jobId && this.done === done) return
    this.jobId = jobId
    this.done = done
    this.invalidate()
  }

  protected build(): void {
    this.#list = h('ul', { class: 'readonly-list job-links' })
    this.classList.add('card')
    this.hidden = true
    this.append(h('h3', { class: 'section-title' }, 'Links'), this.#list)
  }

  render(): void {
    this.#load.sync(this.jobId ? `${this.jobId}\n${this.done}` : undefined)
    const links = (this.#load.state.data ?? []).filter((link) => safeLinkURL(link.url))
    show(this, links.length > 0)
    syncKeyed(
      this.#list,
      links,
      (link) => link.name,
      () => h('li', null, h('a', { target: '_blank', rel: 'noopener noreferrer' })),
      (row, link) => {
        const anchor = row.querySelector('a')!
        setAttr(anchor, 'href', link.url)
        setText(anchor, link.name)
      },
    )
  }
}

export const jobLinksCardTag = 'rc-job-links-card'
define(jobLinksCardTag, JobLinksCard)

export function jobLinksCard(jobId: string): JobLinksCard {
  const el = document.createElement(jobLinksCardTag) as JobLinksCard
  el.jobId = jobId
  return el
}
