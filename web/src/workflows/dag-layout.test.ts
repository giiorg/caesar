import { describe, expect, it } from 'vitest'
import { layoutDag } from './dag-layout'
import { type DocEdge, type DocNode, deriveEdges, toDocument } from './derive'

const node = (id: string, type: DocNode['type']): DocNode => ({ id, type })
const edge = (from: string, to: string, artifact: DocEdge['artifact']): DocEdge => ({
  from,
  to,
  artifact,
})

/**
 * THE CANONICAL CONVEYOR, as a document rather than as cards — this is what
 * comes back off the wire, and it is the fixture whose layering the bypass edge
 * makes non-obvious.
 */
const conveyor = {
  nodes: [
    node('im', 'implement'),
    node('rev', 'review'),
    node('g', 'gate'),
    node('pub', 'git-publish'),
  ],
  edges: [
    edge('im', 'rev', 'patch'),
    edge('im', 'g', 'patch'),
    edge('rev', 'g', 'review_verdict'),
    edge('g', 'pub', 'patch'),
  ],
}

describe('layoutDag', () => {
  /**
   * LONGEST PATH, WHICH IS WHY THE GATE IS IN COLUMN 3 AND NOT COLUMN 1.
   *
   * The gate has two incoming edges: the review's verdict from column 1, and
   * the implement's patch straight from column 0 — the bypass. A layering that
   * took the FIRST source, or the shortest path, or the nearest one, would put
   * the gate beside the review and draw the conveyor as a fork. Only
   * `1 + max(source columns)` puts each step past everything it consumes, which
   * is the one property a reader of this picture relies on.
   */
  it('lays the conveyor out as one step per column, bypass edge notwithstanding', () => {
    expect(layoutDag(conveyor).columns).toEqual([['im'], ['rev'], ['g'], ['pub']])
  })

  /**
   * THE BRANCHING DOCUMENT — two implements consuming one plan. Authorable
   * through the API (`Validate`: 0 violations), not authorable in the editor,
   * and therefore exactly the document this preview exists to render: the read
   * view can draw it even though the outline cannot open it.
   */
  it('puts siblings that consume the same producer in one column', () => {
    const branching = {
      nodes: [
        node('plan-1', 'plan'),
        node('impl-a', 'implement'),
        node('impl-b', 'implement'),
        node('gate-1', 'gate'),
        node('publish-1', 'git-publish'),
      ],
      edges: [
        edge('plan-1', 'impl-a', 'plan'),
        edge('plan-1', 'impl-b', 'plan'),
        edge('impl-a', 'gate-1', 'patch'),
        edge('gate-1', 'publish-1', 'patch'),
      ],
    }
    expect(layoutDag(branching).columns).toEqual([
      ['plan-1'],
      ['impl-a', 'impl-b'],
      ['gate-1'],
      ['publish-1'],
    ])
  })

  it('lays a single node out as a single column', () => {
    expect(layoutDag({ nodes: [node('im', 'implement')] }).columns).toEqual([['im']])
  })

  /**
   * NO EDGES AT ALL puts every node in the first column, which is what a
   * document with no bindings IS — a set of steps nothing orders. The
   * `edges` key is absent rather than empty here because that is the canonical
   * form the server emits.
   */
  it('stacks an unbound document into one column', () => {
    expect(
      layoutDag({ nodes: [node('a', 'implement'), node('b', 'gate'), node('c', 'git-publish')] })
        .columns,
    ).toEqual([['a', 'b', 'c']])
  })

  /**
   * ROW ORDER IS DOCUMENT ORDER, and the fixture lists the two column-1 nodes
   * in the OPPOSITE order to the edges that place them — so a layout that
   * ordered rows by the edge list rather than by the node list would swap them.
   */
  it('orders the rows of a column by document order, not by edge order', () => {
    const doc = {
      nodes: [node('root', 'plan'), node('second', 'implement'), node('first', 'implement')],
      edges: [edge('root', 'first', 'plan'), edge('root', 'second', 'plan')],
    }
    expect(layoutDag(doc).columns).toEqual([['root'], ['second', 'first']])
  })

  /**
   * THE EDGES CARRY THE CELLS THEIR ENDS SIT IN — the whole reason this
   * function returns them at all rather than leaving the renderer to look each
   * one up. The bypass edge is the interesting entry: it spans two columns
   * while its neighbours span one.
   */
  it('gives every edge the cells of both its ends', () => {
    expect(layoutDag(conveyor).edges).toEqual([
      { edge: edge('im', 'rev', 'patch'), from: { column: 0, row: 0 }, to: { column: 1, row: 0 } },
      { edge: edge('im', 'g', 'patch'), from: { column: 0, row: 0 }, to: { column: 2, row: 0 } },
      {
        edge: edge('rev', 'g', 'review_verdict'),
        from: { column: 1, row: 0 },
        to: { column: 2, row: 0 },
      },
      { edge: edge('g', 'pub', 'patch'), from: { column: 2, row: 0 }, to: { column: 3, row: 0 } },
    ])
  })

  it('gives a sibling in the second row of its column the row it is in', () => {
    const doc = {
      nodes: [node('root', 'plan'), node('a', 'implement'), node('b', 'implement')],
      edges: [edge('root', 'a', 'plan'), edge('root', 'b', 'plan')],
    }
    expect(layoutDag(doc).edges.map((laid) => laid.to)).toEqual([
      { column: 1, row: 0 },
      { column: 1, row: 1 },
    ])
  })

  it('is empty for an empty document', () => {
    expect(layoutDag({ nodes: [] })).toEqual({ columns: [], edges: [] })
  })

  /**
   * A CYCLE TERMINATES, AND THE CYCLE IS REACHABLE — this is not defensive
   * padding against an impossible input.
   *
   * Duplicate card ids are legal mid-edit: `prefixViolation` judges position
   * only and `grammarViolation` is not consulted until Save, so an outline
   * holding two cards both called `impl-1` is an ordinary intermediate state.
   * `deriveEdges` walks that outline and emits `impl-1 → impl-1` — a self-loop
   * — straight into the live preview. Naive longest-path recursion does not
   * return on it. The measured shape of the bug is a hung tab, not a wrong
   * picture, which is why it is pinned here rather than left to the renderer.
   */
  it('terminates on the self-loop two identically-named cards produce', () => {
    const cards = [
      { id: 'impl-1', type: 'implement' as const, harness: 'cc', instructions: 'x' },
      { id: 'impl-1', type: 'implement' as const, harness: 'cc', instructions: 'y' },
    ]
    // The self-loop is DERIVED, not hand-written: the assertion is about what
    // the editor actually feeds this function.
    expect(deriveEdges(cards)).toEqual([{ from: 'impl-1', to: 'impl-1', artifact: 'patch' }])
    expect(layoutDag(toDocument(cards)).columns).toEqual([['impl-1', 'impl-1']])
  })

  it('terminates on a two-node cycle', () => {
    const doc = {
      nodes: [node('a', 'implement'), node('b', 'gate')],
      edges: [edge('a', 'b', 'patch'), edge('b', 'a', 'patch')],
    }
    // NO EMPTY COLUMN, which is the assertion that bites. Charging the back
    // edge anything at all — laying it out as a source in column 0 rather than
    // skipping it — puts `b` in 1 and `a` in 2 with nothing in 0, and a preview
    // whose first column is blank reads as a step that failed to draw. Every
    // acyclic document is dense by construction, so this is the only fixture
    // that can see the difference.
    expect(layoutDag(doc).columns).toEqual([['b'], ['a']])
    expect(layoutDag(doc).edges.map((laid) => laid.edge.from)).toEqual(['a', 'b'])
  })

  /**
   * A PRECONDITION VIOLATION, PINNED RATHER THAN LICENSED. Both producers
   * guarantee edges name nodes of the document — `readDocument` refuses a
   * dangling one so that this is true, `deriveEdges` builds every edge from
   * card ids it walked past — so nothing in the app can reach this. It is here
   * because the function is nonetheless total and a reader deserves to know
   * which way: an unplaceable edge is left out, never drawn to a coordinate
   * this function invented.
   */
  it('lays no edge for an end that is not a node of the document', () => {
    const laid = layoutDag({
      nodes: [node('a', 'implement')],
      edges: [edge('a', 'ghost', 'patch')],
    })
    expect(laid.columns).toEqual([['a']])
    expect(laid.edges).toEqual([])
  })
})
