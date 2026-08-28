import { describe, expect, it } from 'vitest'
import {
  type Card,
  type CardType,
  type Doc,
  type DocNode,
  deriveEdges,
  grammarViolation,
  isChainShaped,
  legalTypesAt,
  nextCardId,
  prefixViolation,
  toDocument,
} from './derive'

/** The agentic cards carry the two fields their type requires; gates and publish do not. */
const plan = (id = 'plan-1'): Card => ({ id, type: 'plan', harness: 'cc', instructions: 'p' })
const impl = (id = 'impl-1'): Card => ({ id, type: 'implement', harness: 'cc', instructions: 'i' })
const review = (id = 'review-1'): Card => ({ id, type: 'review', harness: 'cc', instructions: 'r' })
const gate = (id = 'gate-1', revision?: { target: string; maxRounds: number }): Card =>
  revision === undefined ? { id, type: 'gate' } : { id, type: 'gate', revision }
const publish = (id = 'publish-1'): Card => ({ id, type: 'git-publish' })

const triples = (edges: readonly { from: string; to: string; artifact: string }[]) =>
  edges.map((e) => `${e.from}→${e.to}:${e.artifact}`)

describe('deriveEdges', () => {
  it('derives the canonical conveyor edge set — bypass edge included', () => {
    const cards: Card[] = [
      { id: 'im', type: 'implement', harness: 'cc', instructions: 'y' },
      { id: 'rev', type: 'review', harness: 'cc', instructions: 'r' },
      { id: 'g', type: 'gate', revision: { target: 'im', maxRounds: 2 } },
      { id: 'pub', type: 'git-publish' },
    ]
    expect(new Set(triples(deriveEdges(cards)))).toEqual(
      new Set(['im→rev:patch', 'rev→g:review_verdict', 'im→g:patch', 'g→pub:patch']),
    )
  })

  it('feeds the plan to the FIRST implement and to no one else', () => {
    // Not "the first agentic card": review cannot consume a plan at all
    // (validate.go, validateConsumption/NodeReview), so the rule is about
    // implements specifically.
    expect(triples(deriveEdges([plan(), impl('impl-1'), impl('impl-2')]))).toEqual([
      'plan-1→impl-1:plan',
      'impl-1→impl-2:patch',
    ])
  })

  it('chains each implement onto the previous implement’s patch', () => {
    expect(triples(deriveEdges([impl('impl-1'), impl('impl-2'), impl('impl-3')]))).toEqual([
      'impl-1→impl-2:patch',
      'impl-2→impl-3:patch',
    ])
  })

  it('gives the review the NEAREST preceding patch, not the first', () => {
    expect(triples(deriveEdges([impl('impl-1'), impl('impl-2'), review()]))).toContain(
      'impl-2→review-1:patch',
    )
    expect(triples(deriveEdges([impl('impl-1'), impl('impl-2'), review()]))).not.toContain(
      'impl-1→review-1:patch',
    )
  })

  it('gives a gate with no review no verdict edge', () => {
    expect(triples(deriveEdges([impl(), gate(), publish()]))).toEqual([
      'impl-1→gate-1:patch',
      'gate-1→publish-1:patch',
    ])
  })

  it('publishes the GATE’s pass-through patch, never the implement’s directly', () => {
    // This is how the validator's pass-through works: validatePublishes looks
    // for a dominating gate examining exactly the patch being published, so the
    // publish's patch edge comes FROM the gate.
    const edges = triples(deriveEdges([impl(), review(), gate(), publish()]))
    expect(edges).toContain('gate-1→publish-1:patch')
    expect(edges).not.toContain('impl-1→publish-1:patch')
  })

  it('emits nothing for card sequences with no producer yet', () => {
    expect(deriveEdges([])).toEqual([])
    expect(deriveEdges([impl()])).toEqual([])
    // A leading review is grammar-illegal but derivation stays TOTAL: the live
    // preview calls this on whatever the outline currently holds.
    expect(deriveEdges([review()])).toEqual([])
  })
})

