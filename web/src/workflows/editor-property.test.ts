import { describe, expect, it } from 'vitest'
import {
  type ArtifactType,
  type Card,
  type CardType,
  type DocEdge,
  grammarViolation,
  isChainShaped,
  prefixViolation,
  toDocument,
} from './derive'
import { type EditorState, EMPTY_EDITOR, editorReducer } from './editor-state'
import { foldSeed, generateOps, OPS_PER_SEED, SEEDS } from './op-generator'

/**
 * The §6.3 acceptance bar, client half: random editor operation sequences
 * derive documents that are legal.
 *
 * ITS SUBJECT IS THE REDUCER AND THE DERIVATION; ITS VERDICT IS ONLY HALF HERE.
 * "Validates server-side" is a statement about Go's `Validate`, which no
 * TypeScript test can make — so this file asserts everything checkable in this
 * language (the reducer's invariant, completeness, chain-shapedness, the
 * artifact table) and the seeded corpus carries the same documents to
 * `backend/internal/workflows/editorcorpus_test.go`, where the real validator
 * answers. Neither half is sufficient: this one runs on every keystroke and
 * catches a broken invariant in a second; that one is the only place the
 * engine's own opinion appears.
 *
 * TWO HUNDRED SEEDS, not the corpus's twenty-four. Sequences are pure and cost
 * microseconds, so the only reason to keep this set small would be reading it,
 * and nobody reads it — the corpus is the set humans read.
 */
const RUNS = 200

/** `validate.go`'s `produces` table, transcribed — the fixed output of each producing type. */
const PRODUCES: Partial<Record<CardType, ArtifactType>> = {
  plan: 'plan',
  implement: 'patch',
  review: 'review_verdict',
  // A GATE PASSES THROUGH what it examined, and on a V1 outline that is always
  // the implement's patch: the grammar admits `implement+` before the gate and
  // nothing after it but the publish, so the plan is never the nearest producer.
  gate: 'patch',
}

