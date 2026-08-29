import type { FieldError } from '../api/errors'
// `./derive.ts`, with the extension, for the reason `op-generator.ts` sets out
// at length: this module is folded outside any bundler by
// `tools/gen-editor-corpus.mts`, and node's resolver will not complete the
// specifier for it. The type-only import above needs no such treatment —
// `import type` is erased before resolution ever happens.
import {
  type Card,
  type CardType,
  isAgentic,
  legalTargetsFor,
  MIN_REVISION_ROUNDS,
  NODE_ID_PATTERN,
  nextCardId,
  prefixViolation,
} from './derive.ts'

/**
 * The outline editor's state machine.
 *
 * PURE, like `derive.ts` and for the same reason: Task 28's corpus generator
 * folds real action sequences through this function outside React, so a
 * reducer that reached for a hook, a clock or the network could not be the
 * thing the Go validator judges.
 *
 * EVERY TRANSITION IS JUDGED BY `prefixViolation`, `move` and `remove`
 * included, and NEVER by `grammarViolation`. An editor mid-construction has no
 * complete document — `[]`, `[plan]` and `[plan, implement]` are all states a
 * person passes through — so complete-document legality would refuse the first
 * card ever added. `grammarViolation` appears in exactly one place in the whole
 * editor: Save. The split is what lets the grammar be structural (the menu
 * offers only legal types, a reorder that breaks the grammar is refused with
 * the reason on the card) without the editor fighting the person on the way up.
 *
 * A REFUSED TRANSITION RETURNS THE SAME `cards` ARRAY, by reference. Task 28's
 * property test asserts it: "refused with a reason" is an escape hatch only if
 * the state really did not move, and reference equality is the cheapest honest
 * form of that assertion.
 */
export type EditorState = {
  cards: readonly Card[]
  /**
   * The last refused transition, bound to the card it is about — or to no card
   * (`null`) when the thing refused was a POSITION rather than a card, which is
   * every refused `add`: the card it would have created does not exist, so
   * there is nothing on screen to hang the sentence from.
   */
  refusal: Refusal | null
  /**
   * A repair the editor made on the person's behalf, in their words. Today
   * there is exactly one — clearing a revision loop whose target was removed —
   * and it exists because the alternative to announcing it is doing it
   * silently.
   */
  note: string | null
}

export type Refusal = { cardId: string | null; reason: string }

/**
 * `setField`'s three rows are one action with a discriminated payload rather
 * than three actions, because they are one gesture — typing into a card — and
 * the discriminant is what keeps `maxRounds`' number from being spelled as a
 * string to fit beside the other two.
 *
 * `retarget` with an EMPTY target clears the loop: the revision loop is
 * optional (`Node.OnRequestChanges` carries `omitempty`), and the card's target
 * select is the control that turns it off, whose "no target" option has the
 * empty value like every other placeholder in this tree.
 */
export type EditorAction =
  | { kind: 'add'; type: CardType; at: number }
  | { kind: 'remove'; id: string }
  | { kind: 'move'; from: number; to: number }
  | { kind: 'retarget'; gateId: string; target: string }
  | { kind: 'setField'; id: string; field: 'harness'; value: string }
  | { kind: 'setField'; id: string; field: 'instructions'; value: string }
  | { kind: 'setField'; id: string; field: 'maxRounds'; value: number }
  | { kind: 'rename'; id: string; newId: string }

export const EMPTY_EDITOR: EditorState = { cards: [], refusal: null, note: null }

export function editorReducer(state: EditorState, action: EditorAction): EditorState {
  switch (action.kind) {
    case 'add':
      return addCard(state, action.type, action.at)
    case 'remove':
      return removeCard(state, action.id)
    case 'move':
      return moveCard(state, action.from, action.to)
    case 'retarget':
      return retarget(state, action.gateId, action.target)
    case 'setField':
      return setField(state, action)
    case 'rename':
      return rename(state, action.id, action.newId)
  }
}

