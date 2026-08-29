// THE `.ts` EXTENSIONS ARE LOAD-BEARING, and they are the one place `src/`
// spells an import the way `tools/` does. `tools/gen-editor-corpus.mts` runs
// this module — and `editor-state.ts` and `derive.ts` behind it — through
// `node --experimental-strip-types`, whose ESM resolver does NOT complete an
// extensionless specifier for a TypeScript file (measured on node 24.19.0,
// `tools/arch/main.ts` records the same). tsconfig's
// `allowImportingTsExtensions` is what lets tsc, Vite and node agree on this
// one spelling; nothing else in `src/` needs it, because nothing else in `src/`
// is executed outside a bundler.
import { CARD_TYPES, isAgentic, legalTypesAt } from './derive.ts'
import { type EditorAction, type EditorState, EMPTY_EDITOR, editorReducer } from './editor-state.ts'

/**
 * Deterministic editor operation sequences, for the §6.3 acceptance bar:
 * "random editor operation sequences within the grammar derive documents that
 * parse and validate SERVER-side".
 *
 * TWO CONSUMERS, ONE GENERATOR. `editor-property.test.ts` folds these through
 * the reducer and asserts the invariants; `tools/gen-editor-corpus.mts` folds
 * the SAME sequences and writes what comes out to `testdata/editor_corpus/`,
 * where the real Go `Validate` judges it. A second generator for the corpus
 * would let the two drift, and the corpus is the half nobody watches.
 *
 * NO PROPERTY-TESTING LIBRARY. fast-check would be a new dependency with its
 * own pin, install and audit — and shrinking, its main draw, buys little here:
 * the failures this hunts are "the reducer accepted an illegal outline" and
 * "the document the editor emits is refused by the engine", both of which are
 * legible from the seed and the op list a failure prints. Thirty lines of
 * mulberry32 over our own `EditorAction` union is the whole need.
 *
 * DETERMINISM IS LOAD-BEARING, not tidiness: the corpus is a COMMITTED
 * artifact riding the regen-diff gate, so `generateOps(7, 40)` must answer the
 * same list on every machine forever. Nothing here reads a clock,
 * `Math.random`, the environment or the filesystem.
 */

/**
 * One corpus document per seed.
 *
 * TWENTY-FOUR IS ABOUT COVERAGE, NOT CONFIDENCE — the property test runs an
 * order of magnitude more sequences and costs nothing, while each seed here is
 * a file in the repository that a human may have to read. The Go judge asserts
 * a diversity floor over this set and that every node type appears somewhere in
 * it, so the number is only ever allowed to shrink as far as those hold.
 */
export const SEEDS = [
  1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24,
] as const

/**
 * How many operations each corpus seed folds. Big enough that a sequence
 * regularly reaches four or five cards and exercises reorders and renames on
 * them; small enough that 24 documents stay readable and the node count stays
 * far below the validator's 50-node cap even if every op were an accepted add.
 */
export const OPS_PER_SEED = 40

/**
 * mulberry32 — 32-bit state, uniform enough for choosing among six op kinds,
 * and short enough to read in one sitting.
 *
 * `>>> 0` after every arithmetic step is what keeps this in the unsigned 32-bit
 * lane V8 and every other engine agree on; `Math.imul` is the 32-bit multiply
 * for the same reason. Both are about REPRODUCIBILITY across machines, which is
 * the property the committed corpus rests on.
 */
