import type { PushButton, TextField } from '@catalystcommunity/controls'
import { ServiceError, api } from '~/api/client.ts'
import type {
  CiPolicyDefaults,
  CiPolicyDetail,
  CiPolicyDocument,
  CiPolicyNodeAuthority,
  CiPolicyRule,
  CiPolicySubjectMatch,
  CiPolicyUse,
  EnumChoice,
} from '~/api/csilapi/types.gen.ts'
import { ChoiceSelect } from '~/components/ChoiceInput.ts'
import { ChipList, pathPatternError, securityIdError, subjectError, subjectHint } from '~/components/ChipList.ts'
import { field, type Field } from '~/components/Field.ts'
import { describeError } from '~/components/ManagementPage.ts'
import { skeleton } from '~/components/States.ts'
import { button, confirmAction, setButtonText, textField } from '~/ui/controls.ts'
import { h, setText, show, syncKeyed, uid } from '~/ui/dom.ts'
import { RcElement, define } from '~/ui/element.ts'
import { Loader } from '~/ui/load.ts'
import './chips.css'

/**
 * A form for the CI policy of one project.
 *
 * The policy says when a pull request may run its OWN CI files ("head CI")
 * instead of the trusted CI files of the base branch. Each rule is one card
 * in plain words. This is not a text editor: the people who decide policy are
 * often not the people who write YAML.
 *
 * A save sends the revision that was loaded. When somebody else saved first,
 * the coordinator refuses with "conflict", and the form says to reload.
 *
 * Round trip: a field that a person did not change goes back as it came,
 * including the difference between an absent list and an empty one. Each card
 * keeps the rule it was loaded from for that.
 */

export const HEAD_REPOSITORY_CHOICES: EnumChoice[] = [
  { value: 'same', label: 'This repository only', description: 'A branch in the same repository. Not a fork.' },
  { value: 'fork', label: 'Forks only', description: 'A fork of the repository.' },
  { value: 'any', label: 'This repository or a fork', description: 'Any pull request.' },
]

const NODE_NAME = /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/

function nodeNameError(value: string): string | undefined {
  return NODE_NAME.test(value) ? undefined : 'A step name has letters, digits, _ and -. It starts with a letter or digit.'
}

function eventError(value: string): string | undefined {
  return /^[a-z][a-z0-9_]*$/.test(value) ? undefined : 'An event is a word like pull_request_opened.'
}

/** A list as it goes back: the values, or what was loaded when it was empty. */
function listOut(values: string[], original: string[] | undefined): string[] | undefined {
  if (values.length) return values
  return original !== undefined && original.length === 0 ? [] : undefined
}

/** A text as it goes back. `implicit` is the server default the control shows for an absent value. */
function textOut(value: string, original: string | undefined, implicit?: string): string | undefined {
  if (value === '') return original === '' ? '' : undefined
  if (original === undefined && implicit !== undefined && value === implicit) return undefined
  return value
}

function matchOut(values: string[], original: CiPolicySubjectMatch | undefined): CiPolicySubjectMatch | undefined {
  if (values.length) return { any: values }
  return original ? { any: [] } : undefined
}

/** Drops the keys whose value is undefined, so the object is what the form means. */
function compact<T extends object>(value: T): T {
  const out: Record<string, unknown> = {}
  for (const [k, v] of Object.entries(value)) if (v !== undefined) out[k] = v
  return out as T
}

/** One "trusted steps" entry of a rule. */
class NodeRow {
  readonly key = uid('nodes')
  readonly el: HTMLDivElement
  readonly nodes: ChipList
  readonly profile: TextField
  readonly workers: TextField
  readonly #fields: Record<'nodes' | 'profile' | 'workers', Field>
  original: CiPolicyNodeAuthority | undefined