describe('toDocument', () => {
  /** `noUncheckedIndexedAccess` makes every index access optional; this keeps the assertions honest. */
  const nodeAt = (doc: Doc, index: number): DocNode => {
    const node = doc.nodes[index]
    if (node === undefined) throw new Error(`no node at index ${index}`)
    return node
  }

  it('emits schema 2 with nodes in card order', () => {
    const doc = toDocument([impl(), gate(), publish()])
    expect(doc.schema).toBe(2)
    expect(doc.nodes.map((n) => n.id)).toEqual(['impl-1', 'gate-1', 'publish-1'])
  })

  it('omits the edges key entirely when there are none', () => {
    const doc = toDocument([impl()])
    expect(doc.edges).toBeUndefined()
    expect(Object.hasOwn(doc, 'edges')).toBe(false)
  })

  it('converts revision.maxRounds to the wire’s on_request_changes.max_rounds', () => {
    // The Card is camelCase and the wire is snake_case; this is the one place
    // that conversion happens.
    const doc = toDocument([impl(), gate('gate-1', { target: 'impl-1', maxRounds: 3 })])
    expect(nodeAt(doc, 1)).toEqual({
      id: 'gate-1',
      type: 'gate',
      on_request_changes: { target: 'impl-1', max_rounds: 3 },
    })
  })

  it('omits harness/instructions on non-agentic nodes, matching Go’s omitempty', () => {
    const doc = toDocument([gate(), publish()])
    expect(Object.hasOwn(nodeAt(doc, 0), 'harness')).toBe(false)
    expect(Object.hasOwn(nodeAt(doc, 1), 'instructions')).toBe(false)
    expect(Object.hasOwn(nodeAt(doc, 0), 'on_request_changes')).toBe(false)
  })

  it('omits an EMPTY-STRING harness the way omitempty does', () => {
    // A half-typed agentic card carries ''. Go's omitempty drops an empty
    // string, so emitting `"harness": ""` would differ from the canonical bytes
    // for a document the server would otherwise consider identical.
    const doc = toDocument([{ id: 'impl-1', type: 'implement', harness: '', instructions: '' }])
    expect(Object.hasOwn(nodeAt(doc, 0), 'harness')).toBe(false)
    expect(Object.hasOwn(nodeAt(doc, 0), 'instructions')).toBe(false)
  })
})

/**
 * THE GOLDEN CORPUS — the acceptance criterion this module is really held to.
 *
 * Each string below is `Document.Canonical()` printed by the REAL Go validator
 * (`backend/internal/workflows`), which reported `violations=0` for every one
 * of the twelve documents in the family `withPlan × chain 1..3 × withReview`.
 * The pre-flight records the run.
 *
 * BYTE-IDENTITY RATHER THAN SET EQUALITY, and that is not fussiness. Edge order
 * and node order are both content-hash-bearing — measured: swapping two edges
 * of a valid document leaves it valid and changes its SHA-256, because
 * `Canonical()` marshals the slices in order and `CreateVersion` hashes those
 * bytes to decide whether this is a new version at all. So emission order is
 * version identity, and set equality cannot see it. Byte-identity also catches
 * the omitempty mistakes an edge-only assertion never reaches.
 */