/**
 * THE MENU AND THE REDUCER ASK THE SAME QUESTION, and this asks it in the form
 * that can answer WHY. `legalTypesAt` is defined as "the types for which
 * inserting a probe leaves `prefixViolation` null" — its own comment says so —
 * so building the candidate outline and judging it is that predicate, not a
 * second opinion about it. The difference is that the menu needs a list and a
 * refusal needs a sentence, and only the judge has the sentence.
 */
function addCard(state: EditorState, type: CardType, at: number): EditorState {
  const card: Card = { id: nextCardId(state.cards, type), type }
  const cards = [...state.cards.slice(0, at), card, ...state.cards.slice(at)]
  const violation = prefixViolation(cards)
  if (violation !== null) return refuse(state, null, violation)
  return { cards, refusal: null, note: null }
}

/**
 * REMOVAL IS A TRANSITION AND IS JUDGED LIKE ONE. Taking the only implement out
 * of a finished conveyor leaves `plan · gate · git-publish`, which no amount of
 * appending can rescue — so the removal is refused and the reason names the
 * card left stranded. The person takes the gate off first.
 */
function removeCard(state: EditorState, id: string): EditorState {
  const at = state.cards.findIndex((c) => c.id === id)
  if (at === -1) return state
  const cards = [...state.cards.slice(0, at), ...state.cards.slice(at + 1)]
  const violation = prefixViolation(cards)
  if (violation !== null) return refuse(state, id, violation)
  return withoutDanglingLoops(cards)
}

/**
 * A MOVE CANNOT DANGLE A REVISION TARGET, which is why this does not repair one
 * and `removeCard` does. A legal prefix admits no plan or implement after a
 * gate (`TRANSITIONS.gated` accepts `git-publish` alone), so any reorder that
 * would carry a gate's target behind it — or the gate in front of its target —
 * is refused by the judge below before the question arises.
 *
 * An index outside the outline is a no-op rather than a refusal: the controls
 * compute their own indices, so this is nonsense rather than something the
 * person tried and may not do, and a refusal message would describe neither.
 */
function moveCard(state: EditorState, from: number, to: number): EditorState {
  const moved = state.cards[from]
  if (moved === undefined || to < 0 || to >= state.cards.length) return state
  const without = [...state.cards.slice(0, from), ...state.cards.slice(from + 1)]
  const cards = [...without.slice(0, to), moved, ...without.slice(to)]
  const violation = prefixViolation(cards)
  if (violation !== null) return refuse(state, moved.id, violation)
  return { cards, refusal: null, note: null }
}

/**
 * The gate's optional revision loop.
 *
 * `legalTargetsFor` IS THE JUDGE, not `prefixViolation`: this transition moves
 * no card, so position legality is untouched and would pass whatever target it
 * was handed — including the review, which precedes the gate, is agentic, and
 * is still refused by `validateRevisions` because the bypass edge means it does
 * not dominate.
 */
function retarget(state: EditorState, gateId: string, target: string): EditorState {
  const gate = state.cards.find((c) => c.id === gateId)
  if (gate === undefined || gate.type !== 'gate') return state

  if (target === '') {
    return {
      cards: mapCard(state.cards, gateId, ({ revision, ...rest }) => rest),
      refusal: null,
      note: null,
    }
  }
  if (!legalTargetsFor(state.cards, gateId).includes(target)) {
    return refuse(
      state,
      gateId,
      `The revision loop on “${gateId}” cannot target “${target}”: it must be a plan or implement step before the gate.`,
    )
  }
  // The round count is the LOOP's setting and not the target's, so changing
  // where the loop points keeps how many times it may run.
  const maxRounds = gate.revision?.maxRounds ?? MIN_REVISION_ROUNDS
  return {
    cards: mapCard(state.cards, gateId, (c) => ({ ...c, revision: { target, maxRounds } })),
    refusal: null,
    note: null,
  }
}

