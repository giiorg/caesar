import { useId } from 'react'
import { type Cell, layoutDag } from './dag-layout'
import type { CardType, DocShape } from './derive'

/**
 * A definition document as a left-to-right layered picture.
 *
 * IT TAKES THE DOCUMENT AND FETCHES NOTHING, which is what lets one component
 * serve both of its callers: the version read view hands it a document off the
 * wire, and the editor hands it `toDocument(cards)` on every keystroke. A
 * preview that looked up its own subject could not be live.
 *
 * IT RENDERS ANY DOCUMENT, not only the ones the outline editor can author.
 * §6.1 is explicit about it — a branching document written through the API
 * opens read-only, and the picture is the whole of what its reader gets. So the
 * input is the document's shape and never the editor's `Card`.
 *
 * NO GRAPH LIBRARY. The spec says so and the pin lane enforces the stance; the
 * layout is `dag-layout.ts`, forty lines of longest-path layering, and this
 * file is the part that turns cells into coordinates.
 */
export function DagPreview(props: { doc: DocShape }) {
  const { columns, edges } = layoutDag(props.doc)
  // MARKER IDS ARE DOCUMENT-GLOBAL. Two previews on one page — which is exactly
  // what the editor screen will be once the read-only outline lands beside it —
  // would otherwise declare the same `id` twice, and `url(#…)` resolves to
  // whichever came first. `useId` gives each instance its own; the colons React
  // puts in it are legal in a URL fragment but read badly in markup, so they go.
  const arrow = `dag-arrow-${useId().replaceAll(':', '')}`

  // AN EMPTY OUTLINE SAYS SO RATHER THAN DRAWING AN EMPTY FRAME. Unreachable
  // from the wire — `Validate` refuses a document with no nodes — but the first
  // thing the editor shows is a preview of nothing, and a bordered empty box is
  // indistinguishable from a preview that failed to draw.
  if (columns.length === 0) {
    return <p className="text-text-muted text-sm">No steps to draw yet.</p>
  }

  const rows = Math.max(...columns.map((column) => column.length))
  const width = PAD * 2 + (columns.length - 1) * COLUMN_PITCH + PILL_W
  const height = PAD * 2 + (rows - 1) * ROW_PITCH + PILL_H

  return (
    // THE COMPONENT SCROLLS, THE PAGE DOES NOT. The SVG carries explicit pixel
    // dimensions rather than 100%, so a wide workflow overflows this box and is
    // reached by scrolling inside it — a phone rendering a six-column document
    // must not end up with a horizontally scrolling document body.
    <div className="border-border overflow-x-auto rounded-md border p-2">
      <svg
        role="img"
        aria-label={imageLabel(props.doc)}
        width={width}
        height={height}
        viewBox={`0 0 ${width} ${height}`}
      >
        <defs>
          <marker
            id={arrow}
            viewBox="0 0 8 8"
            refX="7"
            refY="4"
            markerWidth="6"
            markerHeight="6"
            orient="auto-start-reverse"
          >
            <path d="M 0 0 L 8 4 L 0 8 z" className="fill-border" />
          </marker>
        </defs>

        {edges.map((laid) => (
          <line
            // The triple IS the identity: `validateShape` refuses a duplicate
            // `from→to (artifact)`, so no two edges of a stored document share
            // one. An index key would re-key every line whenever the editor
            // inserts a card above them.
            key={`${laid.edge.from}→${laid.edge.to}:${laid.edge.artifact}`}
            x1={left(laid.from) + PILL_W}
            y1={top(laid.from) + PILL_H / 2}
            x2={left(laid.to)}
            y2={top(laid.to) + PILL_H / 2}
            markerEnd={`url(#${arrow})`}
            className="stroke-border"
            strokeWidth={1}
          >
            {/* THE ARTIFACT TYPE IS ONLY LEGIBLE HERE. Two of the gate's
                incoming lines are otherwise identical — a patch straight from
                the implement and a verdict from the review — and which is
                which is what a person opened the definition to find out. */}
            <title>{`${laid.edge.from} → ${laid.edge.to}: ${laid.edge.artifact}`}</title>
          </line>
        ))}

        {placements(columns).map((placed) => (
          <NodePill
            key={placed.key}
            id={placed.id}
            type={typeOf(props.doc, placed.id)}
            at={placed.at}
          />
        ))}
      </svg>
    </div>
  )
}

/**
 * Every pill, flattened out of the columns, each with a React key.
 *
 * KEYED BY ID RATHER THAN BY CELL, so that inserting a card above an existing
 * one moves its pill instead of re-mounting every pill below it — which is what
 * a position key does, and what the editor would do on every keystroke.
 *
 * THE OCCURRENCE SUFFIX IS FOR AN ID THAT REPEATS, which is an ordinary
 * intermediate state and not a broken one: two cards can share a name while the
 * editor is mid-edit, since `grammarViolation` refuses duplicates at Save and
 * not before. A duplicate React key would drop one of the two pills from the
 * picture of the very state the person is trying to fix.
 */
function placements(columns: readonly string[][]): { id: string; at: Cell; key: string }[] {
  const seen = new Map<string, number>()
  const placed: { id: string; at: Cell; key: string }[] = []
  for (const [column, ids] of columns.entries()) {
    for (const [row, id] of ids.entries()) {
      const before = seen.get(id) ?? 0
      seen.set(id, before + 1)
      placed.push({ id, at: { column, row }, key: before === 0 ? id : `${id}#${before}` })
    }
  }
  return placed
}

