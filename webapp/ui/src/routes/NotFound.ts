import { RcPage } from '~/app/page.ts'
import { h } from '~/ui/dom.ts'
import { define } from '~/ui/element.ts'
import { href } from '~/ui/router.ts'

export class NotFoundPage extends RcPage {
  protected build(): void {
    this.append(
      h(
        'div',
        { class: 'page' },
        h('div', { class: 'empty' }, h('div', { class: 'empty-title' }, 'That page does not exist.'), h('p', null, h('a', { href: href('/') }, 'Go to workflows'))),
      ),
    )
  }

  /** Any other unknown path is still this page. */
  acceptRoute(next: { name: string }): boolean {
    return next.name === 'notFound'
  }
}

export const tag = 'rc-not-found-page'
define(tag, NotFoundPage)
