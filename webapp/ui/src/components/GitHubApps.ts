import type { PushButton } from '@catalystcommunity/controls'
import { api } from '~/api/client.ts'
import type { GitHubAppInstallationSummary, GitHubAppSummary, ListGitHubAppsResponse } from '~/api/csilapi/types.gen.ts'
import { Action, ActionError } from '~/components/ManagementPage.ts'
import { button, confirmAction, setButtonText } from '~/ui/controls.ts'
import { h, setText, show, syncKeyed } from '~/ui/dom.ts'
import { RcElement, define } from '~/ui/element.ts'
import { Loader } from '~/ui/load.ts'

/** "1,234 of 5,000 left" for a rate-limit snapshot, or "no data yet". */
export function rateLimitText(remaining?: number, limit?: number): string {
  if (remaining === undefined || limit === undefined) return 'no data yet'
  return `${remaining.toLocaleString('en-US')} of ${limit.toLocaleString('en-US')} left`
}

/**
 * The repositories of an installation, with " (pending approval)" after each
 * repository whose project the registration created disabled.
 */
export function repositoriesText(installation: GitHubAppInstallationSummary): string {
  if (!installation.repositories.length) return 'none'
  const pending = new Set(installation.pendingRepositories ?? [])
  return installation.repositories.map((name) => (pending.has(name) ? `${name} (pending approval)` : name)).join(', ')
}

export function installationState(installation: GitHubAppInstallationSummary): string {
  if (installation.deleted) return 'deleted'
  if (installation.suspended) return 'suspended'
  return 'active'
}

/**
 * The GitHub Apps of the deployment (docs/github-app.md), on the Admin page.
 *
 * Global-admin only. It lists each App with its rate-limit headroom, marks
 * the App that new installations should use, and lists the installations
 * with their repositories. An administrator can disable or enable an App and
 * start a sync. The REST API and the CLI register an App, because that needs
 * the private key file; the UI never sees a key or a webhook secret.
 */
export class GitHubAppsCard extends RcElement {
  #load = new Loader<string, ListGitHubAppsResponse>(this, () => api.listGithubApps({}))
  #action = new Action(() => this.invalidate())
  #error = new ActionError()
  #notice!: HTMLParagraphElement
  #loading!: HTMLParagraphElement
  #none!: HTMLParagraphElement
  #appsWrap!: HTMLElement
  #apps!: HTMLTableSectionElement
  #installsWrap!: HTMLElement
  #installs!: HTMLTableSectionElement

