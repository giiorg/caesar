import type { DocEdge, DocShape } from './derive'

/**
 * Where a node sits: which column, and which row within it.
 *
 * BOTH ARE INDEXES, NOT PIXELS. Geometry — pill size, column pitch, padding —
 * belongs to the renderer, and keeping it out of here is what lets the layout
 * be tested by reading a small array instead of by asserting on coordinates
 * nobody can check by eye.
 */
export type Cell = { column: number; row: number }

/** One binding, with the cells its two ends sit in. */
export type LaidEdge = { edge: DocEdge; from: Cell; to: Cell }

export type DagLayout = {
  /** Node ids, column by column, each column in document order. */
  columns: string[][]
  /** The document's edges, in document order, each carrying its endpoints' cells. */
  edges: LaidEdge[]
}

/**
 * A document as a layered picture. PURE — no React, no geometry, no DOM.
 *
 * LONGEST-PATH LAYERING: a node's column is `1 + max(column of its sources)`.
 * The bypass edge is why it cannot be anything cheaper. In the ordinary
 * conveyor the gate consumes the review's verdict AND the implement's patch
 * directly, past the review — so the gate has sources in two different columns,
 * and a layering that took the first source, or the nearest one, or the
 * shortest path, would draw the conveyor as a fork. Longest path is the rule
 * that puts every step past everything it consumes, which is the one property a
 * person reading this picture relies on.
 *
 * NO CROSSING MINIMIZATION. V1 documents are the conveyor family plus the odd
 * API-authored branch; ordering rows by document order keeps the picture stable
 * as the editor edits and costs nothing a reader would notice.
 *
 * IT ASSUMES EDGES NAME NODES OF THIS DOCUMENT, and both producers guarantee
 * it: `readDocument` refuses a document with a dangling edge, and `deriveEdges`
 * builds every edge out of card ids it just walked past. An edge whose source
 * is not a node would silently contribute nothing here rather than throw, which
 * is the wrong kind of quiet — hence the check upstream, where `null` can be
 * returned honestly.
 *
 * IT DOES NOT ASSUME ACYCLICITY, and that is a reachable case rather than
 * defensive padding. Duplicate card ids are legal mid-edit — `prefixViolation`
 * judges position only, and `grammarViolation` is not consulted until Save — so
 * two cards both called `impl-1` make `deriveEdges` emit `impl-1 → impl-1` and
 * hand it to the live preview. Naive longest-path recursion does not return on
 * that, and the symptom is a hung tab rather than a wrong picture.
 */
export function layoutDag(doc: DocShape): DagLayout {
  const incoming = new Map<string, string[]>()
  for (const edge of doc.edges ?? []) {
    const sources = incoming.get(edge.to)
    if (sources === undefined) incoming.set(edge.to, [edge.from])
    else sources.push(edge.from)
  }

  const depth = new Map<string, number>()
  const resolving = new Set<string>()

  /**
   * A node's column, memoized, with the recursion guarded by `resolving`.
   *
   * A SOURCE ALREADY BEING RESOLVED IS A BACK EDGE, AND IT IS SKIPPED
   * OUTRIGHT — the cycle is laid out as if that edge were not there. There is
   * no correct picture of a cyclic dataflow document, and the ones that reach
   * here are the degenerate states an editor holds for a keystroke, so
   * termination and a stable arrangement are the whole requirement.
   *
   * Skipping rather than counting the back edge as zero is what keeps the
   * layering DENSE: every column from 0 up stays occupied, because a node in
   * column k took that number from `1 + max(...)` and the source that set it
   * therefore sits in k-1. Charging a back edge any value at all breaks that
   * chain and leaves a hole at the front — a two-node cycle lands its members
   * in columns 1 and 2 and nothing in 0 — and a preview whose first column is
   * blank reads as a step that failed to draw.
   */
  const columnOf = (id: string): number => {
    const known = depth.get(id)
    if (known !== undefined) return known
    resolving.add(id)
    let column = 0
    for (const source of incoming.get(id) ?? []) {
      if (resolving.has(source)) continue
      column = Math.max(column, columnOf(source) + 1)
    }
    resolving.delete(id)
    depth.set(id, column)
    return column
  }

  // WRITTEN STRAIGHT INTO THE COLUMN'S SLOT, AND NOT COMPACTED AFTERWARDS. A
  // pass that dropped empty columns at the end would be quietly repairing the
  // layering rather than relying on it, and it would hide the one bug worth
  // catching here: a hole in the middle of the result means some node was
  // placed with nothing to its left, which the rule above cannot produce and a
  // mis-handled back edge can. Leaving the hole in makes the cycle fixture see
  // it. The `undefined` arm is the FIRST node to land in a column, not a
  // failure — the nodes arrive in document order, which need not be
  // topological, so a column is as likely to be opened late as early.
  const columns: string[][] = []
  for (const node of doc.nodes) {
    const column = columnOf(node.id)
    const bucket = columns[column]
    if (bucket === undefined) columns[column] = [node.id]
    else bucket.push(node.id)
  }

  // ONE CELL PER ID. A repeated id is only reachable while the editor holds two
  // cards on one name — `grammarViolation` refuses duplicates at Save, not
  // before — and its edges then land on whichever pill this loop reached last.
  // WHICH ONE IS NOT A DECISION: both pills are drawn either way, the state
  // lasts as long as it takes to rename one of them, and preferring an end
  // would be behaviour no fixture could justify.
  const cells = new Map<string, Cell>()
  for (const [column, ids] of columns.entries()) {
    for (const [row, id] of ids.entries()) {
      cells.set(id, { column, row })
    }
  }

  const edges: LaidEdge[] = []
  for (const edge of doc.edges ?? []) {
    const from = cells.get(edge.from)
    const to = cells.get(edge.to)
    // AN END THAT IS NOT A NODE HAS NO CELL, so there is nothing to draw
    // between. Unreachable through either producer — `readDocument` refuses a
    // document with a dangling edge precisely so that this function's
    // precondition is true, and `deriveEdges` builds every edge out of card ids
    // it just walked past — but the type says `Cell | undefined` and a
    // renderer must not be handed a coordinate this function made up.
    if (from === undefined || to === undefined) continue
    edges.push({ edge, from, to })
  }

  return { columns, edges }
}
