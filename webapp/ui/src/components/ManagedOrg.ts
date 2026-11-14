import type { OrgSummary } from '~/api/csilapi/types.gen.ts'
import { RcPage } from '~/app/page.ts'
import { SESSION, adminOrgIds, canManageSomeOrg, session } from '~/lib/session.ts'
import type { ResourceHandle } from '~/store/index.ts'
import { useOrgs } from '~/store/resources.ts'
import { h, show } from '~/ui/dom.ts'
import { ROUTE, setQuery } from '~/ui/router.ts'
import { notPermitted } from './ManagementPage.ts'
import { OrgPicker, pickInitialOrg } from './OrgPicker.ts'
import { skeleton } from './States.ts'

/**
 * The org an org-management page manages, and how to change it.
 *
 * These pages used to pass the caller's own user id as the org id. Projects,
 * groups, roles and secrets live in `organizations` rows with their own ids,
 * and a person who signed in through LinkKeys has no such row at all, so the
 * pages managed an org that owned nothing: a role granted there reached
 * nobody and a secret set there was resolvable by no project.
 *
 * The choice lives in `?org=` so it survives moving between Access, Groups
 * and Secrets and can be linked to. Absent or not administered by the caller,
 * it falls back to the default org when they may manage it, else the first
 * one they may. `orgId()` is '' until the list has loaded or when the caller
 * administers nothing; callers must not issue a request with that.
 */
export interface ManagedOrg {
  /** The chosen org's id, or '' when there is none yet. */
  orgId(): string
  /** The chosen org, or undefined when there is none yet. */
  org(): OrgSummary | undefined
  /** Every org the caller administers. */
  orgs(): OrgSummary[]
  setOrg(orgId: string): void
}

/**
 * The base of every org-management page, and the gate it passes through.
 *
 * Order matters. While the session is still loading nothing is drawn, so a
 * page never flashes "not permitted" at somebody who is about to be. Then the
 * caller must administer SOME org (the nav uses the same test), then the org
 * list must have answered, then there must be an org to manage. Only then is
 * the body built, and only then does any org-scoped request go out.
 *
 * A subclass sets `what`, builds its body once in `buildBody()` and updates
 * it in `renderBody()`, which runs only while the gate is open. Put
 * `this.orgPicker()` in the body's header.
 */
export abstract class ManagedOrgPage extends RcPage {
  static watches = [ROUTE, SESSION]

  /** What the page manages, for the refusal text ("worker classes"). */
  protected abstract readonly what: string

  #orgs!: ResourceHandle<{ orgs: OrgSummary[] }>
  #loading!: HTMLElement
  #refused!: HTMLElement
  #noOrg!: HTMLElement
  #body: HTMLElement | null = null
  #picker: OrgPicker | null = null

  readonly managed: ManagedOrg = {
    orgId: () => this.#orgId(),
    org: () => this.#allowed().find((org) => org.orgId === this.#orgId()),
    orgs: () => this.#allowed(),
    setOrg: (orgId) => setQuery({ org: orgId }),
  }

  #may(org: OrgSummary): boolean {
    const current = session()
    return Boolean(current?.is_global_admin) || adminOrgIds(current).includes(org.orgId)
  }

  #allowed(): OrgSummary[] {
    return (this.#orgs?.state().data?.orgs ?? []).filter((org) => this.#may(org))
  }

  #orgId(): string {
    const requested = this.route().query.get('org') ?? ''
    if (requested && this.#allowed().some((org) => org.orgId === requested)) return requested
    return pickInitialOrg(this.#orgs?.state().data?.orgs ?? [], (org) => this.#may(org))
  }

  protected build(): void {
    this.#loading = h('div', { class: 'page' }, skeleton(3))
    this.#refused = notPermitted(this.what)
    this.#noOrg = h(
      'div',
      { class: 'page' },
      h(
        'div',
        { class: 'empty' },
        h('div', { class: 'empty-title' }, 'You do not administer any organization.'),
        h('p', null, `Managing ${this.what} needs the admin role on an organization. Ask an administrator to grant it.`),
      ),
    )
    this.append(this.#loading, this.#refused, this.#noOrg)
  }

  protected connected(): void {
    this.#orgs = useOrgs(this)
  }

  /** The org picker as it appears in every org-management page header. */
  protected orgPicker(): HTMLElement {
    this.#picker ??= new OrgPicker({ ariaLabel: 'Organization', onChange: (orgId) => this.managed.setOrg(orgId) })
    return h('div', { class: 'row org-header-picker' }, h('span', { class: 'field-label', 'aria-hidden': 'true' }, 'Organization'), this.#picker.el)
  }

  /** Makes the page body. Runs one time, when the gate first opens. */
  protected abstract buildBody(): HTMLElement

  /** Updates the page body. Runs on each render while the gate is open. */
  protected abstract renderBody(): void

  render(): void {
    const current = session()
    const orgs = this.#orgs.state()
    const sessionLoading = !current
    const refused = !sessionLoading && !canManageSomeOrg(current)
    const listLoading = !sessionLoading && !refused && !orgs.loaded && !orgs.error
    const open = !sessionLoading && !refused && !listLoading && Boolean(this.#orgId())
    const noOrg = !sessionLoading && !refused && !listLoading && !open

    show(this.#loading, sessionLoading || listLoading)
    show(this.#refused, refused)
    show(this.#noOrg, noOrg)

    if (open && !this.#body) {
      this.#body = this.buildBody()
      this.append(this.#body)
    }
    if (this.#body) show(this.#body, open)
    if (open) {
      this.#picker?.update(this.#allowed(), this.#orgId())
      this.renderBody()
    }
  }
}

/** A link to another org-management page that keeps the chosen org. */
export function orgHref(path: string, orgId: string): string {
  return orgId ? `${path}?org=${encodeURIComponent(orgId)}` : path
}
