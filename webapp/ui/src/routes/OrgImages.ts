import { imageAliasesPanel, type ImageAliasesPanel } from '~/components/ImageAliases.ts'
import { ManagedOrgPage, orgHref } from '~/components/ManagedOrg.ts'
import { h, setAttr } from '~/ui/dom.ts'
import { define } from '~/ui/element.ts'
import { href } from '~/ui/router.ts'

/**
 * The image aliases of an organization (docs/image-aliases.md). An
 * organization admin names pinned images here; jobs name the alias, and the
 * coordinator gives the serving workers the images before they get a job.
 * Project aliases are on the Images tab of each project.
 */
export class OrgImagesPage extends ManagedOrgPage {
  protected readonly what = 'image aliases'

  #panel: ImageAliasesPanel | null = null
  #profilesLink!: HTMLAnchorElement

  protected buildBody(): HTMLElement {
    this.#panel = imageAliasesPanel('')
    this.#profilesLink = h('a', { class: 'btn btn-sm' }, 'Execution profiles')
    return h(
      'div',
      { class: 'page' },
      h(
        'div',
        { class: 'page-header' },
        h(
          'div',
          null,
          h('h1', null, 'Images'),
          h(
            'p',
            { class: 'meta' },
            'Image aliases of the organization. A new pin goes to the serving workers first; new jobs use it when each online serving worker has the image.',
          ),
        ),
        h('div', { class: 'row', style: { alignItems: 'center' } }, this.orgPicker(), this.#profilesLink),
      ),
      this.#panel,
    )
  }

  protected renderBody(): void {
    this.#panel?.setScope(this.managed.org()?.name ?? '')
    setAttr(this.#profilesLink, 'href', href(orgHref('/org/profiles', this.managed.orgId())))
  }
}

export const tag = 'rc-org-images-page'
define(tag, OrgImagesPage)
