import { envDefaultsCard, type EnvDefaultsCard } from '~/components/EnvDefaults.ts'
import { imagePolicyCard, type ImagePolicyCard } from '~/components/ImagePolicy.ts'
import { ManagedOrgPage, orgHref } from '~/components/ManagedOrg.ts'
import { priorityLaneSettings, type PriorityLaneSettings } from '~/components/Priority.ts'
import { refClassSettings, type RefClassSettings } from '~/components/RefClasses.ts'
import { shedLevelCard, type ShedLevelCard } from '~/components/ShedLevel.ts'
import { ciTrustSettingsForm, type CiTrustSettingsForm } from '~/components/TrustSettings.ts'
import { orgReportingCard, requiredSetOrgCard, type OrgReportingCard, type RequiredSetOrgCard } from '~/components/OrgSettings.ts'
import { oidcSettingsCard, type OidcSettingsCard } from '~/components/Oidc.ts'
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
  #refClasses!: RefClassSettings
  #shed!: ShedLevelCard
  #lanes!: PriorityLaneSettings
  #envDefaults!: EnvDefaultsCard
  #imagePolicy!: ImagePolicyCard
  #rolesLink!: HTMLAnchorElement
  #profilesLink!: HTMLAnchorElement
  #requiredSet!: RequiredSetOrgCard
  #reporting!: OrgReportingCard
  #oidc!: OidcSettingsCard

  #orgName(): string {
    return this.managed.org()?.name ?? ''
  }

  protected buildBody(): HTMLElement {
    this.#rolesLink = h('a', { class: 'btn btn-sm' }, 'Manage access')
    this.#profilesLink = h('a', { class: 'btn btn-sm' }, 'Execution profiles')
    this.#requiredSet = requiredSetOrgCard(this.#orgName())
    this.#reporting = orgReportingCard(this.#orgName())
    this.#oidc = oidcSettingsCard(this.#orgName())
    this.#form = ciTrustSettingsForm(this.#orgName())
    this.#refClasses = refClassSettings(this.#orgName())
    this.#shed = shedLevelCard(this.managed.orgId())
    this.#lanes = priorityLaneSettings(this.#orgName())
    this.#envDefaults = envDefaultsCard({ organization: this.#orgName() }, 'Organization environment defaults')
    this.#imagePolicy = imagePolicyCard(this.#orgName())
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
        h('div', { class: 'row', style: { alignItems: 'center' } }, this.orgPicker(), this.#rolesLink, this.#profilesLink),
      ),
      this.#form,
      this.#requiredSet,
      this.#reporting,
      this.#refClasses,
      this.#lanes,
      this.#shed,
      this.#oidc,
      this.#envDefaults,
      this.#imagePolicy,
    )
  }

  protected renderBody(): void {
    setAttr(this.#rolesLink, 'href', href(orgHref('/org/roles', this.managed.orgId())))
    setAttr(this.#profilesLink, 'href', href(orgHref('/org/profiles', this.managed.orgId())))
    this.#requiredSet.setScope(this.#orgName())
    this.#reporting.setScope(this.#orgName())
    this.#oidc.setScope(this.#orgName())
    this.#form.setScope(this.#orgName())
    this.#refClasses.setScope(this.#orgName())
    this.#shed.setScope(this.managed.orgId())
    this.#lanes.setScope(this.#orgName())
    this.#envDefaults.setScope({ organization: this.#orgName() })
    this.#imagePolicy.setScope(this.#orgName())
  }
}

export const tag = 'rc-org-trust-page'
define(tag, OrgTrustPage)