/**
 * TYPING NEVER REFUSES. `prefixViolation` judges position only, and a field the
 * server would reject — empty instructions, nine thousand bytes of them — is an
 * ordinary intermediate state rather than an illegal one. Refusing the
 * keystroke that produced it is exactly the failure the prefix/complete split
 * exists to remove; the card counts the bytes inline and `grammarViolation`
 * holds the line at Save.
 *
 * An EMPTY STRING is stored rather than deleted, and that costs nothing on the
 * wire: `toNode` drops a falsy `harness` or `instructions` because `Node`'s
 * `omitempty` drops an empty string as well as an absent one.
 */
function setField(
  state: EditorState,
  action: Extract<EditorAction, { kind: 'setField' }>,
): EditorState {
  const card = state.cards.find((c) => c.id === action.id)
  if (card === undefined) return state

  if (action.field === 'maxRounds') {
    // No loop, nothing to set — the card renders no rounds input until a
    // target is picked, so this is unreachable from the UI and meaningless
    // besides: a round count with no target is not a revision loop.
    if (card.revision === undefined) return state
    const revision = { ...card.revision, maxRounds: action.value }
    return {
      cards: mapCard(state.cards, action.id, (c) => ({ ...c, revision })),
      refusal: null,
      note: null,
    }
  }
  /**
   * A HARNESS ON A GATE IS NOT A FIELD, it is a mistake — `validateNodeConfig`
   * answers "only agentic steps take a harness", so storing one would build a
   * document the write door refuses for a reason the person never typed. Like
   * the `maxRounds` no-op above, this is unreachable from the UI (a gate card
   * renders no harness or instructions control) and meaningless besides, and
   * for the same reason it is a no-op rather than a refusal: nobody asked for
   * it, so there is nobody to answer.
   *
   * MEASURED, not anticipated: the corpus generator dispatches by id without
   * seeing the outline, so it lands `setField harness` on gates — and without
   * this line the completed documents carry a harness on a gate and
   * `grammarViolation` refuses them at the end of the fold.
   */
  if (!isAgentic(card.type)) return state
  const { field, value } = action
  return {
    cards: mapCard(state.cards, action.id, (c) => ({ ...c, [field]: value })),
    refusal: null,
    note: null,
  }
}

/**
 * Ids auto-derive and are editable for taste, so this is the one transition
 * whose input is free text — and it is judged by the validator's own pattern
 * and its uniqueness rule rather than by position.
 *
 * IT CARRIES A REVISION LOOP WITH IT. A gate targeting the renamed step would
 * otherwise be left naming a node that is not there: a document that looks
 * finished, passes the grammar walk, and is refused by `validateRevisions` at
 * the door. Following the rename loses nothing, which is why — unlike
 * `removeCard`'s clearing — there is nothing to announce.
 */
function rename(state: EditorState, id: string, newId: string): EditorState {
  const card = state.cards.find((c) => c.id === id)
  if (card === undefined || newId === id) return state

  if (!NODE_ID_PATTERN.test(newId)) {
    return refuse(
      state,
      id,
      `“${newId}” is not a usable step id: it must match ${NODE_ID_PATTERN.source}.`,
    )
  }
  if (state.cards.some((c) => c.id === newId)) {
    return refuse(state, id, `Another step is already called “${newId}”; ids must be unique.`)
  }

  const cards = state.cards.map((c) => {
    const renamed = c.id === id ? { ...c, id: newId } : c
    return renamed.revision?.target === id
      ? { ...renamed, revision: { ...renamed.revision, target: newId } }
      : renamed
  })
  return { cards, refusal: null, note: null }
}

/**
 * Clear any revision loop left pointing at a step that is no longer here, and
 * say which. The plan's decided behaviour, and the reason it is clear-and-note
 * rather than either silent option: keeping a dangling target ships a document
 * `validateRevisions` refuses, and clearing it quietly drops a setting the
 * person chose.
 */
