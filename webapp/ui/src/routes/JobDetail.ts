import type { PushButton } from '@catalystcommunity/controls'
import { api } from '~/api/client.ts'
import type { GetCapabilitiesResponse, GetJobResponse } from '~/api/csilapi/types.gen.ts'
import { RcPage } from '~/app/page.ts'
import { describeError } from '~/components/ManagementPage.ts'
import { metricsPanel, type MetricsPanel } from '~/components/MetricsPanel.ts'
import { resourceHistory, type ResourceHistory } from '~/components/ResourceHistory.ts'
import { ErrorNotice, ResourceView, duration, relativeTime } from '~/components/States.ts'
import { isTerminal, setStatus, statusBadge } from '~/components/StatusBadge.ts'
import { BUFFER_CAPS, type ResourceHandle } from '~/store/index.ts'
import { eventSocket, topics } from '~/store/events.ts'
import { useCapabilities, useJob } from '~/store/resources.ts'
import { button, confirmAction } from '~/ui/controls.ts'
import { h, setAttr, setText, show } from '~/ui/dom.ts'
import { RcElement, define } from '~/ui/element.ts'
import { href } from '~/ui/router.ts'
import './job.css'

type Tab = 'logs' | 'metrics' | 'history'
type Capability = 'cancelJob' | 'killJob' | 'retryJob'

/**
 * One job.
 *
 * The shell makes a new page element for each job id (RcPage's default
 * acceptRoute), so a second job can never inherit the first job's log buffer
 * or metric choices.
 */
export class JobDetailPage extends RcPage {
  #job!: ResourceHandle<GetJobResponse>
  #caps!: ResourceHandle<GetCapabilitiesResponse>
  #view!: ResourceView<GetJobResponse>
  #back!: HTMLAnchorElement
  #actionError = new ErrorNotice()
  #tab: Tab = 'logs'
  #logsTab!: HTMLButtonElement
  #metricsTab!: HTMLButtonElement
  #logs!: LogViewer
  #metrics: MetricsPanel | null = null
  #historyTab!: HTMLButtonElement
  #history: ResourceHistory | null = null
  #panels!: HTMLDivElement

