import { describe, expect, it, beforeEach, afterEach, vi } from 'vitest'
import { useResource, storeStats, resetStore, sweepNow, appendBounded, BUFFER_CAPS, type ResourceHandle } from './index.ts'
import { eventSocket } from './events.ts'
import { mount, settle, testHost, unmount, type TestHost } from '~/test/mount.ts'

/**
 * These are the tests behind the claim that the UI can be left open for days.
 *
 * The failure they guard against is silent and slow: an element that acquires
 * a dataset and a live topic, then leaves the page without releasing either.
 *
 * Each test connects a real element and removes it, so release goes through
 * the same BaseComponent own()/disconnect path a route change takes. Nothing
 * breaks immediately; the tab just gets heavier every time somebody navigates,
 * and by the time anyone notices, the cause is forty routes back.
 */

beforeEach(() => {
  resetStore()
})

afterEach(() => {
  resetStore()
})

/** Connects an element whose connected() runs `body`, and returns its remover. */
function mountScope(body: (host: TestHost) => void): () => void {
  const host = mount(testHost(body))
  return () => unmount(host)
}

describe('reference counting', () => {
  it('releases an entry when its only holder unmounts', async () => {
    const dispose = mountScope((host) => {
      useResource(host, { key: 'jobs', fetcher: async () => ['a'] })
    })
    expect(storeStats().holders).toBe(1)

    dispose()
    expect(storeStats().holders).toBe(0)

    // Still cached during the grace period, so back-navigation is instant.
    expect(storeStats().entries).toBe(1)

    // Then evicted.
    sweepNow(0)
    expect(storeStats().entries).toBe(0)
  })

  it('shares one entry between concurrent holders and keeps it while any remain', () => {
    const fetcher = vi.fn(async () => ['a'])

    const disposeFirst = mountScope((host) => {
      useResource(host, { key: 'jobs', fetcher })
    })
    const disposeSecond = mountScope((host) => {
      useResource(host, { key: 'jobs', fetcher })
    })

    expect(storeStats().entries).toBe(1)
    expect(storeStats().holders).toBe(2)

    disposeFirst()
    sweepNow(0)
    // One holder remains, so nothing is evicted.
    expect(storeStats().entries).toBe(1)

    disposeSecond()
    sweepNow(0)
    expect(storeStats().entries).toBe(0)
  })
})

describe('leak guard', () => {
  /**
   * The headline test. Mounting and unmounting the same routes many times must
   * return the store to exactly where it started -- no entries, no holders, no
   * topic subscriptions, no listeners.
   */
  it('returns to baseline after many mount/unmount cycles', () => {
    const baseline = storeStats()

    for (let i = 0; i < 50; i++) {
      const dispose = mountScope((host) => {
        useResource(host, {
          key: 'jobs',
          fetcher: async () => [],
          topics: ['jobs'],
          shouldRefresh: () => false,
        })
        useResource(host, {
          key: `workflow:${i}`,
          fetcher: async () => ({}),
          topics: [`workflow:${i}`],
          shouldRefresh: () => false,
        })
      })
      dispose()
      sweepNow(0)
    }

    const after = storeStats()
    expect(after.entries).toBe(baseline.entries)
    expect(after.holders).toBe(baseline.holders)
    expect(after.topics).toBe(baseline.topics)
    expect(after.listeners).toBe(baseline.listeners)
    expect(after.subscriptions).toBe(baseline.subscriptions)
  })

  it('does not accumulate topic subscriptions across visits to different workflows', () => {
    // Visiting forty workflow pages in a session must not leave forty
    // subscriptions live.
    for (let i = 0; i < 40; i++) {
      const dispose = mountScope((host) => {
        useResource(host, {
          key: `workflow:${i}`,
          fetcher: async () => ({}),
          topics: [`workflow:${i}`],
        })
      })
      dispose()
      sweepNow(0)
      expect(eventSocket.stats().topics).toBe(0)
    }
  })
})

describe('topic reference counting', () => {
  it('subscribes once for many holders and unsubscribes only on the last release', () => {
    const releaseFirst = eventSocket.acquireTopic('jobs')
    expect(eventSocket.stats().topics).toBe(1)

    const releaseSecond = eventSocket.acquireTopic('jobs')
    expect(eventSocket.stats().topics).toBe(1)

    releaseFirst()
    expect(eventSocket.stats().topics).toBe(1)

    releaseSecond()
    expect(eventSocket.stats().topics).toBe(0)
  })

  it('ignores a double release so one component cannot drop another\'s subscription', () => {
    const releaseFirst = eventSocket.acquireTopic('jobs')
    const releaseSecond = eventSocket.acquireTopic('jobs')

    releaseFirst()
    releaseFirst() // buggy caller releasing twice
    expect(eventSocket.stats().topics).toBe(1)

    releaseSecond()
    expect(eventSocket.stats().topics).toBe(0)
  })
})

