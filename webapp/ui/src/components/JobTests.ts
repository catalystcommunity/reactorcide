import type { PushButton, TextField } from '@catalystcommunity/controls'
import { api } from '~/api/client.ts'
import type { GetJobTestResultsResponse, JobTestSummaryView, TestResultView } from '~/api/csilapi/types.gen.ts'
import { field } from '~/components/Field.ts'
import { Action, ActionError } from '~/components/ManagementPage.ts'
import { button, clearField, setButtonText, setFieldValue, textField } from '~/ui/controls.ts'
import { h, setText, show, syncKeyed } from '~/ui/dom.ts'
import { RcElement, define } from '~/ui/element.ts'
import { Loader } from '~/ui/load.ts'

/** The display name of a test: class and name, or the suite. */
export function testName(test: { suite: string; classname: string; name: string }): string {
  const parts = [test.classname, test.name].filter(Boolean)
  return parts.length ? parts.join(' › ') : test.suite
}

/** The first non-empty lines of a failure message. */
export function firstLines(message: string, count = 3): string {
  return message
    .split('\n')
    .map((line) => line.trimEnd())
    .filter((line) => line.trim() !== '')
    .slice(0, count)
    .join('\n')
}

/** The one-line test summary. */
export function summaryText(summary: JobTestSummaryView): string {
  const failures = summary.failed + summary.errored
  const parts = [`${summary.passed} passed`]
  if (failures > 0) parts.push(`${failures} failed${summary.quarantinedFailures > 0 ? ` (${summary.quarantinedFailures} quarantined)` : ''}`)
  if (summary.skipped > 0) parts.push(`${summary.skipped} skipped`)
  if (summary.flaky > 0) parts.push(`${summary.flaky} flaky`)
  return `${summary.total} tests: ${parts.join(', ')}`
}

/**
 * The test results of a job from its JUnit reports: the counts, then the
 * failing tests with the first lines of their messages, and flags for
 * quarantined and flaky tests. A project owner (or an owner of the
 * workflow, or an org admin) can quarantine a failing test here. Hidden when
 * the job has no test report.
 */
export class JobTestsCard extends RcElement {
  jobId = ''
  done = false

