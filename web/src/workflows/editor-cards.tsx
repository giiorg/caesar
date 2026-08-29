import { type Dispatch, useEffect, useState } from 'react'
import { CONTROL_CLASS, FieldFrame } from '../forms/field-frame'
import { CARD_REGISTRY, type CardFieldName } from './card-registry'
import {
  type Card,
  type CardType,
  instructionsByteCount,
  legalTargetsFor,
  legalTypesAt,
  MAX_INSTRUCTIONS_BYTES,
  MAX_REVISION_ROUNDS,
  MIN_REVISION_ROUNDS,
} from './derive'
import type { EditorAction } from './editor-state'

/**
 * The controls on an editable card, and the menu that adds one.
 *
 * HAND-AUTHORED TYPED INPUTS, not `useAppForm` — spec §10.9's ruling, and the
 * reason is that this is not a flat form. The REDUCER owns the state: ordering,
 * refusals and the revision loop's repair are decisions no field-level
 * validator can express, and react-hook-form's `useFieldArray` — which the
 * first draft of this task reached for — has no precedent anywhere in this tree.
 * What is reused is `field-frame.tsx`'s label/message/`aria-describedby`
 * wiring, which is the part that gets re-derived and re-forgotten per form.
 * `useField` itself is not: it reads react-hook-form's context, and there is no
 * form here to have one.
 *
 * WHICH CONTROL A FIELD RENDERS AS IS ANSWERED HERE, from the field's name,
 * rather than in the registry. Task 29 proves that table against the engine's
 * descriptor, and a `control: 'textarea'` column is a row the descriptor does
 * not carry and that coverage test therefore could not check.
 */
export function CardControls(props: {
  card: Card
  cards: readonly Card[]
  harnesses: readonly string[]
  dispatch: Dispatch<EditorAction>
}) {
  return (
    <div className="flex flex-col gap-3">
      {CARD_REGISTRY[props.card.type].fields.map((field) => (
        <CardFieldControl key={field.name} field={field.name} {...props} />
      ))}
    </div>
  )
}

function CardFieldControl(props: {
  field: CardFieldName
  card: Card
  cards: readonly Card[]
  harnesses: readonly string[]
  dispatch: Dispatch<EditorAction>
}) {
  switch (props.field) {
    case 'harness':
      return (
        <HarnessField card={props.card} harnesses={props.harnesses} dispatch={props.dispatch} />
      )
    case 'instructions':
      return <InstructionsField card={props.card} dispatch={props.dispatch} />
    case 'revision':
      return <RevisionField card={props.card} cards={props.cards} dispatch={props.dispatch} />
  }
}

/**
 * THE PICKER IS THE GUARD. Harness names are not validated at save — the
 * document carries a bare string and `Validate` never looks it up — so a typo
 * becomes a run that cannot start. Offering the registry is what makes the
 * common case correct.
 *
 * A NAME THE REGISTRY NO LONGER HAS IS STILL OFFERED, marked. Prefilling from
 * an older version can hand this card a harness that has since been
 * deregistered; a select that simply did not contain it would show blank and
 * quietly rewrite the step on the next save. The person has to be the one who
 * changes it.
 */
function HarnessField(props: {
  card: Card
  harnesses: readonly string[]
  dispatch: Dispatch<EditorAction>
}) {
  const id = `card-${props.card.id}-harness`
  const current = props.card.harness ?? ''
  const missing = current !== '' && !props.harnesses.includes(current)
  return (
    <FieldFrame id={id} label="Harness" message={null}>
      <select
        id={id}
        className={CONTROL_CLASS}
        value={current}
        onChange={(e) =>
          props.dispatch({
            kind: 'setField',
            id: props.card.id,
            field: 'harness',
            value: e.target.value,
          })
        }
      >
        <option value="">Pick a harness</option>
        {missing && <option value={current}>{current} (not registered)</option>}
        {props.harnesses.map((name) => (
          <option key={name} value={name}>
            {name}
          </option>
        ))}
      </select>
    </FieldFrame>
  )
}

/**
 * The byte counter is inline and it counts the way the server counts.
 *
 * BEFORE SAVE, because the alternative is a person typing nine thousand bytes
 * of prose and learning at the end that the limit was measured in bytes and
 * their em-dashes cost two each. `instructionsByteCount` is the same function
 * `fieldViolation` uses, so this number and the server's cannot disagree.
 */
function InstructionsField(props: { card: Card; dispatch: Dispatch<EditorAction> }) {
  const id = `card-${props.card.id}-instructions`
  const value = props.card.instructions ?? ''
  const bytes = instructionsByteCount(value)
  const over = bytes > MAX_INSTRUCTIONS_BYTES
  const message = over
    ? `${bytes} bytes of instructions; the limit is ${MAX_INSTRUCTIONS_BYTES}.`
    : null
  return (
    <FieldFrame id={id} label="Instructions" message={message}>
      <textarea
        id={id}
        rows={4}
        className={CONTROL_CLASS}
        aria-invalid={over}
        aria-describedby={over ? `${id}-error` : undefined}
        value={value}
        onChange={(e) =>
          props.dispatch({
            kind: 'setField',
            id: props.card.id,
            field: 'instructions',
            value: e.target.value,
          })
        }
      />
    </FieldFrame>
  )
}

