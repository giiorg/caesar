/**
 * The editor's derivation engine: ordered typed cards → a definition document.
 *
 * PURE. No React, no IO, no network. The outline editor, the DAG preview and
 * the corpus generator all import this, which is why it holds no state and
 * answers every question from its arguments.
 *
 * THE RULES ARE A TRANSCRIPTION, NOT A READING. `genConveyor` in
 * `backend/internal/workflows/property_test.go:163-208` is an executable
 * statement of the same rules, drawn by `rapid` and asserted by
 * `TestPropValidAcceptsGeneratedConveyors` to produce documents `Validate`
 * accepts on every draw. Each derivation branch below carries the rule it
 * transcribes and the consumption-table entry in `validate.go` that the edge
 * must satisfy. Where the spec prose and that generator differ in sharpness the
 * generator wins — see `deriveEdges`' plan branch.
 *
 * ADJACENCY WOULD BE WRONG, which is why derivation walks the WHOLE ordered
 * sequence rather than pairs. In `implement → review → gate → git-publish` the
 * gate examines the implement's patch PAST the review — the bypass edge — while
 * also consuming the review's verdict. Two of the gate's three bindings skip
 * over its neighbour.
 */

/** The V1 step vocabulary — `definition.go`'s `NodeType`, exactly. */
export type CardType = 'plan' | 'implement' | 'review' | 'gate' | 'git-publish'

/**
 * Every type, in the order menus offer them.
 *
 * The order is load-bearing: `legalTypesAt` filters this array and its answer
 * is what an add-step menu renders, so this is the menu's order.
 */
export const CARD_TYPES: readonly CardType[] = [
  'plan',
  'implement',
  'review',
  'gate',
  'git-publish',
]

/** `definition.go`'s `ArtifactType`. `log_ref`/`failure_snapshot` are runtime outputs, never bindings. */
export type ArtifactType = 'plan' | 'patch' | 'review_verdict'

/** Agentic types run a harness and so require harness + instructions. `NodeType.Agentic()`. */
export function isAgentic(type: CardType): boolean {
  return type === 'plan' || type === 'implement' || type === 'review'
}

/**
 * One card in the editor's outline.
 *
 * `revision` IS CAMELCASE AND THE WIRE IS NOT. The document carries
 * `on_request_changes: {target, max_rounds}`; `toDocument` is the single place
 * that conversion happens.
 */
export type Card = {
  /** `^[a-z][a-z0-9_-]{0,63}$` — `validate.go:28`'s pattern, and its 64-character cap. */
  id: string
  type: CardType
  /** Agentic cards only. */
  harness?: string
  /** Agentic cards only. */
  instructions?: string
  /** Gate cards only; `target` is a preceding plan/implement id. */
  revision?: { target: string; maxRounds: number }
}

/**
 * The wire shapes, exactly `definition.go`'s `Node`/`Edge`/`Document`.
 *
 * Key ORDER in these literals is load-bearing — see `toDocument`.
 */
export type DocNode = {
  id: string
  type: CardType
  harness?: string
  instructions?: string
  on_request_changes?: { target: string; max_rounds: number }
}

export type DocEdge = { from: string; to: string; artifact: ArtifactType }

/**
 * `edges` IS OPTIONAL because `toDocument` omits the key entirely when the set
 * is empty — the canonical absent form, since `Document.Edges` carries
 * `omitempty` and `ParseDocument` normalizes `"edges": []` to nil so the two
 * hash identically. Declaring it required would make that emit fail to
 * type-check.
 */
export type Doc = { schema: 2; nodes: DocNode[]; edges?: DocEdge[] }

/** `validate.go:23` — bytes, not characters. Exported so the editor's inline counter agrees with the server. */
export const MAX_INSTRUCTIONS_BYTES = 8192

/** `validate.go:25`. */
export const MAX_REVISION_ROUNDS = 10

/** `validate.go:28`, character for character. */
const NODE_ID_PATTERN = /^[a-z][a-z0-9_-]{0,63}$/

/**
 * Derived edges for a card sequence, in the emission order the corpus pins.
 *
 * ORDER IS VERSION IDENTITY, which is why this returns an array and not a set.
 * `Document.Canonical()` marshals the edge slice in order, `CreateVersion`
 * hashes exactly those bytes (`workflows.go:221`) and `GetVersionByHash`
 * decides from that hash whether a save registers a NEW version or returns the
 * existing one (`workflows.go:233-237`). Measured: swapping two edges of a
 * valid document leaves it equally valid and changes its SHA-256. So a drift in
 * this order would silently start minting duplicate versions of unchanged
 * workflows. The order transcribed here is `genConveyor`'s.
 *
 * TOTAL ON ILLEGAL SEQUENCES. The live preview calls this on whatever the
 * outline currently holds, which mid-construction is rarely a legal document;
 * every branch is guarded so a missing producer simply yields no edge rather
 * than a broken one. Legality is `prefixViolation`/`grammarViolation`'s job.
 */
