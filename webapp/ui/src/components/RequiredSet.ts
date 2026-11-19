import type { PushButton, TextField } from '@catalystcommunity/controls'
import { api } from '~/api/client.ts'
import type { GetRequiredSetResponse, RequiredSetProposal, RequiredSetWorkflow } from '~/api/csilapi/types.gen.ts'
import { field } from '~/components/Field.ts'
import { Action, ActionError } from '~/components/ManagementPage.ts'
import { ProjectCard } from '~/components/ProjectCard.ts'
import { relativeTime } from '~/components/States.ts'
import { button, confirmAction, setButtonText, textField } from '~/ui/controls.ts'
import { h, setText, show, syncKeyed } from '~/ui/dom.ts'
import { define } from '~/ui/element.ts'
import { Loader } from '~/ui/load.ts'

/**
 * The required set of a project (docs/required-set.md). Only the workflows in
 * the set count toward "Reactorcide Checks". The other workflows are advisory:
 * they run and report in the pull request comment only. A quarantine removes a
 * workflow from the set until its expiry.
 */

/** The text of a state, with the expiry of a quarantine. */
export function requiredStateText(row: RequiredSetWorkflow): string {
  switch (row.state) {
    case 'required':
      return 'Required'
    case 'quarantined':
      return row.quarantinedUntil ? `Quarantined until ${formatExpiry(row.quarantinedUntil)}` : 'Quarantined'
    case 'advisory':
      return 'Advisory — not required'
  }
  return row.state
}

/** An absolute UTC time, so an expiry is exact. */
export function formatExpiry(iso: string): string {
  const value = Date.parse(iso)
  if (Number.isNaN(value)) return iso
  return new Date(value).toISOString().replace('T', ' ').replace(/:\d\d\.\d+Z$/, ' UTC')
}

/** "every branch" for the empty pattern. */
export function branchLabel(pattern: string): string {
  return pattern === '' ? 'every branch' : pattern
}

function proposalActionText(proposal: RequiredSetProposal): string {
  return proposal.action === 'demote' ? 'Remove from the set' : 'Add to the set'
}

function rowKey(row: { workflowSecurityId: string; branchPattern: string }): string {
  return `${row.workflowSecurityId}\u0000${row.branchPattern}`
}

type WorkflowRow = HTMLTableRowElement & { data?: RequiredSetWorkflow }
type ProposalRow = HTMLTableRowElement & { data?: RequiredSetProposal }

class RequiredSetCard extends ProjectCard {
  #load = new Loader<string, GetRequiredSetResponse>(this, async (projectId) => api.getRequiredSet({ projectId }))
  #action = new Action(() => this.invalidate())
  #error = new ActionError()
  #status!: HTMLParagraphElement
  #approvers!: HTMLParagraphElement
  #toggle!: PushButton
  #loading!: HTMLParagraphElement
  #body!: HTMLDivElement
  #empty!: HTMLParagraphElement
  #rows!: HTMLTableSectionElement
  #proposalsPanel!: HTMLDivElement
  #proposals!: HTMLTableSectionElement
  #forms!: HTMLDivElement
  #workflow!: TextField
  #branch!: TextField
  #reason!: TextField
  #duration!: TextField
  #propose!: PushButton
  #quarantine!: PushButton

