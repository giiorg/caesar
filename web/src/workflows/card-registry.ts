import type { CardType } from './derive'

/**
 * Every field carries its WIRE name — the JSON key on the engine's Node
 * (`definition.go:92-98`) — because the editor's label and the engine's key
 * differ for the gate: the card says "revision", the document says
 * "on_request_changes". Task 29's coverage test joins on `wire`, so a registry
 * that dropped it would fail on a naming difference and invite someone to
 * rename one side, losing the mapping the save path needs.
 */
export type CardField = { name: string; wire: string }

export type CardSpec = { fields: readonly CardField[] }

/**
 * Which fields each type of card carries — the table that drives card
 * rendering, and the one Task 29 holds against the engine's own descriptor.
 *
 * UNDECORATED ON PURPOSE. There is no label, no placeholder and no control kind
 * here, because Task 29 proves this table against the ENGINE and the engine's
 * descriptor carries none of those: a row this side invents is a row that
 * coverage test cannot check, sitting in the one file whose whole value is that
 * it can be. Which control a field renders as is `editor-cards.tsx`'s question,
 * answered from `name`.
 *
 * THE GATE'S ROW IS ONE FIELD AND TWO CONTROLS. `on_request_changes` is a
 * single wire key holding `{target, max_rounds}`, so the loop is one field of
 * the node however many inputs it takes to fill in — and `derive.ts`'s
 * `revisionViolation` judges it as one thing too.
 */
export const CARD_REGISTRY = {
  plan: {
    fields: [
      { name: 'harness', wire: 'harness' },
      { name: 'instructions', wire: 'instructions' },
    ],
  },
  implement: {
    fields: [
      { name: 'harness', wire: 'harness' },
      { name: 'instructions', wire: 'instructions' },
    ],
  },
  review: {
    fields: [
      { name: 'harness', wire: 'harness' },
      { name: 'instructions', wire: 'instructions' },
    ],
  },
  gate: { fields: [{ name: 'revision', wire: 'on_request_changes' }] },
  'git-publish': { fields: [] },
} as const satisfies Record<CardType, CardSpec>

/**
 * The field names this registry actually uses, read back off the table.
 *
 * IT IS DERIVED RATHER THAN DECLARED so that `editor-cards.tsx`'s switch over
 * it is exhaustive: adding a row here with a name no control renders becomes a
 * COMPILE error at the switch instead of a field that silently draws nothing.
 * That is the whole reason the table is `as const` and not merely typed.
 */
export type CardFieldName = (typeof CARD_REGISTRY)[CardType]['fields'][number]['name']