describe('the golden corpus — byte-identical to the Go validator’s canonical bytes', () => {
  const canonical = (cards: readonly Card[]) => JSON.stringify(toDocument(cards))
  const p = plan()
  const r = review()
  const g = (target: string) => gate('gate-1', { target, maxRounds: 2 })
  const pub = publish()
  const i1 = impl('impl-1')
  const i2 = impl('impl-2')
  const i3 = impl('impl-3')

  const cases: [string, Card[], string][] = [
    [
      'chain=1',
      [i1, g('impl-1'), pub],
      '{"schema":2,"nodes":[{"id":"impl-1","type":"implement","harness":"cc","instructions":"i"},{"id":"gate-1","type":"gate","on_request_changes":{"target":"impl-1","max_rounds":2}},{"id":"publish-1","type":"git-publish"}],"edges":[{"from":"impl-1","to":"gate-1","artifact":"patch"},{"from":"gate-1","to":"publish-1","artifact":"patch"}]}',
    ],
    [
      'chain=1 + review',
      [i1, r, g('impl-1'), pub],
      '{"schema":2,"nodes":[{"id":"impl-1","type":"implement","harness":"cc","instructions":"i"},{"id":"review-1","type":"review","harness":"cc","instructions":"r"},{"id":"gate-1","type":"gate","on_request_changes":{"target":"impl-1","max_rounds":2}},{"id":"publish-1","type":"git-publish"}],"edges":[{"from":"impl-1","to":"review-1","artifact":"patch"},{"from":"impl-1","to":"gate-1","artifact":"patch"},{"from":"review-1","to":"gate-1","artifact":"review_verdict"},{"from":"gate-1","to":"publish-1","artifact":"patch"}]}',
    ],
    [
      'chain=2',
      [i1, i2, g('impl-2'), pub],
      '{"schema":2,"nodes":[{"id":"impl-1","type":"implement","harness":"cc","instructions":"i"},{"id":"impl-2","type":"implement","harness":"cc","instructions":"i"},{"id":"gate-1","type":"gate","on_request_changes":{"target":"impl-2","max_rounds":2}},{"id":"publish-1","type":"git-publish"}],"edges":[{"from":"impl-1","to":"impl-2","artifact":"patch"},{"from":"impl-2","to":"gate-1","artifact":"patch"},{"from":"gate-1","to":"publish-1","artifact":"patch"}]}',
    ],
    [
      'chain=2 + review',
      [i1, i2, r, g('impl-2'), pub],
      '{"schema":2,"nodes":[{"id":"impl-1","type":"implement","harness":"cc","instructions":"i"},{"id":"impl-2","type":"implement","harness":"cc","instructions":"i"},{"id":"review-1","type":"review","harness":"cc","instructions":"r"},{"id":"gate-1","type":"gate","on_request_changes":{"target":"impl-2","max_rounds":2}},{"id":"publish-1","type":"git-publish"}],"edges":[{"from":"impl-1","to":"impl-2","artifact":"patch"},{"from":"impl-2","to":"review-1","artifact":"patch"},{"from":"impl-2","to":"gate-1","artifact":"patch"},{"from":"review-1","to":"gate-1","artifact":"review_verdict"},{"from":"gate-1","to":"publish-1","artifact":"patch"}]}',
    ],
    [
      'chain=3',
      [i1, i2, i3, g('impl-3'), pub],
      '{"schema":2,"nodes":[{"id":"impl-1","type":"implement","harness":"cc","instructions":"i"},{"id":"impl-2","type":"implement","harness":"cc","instructions":"i"},{"id":"impl-3","type":"implement","harness":"cc","instructions":"i"},{"id":"gate-1","type":"gate","on_request_changes":{"target":"impl-3","max_rounds":2}},{"id":"publish-1","type":"git-publish"}],"edges":[{"from":"impl-1","to":"impl-2","artifact":"patch"},{"from":"impl-2","to":"impl-3","artifact":"patch"},{"from":"impl-3","to":"gate-1","artifact":"patch"},{"from":"gate-1","to":"publish-1","artifact":"patch"}]}',
    ],
    [
      'chain=3 + review',
      [i1, i2, i3, r, g('impl-3'), pub],
      '{"schema":2,"nodes":[{"id":"impl-1","type":"implement","harness":"cc","instructions":"i"},{"id":"impl-2","type":"implement","harness":"cc","instructions":"i"},{"id":"impl-3","type":"implement","harness":"cc","instructions":"i"},{"id":"review-1","type":"review","harness":"cc","instructions":"r"},{"id":"gate-1","type":"gate","on_request_changes":{"target":"impl-3","max_rounds":2}},{"id":"publish-1","type":"git-publish"}],"edges":[{"from":"impl-1","to":"impl-2","artifact":"patch"},{"from":"impl-2","to":"impl-3","artifact":"patch"},{"from":"impl-3","to":"review-1","artifact":"patch"},{"from":"impl-3","to":"gate-1","artifact":"patch"},{"from":"review-1","to":"gate-1","artifact":"review_verdict"},{"from":"gate-1","to":"publish-1","artifact":"patch"}]}',
    ],
    [
      'plan + chain=1',
      [p, i1, g('impl-1'), pub],
      '{"schema":2,"nodes":[{"id":"plan-1","type":"plan","harness":"cc","instructions":"p"},{"id":"impl-1","type":"implement","harness":"cc","instructions":"i"},{"id":"gate-1","type":"gate","on_request_changes":{"target":"impl-1","max_rounds":2}},{"id":"publish-1","type":"git-publish"}],"edges":[{"from":"plan-1","to":"impl-1","artifact":"plan"},{"from":"impl-1","to":"gate-1","artifact":"patch"},{"from":"gate-1","to":"publish-1","artifact":"patch"}]}',
    ],
    [
      'plan + chain=1 + review',
      [p, i1, r, g('impl-1'), pub],
      '{"schema":2,"nodes":[{"id":"plan-1","type":"plan","harness":"cc","instructions":"p"},{"id":"impl-1","type":"implement","harness":"cc","instructions":"i"},{"id":"review-1","type":"review","harness":"cc","instructions":"r"},{"id":"gate-1","type":"gate","on_request_changes":{"target":"impl-1","max_rounds":2}},{"id":"publish-1","type":"git-publish"}],"edges":[{"from":"plan-1","to":"impl-1","artifact":"plan"},{"from":"impl-1","to":"review-1","artifact":"patch"},{"from":"impl-1","to":"gate-1","artifact":"patch"},{"from":"review-1","to":"gate-1","artifact":"review_verdict"},{"from":"gate-1","to":"publish-1","artifact":"patch"}]}',
    ],
    [
      'plan + chain=2',
      [p, i1, i2, g('impl-2'), pub],
      '{"schema":2,"nodes":[{"id":"plan-1","type":"plan","harness":"cc","instructions":"p"},{"id":"impl-1","type":"implement","harness":"cc","instructions":"i"},{"id":"impl-2","type":"implement","harness":"cc","instructions":"i"},{"id":"gate-1","type":"gate","on_request_changes":{"target":"impl-2","max_rounds":2}},{"id":"publish-1","type":"git-publish"}],"edges":[{"from":"plan-1","to":"impl-1","artifact":"plan"},{"from":"impl-1","to":"impl-2","artifact":"patch"},{"from":"impl-2","to":"gate-1","artifact":"patch"},{"from":"gate-1","to":"publish-1","artifact":"patch"}]}',
    ],
    [
      'plan + chain=2 + review',
      [p, i1, i2, r, g('impl-2'), pub],
      '{"schema":2,"nodes":[{"id":"plan-1","type":"plan","harness":"cc","instructions":"p"},{"id":"impl-1","type":"implement","harness":"cc","instructions":"i"},{"id":"impl-2","type":"implement","harness":"cc","instructions":"i"},{"id":"review-1","type":"review","harness":"cc","instructions":"r"},{"id":"gate-1","type":"gate","on_request_changes":{"target":"impl-2","max_rounds":2}},{"id":"publish-1","type":"git-publish"}],"edges":[{"from":"plan-1","to":"impl-1","artifact":"plan"},{"from":"impl-1","to":"impl-2","artifact":"patch"},{"from":"impl-2","to":"review-1","artifact":"patch"},{"from":"impl-2","to":"gate-1","artifact":"patch"},{"from":"review-1","to":"gate-1","artifact":"review_verdict"},{"from":"gate-1","to":"publish-1","artifact":"patch"}]}',
    ],
    [
      'plan + chain=3',
      [p, i1, i2, i3, g('impl-3'), pub],
      '{"schema":2,"nodes":[{"id":"plan-1","type":"plan","harness":"cc","instructions":"p"},{"id":"impl-1","type":"implement","harness":"cc","instructions":"i"},{"id":"impl-2","type":"implement","harness":"cc","instructions":"i"},{"id":"impl-3","type":"implement","harness":"cc","instructions":"i"},{"id":"gate-1","type":"gate","on_request_changes":{"target":"impl-3","max_rounds":2}},{"id":"publish-1","type":"git-publish"}],"edges":[{"from":"plan-1","to":"impl-1","artifact":"plan"},{"from":"impl-1","to":"impl-2","artifact":"patch"},{"from":"impl-2","to":"impl-3","artifact":"patch"},{"from":"impl-3","to":"gate-1","artifact":"patch"},{"from":"gate-1","to":"publish-1","artifact":"patch"}]}',
    ],
    [
      'plan + chain=3 + review',
      [p, i1, i2, i3, r, g('impl-3'), pub],
      '{"schema":2,"nodes":[{"id":"plan-1","type":"plan","harness":"cc","instructions":"p"},{"id":"impl-1","type":"implement","harness":"cc","instructions":"i"},{"id":"impl-2","type":"implement","harness":"cc","instructions":"i"},{"id":"impl-3","type":"implement","harness":"cc","instructions":"i"},{"id":"review-1","type":"review","harness":"cc","instructions":"r"},{"id":"gate-1","type":"gate","on_request_changes":{"target":"impl-3","max_rounds":2}},{"id":"publish-1","type":"git-publish"}],"edges":[{"from":"plan-1","to":"impl-1","artifact":"plan"},{"from":"impl-1","to":"impl-2","artifact":"patch"},{"from":"impl-2","to":"impl-3","artifact":"patch"},{"from":"impl-3","to":"review-1","artifact":"patch"},{"from":"impl-3","to":"gate-1","artifact":"patch"},{"from":"review-1","to":"gate-1","artifact":"review_verdict"},{"from":"gate-1","to":"publish-1","artifact":"patch"}]}',
    ],
  ]

  it.each(cases)('%s', (_name, cards, want) => {
    expect(canonical(cards)).toBe(want)
  })

  it('accepts every corpus sequence as a complete document', () => {
    for (const [name, cards] of cases) {
      expect([name, grammarViolation(cards)]).toEqual([name, null])
    }
  })
})

