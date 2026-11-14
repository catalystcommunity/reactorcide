import { layoutDag, NODE_WIDTH, NODE_HEIGHT, type LaidOutNode } from '~/lib/dag.ts'
import { presentationFor } from '~/components/StatusBadge.ts'
import type { WorkflowNodeSummary } from '~/api/csilapi/types.gen.ts'
import { h, s, setAttr, setText, show, syncKeyed } from '~/ui/dom.ts'
import { navigate } from '~/ui/router.ts'
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
 * graph animates as the workflow runs. Nodes are kept by name across refreshes
 * (syncKeyed), so a node that has the keyboard focus keeps it while the
 * workflow advances.
 */

const PADDING = 16

type Laid = LaidOutNode<{ name: string; dependsOn: string[]; node: WorkflowNodeSummary }>

export class DagView {
  readonly el: HTMLDivElement
  readonly #empty: HTMLDivElement
  readonly #cycle: HTMLDivElement
  readonly #scroll: HTMLDivElement
  readonly #svg: SVGElement
  readonly #frame: SVGElement
  readonly #edges: SVGElement
  readonly #nodes: SVGElement

  constructor() {
    this.#empty = h('div', { class: 'empty' }, 'This workflow has no nodes yet.')
    this.#cycle = h(
      'div',
      { class: 'alert alert-error', role: 'alert', hidden: true },
      'These nodes contain a dependency cycle. The graph below breaks the cycle to draw something, so the layout is approximate.',
    )
    this.#edges = s('g', { class: 'dag-edges' })
    this.#nodes = s('g', { class: 'dag-nodes' })
    this.#frame = s('g', { transform: `translate(${PADDING}, ${PADDING})` }, this.#edges, this.#nodes)
    this.#svg = s(
      'svg',
      { class: 'dag', role: 'img' },
      s(
        'defs',
        null,
        s(
          'marker',
          { id: 'dag-arrow', viewBox: '0 0 10 10', refX: '9', refY: '5', markerWidth: '6', markerHeight: '6', orient: 'auto-start-reverse' },
          s('path', { d: 'M 0 0 L 10 5 L 0 10 z', fill: 'currentColor' }),
        ),
      ),
      this.#frame,
    )
    // Wide graphs scroll inside this container; the page body never does.
    this.#scroll = h('div', { class: 'dag-scroll scroll-x' }, this.#svg)
    this.el = h('div', { class: 'dag-view' }, this.#empty, this.#cycle, this.#scroll)
  }

  update(nodes: WorkflowNodeSummary[]): void {
    const hasNodes = nodes.length > 0
    show(this.#empty, !hasNodes)
    show(this.#scroll, hasNodes)
    if (!hasNodes) {
      show(this.#cycle, false)
      return
    }

    const layout = layoutDag(nodes.map((node) => ({ name: node.name, dependsOn: node.dependsOn ?? [], node })))
    show(this.#cycle, layout.hasCycle)

    const width = layout.width + PADDING * 2
    const height = layout.height + PADDING * 2
    setAttr(this.#svg, 'width', width)
    setAttr(this.#svg, 'height', height)
    setAttr(this.#svg, 'viewBox', `0 0 ${width} ${height}`)
    setAttr(this.#svg, 'aria-label', `Workflow graph with ${nodes.length} nodes`)

    const byName = new Map<string, Laid>()
    for (const laid of layout.nodes) byName.set(laid.name, laid)

    // Edges are not focusable, so redrawing them loses nothing.
    const paths: SVGElement[] = []
    for (const edge of layout.edges) {
      const from = byName.get(edge.from)
      const to = byName.get(edge.to)
      if (!from || !to) continue
      const x1 = from.x + NODE_WIDTH
      const y1 = from.y + NODE_HEIGHT / 2
      const x2 = to.x
      const y2 = to.y + NODE_HEIGHT / 2
      const midpoint = (x1 + x2) / 2
      paths.push(
        s('path', {
          class: `dag-edge ${edge.isBackEdge ? 'dag-edge-back' : ''}`,
          d: `M ${x1} ${y1} C ${midpoint} ${y1}, ${midpoint} ${y2}, ${x2} ${y2}`,
          'marker-end': 'url(#dag-arrow)',
        }),
      )
    }
    this.#edges.replaceChildren(...paths)

    syncKeyed<Laid, SVGElement>(this.#nodes, layout.nodes, (laid) => laid.name, createNode, updateNode)
  }
}

/**
 * Navigation is wired onto the SVG group rather than a link element.
 *
 * An HTML <a> inside an <svg> subtree is not rendered by any browser -- every
 * node that had a job would have drawn as nothing at all. role/tabindex/keydown
 * keep the node reachable and operable from the keyboard, which is what the
 * link was there for. The current job id is read off the element at the time
 * of the click, so a node that gains its job after a refresh opens it.
 */
function createNode(): SVGElement {
  const group = s(
    'g',
    null,
    s('title'),
    s('rect', { class: 'dag-node-box', width: NODE_WIDTH, height: NODE_HEIGHT, rx: '8' }),
    s('text', { class: 'dag-node-glyph', x: '14', y: NODE_HEIGHT / 2 + 5, 'aria-hidden': 'true' }),
    s('text', { class: 'dag-node-name', x: '32', y: NODE_HEIGHT / 2 - 3 }),
    s('text', { class: 'dag-node-status', x: '32', y: NODE_HEIGHT / 2 + 13 }),
  )
  const open = (): void => {
    const id = group.dataset.jobId
    if (id) navigate(`/jobs/${id}`)
  }
  group.addEventListener('click', open)
  group.addEventListener('keydown', (event) => {
    const key = (event as KeyboardEvent).key
    if (key === 'Enter' || key === ' ') {
      event.preventDefault()
      open()
    }
  })
  return group
}

function updateNode(group: SVGElement, laid: Laid): void {
  const node = laid.node.node
  const p = presentationFor(node.status)
  const jobId = node.jobId
  const label = node.displayName || node.name
  if (jobId) group.dataset.jobId = jobId
  else delete group.dataset.jobId
  setAttr(group, 'class', `dag-node dag-node-${p.tone} ${jobId ? 'dag-node-link' : ''}`)
  setAttr(group, 'transform', `translate(${laid.x}, ${laid.y})`)
  setAttr(group, 'role', jobId ? 'link' : null)
  setAttr(group, 'tabindex', jobId ? '0' : null)
  setAttr(group, 'aria-label', jobId ? `${label}, ${node.status}. Open this job.` : null)
  setText(group.querySelector('title')!, `${label} — ${node.status}${node.decisionReason ? ` (${node.decisionReason})` : ''}`)
  setText(group.querySelector('.dag-node-glyph')!, p.glyph)
  setText(group.querySelector('.dag-node-name')!, truncate(label, 18))
  setText(group.querySelector('.dag-node-status')!, node.status)
}

function truncate(text: string, max: number): string {
  return text.length <= max ? text : `${text.slice(0, max - 1)}…`
}
