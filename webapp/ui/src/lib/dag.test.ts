import { describe, expect, it } from 'vitest'
import { layoutDag, type DagInputNode } from './dag.ts'

const node = (name: string, ...dependsOn: string[]): DagInputNode => ({ name, dependsOn })

function rankOf(layout: ReturnType<typeof layoutDag<DagInputNode>>, name: string): number {
  return layout.nodes.find((n) => n.name === name)!.rank
}

describe('layoutDag', () => {
  it('places a node after every node it depends on', () => {
    const layout = layoutDag([
      node('build'),
      node('test', 'build'),
      node('deploy', 'test'),
    ])

    expect(rankOf(layout, 'build')).toBe(0)
    expect(rankOf(layout, 'test')).toBe(1)
    expect(rankOf(layout, 'deploy')).toBe(2)
  })

  it('ranks by the LONGEST dependency chain, not the shortest', () => {
    // deploy depends on build directly AND through test. It must sit after
    // test, or the edge from test would point backwards.
    const layout = layoutDag([
      node('build'),
      node('test', 'build'),
      node('deploy', 'build', 'test'),
    ])

    expect(rankOf(layout, 'deploy')).toBe(2)
    expect(layout.edges.every((e) => !e.isBackEdge)).toBe(true)
  })

  it('puts independent roots in the same first column', () => {
    const layout = layoutDag([node('lint'), node('build'), node('docs')])
    expect(layout.nodes.every((n) => n.rank === 0)).toBe(true)
    // And on distinct rows, so they do not draw on top of each other.
    const rows = new Set(layout.nodes.map((n) => n.y))
    expect(rows.size).toBe(3)
  })

  it('is deterministic, so a status change does not reshuffle the graph', () => {
    const nodes = [node('build'), node('test', 'build'), node('lint'), node('deploy', 'test', 'lint')]
    const first = layoutDag(nodes)
    const second = layoutDag([...nodes].reverse())

    const positions = (layout: typeof first) =>
      layout.nodes.map((n) => `${n.name}@${n.x},${n.y}`).sort()

    expect(positions(first)).toEqual(positions(second))
  })

  /**
   * The safety requirement. A cycle cannot occur in a valid workflow, but this
   * renders whatever the server sends, and a layout that loops forever takes
   * the whole tab with it.
   */
  it('terminates on a cycle and reports it', () => {
    const layout = layoutDag([node('a', 'c'), node('b', 'a'), node('c', 'b')])

    expect(layout.hasCycle).toBe(true)
    expect(layout.nodes).toHaveLength(3)
    // Every node still gets a position, so something renders.
    expect(layout.nodes.every((n) => Number.isFinite(n.x) && Number.isFinite(n.y))).toBe(true)
  })

  it('terminates on a self-dependency', () => {
    const layout = layoutDag([node('a', 'a'), node('b', 'a')])
    expect(layout.hasCycle).toBe(true)
    expect(layout.nodes).toHaveLength(2)
  })

  it('ignores a dependency on a node that does not exist', () => {
    // A partially expanded workflow legitimately references nodes not yet
    // created, and that must not drop the ones that do exist.
    const layout = layoutDag([node('build'), node('deploy', 'build', 'not-created-yet')])

    expect(layout.nodes).toHaveLength(2)
    expect(layout.edges).toHaveLength(1)
    expect(layout.edges[0]).toMatchObject({ from: 'build', to: 'deploy' })
  })

  it('handles an empty graph', () => {
    const layout = layoutDag([])
    expect(layout.nodes).toEqual([])
    expect(layout.width).toBe(0)
    expect(layout.height).toBe(0)
  })

  it('handles a deep chain without recursion limits', () => {
    // A recursive implementation would risk a stack overflow here.
    const nodes: DagInputNode[] = [node('n0')]
    for (let i = 1; i < 2000; i++) nodes.push(node(`n${i}`, `n${i - 1}`))

    const layout = layoutDag(nodes)
    expect(layout.nodes).toHaveLength(2000)
    expect(rankOf(layout, 'n1999')).toBe(1999)
  })

  it('reduces crossings by ordering on dependency position', () => {
    // Two parallel chains. Barycenter ordering should keep each chain's second
    // node aligned with its own parent rather than interleaving them.
    const layout = layoutDag([
      node('a1'),
      node('b1'),
      node('a2', 'a1'),
      node('b2', 'b1'),
    ])

    const a1 = layout.nodes.find((n) => n.name === 'a1')!
    const a2 = layout.nodes.find((n) => n.name === 'a2')!
    const b1 = layout.nodes.find((n) => n.name === 'b1')!
    const b2 = layout.nodes.find((n) => n.name === 'b2')!

    // Whichever chain ends up on top, each child sits on its parent's row.
    expect(a2.order).toBe(a1.order)
    expect(b2.order).toBe(b1.order)
  })
})