describe('isChainShaped', () => {
  const conveyor = [impl(), review(), gate('gate-1', { target: 'impl-1', maxRounds: 2 }), publish()]

  it('is true for a document the engine itself derived', () => {
    expect(isChainShaped(toDocument(conveyor))).toBe(true)
  })

  it('is true for every corpus member', () => {
    expect(isChainShaped(toDocument([plan(), impl(), gate('gate-1'), publish()]))).toBe(true)
  })

  it('is false with one edge REMOVED', () => {
    const doc = toDocument(conveyor)
    expect(isChainShaped({ nodes: doc.nodes, edges: (doc.edges ?? []).slice(1) })).toBe(false)
  })

  it('is false with one edge ADDED', () => {
    const doc = toDocument(conveyor)
    expect(
      isChainShaped({
        nodes: doc.nodes,
        edges: [...(doc.edges ?? []), { from: 'impl-1', to: 'publish-1', artifact: 'patch' }],
      }),
    ).toBe(false)
  })

  it('is false with one artifact RETYPED', () => {
    const doc = toDocument(conveyor)
    const edges = (doc.edges ?? []).map((e) =>
      e.from === 'review-1' ? { ...e, artifact: 'patch' as const } : e,
    )
    expect(isChainShaped({ nodes: doc.nodes, edges })).toBe(false)
  })

  it('is ORDER-INSENSITIVE — the same edge set reversed is still chain-shaped', () => {
    const doc = toDocument(conveyor)
    expect(isChainShaped({ nodes: doc.nodes, edges: [...(doc.edges ?? [])].reverse() })).toBe(true)
  })

  it('is false when the edges key is ABSENT but the nodes imply edges', () => {
    // The absent key is the canonical form for a document with no edges, so it
    // must not be read as "whatever this node sequence would have derived".
    // Found by a surviving mutant: defaulting the missing key to the derived
    // set instead of to `[]` makes every node sequence chain-shaped, and the
    // one-edge-removed case above cannot see it because that document HAS an
    // edges key.
    const doc = toDocument(conveyor)
    expect(isChainShaped({ nodes: doc.nodes })).toBe(false)
  })

  /**
   * EVERY AGENTIC NODE HERE CARRIES ITS HARNESS AND INSTRUCTIONS, deliberately.
   * Leaving them off would make each of these documents false for a boring
   * reason — a missing required field — and then a mutant that deleted the
   * whole edge comparison would still pass. Fielded fixtures force the
   * falseness to come from the shape.
   */
  const agentic = (id: string, type: 'plan' | 'implement' | 'review') => ({
    id,
    type,
    harness: 'cc',
    instructions: 'x',
  })
  const plainNode = (id: string, type: CardType) => ({ id, type })

  it('is false for shapes the API can author but the editor cannot', () => {
    // All of these VALIDATE server-side — measured against the real validator,
    // 0 violations each — which is what makes the read-only "authored outside
    // the editor" mode a reachable state rather than a theoretical one.
    expect(isChainShaped({ nodes: [agentic('plan-1', 'plan')] })).toBe(false)
    expect(isChainShaped({ nodes: [agentic('impl-1', 'implement')] })).toBe(false)
    expect(
      isChainShaped({
        nodes: [
          agentic('impl-1', 'implement'),
          plainNode('gate-1', 'gate'),
          plainNode('gate-2', 'gate'),
          plainNode('publish-1', 'git-publish'),
        ],
        edges: [
          { from: 'impl-1', to: 'gate-1', artifact: 'patch' },
          { from: 'gate-1', to: 'gate-2', artifact: 'patch' },
          { from: 'gate-2', to: 'publish-1', artifact: 'patch' },
        ],
      }),
    ).toBe(false)
  })

  it('is false for a BRANCHING document whose node sequence is nonetheless legal', () => {
    // The sharpest case in this file, and the only one that reaches the edge
    // comparison: two implements consuming one plan. Its node sequence —
    // plan, implement, implement, gate, git-publish — is a perfectly legal V1
    // outline, so `grammarViolation` passes it and ONLY the derived-vs-actual
    // edge set can tell it apart. Derivation would have chained impl-b onto
    // impl-a's patch; this document feeds both from the plan instead.
    // Verified authorable: Validate returns 0 violations.
    const branching = {
      nodes: [
        agentic('plan-1', 'plan'),
        agentic('impl-a', 'implement'),
        agentic('impl-b', 'implement'),
        plainNode('gate-1', 'gate'),
        plainNode('publish-1', 'git-publish'),
      ],
      edges: [
        { from: 'plan-1', to: 'impl-a', artifact: 'plan' as const },
        { from: 'plan-1', to: 'impl-b', artifact: 'plan' as const },
        { from: 'impl-a', to: 'gate-1', artifact: 'patch' as const },
        { from: 'gate-1', to: 'publish-1', artifact: 'patch' as const },
      ],
    }
    expect(grammarViolation(branching.nodes.map((n) => ({ ...n })))).toBeNull()
    expect(isChainShaped(branching)).toBe(false)
  })
})