function withoutDanglingLoops(cards: readonly Card[]): EditorState {
  const present = new Set(cards.map((c) => c.id))
  const cleared: string[] = []
  const repaired = cards.map((card) => {
    const target = card.revision?.target
    if (target === undefined || present.has(target)) return card
    cleared.push(`${card.id} → ${target}`)
    const { revision, ...rest } = card
    return rest
  })
  if (cleared.length === 0) return { cards, refusal: null, note: null }
  return {
    cards: repaired,
    refusal: null,
    note: `The revision loop on ${cleared.join(', ')} was cleared: its target is no longer in the outline.`,
  }
}

/** One card replaced, the rest by reference — so React re-renders the row that changed. */
function mapCard(cards: readonly Card[], id: string, f: (card: Card) => Card): Card[] {
  return cards.map((c) => (c.id === id ? f(c) : c))
}

/** The same cards, by reference, plus the reason. */
function refuse(state: EditorState, cardId: string | null, reason: string): EditorState {
  return { cards: state.cards, refusal: { cardId, reason }, note: null }
}

/**
 * A server rejection, placed: per card where the path names one, on the banner
 * where it does not.
 *
 * NOT REDUCER STATE, and the separation is the point. A `Refusal` is this
 * editor's answer to a gesture the person just made and it is produced by a
 * pure function of the outline; this is the SERVER's answer to a document that
 * was posted, and it stops being true the moment anything changes. Folding it
 * into `EditorState` would make every keystroke responsible for deciding
 * whether a rejection from two seconds ago still applies. It lives with the
 * mutation that produced it, which throws it away on the next attempt.
 */
export type SaveRejection = {
  /** The unplaceable part, or the whole rejection when nothing was placeable. */
  banner: string | null
  /** Card id → the messages bound to it, in the order the server listed them. */
  byCard: ReadonlyMap<string, readonly string[]>
}

/**
 * `Violation.Path` for a node, as the API emits it: `nodes[2].harness`,
 * `nodes[0].on_request_changes.max_rounds`, or bare `nodes[4]`.
 *
 * THE INDEX IS THE WHOLE MECHANISM. Violations address the POSTED array
 * positionally (`workflows.go`'s violation → field-error mapping), so a card
 * list that posted its nodes in a different order — or a mapper that matched
 * `nodes[1]` against `nodes[10]` — would hang the server's sentence on the
 * wrong step. The anchors and the `\d+` group are what make that impossible.
 */
const NODE_PATH = /^nodes\[(\d+)\](?:\.(.+))?$/

/**
 * The ordering below is `applyProblemToForm`'s, deliberately: unplaceable wins,
 * then placed, then the detail. A message this screen cannot put next to a card
 * is still the server's explanation of why the save failed, and swallowing it
 * because something ELSE placed leaves a person staring at an editor that
 * refused with no reason given.
 *
 * The card-bound message keeps its FIELD in front of it. The server's sentences
 * are written to sit under a named field ("must be 1-8192 bytes"), and a card
 * carries five of them, so the path's tail is the only thing that says which.
 */
export function bindSaveErrors(
  problem: { fields: readonly FieldError[]; detail: string; title: string },
  cards: readonly Card[],
): SaveRejection {
  const byCard = new Map<string, string[]>()
  const unplaceable: string[] = []
  for (const { field, message } of problem.fields) {
    const match = NODE_PATH.exec(field)
    // An index past the end of the outline is unplaceable rather than
    // ignorable: it is the server describing a document this screen no longer
    // holds, and dropping it would refuse with nothing said.
    const card = match === null ? undefined : cards[Number(match[1])]
    if (card === undefined) {
      unplaceable.push(message)
      continue
    }
    const suffix = match?.[2]
    const text = suffix === undefined ? message : `${suffix}: ${message}`
    byCard.set(card.id, [...(byCard.get(card.id) ?? []), text])
  }
  if (unplaceable.length > 0) return { banner: unplaceable.join('; '), byCard }
  if (byCard.size > 0) return { banner: null, byCard }
  return { banner: problem.detail !== '' ? problem.detail : problem.title, byCard }
}
