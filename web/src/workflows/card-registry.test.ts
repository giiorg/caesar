import { describe, expect, it } from 'vitest'
import { CARD_REGISTRY } from './card-registry'
import { CARD_TYPES, isAgentic } from './derive'

const fieldNames = (type: keyof typeof CARD_REGISTRY) =>
  CARD_REGISTRY[type].fields.map((f) => f.name)

describe('CARD_REGISTRY', () => {
  /**
   * THE REGISTRY AND THE VALIDATOR'S FIELD RULE MUST AGREE, and this is the
   * join that makes them. `validateNodeConfig` — transcribed in `derive.ts`'s
   * `fieldViolation` — says an agentic node needs a harness AND instructions
   * and a non-agentic node may carry NEITHER. A registry row that disagreed
   * would render a card whose fields the server refuses, or leave a required
   * field with no control at all, and nothing else in this tree would notice
   * until a save came back 400.
   *
   * It iterates `CARD_TYPES` rather than the registry's own keys, so a sixth
   * node type added to the vocabulary arrives here as a failure rather than as
   * a row nobody checked.
   */
  it('gives exactly the agentic types a harness and instructions', () => {
    for (const type of CARD_TYPES) {
      const names = fieldNames(type)
      if (isAgentic(type)) {
        expect(names).toEqual(['harness', 'instructions'])
      } else {
        expect(names).not.toContain('harness')
        expect(names).not.toContain('instructions')
      }
    }
  })

  /**
   * THE GATE IS THE ONLY ROW WHERE THE TWO NAMES DIFFER, which is the entire
   * reason `wire` exists as a column. If every field's `wire` equalled its
   * `name`, the column would be dead weight and the next person would delete
   * it — taking the one mapping the save path needs with it.
   */
  it('is where the editor’s word and the engine’s key part company', () => {
    const differing = CARD_TYPES.flatMap((type) =>
      CARD_REGISTRY[type].fields.filter((f) => f.name !== f.wire).map((f) => [type, f]),
    )
    expect(differing).toEqual([['gate', { name: 'revision', wire: 'on_request_changes' }]])
  })

  /** A publish node takes no configuration at all — `validateNodeConfig` again. */
  it('gives git-publish no fields', () => {
    expect(CARD_REGISTRY['git-publish'].fields).toEqual([])
  })
})