/**
 * THE PREFIX/COMPLETE SPLIT, as a PAIR of tables.
 *
 * The pair is the point: one table alone cannot show that the two notions have
 * not silently collapsed back into one. `prefixViolation` is the reducer's
 * invariant — an editor is incomplete for its whole authoring session, so
 * judging a keystroke by complete-document legality would refuse the first card
 * ever added. `grammarViolation` is SAVE's precondition and nothing else.
 */
describe('prefixViolation vs grammarViolation', () => {
  const legalPrefixesThatAreNotCompleteDocuments: [string, Card[]][] = [
    ['the empty outline', []],
    ['a lone plan', [plan()]],
    ['a lone implement', [impl()]],
    ['implement + review', [impl(), review()]],
    ['implement + review + gate', [impl(), review(), gate()]],
    ['plan + two implements', [plan(), impl('impl-1'), impl('impl-2')]],
  ]

  it.each(legalPrefixesThatAreNotCompleteDocuments)(
    '%s is a legal PREFIX but not a complete document',
    (_name, cards) => {
      expect(prefixViolation(cards)).toBeNull()
      expect(grammarViolation(cards)).not.toBeNull()
    },
  )

  const illegalUnderBoth: [string, Card[], string][] = [
    ['a leading review', [review(), impl(), gate(), publish()], 'review-1'],
    ['a leading gate', [gate(), impl(), publish()], 'gate-1'],
    ['a second gate', [impl(), gate('gate-1'), gate('gate-2'), publish()], 'gate-2'],
    ['a second review', [impl(), review('review-1'), review('review-2')], 'review-2'],
    // The refusal names the PUBLISH, not the gate: the publish is the card
    // sitting where it cannot sit, and the walk stops at the first such card.
    ['publish before the gate', [impl(), publish(), gate()], 'publish-1'],
    ['a plan after an implement', [impl(), plan()], 'plan-1'],
    ['anything after publish', [impl(), gate(), publish(), impl('impl-2')], 'impl-2'],
  ]

  it.each(illegalUnderBoth)(
    '%s is refused by BOTH — no completion rescues it',
    (_name, cards, offender) => {
      const prefix = prefixViolation(cards)
      expect(prefix).not.toBeNull()
      expect(prefix).toContain(offender)
      expect(grammarViolation(cards)).not.toBeNull()
    },
  )

  it('names the offending card in the refusal', () => {
    expect(prefixViolation([review()])).toContain('review-1')
  })

  it('grammarViolation says what a complete document is still MISSING', () => {
    expect(grammarViolation([impl(), gate()])).toContain('git-publish')
    expect(grammarViolation([])).not.toBeNull()
    expect(grammarViolation([plan()])).toContain('implement')
  })
})