  #id(): string {
    return this.route().id ?? ''
  }

  #live(): boolean {
    return !isTerminal(this.#job?.state().data?.job.status ?? '')
  }

  protected build(): void {
    this.#back = h('a', { href: href('/jobs'), class: 'meta' }, '← All jobs')
    this.#view = new ResourceView<GetJobResponse>({ content: () => this.#content() })

    /*
     * The panels are deliberately OUTSIDE the ResourceView.
     *
     * They own long-lived state -- a bounded log buffer, a scroll position,
     * the selected metric family -- and need only the job id and whether it
     * is still running. Keeping them out of the job's content means a
     * job_update can never reach them as anything but a `live` change.
     */
    this.#logsTab = h('button', { type: 'button', class: 'tab', role: 'tab' }, 'Logs')
    this.#metricsTab = h('button', { type: 'button', class: 'tab', role: 'tab' }, 'Resources')
    this.#logsTab.addEventListener('click', () => this.#setTab('logs'))
    this.#metricsTab.addEventListener('click', () => this.#setTab('metrics'))
    this.#historyTab = h('button', { type: 'button', class: 'tab', role: 'tab' }, 'History')
    this.#historyTab.addEventListener('click', () => this.#setTab('history'))
    this.#logs = logViewer(this.#id())
    this.#panels = h('div', null, this.#logs)

    this.append(
      h(
        'div',
        { class: 'page' },
        h('div', { style: { marginBottom: 'var(--space-4)' } }, this.#back),
        this.#actionError.el,
        this.#view.el,
        h(
          'div',
          { class: 'card' },
          h('div', { class: 'tabs', role: 'tablist', 'aria-label': 'Job detail views' }, this.#logsTab, this.#metricsTab, this.#historyTab),
          this.#panels,
        ),
      ),
    )
  }

  #setTab(tab: Tab): void {
    this.#tab = tab
    // The metrics panel is made the first time it is asked for, then kept:
    // its choices survive a trip to the Logs tab and back.
    if (tab === 'metrics' && !this.#metrics) {
      this.#metrics = metricsPanel(this.#id(), this.#live())
      this.#panels.append(this.#metrics)
    }
    // History is made on first use too: it asks the server for every run of
    // this job, which is not worth doing for somebody reading the logs.
    if (tab === 'history' && !this.#history) {
      this.#history = resourceHistory(this.#id())
      this.#panels.append(this.#history)
    }
    this.invalidate()
  }

  protected connected(): void {
    this.#job = useJob(this, () => this.#id())
    // Asked of the job's own project, not the session. A project owner may
    // cancel and retry here and has nothing in the global set; an org admin
    // the same. Until the job has loaded there is no scope to ask about, and
    // no button shows.
    this.#caps = useCapabilities(this, () => {
      const job = this.#job.state().data?.job
      return job ? { projectId: job.projectId } : undefined
    })
  }

  render(): void {
    this.#job.sync()
    this.#caps.sync()
    const job = this.#job.state().data?.job
    setAttr(this.#back, 'href', href(job?.workflowId ? `/workflows/${job.workflowId}` : '/jobs'))
    setText(this.#back, job?.workflowId ? '← Back to workflow' : '← All jobs')
    this.#view.update(this.#job.state())

    const live = this.#live()
    this.#logs.live = live
    if (this.#metrics) this.#metrics.live = live
    setAttr(this.#logsTab, 'aria-selected', String(this.#tab === 'logs'))
    setAttr(this.#metricsTab, 'aria-selected', String(this.#tab === 'metrics'))
    show(this.#logs, this.#tab === 'logs')
    if (this.#metrics) show(this.#metrics, this.#tab === 'metrics')
    setAttr(this.#historyTab, 'aria-selected', String(this.#tab === 'history'))
    if (this.#history) show(this.#history, this.#tab === 'history')
  }

  #can(capability: Capability): boolean {
    return Boolean(this.#caps.state().data?.[capability])
  }

  async #act(work: () => Promise<unknown>, fallback: string): Promise<void> {
    this.#actionError.set(undefined)
    try {
      await work()
      await this.#job.refresh()
    } catch (cause) {
      if (this.isActive()) this.#actionError.set(describeError(cause, fallback))
    }
  }

  #content(): { node: Node; update(data: GetJobResponse): void } {
    const title = h('h1')
    const badge = statusBadge()
    const exit = h('span', { class: 'meta mono' })
    const description = h('p', { class: 'meta' })

    const cancel: PushButton = button('Cancel', {
      small: true,
      onClick: () => void this.#act(() => api.cancelJob({ jobId: this.#id() }), 'The job could not be cancelled.'),
    })
    const kill: PushButton = button('Kill', {
      small: true,
      variant: 'danger',
      onClick: async () => {
        // Kill skips cleanup hooks, so it gets a confirmation that says
        // exactly that rather than a generic one.
        if (!(await confirmAction('Kill this job immediately? Cleanup hooks will not run.', { okLabel: 'Kill' }))) return
        await this.#act(() => api.killJob({ jobId: this.#id() }), 'The job could not be killed.')
      },
    })
    const retry: PushButton = button('Retry', {
      small: true,
      onClick: () => void this.#act(() => api.retryJob({ jobId: this.#id() }), 'The job could not be retried.'),
    })

    const item = (label: string, cls = ''): { el: HTMLElement; value: HTMLElement } => {
      const value = h('dd', { class: cls })
      return { el: h('div', null, h('dt', null, label), value), value }
    }
    const id = item('Job ID', 'mono truncate')
    const runtime = item('Runtime')
    const scheduling = item('Scheduling')
    const updated = item('Updated')
    const queue = item('Queue', 'mono truncate')
    const sourceRef = item('Source ref', 'mono truncate')
    const image = item('Runner image', 'mono truncate')
    const workerClass = item('Worker class', 'mono')

    const failureText = h('pre', { class: 'log-viewer' })
    const failure = h('div', { class: 'card' }, h('h3', { class: 'section-title' }, 'Failure reason'), failureText)

    const node = h(
      'div',
      null,
      h(
        'div',
        { class: 'page-header' },
        h('div', null, h('div', { class: 'row' }, title, badge, exit), description),
        h('div', { class: 'row' }, cancel, kill, retry),
      ),
      h(
        'div',
        { class: 'card' },
        h('dl', { class: 'grid-dl' }, id.el, runtime.el, scheduling.el, updated.el, queue.el, sourceRef.el, image.el, workerClass.el),
      ),
      failure,
    )

    return {
      node,
      update: (data) => {
        const job = data.job
        const live = !isTerminal(job.status)
        setText(title, job.name)
        setStatus(badge, job.status)
        show(exit, job.exitCode !== undefined)
        setText(exit, job.exitCode !== undefined ? `exit ${job.exitCode}` : '')
        setText(description, job.description || 'No description')
        show(cancel, this.#can('cancelJob') && live)
        show(kill, this.#can('killJob') && live)
        show(retry, this.#can('retryJob') && !live)
        setText(id.value, job.jobId)
        setText(runtime.value, duration(job.startedAt, job.completedAt))
        setText(scheduling.value, duration(job.schedulingStartedAt, job.startedAt ?? job.completedAt))
        setText(updated.value, relativeTime(job.updatedAt))
        setText(queue.value, job.queueName ?? '')
        show(sourceRef.el, job.sourceRef)
        setText(sourceRef.value, job.sourceRef ?? '')
        show(image.el, job.runnerImage)
        setText(image.value, job.runnerImage ?? '')
        show(workerClass.el, job.workerClass)
        setText(workerClass.value, job.workerClass ?? '')
        show(failure, job.lastError)
        setText(failureText, job.lastError ?? '')
      },
    }
  }
}

export const tag = 'rc-job-detail-page'
define(tag, JobDetailPage)

interface LogLine {
  timestamp: string
  stream: string
  message: string
}

/**
 * The log viewer.
 *
 * Two things here matter beyond rendering lines.
 *
 * The buffer is BOUNDED. A chatty job watched for hours would otherwise grow
 * until the tab died; the viewer keeps the newest BUFFER_CAPS.logLines line
 * elements and removes the oldest. New lines are APPENDED as new elements;
 * existing ones are never made again, so the reader's text selection and
 * scroll position survive every poll.
 *
 * Scrolling FOLLOWS only when the reader is already at the bottom. Yanking the
 * viewport down while somebody is reading an error further up is the single
 * most annoying thing a log view can do.
 *
 * Set `jobId` before it connects. `live` may change; it gates the timer poll.
 */
export class LogViewer extends RcElement {
  jobId = ''
  live = false

  #cursor: string | undefined
  #inFlight = false
  #lineCount = 0
  #error = new ErrorNotice()
  #viewport!: HTMLDivElement
  #none!: HTMLDivElement

  /** The scrolling element, for tests. */
  get viewport(): HTMLDivElement {
    return this.#viewport
  }

  protected build(): void {
    this.#none = h('div', { class: 'meta' }, 'No logs yet.')
    this.#viewport = h('div', { class: 'log-viewer', tabindex: '0', role: 'log', 'aria-label': 'Job logs' }, this.#none)
    this.append(this.#error.el, this.#viewport)
  }

  protected connected(): void {
    void this.poll()

    // A log_available event triggers a fetch. Falling back to a timer as well
    // means a missed notification costs a few seconds of staleness rather than
    // leaving the view wrong until a reload.
    this.own(eventSocket.acquireTopic(topics.job(this.jobId)))
    this.own(
      eventSocket.addListener((event) => {
        if (event.job_id === this.jobId && event.type === 'log_available') void this.poll()
      }),
    )
    const timer = setInterval(() => {
      if (this.live && document.visibilityState === 'visible') void this.poll()
    }, 4_000)
    this.own(() => clearInterval(timer))
  }

  #atBottom(): boolean {
    const v = this.#viewport
    return v.scrollHeight - v.scrollTop - v.clientHeight < 40
  }

  /** Fetches the next page of log lines. Concurrent calls collapse to one. */
  async poll(): Promise<void> {
    if (this.#inFlight) return
    this.#inFlight = true
    try {
      const response = await api.getJobLogs({
        jobId: this.jobId,
        stream: 'combined',
        cursor: this.#cursor,
        maxEntries: 1000,
      })
      if (!this.isActive()) return
      const wasAtBottom = this.#atBottom()
      if (response.entries.length > 0) this.appendLines(response.entries)
      if (response.nextCursor) this.#cursor = response.nextCursor
      this.#error.set(undefined)
      if (wasAtBottom) {
        queueMicrotask(() => {
          this.#viewport.scrollTop = this.#viewport.scrollHeight
        })
      }
    } catch (cause) {
      if (this.isActive()) this.#error.set(cause instanceof Error ? cause.message : 'Logs are unavailable.')
    } finally {
      this.#inFlight = false
    }
  }

  /** Appends line elements and drops the oldest beyond the cap. */
  appendLines(entries: readonly LogLine[]): void {
    if (entries.length === 0) return
    if (this.#lineCount === 0) this.#none.remove()
    // Only the newest `cap` of a huge batch are worth making at all.
    const cap = BUFFER_CAPS.logLines
    const fresh = entries.length > cap ? entries.slice(entries.length - cap) : entries
    const fragment = document.createDocumentFragment()
    for (const line of fresh) {
      fragment.append(
        h('div', { class: `log-line${line.stream === 'stderr' ? ' log-stderr' : ''}` }, h('span', { class: 'log-timestamp' }, line.timestamp), line.message),
      )
    }
    this.#viewport.append(fragment)
    this.#lineCount += fresh.length
    while (this.#lineCount > cap) {
      this.#viewport.firstElementChild?.remove()
      this.#lineCount -= 1
    }
  }

  /** How many lines the viewer holds. */
  get lineCount(): number {
    return this.#lineCount
  }
}
define('rc-log-viewer', LogViewer)

export function logViewer(jobId: string): LogViewer {
  const el = document.createElement('rc-log-viewer') as LogViewer
  el.jobId = jobId
  return el
}