/**
 * The gate's optional revision loop: one wire key, two controls.
 *
 * THE TARGET LIST IS `legalTargetsFor`'s ANSWER, which excludes the review even
 * though it is agentic and precedes the gate — the bypass edge means it does
 * not dominate, and `validateRevisions` refuses it. Offering it and letting the
 * save fail would teach the person a rule the screen could have told them.
 *
 * The rounds input only exists once a target does: a round count with no target
 * is not a revision loop, and the reducer ignores one.
 */
function RevisionField(props: {
  card: Card
  cards: readonly Card[]
  dispatch: Dispatch<EditorAction>
}) {
  const targetId = `card-${props.card.id}-revision-target`
  const roundsId = `card-${props.card.id}-revision-rounds`
  const revision = props.card.revision
  return (
    <>
      <FieldFrame id={targetId} label="Revision loop sends work back to" message={null}>
        <select
          id={targetId}
          className={CONTROL_CLASS}
          value={revision?.target ?? ''}
          onChange={(e) =>
            props.dispatch({
              kind: 'retarget',
              gateId: props.card.id,
              target: e.target.value,
            })
          }
        >
          <option value="">No revision loop</option>
          {legalTargetsFor(props.cards, props.card.id).map((id) => (
            <option key={id} value={id}>
              {id}
            </option>
          ))}
        </select>
      </FieldFrame>
      {revision !== undefined && (
        <FieldFrame id={roundsId} label="Maximum rounds" message={null}>
          <input
            id={roundsId}
            type="number"
            className={CONTROL_CLASS}
            min={MIN_REVISION_ROUNDS}
            max={MAX_REVISION_ROUNDS}
            value={revision.maxRounds}
            onChange={(e) =>
              props.dispatch({
                kind: 'setField',
                id: props.card.id,
                field: 'maxRounds',
                value: e.target.valueAsNumber,
              })
            }
          />
        </FieldFrame>
      )}
    </>
  )
}

/**
 * The step's id — auto-derived, editable for taste.
 *
 * IT COMMITS ON BLUR RATHER THAN ON EVERY KEYSTROKE, and that is the one place
 * in this editor where a control holds state the reducer does not. `rename`
 * validates against the validator's pattern, so a per-keystroke dispatch would
 * refuse the empty string the moment someone selected the text to retype it —
 * the refused-keystroke failure the whole prefix/complete split exists to
 * avoid, arriving through the back door. The typed text stays put when a rename
 * is refused, so the reason is next to something the person can fix.
 */
export function StepIdField(props: { card: Card; dispatch: Dispatch<EditorAction> }) {
  const id = `card-${props.card.id}-id`
  const committed = props.card.id
  const [typed, setTyped] = useState(committed)
  // Re-sync when the id changes from outside this input — a prefill, a draft
  // restore, or the reducer following a rename through a revision target.
  useEffect(() => setTyped(committed), [committed])

  return (
    <FieldFrame id={id} label="Step id" message={null}>
      <input
        id={id}
        className={`${CONTROL_CLASS} font-mono`}
        value={typed}
        onChange={(e) => setTyped(e.target.value)}
        onBlur={() => props.dispatch({ kind: 'rename', id: committed, newId: typed })}
        onKeyDown={(e) => {
          if (e.key === 'Enter') e.currentTarget.blur()
        }}
      />
    </FieldFrame>
  )
}

/**
 * The add-step menu for one gap in the outline.
 *
 * IT LISTS `legalTypesAt`'s ANSWER AND NOTHING ELSE — this is what "the grammar
 * is enforced structurally" means in the spec. A menu offering a review at
 * position zero would be offering a document the validator's consumption table
 * refuses, and teaching the rule through a rejection instead of through the
 * menu.
 *
 * A NATIVE `<select>`, for `select-field.tsx`'s reason: on the phone this
 * screen is designed for it opens the OS picker, and nothing this project would
 * build gets close. The value resets to the placeholder after each pick,
 * because adding a step is an action rather than a setting.
 */
export function AddStepMenu(props: {
  cards: readonly Card[]
  at: number
  dispatch: Dispatch<EditorAction>
}) {
  const legal = legalTypesAt(props.cards, props.at)
  // Nothing may go here — a finished conveyor has no gap that accepts a sixth
  // step. An empty menu would be a control that does nothing when opened.
  if (legal.length === 0) return null
  return (
    <select
      aria-label={gapLabel(props.cards, props.at)}
      className={`${CONTROL_CLASS} self-start text-sm`}
      value=""
      onChange={(e) => {
        props.dispatch({ kind: 'add', type: e.target.value as CardType, at: props.at })
      }}
    >
      <option value="">Add step…</option>
      {legal.map((type) => (
        <option key={type} value={type}>
          {type}
        </option>
      ))}
    </select>
  )
}

/**
 * A gap named by what it sits before, because that is what a person can see.
 *
 * Ids are unique across an outline (`add` derives a free one, `rename` refuses
 * a taken one), so these labels are unique too — which is what lets a test, and
 * a screen reader's element list, tell one menu from another.
 */
function gapLabel(cards: readonly Card[], at: number): string {
  if (cards.length === 0) return 'Add the first step'
  const before = cards[at]
  return before === undefined ? 'Add a step at the end' : `Add a step before ${before.id}`
}