export function deriveEdges(cards: readonly Card[]): DocEdge[] {
  const edges: DocEdge[] = []
  /** The id of the newest patch producer — an implement, or a gate passing one through. */
  let prevPatch: string | null = null
  let planId: string | null = null
  let firstImplementDone = false
  let reviewId: string | null = null

  for (const card of cards) {
    switch (card.type) {
      case 'plan':
        // A plan node consumes nothing (`validateConsumption`, NodePlan) and
        // produces `plan`, so it contributes no incoming edge here.
        planId = card.id
        break

      case 'implement':
        // "The plan feeds the FIRST implement" — sharper than "the first
        // agentic card", and the sharpness matters: a review cannot consume a
        // plan at all (`validateConsumption`, NodeReview: "review cannot
        // consume review_verdict or plan"), so `plan` never reaches anything
        // but an implement. `implement consumes at most 1 plan`, which is why
        // only the first one is fed.
        if (!firstImplementDone && planId !== null) {
          edges.push({ from: planId, to: card.id, artifact: 'plan' })
        }
        // "Each implement takes the previous implement's patch."
        // `implement consumes at most 1 patch`.
        if (prevPatch !== null) {
          edges.push({ from: prevPatch, to: card.id, artifact: 'patch' })
        }
        firstImplementDone = true
        prevPatch = card.id
        break

      case 'review':
        // "A review takes the NEAREST preceding patch" — `prevPatch`, not the
        // first producer. `review consumes exactly 1 patch`.
        if (prevPatch !== null) {
          edges.push({ from: prevPatch, to: card.id, artifact: 'patch' })
        }
        reviewId = card.id
        break

      case 'gate':
        // THE BYPASS EDGE. The gate examines the patch directly from the
        // implement, past the review — `a gate examines exactly one plan or
        // patch` — and separately consumes the review's verdict, `a gate
        // consumes at most 1 review_verdict`. This pair is the reason
        // derivation cannot be adjacency-based.
        if (prevPatch !== null) {
          edges.push({ from: prevPatch, to: card.id, artifact: 'patch' })
        }
        if (reviewId !== null) {
          edges.push({ from: reviewId, to: card.id, artifact: 'review_verdict' })
        }
        // A gate PASSES THROUGH its examined type
        // (`validateGatePassThrough`), so downstream the gate is the patch
        // producer. This assignment is what puts the gate on the publish's
        // edge below.
        prevPatch = card.id
        break

      case 'git-publish':
        // "git-publish takes the GATE's pass-through patch." Not the
        // implement's: `validatePublishes` requires a dominating gate examining
        // exactly the patch being published, which is what makes
        // publish-then-gate unrepresentable. `git-publish consumes exactly 1
        // patch` and nothing else.
        if (prevPatch !== null) {
          edges.push({ from: prevPatch, to: card.id, artifact: 'patch' })
        }
        break
    }
  }
  return edges
}

/**
 * One card as its wire node.
 *
 * THE OMISSIONS ARE `omitempty`, TRANSCRIBED. `Node`'s Harness, Instructions
 * and OnRequestChanges all carry `omitempty` (`definition.go:86-92`), which
 * drops an EMPTY STRING as well as an absent one — so a half-typed agentic card
 * carrying `''` must emit no key at all, or these bytes stop matching the
 * canonical bytes the server would produce for the same document. Key insertion
 * order matches `Node`'s Go field order, which `definition.go:104-108` says is
 * the canonical encoding order and must never be reordered.
 */
function toNode(card: Card): DocNode {
  return {
    id: card.id,
    type: card.type,
    ...(card.harness ? { harness: card.harness } : {}),
    ...(card.instructions ? { instructions: card.instructions } : {}),
    ...(card.revision
      ? {
          on_request_changes: {
            target: card.revision.target,
            max_rounds: card.revision.maxRounds,
          },
        }
      : {}),
  }
}

/**
 * The document for a card sequence — nodes in card order, edges derived.
 *
 * `JSON.stringify` of this is byte-identical to Go's `Document.Canonical()` for
 * the same document, which is the property the golden corpus in the test file
 * asserts against bytes printed by the real validator.
 */