  #load = new Loader<string, GetJobTestResultsResponse>(this, async (key) =>
    api.getJobTestResults({ jobId: key.split('\n')[0], failedOnly: true, limit: 200 }),
  )
  #action = new Action(() => this.invalidate())
  #error = new ActionError()
  #summary!: HTMLElement
  #override!: HTMLElement
  #parseErrors!: HTMLUListElement
  #list!: HTMLUListElement
  #form!: HTMLFormElement
  #target: TestResultView | undefined
  #targetText!: HTMLElement
  #reason!: TextField
  #until!: TextField
  #owner!: TextField
  #submit!: PushButton

  setJob(jobId: string, done: boolean): void {
    if (this.jobId === jobId && this.done === done) return
    this.jobId = jobId
    this.done = done
    this.invalidate()
  }

  protected build(): void {
    this.classList.add('card')
    this.hidden = true
    this.#summary = h('p', { class: 'test-summary' })
    this.#override = h('p', { class: 'meta' }, 'The workflow node counts as a success: every failed test is quarantined (quarantine_policy: ignore_quarantined). The job exit code is unchanged.')
    this.#parseErrors = h('ul', { class: 'readonly-list error-text' })
    this.#list = h('ul', { class: 'readonly-list job-tests' })
    this.#targetText = h('span', { class: 'mono' })
    this.#reason = textField({ placeholder: 'Fails on arm64 runners', onInput: () => this.invalidate() })
    this.#until = textField({ mono: true, value: '7d', placeholder: '7d or 2026-12-01T00:00:00Z', onInput: () => this.invalidate() })
    this.#owner = textField({ mono: true, placeholder: 'Your principal' })
    this.#submit = button('Quarantine', { variant: 'primary', type: 'submit' })
    this.#form = h(
      'form',
      { class: 'quarantine-form', onSubmit: (event: Event) => void this.#onQuarantine(event) },
      h('p', null, 'Quarantine ', this.#targetText, '. A quarantine needs a reason and an expiry.'),
      h(
        'div',
        { class: 'form-row' },
        field({ label: 'Reason', required: true, control: this.#reason }).el,
        field({ label: 'Until', required: true, control: this.#until }).el,
        field({ label: 'Owner principal ID', control: this.#owner }).el,
      ),
      this.#submit,
      ' ',
      button('Cancel', { onClick: () => this.#choose(undefined) }),
    )
    this.append(h('h3', { class: 'section-title' }, 'Tests'), this.#error.el, this.#summary, this.#override, this.#parseErrors, this.#list, this.#form)
  }

  #choose(test: TestResultView | undefined): void {
    this.#target = test
    this.#action.clearError()
    this.invalidate()
  }

  async #onQuarantine(event: Event): Promise<void> {
    event.preventDefault()
    const target = this.#target
    const projectId = this.#load.state.data?.projectId
    const reason = this.#reason.value.trim()
    const until = this.#until.value.trim()
    if (!target || !projectId || !reason || !until) return
    const owner = this.#owner.value.trim()
    const ok = await this.#action.run('The test could not be quarantined.', () =>
      api.quarantineTest({ projectId, testId: target.testId, reason, until, ...(owner ? { ownerPrincipalId: owner } : {}) }),
    )
    if (ok && this.isActive()) {
      clearField(this.#reason)
      setFieldValue(this.#until, '7d')
      this.#target = undefined
      await this.#load.reload()
    }
  }

  render(): void {
    this.#load.sync(this.jobId ? `${this.jobId}\n${this.done}` : undefined)
    const data = this.#load.state.data
    const summary = data?.summary
    show(this, Boolean(summary))
    this.#error.set(this.#action.error ?? this.#load.state.error?.message)
    if (!summary) return
    setText(this.#summary, summaryText(summary))
    this.#summary.classList.toggle('error-text', summary.failed + summary.errored > summary.quarantinedFailures)
    show(this.#override, summary.quarantineOverride)
    const problems = [...summary.parseErrors]
    if (summary.truncated) problems.push('A report was larger than the limit; only the first part was read.')
    syncKeyed(
      this.#parseErrors,
      problems,
      (text) => text,
      () => h('li'),
      (row, text) => setText(row, text),
    )
    const canQuarantine = Boolean(data?.canQuarantine && data.projectId)
    syncKeyed(
      this.#list,
      data?.results ?? [],
      (test) => `${test.testId}\n${test.status}\n${test.message}`,
      (test) =>
        h(
          'li',
          { class: 'test-row' },
          h('div', null, h('span', { class: 'test-status' }), ' ', h('span', { class: 'mono test-name' }), ' ', h('span', { class: 'test-flags meta' }), ' ',
            button('Quarantine', { small: true, ariaLabel: `Quarantine ${testName(test)}`, onClick: () => this.#choose(test) })),
          h('div', { class: 'meta mono test-location' }),
          h('pre', { class: 'mono test-message' }),
        ),
      (row, test) => {
        setText(row.querySelector('.test-status')!, test.status === 'error' ? 'Error' : 'Failed')
        setText(row.querySelector('.test-name')!, testName(test))
        const flags = [test.quarantined ? 'quarantined' : '', test.flaky ? 'flaky' : ''].filter(Boolean)
        setText(row.querySelector('.test-flags')!, flags.length ? `(${flags.join(', ')})` : '')
        const location = test.file ? `${test.file}${test.line ? `:${test.line}` : ''}` : ''
        const locationEl = row.querySelector('.test-location') as HTMLElement
        setText(locationEl, location)
        show(locationEl, location !== '')
        const message = row.querySelector('.test-message') as HTMLElement
        setText(message, firstLines(test.message))
        show(message, test.message !== '')
        const quarantine = row.querySelector('push-button') as PushButton
        show(quarantine, canQuarantine && !test.quarantined)
      },
    )
    show(this.#form, canQuarantine && this.#target !== undefined)
    setText(this.#targetText, this.#target ? testName(this.#target) : '')
    this.#submit.disabled = this.#action.busy || !this.#reason.value.trim() || !this.#until.value.trim()
    setButtonText(this.#submit, this.#action.busy ? 'Quarantining…' : 'Quarantine')
  }
}

export const jobTestsCardTag = 'rc-job-tests-card'
define(jobTestsCardTag, JobTestsCard)

export function jobTestsCard(jobId: string): JobTestsCard {
  const el = document.createElement(jobTestsCardTag) as JobTestsCard
  el.jobId = jobId
  return el
}
