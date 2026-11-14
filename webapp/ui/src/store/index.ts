import { dataStore } from '~/ui/store.ts'
import { eventSocket, type LiveEvent } from './events.ts'

/** The store collection that holds each entry's ResourceState, keyed by entry key. */
export const RESOURCES = 'resources'

/**
 * The shared datastore.
 *
 * Many components subscribe to the same dataset, and nothing may leak when the
 * operator moves between pages or leaves a tab open for days. Four mechanisms
 * together make that true:
 *
 *  1. REFERENCE COUNTING. An entry is only reachable through `useResource`,
 *     which hands its own release to the element's `own()`. The element's
 *     disconnect releases it; a component cannot forget to release what it
 *     never explicitly acquired.
 *
 *  2. TIMED EVICTION. Dropping to zero holders starts a grace period rather
 *     than deleting immediately, so back-navigation is instant. After the
 *     grace period the entry, its data, and its live topics all go.
 *
 *  3. BOUNDED BUFFERS. Append-only data (logs, metric points) lives in ring
 *     buffers with a hard cap. This is what makes "open for days" safe; see
 *     `appendBounded`.
 *
 *  4. A LEAK TEST. `storeStats()` reports what is held, and store.test.ts
 *     connects and disconnects elements repeatedly and asserts the numbers
 *     return to baseline. A leak fails the build rather than waiting for somebody to
 *     notice a slow tab.
 */

/** How long an unheld entry survives, so back-navigation stays instant. */
const EVICTION_GRACE_MS = 30_000

/** How often the sweeper looks for expired entries. */
const SWEEP_INTERVAL_MS = 10_000

export interface ResourceState<T> {
  data: T | undefined
  error: Error | undefined
  loading: boolean
  /** Set once the first load has resolved, so a refresh does not flash a spinner. */
  loaded: boolean
}

interface Entry<T> {
  key: string
  holders: number
  releasedAt: number | null
  state: ResourceState<T>
  fetcher: () => Promise<T>
  /** Releases every live topic this entry acquired. */
  releaseTopics: (() => void)[]
  /** Removes this entry's event listener. */
  removeListener: (() => void) | null
  inFlight: Promise<void> | null
}

const entries = new Map<string, Entry<unknown>>()
let sweeper: ReturnType<typeof setInterval> | null = null

function startSweeper(): void {
  if (sweeper || typeof setInterval === 'undefined') return
  sweeper = setInterval(sweep, SWEEP_INTERVAL_MS)
  // Never hold the process open for this in a Node/test context.
  ;(sweeper as unknown as { unref?: () => void }).unref?.()
}

function sweep(): void {
  const now = Date.now()
  for (const [key, entry] of entries) {
    if (entry.holders > 0 || entry.releasedAt === null) continue
    if (now - entry.releasedAt < EVICTION_GRACE_MS) continue
    disposeEntry(key, entry)
  }
}

function disposeEntry(key: string, entry: Entry<unknown>): void {
  for (const release of entry.releaseTopics) release()
  entry.releaseTopics = []
  entry.removeListener?.()
  entry.removeListener = null
  entries.delete(key)
  dataStore.remove(RESOURCES, key)
}

export interface ResourceOptions<T> {
  /** Stable cache key. Two callers with the same key share one entry. */
  key: string
  /** Loads the data. Called on first acquire and on each refresh. */
  fetcher: () => Promise<T>
  /** Live topics this resource needs while held. */
  topics?: string[]
  /**
   * Decides whether an event should refresh this resource. Return false for
   * events that cannot affect it — a refresh per irrelevant frame is how a
   * busy instance turns a live UI into a load generator.
   */
  shouldRefresh?: (event: LiveEvent) => boolean
  /**
   * Applies an event directly, avoiding a refetch. Return the next value, or
   * undefined to fall through to `shouldRefresh`. Use this for a status change
   * that the event itself fully describes.
   */
  applyEvent?: (current: T | undefined, event: LiveEvent) => T | undefined
}