export function toDocument(cards: readonly Card[]): Doc {
  const nodes = cards.map(toNode)
  const edges = deriveEdges(cards)
  // The canonical ABSENT form, not `[]`. The two hash identically because
  // `ParseDocument` nils an empty slice, but this is the form the server emits
  // and therefore the form a round-trip comparison sees.
  return edges.length === 0 ? { schema: 2, nodes } : { schema: 2, nodes, edges }
}

/**
 * The V1 grammar as a state machine: `plan? · implement+ · review? · gate · git-publish`.
 *
 * V1 pins EXACTLY ONE gate and ONE publish — the family `genConveyor` draws
 * from. `Validate` is strictly more permissive (a lone plan node, two gates in
 * sequence and a branching plan→{implement,implement} document all validate;
 * measured), and that gap is deliberate: it is exactly the set of documents the
 * editor opens read-only as "authored outside the editor".
 */
type GrammarState = 'start' | 'planned' | 'implemented' | 'reviewed' | 'gated' | 'published'

const TRANSITIONS: Record<GrammarState, Partial<Record<CardType, GrammarState>>> = {
  start: { plan: 'planned', implement: 'implemented' },
  planned: { implement: 'implemented' },
  implemented: { implement: 'implemented', review: 'reviewed', gate: 'gated' },
  reviewed: { gate: 'gated' },
  gated: { 'git-publish': 'published' },
  published: {},
}

/** What each type requires of its position, said the way a person would say it. */
const PLACEMENT_RULE: Record<CardType, string> = {
  plan: 'a plan opens the workflow, so it can only be the first step',
  implement:
    'an implement follows the optional plan and any earlier implement, and must come before the review, the gate and the publish',
  review:
    'a review needs a patch before it, so it follows the implement steps — and there is at most one',
  gate: 'a gate follows the implement steps and the optional review, and V1 allows exactly one',
  'git-publish': 'a git-publish is the final step and follows the gate',
}

/** What a sequence stopped in this state still needs before it is a document. */
const STILL_NEEDS: Record<Exclude<GrammarState, 'published'>, string> = {
  start: 'an implement step, a gate and a git-publish',
  planned: 'an implement step, a gate and a git-publish',
  implemented: 'a gate and a git-publish',
  reviewed: 'a gate and a git-publish',
  gated: 'a git-publish step',
}

/**
 * Walks the type sequence, returning the index of the first card that cannot go
 * where it is, or the state the whole sequence reaches.
 */
function walk(
  cards: readonly Card[],
): { ok: true; state: GrammarState } | { ok: false; at: number } {
  let state: GrammarState = 'start'
  for (const [index, card] of cards.entries()) {
    // THE ANNOTATION IS LOAD-BEARING, not decoration: without it `next` is
    // inferred from `TRANSITIONS[state]`, `state`'s narrowed type inside the
    // loop comes from `state = next` below, and TypeScript reports the cycle
    // (TS7022) rather than picking a type.
    const next: GrammarState | undefined = TRANSITIONS[state][card.type]
    if (next === undefined) return { ok: false, at: index }
    state = next
  }
  return { ok: true, state }
}

function misplaced(card: Card): string {
  return `${card.type} “${card.id}” cannot go here: ${PLACEMENT_RULE[card.type]}.`
}

/**
 * PREFIX legality: is this sequence on the way to some complete one?
 *
 * `[]`, `[implement]` and `[implement, review]` are all legal prefixes and NONE
 * of them is a legal complete document — an editor lives in this state for its
 * entire authoring session, so a reducer invariant written against
 * `grammarViolation` would refuse every ordinary construction step. This is the
 * REDUCER's invariant; `grammarViolation` is SAVE's precondition. `null` means
 * completable.
 *
 * COMPLETION IS BY APPENDING — literal regex-prefix legality — and that choice
 * is what gives the notion its teeth. Under "completable by inserting
 * anywhere", a leading review would be legal (drop an implement in front of
 * it) and the two notions would stop distinguishing the cases they exist to
 * distinguish.
 *
 * IT JUDGES POSITION ONLY, never field contents. A card whose instructions are
 * still empty is an ordinary intermediate state, not an illegal one; refusing a
 * keystroke for it is precisely the failure this split removes. Field legality
 * belongs to `grammarViolation`, at save.
 */
export function prefixViolation(cards: readonly Card[]): string | null {
  const result = walk(cards)
  if (result.ok) return null
  const card = cards[result.at]
  return card === undefined ? null : misplaced(card)
}

