import { expect, test } from '@playwright/test'
import { openGate, quiesce, signIn } from './harness'

test.beforeEach(async ({ request }) => {
  // BEAT 1 and BEAT 3 below are ABSOLUTE claims about the inbox, and this file
  // is no longer the only one that opens gates: `gate-decision.spec.ts` and
  // `task-timeline.spec.ts` both leave runs behind in the one database this
  // lane's one server serves. See `harness.ts`'s `quiesce`.
  await quiesce(request)
})

test('a gate opened by the backend moves the approvals badge with no user action', async ({
  page,
  request,
}) => {
  // --- login -> shell ---
  await signIn(page)

  // --- the two presentations, and which one THIS project sees ---
  //
  // The shell renders the phone tab bar and the desktop rail into the same
  // document and lets CSS choose. THIS IS THE ONLY PLACE THOSE CLASSES ARE
  // FALSIFIABLE — jsdom loads no Tailwind, so every component test sees both
  // presentations fully present and would pass against a rail that is visible
  // on a phone.
  //
  // Three engines, three DIFFERENT hiddenness models, each with its own
  // measured mutant — no one of them subsumes another:
  //
  //   1. a CSS selector, which reads the DOM and asks nothing else
  //          delete either block    ->  1, not 2
  //   2. `toBeHidden`, a BOUNDING BOX (types/test.d.ts:9049 — it also passes
  //      on a locator resolving to no node at all, so it must follow a count)
  //          unhide the hidden one  ->  visible
  //   3. the role engine, which is ARIA — the property BEAT 1's unscoped
  //      click depends on
  //          the hidden one sized w-0 h-0 instead  ->  2, not 1, with (1) and
  //          (2) still green
  //
  // ENGINES 1 AND 3 ARE NOT BRANCHED, and that is the finding rather than a
  // shortcut. MEASURED at both display states: the anchor count is 2 and the
  // role count is 1 on a phone AND at desktop, because what they state is the
  // viewport-independent invariant — both presentations are in the DOM, exactly
  // one of them is in the accessibility tree. Copying them into two arms would
  // be two spellings of one claim, which is the drift `nav.tsx`'s own
  // `SECONDARY` comment is written against.
  //
  // So the branch is exactly WHICH presentation is the hidden one, and nothing
  // else. Engine 3 keeps a live mutant on both sides, mirrored rather than
  // inherited: at desktop, dropping `md:hidden` from the tab bar puts both link
  // sets in the a11y tree and answers 2.
  //
  // `getByTestId` answers to none of the three: a plain attribute selector
  // matches both presentations, which is why every badge assertion below is
  // scoped to the one this viewport shows.
  const bar = page.getByTestId('phone-tabs')
  const rail = page.getByTestId('desktop-rail')
  const desktop = test.info().project.name === 'desktop'
  const [shown, gone] = desktop ? [rail, bar] : [bar, rail]

  await expect(page.getByRole('navigation').locator('a[href="/approvals"]')).toHaveCount(2)
  // THE COUNT GUARD IS NOT REDUNDANT WITH (1), and the old comment here said it
  // was. (1) counts ANCHORS, so the mutant "delete the container's data-testid"
  // leaves it reading 2 while turning the `toBeHidden` below into a green
  // assertion about no node at all. Both arms carry the guard; the phone arm
  // had this hole uncovered until this task.
  await expect(gone).toHaveCount(1)
  await expect(gone).toBeHidden()
  await expect(shown).toBeVisible()
  await expect(page.getByRole('link', { name: 'Approvals' })).toHaveCount(1)

  // --- BEAT 1: REST returns N ---
  await page.getByRole('link', { name: 'Approvals' }).click()
  await expect(page.getByText('Nothing is waiting on you.')).toBeVisible()
  // Scoped to the presentation a person is actually looking at. MEASURED, and
  // it is why this is not left as `bar`: `toHaveText` and `toHaveCount` both
  // PASS against a `display:none` element, so a bar-scoped badge assertion at
  // desktop would report success about chrome nobody can see.
  await expect(shown.getByTestId('approvals-badge')).toHaveCount(0)

  // --- BEAT 2: the BACKEND TRANSACTION opens the gate ---
  // A test that skips this beat is the failure mode the spec's §7 exists to
  // prevent. GET /approvals is ListWaitingGates — it reads phase_runs.status
  // and never run_events — so appending an event and nothing else would
  // invalidate the query, trigger the refetch, and return the identical list:
  // the badge would not move and the spec would prove nothing while looking
  // like it passed.
  await openGate(request)

  // --- BEATS 3 and 4: SSE invalidates; REST returns N+1 ---
  // No click, no reload, no navigation between here and the assertion. The
  // assertion is on the RENDERED badge, never on a request count: a refetch is
  // the mechanism, and forbidding it would assert against the design.
  await expect(shown.getByTestId('approvals-badge')).toHaveText('1', { timeout: 30_000 })
  await expect(page.getByText('E2E approval')).toBeVisible()
})