/** What an element must offer to hold a resource. RcElement has all three. */
export interface ResourceHost {
  own<R extends () => void>(remove: R): R
  invalidate(): void
  isActive(): boolean
}

const INITIAL: ResourceState<never> = { data: undefined, error: undefined, loading: true, loaded: false }

/** Live data-store subscriptions held by resource handles, for the leak test. */
let subscriptions = 0

/**
 * One element's hold on one shared resource.
 *
 * `sync()` re-reads the options builder and, when the KEY changed, releases
 * the old entry and acquires the new one. Call it from render(): a page whose
 * route param or filter changed must follow it, and a page kept across a query
 * change (a filter) is exactly the case where options read once would pin the
 * page to whatever it was first opened with. An unchanged key does nothing, so
 * calling it on every render is cheap.
 */
export class ResourceHandle<T> {
  #entry: Entry<T> | null = null
  #unsubscribe: (() => void) | null = null
  readonly #host: ResourceHost
  readonly #optionsOf: () => ResourceOptions<T>

  constructor(host: ResourceHost, optionsOf: () => ResourceOptions<T>) {
    this.#host = host
    this.#optionsOf = optionsOf
    this.sync()
    host.own(() => this.release())
  }

  sync(): void {
    const next = this.#optionsOf()
    if (this.#entry && this.#entry.key === next.key) return
    this.release()
    const entry = acquire(next)
    this.#entry = entry
    // A write of THIS key renders the host again. The store indexes keyed
    // subscriptions, so an event storm on other keys costs this host nothing.
    this.#unsubscribe = dataStore.subscribe(() => this.#host.invalidate(), {
      collection: RESOURCES,
      keys: [entry.key],
      isActive: () => this.#host.isActive(),
    })
    subscriptions += 1
  }

  state(): ResourceState<T> {
    return (this.#entry?.state ?? INITIAL) as ResourceState<T>
  }

  /** Reloads the dataset that is on screen, with the entry's own fetcher. */
  refresh(): Promise<void> {
    const entry = this.#entry
    return entry ? load(entry, entry.fetcher) : Promise.resolve()
  }

  /** Stops holding. Idempotent; the host's disconnect calls it. */
  release(): void {
    if (this.#unsubscribe) {
      this.#unsubscribe()
      this.#unsubscribe = null
      subscriptions -= 1
    }
    const held = this.#entry
    if (!held) return
    this.#entry = null
    held.holders -= 1
    if (held.holders <= 0) {
      held.holders = 0
      held.releasedAt = Date.now()
    }
  }
}

/**
 * Acquires a shared resource for as long as `host` stays connected.
 *
 * Call it from the host's `connected()`. Release is automatic: the handle
 * gives its release to `host.own()`, so an element that leaves the page stops
 * holding the entry and its live topics, however it left.
 */
export function useResource<T>(
  host: ResourceHost,
  options: ResourceOptions<T> | (() => ResourceOptions<T>),
): ResourceHandle<T> {
  return new ResourceHandle<T>(host, typeof options === 'function' ? options : () => options)
}

function write<T>(entry: Entry<T>, next: ResourceState<T>): void {
  entry.state = next
  dataStore.set(RESOURCES, entry.key, next)
}

function acquire<T>(options: ResourceOptions<T>): Entry<T> {
  const key = options.key
  const existing = entries.get(key) as Entry<T> | undefined
  if (existing) {
    existing.holders += 1
    existing.releasedAt = null
    // A resource reacquired after sitting idle may be stale. Refresh in the
    // background rather than blanking the view the operator just came back to.
    if (existing.holders === 1) void load(existing, options.fetcher)
    return existing
  }

  const entry: Entry<T> = {
    key,
    holders: 1,
    releasedAt: null,
    state: { ...INITIAL },
    fetcher: options.fetcher,
    releaseTopics: [],
    removeListener: null,
    inFlight: null,
  }
  entries.set(key, entry as Entry<unknown>)
  dataStore.set(RESOURCES, key, entry.state)

  const wanted = options.topics ?? []
  for (const topic of wanted) {
    entry.releaseTopics.push(eventSocket.acquireTopic(topic))
  }

  if (wanted.length) {
    entry.removeListener = eventSocket.addListener((event) => {
      if (options.applyEvent) {
        const next = options.applyEvent(entry.state.data, event)
        if (next !== undefined) {
          write(entry, { ...entry.state, data: next })
          return
        }
      }
      if (options.shouldRefresh?.(event)) {
        void load(entry, options.fetcher)
      }
    })
  }

  startSweeper()
  void load(entry, options.fetcher)
  return entry
}