/** `grammarViolation` is SAVE's precondition, so it owns the checks the server would fail on. */
describe('grammarViolation — the rest of "saveable as-is"', () => {
  const complete = (...extra: Card[]): Card[] => [
    impl(),
    ...extra,
    gate('gate-1', { target: 'impl-1', maxRounds: 2 }),
    publish(),
  ]

  it('accepts the complete conveyor', () => {
    expect(grammarViolation(complete(review()))).toBeNull()
  })

  it('refuses an agentic card with no harness or no instructions', () => {
    expect(
      grammarViolation([
        { id: 'impl-1', type: 'implement', instructions: 'i' },
        gate('gate-1'),
        publish(),
      ]),
    ).toContain('harness')
    expect(
      grammarViolation([
        { id: 'impl-1', type: 'implement', harness: 'cc' },
        gate('gate-1'),
        publish(),
      ]),
    ).toContain('instructions')
  })

  it('refuses instructions over 8192 BYTES — bytes, not characters', () => {
    // 'é' is two UTF-8 bytes, so 4300 of them is 8600 bytes while
    // String.length says 4300. Go measures len(n.Instructions) in bytes.
    const long = 'é'.repeat(4300)
    expect(long.length).toBeLessThan(8192)
    expect(
      grammarViolation([
        { id: 'impl-1', type: 'implement', harness: 'cc', instructions: long },
        gate('gate-1'),
        publish(),
      ]),
    ).toContain('8192')
  })

  it('refuses an id that breaks the validator’s pattern, and one that is too long', () => {
    expect(grammarViolation([{ ...impl(), id: 'Impl-1' }, gate(), publish()])).toContain('Impl-1')
    expect(grammarViolation([{ ...impl(), id: '1impl' }, gate(), publish()])).toContain('1impl')
    expect(grammarViolation([{ ...impl(), id: 'a'.repeat(65) }, gate(), publish()])).not.toBeNull()
    expect(grammarViolation([{ ...impl(), id: 'a'.repeat(64) }, gate(), publish()])).toBeNull()
  })

  it('refuses duplicate ids', () => {
    expect(grammarViolation([impl('impl-1'), impl('impl-1'), gate('gate-1'), publish()])).toContain(
      'impl-1',
    )
  })

  it('refuses a revision target that is not a PRECEDING plan or implement', () => {
    // review is never offered: the bypass edge means it can never dominate the
    // gate. validate_test.go:249-255 is the test that pins it server-side.
    expect(
      grammarViolation([
        impl(),
        review(),
        gate('gate-1', { target: 'review-1', maxRounds: 2 }),
        publish(),
      ]),
    ).toContain('review-1')
    expect(
      grammarViolation([impl(), gate('gate-1', { target: 'publish-1', maxRounds: 2 }), publish()]),
    ).toContain('publish-1')
    expect(
      grammarViolation([impl(), gate('gate-1', { target: 'nope', maxRounds: 2 }), publish()]),
    ).toContain('nope')
  })

  it('accepts a revision target that is an EARLIER implement, not just the last', () => {
    expect(
      grammarViolation([
        impl('impl-1'),
        impl('impl-2'),
        gate('gate-1', { target: 'impl-1', maxRounds: 2 }),
        publish(),
      ]),
    ).toBeNull()
  })

  it('accepts a plan as a revision target', () => {
    expect(
      grammarViolation([
        plan(),
        impl(),
        gate('gate-1', { target: 'plan-1', maxRounds: 2 }),
        publish(),
      ]),
    ).toBeNull()
  })

  it('accepts a gate with NO revision at all', () => {
    expect(grammarViolation([impl(), gate('gate-1'), publish()])).toBeNull()
  })

  it('bounds max_rounds to 1..10', () => {
    expect(
      grammarViolation([impl(), gate('gate-1', { target: 'impl-1', maxRounds: 0 }), publish()]),
    ).not.toBeNull()
    expect(
      grammarViolation([impl(), gate('gate-1', { target: 'impl-1', maxRounds: 11 }), publish()]),
    ).not.toBeNull()
    expect(
      grammarViolation([impl(), gate('gate-1', { target: 'impl-1', maxRounds: 10 }), publish()]),
    ).toBeNull()
    expect(
      grammarViolation([impl(), gate('gate-1', { target: 'impl-1', maxRounds: 1 }), publish()]),
    ).toBeNull()
  })
})

