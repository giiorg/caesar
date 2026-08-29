import { type Dispatch, useRef } from 'react'
import type { Card, CardType } from './derive'
import { AddStepMenu, CardControls, StepIdField } from './editor-cards'
import type { EditorAction, Refusal } from './editor-state'

/**
 * The definition as an ordered list of typed step cards — the outline half of
 * the locked design, and the same renderer in both places it appears.
 *
 * ONE COMPONENT, TWO MODES. The version read view shows a stored document's
 * steps; the editor shows the cards being authored; and the editor ALSO falls
 * back to the read-only mode for a document it cannot round-trip ("authored
 * outside the editor"). Three call sites, one row layout — a second renderer
 * for the read-only case would drift from this one field by field, and the
 * foreign-document case would be reading a projection the editor does not use.
 *
 * READ-ONLY IS THE ABSENCE OF `edit`, NOT A `readOnly` BOOLEAN. The plan asked
 * for the boolean; a boolean beside an optional `dispatch` admits two states
 * that mean nothing — read-only WITH a dispatch, editable WITHOUT one — and
 * whichever the caller picks, nothing complains until a control does nothing.
 * With the handles bundled, the type refuses both.
 *
 * IT RENDERS ANY DOCUMENT, not only ones this editor can author. The version
 * read view hands it branching documents written through the API, so the rows
 * are per-node and the order is the document's; nothing here consults the
 * grammar.
 */
export type OutlineEdit = {
  dispatch: Dispatch<EditorAction>
  /** Registered harness names, for the agentic cards' picker. */
  harnesses: readonly string[]
  refusal: Refusal | null
  /**
   * A repair the reducer made on the person's behalf — today, a revision loop
   * cleared because its target was removed. Announced rather than silent: the
   * alternative to telling them is dropping a setting they chose.
   */
  note: string | null
}

export function OutlineView(props: { cards: readonly Card[]; edit?: OutlineEdit }) {
  const { cards, edit } = props
  /**
   * The card being dragged, by index.
   *
   * A REF RATHER THAN `dataTransfer`, because the drag never leaves this list:
   * the payload is an index into an array this component already holds, and
   * round-tripping it through a string on the DataTransfer would be ceremony
   * plus a parse. `setData` is still called below — Firefox refuses to start a
   * drag without it — but it is for the browser, not for us.
   */
  const dragging = useRef<number | null>(null)

  // Unreachable from the wire: `Validate` refuses a document with no nodes, and
  // an editor's empty outline renders the add menu below instead. It is here
  // because an empty `<ol>` draws as nothing at all, which reads as an outline
  // that failed rather than one with nothing in it.
  if (cards.length === 0 && edit === undefined) {
    return <p className="text-text-muted text-sm">No steps in this definition.</p>
  }

  return (
    <div className="flex flex-col gap-2">
      {/* A refusal bound to no card is about a POSITION — every refused `add` —
          so it belongs above the list rather than on a card that does not
          exist. */}
      {edit?.refusal?.cardId === null && <Refused reason={edit.refusal.reason} />}
      {edit?.note !== undefined && edit.note !== null && (
        <p role="status" className="text-text-muted text-sm">
          {edit.note}
        </p>
      )}

      <ol aria-label="Steps" className="flex flex-col gap-2">
        {cards.map((card, index) => (
          <li
            // KEYED BY ID, which is safe here because ids are unique wherever
            // this component is used and not merely by convention: `add`
            // derives a free one (`nextCardId`), `rename` refuses a taken one,
            // and a stored document cannot carry a duplicate because
            // `validateShape` refuses it at the write door.
            //
            // Typing into the id field does NOT churn this key — `StepIdField`
            // holds the uncommitted text in its own state and dispatches on
            // blur, so `card.id` changes once per rename rather than once per
            // keystroke. An index key would instead re-key every row below an
            // insertion, which is the case this ordering-heavy list hits most.
            key={card.id}
            className="flex flex-col gap-2"
          >
            {edit !== undefined && (
              <AddStepMenu cards={cards} at={index} dispatch={edit.dispatch} />
            )}
            <Row
              card={card}
              index={index}
              cards={cards}
              edit={edit}
              onDragStart={() => {
                dragging.current = index
              }}
              onDrop={() => {
                const from = dragging.current
                dragging.current = null
                if (from !== null && from !== index && edit !== undefined) {
                  edit.dispatch({ kind: 'move', from, to: index })
                }
              }}
            />
          </li>
        ))}
      </ol>

      {edit !== undefined && (
        <AddStepMenu cards={cards} at={cards.length} dispatch={edit.dispatch} />
      )}
    </div>
  )
}