/**
 * Loads or reloads an entry, collapsing concurrent calls.
 *
 * Without the in-flight guard a burst of events produces a burst of identical
 * requests, and their responses can land out of order and leave the newest one
 * overwritten by an older one.
 */
function load<T>(entry: Entry<T>, fetcher: () => Promise<T>): Promise<void> {
  if (entry.inFlight) return entry.inFlight

  write(entry, { ...entry.state, loading: true })

  const run = fetcher()
    .then((data) => {
      // The entry may have been evicted while the request was in flight.
      if (!entries.has(entry.key)) return
      write(entry, { data, error: undefined, loading: false, loaded: true })
    })
    .catch((error: unknown) => {
      if (!entries.has(entry.key)) return
      write(entry, {
        ...entry.state,
        error: error instanceof Error ? error : new Error(String(error)),
        loading: false,
        // Keep `loaded` as it was: a failed refresh should leave the previous
        // data on screen with an error beside it, not blank the page.
      })
    })
    .finally(() => {
      entry.inFlight = null
    })

  entry.inFlight = run
  return run
}

/**
 * Appends to a bounded buffer, returning a NEW array.
 *
 * This is the mechanism that makes a page safe to leave open for days. A log
 * viewer on a chatty job would otherwise grow without limit; here the oldest
 * entries fall off the front once the cap is reached.
 */
export function appendBounded<T>(existing: readonly T[], incoming: readonly T[], cap: number): T[] {
  if (incoming.length === 0) return existing as T[]
  const combined = existing.concat(incoming as T[])
  return combined.length <= cap ? combined : combined.slice(combined.length - cap)
}

/** Caps, in items. Generous for real use, finite for a tab left open overnight. */
export const BUFFER_CAPS = {
  /** Log lines held per stream. */
  logLines: 5_000,
  /** Points held per metric series. */
  metricPoints: 2_000,
} as const

/**
 * What the store is holding right now.
 *
 * Exported for the leak test, and useful from the browser console when
 * something feels slow. `window.__reactorcideStore` is wired in main.ts for
 * development builds only.
 */
export function storeStats(): {
  entries: number
  holders: number
  topics: number
  listeners: number
  subscriptions: number
} {
  let holders = 0
  for (const entry of entries.values()) holders += entry.holders
  const socket = eventSocket.stats()
  return {
    entries: entries.size,
    holders,
    topics: socket.topics,
    listeners: socket.listeners,
    subscriptions,
  }
}

/**
 * Drops everything immediately, ignoring the grace period.
 *
 * For tests, and for sign-out: a session ending must not leave the previous
 * user's data sitting in memory for the next thirty seconds.
 */
export function resetStore(): void {
  for (const [key, entry] of [...entries]) disposeEntry(key, entry)
  entries.clear()
  if (sweeper) {
    clearInterval(sweeper)
    sweeper = null
  }
}

/** Forces an eviction sweep. Tests use this instead of waiting. */
export function sweepNow(graceOverrideMs = EVICTION_GRACE_MS): void {
  const now = Date.now()
  for (const [key, entry] of [...entries]) {
    if (entry.holders > 0 || entry.releasedAt === null) continue
    if (now - entry.releasedAt < graceOverrideMs) continue
    disposeEntry(key, entry)
  }
}