describe('bounded buffers', () => {
  it('caps growth so a long-lived page cannot grow without limit', () => {
    let lines: string[] = []
    // Far more than the cap, as a chatty job over many hours would produce.
    for (let batch = 0; batch < 100; batch++) {
      const incoming = Array.from({ length: 200 }, (_, i) => `line ${batch}-${i}`)
      lines = appendBounded(lines, incoming, BUFFER_CAPS.logLines)
    }
    expect(lines.length).toBe(BUFFER_CAPS.logLines)
  })

  it('keeps the newest entries and drops the oldest', () => {
    const kept = appendBounded(['old-1', 'old-2'], ['new-1', 'new-2'], 3)
    expect(kept).toEqual(['old-2', 'new-1', 'new-2'])
  })

  it('returns the original array when nothing is appended', () => {
    const existing = ['a']
    expect(appendBounded(existing, [], 10)).toBe(existing)
  })
})

describe('loading state', () => {
  it('keeps previous data visible when a refresh fails', async () => {
    let shouldFail = false

    // The resource is captured out of connected(), which runs synchronously
    // when the element is appended.
    let resource!: ResourceHandle<string[]>
    const dispose = mountScope((host) => {
      resource = useResource<string[]>(host, {
        key: 'jobs',
        fetcher: async () => {
          if (shouldFail) throw new Error('coordinator unreachable')
          return ['job-1']
        },
      })
    })

    // Let the initial load settle.
    await vi.waitFor(() => expect(resource.state().loaded).toBe(true))
    expect(resource.state().data).toEqual(['job-1'])

    shouldFail = true
    await resource.refresh()

    // A failed refresh must not blank the page: the operator keeps seeing the
    // last good data with an error beside it.
    expect(resource.state().data).toEqual(['job-1'])
    expect(resource.state().error?.message).toBe('coordinator unreachable')
    expect(resource.state().loaded).toBe(true)

    dispose()
  })

  it('reports the first load through loading and loaded', async () => {
    let resource!: ResourceHandle<string[]>
    const dispose = mountScope((host) => {
      resource = useResource<string[]>(host, { key: 'jobs', fetcher: async () => ['job-1'] })
    })

    expect(resource.state().loading).toBe(true)
    expect(resource.state().loaded).toBe(false)

    await vi.waitFor(() => expect(resource.state().loaded).toBe(true))
    expect(resource.state().loading).toBe(false)
    expect(resource.state().error).toBeUndefined()

    dispose()
  })
})

describe('reactive keys', () => {
  /**
   * The shell KEEPS a page when only a search param changes (a filter). A resource whose key was read once at setup therefore kept
   * serving the entity or the filter the page was first opened with: /jobs/a
   * stayed on screen after navigating to /jobs/b, and the status dropdown
   * updated the URL without ever refetching.
   */
  it('swaps entries when the key changes, releasing the old one', () => {
    let id = 'a'
    let resource!: ResourceHandle<string>

    const dispose = mountScope((host) => {
      resource = useResource(host, () => ({ key: `job:${id}`, fetcher: async () => id }))
    })

    expect(storeStats().entries).toBe(1)
    expect(storeStats().holders).toBe(1)

    id = 'b'
    resource.sync()

    // The new key is held, and the old one is unheld and evictable.
    expect(storeStats().holders).toBe(1)
    sweepNow(0)
    expect(storeStats().entries).toBe(1)

    dispose()
    sweepNow(0)
    expect(storeStats().entries).toBe(0)
    expect(storeStats().holders).toBe(0)
  })
})

describe('rendering', () => {
  it('renders the holding element again when its resource loads, and not after it leaves', async () => {
    let resolve!: (value: string[]) => void
    let resource!: ResourceHandle<string[]>
    const host = mount(
      testHost((h) => {
        resource = useResource<string[]>(h, {
          key: 'jobs',
          fetcher: () => new Promise((r) => (resolve = r)),
        })
      }),
    )
    await settle()
    const before = host.renders

    resolve(['job-1'])
    await settle()
    expect(resource.state().data).toEqual(['job-1'])
    expect(host.renders).toBeGreaterThan(before)

    unmount(host)
    const afterLeave = host.renders
    await resource.refresh()
    await settle()
    expect(host.renders).toBe(afterLeave)
    expect(storeStats().subscriptions).toBe(0)
  })
})