  protected build(): void {
    this.#status = h('p', { class: 'required-set-status' })
    this.#approvers = h('p', { class: 'meta required-set-approvers' })
    this.#toggle = button('Turn on', { onClick: () => void this.#setEnabled() })
    this.#loading = h('p', { class: 'meta' }, 'Loading…')
    this.#empty = h('p', { class: 'empty', hidden: true }, 'No workflow has run in this project yet.')
    this.#rows = h('tbody')
    this.#proposals = h('tbody')
    this.#proposalsPanel = h(
      'div',
      { hidden: true },
      h('h4', { class: 'section-title' }, 'Pending proposals'),
      h(
        'div',
        { class: 'scroll-x' },
        h(
          'table',
          { class: 'table' },
          h('thead', null, h('tr', null, h('th', null, 'Change'), h('th', null, 'Workflow'), h('th', null, 'Branch'), h('th', null, 'Proposed by'), h('th', null, 'Reason'), h('th', null, h('span', { class: 'sr-only' }, 'Actions')))),
          this.#proposals,
        ),
      ),
    )
    this.#workflow = textField({ placeholder: 'build', mono: true })
    this.#branch = textField({ placeholder: 'empty: every branch, or release/*', mono: true })
    this.#reason = textField({ placeholder: 'Why' })
    this.#duration = textField({ value: '7d', mono: true })
    this.#propose = button('Propose to add', { variant: 'primary', onClick: () => void this.#proposeChange('promote') })
    this.#quarantine = button('Quarantine', { onClick: () => void this.#quarantineWorkflow() })
    this.#forms = h(
      'div',
      { class: 'required-set-forms' },
      h('h4', { class: 'section-title' }, 'Change the set'),
      field({ label: 'Workflow ID', required: true, control: this.#workflow }).el,
      field({ label: 'Branch pattern', hint: 'The target branches of the entry. Empty is every branch. The approver rules use it.', control: this.#branch }).el,
      field({ label: 'Reason', hint: 'Required for a quarantine.', control: this.#reason }).el,
      field({ label: 'Quarantine for', hint: 'A duration (72h, 7d) or an RFC 3339 time. A quarantine always ends.', control: this.#duration }).el,
      h('div', { class: 'row' }, this.#propose, this.#quarantine),
    )
    this.#body = h(
      'div',
      { hidden: true },
      h('div', { class: 'row' }, this.#status, this.#toggle),
      this.#approvers,
      this.#empty,
      h(
        'div',
        { class: 'scroll-x' },
        h(
          'table',
          { class: 'table required-set-table' },
          h('thead', null, h('tr', null, h('th', null, 'Workflow'), h('th', null, 'Branch'), h('th', null, 'Status'), h('th', null, 'Who'), h('th', null, 'When'), h('th', null, h('span', { class: 'sr-only' }, 'Actions')))),
          this.#rows,
        ),
      ),
      this.#proposalsPanel,
      this.#forms,
    )
    this.append(
      h('h3', { class: 'section-title' }, 'Required set'),
      h(
        'p',
        { class: 'meta' },
        'Only required workflows count toward "Reactorcide Checks". An advisory workflow runs and reports in the pull request comment only. In branch protection, require only "Reactorcide Checks".',
      ),
      this.#error.el,
      this.#loading,
      this.#body,
    )
  }

  async #reload(): Promise<void> {
    if (this.isActive()) await this.#load.reload()
  }

  async #setEnabled(): Promise<void> {
    const data = this.#load.state.data
    if (!data || !this.project) return
    const enable = !data.enabled
    const text = enable
      ? 'Every workflow that this project knows goes into the required set, so nothing changes now. After this, a new workflow is advisory until an approver promotes it.'
      : 'Every workflow blocks a merge again. The entries of the set stay for a later "turn on".'
    if (!(await confirmAction(text, { okLabel: enable ? 'Turn on' : 'Turn off', danger: !enable }))) return
    const projectId = this.project.projectId
    if (await this.#action.run('The required set could not be changed.', () => api.setRequiredSetEnabled({ projectId, enabled: enable }))) await this.#reload()
  }

  async #proposeChange(action: 'promote' | 'demote', row?: RequiredSetWorkflow): Promise<void> {
    if (!this.project) return
    const workflowSecurityId = row?.workflowSecurityId ?? this.#workflow.value.trim()
    if (!workflowSecurityId) {
      this.#error.set('Give the workflow ID.')
      return
    }
    const branchPattern = row ? row.branchPattern : this.#branch.value.trim()
    const data = this.#load.state.data
    // An approver of every branch approves at once; another caller proposes.
    const approve = Boolean(data?.canApproveAllBranches && branchPattern === '')
    const projectId = this.project.projectId
    const ok = await this.#action.run('The proposal could not be stored.', () =>
      api.proposeRequiredSetChange({ projectId, action, workflowSecurityId, branchPattern, reason: this.#reason.value.trim() || undefined, approve }),
    )
    if (ok) {
      if (!row) this.#workflow.value = ''
      await this.#reload()
    }
  }

  async #decide(proposal: RequiredSetProposal, decision: 'approve' | 'reject' | 'withdraw'): Promise<void> {
    if (!this.project) return
    const projectId = this.project.projectId
    if (await this.#action.run('The proposal could not be decided.', () => api.decideRequiredSetProposal({ projectId, proposalId: proposal.proposalId, decision }))) await this.#reload()
  }

  async #quarantineWorkflow(row?: RequiredSetWorkflow): Promise<void> {
    if (!this.project) return
    const workflowSecurityId = row?.workflowSecurityId ?? this.#workflow.value.trim()
    const reason = this.#reason.value.trim()
    if (!workflowSecurityId || !reason) {
      this.#error.set('A quarantine needs the workflow ID and a reason. Type the reason, then select Quarantine.')
      return
    }
    const projectId = this.project.projectId
    const branchPattern = row ? row.branchPattern : this.#branch.value.trim()
    const ok = await this.#action.run('The workflow could not be quarantined.', () =>
      api.quarantineRequiredWorkflow({ projectId, workflowSecurityId, branchPattern, until: this.#duration.value.trim() || '7d', reason }),
    )
    if (ok) await this.#reload()
  }

  async #endQuarantine(row: RequiredSetWorkflow): Promise<void> {
    if (!this.project) return
    const projectId = this.project.projectId
    const ok = await this.#action.run('The quarantine could not be ended.', () =>
      api.unquarantineRequiredWorkflow({ projectId, workflowSecurityId: row.workflowSecurityId, branchPattern: row.branchPattern }),
    )
    if (ok) await this.#reload()
  }

  #createWorkflowRow(): WorkflowRow {
    const tr = h('tr') as WorkflowRow
    const current = (): RequiredSetWorkflow => tr.data!
    const promote = button('Propose to add', { small: true, onClick: () => void this.#proposeChange('promote', current()) })
    promote.classList.add('act-promote')
    const remove = button('Propose removal', { small: true, onClick: () => void this.#proposeChange('demote', current()) })
    remove.classList.add('act-remove')
    const quarantine = button('Quarantine', { small: true, onClick: () => void this.#quarantineWorkflow(current()) })
    quarantine.classList.add('act-quarantine')
    const end = button('End quarantine', { small: true, onClick: () => void this.#endQuarantine(current()) })
    end.classList.add('act-end')
    tr.append(
      h('td', { class: 'mono wf-id' }),
      h('td', { class: 'mono wf-branch' }),
      h('td', { class: 'wf-state' }, h('span', { class: 'status' }, h('span', { class: 'status-glyph', 'aria-hidden': 'true' }), h('span', { class: 'status-text' }))),
      h('td', { class: 'wf-who' }),
      h('td', { class: 'wf-when' }),
      h('td', { class: 'row wf-actions' }, promote, remove, quarantine, end),
    )
    return tr
  }

  #updateWorkflowRow(tr: WorkflowRow, row: RequiredSetWorkflow, data: GetRequiredSetResponse): void {
    tr.data = row
    setText(tr.querySelector('.wf-id')!, row.name && row.name !== row.workflowSecurityId ? `${row.workflowSecurityId} (${row.name})` : row.workflowSecurityId)
    setText(tr.querySelector('.wf-branch')!, branchLabel(row.branchPattern))
    const status = tr.querySelector<HTMLElement>('.status')!
    const tone = row.state === 'required' ? 'success' : row.state === 'quarantined' ? 'warning' : 'neutral'
    const glyph = row.state === 'required' ? '●' : row.state === 'quarantined' ? '⏸' : '○'
    status.className = `status status-${tone}`
    setText(status.querySelector('.status-glyph')!, glyph)
    setText(status.querySelector('.status-text')!, requiredStateText(row))
    const quarantined = row.state === 'quarantined'
    const who = quarantined ? (row.quarantinedByName ?? row.quarantinedBy) : (row.addedByName ?? row.addedBy ?? (row.source === 'seed' ? 'turned on' : undefined))
    setText(tr.querySelector('.wf-who')!, who ?? '—')
    const when = tr.querySelector<HTMLElement>('.wf-when')!
    setText(when, quarantined && row.quarantineReason ? `${relativeTime(row.addedAt)} · ${row.quarantineReason}` : relativeTime(row.addedAt ?? row.lastSeen))
    const pending = new Set(data.proposals.filter((p) => p.state === 'pending').map((p) => `${p.action}:${rowKey(p)}`))
    const canChange = data.enabled
    show(tr.querySelector('.act-promote')!, canChange && data.canPropose && !row.inSet && !pending.has(`promote:${rowKey(row)}`))
    show(tr.querySelector('.act-remove')!, canChange && data.canPropose && row.inSet && !pending.has(`demote:${rowKey(row)}`))
    show(tr.querySelector('.act-quarantine')!, canChange && row.inSet && row.state === 'required' && data.canApproveAllBranches)
    show(tr.querySelector('.act-end')!, canChange && quarantined && data.canApproveAllBranches)
  }

  #createProposalRow(): ProposalRow {
    const tr = h('tr') as ProposalRow
    const current = (): RequiredSetProposal => tr.data!
    const approve = button('Approve', { small: true, variant: 'primary', onClick: () => void this.#decide(current(), 'approve') })
    approve.classList.add('act-approve')
    const reject = button('Reject', { small: true, onClick: () => void this.#decide(current(), 'reject') })
    reject.classList.add('act-reject')
    const withdraw = button('Withdraw', { small: true, onClick: () => void this.#decide(current(), 'withdraw') })
    withdraw.classList.add('act-withdraw')
    tr.append(
      h('td', { class: 'p-action' }),
      h('td', { class: 'mono p-workflow' }),
      h('td', { class: 'mono p-branch' }),
      h('td', { class: 'p-by' }),
      h('td', { class: 'p-reason' }),
      h('td', { class: 'row' }, approve, reject, withdraw),
    )
    return tr
  }

  #updateProposalRow(tr: ProposalRow, proposal: RequiredSetProposal, data: GetRequiredSetResponse): void {
    tr.data = proposal
    setText(tr.querySelector('.p-action')!, proposalActionText(proposal))
    setText(tr.querySelector('.p-workflow')!, proposal.workflowSecurityId)
    setText(tr.querySelector('.p-branch')!, branchLabel(proposal.branchPattern))
    setText(tr.querySelector('.p-by')!, `${proposal.proposedByName ?? proposal.proposedBy ?? '—'} · ${relativeTime(proposal.proposedAt)}`)
    setText(tr.querySelector('.p-reason')!, proposal.reason ?? '')
    // The coordinator decides who may approve a branch-scoped entry; the
    // buttons show for the approvers of every branch and for proposers.
    show(tr.querySelector('.act-approve')!, data.canApproveAllBranches)
    show(tr.querySelector('.act-reject')!, data.canApproveAllBranches)
    show(tr.querySelector('.act-withdraw')!, data.canPropose)
  }

  render(): void {
    this.#load.sync(this.project?.projectId)
    const data = this.#load.state.data
    this.#error.set(this.#action.error ?? this.#load.state.error?.message)
    show(this.#loading, !data && !this.#load.state.error)
    show(this.#body, data)
    if (!data) return
    setText(this.#status, data.enabled ? 'The required set is on.' : 'The required set is off: every workflow blocks a merge.')
    setButtonText(this.#toggle, data.enabled ? 'Turn off' : 'Turn on')
    show(this.#toggle, data.canManage)
    this.#toggle.disabled = this.#action.busy
    const approvers = data.approvers.length ? data.approvers.join(', ') : 'org admins'
    const rules = data.approverRules.map((rule) => `${rule.branches.join(', ')}: ${rule.approvers.join(', ')}`)
    setText(this.#approvers, `Approvers: ${approvers}${rules.length ? `. Branch rules (first match wins): ${rules.join('; ')}` : ''}. Max quarantine: ${data.maxQuarantineDays} days.`)
    show(this.#empty, data.workflows.length === 0)
    syncKeyed(this.#rows, data.workflows, rowKey, () => this.#createWorkflowRow(), (tr, row) => this.#updateWorkflowRow(tr, row, data))
    const pending = data.proposals.filter((p) => p.state === 'pending')
    show(this.#proposalsPanel, pending.length > 0)
    syncKeyed(this.#proposals, pending, (p) => p.proposalId, () => this.#createProposalRow(), (tr, p) => this.#updateProposalRow(tr, p, data))
    show(this.#forms, data.enabled && data.canPropose)
    show(this.#quarantine, data.canApproveAllBranches)
    this.#propose.disabled = this.#action.busy
    this.#quarantine.disabled = this.#action.busy
  }
}

export const requiredSetCardTag = 'rc-project-required-set-card'
define(requiredSetCardTag, RequiredSetCard)
export type { RequiredSetCard }
