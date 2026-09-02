import { For, Show, createMemo, type Accessor, type JSX } from 'solid-js'
import type { ResourceState } from '~/store/index.ts'

/**
 * Loading, empty and error presentation, in one place.
 *
 * The rule these encode: a refresh that fails must never blank the page. An
 * operator watching a running job wants the last known state with a warning
 * beside it, not an error screen where their data was.
 */

export function Skeleton(props: { rows?: number }): JSX.Element {
  return (
    <div class="stack" aria-hidden="true">
      <For each={Array.from({ length: props.rows ?? 5 })}>
        {(_, index) => (
          <div class="skeleton" style={{ width: `${100 - (index() % 3) * 12}%`, height: '1.6rem' }} />
        )}
      </For>
    </div>
  )
}

export function EmptyState(props: { title: string; children?: JSX.Element }): JSX.Element {
  return (
    <div class="empty">
      <div class="empty-title">{props.title}</div>
      <Show when={props.children}>
        <div>{props.children}</div>
      </Show>
    </div>
  )
}

export function ErrorNotice(props: { error: Error; stale?: boolean }): JSX.Element {
  return (
    <div class="alert alert-error" role="alert">
      <Show when={props.stale} fallback={<span>{props.error.message}</span>}>
        {/* Data is on screen but no longer fresh: say exactly that, rather
            than implying the visible data is wrong. */}
        <span>Showing the last known state. {props.error.message}</span>
      </Show>
    </div>
  )
}

/**
 * Renders one of loading / error / empty / content for a store resource.
 *
 * `children` receives an ACCESSOR, not a value, and that is the whole point.
 *
 * The previous version took `(data: T)` and called `props.children(data)` inside
 * the JSX. Reading `props.state` there made the call site itself reactive, so
 * every refresh destroyed the entire subtree and built a new one. On a live
 * page that fires constantly: a `project_update` while somebody was halfway
 * through the settings form threw their edits away, and every `job_update`
 * reset the log viewer's buffer and scroll position.
 *
 * With an accessor, `children` is invoked ONCE — when the resource first has
 * data — and everything inside reads through the accessor, so a refresh updates
 * only the text nodes and list rows that actually changed. Component state,
 * form input, scroll position and open dropdowns all survive.
 *
 * `<For each={data().jobs}>` still updates correctly: For tracks the accessor
 * itself and reconciles by key.
 */
export function ResourceView<T>(props: {
  state: ResourceState<T>
  empty?: JSX.Element
  isEmpty?: (data: T) => boolean
  children: (data: Accessor<T>) => JSX.Element
}): JSX.Element {
  // Whether there is content worth rendering. A boolean rather than the data
  // itself, so the Show below flips only on a genuine change of state and not
  // on every new value.
  // The DATA when there is content, undefined otherwise.
  //
  // Not a boolean, deliberately. Solid's Show only offers its function-child
  // form (the one that hands back an accessor) when `when` is a value rather
  // than a boolean, and the function-child form is what keeps the subtree from
  // being rebuilt. Show compares non-keyed conditions by TRUTHINESS, so the
  // data's identity changing on every refresh does not re-trigger it.
  const content = createMemo(() => {
    const { data, loaded } = props.state
    if (!loaded || data === undefined) return undefined
    return props.isEmpty?.(data) ? undefined : data
  })

  const showEmpty = createMemo(() => props.state.loaded && content() === undefined)
  const showSkeleton = createMemo(() => !props.state.loaded && !props.state.error)

  return (
    <>
      <Show when={props.state.error}>
        <ErrorNotice error={props.state.error!} stale={props.state.loaded} />
      </Show>

      <Show when={showSkeleton()}>
        <Skeleton />
      </Show>

      {/*
        The FUNCTION child is what makes this work, and it is not a style
        choice. Solid invokes a Show's function child inside untrack() when the
        condition turns truthy, and compares non-keyed conditions by truthiness,
        so the subtree is built exactly once -- when data first arrives -- and a
        refresh does not touch it. Passing the JSX directly instead rebuilds it
        on every change, which is the bug this replaced.

        It also cannot be built eagerly outside the Show: data is undefined
        until the first load resolves, and every caller dereferences it.
      */}
      <Show when={content()}>{(data) => props.children(data as Accessor<T>)}</Show>

      <Show when={showEmpty()}>{props.empty ?? <EmptyState title="Nothing here yet." />}</Show>
    </>
  )
}

/** A relative time that stays readable in a dense table. */
export function relativeTime(iso: string | undefined): string {
  if (!iso) return '—'
  const then = Date.parse(iso)
  if (Number.isNaN(then)) return '—'
  const seconds = Math.round((Date.now() - then) / 1000)
  if (seconds < 45) return 'just now'
  if (seconds < 90) return 'a minute ago'
  const minutes = Math.round(seconds / 60)
  if (minutes < 60) return `${minutes} minutes ago`
  const hours = Math.round(minutes / 60)
  if (hours < 24) return `${hours} hour${hours === 1 ? '' : 's'} ago`
  const days = Math.round(hours / 24)
  if (days < 30) return `${days} day${days === 1 ? '' : 's'} ago`
  return new Date(then).toLocaleDateString()
}

/** A duration between two timestamps, or from a start to now. */
export function duration(startedAt?: string, completedAt?: string): string {
  if (!startedAt) return '—'
  const start = Date.parse(startedAt)
  if (Number.isNaN(start)) return '—'
  const end = completedAt ? Date.parse(completedAt) : Date.now()
  const seconds = Math.max(0, Math.round((end - start) / 1000))
  if (seconds < 60) return `${seconds}s`
  const minutes = Math.floor(seconds / 60)
  const remainder = seconds % 60
  if (minutes < 60) return `${minutes}m ${remainder}s`
  const hours = Math.floor(minutes / 60)
  return `${hours}h ${minutes % 60}m`
}
