/**
 * The base class of every Reactorcide element: a Zest BaseComponent in the
 * light DOM, so the application CSS applies.
 *
 * - `build()` makes the elements ONE time, at the first connect.
 * - `connected()` acquires what one connection holds: store resources
 *   (`useResource(this, ...)`), live listeners, `this.own(...)`,
 *   `this.listen(...)`. Everything acquired here is released on disconnect.
 * - `render()` changes only the parts that changed. It runs after each
 *   connect, after each change of a watched collection, and after each
 *   `invalidate()`. It must be cheap and idempotent: it runs often.
 *
 * Do not name a member `render` with a different signature, `title`,
 * `remove`, `hidden`, or any other HTMLElement member.
 */
import { BaseComponent } from '@catalystcommunity/base-components'
import { dataStore } from './store.ts'

export class RcElement extends BaseComponent {
  /** Store collections whose changes render this element again. */
  static watches: readonly string[] = []
  #built = false

  connectedCallback(): void {
    super.connectedCallback()
    if (!this.#built) {
      this.#built = true
      // A custom element is inline by default. base.css makes every
      // Reactorcide element a block through this attribute, so a card or a
      // page element that is a custom element lays out as one.
      this.setAttribute('data-rc', '')
      this.build()
    }
    for (const collection of (this.constructor as typeof RcElement).watches) {
      this.own(dataStore.subscribe(() => this.invalidate(), { collection, isActive: () => this.isActive() }))
    }
    this.connected()
    this.render()
  }

  /** Make the elements. Runs one time. */
  protected build(): void {}

  /** Acquire resources and add subscriptions. Runs at each connect. */
  protected connected(): void {}

  render(): void {}
}

/** Define a tag one time. */
export function define(tag: string, cls: CustomElementConstructor): void {
  if (!customElements.get(tag)) customElements.define(tag, cls)
}
