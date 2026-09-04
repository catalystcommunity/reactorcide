/**
 * The live event socket.
 *
 * One WebSocket serves the whole application. Pages declare the TOPICS they
 * care about, and a topic is reference-counted: the socket subscribes when the
 * first interested party appears and unsubscribes when the last one leaves.
 * That is what stops a session that has visited forty workflow pages from still
 * receiving forty workflows' events an hour later.
 *
 * Every frame arriving here has already been authorized by the coordinator
 * against the caller's own session. The topic set narrows what a page receives;
 * it never widens what it is allowed to receive.
 */

export type EventType =
  | 'job_update'
  | 'job_created'
  | 'log_available'
  | 'metrics_available'
  | 'workflow_created'
  | 'workflow_update'
  | 'workflow_node_update'
  | 'project_update'

export interface LiveEvent {
  type: EventType
  job_id?: string
  status?: string
  updated_at?: string
  stream?: string
  project_id?: string
  owner_user_id?: string
  workflow_id?: string
  node_id?: string
  node_name?: string
}

export type EventListener = (event: LiveEvent) => void

/**
 * How a socket gets made. Injectable so tests can supply a fake, and so an
 * environment with no usable WebSocket can opt out entirely by returning null.
 *
 * This is not only a test seam. jsdom's WebSocket fails ASYNCHRONOUSLY, so a
 * try/catch around the constructor does not contain it; the only reliable way
 * to not open a socket is to not construct one.
 */
export type SocketFactory = (url: string) => WebSocket | null

const defaultSocketFactory: SocketFactory = (url) => {
  if (typeof WebSocket === 'undefined') return null
  try {
    return new WebSocket(url)
  } catch {
    return null
  }
}

/** Reconnect backoff, in milliseconds. */
const INITIAL_BACKOFF = 1_000
const MAX_BACKOFF = 30_000

class EventSocket {
  private socket: WebSocket | null = null
  private backoff = INITIAL_BACKOFF
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null
  private closed = false

  /** topic -> how many holders want it. */
  private readonly topicRefs = new Map<string, number>()
  /** Listeners, keyed by an opaque handle so removal is O(1). */
  private readonly listeners = new Set<EventListener>()
  /** Callbacks fired when the connection state changes. */
  private readonly statusListeners = new Set<(connected: boolean) => void>()

  private connected = false
  private socketFactory: SocketFactory = defaultSocketFactory

  /** Replaces the socket factory. Returns the previous one, for restoration. */
  setSocketFactory(factory: SocketFactory): SocketFactory {
    const previous = this.socketFactory
    this.socketFactory = factory
    return previous
  }

  /**
   * Acquires a topic. Returns a release function.
   *
   * The release function is what every caller MUST wire into onCleanup. It is
   * returned rather than exposing an `unsubscribe(topic)` so a caller cannot
   * release a topic it never acquired and drop somebody else's subscription.
   */
  acquireTopic(topic: string): () => void {
    const current = this.topicRefs.get(topic) ?? 0
    this.topicRefs.set(topic, current + 1)
    if (current === 0) {
      this.ensureConnected()
      this.send({ subscribe: [topic] })
    }

    let released = false
    return () => {
      // Guard against a double release, which would otherwise decrement
      // somebody else's count to zero and silently kill their updates.
      if (released) return
      released = true

      const count = this.topicRefs.get(topic) ?? 0
      if (count <= 1) {
        this.topicRefs.delete(topic)
        this.send({ unsubscribe: [topic] })
      } else {
        this.topicRefs.set(topic, count - 1)
      }
    }
  }

  /** Registers a listener. Returns a remove function for onCleanup. */
  addListener(listener: EventListener): () => void {
    this.listeners.add(listener)
    return () => {
      this.listeners.delete(listener)
    }
  }

  onStatusChange(listener: (connected: boolean) => void): () => void {
    this.statusListeners.add(listener)
    listener(this.connected)
    return () => {
      this.statusListeners.delete(listener)
    }
  }

  isConnected(): boolean {
    return this.connected
  }

  private ensureConnected(): void {
    if (this.socket || this.closed) return

    const scheme = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const socket = this.socketFactory(`${scheme}//${window.location.host}/app/ws`)
    if (!socket) {
      // No usable socket: a proxy blocking the upgrade, a strict CSP, a test
      // DOM. Live updates are an enhancement rather than a requirement -- the
      // pages that use them also poll -- so this degrades to "no live updates"
      // instead of taking the page down.
      this.setConnected(false)
      return
    }
    this.socket = socket

    socket.onopen = () => {
      this.backoff = INITIAL_BACKOFF
      this.setConnected(true)
      // Re-declare every live topic. A reconnect starts with a server-side
      // subscription set of nothing, so without this the page would go quiet
      // after a network blip and only a reload would fix it.
      const topics = [...this.topicRefs.keys()]
      if (topics.length > 0) this.send({ subscribe: topics })
    }

    socket.onmessage = (message) => {
      let event: LiveEvent
      try {
        event = JSON.parse(message.data as string) as LiveEvent
      } catch {
        return
      }
      for (const listener of this.listeners) listener(event)
    }

    socket.onclose = () => {
      this.socket = null
      this.setConnected(false)
      this.scheduleReconnect()
    }

    socket.onerror = () => socket.close()
  }

  private scheduleReconnect(): void {
    if (this.closed || this.reconnectTimer) return
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null
      // Only reconnect if somebody still wants events. A session left on a
      // page with no subscriptions should not hold a socket open, nor keep
      // retrying one, for as long as the tab is open.
      if (this.topicRefs.size > 0) this.ensureConnected()
    }, this.backoff)
    this.backoff = Math.min(this.backoff * 2, MAX_BACKOFF)
  }

  private setConnected(value: boolean): void {
    if (this.connected === value) return
    this.connected = value
    for (const listener of this.statusListeners) listener(value)
  }

  private send(message: { subscribe?: string[]; unsubscribe?: string[] }): void {
    if (this.socket && this.socket.readyState === 1 /* OPEN */) {
      this.socket.send(JSON.stringify(message))
    }
    // A message sent while disconnected is intentionally dropped: onopen
    // re-declares the whole topic set, so queueing would only risk sending a
    // subscription for a topic that has since been released.
  }

  /** Test and teardown hook. */
  close(): void {
    this.closed = true
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer)
    this.reconnectTimer = null
    this.socket?.close()
    this.socket = null
    this.topicRefs.clear()
    this.listeners.clear()
    this.statusListeners.clear()
    this.connected = false
  }

  /** Diagnostics for the leak test. See store/index.ts's storeStats. */
  stats(): { topics: number; listeners: number; connected: boolean } {
    return {
      topics: this.topicRefs.size,
      listeners: this.listeners.size,
      connected: this.connected,
    }
  }
}

export const eventSocket = new EventSocket()

/** Topic name helpers, so a typo cannot silently subscribe to nothing. */
export const topics = {
  jobs: 'jobs',
  workflows: 'workflows',
  /** Every project event the caller may see. The project LIST needs this. */
  projects: 'projects',
  job: (id: string) => `job:${id}`,
  /** Covers the workflow, its nodes, and its jobs. */
  workflow: (id: string) => `workflow:${id}`,
  project: (id: string) => `project:${id}`,
} as const
