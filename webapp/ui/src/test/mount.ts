/**
 * Test helpers for elements.
 *
 * `mount` connects an element to the document, which runs build(),
 * connected() and the first render(). `unmount` removes it, which runs every
 * removal the element owns -- the same path a route change takes.
 * `settle` waits for queued renders: BaseComponent renders after a microtask.
 */
import { flushRenders } from '@catalystcommunity/base-components'
import { RcElement, define } from '~/ui/element.ts'

export function mount<T extends Element>(el: T): T {
  document.body.append(el)
  return el
}

export function unmount(el: Element): void {
  el.remove()
}

export async function settle(): Promise<void> {
  for (let i = 0; i < 3; i++) {
    await Promise.resolve()
    flushRenders()
  }
  await new Promise((resolve) => setTimeout(resolve, 0))
  flushRenders()
}

/**
 * A bare element whose connected() and render() a test supplies, for testing
 * a helper in the same lifecycle a page gives it.
 */
export class TestHost extends RcElement {
  onConnect: (host: TestHost) => void = () => {}
  onRender: (host: TestHost) => void = () => {}
  renders = 0
  protected connected(): void {
    this.onConnect(this)
  }
  render(): void {
    this.renders += 1
    this.onRender(this)
  }
}
define('rc-test-host', TestHost)

export function testHost(onConnect: (host: TestHost) => void, onRender: (host: TestHost) => void = () => {}): TestHost {
  const el = document.createElement('rc-test-host') as TestHost
  el.onConnect = onConnect
  el.onRender = onRender
  return el
}
