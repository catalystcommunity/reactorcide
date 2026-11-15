import { ManagedOrgPage, orgHref } from '~/components/ManagedOrg.ts'
import { ciTrustSettingsForm, type CiTrustSettingsForm } from '~/components/TrustSettings.ts'
import { h, setAttr } from '~/ui/dom.ts'
import { define } from '~/ui/element.ts'
import { href } from '~/ui/router.ts'

/**
 * The CI trust settings of an organization. Every project follows them,
 * unless the project sets its own value for a field. Required reviews set
 * here apply to every project.
 *
 * The settings operations name the org by NAME, like the worker classes.
 */
export class OrgTrustPage extends ManagedOrgPage {
  protected readonly what = 'CI trust settings'

  #form!: CiTrustSettingsForm
  #rolesLink!: HTMLAnchorElement

  #orgName(): string {
    return this.managed.org()?.name ?? ''
  }

  protected buildBody(): HTMLElement {
    this.#rolesLink = h('a', { class: 'btn btn-sm' }, 'Manage access')
    this.#form = ciTrustSettingsForm(this.#orgName())
    return h(
      'div',
      { class: 'page' },
      h(
        'div',
        { class: 'page-header' },
        h(
          'div',
          null,
          h('h1', null, 'CI trust'),
          h('p', { class: 'meta' }, 'Decide which pull requests wait for approval, who approves them, and which reviews a change needs. Projects follow these values.'),
        ),
        h('div', { class: 'row', style: { alignItems: 'center' } }, this.orgPicker(), this.#rolesLink),
      ),
      this.#form,
    )
  }

  protected renderBody(): void {
    setAttr(this.#rolesLink, 'href', href(orgHref('/org/roles', this.managed.orgId())))
    this.#form.setScope(this.#orgName())
  }
}

export const tag = 'rc-org-trust-page'
define(tag, OrgTrustPage)