/**
 * COMPLETE-document legality: this sequence is saveable as-is.
 *
 * It owns every check the server would fail the document on, so a Save button
 * gated on it is honest rather than optimistic: position, completeness, ids,
 * per-type fields and the revision loop. Its one caller is Save.
 *
 * NOT ENFORCED HERE: the 50-node cap (`maxNodes`). Reaching it takes 46
 * implements; the server refuses it and the save path renders the field error.
 * Its absence is a decision, not an oversight.
 */
export function grammarViolation(cards: readonly Card[]): string | null {
  const result = walk(cards)
  if (!result.ok) {
    const card = cards[result.at]
    return card === undefined ? null : misplaced(card)
  }
  if (result.state !== 'published') {
    return `This workflow is not complete yet: it still needs ${STILL_NEEDS[result.state]}.`
  }

  const seen = new Set<string>()
  for (const card of cards) {
    if (!NODE_ID_PATTERN.test(card.id)) {
      return `“${card.id}” is not a usable step id: it must match ^[a-z][a-z0-9_-]{0,63}$.`
    }
    if (seen.has(card.id)) return `Two steps share the id “${card.id}”; ids must be unique.`
    seen.add(card.id)

    const fieldFault = fieldViolation(card)
    if (fieldFault !== null) return fieldFault
    const revisionFault = revisionViolation(card, cards)
    if (revisionFault !== null) return revisionFault
  }
  return null
}

/** `validateNodeConfig`, transcribed: agentic cards need both fields, others take neither. */
function fieldViolation(card: Card): string | null {
  if (isAgentic(card.type)) {
    if (!card.harness) return `Step “${card.id}” needs a harness.`
    if (card.harness.trim() !== card.harness) {
      return `Step “${card.id}” has a harness with surrounding whitespace.`
    }
    if (!card.instructions) return `Step “${card.id}” needs instructions.`
    // BYTES, NOT CHARACTERS. Go measures `len(n.Instructions)`, so a string of
    // multi-byte characters can be well under 8192 by `String.length` and over
    // it on the wire.
    const bytes = new TextEncoder().encode(card.instructions).length
    if (bytes > MAX_INSTRUCTIONS_BYTES) {
      return `Step “${card.id}” has ${bytes} bytes of instructions; the limit is ${MAX_INSTRUCTIONS_BYTES}.`
    }
    return null
  }
  if (card.harness) return `Only agentic steps take a harness; “${card.id}” is a ${card.type}.`
  if (card.instructions) {
    return `Only agentic steps take instructions; “${card.id}” is a ${card.type}.`
  }
  return null
}

/**
 * The gate's revision loop: an agentic target that DOMINATES the gate, and a
 * bounded round count.
 *
 * A REVIEW IS NEVER A LEGAL TARGET, and the reason is the bypass edge: the
 * implement→gate patch edge routes around the review, so the review does not
 * lie on every path to the gate and therefore never dominates it. Server-side
 * that is `validateRevisions`' `dom[n.ID][target]` check, and
 * `validate_test.go:249-255` is the test that pins it — its comment reads
 * "review does NOT dominate the gate: the impl→gate patch edge bypasses it."
 * On a linear outline "dominates" reduces to "is a plan or implement step",
 * which is the form this checks.
 *
 * IT SEARCHES THE WHOLE SEQUENCE, NOT THE PART BEFORE THE GATE, and that is
 * measured rather than careless. The first draft sliced to the preceding cards,
 * and the mutant that widened the slice to every card SURVIVED the whole suite
 * — because it cannot be distinguished. This function only ever runs after the
 * grammar walk has reached `published`, and that walk admits no plan or
 * implement after a gate (`TRANSITIONS.gated` accepts `git-publish` alone), so
 * every plan and implement in a sequence that gets this far already precedes
 * the gate. Slicing re-checked something the grammar had already guaranteed. A
 * line no fixture can distinguish is a line to delete, not to keep for comfort
 * — the same call `workflows-query.ts` records about its array guard.
 */
function revisionViolation(card: Card, cards: readonly Card[]): string | null {
  const revision = card.revision
  if (revision === undefined) return null
  if (card.type !== 'gate') {
    return `Only a gate carries a revision loop; “${card.id}” is a ${card.type}.`
  }
  if (revision.maxRounds < 1 || revision.maxRounds > MAX_REVISION_ROUNDS) {
    return `The revision loop on “${card.id}” must run between 1 and ${MAX_REVISION_ROUNDS} rounds.`
  }
  const target = cards.find((c) => c.id === revision.target)
  if (target === undefined || (target.type !== 'plan' && target.type !== 'implement')) {
    return `The revision loop on “${card.id}” targets “${revision.target}”, which is not a plan or implement step before it.`
  }
  return null
}

