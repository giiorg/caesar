import { describe, expect, it } from 'vitest'
import { MIN_REVISION_ROUNDS, NODE_ID_PATTERN, prefixViolation } from './derive'
import { type EditorAction, type EditorState, EMPTY_EDITOR, editorReducer } from './editor-state'

/** Fold a script through the reducer, the way the component's `dispatch` would. */
const run = (actions: readonly EditorAction[], from: EditorState = EMPTY_EDITOR): EditorState =>
  actions.reduce(editorReducer, from)

/** The ordinary conveyor, built the way a person builds it: append, append, append. */
const CONVEYOR: readonly EditorAction[] = [
  { kind: 'add', type: 'plan', at: 0 },
  { kind: 'add', type: 'implement', at: 1 },
  { kind: 'add', type: 'review', at: 2 },
  { kind: 'add', type: 'gate', at: 3 },
  { kind: 'add', type: 'git-publish', at: 4 },
]

const ids = (state: EditorState) => state.cards.map((c) => c.id)
const types = (state: EditorState) => state.cards.map((c) => c.type)

describe('add', () => {
  it('appends a card of the asked-for type with an auto-derived id', () => {
    const state = run(CONVEYOR)
    expect(ids(state)).toEqual(['plan-1', 'impl-1', 'review-1', 'gate-1', 'publish-1'])
    expect(types(state)).toEqual(['plan', 'implement', 'review', 'gate', 'git-publish'])
    expect(state.refusal).toBeNull()
  })

  /**
   * THE GRAMMAR IS STRUCTURAL, so a review with no patch before it is refused
   * rather than accepted-and-flagged. The menu would never have offered it —
   * `legalTypesAt` answers `['plan', 'implement']` at position 0 — and this is
   * the reducer holding the same line for a caller that did not ask the menu.
   */
  it('refuses a review before any implement, with the reason', () => {
    const state = editorReducer(EMPTY_EDITOR, { kind: 'add', type: 'review', at: 0 })
    expect(state.cards).toEqual([])
    expect(state.refusal?.reason).toMatch(/review needs a patch before it/i)
  })

  /**
   * The refusal names no card, and that is not an oversight: the card it is
   * about was never created, so there is nothing on screen to hang it from. The
   * outline renders a null-bound refusal above the list, beside the add menu
   * that produced it.
   */
  it('binds an add refusal to the outline rather than to a card', () => {
    const state = editorReducer(EMPTY_EDITOR, { kind: 'add', type: 'git-publish', at: 0 })
    expect(state.refusal).not.toBeNull()
    expect(state.refusal?.cardId).toBeNull()
  })

  it('inserts in the middle when the position accepts the type', () => {
    const state = run([...CONVEYOR, { kind: 'add', type: 'implement', at: 2 }])
    expect(ids(state)).toEqual(['plan-1', 'impl-1', 'impl-2', 'review-1', 'gate-1', 'publish-1'])
  })
})

describe('move', () => {
  /**
   * The plan's own case. `git-publish` before the gate is the one reorder the
   * grammar exists to refuse — `validatePublishes` needs a dominating gate
   * examining exactly the patch being published, which is what makes
   * publish-then-gate unrepresentable rather than merely unusual.
   */
  it('refuses a publish dragged above the gate, leaving the outline untouched', () => {
    const built = run(CONVEYOR)
    const moved = editorReducer(built, { kind: 'move', from: 4, to: 3 })

    expect(types(moved)).toEqual(types(built))
    expect(moved.refusal?.reason).toMatch(/git-publish is the final step/i)
    // BOUND TO THE CARD THE PERSON DRAGGED, which is where the grip is and so
    // where they are looking when the drop is refused.
    expect(moved.refusal?.cardId).toBe('publish-1')
  })

  /**
   * A REFUSED OP LEAVES THE CARDS BYTE-IDENTICAL — asserted by reference, which
   * is the strongest cheap form of it. Task 28's property test leans on this:
   * "refused with a reason" is only an escape hatch rather than a hole if the
   * state really did not move.
   */
  it('returns the same cards array a refused transition started from', () => {
    const built = run(CONVEYOR)
    const moved = editorReducer(built, { kind: 'move', from: 4, to: 3 })
    expect(moved.cards).toBe(built.cards)
  })

  it('accepts a reorder the grammar allows', () => {
    const built = run([...CONVEYOR, { kind: 'add', type: 'implement', at: 2 }])
    const moved = editorReducer(built, { kind: 'move', from: 1, to: 2 })
    expect(ids(moved)).toEqual(['plan-1', 'impl-2', 'impl-1', 'review-1', 'gate-1', 'publish-1'])
    expect(moved.refusal).toBeNull()
  })
})