  constructor(original: CiPolicyNodeAuthority | undefined, onRemove: (row: NodeRow) => void, onChange: () => void) {
    this.original = original
    this.nodes = new ChipList({ ariaLabel: 'Add a step name', placeholder: 'deploy', mono: true, validate: nodeNameError, onChange })
    this.profile = textField({ mono: true, placeholder: 'standard', onInput: onChange })
    this.workers = textField({ mono: true, placeholder: 'default', onInput: onChange })
    this.#fields = {
      nodes: field({ label: 'Steps', required: true, hint: 'The workflow step (node) names.', control: this.nodes.el, labelTarget: this.nodes.input }),
      profile: field({ label: 'Profile', required: true, control: this.profile }),
      workers: field({ label: 'Worker class', required: true, control: this.workers }),
    }
    this.el = h(
      'div',
      { class: 'sub-card node-row' },
      h(
        'div',
        { class: 'sub-card-header' },
        h('span', { class: 'field-label' }, 'These steps run from the trusted base CI'),
        button('Remove', { small: true, variant: 'flat', onClick: () => onRemove(this) }),
      ),
      this.#fields.nodes.el,
      h('div', { class: 'form-row' }, this.#fields.profile.el, this.#fields.workers.el),
    )
    this.nodes.set(original?.nodes ?? [])
    this.profile.value = original?.profile ?? ''
    this.workers.value = original?.workers ?? ''
  }

  value(): CiPolicyNodeAuthority {
    const o = this.original
    return compact({
      nodes: this.nodes.values(),
      // Trusted steps always run from the base. Keep what was loaded.
      ciSource: o ? o.ciSource : 'base',
      profile: textOut(this.profile.value.trim(), o?.profile),
      workers: textOut(this.workers.value.trim(), o?.workers),
    })
  }

  errors(): Partial<Record<'nodes' | 'profile' | 'workers', string>> {
    return {
      nodes: this.nodes.values().length ? undefined : 'Name at least one step.',
      profile: this.profile.value.trim() ? undefined : 'Name the profile of these steps.',
      workers: this.workers.value.trim() ? undefined : 'Name the worker class of these steps.',
    }
  }

  render(touched: boolean): void {
    const errors = this.errors()
    for (const key of ['nodes', 'profile', 'workers'] as const) this.#fields[key].setError(touched ? errors[key] : undefined)
  }
}

type RuleField = 'id' | 'workflows' | 'paths' | 'profile' | 'workers'

/** One rule card. Its controls hold the draft. */
export class RuleCard {
  readonly key = uid('rule')
  readonly el: HTMLDivElement
  readonly id: TextField
  readonly actors: ChipList
  readonly workflows: ChipList
  readonly paths: ChipList
  readonly events: ChipList
  readonly branches: ChipList
  readonly headRepository: ChoiceSelect
  readonly approval: ChipList
  readonly profile: TextField
  readonly workers: TextField
  readonly #fields: Record<RuleField, Field>
  readonly #heading: HTMLHeadingElement
  readonly #nodeList: HTMLDivElement
  readonly #up: PushButton
  readonly #down: PushButton
  readonly #onChange: () => void
  #nodes: NodeRow[] = []
  #headRepository: string
  original: CiPolicyRule | undefined

  constructor(original: CiPolicyRule | undefined, actions: { move: (card: RuleCard, by: number) => void; remove: (card: RuleCard) => void; change: () => void }) {
    this.original = original
    const change = actions.change
    this.#onChange = change
    this.id = textField({ mono: true, placeholder: 'docs-team', onInput: change })
    this.actors = new ChipList({
      ariaLabel: 'Add a person or team who can change CI files',
      placeholder: 'reactorcide_group:ci-maintainers',
      mono: true,
      validate: (v) => subjectError(v, { projectOwner: false }),
      onChange: change,
    })
    this.workflows = new ChipList({ ariaLabel: 'Add a workflow id', placeholder: 'docs', mono: true, validate: (v) => securityIdError(v, 'A workflow id'), onChange: change })
    this.paths = new ChipList({ ariaLabel: 'Add a CI file pattern', placeholder: '.reactorcide/workflows/docs.yaml', mono: true, validate: pathPatternError, onChange: change })
    this.events = new ChipList({ ariaLabel: 'Add an event', placeholder: 'pull_request_opened', mono: true, validate: eventError, onChange: change })
    this.branches = new ChipList({ ariaLabel: 'Add a target branch', placeholder: 'main', mono: true, onChange: change })
    this.#headRepository = original?.headRepository ?? 'same'
    this.headRepository = new ChoiceSelect({
      value: this.#headRepository,
      choices: HEAD_REPOSITORY_CHOICES,
      onChange: (value) => {
        this.#headRepository = value
        change()
      },
    })
    this.approval = new ChipList({
      ariaLabel: 'Add an approver who can allow it',
      placeholder: 'project_owner',
      mono: true,
      validate: (v) => subjectError(v),
      onChange: change,
    })
    this.profile = textField({ mono: true, placeholder: 'pr-untrusted', onInput: change })
    this.workers = textField({ mono: true, placeholder: 'default', onInput: change })

    this.#fields = {
      id: field({ label: 'Rule name', required: true, hint: 'Lowercase letters, digits and hyphens.', control: this.id }),
      workflows: field({
        label: 'Which workflows',
        required: true,
        hint: 'Workflow ids. A workflow can be in one rule only.',
        control: this.workflows.el,
        labelTarget: this.workflows.input,
      }),
      paths: field({
        label: 'Which CI files',
        required: true,
        hint: 'Path patterns with * and **. Every changed CI file must match one of them.',
        control: this.paths.el,
        labelTarget: this.paths.input,
      }),
      profile: field({ label: 'Profile', required: true, hint: 'The execution profile of the run.', control: this.profile }),
      workers: field({ label: 'Worker class', required: true, hint: 'The worker class that runs it.', control: this.workers }),
    }