function mulberry32(seed: number): () => number {
  let a = seed >>> 0
  return () => {
    a = (a + 0x6d2b79f5) >>> 0
    let t = a
    t = Math.imul(t ^ (t >>> 15), t | 1)
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61)
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

/**
 * The ids the generator aims its by-id operations at.
 *
 * IT IS A GUESS, AND THAT IS THE DESIGN. `generateOps` does not see the state —
 * the signature is `(seed, count)` — so `remove impl-3` on an outline with two
 * implements is a no-op the reducer absorbs, exactly as a stale click would be.
 * The names are the ones `nextCardId` mints (`ID_STEM` + an ordinal) plus the
 * ones `rename` below introduces, so a healthy fraction of them land.
 */
const TARGET_IDS = [
  'plan-1',
  'impl-1',
  'impl-2',
  'impl-3',
  'review-1',
  'gate-1',
  'publish-1',
  'step-a',
  'step-b',
]

/**
 * Rename targets: every one matches `NODE_ID_PATTERN`, so the renames that fail
 * fail on UNIQUENESS — the branch worth exercising, since a colliding id is the
 * one the validator would refuse at the write door.
 */
const RENAME_IDS = ['step-a', 'step-b', 'first-step', 'the-gate']

/** Harness names, plus the empty string that clears one. `''` is a state the card renders. */
const HARNESS_VALUES = ['claude-code', 'codex', '']

/**
 * Instruction bodies: ASCII letters, digits, spaces and hyphens only.
 *
 * THE CHARSET IS A CONSTRAINT OF THE CORPUS, not a lack of imagination.
 * `encoding/json` escapes `<`, `>` and `&` as `<` and friends while
 * `JSON.stringify` does not, so a document carrying one of them would be
 * byte-identical in meaning and different on disk — and `editorcorpus_test.go`
 * compares the committed bytes against `Document.Canonical()` to prove this
 * editor emits the canonical encoding. Any character added here must survive
 * that comparison.
 */
const INSTRUCTION_VALUES = [
  'draft the change',
  'implement it',
  'review the patch carefully',
  'ship it',
  '',
]

/** The op kinds, weighted. */
type OpKind = 'add' | 'setField' | 'move' | 'rename' | 'remove' | 'retarget'

/**
 * ADDS DOMINATE, and they have to: every other operation names a card or an
 * index, so a uniform mix over an outline that starts empty spends its first
 * dozen draws on no-ops. The weights below were chosen so that a 40-op sequence
 * lands somewhere between three and eight cards — long enough for reorders and
 * revision loops to be real, short enough that the corpus stays readable.
 */
const WEIGHTS: readonly (readonly [OpKind, number])[] = [
  ['add', 42],
  ['setField', 20],
  ['move', 20],
  ['rename', 8],
  ['remove', 8],
  ['retarget', 10],
]
const TOTAL_WEIGHT = WEIGHTS.reduce((sum, [, w]) => sum + w, 0)

export function generateOps(seed: number, count: number): EditorAction[] {
  const rand = mulberry32(seed)
  const pick = <T>(xs: readonly T[]): T => xs[Math.floor(rand() * xs.length)] as T
  const int = (n: number) => Math.floor(rand() * n)

  const ops: EditorAction[] = []
  for (let i = 0; i < count; i++) {
    let roll = rand() * TOTAL_WEIGHT
    let kind: OpKind = 'add'
    for (const [candidate, weight] of WEIGHTS) {
      roll -= weight
      if (roll < 0) {
        kind = candidate
        break
      }
    }
    ops.push(op(kind, pick, int, rand))
  }
  return ops
}

function op(
  kind: OpKind,
  pick: <T>(xs: readonly T[]) => T,
  int: (n: number) => number,
  rand: () => number,
): EditorAction {
  switch (kind) {
    case 'add':
      // `at` may point past the end of a short outline. `addCard` splices with
      // slices, which clamp, so that lands as an append — the same thing the
      // trailing add-step menu does.
      return { kind: 'add', type: pick(CARD_TYPES), at: int(8) }
    case 'move': {
      /**
       * MOSTLY ONE STEP, SOMETIMES ANYWHERE — the editor's two reorder
       * controls, in the proportion they exist. The ↑/↓ buttons move a card by
       * exactly one (and they are the PHONE's mechanism, since HTML5 drag emits
       * nothing from a touch); the grip can drop a card anywhere in the list.
       *
       * MEASURED, and this is the reason the split is here rather than a
       * uniform pair of indices. With `from: int(8), to: int(8)` the twenty-four
       * corpus seeds contained ZERO effective reorders — 136 move ops, of which
       * 88 were refused by the grammar and every one of the four the reducer
       * accepted had `from === to`. The corpus was then byte-identical with
       * `move` replaced by `return state`, which is the plan's own proof (v)
       * failing: a corpus that never reorders anything cannot be the reducer's
       * subject. Legal reorders are almost all ADJACENT — the V1 grammar only
       * lets two same-type neighbours change places — so weighting toward ±1 is
       * what makes them happen at all.
       */
      const from = int(5)
      const to = rand() < 0.75 ? from + (rand() < 0.5 ? -1 : 1) : int(5)
      return { kind: 'move', from, to }
    }
    case 'remove':
      return { kind: 'remove', id: pick(TARGET_IDS) }
    case 'rename':
      return { kind: 'rename', id: pick(TARGET_IDS), newId: pick(RENAME_IDS) }
    case 'retarget':
      // The empty target is the "no revision loop" option on the gate card's
      // select, so it belongs in the pool rather than beside it.
      return { kind: 'retarget', gateId: pick(TARGET_IDS), target: pick([...TARGET_IDS, '']) }
    case 'setField': {
      const field = pick(['harness', 'instructions', 'maxRounds'] as const)
      const id = pick(TARGET_IDS)
      if (field === 'maxRounds') {
        // IN RANGE, ALWAYS. `setField` is the one transition that refuses
        // nothing — typing is not a legality question — so an out-of-range
        // round count would simply sit in the state until `grammarViolation`
        // caught it at save, and every corpus document that drew one would be
        // unsaveable for a reason that has nothing to do with derivation.
        return { kind: 'setField', id, field, value: 1 + int(10) }
      }
      return {
        kind: 'setField',
        id,
        field,
        value: pick(field === 'harness' ? HARNESS_VALUES : INSTRUCTION_VALUES),
      }
    }
  }
}

/**
 * Drive an arbitrary legal PREFIX up to a complete, saveable document — through
 * the reducer, never around it.
 *
 * WHY IT EXISTS: a generated sequence stops wherever it stops, and `[plan]` or
 * `[implement, review]` is a perfectly legal prefix that is not a document. The
 * acceptance bar is about documents the EDITOR produces, so the completion has
 * to be something the editor could do — which means dispatching the same
 * actions a person clicking would, and inheriting every refusal they would hit.
 *
 * IT APPENDS AT THE END AND ASKS `legalTypesAt` WHAT MAY GO THERE, then prefers
 * the type that advances the grammar: `git-publish` if the gate is placed, a
 * gate if the implements are, an implement otherwise. That preference walks the
 * state machine forward on every iteration — `implemented → gated → published`
 * — which is why the loop terminates rather than needing a guard.
 *
 * THEN IT FILLS THE AGENTIC FIELDS, because completeness is not only structural:
 * `validateNodeConfig` requires a harness and instructions on plan/implement/
 * review, and a generated sequence may have cleared either with the empty
 * string it is allowed to type.
 */
export function forceComplete(state: EditorState): EditorState {
  let current = state
  for (;;) {
    const legal = legalTypesAt(current.cards, current.cards.length)
    const next = (['git-publish', 'gate', 'implement'] as const).find((t) => legal.includes(t))
    if (next === undefined) break
    current = editorReducer(current, { kind: 'add', type: next, at: current.cards.length })
  }
  for (const card of current.cards) {
    if (!isAgentic(card.type)) continue
    if (!card.harness) {
      current = editorReducer(current, {
        kind: 'setField',
        id: card.id,
        field: 'harness',
        value: 'claude-code',
      })
    }
    if (!card.instructions) {
      current = editorReducer(current, {
        kind: 'setField',
        id: card.id,
        field: 'instructions',
        value: `do the ${card.type} work`,
      })
    }
  }
  return current
}

/**
 * One seed, folded: generate, apply, complete. The single definition of "the
 * document seed N produces", shared by the property test and the corpus
 * generator so the two can never disagree about what they are asserting on.
 */
export function foldSeed(seed: number, count: number = OPS_PER_SEED): EditorState {
  let state: EditorState = EMPTY_EDITOR
  for (const action of generateOps(seed, count)) state = editorReducer(state, action)
  return forceComplete(state)
}