  protected build(): void {
    this.#notice = h('p', { class: 'meta', role: 'status' })
    this.#loading = h('p', { class: 'meta' }, 'Loading…')
    this.#none = h(
      'p',
      { class: 'empty' },
      'No GitHub App is registered. Projects use their VCS tokens and commit statuses. Register an App with "reactorcide github-apps add".',
    )
    this.#apps = h('tbody')
    this.#appsWrap = h(
      'div',
      { class: 'scroll-x' },
      h(
        'table',
        { class: 'table' },
        h(
          'thead',
          null,
          h(
            'tr',
            null,
            h('th', null, 'App'),
            h('th', null, 'State'),
            h('th', null, 'Headroom'),
            h('th', null, 'App rate limit'),
            h('th', null, 'Installations'),
            h('th', null, 'Repositories'),
            h('th'),
          ),
        ),
        this.#apps,
      ),
    )
    this.#installs = h('tbody')
    this.#installsWrap = h(
      'div',
      null,
      h('h4', { class: 'section-title' }, 'Installations'),
      h(
        'div',
        { class: 'scroll-x' },
        h(
          'table',
          { class: 'table' },
          h(
            'thead',
            null,
            h(
              'tr',
              null,
              h('th', null, 'Installation'),
              h('th', null, 'Account'),
              h('th', null, 'App'),
              h('th', null, 'State'),
              h('th', null, 'Headroom'),
              h('th', null, 'Rate limit'),
              h('th', null, 'Repositories'),
            ),
          ),
          this.#installs,
        ),
      ),
    )
    this.classList.add('card')
    this.append(
      h('h3', { class: 'section-title' }, 'GitHub Apps'),
      h(
        'p',
        { class: 'meta' },
        'A project whose repository is in an App installation uses short-lived installation tokens and posts "Reactorcide Checks" as a check run. Install new accounts on the recommended App: it has the most rate-limit headroom. A repository marked "pending approval" has a disabled project: enable the project to approve it.',
      ),
      this.#error.el,
      this.#notice,
      this.#loading,
      this.#none,
      this.#appsWrap,
      this.#installsWrap,
    )
  }

  async #setEnabled(app: GitHubAppSummary): Promise<void> {
    const enabled = !app.enabled
    if (!enabled) {
      const ok = await confirmAction(
        `Disable GitHub App ${app.slug || app.appId}? Its projects use their VCS tokens and commit statuses again until you enable it.`,
        { okLabel: 'Disable' },
      )
      if (!ok || !this.isActive()) return
    }
    const done = await this.#action.run('The GitHub App could not be changed.', () => api.setGithubAppEnabled({ appId: app.appId, enabled }))
    if (done && this.isActive()) await this.#load.reload()
  }

  async #sync(app: GitHubAppSummary): Promise<void> {
    let summary = ''
    const done = await this.#action.run('The sync failed.', async () => {
      const result = await api.syncGithubApp({ appId: app.appId })
      summary = `Sync of ${app.slug || app.appId}: ${result.installations} installations, ${result.created} new projects, ${result.disabled} disabled projects.`
      if (result.errors.length) summary += ` Errors: ${result.errors.join('; ')}`
    })
    if (!this.isActive()) return
    if (done) {
      setText(this.#notice, summary)
      await this.#load.reload()
    }
  }

  #appRow(): HTMLTableRowElement {
    const toggle = button('Disable', { small: true })
    const sync = button('Sync', { small: true })
    const row = h(
      'tr',
      null,
      h('td', { class: 'app-name' }),
      h('td', { class: 'app-state' }),
      h('td', { class: 'app-headroom' }),
      h('td', { class: 'app-rate' }),
      h('td', { class: 'app-installs' }),
      h('td', { class: 'app-repos' }),
      h('td', { class: 'row' }, h('a', { class: 'btn btn-sm app-install', target: '_blank', rel: 'noopener' }, 'Install'), toggle, sync),
    )
    return row
  }

  #updateAppRow(row: HTMLTableRowElement, app: GitHubAppSummary): void {
    const cells = row.cells
    setText(cells[0], `${app.slug || app.name || 'App'} (${app.appId})${app.recommended ? ' — recommended' : ''}`)
    setText(cells[1], `${app.enabled ? 'enabled' : 'disabled'}${app.openRegistration ? ', open registration' : ''}`)
    setText(cells[2], `${app.headroomPercent}%`)
    setText(cells[3], rateLimitText(app.ratelimitRemaining, app.ratelimitLimit))
    setText(cells[4], String(app.activeInstallations))
    setText(cells[5], String(app.repositories))
    const install = row.querySelector('.app-install') as HTMLAnchorElement
    show(install, Boolean(app.installUrl) && app.enabled)
    if (app.installUrl && install.href !== app.installUrl) install.href = app.installUrl
    const [toggle, sync] = row.querySelectorAll('push-button') as unknown as PushButton[]
    setButtonText(toggle, app.enabled ? 'Disable' : 'Enable')
    toggle.setAttribute('aria-label', `${app.enabled ? 'Disable' : 'Enable'} GitHub App ${app.appId}`)
    sync.setAttribute('aria-label', `Sync GitHub App ${app.appId}`)
    toggle.disabled = this.#action.busy
    sync.disabled = this.#action.busy || !app.enabled
    toggle.onclick = () => void this.#setEnabled(app)
    sync.onclick = () => void this.#sync(app)
  }

  render(): void {
    this.#load.sync('all')
    this.#error.set(this.#action.error ?? this.#load.state.error?.message)
    const data = this.#load.state.data
    show(this.#loading, !data && !this.#load.state.error)
    show(this.#none, data && data.apps.length === 0)
    show(this.#appsWrap, data && data.apps.length > 0)
    show(this.#installsWrap, data && data.installations.length > 0)
    if (!data) return
    syncKeyed(
      this.#apps,
      data.apps,
      (app) => String(app.appId),
      () => this.#appRow(),
      (row, app) => this.#updateAppRow(row, app),
    )
    const slugs = new Map(data.apps.map((app) => [app.appId, app.slug || String(app.appId)]))
    syncKeyed(
      this.#installs,
      data.installations,
      (installation) => String(installation.installationId),
      () => h('tr', null, h('td'), h('td'), h('td'), h('td'), h('td'), h('td'), h('td', { class: 'mono' })),
      (row, installation) => {
        const cells = (row as HTMLTableRowElement).cells
        setText(cells[0], String(installation.installationId))
        setText(cells[1], `${installation.accountLogin} (${installation.accountType || 'account'}, ${installation.repositorySelection || 'unknown'})`)
        setText(cells[2], slugs.get(installation.appId) ?? String(installation.appId))
        setText(cells[3], installationState(installation))
        setText(cells[4], `${installation.headroomPercent}%`)
        setText(cells[5], rateLimitText(installation.ratelimitRemaining, installation.ratelimitLimit))
        setText(cells[6], repositoriesText(installation))
      },
    )
  }
}

export const githubAppsCardTag = 'rc-github-apps-card'
define(githubAppsCardTag, GitHubAppsCard)

export function githubAppsCard(): GitHubAppsCard {
  return document.createElement(githubAppsCardTag) as GitHubAppsCard
}
