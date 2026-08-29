import { describe, expect, it } from 'vitest'
import descriptor from '../../../backend/api/workflow-descriptor.json'
import { CARD_REGISTRY } from './card-registry'
import {
  CARD_TYPES,
  isAgentic,
  MAX_INSTRUCTIONS_BYTES,
  MAX_REVISION_ROUNDS,
  MIN_REVISION_ROUNDS,
  NODE_ID_PATTERN,
} from './derive'

/**
 * The §10.9 drift gate, web side.
 *
 * Locked outline B asked for "fields generated from the engine's schema". What
 * ships is HAND-AUTHORED forms plus this file: the editor's forms are typed
 * React, and `backend/api/workflow-descriptor.json` — emitted from
 * `definition.go` and convicted row by row against `Validate` in
 * `descriptor_test.go` — is what stops them drifting from the engine they
 * submit to.
 *
 * THE IMPORT REACHES OUT OF `web/`, deliberately: there is one descriptor and
 * the backend generates it, so copying it into `src/` would create a second
 * artifact to keep in step — the exact failure this file exists to prevent.
 * MEASURED that all four web lanes accept the path: `vitest run`, `tsc
 * --noEmit` (no `resolveJsonModule` needed — `moduleResolution: "bundler"`
 * implies it), `biome ci` and `tools/arch/main.ts`.
 *
 * THE JOIN IS ON THE WIRE NAME, never the label. The editor calls the gate's
 * field "revision" and the engine calls it `on_request_changes`; `CardSpec`
 * carries both. Joining on labels would fail here on a naming difference and
 * invite someone to "fix" it by renaming one side, losing the mapping the save
 * path needs.
 */

type Requirement = 'required' | 'forbidden' | 'optional'
type Rule = (typeof descriptor.rules)[number]

const cardTypes: readonly string[] = CARD_TYPES
const rulesFor = (type: string) => descriptor.rules.filter((r) => r.node_type === type)
const wiresOf = (type: keyof typeof CARD_REGISTRY) => CARD_REGISTRY[type].fields.map((f) => f.wire)
const requirementOf = (rule: Rule) => rule.requirement as Requirement