/**
 * Which types may be inserted at `index` and still leave a completable outline.
 *
 * THE SAME JUDGEMENT AS `prefixViolation`, ASKED POSITIONALLY — and it is
 * implemented by asking it, so the two cannot drift apart. The relation is
 * exactly `T ∈ legalTypesAt(cards, i) ⟺ prefixViolation(insert(cards, i, T)) === null`.
 * (Which is also why the test file pins this function's CONTENT rather than
 * asserting that relation: under this implementation the relation holds for
 * every implementation of `prefixViolation`, correct or broken, so asserting it
 * would discriminate nothing.)
 *
 * The card it inserts to ask is a probe, never returned; only its type is read.
 */
export function legalTypesAt(cards: readonly Card[], index: number): CardType[] {
  return CARD_TYPES.filter((type) => {
    const probe: Card = { id: 'probe', type }
    return prefixViolation([...cards.slice(0, index), probe, ...cards.slice(index)]) === null
  })
}

/** The id stems, matching `validate_test.go:10-28`'s `conveyor()` so TS and Go fixtures read alike. */
const ID_STEM: Record<CardType, string> = {
  plan: 'plan',
  implement: 'impl',
  review: 'review',
  gate: 'gate',
  'git-publish': 'publish',
}

/**
 * The next free auto id for a type: `impl-1`, `impl-2`, …
 *
 * SMALLEST FREE, NOT ONE PAST THE LAST, and it checks ids of EVERY type: node
 * ids are unique across the whole document (`validateShape`'s duplicate check
 * is not per-type), and a card the user renamed by hand can be sitting on any
 * name at all. So a renamed-away `impl-1` is reused and a hand-typed `impl-2`
 * is stepped over.
 */
export function nextCardId(cards: readonly Card[], type: CardType): string {
  const taken = new Set(cards.map((c) => c.id))
  const stem = ID_STEM[type]
  for (let n = 1; ; n++) {
    const candidate = `${stem}-${n}`
    if (!taken.has(candidate)) return candidate
  }
}

/**
 * Does this document round-trip through the editor unchanged?
 *
 * `true` means the outline editor can open it: its nodes form a legal V1
 * sequence AND re-deriving the edges from that sequence reproduces exactly the
 * edge set the document carries. `false` sends the reader to the read-only
 * "authored outside the editor" view — a reachable state, not a theoretical
 * one: a lone plan node, two gates in sequence and a branching
 * plan→{implement, implement} document all pass the server's `Validate`
 * (measured, 0 violations each) and none of them is editable here.
 *
 * SET EQUALITY, ORDER-INSENSITIVE. Emission order is version identity when this
 * module WRITES a document, but a document being READ was written by someone
 * else and its order carries no meaning — an API author who listed the same
 * bindings in another order authored the same graph, and refusing to open it
 * would be this module mistaking its own convention for a rule.
 *
 * NO `schema` IN THE PARAMETER. Chain-shapedness is a property of the graph,
 * not of the schema bump: schema 2 differs from 1 in how agentic nodes are
 * validated against payload schemas and "the DOCUMENT shape is unchanged"
 * (`definition.go:20-27`), so a v1 document is editable on exactly the same
 * terms.
 */
export function isChainShaped(doc: {
  nodes: readonly DocNode[]
  edges?: readonly DocEdge[]
}): boolean {
  const cards = doc.nodes.map(toCard)
  if (grammarViolation(cards) !== null) return false
  const derived = deriveEdges(cards)
  const actual = doc.edges ?? []
  if (derived.length !== actual.length) return false
  const key = (e: DocEdge) => `${e.from} ${e.to} ${e.artifact}`
  const want = new Set(derived.map(key))
  return actual.every((e) => want.has(key(e)))
}

/** A wire node as a card — the inverse of `toNode`, including the snake_case conversion. */
function toCard(node: DocNode): Card {
  return {
    id: node.id,
    type: node.type,
    ...(node.harness === undefined ? {} : { harness: node.harness }),
    ...(node.instructions === undefined ? {} : { instructions: node.instructions }),
    ...(node.on_request_changes === undefined
      ? {}
      : {
          revision: {
            target: node.on_request_changes.target,
            maxRounds: node.on_request_changes.max_rounds,
          },
        }),
  }
}
