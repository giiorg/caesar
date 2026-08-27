import { cleanup, screen } from '@testing-library/react'
import { expect } from 'vitest'
import type { GateEvidence } from './decide-gate'

/**
 * §5's decision rules, asserted against whatever surface hosts the controls.
 *
 * `.testkit.ts`, NOT `.test.ts`, and the extension is load-bearing twice over.
 * `vitest.config.ts:61` collects only files whose names end `.test.ts` or
 * `.test.tsx`, so a `.test` file imported by another test file would have its
 * own `describe` blocks registered a second time and every shared assertion
 * would run once per host under the wrong file's name. And
 * `tools/arch/import-graph.ts:8` excludes only
 * `.(test|spec|stories).tsx?` from its consumer census — `.testkit.ts` does NOT
 * match that pattern, so this module IS counted as a consumer of what it
 * imports. That direction is the harmless one (it only ever adds importers to
 * `gates/`, its own group), while its own importers are all `.test.tsx` files
 * and are not counted, which is why a module with three hosts is allowed to
 * sit here beside its subject rather than being clamped upward.
 *
 * WHY THE RULE IS SHARED RATHER THAN RESTATED: three surfaces offer a decision
 * (Home's gate card, the run-detail gate phase, Approvals) and the mistake this
 * guards against — suppressing approve and leaving the other two — looks
 * obviously right in each of them independently. `backend/internal/runs/gates.go:190`
 * returns ClassForbidden on the requester check BEFORE it branches on decision
 * type at `:204`, so request_changes and reject are refused identically.
 */

/**
 * A gate nobody may decide, with the reason left for each case to name.
 *
 * `wall_deadline` IS LOAD-BEARING and it is deliberately far in the future: a
 * host may suppress on its own clock as well as on `cannot_decide_reason`
 * (gate-controls.tsx does), and a past deadline here would make every case pass
 * through that check instead — the reasons would never be read at all and all
 * four cases would be vacuous. 2099 keeps each case about the reason it names,
 * and it is what makes case 4 an assertion about the SERVER's answer.
 */
const REFUSED: GateEvidence = {
  examined_artifact_id: 'a1',
  examined_artifact_type: 'patch',
  summary_artifact_id: null,
  can_decide: false,
  cannot_decide_reason: '',
  wall_deadline: '2099-01-01T00:00:00Z',
}

/**
 * Named individually rather than counted. `queryAllByRole('button')` would pass
 * against a surface that rendered three DISABLED buttons, which is the design
 * this rule rejects — a disabled control still says "this is yours to do,
 * later".
 */
function expectNoControls(): void {
  expect(screen.queryByRole('button', { name: /^approve$/i })).toBeNull()
  expect(screen.queryByRole('button', { name: /request changes/i })).toBeNull()
  expect(screen.queryByRole('button', { name: /^reject$/i })).toBeNull()
}

/**
 * The four assertions every host re-runs verbatim.
 *
 * `render` mounts the host's own surface around the evidence it is given; the
 * kit knows nothing about how much of the app that is.
 *
 * THE AWAIT COMES BEFORE THE ABSENCE ASSERTIONS, in every case, and that
 * ordering is what stops the kit being vacuous in an asynchronous host. A host
 * that mounts a route fetches before it can render a gate at all, so
 * `queryByRole` immediately after `render` would find no buttons because
 * nothing had rendered yet — passing against a surface that goes on to render
 * all three a tick later. Waiting for the refusal message first proves the
 * controls' own surface is on screen before anything is claimed absent from it.
 *
 * `cleanup()` between cases is likewise required rather than tidy.
 * `src/test/setup.ts:29` registers `afterEach(cleanup)` — between TESTS — and
 * all three hosts call this inside a single `it`, so without an explicit
 * unmount each case renders on top of the last. MEASURED with the calls
 * deleted: `Found multiple elements with the text: /approve grant/i` at case 3,
 * which re-renders the message case 1 left on screen. It fails deterministically
 * and it would fail this way in every host.
 *
 * THE LAST `cleanup()` UNMOUNTS THE HOST'S WHOLE TREE, not just case 4's
 * render — `cleanup` unmounts every container in a module-level registry and
 * then clears it (`@testing-library/react/dist/pure.js:301-314`), so it knows
 * nothing about which render belongs to this kit. A host that renders
 * anything else inside the same `it` must therefore call this kit LAST, or
 * mount its own surface again after it returns.
 *
 * Every assertion below has been observed failing on its own, which is the
 * only reason three hosts may re-run them sight unseen: case 2's suppression
 * check dies at `expectNoControls` on a component that suppresses the other
 * three reasons and not this one, case 2's message check dies when
 * `self_approval_forbidden` is dropped from gate-controls.tsx's REASONS, and
 * case 4's message check dies when `deadline_expired` is dropped from it — the
 * generic fallback then renders instead, which case 4's `/wall deadline/i` does
 * not match.
 *
 * All three were measured against gate-controls.test.tsx, and the two halves of
 * each run are not symmetric: the earlier cases had already PASSED, while the
 * later ones were never REACHED, because a failing assertion throws and the
 * exception propagates straight out of this function. That asymmetry is the
 * evidence. An earlier case passing on the same mutant is what shows the kill
 * is scoped to the reason the mutant names rather than to the kit as a whole;
 * a later case not running is why neither of the case-2 mutants says anything
 * about case 3, which M4 and M5 cover instead.
 */
export async function expectDecisionRules(render: (evidence: GateEvidence) => void): Promise<void> {
  // 1. Short of the approve grant. `GET /approvals` lists gates for any grant
  //    while decideGate requires GrantApprove, deliberately, so a caller can
  //    hold a row in their inbox that they may not decide.
  render({ ...REFUSED, cannot_decide_reason: 'not_approver' })
  expect(await screen.findByText(/approve grant/i)).toBeInTheDocument()
  expectNoControls()
  cleanup()

  // 2. THE PLAUSIBLE-LOOKING MISTAKE. The requester of a run whose policy
  //    forbids self-approval is refused before the decision type is read, and
  //    on a single-operator system the operator IS always the requester — which
  //    makes this the normal path rather than an edge case.
  render({ ...REFUSED, cannot_decide_reason: 'self_approval_forbidden' })
  expect(await screen.findByText(/your own gate/i)).toBeInTheDocument()
  expectNoControls()
  cleanup()

  // 3. Two refusals with one message is a message that tells nobody what to do:
  //    one is fixed by a grant, the other by a policy on a run already created.
  render({ ...REFUSED, cannot_decide_reason: 'not_approver' })
  expect(await screen.findByText(/approve grant/i)).toBeInTheDocument()
  expect(screen.queryByText(/your own gate/i)).toBeNull()
  cleanup()

  // 4. THE SERVER'S CLOCK BEATS THE READER'S. `wall_deadline` here is REFUSED's
  //    own, in 2099 — so this host's client-side deadline check does NOT fire,
  //    and the only thing that can produce a refusal is the server's
  //    `deadline_expired`. That combination is not contrived: `can_decide`
  //    is measured against the DATABASE clock (`api.go:532`), the clock that
  //    will judge the POST, and a browser running behind it sees exactly this.
  //    A host that suppressed on its own clock alone would offer three controls
  //    here, every one of them a certain 409.
  render({ ...REFUSED, cannot_decide_reason: 'deadline_expired' })
  expect(await screen.findByText(/wall deadline/i)).toBeInTheDocument()
  expectNoControls()
  cleanup()
}