    this.#heading = h('h4', { class: 'section-title' })
    this.#up = button('Move up', { small: true, variant: 'flat', onClick: () => actions.move(this, -1) })
    this.#down = button('Move down', { small: true, variant: 'flat', onClick: () => actions.move(this, 1) })
    this.#nodeList = h('div')

    this.el = h(
      'div',
      { class: 'card rule-card' },
      h(
        'div',
        { class: 'card-header' },
        this.#heading,
        h('div', { class: 'row' }, this.#up, this.#down, button('Remove rule', { small: true, variant: 'danger', onClick: () => actions.remove(this) })),
      ),
      this.#fields.id.el,
      field({
        label: 'Who can change CI files without approval',
        hint: `Empty means anyone. ${subjectHint({ projectOwner: false })}`,
        control: this.actors.el,
        labelTarget: this.actors.input,
      }).el,
      this.#fields.workflows.el,
      this.#fields.paths.el,
      h(
        'div',
        { class: 'form-row' },
        field({ label: 'Events', hint: 'Empty means every event.', control: this.events.el, labelTarget: this.events.input }).el,
        field({ label: 'Target branches', hint: 'Branch names or patterns. Empty means every branch.', control: this.branches.el, labelTarget: this.branches.input }).el,
      ),
      field({ label: 'Pull requests from', control: this.headRepository.el }).el,
      field({
        label: 'Also allowed when one of these approves with /reactorcide override-ci',
        hint: `Empty means no approval is needed. ${subjectHint()}`,
        control: this.approval.el,
        labelTarget: this.approval.input,
      }).el,
      h('div', { class: 'field-label' }, 'Run with'),
      h('div', { class: 'form-row' }, this.#fields.profile.el, this.#fields.workers.el),
      h('div', { class: 'field-label' }, 'Trusted steps'),
      h('p', { class: 'meta' }, 'Steps named here always run from the trusted base CI, with their own profile and workers. Use this for a deploy step.'),
      this.#nodeList,
      button('Add trusted steps', { small: true, onClick: () => this.#addNodes() }),
    )

    this.id.value = original?.id ?? ''
    this.actors.set(original?.actors?.any ?? [])
    this.workflows.set(original?.workflows ?? [])
    this.paths.set(original?.paths ?? [])
    this.events.set(original?.events ?? [])
    this.branches.set(original?.baseBranches ?? [])
    this.approval.set(original?.approval?.any ?? [])
    this.profile.value = original?.use.profile ?? ''
    this.workers.value = original?.use.workers ?? ''
    this.#nodes = (original?.use.baseNodes ?? []).map((n) => this.#newNodes(n))
  }

  #newNodes(original: CiPolicyNodeAuthority | undefined): NodeRow {
    return new NodeRow(
      original,
      (row) => {
        this.#nodes = this.#nodes.filter((r) => r !== row)
        this.#onChange()
      },
      () => this.#onChange(),
    )
  }

  #addNodes(): void {
    this.#nodes = [...this.#nodes, this.#newNodes(undefined)]
    this.#onChange()
  }

  value(): CiPolicyRule {
    const o = this.original
    const nodes = this.#nodes.map((n) => n.value())
    const use: CiPolicyUse = compact({
      // A rule always runs the head CI. Keep what was loaded.
      ciSource: o ? o.use.ciSource : 'head',
      profile: textOut(this.profile.value.trim(), o?.use.profile),
      workers: textOut(this.workers.value.trim(), o?.use.workers),
      baseNodes: nodes.length ? nodes : o?.use.baseNodes !== undefined && o.use.baseNodes.length === 0 ? [] : undefined,
    })
    return compact({
      id: this.id.value.trim(),
      actors: matchOut(this.actors.values(), o?.actors),
      workflows: listOut(this.workflows.values(), o?.workflows),
      paths: listOut(this.paths.values(), o?.paths),
      events: listOut(this.events.values(), o?.events),
      baseBranches: listOut(this.branches.values(), o?.baseBranches),
      headRepository: textOut(this.#headRepository, o?.headRepository, 'same'),
      approval: matchOut(this.approval.values(), o?.approval),
      use,
    })
  }

  errors(): string[] {
    const e = this.#fieldErrors()
    const out = Object.values(e).filter((v): v is string => Boolean(v))
    for (const n of this.#nodes) out.push(...Object.values(n.errors()).filter((v): v is string => Boolean(v)))
    return out
  }

  #fieldErrors(): Partial<Record<RuleField, string>> {
    const id = this.id.value.trim()
    return {
      id: id ? securityIdError(id, 'A rule name') : 'A rule needs a name.',
      workflows: this.workflows.values().length ? undefined : 'Add at least one workflow.',
      paths: this.paths.values().length ? undefined : 'Add at least one CI file pattern.',
      profile: this.profile.value.trim() ? undefined : 'Name the profile of the run.',
      workers: this.workers.value.trim() ? undefined : 'Name the worker class of the run.',
    }
  }

  render(index: number, count: number, touched: boolean): void {
    const id = this.id.value.trim()
    setText(this.#heading, `Rule ${index + 1}${id ? `: ${id}` : ''}`)
    this.#up.disabled = index === 0
    this.#down.disabled = index === count - 1
    this.headRepository.update(HEAD_REPOSITORY_CHOICES, this.#headRepository)
    const errors = this.#fieldErrors()
    for (const key of Object.keys(this.#fields) as RuleField[]) this.#fields[key].setError(touched ? errors[key] : undefined)
    syncKeyed(this.#nodeList, this.#nodes, (n) => n.key, (n) => n.el, (_el, n) => n.render(touched))
  }
}

type Loaded = { policy?: CiPolicyDetail; builtIn?: CiPolicyDetail; builtInReason?: string }

const EMPTY: CiPolicyDocument = { version: 1, headCi: [] }

export class CiPolicyEditor extends RcElement {
  projectId = ''

  #load = new Loader<string, Loaded>(this, async (projectId) => {
    try {
      const response = await api.getCiPolicy({ projectId })
      // The built-in policy applies, and nothing is stored: start the draft
      // from it, with no revision to expect.
      if (response.builtIn) return { builtIn: response.policy, builtInReason: response.builtInReason }
      return { policy: response.policy }
    } catch (cause) {
      if (cause instanceof ServiceError && cause.isNotFound) return {}
      throw cause
    }
  })
  #seededFor = ''
  #original: CiPolicyDocument = EMPTY
  #revision: string | undefined
  #rules: RuleCard[] = []
  #touched = false
  #busy = false
  #error = ''
  #conflict = false
  #message = ''

  #loading!: HTMLElement
  #loadError!: HTMLDivElement
  #body!: HTMLDivElement
  #status!: HTMLParagraphElement
  #errorBox!: HTMLDivElement
  #notice!: HTMLDivElement
  #reloadInline!: PushButton
  #defaultSource!: HTMLElement
  #defaultProfile!: TextField
  #ruleList!: HTMLDivElement
  #noRules!: HTMLDivElement
  #save!: PushButton
  #reload!: PushButton
  #delete!: PushButton

  protected build(): void {
    this.#loading = skeleton(4)
    this.#loadError = h('div', { class: 'alert alert-error', role: 'alert', hidden: true })
    this.#status = h('p', { class: 'meta' })
    this.#reloadInline = button('Reload', { small: true, onClick: () => void this.#reloadPolicy() })
    this.#errorBox = h('div', { class: 'alert alert-error', role: 'alert', hidden: true }, h('span', { class: 'policy-error-text' }), ' ', this.#reloadInline)
    this.#notice = h('div', { class: 'alert alert-info', role: 'status', hidden: true })
    this.#defaultSource = h('span', { class: 'mono' })
    this.#defaultProfile = textField({ mono: true, placeholder: 'standard', onInput: () => this.invalidate() })
    this.#ruleList = h('div', { class: 'rule-list' })
    this.#noRules = h(
      'div',
      { class: 'empty' },
      h('div', { class: 'empty-title' }, 'No rules.'),
      h('p', null, 'Pull requests run only the trusted CI files of the base branch. Add a rule to let some pull requests run their own CI files.'),
    )
    this.#save = button('Save policy', { variant: 'primary', onClick: () => void this.#savePolicy() })
    this.#reload = button('Discard changes', { onClick: () => void this.#reloadPolicy() })
    this.#delete = button('Delete policy', { variant: 'danger', onClick: () => void this.#deletePolicy() })

    this.#body = h(
      'div',
      { hidden: true },
      h(
        'div',
        { class: 'card' },
        h(
          'div',
          { class: 'card-header' },
          h(
            'div',
            null,
            h('h3', null, 'CI policy'),
            h(
              'p',
              { class: 'meta' },
              'A pull request normally runs the trusted CI files of its base branch. A rule here lets a pull request run its own CI files when the change matches the rule. The first rule that matches is used.',
            ),
          ),
        ),
        this.#status,
        this.#errorBox,
        this.#notice,
        h('h4', { class: 'section-title' }, 'Defaults'),
        h('div', { class: 'field' }, h('div', { class: 'field-label' }, 'CI files when no rule matches'), h('div', null, 'Trusted base CI (', this.#defaultSource, ')')),
        field({ label: 'Default profile', hint: 'The execution profile when no rule matches. Empty means standard.', control: this.#defaultProfile }).el,
      ),
      this.#noRules,
      this.#ruleList,
      h(
        'div',
        { class: 'card' },
        h('div', { class: 'row' }, button('Add rule', { onClick: () => this.#addRule() }), this.#save, this.#reload, this.#delete),
      ),
    )
    this.append(this.#loadError, this.#loading, this.#body)
  }

  #seedLoaded(data: Loaded | undefined): void {
    this.#seed(data?.policy ?? data?.builtIn)
    if (!data?.policy) this.#revision = undefined
  }

  #seed(policy: CiPolicyDetail | undefined): void {
    const doc = policy?.document ?? EMPTY
    this.#original = doc
    this.#revision = policy?.revision
    this.#defaultSource.textContent = doc.defaults?.ciSource ?? 'base'
    this.#defaultProfile.value = doc.defaults?.profile ?? ''
    this.#rules = doc.headCi.map((rule) => this.#newRule(rule))
    this.#touched = false
  }

  #newRule(rule: CiPolicyRule | undefined): RuleCard {
    return new RuleCard(rule, {
      move: (card, by) => {
        const i = this.#rules.indexOf(card)
        const j = i + by
        if (i < 0 || j < 0 || j >= this.#rules.length) return
        const next = [...this.#rules]
        ;[next[i], next[j]] = [next[j], next[i]]
        this.#rules = next
        this.invalidate()
      },
      remove: (card) => {
        this.#rules = this.#rules.filter((r) => r !== card)
        this.invalidate()
      },
      change: () => this.invalidate(),
    })
  }

  #addRule(): void {
    const card = this.#newRule(undefined)
    this.#rules = [...this.#rules, card]
    this.invalidate()
    card.id.focus()
  }

  /** The document the form shows now. */
  document(): CiPolicyDocument {
    const o = this.#original
    let defaults: CiPolicyDefaults | undefined
    const profile = textOut(this.#defaultProfile.value.trim(), o.defaults?.profile)
    if (o.defaults || profile !== undefined) defaults = compact({ ciSource: o.defaults?.ciSource, profile })
    return compact({ version: o.version ?? 1, defaults, headCi: this.#rules.map((r) => r.value()) })
  }

  #problem(): string | undefined {
    const ids = new Set<string>()
    const workflows = new Map<string, string>()
    for (const [i, card] of this.#rules.entries()) {
      const errors = card.errors()
      if (errors.length) return `Rule ${i + 1}: ${errors[0]}`
      const rule = card.value()
      if (ids.has(rule.id)) return `Two rules have the name "${rule.id}". Each rule needs its own name.`
      ids.add(rule.id)
      for (const w of rule.workflows ?? []) {
        const prior = workflows.get(w)
        if (prior) return `The workflow "${w}" is in the rules "${prior}" and "${rule.id}". A workflow can be in one rule only.`
        workflows.set(w, rule.id)
      }
    }
    return undefined
  }

  #fail(cause: unknown, fallback: string): void {
    this.#conflict = cause instanceof ServiceError && cause.code === 'conflict'
    this.#error = this.#conflict
      ? 'Someone changed the policy after you loaded it. Your changes are not saved. Reload to see the new policy.'
      : describeError(cause, fallback)
  }

  async #savePolicy(): Promise<void> {
    if (this.#busy) return
    this.#touched = true
    this.#message = ''
    this.#conflict = false
    this.#error = this.#problem() ?? ''
    if (this.#error) {
      this.invalidate()
      return
    }
    this.#busy = true
    this.invalidate()
    const document = this.document()
    try {
      const request: Parameters<typeof api.putCiPolicy>[0] = { projectId: this.projectId, document }
      if (this.#revision) request.expectedRevision = this.#revision
      const { policy } = await api.putCiPolicy(request)
      if (!this.isActive()) return
      this.#revision = policy.revision
      // Keep the draft on screen. What was saved is now the base of the next round trip.
      this.#original = document
      this.#rules.forEach((card, i) => (card.original = document.headCi[i]))
      this.#load.mutate({ policy })
      this.#message = `Policy saved. Revision ${policy.revision}.`
    } catch (cause) {
      this.#fail(cause, 'The policy could not be saved.')
    } finally {
      this.#busy = false
      this.invalidate()
    }
  }

  async #reloadPolicy(): Promise<void> {
    this.#error = ''
    this.#conflict = false
    this.#message = ''
    await this.#load.reload()
    if (!this.isActive()) return
    if (!this.#load.state.error) this.#seedLoaded(this.#load.state.data)
    this.invalidate()
  }

  async #deletePolicy(): Promise<void> {
    if (this.#busy || !this.#revision) return
    const ok = await confirmAction('Delete the CI policy of this project? Pull requests then run only the trusted CI files of the base branch.', {
      okLabel: 'Delete policy',
    })
    if (!ok || !this.isActive()) return
    this.#busy = true
    this.#error = ''
    this.#message = ''
    this.invalidate()
    try {
      await api.deleteCiPolicy({ projectId: this.projectId, expectedRevision: this.#revision })
      if (!this.isActive()) return
      this.#load.mutate({})
      this.#seed(undefined)
      this.#message = 'Policy deleted.'
    } catch (cause) {
      this.#fail(cause, 'The policy could not be deleted.')
    } finally {
      this.#busy = false
      this.invalidate()
    }
  }

  render(): void {
    this.#load.sync(this.projectId || undefined)
    const state = this.#load.state
    if (state.loaded && this.#seededFor !== this.projectId) {
      this.#seededFor = this.projectId
      this.#seedLoaded(state.data)
    }
    const ready = this.#seededFor === this.projectId && Boolean(this.projectId)
    show(this.#loading, !ready && !state.error)
    show(this.#loadError, state.error)
    setText(this.#loadError, state.error ? describeError(state.error, 'The CI policy could not be loaded.') : '')
    show(this.#body, ready)
    if (!ready) return

    const policy = state.data?.policy
    setText(
      this.#status,
      policy && this.#revision
        ? `Revision ${this.#revision}. Saved ${new Date(policy.updatedAt).toLocaleString()}${policy.updatedBy ? ` by ${policy.updatedBy}` : ''}.`
        : state.data?.builtIn
          ? `No policy is stored for this project. The built-in policy (revision ${state.data.builtIn.revision}) applies: ${state.data.builtInReason ?? 'the organization has the secure defaults profile'}. The rules below are the built-in policy; save to store them for this project.`
          : 'No policy is stored for this project. Save to make one.',
    )
    show(this.#errorBox, this.#error)
    setText(this.#errorBox.querySelector('.policy-error-text')!, this.#error)
    show(this.#reloadInline, this.#conflict)
    show(this.#notice, this.#message)
    setText(this.#notice, this.#message)

    show(this.#noRules, this.#rules.length === 0)
    const count = this.#rules.length
    syncKeyed(this.#ruleList, this.#rules, (r) => r.key, (r) => r.el, (_el, r, i) => r.render(i, count, this.#touched))

    this.#save.disabled = this.#busy
    this.#reload.disabled = this.#busy
    this.#delete.disabled = this.#busy || !this.#revision
    setButtonText(this.#save, this.#busy ? 'Saving…' : 'Save policy')
  }
}

export const policyEditorTag = 'rc-ci-policy-editor'
define(policyEditorTag, CiPolicyEditor)

export function ciPolicyEditor(projectId: string): CiPolicyEditor {
  const el = document.createElement(policyEditorTag) as CiPolicyEditor
  el.projectId = projectId
  return el
}
