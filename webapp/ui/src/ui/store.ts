/**
 * The one application store: a Zest BasicDataService.
 *
 * Collections in use:
 *  - `resources` -- the state of each store/index.ts entry, keyed by entry key.
 *  - `session`   -- `session` and `config` (lib/session.ts).
 *  - `ui`        -- `theme` (lib/theme.ts), `connected` (the live socket).
 *  - `route`     -- `current` (ui/router.ts).
 *
 * An element lists the collections it renders in `static watches`, or
 * subscribes to single keys itself. Change a value by setting a NEW object:
 * the store compares with Object.is and sends nothing for the same value.
 */
import BasicDataService from '@catalystcommunity/basic-data-service'

export const dataStore = new BasicDataService()

/** A single typed value in the store. */
export interface Cell<T> {
  get(): T
  set(value: T): void
}

export function cell<T>(collection: string, key: string, initial: T): Cell<T> {
  if (!dataStore.has(collection, key)) dataStore.set(collection, key, initial)
  return {
    get: () => (dataStore.has(collection, key) ? (dataStore.get(collection, key) as T) : initial),
    set: (value: T) => dataStore.set(collection, key, value),
  }
}