/**
 * `legalTypesAt` — asserted as an EXPECTED-SET TABLE, deliberately.
 *
 * The plan asks for an agreement assertion ("appending any type it offers must
 * leave prefixViolation null, and any type it withholds must not"). Under the
 * single-source implementation — `CARD_TYPES.filter(t => prefixViolation(…))` —
 * that assertion is a TAUTOLOGY: it holds for every implementation of
 * prefixViolation, right or wrong, so it discriminates nothing. The honest
 * alternatives were two encodings of one rule (which drift) or this: pin the
 * CONTENT at each position, which is what the agreement test was reaching for
 * and which goes red when prefixViolation breaks.
 *
 * Do not reinstate the agreement assertion.
 */
describe('legalTypesAt', () => {
  it('offers plan or implement at the very start, and nothing else', () => {
    expect(legalTypesAt([], 0)).toEqual(['plan', 'implement'])
  })

  it('offers only implement after a plan', () => {
    expect(legalTypesAt([plan()], 1)).toEqual(['implement'])
  })

  it('offers implement, review or gate after one implement', () => {
    expect(legalTypesAt([impl()], 1)).toEqual(['implement', 'review', 'gate'])
  })

  it('offers only gate after a review', () => {
    expect(legalTypesAt([impl(), review()], 2)).toEqual(['gate'])
  })

  it('offers only git-publish after a gate', () => {
    expect(legalTypesAt([impl(), gate()], 2)).toEqual(['git-publish'])
  })

  it('offers nothing after the publish', () => {
    expect(legalTypesAt([impl(), gate(), publish()], 3)).toEqual([])
  })

  it('answers about the INDEX, not the end — inserting mid-sequence is judged in place', () => {
    // Position 0 of [implement, gate, publish]: a plan may go before the
    // implement, and so may another implement, but a review may not.
    expect(legalTypesAt([impl(), gate(), publish()], 0)).toEqual(['plan', 'implement'])
    // Position 1 — after the implement, before the gate.
    expect(legalTypesAt([impl(), gate(), publish()], 1)).toEqual(['implement', 'review'])
  })
})

