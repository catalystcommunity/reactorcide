/**
 * A keyed, page-local async load: the replacement for Solid's createResource.
 *
 * Use it for data one page owns (an org's groups, a project's secrets list)
 * that does not belong in the shared store. Use store/resources.ts for shared,
 * live, reference-counted data.
 *
 * - `sync(key)` from render(): a NEW key starts a load and clears the data
 *   (it is a different entity); the same key does nothing.
 * - `reload()` after a mutation: keeps the data on screen while it loads.
 * - Only the newest request may write. A response that lands after a newer
 *   request, or after the host left the page, is dropped.
 * - A failed reload keeps the previous data and sets `error`, so a page never
 *   blanks over a transient failure.
 *
 * The host is rendered again after each change of `state`.
 */
import type { ResourceState } from '~/store/index.ts'

export interface LoadHost {
  invalidate(): void
  isActive(): boolean
}

export class Loader<K, T> {
  state: ResourceState<T> = { data: undefined, error: undefined, loading: false, loaded: false }
  readonly #host: LoadHost
  readonly #fetcher: (key: K) => Promise<T>
  #key: K | undefined
  #signature: string | undefined
  #request = 0

  constructor(host: LoadHost, fetcher: (key: K) => Promise<T>) {
    this.#host = host
    this.#fetcher = fetcher
  }

  /** Loads for `key` when it differs from the last key. `undefined` means "nothing to load yet". */
  sync(key: K | undefined): void {
    const signature = key === undefined ? undefined : JSON.stringify(key)
    if (signature === this.#signature) return
    this.#signature = signature
    this.#key = key
    this.#request += 1
    this.state = { data: undefined, error: undefined, loading: key !== undefined, loaded: false }
    if (key !== undefined) void this.#run(key)
    this.#host.invalidate()
  }

  /** Loads the current key again. */
  reload(): Promise<void> {
    if (this.#key === undefined) return Promise.resolve()
    this.state = { ...this.state, loading: true }
    this.#host.invalidate()
    return this.#run(this.#key)
  }

  /** Replaces the data without a request (an optimistic or returned value). */
  mutate(data: T): void {
    this.state = { data, error: undefined, loading: false, loaded: true }
    this.#host.invalidate()
  }

  /** Forgets the key, so the next sync loads again (call on disconnect if the page is reused). */
  reset(): void {
    this.#signature = undefined
    this.#key = undefined
    this.#request += 1
  }

  async #run(key: K): Promise<void> {
    const request = ++this.#request
    try {
      const data = await this.#fetcher(key)
      if (request !== this.#request) return
      this.state = { data, error: undefined, loading: false, loaded: true }
    } catch (cause) {
      if (request !== this.#request) return
      this.state = {
        ...this.state,
        error: cause instanceof Error ? cause : new Error(String(cause)),
        loading: false,
      }
    }
    if (this.#host.isActive()) this.#host.invalidate()
  }
}
