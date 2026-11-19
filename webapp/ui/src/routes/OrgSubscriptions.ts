import { ManagedOrgPage } from '~/components/ManagedOrg.ts'
import { subscriptionsManager, type SubscriptionsManager } from '~/components/Subscriptions.ts'
import { h } from '~/ui/dom.ts'
import { define } from '~/ui/element.ts'

export { SubscriptionPanel } from '~/components/Subscriptions.ts'

/**
 * Outbound event subscriptions of the organization (docs/outbound-events.md).
 * The list, the form and the delivery log are in components/Subscriptions.ts;
 * a project owner has the same view for one project on the project page.
 */
export class OrgSubscriptionsPage extends ManagedOrgPage {
  protected readonly what = 'event subscriptions'

  #manager!: SubscriptionsManager

  protected buildBody(): HTMLElement {
    this.#manager = subscriptionsManager()
    return h(
      'div',
      { class: 'page' },
      h(
        'div',
        { class: 'page-header' },
        h(
          'div',
          null,
          h('h1', null, 'Event subscriptions'),
          h('p', { class: 'meta' }, 'Send phase events of runs to a webhook. Delivery is at least once, in order for each workflow. A sink cannot delay a run. A project owner manages the subscriptions of one project on its Subscriptions tab.'),
        ),
        h('div', { class: 'row', style: { alignItems: 'center' } }, this.orgPicker()),
      ),
      this.#manager,
    )
  }

  protected renderBody(): void {
    this.#manager.setScope({ orgId: this.managed.orgId() })
  }
}

export const tag = 'rc-org-subscriptions-page'
define(tag, OrgSubscriptionsPage)