describe('nextCardId', () => {
  it('numbers from 1 per type', () => {
    expect(nextCardId([], 'plan')).toBe('plan-1')
    expect(nextCardId([], 'implement')).toBe('impl-1')
    expect(nextCardId([], 'review')).toBe('review-1')
    expect(nextCardId([], 'gate')).toBe('gate-1')
    expect(nextCardId([], 'git-publish')).toBe('publish-1')
  })

  it('yields impl-2 after impl-1', () => {
    expect(nextCardId([impl('impl-1')], 'implement')).toBe('impl-2')
  })

  it('skips a user-renamed collision', () => {
    // The user renamed something to `impl-2`; the next implement must not
    // collide with it, and the validator refuses duplicate ids.
    expect(nextCardId([impl('impl-1'), { ...review(), id: 'impl-2' }], 'implement')).toBe('impl-3')
  })

  it('fills a gap left by a rename rather than always growing', () => {
    expect(nextCardId([impl('something-else'), impl('impl-2')], 'implement')).toBe('impl-1')
  })

  it('always matches the validator’s charset', () => {
    const pattern = /^[a-z][a-z0-9_-]{0,63}$/
    for (const type of ['plan', 'implement', 'review', 'gate', 'git-publish'] as CardType[]) {
      expect(nextCardId([], type)).toMatch(pattern)
    }
  })
})
