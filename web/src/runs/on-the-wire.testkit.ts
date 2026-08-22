import type { Run } from './run-query'

/**
 * A run as the WIRE carries it, which is not what the generated type can say.
 *
 * `dto.go:182` and `:191` declare a phase's `decision` and `evidence` as
 * pointers with NO `omitempty`, so an absent one marshals to `null` and never
 * to a missing key — the DTO's own comment says the property "really is null on
 * every non-gate phase and on every decided one". The generated types render
 * both optional-and-NON-nullable (a `$ref` property carries no null branch in
 * this toolchain), so a `Run`-annotated fixture cannot write `null` at all.
 *
 * A fixture that leaves the key out instead is testing a body the server never
 * sends, and it makes `!phase.evidence` and `phase.evidence !== undefined`
 * indistinguishable — MEASURED in Task 12: with the key merely absent, swapping
 * the deep approvals route to `=== undefined` left the whole suite green. This
 * puts the true shape on the wire while a `Run` annotation on the argument
 * keeps every other field honest.
 *
 * `.testkit.ts`, NOT `.test.ts`, for the reason `decision-rules.testkit.ts`
 * records at length: `vitest.config.ts:61` collects `.test.ts(x)`, so a fixture
 * exported from a collected file would re-register that file's `describe`
 * blocks in every importer. It sits in `runs/` because that is where its
 * subject's type lives, and `tools/arch/import-graph.ts:8` does not exclude
 * `.testkit.ts` from its consumer census — so this module counts as a consumer
 * of `run-query.ts`, its own group, while its importers are all `.test.tsx`
 * files and are not counted.
 */
export function onTheWire(run: Run): Record<string, unknown> {
  return {
    ...run,
    // Spread LAST, so a phase that really carries one keeps it and only the
    // absent ones become null.
    phases: (run.phases ?? []).map((phase) => ({ decision: null, evidence: null, ...phase })),
  }
}
