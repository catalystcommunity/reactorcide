/**
 * Layered layout for a workflow graph.
 *
 * A Sugiyama-style layout, minus the parts that need a library: longest-path
 * layering to assign ranks, then barycenter ordering within each rank to reduce
 * edge crossings. That is enough for CI graphs, which are small, shallow, and
 * mostly tree-shaped.
 *
 * The one hard requirement is that a malformed graph must not hang the tab. A
 * cycle cannot occur in a valid workflow, but this renders whatever the server
 * sent — including whatever a future bug sends — so every traversal here is
 * bounded and cycle-safe rather than trusting the data.
 */

export interface DagInputNode {
  name: string
  dependsOn: string[]
}

export interface LaidOutNode<T> {
  node: T
  name: string
  /** Column, left to right: the depth of this node's longest dependency chain. */
  rank: number
  /** Row within the column. */
  order: number
  x: number
  y: number
}

export interface LaidOutEdge {
  from: string
  to: string
  /** An edge whose target sits at or before its source: only possible in a cycle. */
  isBackEdge: boolean
}

export interface DagLayout<T> {
  nodes: LaidOutNode<T>[]
  edges: LaidOutEdge[]
  width: number
  height: number
  /** True when a dependency cycle was detected and broken to lay the graph out. */
  hasCycle: boolean
}

export const NODE_WIDTH = 180
export const NODE_HEIGHT = 52
const RANK_GAP = 76
const ROW_GAP = 20

/**
 * Assigns each node a rank: one more than its deepest dependency.
 *
 * Iterative with an explicit stack and a visiting set. A recursive version
 * would blow the stack on a deep graph and loop forever on a cyclic one; here a
 * node already on the stack is treated as rank 0 for the purpose of breaking
 * the cycle, and the cycle is reported rather than hidden.
 */
function assignRanks(
  names: string[],
  dependencies: Map<string, string[]>,
): { ranks: Map<string, number>; hasCycle: boolean } {
  const ranks = new Map<string, number>()
  const state = new Map<string, 'visiting' | 'done'>()
  let hasCycle = false

  for (const start of names) {
    if (state.get(start) === 'done') continue

    // (name, dependenciesResolved) frames.
    const stack: { name: string; expanded: boolean }[] = [{ name: start, expanded: false }]

    while (stack.length > 0) {
      const frame = stack[stack.length - 1]

      if (frame.expanded) {
        stack.pop()
        let rank = 0
        for (const dependency of dependencies.get(frame.name) ?? []) {
          // A dependency still 'visiting' is a cycle; contribute nothing rather
          // than waiting for a rank that will never settle.
          if (state.get(dependency) === 'done') {
            rank = Math.max(rank, (ranks.get(dependency) ?? 0) + 1)
          }
        }
        ranks.set(frame.name, rank)
        state.set(frame.name, 'done')
        continue
      }

      frame.expanded = true
      state.set(frame.name, 'visiting')

      for (const dependency of dependencies.get(frame.name) ?? []) {
        const dependencyState = state.get(dependency)
        if (dependencyState === 'visiting') {
          hasCycle = true
          continue
        }
        // A dependency naming a node that does not exist is ignored rather
        // than fatal: a partially expanded workflow legitimately has these.
        if (dependencyState === 'done' || !dependencies.has(dependency)) continue
        stack.push({ name: dependency, expanded: false })
      }
    }
  }

  return { ranks, hasCycle }
}

/**
 * Orders nodes within each rank by the mean position of their dependencies,
 * which is what pulls an edge's endpoints towards each other and removes most
 * crossings. Two passes is plenty at CI graph sizes.
 */
function orderWithinRanks(byRank: Map<number, string[]>, dependencies: Map<string, string[]>): void {
  const positionOf = new Map<string, number>()
  const recordPositions = () => {
    for (const names of byRank.values()) {
      names.forEach((name, index) => positionOf.set(name, index))
    }
  }
  recordPositions()

  const ranks = [...byRank.keys()].sort((a, b) => a - b)
  for (let pass = 0; pass < 2; pass++) {
    for (const rank of ranks) {
      if (rank === 0) continue
      const names = byRank.get(rank)!
      const barycenter = new Map<string, number>()
      for (const name of names) {
        const parents = (dependencies.get(name) ?? []).filter((p) => positionOf.has(p))
        barycenter.set(
          name,
          parents.length === 0
            ? positionOf.get(name) ?? 0
            : parents.reduce((sum, p) => sum + (positionOf.get(p) ?? 0), 0) / parents.length,
        )
      }
      names.sort((a, b) => {
        const difference = (barycenter.get(a) ?? 0) - (barycenter.get(b) ?? 0)
        // Ties break by name so the layout is deterministic: a re-render after a
        // status change must not reshuffle the graph under the reader.
        return difference !== 0 ? difference : a.localeCompare(b)
      })
      recordPositions()
    }
  }
}

export function layoutDag<T extends DagInputNode>(nodes: T[]): DagLayout<T> {
  if (nodes.length === 0) {
    return { nodes: [], edges: [], width: 0, height: 0, hasCycle: false }
  }

  const dependencies = new Map<string, string[]>()
  for (const node of nodes) dependencies.set(node.name, node.dependsOn ?? [])

  const names = nodes.map((n) => n.name)
  const { ranks, hasCycle } = assignRanks(names, dependencies)

  const byRank = new Map<number, string[]>()
  for (const name of names) {
    const rank = ranks.get(name) ?? 0
    const bucket = byRank.get(rank)
    if (bucket) bucket.push(name)
    else byRank.set(rank, [name])
  }
  // Sort each bucket before ordering so the starting point is deterministic.
  for (const bucket of byRank.values()) bucket.sort((a, b) => a.localeCompare(b))

  orderWithinRanks(byRank, dependencies)

  const nodeByName = new Map(nodes.map((n) => [n.name, n]))
  const laidOut: LaidOutNode<T>[] = []
  let maxRank = 0
  let maxRow = 0

  for (const [rank, bucket] of byRank) {
    maxRank = Math.max(maxRank, rank)
    maxRow = Math.max(maxRow, bucket.length)
    bucket.forEach((name, order) => {
      const node = nodeByName.get(name)
      if (!node) return
      laidOut.push({
        node,
        name,
        rank,
        order,
        x: rank * (NODE_WIDTH + RANK_GAP),
        y: order * (NODE_HEIGHT + ROW_GAP),
      })
    })
  }

  const edges: LaidOutEdge[] = []
  for (const node of nodes) {
    for (const dependency of node.dependsOn ?? []) {
      if (!nodeByName.has(dependency)) continue
      const fromRank = ranks.get(dependency) ?? 0
      const toRank = ranks.get(node.name) ?? 0
      edges.push({ from: dependency, to: node.name, isBackEdge: toRank <= fromRank })
    }
  }

  return {
    nodes: laidOut,
    edges,
    width: (maxRank + 1) * NODE_WIDTH + maxRank * RANK_GAP,
    height: maxRow * NODE_HEIGHT + Math.max(0, maxRow - 1) * ROW_GAP,
    hasCycle,
  }
}