describe('the card registry covers the engine descriptor', () => {
  /**
   * The vocabularies are the same SET and the same ORDER. Order matters
   * because `CARD_TYPES` is the add-step menu's order (`legalTypesAt` filters
   * it), and the descriptor emits `NodeTypes()`, which is `definition.go`'s
   * spec order — so this also pins the menu to the spec's sequence.
   */
  it('knows exactly the node types the engine has', () => {
    expect(cardTypes).toEqual(descriptor.node_types)
    expect(Object.keys(CARD_REGISTRY).sort()).toEqual([...descriptor.node_types].sort())
  })

  /** `isAgentic` is a transcription of `NodeType.Agentic()`; this is the join. */
  it('agrees with the engine about which types are agentic', () => {
    for (const type of CARD_TYPES) {
      expect(isAgentic(type)).toBe(descriptor.agentic.includes(type))
    }
  })

  /**
   * ROWS → REGISTRY. A field the engine REQUIRES must have a control, or the
   * card cannot produce a document that saves; a field the engine FORBIDS must
   * not, or the card offers an input whose every value is a 400.
   */
  it('renders every required field and no forbidden one', () => {
    for (const type of CARD_TYPES) {
      const wires = wiresOf(type)
      for (const rule of rulesFor(type)) {
        if (requirementOf(rule) === 'required') {
          expect(wires, `${type}.${rule.wire_field} is required`).toContain(rule.wire_field)
        }
        if (requirementOf(rule) === 'forbidden') {
          expect(wires, `${type}.${rule.wire_field} is forbidden`).not.toContain(rule.wire_field)
        }
      }
    }
  })

  /**
   * REGISTRY → ROWS, and it is not the same assertion read backwards.
   *
   * The rules above fire only on `required` and `forbidden`. The gate's
   * `on_request_changes` is OPTIONAL — and it is the gate's ONLY registry row,
   * so without this direction the gate card's single field is checked by
   * nothing at all. This also catches the sharper case: a registry field whose
   * `wire` names a key `definition.go`'s `Node` does not have, which no pass
   * over the engine's rows can see, because there is no row.
   */
  it('carries no field the engine lacks, forbids, or treats as structural', () => {
    // `id` and `type` are the node's identity, not per-type configuration:
    // the card's id is edited by rename and its type by which card it is.
    const structural = ['id', 'type']
    for (const type of CARD_TYPES) {
      for (const field of CARD_REGISTRY[type].fields) {
        expect(descriptor.node_fields, `${type}.${field.name} → ${field.wire}`).toContain(
          field.wire,
        )
        expect(structural).not.toContain(field.wire)
        const rule = rulesFor(type).find((r) => r.wire_field === field.wire)
        expect(rule, `no descriptor rule for ${type}.${field.wire}`).toBeDefined()
        expect(rule && requirementOf(rule)).not.toBe('forbidden')
      }
    }
  })

  /**
   * The pattern the rename control validates with, compared as a STRING to the
   * one the engine publishes. This is a check only while each side holds
   * exactly one spelling of it — `NODE_ID_PATTERN` is that spelling here, and
   * `descriptorgen` reads `nodeIDPattern.String()` off the compiled regexp
   * rather than a copy of the literal.
   */
  it('validates node ids with the engine’s own pattern', () => {
    expect(NODE_ID_PATTERN.source).toBe(descriptor.id_pattern)
  })

  /**
   * WHERE TASK 27'S HARD-CODED 1–10 GETS HELD TO THE ENGINE. `editor-cards.tsx`
   * puts these two constants straight onto the rounds input's `min`/`max`, and
   * `revisionViolation` refuses outside them, so pinning the constants pins
   * both the control and the client-side refusal.
   */
  it('bounds the revision rounds where the engine bounds them', () => {
    const rule = rulesFor('gate').find((r) => r.wire_field === 'on_request_changes')
    expect(rule?.sub_field).toBe('max_rounds')
    expect(rule?.min_int).toBe(MIN_REVISION_ROUNDS)
    expect(rule?.max_int).toBe(MAX_REVISION_ROUNDS)
  })

  /**
   * The instructions cap, joined the same way — it drives the card's live byte
   * counter and `fieldViolation`'s refusal.
   *
   * THE HARNESS CAP HAS NO PARTNER HERE, and that is a fact about the editor
   * rather than an oversight. `validate.go` caps a harness at 100 bytes;
   * `derive.ts` checks a harness for presence and surrounding whitespace only.
   * Exporting a `MAX_HARNESS_BYTES` no control and no refusal consults would be
   * a constant with no production caller — a seam that makes its own test
   * vacuous. An over-long harness is refused by the server and lands on its
   * card through the positional 400 binding instead.
   */
  it('counts instruction bytes to the engine’s cap', () => {
    for (const type of CARD_TYPES.filter(isAgentic)) {
      const rule = rulesFor(type).find((r) => r.wire_field === 'instructions')
      expect(rule?.max_bytes, `${type}.instructions`).toBe(MAX_INSTRUCTIONS_BYTES)
    }
  })

  /**
   * The descriptor is a GENERATED file, and a generator that silently emitted
   * nothing would make every assertion above vacuously true — each one either
   * iterates `descriptor.rules` or looks a row up in it. This pins the grid's
   * size to the vocabulary it is built from.
   */
  it('describes every field of every type', () => {
    const nonStructural = descriptor.node_fields.filter((f) => f !== 'id' && f !== 'type')
    expect(nonStructural.length).toBeGreaterThan(0)
    expect(descriptor.rules).toHaveLength(descriptor.node_types.length * nonStructural.length)
  })
})
