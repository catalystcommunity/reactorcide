import type { PushButton, TextField } from '@catalystcommunity/controls'
import { api } from '~/api/client.ts'
import type { CiTrustSettings, EnumChoice, GetCiTrustSettingsResponse, RequiredReview } from '~/api/csilapi/types.gen.ts'
import { ChoiceMultiSelect, ChoiceSelect } from '~/components/ChoiceInput.ts'
import { ChipList, pathPatternError, securityIdError, subjectError, subjectHint } from '~/components/ChipList.ts'
import { field, type Field } from '~/components/Field.ts'
import { Action, ActionError, describeError } from '~/components/ManagementPage.ts'
import { skeleton } from '~/components/States.ts'
import { button, setButtonText, textField } from '~/ui/controls.ts'
import { h, setText, show, syncKeyed, uid } from '~/ui/dom.ts'
import { RcElement, define } from '~/ui/element.ts'
import { Loader } from '~/ui/load.ts'
import './chips.css'

/**
 * The CI trust settings of an organization or of one project.
 *
 * A settings row stores only what it sets. An ABSENT field means "inherit"
 * on a project and "default" on an organization. The codec keeps absent and
 * empty apart, so this form sends `undefined` for "inherit" and `[]` for an
 * explicit empty list. Each field has its own choice between the two.
 *
 * A project owner can only make a project stricter. The coordinator refuses a
 * looser save with "forbidden" and lists the reasons; the form shows that
 * message as it is.
 */

export const RUN_GATE_CHOICES: EnumChoice[] = [
  { value: 'forks', label: 'Forks', description: 'The pull request comes from a fork.' },
  { value: 'first_time_contributors', label: 'First-time contributors', description: 'The author has no merged change in this repository.' },
  { value: 'outside_contributors', label: 'Outside contributors', description: 'The author has no write access to the repository.' },
]

export const AUTHORITY_CHOICES: EnumChoice[] = [
  { value: 'no_secrets', label: 'No secrets', description: 'Run the trusted base CI with the pr-untrusted profile. No secrets.' },
  { value: 'normal', label: 'Normal', description: 'Run the trusted base CI with the normal profile and its secrets.' },
]

const DEFAULTS: Required<Omit<CiTrustSettings, 'requiredReviews'>> = {
  runGate: [],
  gatedRunAuthority: 'no_secrets',
  runApprovers: ['project_owner', 'repository_write'],
  overrideApprovers: ['project_owner', 'repository_write'],
  overrideProfile: 'pr-untrusted',
}

type Mode = 'inherit' | 'set'

function label(choices: EnumChoice[], value: string): string {
  return choices.find((c) => c.value === value)?.label ?? value
}

function gateText(values: string[] | undefined): string {
  if (!values) return '—'
  return values.length ? values.map((v) => label(RUN_GATE_CHOICES, v)).join(', ') : 'No gate. Every pull request runs.'
}

function subjectsText(values: string[] | undefined): string {
  if (!values) return '—'
  return values.length ? values.join(', ') : 'Nobody'
}

function reviewsText(values: RequiredReview[] | undefined): string {
  if (!values) return '—'
  return values.length ? values.map((r) => r.id).join(', ') : 'None'
}

/**
 * One setting: the choice between inherit (or default) and an own value, the
 * editor for the own value, and the values to compare with.
 */
class Setting {
  readonly el: HTMLDivElement
  readonly #mode: ChoiceSelect
  readonly #editor: HTMLElement
  readonly #org: HTMLDivElement
  readonly #force: HTMLDivElement
  readonly #modes: EnumChoice[]
  mode: Mode = 'inherit'

