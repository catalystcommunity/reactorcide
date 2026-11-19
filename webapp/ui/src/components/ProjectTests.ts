import type { PushButton } from '@catalystcommunity/controls'
import { api } from '~/api/client.ts'
import type { FlakyTestView, ListTestQuarantinesResponse, TestQuarantineView } from '~/api/csilapi/types.gen.ts'
import { Action, ActionError } from '~/components/ManagementPage.ts'
import { relativeTime } from '~/components/States.ts'
import { testName } from '~/components/JobTests.ts'
import { button, confirmAction } from '~/ui/controls.ts'
import { h, setText, show, syncKeyed } from '~/ui/dom.ts'
import { RcElement, define } from '~/ui/element.ts'
import { Loader } from '~/ui/load.ts'

interface ProjectTestsData {
  flaky: FlakyTestView[]
  windowDays: number
  quarantines: ListTestQuarantinesResponse
}

/**
 * The flaky tests and the test quarantines of a project. A test is flaky
 * when it passed and failed on the same commit (also across retries and
 * re-runs) in the detection window. A quarantine has an owner, a reason, and
 * an expiry; the people who can manage it can end it here. Hidden when the
 * project has neither.
 */
export class ProjectTestsCard extends RcElement {
  projectId = ''

  #load = new Loader<string, ProjectTestsData>(this, async (projectId) => {
    const [flaky, quarantines] = await Promise.all([api.listFlakyTests({ projectId }), api.listTestQuarantines({ projectId })])
    return { flaky: flaky.tests, windowDays: flaky.windowDays, quarantines }
  })
  #action = new Action(() => this.invalidate())
  #error = new ActionError()
  #flakyTitle!: HTMLElement
  #flakyBody!: HTMLTableSectionElement
  #flakyTable!: HTMLElement
  #quarantineBody!: HTMLTableSectionElement
  #quarantineTable!: HTMLElement
  #quarantineTitle!: HTMLElement

  setProject(projectId: string): void {
    if (this.projectId === projectId) return
    this.projectId = projectId
    this.invalidate()
  }

  protected build(): void {
    this.classList.add('card')
    this.hidden = true
    this.#flakyTitle = h('h4')
    this.#flakyBody = h('tbody')
    this.#flakyTable = h(
      'div',
      { class: 'scroll-x' },
      h('table', { class: 'table' }, h('thead', null, h('tr', null, h('th', null, 'Test'), h('th', null, 'Flaky commits'), h('th', null, 'Runs'), h('th', null, 'Last flaky'))), this.#flakyBody),
    )
    this.#quarantineTitle = h('h4', null, 'Quarantined tests')
    this.#quarantineBody = h('tbody')
    this.#quarantineTable = h(
      'div',
      { class: 'scroll-x' },
      h(
        'table',
        { class: 'table' },
        h('thead', null, h('tr', null, h('th', null, 'Test'), h('th', null, 'Owner'), h('th', null, 'Reason'), h('th', null, 'Expires'), h('th'))),
        this.#quarantineBody,
      ),
    )
    this.append(
      h('h3', { class: 'section-title' }, 'Tests'),
      h('p', { class: 'meta' }, 'A quarantined failure does not fail the test summary in the pull request comment. It fails the workflow node only when the node has no quarantine_policy: ignore_quarantined. Jobs read the quarantined tests from REACTORCIDE_QUARANTINED_TESTS_FILE.'),
      this.#error.el,
      this.#flakyTitle,
      this.#flakyTable,
      this.#quarantineTitle,
      this.#quarantineTable,
    )
  }

  async #end(q: TestQuarantineView): Promise<void> {
    if (!(await confirmAction(`End the quarantine of "${testName(q)}"?`, { okLabel: 'End quarantine' }))) return
    const ok = await this.#action.run('The quarantine could not be ended.', () => api.unquarantineTest({ projectId: this.projectId, quarantineId: q.quarantineId }))
    if (ok && this.isActive()) await this.#load.reload()
  }

  render(): void {
    this.#load.sync(this.projectId || undefined)
    const data = this.#load.state.data
    const flaky = data?.flaky ?? []
    const quarantines = data?.quarantines.quarantines ?? []
    const canManage = Boolean(data?.quarantines.canManage)
    show(this, flaky.length > 0 || quarantines.length > 0)
    this.#error.set(this.#action.error)
    setText(this.#flakyTitle, `Flaky tests (last ${data?.windowDays ?? 30} days)`)
    show(this.#flakyTitle, flaky.length > 0)
    show(this.#flakyTable, flaky.length > 0)
    syncKeyed(
      this.#flakyBody,
      flaky,
      (t) => t.testId,
      () => h('tr', null, h('td', { class: 'mono flaky-name' }), h('td', { class: 'flaky-count' }), h('td', { class: 'flaky-runs' }), h('td', { class: 'flaky-last' })),
      (row, t) => {
        setText(row.querySelector('.flaky-name')!, testName(t))
        setText(row.querySelector('.flaky-count')!, String(t.flakyCount))
        setText(row.querySelector('.flaky-runs')!, String(t.totalRuns))
        setText(row.querySelector('.flaky-last')!, t.lastFlakyAt ? relativeTime(t.lastFlakyAt) : '-')
      },
    )
    show(this.#quarantineTitle, quarantines.length > 0)
    show(this.#quarantineTable, quarantines.length > 0)
    syncKeyed(
      this.#quarantineBody,
      quarantines,
      (q) => q.quarantineId,
      (q) =>
        h(
          'tr',
          null,
          h('td', { class: 'mono q-name' }),
          h('td', { class: 'mono q-owner truncate' }),
          h('td', { class: 'q-reason' }),
          h('td', { class: 'q-expires' }),
          h('td', null, button('End', { small: true, variant: 'danger', ariaLabel: `End the quarantine of ${testName(q)}`, onClick: () => void this.#end(q) })),
        ),
      (row, q) => {
        setText(row.querySelector('.q-name')!, testName(q) || q.testId)
        setText(row.querySelector('.q-owner')!, q.ownerPrincipalId)
        setText(row.querySelector('.q-reason')!, q.reason)
        setText(row.querySelector('.q-expires')!, new Date(q.expiresAt).toLocaleString())
        const end = row.querySelector('push-button') as PushButton
        show(end, canManage)
        end.disabled = this.#action.busy
      },
    )
  }
}

export const projectTestsCardTag = 'rc-project-tests-card'
define(projectTestsCardTag, ProjectTestsCard)

export function projectTestsCard(projectId: string): ProjectTestsCard {
  const el = document.createElement(projectTestsCardTag) as ProjectTestsCard
  el.projectId = projectId
  return el
}