/**
 * One step.
 *
 * `data-node-type` IS ON THE DOM BECAUSE IT IS THE ONLY WAY A TEST CAN ASSERT
 * WHAT A PERSON SEES here: the whole content of a pill is its colour and its
 * shape, and a test reading class names would be asserting Tailwind's spelling
 * rather than the design. `phase-dots.tsx` made the same call for the same
 * reason, and this file follows its colour language rather than inventing a
 * second one.
 */
function NodePill(props: { id: string; type: CardType | null; at: Cell }) {
  const x = left(props.at)
  const y = top(props.at)
  return (
    <g data-node-type={props.type}>
      {/* id AND TYPE, the shape `phase-dots.tsx` uses for its own title — the
          type is not otherwise readable from a pill, only guessable from its
          colour, and a person checking a definition should not have to. */}
      <title>{`${props.id} — ${props.type ?? 'unknown'}`}</title>
      <rect
        x={x}
        y={y}
        width={PILL_W}
        height={PILL_H}
        // A GATE IS A SQUARE, exactly as it is in the phase dots: it is a
        // decision rather than work, and the two must differ by more than
        // colour for anyone who cannot tell the colours apart.
        rx={props.type === 'gate' ? 0 : 6}
        strokeWidth={1}
        className={PILL_CLASS[props.type ?? 'unknown']}
      />
      <text
        x={x + PILL_W / 2}
        y={y + PILL_H / 2}
        textAnchor="middle"
        dominantBaseline="central"
        fontSize={LABEL_FONT_PX}
        className="fill-text font-mono"
      >
        {truncate(props.id)}
      </text>
    </g>
  )
}

/**
 * THE MOCKUP'S COLOUR LANGUAGE, READ THROUGH THE ONE THIS APP ALREADY SPEAKS.
 *
 * `04-workflow-editor.html` draws agent nodes blue, git green and gates a
 * red-pink — and the pink is the one place this departs from it. `danger` is
 * this app's word for a failure, and a gate is a decision, not one; the phase
 * dots already say `waiting_decision` in `warning` and every run screen in the
 * app is read that way. Following the running convention rather than the
 * mockup's hex keeps one meaning per colour, which is worth more than matching
 * a swatch.
 *
 * Only the eleven semantic names resolve (`tools/arch/tokens.ts`), and `fill-`
 * and `stroke-` are colour utilities to that checker like any other.
 */
const PILL_CLASS: Record<CardType | 'unknown', string> = {
  plan: 'fill-accent/15 stroke-accent',
  implement: 'fill-accent/15 stroke-accent',
  review: 'fill-accent/15 stroke-accent',
  gate: 'fill-warning/15 stroke-warning',
  'git-publish': 'fill-success/15 stroke-success',
  // A node the layout placed and the document has no type for. Unreachable
  // through `readDocument`, which refuses an unknown type outright, and through
  // `toDocument`, whose nodes come from typed cards — but `columns` is a list
  // of ids and this is what an id with no node behind it draws as.
  unknown: 'fill-surface-raised stroke-border',
}

/**
 * The label a screen reader is given for the whole picture.
 *
 * `role="img"` collapses the SVG's internals for assistive technology, which is
 * right — a pile of rects and lines reads as nothing — and makes this the ONLY
 * description of the workflow that reaches a non-visual reader. So it names the
 * steps in order rather than saying "diagram".
 */
function imageLabel(doc: DocShape): string {
  const types = doc.nodes.map((n) => n.type)
  return `${types.length} step${types.length === 1 ? '' : 's'}: ${types.join(', ')}`
}

function typeOf(doc: DocShape, id: string): CardType | null {
  return doc.nodes.find((n) => n.id === id)?.type ?? null
}

/**
 * SVG TEXT NEITHER WRAPS NOR CLIPS, so a long label runs straight across the
 * next column. Ids are legal up to 64 characters (`^[a-z][a-z0-9_-]{0,63}$`),
 * and auto-derived ones are `impl-1`, so both extremes are ordinary.
 *
 * The whole id stays on the pill's `<title>`: truncating its only copy would
 * make two long ids indistinguishable, which is the opposite of what a step
 * name is for.
 */
function truncate(id: string): string {
  return id.length <= MAX_LABEL_CHARS ? id : `${id.slice(0, MAX_LABEL_CHARS - 1)}…`
}

/**
 * GEOMETRY, and the pill's width is COMPUTED FROM the label budget rather than
 * chosen beside it. A monospace face advances about 0.6em per glyph, so the two
 * numbers are one decision; writing them independently is how a pill ends up
 * narrower than the text it was sized for.
 */
const LABEL_FONT_PX = 11
const MAX_LABEL_CHARS = 17
const PILL_PADDING_X = 10
const PILL_W = Math.ceil(MAX_LABEL_CHARS * LABEL_FONT_PX * 0.6) + PILL_PADDING_X * 2
const PILL_H = 34
const COLUMN_PITCH = PILL_W + 48
const ROW_PITCH = PILL_H + 20
const PAD = 8

const left = (at: Cell) => PAD + at.column * COLUMN_PITCH
const top = (at: Cell) => PAD + at.row * ROW_PITCH