describe('remove', () => {
  /**
   * THE PLAN'S DECIDED BEHAVIOUR: clear the loop and SAY SO. Silently keeping a
   * target that names no step would ship a document the server refuses
   * (`validateRevisions`), and silently clearing it would lose a setting the
   * person chose without telling them.
   */
  it('clears a revision loop whose target it removes, and notes that it did', () => {
    const built = run([
      ...CONVEYOR,
      { kind: 'add', type: 'implement', at: 2 },
      { kind: 'retarget', gateId: 'gate-1', target: 'impl-2' },
    ])
    expect(built.cards.find((c) => c.id === 'gate-1')?.revision?.target).toBe('impl-2')

    const removed = editorReducer(built, { kind: 'remove', id: 'impl-2' })
    expect(removed.cards.find((c) => c.id === 'gate-1')?.revision).toBeUndefined()
    expect(removed.note).toMatch(/impl-2/)
    expect(removed.note).toMatch(/revision loop/i)
  })

  it('leaves a revision loop alone when it removes some other step', () => {
    const built = run([
      ...CONVEYOR,
      { kind: 'add', type: 'implement', at: 2 },
      { kind: 'retarget', gateId: 'gate-1', target: 'impl-1' },
    ])
    const removed = editorReducer(built, { kind: 'remove', id: 'impl-2' })
    expect(removed.cards.find((c) => c.id === 'gate-1')?.revision?.target).toBe('impl-1')
    expect(removed.note).toBeNull()
  })

  /**
   * REMOVE IS A TRANSITION AND IS JUDGED LIKE ONE. Taking the only implement
   * out of the conveyor leaves `plan · review · gate · git-publish`, which no
   * amount of appending can rescue — a review needs a patch before it. The
   * person has to take the downstream steps off first.
   *
   * THE REASON NAMES THE FIRST STRANDED CARD, which is the review and not the
   * card the person clicked: `prefixViolation` reports the earliest position
   * the walk cannot leave, and that is the step whose position actually became
   * impossible. The refusal is BOUND to `impl-1` all the same, because that is
   * the card the person acted on and where they are looking.
   */
  it('refuses a removal that strands the rest of the outline', () => {
    const built = run(CONVEYOR)
    const removed = editorReducer(built, { kind: 'remove', id: 'impl-1' })
    expect(removed.cards).toBe(built.cards)
    expect(removed.refusal?.cardId).toBe('impl-1')
    expect(removed.refusal?.reason).toMatch(/review needs a patch before it/i)
  })

  /**
   * The same rule with the review out of the way, so the message is about the
   * GATE — proof that the sentence tracks the outline rather than being one
   * fixed string this reducer always emits.
   */
  it('names the gate when the gate is what a removal strands', () => {
    const built = run([
      { kind: 'add', type: 'plan', at: 0 },
      { kind: 'add', type: 'implement', at: 1 },
      { kind: 'add', type: 'gate', at: 2 },
      { kind: 'add', type: 'git-publish', at: 3 },
    ])
    const removed = editorReducer(built, { kind: 'remove', id: 'impl-1' })
    expect(removed.cards).toBe(built.cards)
    expect(removed.refusal?.reason).toMatch(/gate follows the implement steps/i)
  })
})

