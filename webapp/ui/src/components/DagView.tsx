import { For, Show, createMemo, type JSX } from 'solid-js'
import { useNavigate } from '@solidjs/router'
import { layoutDag, NODE_WIDTH, NODE_HEIGHT, type LaidOutNode } from '~/lib/dag.ts'
import { presentationFor } from '~/components/StatusBadge.tsx'
import type { WorkflowNodeSummary } from '~/api/csilapi/types.gen.ts'
import './dag.css'

/**
 * The workflow graph.
 *
 * Rendered as inline SVG rather than a canvas so every node is a real, focusable
 * link: a graph that cannot be tabbed through or read by a screen reader is a
 * picture of a workflow rather than a view of one. The list on the other tab
 * remains the complete alternative.
 *
 * Node colour comes from the same live store the rest of the page uses, so the
 * graph animates as the workflow runs.
 */

const PADDING = 16

export function DagView(props: { nodes: WorkflowNodeSummary[] }): JSX.Element {
  const layout = createMemo(() =>
    layoutDag(
      props.nodes.map((node) => ({
        name: node.name,
        dependsOn: node.dependsOn ?? [],
        node,
      })),
    ),
  )

  const positionByName = createMemo(() => {
    const map = new Map<string, LaidOutNode<{ name: string; dependsOn: string[]; node: WorkflowNodeSummary }>>()
    for (const laid of layout().nodes) map.set(laid.name, laid)
    return map
  })

  return (
    <Show
      when={props.nodes.length > 0}
      fallback={<div class="empty">This workflow has no nodes yet.</div>}
    >
      <Show when={layout().hasCycle}>
        <div class="alert alert-error" role="alert">
          These nodes contain a dependency cycle. The graph below breaks the cycle to draw
          something, so the layout is approximate.
        </div>
      </Show>

      {/* Wide graphs scroll inside this container; the page body never does. */}
      <div class="dag-scroll scroll-x">
        <svg
          class="dag"
          width={layout().width + PADDING * 2}
          height={layout().height + PADDING * 2}
          viewBox={`0 0 ${layout().width + PADDING * 2} ${layout().height + PADDING * 2}`}
          role="img"
          aria-label={`Workflow graph with ${props.nodes.length} nodes`}
        >
          <defs>
            <marker
              id="dag-arrow"
              viewBox="0 0 10 10"
              refX="9"
              refY="5"
              markerWidth="6"
              markerHeight="6"
              orient="auto-start-reverse"
            >
              <path d="M 0 0 L 10 5 L 0 10 z" fill="currentColor" />
            </marker>
          </defs>

          <g transform={`translate(${PADDING}, ${PADDING})`}>
            <g class="dag-edges">
              <For each={layout().edges}>
                {(edge) => {
                  const from = positionByName().get(edge.from)
                  const to = positionByName().get(edge.to)
                  if (!from || !to) return null
                  const x1 = from.x + NODE_WIDTH
                  const y1 = from.y + NODE_HEIGHT / 2
                  const x2 = to.x
                  const y2 = to.y + NODE_HEIGHT / 2
                  const midpoint = (x1 + x2) / 2
                  return (
                    <path
                      class={`dag-edge ${edge.isBackEdge ? 'dag-edge-back' : ''}`}
                      d={`M ${x1} ${y1} C ${midpoint} ${y1}, ${midpoint} ${y2}, ${x2} ${y2}`}
                      marker-end="url(#dag-arrow)"
                    />
                  )
                }}
              </For>
            </g>

            <For each={layout().nodes}>
              {(laid) => <DagNode laid={laid} />}
            </For>
          </g>
        </svg>
      </div>
    </Show>
  )
}

function DagNode(props: {
  laid: LaidOutNode<{ name: string; dependsOn: string[]; node: WorkflowNodeSummary }>
}): JSX.Element {
  const navigate = useNavigate()
  const node = () => props.laid.node.node
  const tone = () => presentationFor(node().status).tone
  const glyph = () => presentationFor(node().status).glyph
  const jobId = () => node().jobId

  // Navigation is wired onto the SVG group rather than a router <A>.
  //
  // <A> renders an HTML <a>, and an HTML-namespace element inside an <svg>
  // subtree is not rendered by any browser -- every node that had a job would
  // have drawn as nothing at all. role/tabindex/keydown keep the node reachable
  // and operable from the keyboard, which is what the link was there for.
  const open = (): void => {
    const id = jobId()
    if (id) navigate(`/jobs/${id}`)
  }

  return (
    <g
      class={`dag-node dag-node-${tone()} ${jobId() ? 'dag-node-link' : ''}`}
      transform={`translate(${props.laid.x}, ${props.laid.y})`}
      role={jobId() ? 'link' : undefined}
      tabindex={jobId() ? 0 : undefined}
      aria-label={
        jobId()
          ? `${node().displayName || node().name}, ${node().status}. Open this job.`
          : undefined
      }
      onClick={() => open()}
      onKeyDown={(event) => {
        if (event.key === 'Enter' || event.key === ' ') {
          event.preventDefault()
          open()
        }
      }}
    >
      <title>
        {node().displayName || node().name} — {node().status}
        {node().decisionReason ? ` (${node().decisionReason})` : ''}
      </title>
      <rect class="dag-node-box" width={NODE_WIDTH} height={NODE_HEIGHT} rx="8" />
      <text class="dag-node-glyph" x="14" y={NODE_HEIGHT / 2 + 5} aria-hidden="true">
        {glyph()}
      </text>
      <text class="dag-node-name" x="32" y={NODE_HEIGHT / 2 - 3}>
        {truncate(node().displayName || node().name, 18)}
      </text>
      <text class="dag-node-status" x="32" y={NODE_HEIGHT / 2 + 13}>
        {node().status}
      </text>
    </g>
  )
}

function truncate(text: string, max: number): string {
  return text.length <= max ? text : `${text.slice(0, max - 1)}…`
}