  constructor(options: { label: string; hint: string; modes: EnumChoice[]; editor: HTMLElement; onChange: () => void }) {
    this.#modes = options.modes
    this.#editor = h('div', { class: 'setting-editor' }, options.editor)
    this.#mode = new ChoiceSelect({
      value: 'inherit',
      choices: options.modes,
      onChange: (value) => {
        this.mode = value === 'set' ? 'set' : 'inherit'
        options.onChange()
      },
    })
    this.#org = h('div', { hidden: true })
    this.#force = h('div')
    const f = field({ label: options.label, hint: options.hint, control: this.#mode.el })
    f.el.append(this.#editor, h('div', { class: 'setting-values' }, this.#org, this.#force))
    this.el = h('div', { class: 'setting' }, f.el)
  }

  setMode(mode: Mode): void {
    this.mode = mode
  }

  update(orgText: string | undefined, forceText: string): void {
    this.#mode.update(this.#modes, this.mode)
    show(this.#editor, this.mode === 'set')
    show(this.#org, orgText !== undefined)
    setText(this.#org, orgText !== undefined ? `Organization value: ${orgText}` : '')
    setText(this.#force, `In force now: ${forceText}`)
  }
}

/** One required review in the editor. Its controls hold the draft. */
class ReviewRow {
  readonly key = uid('review')
  readonly el: HTMLDivElement
  readonly id: TextField
  readonly description: TextField
  readonly paths: ChipList
  readonly accept: ChipList
  readonly count: TextField
  readonly #fields: Record<'id' | 'accept' | 'count', Field>
  readonly #heading: HTMLHeadingElement

  constructor(onRemove: (row: ReviewRow) => void, onChange: () => void) {
    this.id = textField({ mono: true, placeholder: 'security', onInput: onChange })
    this.description = textField({ placeholder: 'A security team member reviews the change.' })
    this.paths = new ChipList({ ariaLabel: 'Add a path pattern', placeholder: 'src/auth/**', mono: true, validate: pathPatternError })
    this.accept = new ChipList({
      ariaLabel: 'Add a subject that can accept',
      placeholder: 'reactorcide_group:security',
      mono: true,
      validate: (v) => subjectError(v, { nonAuthor: true }),
      onChange,
    })
    this.count = textField({ placeholder: '1', mono: true, onInput: onChange })
    this.#fields = {
      id: field({ label: 'Review id', required: true, hint: 'Lowercase letters, digits and hyphens. It is also the label in /reactorcide accept LABEL.', control: this.id }),
      accept: field({ label: 'Who can accept', required: true, hint: subjectHint({ nonAuthor: true }), control: this.accept.el, labelTarget: this.accept.input }),
      count: field({ label: 'People needed', hint: 'The number of different people who must accept. Empty means 1.', control: this.count }),
    }
    this.#heading = h('h4', { class: 'section-title' })
    this.el = h(
      'div',
      { class: 'sub-card review-row' },
      h('div', { class: 'sub-card-header' }, this.#heading, button('Remove review', { small: true, variant: 'danger', onClick: () => onRemove(this) })),
      h('div', { class: 'form-row' }, this.#fields.id.el, this.#fields.count.el),
      field({ label: 'Description', hint: 'Tell people what this review is for.', control: this.description }).el,
      field({ label: 'Which changes', hint: 'Path patterns with * and **. Empty means every pull request.', control: this.paths.el, labelTarget: this.paths.input }).el,
      this.#fields.accept.el,
    )
  }

  setValue(review: RequiredReview): void {
    this.id.value = review.id
    this.description.value = review.description ?? ''
    this.paths.set(review.paths ?? [])
    this.accept.set(review.accept)
    this.count.value = review.count !== undefined ? String(review.count) : ''
  }

  value(): RequiredReview {
    const out: RequiredReview = { id: this.id.value.trim(), accept: this.accept.values() }
    const description = this.description.value.trim()
    if (description) out.description = description
    const paths = this.paths.values()
    if (paths.length) out.paths = paths
    const count = this.count.value.trim()
    if (count) out.count = Number(count)
    return out
  }

  errors(): Partial<Record<'id' | 'accept' | 'count', string>> {
    const v = this.value()
    const count = this.count.value.trim()
    return {
      id: v.id ? securityIdError(v.id, 'A review id') : 'A review needs an id.',
      accept: v.accept.length ? undefined : 'Add at least one subject that can accept.',
      count: !count || (/^\d+$/.test(count) && Number(count) >= 1 && Number(count) <= 20) ? undefined : 'Write a number from 1 to 20.',
    }
  }

  render(showErrors: boolean): void {
    const errors = this.errors()
    for (const key of ['id', 'accept', 'count'] as const) this.#fields[key].setError(showErrors ? errors[key] : undefined)
    setText(this.#heading, this.id.value.trim() ? `Review "${this.id.value.trim()}"` : 'New review')
  }
}

/** Add, remove and edit required reviews. */
export class ReviewEditor {
  readonly el: HTMLDivElement
  #rows: ReviewRow[] = []
  readonly #list: HTMLDivElement
  readonly #empty: HTMLParagraphElement
  readonly #onChange: () => void
  touched = false

  constructor(onChange: () => void) {
    this.#onChange = onChange
    this.#list = h('div', { class: 'review-list' })
    this.#empty = h('p', { class: 'meta' }, 'No reviews. Add one to require it.')
    this.el = h('div', null, this.#empty, this.#list, button('Add review', { small: true, onClick: () => this.#add() }))
  }

  set(reviews: readonly RequiredReview[]): void {
    this.#rows = reviews.map((review) => {
      const row = this.#newRow()
      row.setValue(review)
      return row
    })
    this.touched = false
  }

  values(): RequiredReview[] {
    return this.#rows.map((row) => row.value())
  }

  /** The first problem, or undefined. */
  error(): string | undefined {
    const seen = new Set<string>()
    for (const row of this.#rows) {
      const errors = row.errors()
      const first = errors.id ?? errors.accept ?? errors.count
      if (first) return `Fix the required reviews: ${first}`
      const id = row.value().id
      if (seen.has(id)) return `Two reviews have the id "${id}". Each review needs its own id.`
      seen.add(id)
    }
    return undefined
  }

  #newRow(): ReviewRow {
    return new ReviewRow(
      (row) => {
        this.#rows = this.#rows.filter((r) => r !== row)
        this.#onChange()
      },
      () => this.#onChange(),
    )
  }

  #add(): void {
    const row = this.#newRow()
    this.#rows = [...this.#rows, row]
    this.#onChange()
    row.id.focus()
  }

  render(): void {
    show(this.#empty, this.#rows.length === 0)
    syncKeyed(this.#list, this.#rows, (r) => r.key, (r) => r.el, (_el, r) => r.render(this.touched))
  }
}

function createReadonlyReview(): HTMLLIElement {
  return h('li', { class: 'sub-card' }, h('div', { class: 'mono ro-id' }), h('div', { class: 'meta ro-desc' }), h('div', { class: 'meta ro-detail' }))
}

function updateReadonlyReview(li: HTMLLIElement, review: RequiredReview): void {
  setText(li.querySelector('.ro-id')!, review.id)
  const desc = li.querySelector<HTMLElement>('.ro-desc')!
  show(desc, review.description)
  setText(desc, review.description ?? '')
  const count = review.count ?? 1
  const paths = review.paths?.length ? `Changes to ${review.paths.join(', ')}` : 'Every pull request'
  setText(li.querySelector('.ro-detail')!, `${paths}. ${count} ${count === 1 ? 'person' : 'people'} from: ${review.accept.join(', ')}.`)
}

type Key = { organization: string; projectId?: string }

/**
 * The form. Set `organization` (the org NAME) and, for a project,
 * `projectId` before it connects, or call `setScope()` later.
 */
export class CiTrustSettingsForm extends RcElement {
  organization = ''
  projectId: string | undefined = undefined

  #load = new Loader<Key, GetCiTrustSettingsResponse>(this, (key) => api.getCiTrustSettings(key))
  #seededFor = ''
  #save = new Action(() => this.invalidate())
  #saveError = new ActionError()
  #touched = false

  #loading!: HTMLElement
  #loadError!: HTMLDivElement
  #form!: HTMLFormElement
  #intro!: HTMLParagraphElement
  #stricter!: HTMLDivElement
  #saved!: HTMLDivElement
  #submit!: PushButton

  #gate!: Setting
  #gateList!: ChoiceMultiSelect
  #gateValues: string[] = []
  #authority!: ChoiceSelect
  #authorityValue = 'inherit'
  #authorityOrg!: HTMLDivElement
  #authorityForce!: HTMLDivElement
  #runApprovers!: Setting
  #runApproverList!: ChipList
  #overrideApprovers!: Setting
  #overrideApproverList!: ChipList
  #profile!: Setting
  #profileInput!: TextField
  #profileField!: Field
  #reviews!: Setting
  #reviewEditor!: ReviewEditor
  #orgReviews!: HTMLDivElement
  #orgReviewList!: HTMLUListElement
  #orgReviewsNone!: HTMLParagraphElement

  setScope(organization: string, projectId?: string): void {
    if (this.organization === organization && this.projectId === projectId) return
    this.organization = organization
    this.projectId = projectId
    this.invalidate()
  }

  #isProject(): boolean {
    return Boolean(this.projectId)
  }

  #key(): Key | undefined {
    if (!this.organization) return undefined
    return this.projectId ? { organization: this.organization, projectId: this.projectId } : { organization: this.organization }
  }

  protected build(): void {
    const project = this.#isProject()
    const rerender = (): void => this.invalidate()
    const modes: EnumChoice[] = project
      ? [
          { value: 'inherit', label: 'Inherit from organization', description: 'Use the value of the organization.' },
          { value: 'set', label: 'Set for this project', description: 'Use the value below for this project only.' },
        ]
      : [
          { value: 'inherit', label: 'Use default', description: 'Use the Reactorcide default.' },
          { value: 'set', label: 'Set for this organization', description: 'Use the value below for every project that inherits it.' },
        ]

    this.#gateList = new ChoiceMultiSelect({ ariaLabel: 'Conditions that hold a pull request', choices: RUN_GATE_CHOICES, onChange: (values) => { this.#gateValues = values; rerender() } })
    this.#gate = new Setting({
      label: 'Hold pull requests for approval',
      hint: 'A held pull request runs only after a run approver comments /reactorcide approve-ci. Select none for no gate.',
      modes,
      editor: this.#gateList.el,
      onChange: rerender,
    })

    this.#authority = new ChoiceSelect({ value: 'inherit', choices: [authorityInherit(project), ...AUTHORITY_CHOICES], onChange: (value) => { this.#authorityValue = value; rerender() } })
    this.#authorityOrg = h('div', { hidden: true })
    this.#authorityForce = h('div')
    const authorityField = field({ label: 'An approved held run gets', hint: 'What a held pull request may use after approve-ci.', control: this.#authority.el })
    authorityField.el.append(h('div', { class: 'setting-values' }, this.#authorityOrg, this.#authorityForce))

    this.#runApproverList = new ChipList({ ariaLabel: 'Add a run approver', placeholder: 'repository_write', mono: true, validate: (v) => subjectError(v), onChange: rerender })
    this.#runApprovers = new Setting({
      label: 'Run approvers',
      hint: `Who can approve-ci, retry and rerun. ${subjectHint()}`,
      modes,
      editor: this.#runApproverList.el,
      onChange: rerender,
    })

    this.#overrideApproverList = new ChipList({ ariaLabel: 'Add an override approver', placeholder: 'project_owner', mono: true, validate: (v) => subjectError(v), onChange: rerender })
    this.#overrideApprovers = new Setting({
      label: 'Override approvers',
      hint: 'Who can comment /reactorcide override-ci. That runs the CI files of the pull request itself.',
      modes,
      editor: this.#overrideApproverList.el,
      onChange: rerender,
    })

    this.#profileInput = textField({ mono: true, placeholder: 'pr-untrusted', onInput: rerender })
    this.#profileField = field({ label: 'Profile', control: this.#profileInput })
    this.#profile = new Setting({
      label: 'Override run profile',
      hint: 'The execution profile of a run that override-ci starts.',
      modes,
      editor: this.#profileField.el,
      onChange: rerender,
    })

    this.#reviewEditor = new ReviewEditor(rerender)
    this.#orgReviewList = h('ul', { class: 'readonly-list' })
    this.#orgReviewsNone = h('p', { class: 'meta', hidden: true }, 'The organization requires no reviews.')
    this.#orgReviews = h(
      'div',
      { hidden: true },
      h('div', { class: 'field-label' }, 'Organization reviews'),
      h('p', { class: 'meta' }, 'These apply to every project. Change them on the organization CI trust page.'),
      this.#orgReviewsNone,
      this.#orgReviewList,
    )
    this.#reviews = new Setting({
      label: project ? 'Project required reviews' : 'Required reviews',
      hint: project
        ? 'Reviews that a pull request needs before it can merge. They add to the organization reviews.'
        : 'Reviews that a pull request needs before it can merge. They apply to every project.',
      modes: project
        ? [
            { value: 'inherit', label: 'Only the organization reviews', description: 'This project adds no reviews.' },
            { value: 'set', label: 'Add project reviews', description: 'This project adds the reviews below.' },
          ]
        : [
            { value: 'inherit', label: 'No reviews', description: 'Pull requests need no review from Reactorcide.' },
            { value: 'set', label: 'Set reviews', description: 'Every project needs the reviews below.' },
          ],
      editor: this.#reviewEditor.el,
      onChange: rerender,
    })

    this.#intro = h('p', { class: 'meta' })
    this.#stricter = h(
      'div',
      { class: 'alert alert-info', hidden: true },
      'You can make these settings stricter. Only an organization administrator can make them less strict than the organization.',
    )
    this.#saved = h('div', { class: 'alert alert-info', role: 'status', hidden: true }, 'CI trust settings saved.')
    this.#submit = button('Save CI trust settings', { type: 'submit', variant: 'primary' })
    this.#loading = skeleton(4)
    this.#loadError = h('div', { class: 'alert alert-error', role: 'alert', hidden: true })

    this.#form = h(
      'form',
      { novalidate: true, hidden: true, onSubmit: (event: Event) => void this.#submitForm(event) },
      this.#stricter,
      this.#saveError.el,
      this.#saved,
      h('h3', { class: 'section-title' }, 'Run gate'),
      this.#gate.el,
      h('div', { class: 'setting' }, authorityField.el),
      this.#runApprovers.el,
      h('h3', { class: 'section-title' }, 'Override'),
      this.#overrideApprovers.el,
      this.#profile.el,
      h('h3', { class: 'section-title' }, 'Required reviews'),
      this.#orgReviews,
      this.#reviews.el,
      h('div', { class: 'row' }, this.#submit),
    )
    this.classList.add('card')
    this.append(h('div', { class: 'card-header' }, h('div', null, h('h3', null, 'CI trust settings'), this.#intro)), this.#loadError, this.#loading, this.#form)
  }

  /** Puts the stored settings into the controls. Runs one time for each scope. */
  #seed(data: GetCiTrustSettingsResponse): void {
    const s = data.settings
    const fallback = data.organizationEffective ?? data.effective
    this.#gate.setMode(s.runGate !== undefined ? 'set' : 'inherit')
    this.#gateValues = [...(s.runGate ?? fallback.runGate ?? [])]
    this.#gateList.update(RUN_GATE_CHOICES, this.#gateValues)
    this.#authorityValue = s.gatedRunAuthority ?? 'inherit'
    this.#runApprovers.setMode(s.runApprovers !== undefined ? 'set' : 'inherit')
    this.#runApproverList.set(s.runApprovers ?? fallback.runApprovers ?? DEFAULTS.runApprovers)
    this.#overrideApprovers.setMode(s.overrideApprovers !== undefined ? 'set' : 'inherit')
    this.#overrideApproverList.set(s.overrideApprovers ?? fallback.overrideApprovers ?? DEFAULTS.overrideApprovers)
    this.#profile.setMode(s.overrideProfile !== undefined ? 'set' : 'inherit')
    this.#profileInput.value = s.overrideProfile ?? fallback.overrideProfile ?? DEFAULTS.overrideProfile
    this.#reviews.setMode(s.requiredReviews !== undefined ? 'set' : 'inherit')
    this.#reviewEditor.set((s.requiredReviews ?? []).map(({ scope: _scope, ...review }) => review))
    this.#touched = false
  }

  /** What the form sends: `undefined` for inherit or default, the value otherwise. */
  settings(): CiTrustSettings {
    const out: CiTrustSettings = {}
    if (this.#gate.mode === 'set') out.runGate = [...this.#gateValues]
    if (this.#authorityValue !== 'inherit') out.gatedRunAuthority = this.#authorityValue
    if (this.#runApprovers.mode === 'set') out.runApprovers = this.#runApproverList.values()
    if (this.#overrideApprovers.mode === 'set') out.overrideApprovers = this.#overrideApproverList.values()
    if (this.#profile.mode === 'set') out.overrideProfile = this.#profileInput.value.trim()
    if (this.#reviews.mode === 'set') out.requiredReviews = this.#reviewEditor.values()
    return out
  }

  #errors(): { profile?: string; reviews?: string } {
    return {
      profile: this.#profile.mode === 'set' && !this.#profileInput.value.trim() ? 'Name a profile, or choose the other option.' : undefined,
      reviews: this.#reviews.mode === 'set' ? this.#reviewEditor.error() : undefined,
    }
  }

  async #submitForm(event: Event): Promise<void> {
    event.preventDefault()
    const key = this.#key()
    const current = this.#load.state.data
    if (!key || !current) return
    this.#touched = true
    this.#reviewEditor.touched = true
    show(this.#saved, false)
    const errors = this.#errors()
    if (errors.profile || errors.reviews) {
      this.#save.error = errors.reviews ?? errors.profile
      this.invalidate()
      return
    }
    let response: Awaited<ReturnType<typeof api.putCiTrustSettings>> | undefined
    const ok = await this.#save.run('The CI trust settings could not be saved.', async () => {
      response = await api.putCiTrustSettings({ ...key, settings: this.settings() })
    })
    if (!ok || !response || !this.isActive()) return
    this.#load.mutate({ ...current, settings: response.settings, effective: response.effective })
    show(this.#saved, true)
  }

  render(): void {
    const key = this.#key()
    this.#load.sync(key)
    const state = this.#load.state
    const data = state.data
    const signature = key ? JSON.stringify(key) : ''
    if (data && this.#seededFor !== signature) {
      this.#seededFor = signature
      this.#seed(data)
    }
    const project = this.#isProject()
    setText(
      this.#intro,
      project
        ? 'Who can run pull requests, approve them and override the trusted CI for this project. A field can follow the organization.'
        : 'Who can run pull requests, approve them and override the trusted CI. Projects follow these values unless they set their own.',
    )
    show(this.#loading, !data && !state.error)
    show(this.#loadError, state.error && !data)
    setText(this.#loadError, state.error ? describeError(state.error, 'The CI trust settings could not be loaded.') : '')
    show(this.#form, data && this.#seededFor === signature)
    if (!data) return

    const org = project ? data.organizationEffective : undefined
    const force = data.effective
    show(this.#stricter, project && !data.canLoosen)

    this.#gate.update(org ? gateText(org.runGate) : undefined, gateText(force.runGate))
    this.#gateList.update(RUN_GATE_CHOICES, this.#gateValues)
    this.#authority.update([authorityInherit(project), ...AUTHORITY_CHOICES], this.#authorityValue)
    show(this.#authorityOrg, org)
    setText(this.#authorityOrg, org ? `Organization value: ${label(AUTHORITY_CHOICES, org.gatedRunAuthority ?? DEFAULTS.gatedRunAuthority)}` : '')
    setText(this.#authorityForce, `In force now: ${label(AUTHORITY_CHOICES, force.gatedRunAuthority ?? DEFAULTS.gatedRunAuthority)}`)
    this.#runApprovers.update(org ? subjectsText(org.runApprovers) : undefined, subjectsText(force.runApprovers))
    this.#overrideApprovers.update(org ? subjectsText(org.overrideApprovers) : undefined, subjectsText(force.overrideApprovers))
    this.#profile.update(org ? (org.overrideProfile ?? '—') : undefined, force.overrideProfile ?? '—')
    this.#profileField.setError(this.#touched ? this.#errors().profile : undefined)

    const orgReviews = project ? (org?.requiredReviews ?? force.requiredReviews?.filter((r) => r.scope === 'org') ?? []) : []
    show(this.#orgReviews, project)
    syncKeyed(this.#orgReviewList, orgReviews, (r) => r.id, createReadonlyReview, updateReadonlyReview)
    show(this.#orgReviewsNone, project && orgReviews.length === 0)
    this.#reviews.update(org ? reviewsText(org.requiredReviews) : undefined, reviewsText(force.requiredReviews))
    this.#reviewEditor.render()

    this.#saveError.set(this.#save.error)
    this.#submit.disabled = this.#save.busy
    setButtonText(this.#submit, this.#save.busy ? 'Saving…' : 'Save CI trust settings')
  }
}

function authorityInherit(project: boolean): EnumChoice {
  return project
    ? { value: 'inherit', label: 'Inherit from organization', description: 'Use the value of the organization.' }
    : { value: 'inherit', label: 'Use default', description: 'Use the Reactorcide default: no secrets.' }
}

export const trustTag = 'rc-ci-trust-settings'
define(trustTag, CiTrustSettingsForm)

/** Makes the form for an organization (no project) or a project. */
export function ciTrustSettingsForm(organization: string, projectId?: string): CiTrustSettingsForm {
  const el = document.createElement(trustTag) as CiTrustSettingsForm
  el.organization = organization
  el.projectId = projectId
  return el
}