describe('retarget', () => {
  it('sets the loop to a preceding implement, with the smallest legal round count', () => {
    const state = run([...CONVEYOR, { kind: 'retarget', gateId: 'gate-1', target: 'impl-1' }])
    expect(state.cards.find((c) => c.id === 'gate-1')?.revision).toEqual({
      target: 'impl-1',
      maxRounds: MIN_REVISION_ROUNDS,
    })
  })

  /**
   * THE REVIEW IS THE CASE WORTH PINNING. It is agentic and it precedes the
   * gate, so every intuition says it is a legal target — and the validator
   * refuses it, because the implement→gate patch edge bypasses the review and a
   * node that can be bypassed does not dominate.
   */
  it('refuses the review, which precedes the gate but does not dominate it', () => {
    const built = run(CONVEYOR)
    const state = editorReducer(built, { kind: 'retarget', gateId: 'gate-1', target: 'review-1' })
    expect(state.cards).toBe(built.cards)
    expect(state.refusal?.cardId).toBe('gate-1')
    expect(state.refusal?.reason).toMatch(/review-1/)
  })

  it('refuses a target that does not name a step at all', () => {
    const built = run(CONVEYOR)
    const state = editorReducer(built, { kind: 'retarget', gateId: 'gate-1', target: 'nowhere' })
    expect(state.cards).toBe(built.cards)
    expect(state.refusal).not.toBeNull()
  })

  /** The empty target is how the card's control turns the optional loop OFF. */
  it('clears the loop when the target is empty', () => {
    const built = run([...CONVEYOR, { kind: 'retarget', gateId: 'gate-1', target: 'impl-1' }])
    const cleared = editorReducer(built, { kind: 'retarget', gateId: 'gate-1', target: '' })
    expect(cleared.cards.find((c) => c.id === 'gate-1')?.revision).toBeUndefined()
    expect(cleared.refusal).toBeNull()
  })

  /** Round count survives a change of target — it is the loop's setting, not the target's. */
  it('keeps the round count when the target changes', () => {
    const built = run([
      ...CONVEYOR,
      { kind: 'add', type: 'implement', at: 2 },
      { kind: 'retarget', gateId: 'gate-1', target: 'impl-1' },
      { kind: 'setField', id: 'gate-1', field: 'maxRounds', value: 4 },
      { kind: 'retarget', gateId: 'gate-1', target: 'impl-2' },
    ])
    expect(built.cards.find((c) => c.id === 'gate-1')?.revision).toEqual({
      target: 'impl-2',
      maxRounds: 4,
    })
  })
})

describe('rename', () => {
  it('renames a card', () => {
    const state = run([...CONVEYOR, { kind: 'rename', id: 'impl-1', newId: 'build_it' }])
    expect(ids(state)).toEqual(['plan-1', 'build_it', 'review-1', 'gate-1', 'publish-1'])
    expect(state.refusal).toBeNull()
  })

  it('refuses an id the validator’s pattern rejects', () => {
    const built = run(CONVEYOR)
    const state = editorReducer(built, { kind: 'rename', id: 'impl-1', newId: 'Impl One' })
    expect(state.cards).toBe(built.cards)
    expect(state.refusal?.cardId).toBe('impl-1')
    expect(state.refusal?.reason).toContain(NODE_ID_PATTERN.source)
  })

  /** 64 characters is the cap; the 65th is what the `{0,63}` quantifier refuses. */
  it('refuses an id one character over the cap and accepts one at it', () => {
    const built = run(CONVEYOR)
    const at = `i${'x'.repeat(63)}`
    expect(editorReducer(built, { kind: 'rename', id: 'impl-1', newId: at }).refusal).toBeNull()
    const over = `i${'x'.repeat(64)}`
    expect(
      editorReducer(built, { kind: 'rename', id: 'impl-1', newId: over }).refusal,
    ).not.toBeNull()
  })

  it('refuses an id another step already carries', () => {
    const built = run(CONVEYOR)
    const state = editorReducer(built, { kind: 'rename', id: 'impl-1', newId: 'gate-1' })
    expect(state.cards).toBe(built.cards)
    expect(state.refusal?.reason).toMatch(/already called/i)
  })

  /**
   * A RENAME CARRIES THE REVISION LOOP WITH IT, and this is the case nothing
   * else covers: `remove` clears a dangling target loudly, but a rename that
   * left the old name behind would produce a document that looks finished,
   * saves, and is refused by `validateRevisions` for naming a node that is not
   * there. Following the rename loses nothing, so there is nothing to announce.
   */
  it('follows the rename through to a gate that targets the renamed step', () => {
    const built = run([...CONVEYOR, { kind: 'retarget', gateId: 'gate-1', target: 'impl-1' }])
    const renamed = editorReducer(built, { kind: 'rename', id: 'impl-1', newId: 'build_it' })
    expect(renamed.cards.find((c) => c.id === 'gate-1')?.revision?.target).toBe('build_it')
    expect(renamed.note).toBeNull()
  })

  it('is a no-op when the id does not change', () => {
    const built = run(CONVEYOR)
    expect(editorReducer(built, { kind: 'rename', id: 'impl-1', newId: 'impl-1' })).toBe(built)
  })
})