function Row(props: {
  card: Card
  index: number
  cards: readonly Card[]
  edit: OutlineEdit | undefined
  onDragStart: () => void
  onDrop: () => void
}) {
  const { card, edit } = props
  const refusal = edit?.refusal?.cardId === card.id ? edit.refusal.reason : null

  return (
    <article
      // `data-node-type` IS ON THE DOM for `phase-dots.tsx`'s reason and
      // `dag-preview.tsx`'s: a badge's meaning is its colour, and a test
      // reading class names would assert Tailwind's spelling rather than the
      // design.
      data-node-type={card.type}
      className="border-border flex flex-col gap-3 rounded-md border p-3"
      onDragOver={(e) => {
        // A drop target only exists if the dragover is cancelled — the one
        // piece of HTML5 drag-and-drop that reads backwards.
        if (edit !== undefined) e.preventDefault()
      }}
      onDrop={(e) => {
        e.preventDefault()
        props.onDrop()
      }}
    >
      <header className="flex items-center gap-2">
        {edit !== undefined && (
          <span
            // DRAGGABLE ON THE GRIP, NOT THE CARD, so that selecting text in a
            // textarea does not start a reorder.
            draggable
            data-testid={`grip-${card.id}`}
            aria-hidden
            className="text-text-muted cursor-grab select-none px-1"
            onDragStart={(e) => {
              e.dataTransfer?.setData('text/plain', String(props.index))
              props.onDragStart()
            }}
          >
            ⠿
          </span>
        )}
        <TypeBadge type={card.type} />
        {edit === undefined && <span className="text-text font-mono text-sm">{card.id}</span>}
        {edit !== undefined && (
          <span className="ml-auto flex gap-1">
            {/* THE ARROWS ARE NOT A FALLBACK, they are the phone's mechanism:
                HTML5 drag-and-drop produces no events from a touch, so on the
                device this screen was designed for the grip above does
                nothing. They are also what a keyboard reaches. */}
            <IconButton
              label={`Move ${card.id} up`}
              disabled={props.index === 0}
              onClick={() =>
                edit.dispatch({ kind: 'move', from: props.index, to: props.index - 1 })
              }
            >
              ↑
            </IconButton>
            <IconButton
              label={`Move ${card.id} down`}
              disabled={props.index === props.cards.length - 1}
              onClick={() =>
                edit.dispatch({ kind: 'move', from: props.index, to: props.index + 1 })
              }
            >
              ↓
            </IconButton>
            <IconButton
              label={`Remove ${card.id}`}
              onClick={() => edit.dispatch({ kind: 'remove', id: card.id })}
            >
              ✕
            </IconButton>
          </span>
        )}
      </header>

      {edit === undefined ? (
        <CardValues card={card} />
      ) : (
        <>
          <StepIdField card={card} dispatch={edit.dispatch} />
          <CardControls
            card={card}
            cards={props.cards}
            harnesses={edit.harnesses}
            dispatch={edit.dispatch}
          />
        </>
      )}

      {refusal !== null && <Refused reason={refusal} />}
    </article>
  )
}

/**
 * The typed fields as text — the read-only projection.
 *
 * IT READS THE CARD, NOT THE REGISTRY, and that is the one place these two
 * halves part company. The registry says which fields a card of this type may
 * carry; a stored document may carry others, or fewer, because it was written
 * through the API by something that never consulted this table. Rendering the
 * registry's rows here would print "Harness: —" under a node that has one under
 * another name, and hide anything the table has not heard of.
 */
function CardValues(props: { card: Card }) {
  const { card } = props
  const rows: { label: string; value: string }[] = []
  if (card.harness !== undefined) rows.push({ label: 'Harness', value: card.harness })
  if (card.instructions !== undefined)
    rows.push({ label: 'Instructions', value: card.instructions })
  if (card.revision !== undefined) {
    rows.push({
      label: 'Revision loop',
      value: `${card.revision.target}, up to ${card.revision.maxRounds} round${
        card.revision.maxRounds === 1 ? '' : 's'
      }`,
    })
  }
  if (rows.length === 0) return null
  return (
    <dl className="flex flex-col gap-1 text-sm">
      {rows.map((row) => (
        <div key={row.label} className="flex gap-2">
          <dt className="text-text-muted shrink-0">{row.label}</dt>
          <dd className="text-text whitespace-pre-wrap break-words">{row.value}</dd>
        </div>
      ))}
    </dl>
  )
}

/**
 * THE MOCKUP'S COLOUR LANGUAGE, READ THROUGH THE ONE THIS APP SPEAKS — the same
 * mapping `dag-preview.tsx` makes and for the same reasons, including the gate:
 * the mockup draws it red-pink, `danger` is this app's word for a failure, and
 * a gate is a decision. `phase-dots` already says `waiting_decision` in
 * `warning`.
 *
 * A pill here and a pill there must not disagree about what colour a gate is,
 * which is why this table is the same shape as that one. They are two files
 * because the SVG needs `fill-`/`stroke-` and this needs a background and a
 * border; a shared table would be a map from type to two unrelated strings.
 */
const BADGE_CLASS: Record<CardType, string> = {
  plan: 'bg-accent/15 text-accent',
  implement: 'bg-accent/15 text-accent',
  review: 'bg-accent/15 text-accent',
  gate: 'bg-warning/15 text-warning',
  'git-publish': 'bg-success/15 text-success',
}

function TypeBadge(props: { type: CardType }) {
  return (
    <span className={`rounded-sm px-2 py-0.5 font-mono text-xs ${BADGE_CLASS[props.type]}`}>
      {props.type}
    </span>
  )
}

/**
 * A refused transition, said where the person acted.
 *
 * `role="alert"` because it is the answer to something they just did and there
 * is no other feedback — the outline simply did not change, which on its own is
 * indistinguishable from a control that is broken.
 */
function Refused(props: { reason: string }) {
  return (
    <p role="alert" className="text-danger text-sm">
      {props.reason}
    </p>
  )
}

function IconButton(props: {
  label: string
  disabled?: boolean
  onClick: () => void
  children: string
}) {
  return (
    <button
      type="button"
      aria-label={props.label}
      disabled={props.disabled ?? false}
      onClick={props.onClick}
      className="border-border text-text rounded-md border px-2 py-1 text-xs disabled:opacity-40"
    >
      {props.children}
    </button>
  )
}