describe('the editor, driven by generated operation sequences', () => {
  /**
   * THE REDUCER'S INVARIANT, after every single step.
   *
   * `prefixViolation`, never `grammarViolation`: the fold starts from `[]` and
   * the first append can never be a complete document, so a
   * complete-legality invariant would refuse every run before its second op.
   * (That was round 1's blocker on this plan, recorded in the plan's own review
   * notes.)
   *
   * The escape hatch is REFUSED-WITH-REASON, and it is only an escape hatch
   * because of the second assertion: a refused transition must leave the cards
   * IDENTICAL — by reference, which is the cheapest honest form of "nothing
   * moved". Without it, a reducer could record a refusal and apply the
   * transition anyway and this property would never notice.
   */
  it('leaves a legal prefix after every accepted step, and moves nothing on a refusal', () => {
    let accepted = 0
    let refused = 0
    for (let seed = 1; seed <= RUNS; seed++) {
      let state: EditorState = EMPTY_EDITOR
      for (const [index, action] of generateOps(seed, OPS_PER_SEED).entries()) {
        const before = state
        state = editorReducer(state, action)
        const where = `seed ${seed}, op ${index}: ${JSON.stringify(action)}`

        if (state.refusal !== null) {
          refused++
          expect(state.cards, `${where} — a refusal must not move the outline`).toBe(before.cards)
        } else if (state.cards !== before.cards) {
          accepted++
        }
        expect(prefixViolation(state.cards), where).toBeNull()
      }
    }
    // THE PROPERTY IS VACUOUS IF THE GENERATOR ONLY EVER PRODUCES ONE OUTCOME.
    // A mix that never refuses would leave the second assertion above untested;
    // one that never accepts would leave the first testing `[]` forever.
    expect(accepted).toBeGreaterThan(RUNS)
    expect(refused).toBeGreaterThan(RUNS)
  })

  /**
   * FORCED COMPLETION ACTUALLY COMPLETES, and it completes through the reducer.
   *
   * This is the assertion that makes the corpus mean something: every document
   * it commits comes out of this path, so if completion could leave a document
   * `grammarViolation` still refuses, the corpus would be a set of documents
   * the editor's own Save button would not have posted.
   */
  it('completes every sequence into a saveable document', () => {
    for (let seed = 1; seed <= RUNS; seed++) {
      const cards = foldSeed(seed).cards
      expect(grammarViolation(cards), `seed ${seed}: ${describeCards(cards)}`).toBeNull()
      // The 50-node cap is `Validate`'s, not `grammarViolation`'s (which
      // deliberately leaves it to the server), so the generator's op budget is
      // held to it here rather than nowhere.
      expect(cards.length).toBeLessThanOrEqual(50)
    }
  })

  /**
   * THE ROUND TRIP. `isChainShaped` is the predicate the editor's PREFILL uses
   * to decide whether a stored version can be opened editable, and it is
   * defined as "re-deriving the edges from the node sequence reproduces the
   * stored set exactly". Asserting it on documents this editor just produced
   * says the thing that matters for §6.1: a version saved here reopens here.
   */
  it('produces documents the editor can reopen', () => {
    for (let seed = 1; seed <= RUNS; seed++) {
      const doc = toDocument(foldSeed(seed).cards)
      expect(isChainShaped(doc), `seed ${seed}`).toBe(true)
    }
  })

  /**
   * THE LOCAL SHAPE FACTS — one gate, one publish, and every edge carrying the
   * artifact its producer produces.
   *
   * These are the rules `validateConsumption` and `validateGatePassThrough`
   * enforce on the other side of the wire, stated here in the terms the
   * derivation uses. They are what turns a corpus failure from "the server said
   * no" into a sentence naming which rule the derivation broke.
   */
  it('derives exactly one gate, one publish, and correctly typed edges', () => {
    for (let seed = 1; seed <= RUNS; seed++) {
      const cards = foldSeed(seed).cards
      const doc = toDocument(cards)
      const types = cards.map((c) => c.type)
      expect(
        types.filter((t) => t === 'gate'),
        `seed ${seed}`,
      ).toHaveLength(1)
      expect(
        types.filter((t) => t === 'git-publish'),
        `seed ${seed}`,
      ).toHaveLength(1)
      expect(new Set(cards.map((c) => c.id)).size, `seed ${seed}: ids are unique`).toBe(
        cards.length,
      )

      const typeOf = new Map(cards.map((c) => [c.id, c.type]))
      for (const edge of doc.edges ?? []) {
        const from = typeOf.get(edge.from)
        expect(from, `seed ${seed}: edge from an unknown node ${edge.from}`).toBeDefined()
        expect(
          typeOf.get(edge.to),
          `seed ${seed}: edge to an unknown node ${edge.to}`,
        ).toBeDefined()
        expect(edge.artifact, `seed ${seed}: ${describeEdge(edge)}`).toBe(
          PRODUCES[from as CardType],
        )
      }
    }
  })

  /**
   * THE CORPUS SEEDS ACTUALLY REORDER SOMETHING — the assertion that keeps the
   * committed corpus a test of the REDUCER rather than of the derivation alone.
   *
   * MEASURED, and it is the session's real find. The first generator drew
   * `move`'s two indices uniformly, and across the twenty-four corpus seeds
   * that produced 136 move operations, 88 grammar refusals, and exactly FOUR
   * the reducer accepted — every one of them with `from === to`, which reorders
   * nothing. The corpus was then byte-identical with `moveCard` replaced by
   * `return state`: the plan's proof (v) failing outright, a whole editor
   * operation whose mutants the Go judge could never see. The fix is in
   * `op-generator.ts` (mostly ±1, the arrows' own move); this is the assertion
   * that reddens if it is ever undone.
   *
   * IT COMPARES THE ID ORDER, not the array identity. `moveCard` builds a new
   * array for `from === to` as well, so reference inequality counts a reorder
   * that did not happen — which is precisely how the gap hid.
   */
  it('reorders cards in the seeds the corpus is built from', () => {
    let reordered = 0
    for (const seed of SEEDS) {
      let state: EditorState = EMPTY_EDITOR
      for (const action of generateOps(seed, OPS_PER_SEED)) {
        const before = state
        state = editorReducer(state, action)
        if (action.kind === 'move' && idOrder(before.cards) !== idOrder(state.cards)) reordered++
      }
    }
    expect(reordered).toBeGreaterThan(2)
  })

  /**
   * DETERMINISM, asserted rather than assumed — the corpus is a committed
   * artifact whose regen-diff gate is only meaningful if the same seed answers
   * the same bytes forever. A generator that reached for `Math.random` would
   * pass every assertion above and redden `contracts:check` on a machine
   * belonging to somebody else.
   */
  it('answers the same document for the same seed', () => {
    for (const seed of [1, 7, 24]) {
      expect(JSON.stringify(toDocument(foldSeed(seed).cards))).toBe(
        JSON.stringify(toDocument(foldSeed(seed).cards)),
      )
    }
  })
})

const describeCards = (cards: readonly Card[]) => cards.map((c) => `${c.id}:${c.type}`).join(' · ')

const idOrder = (cards: readonly Card[]) => cards.map((c) => c.id).join(',')

const describeEdge = (e: DocEdge) => `${e.from} → ${e.to} carries ${e.artifact}`