describe('setField', () => {
  it('records a harness and instructions on an agentic card', () => {
    const state = run([
      ...CONVEYOR,
      { kind: 'setField', id: 'impl-1', field: 'harness', value: 'claude-code' },
      { kind: 'setField', id: 'impl-1', field: 'instructions', value: 'do the thing' },
    ])
    const card = state.cards.find((c) => c.id === 'impl-1')
    expect(card?.harness).toBe('claude-code')
    expect(card?.instructions).toBe('do the thing')
  })

  /**
   * SETTING A FIELD NEVER REFUSES, and instructions past the byte cap is the
   * case that proves it. `prefixViolation` judges POSITION ONLY; a field the
   * server will reject is an ordinary intermediate state, and refusing the
   * keystroke that produced it is precisely the failure the prefix/complete
   * split exists to remove. The card shows the byte count inline; `Save` is
   * where the document has to be finished.
   */
  it('accepts instructions past the byte limit rather than refusing the keystroke', () => {
    const built = run(CONVEYOR)
    const huge = 'x'.repeat(9000)
    const state = editorReducer(built, {
      kind: 'setField',
      id: 'impl-1',
      field: 'instructions',
      value: huge,
    })
    expect(state.cards.find((c) => c.id === 'impl-1')?.instructions).toBe(huge)
    expect(state.refusal).toBeNull()
  })

  it('sets the round count on a gate that has a loop', () => {
    const state = run([
      ...CONVEYOR,
      { kind: 'retarget', gateId: 'gate-1', target: 'impl-1' },
      { kind: 'setField', id: 'gate-1', field: 'maxRounds', value: 5 },
    ])
    expect(state.cards.find((c) => c.id === 'gate-1')?.revision?.maxRounds).toBe(5)
  })

  /** No loop, nothing to set — the card renders no rounds input until a target is picked. */
  it('ignores a round count on a gate with no loop', () => {
    const built = run(CONVEYOR)
    const state = editorReducer(built, {
      kind: 'setField',
      id: 'gate-1',
      field: 'maxRounds',
      value: 5,
    })
    expect(state).toBe(built)
  })
})

/**
 * THE REDUCER'S INVARIANT, and it is `prefixViolation` rather than
 * `grammarViolation` deliberately. An editor is incomplete for its whole
 * authoring session — `[]`, `[plan]` and `[plan, implement]` are all states a
 * person passes through — so holding transitions to complete-document legality
 * would refuse the first card ever added.
 */
describe('every accepted transition leaves a legal prefix', () => {
  it('holds at the empty start and after every step of a scripted session', () => {
    const script: readonly EditorAction[] = [
      { kind: 'add', type: 'plan', at: 0 },
      { kind: 'add', type: 'implement', at: 1 },
      // Refused: a publish cannot precede its gate. The invariant must hold
      // across refusals too — a refusal that corrupted the state would be the
      // one way this loop could pass while the editor was broken.
      { kind: 'add', type: 'git-publish', at: 0 },
      { kind: 'add', type: 'implement', at: 2 },
      { kind: 'move', from: 1, to: 2 },
      { kind: 'add', type: 'review', at: 3 },
      { kind: 'add', type: 'gate', at: 4 },
      { kind: 'retarget', gateId: 'gate-1', target: 'impl-1' },
      { kind: 'add', type: 'git-publish', at: 5 },
      { kind: 'rename', id: 'impl-1', newId: 'first_pass' },
      { kind: 'remove', id: 'review-1' },
      { kind: 'setField', id: 'plan-1', field: 'instructions', value: 'draft it' },
    ]

    let state = EMPTY_EDITOR
    expect(prefixViolation(state.cards)).toBeNull()
    for (const action of script) {
      state = editorReducer(state, action)
      expect(prefixViolation(state.cards)).toBeNull()
    }
    // The script really did build something — an invariant loop over a state
    // that never changed would pass against a reducer that refused everything.
    expect(types(state)).toEqual(['plan', 'implement', 'implement', 'gate', 'git-publish'])
  })
})