/**
 * THE GEOMETRY HALF OF THE PINNED BAR, and it is only falsifiable here: jsdom
 * loads no Tailwind, so the shell test beside this one can assert which class
 * names were written and nothing about where anything lands.
 *
 * PHONE ONLY, AS A WHOLE TEST rather than as a branch inside one. Its subject
 * is a bar that does not exist at desktop: `phone-tabs` is `md:hidden`, so the
 * `toBeVisible` two lines down fails at 1280px and every measurement after it
 * is about an element with no box. There is no desktop inverse to write here —
 * the desktop shell pins nothing over the end of a page, which is why `<main>`
 * gives its bottom clearance back with `md:pb-4`.
 *
 * It opens a gate and then DECIDES it, so it leaves the inbox as it found it —
 * BEAT 1 and BEAT 3 above are absolute claims about that inbox.
 */
test('the pinned bar never covers the decision controls or the end of a page', async ({
  page,
  request,
}) => {
  test.skip(test.info().project.name !== 'mobile', 'the pinned bar exists only on the phone')
  await signIn(page)
  const runId = await openGate(request)
  // The card for THIS run, found by the run in its own link. `GateCard` makes
  // the title the only link (a <button> inside an <a> is invalid HTML), so
  // `getByRole('link').first()` is that title — the same shape
  // gate-decision.spec.ts uses.
  await page
    .getByTestId('gate-card')
    .filter({ has: page.locator(`a[href^="/runs/${runId}"]`) })
    .getByRole('link')
    .first()
    .click()
  const bar = page.getByTestId('phone-tabs')
  const approve = page.getByRole('button', { name: 'Approve', exact: true })
  await expect(bar).toBeVisible()
  await expect(approve).toBeVisible()
  const barBox = await bar.boundingBox()
  const btnBox = await approve.boundingBox()
  // The decision controls sit fully ABOVE the pinned bar — overlap is the bug class.
  expect(btnBox!.y + btnBox!.height).toBeLessThanOrEqual(barBox!.y + 1)

  // THE MAIN-PADDING HALF NEEDS ITS OWN SUBJECT. The Approve button lives in a
  // `sticky bottom-…` bar inside the approval surface with its OWN offset — it
  // clears the tab bar whether or not <main> reserves anything, so measuring it
  // proves nothing about `pb-tab-bar`. The subject is the event log: an
  // ordinary, non-sticky block that is the LAST element of this page, so only
  // <main>'s reservation keeps it off the bar at full scroll.
  //
  // `window.scrollTo`, not `keyboard.press('End')`: End depends on where focus
  // landed after the click, and `scrollIntoViewIfNeeded` stops as soon as the
  // element is PARTLY in view — the exact condition under test.
  await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight))
  const tailBox = await page.getByTestId('event-log').boundingBox()
  expect(tailBox!.y + tailBox!.height).toBeLessThanOrEqual(barBox!.y + 1)
  // A pinned bar does not scroll away — without this, removing `fixed` passes
  // both halves vacuously.
  expect((await bar.boundingBox())!.y).toBe(barBox!.y)
  // WHAT THE ASSERTION ABOVE DOES NOT COVER, stated rather than implied: the
  // decision bar's OWN `bottom-tab-bar`. MEASURED — swapping it back to
  // `bottom-0` leaves this test green on both hosts, because a bottom-sticky
  // element only leaves its flow position once its containing block outgrows
  // the viewport and neither fixture gate does (forcing the element's `bottom`
  // to 0px, 64px and 200px in the live page moved its box not at all). So the
  // half above is falsified by `fixed` and `pb-tab-bar`, and NOT by that
  // offset; see approval-surface.tsx.
  //
  // THE DESKTOP PROJECT DOES NOT PAY THAT DEBT, and the note that sent it to
  // Task 32 was wrong about the mechanism. Two reasons, both measured: at `md:`
  // the class in force is the `md:bottom-0` OVERRIDE, so a wide viewport never
  // exercises `bottom-tab-bar` at all; and the cause of the survival is not the
  // viewport but the FIXTURE — forcing the element's `bottom` to 0px, 64px and
  // 200px in the live page moved its box not at all, because a bottom-sticky
  // element only leaves its flow position once its containing block outgrows
  // the screen, and this gate's diff is far too short on either host. What
  // would falsify it is a seeded patch long enough to overflow, which is a
  // change to the Go harness's fake executor.
  //
  // The deep route is still worth walking. `ApprovalSurface` is the whole page
  // there rather than a panel nested in a timeline row, so its controls sit at
  // the bottom of <main> — the arrangement in which the reservation has to
  // hold, on the screen a notification actually opens.
  await page.getByRole('link', { name: 'Approvals' }).click()
  await page.getByRole('link').filter({ hasText: 'E2E approval' }).first().click()
  const deepApprove = page.getByRole('button', { name: 'Approve', exact: true })
  await expect(deepApprove).toBeVisible()
  await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight))
  const deepBar = (await bar.boundingBox())!
  const deepBtn = (await deepApprove.boundingBox())!
  expect(deepBtn.y + deepBtn.height).toBeLessThanOrEqual(deepBar.y + 1)
  // Clickable proves not covered: Playwright refuses a covered target. This is
  // also what returns the inbox to empty for the tests above.
  await deepApprove.click()
})
