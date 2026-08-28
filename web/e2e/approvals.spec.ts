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

  // --- the two presentations, at the ONE viewport this project runs ---
  //
  // The shell renders the phone tab bar and the desktop rail into the same
  // document and lets CSS choose; playwright.config.ts defines a single mobile
  // project (Pixel 7, 412px), so `md:` never matches and the rail is
  // display:none. THIS IS THE ONLY PLACE THOSE CLASSES ARE FALSIFIABLE — jsdom
  // loads no Tailwind, so every component test sees both presentations fully
  // present and would pass against a rail that is visible on a phone.
  //
  // Three engines, three DIFFERENT hiddenness models, each with its own
  // measured mutant — no one of them subsumes another:
  //
  //   1. a CSS selector, which reads the DOM and asks nothing else
  //          delete the rail block  ->  1, not 2
  //   2. `toBeHidden`, a BOUNDING BOX (types/test.d.ts:9049 — it also passes
  //      on a locator resolving to no node at all, so it must follow (1))
  //          unhide the rail        ->  visible
  //   3. the role engine, which is ARIA — the property BEAT 1's unscoped
  //      click depends on
  //          rail sized to w-0 h-0  ->  2, not 1, with (1) and (2) still green
  //
  // `getByTestId` answers to none of the three: a plain attribute selector
  // matches both presentations, which is why every badge assertion below is
  // scoped to `bar`.
  const bar = page.getByTestId('phone-tabs')
  await expect(page.getByRole('navigation').locator('a[href="/approvals"]')).toHaveCount(2)
  await expect(page.getByTestId('desktop-rail')).toBeHidden()
  await expect(page.getByRole('link', { name: 'Approvals' })).toHaveCount(1)

  // --- BEAT 1: REST returns N ---
  await page.getByRole('link', { name: 'Approvals' }).click()
  await expect(page.getByText('Nothing is waiting on you.')).toBeVisible()
  await expect(bar.getByTestId('approvals-badge')).toHaveCount(0)

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
  await expect(bar.getByTestId('approvals-badge')).toHaveText('1', { timeout: 30_000 })
  await expect(page.getByText('E2E approval')).toBeVisible()
})

/**
 * THE GEOMETRY HALF OF THE PINNED BAR, and it is only falsifiable here: jsdom
 * loads no Tailwind, so the shell test beside this one can assert which class
 * names were written and nothing about where anything lands.
 *
 * It opens a gate and then DECIDES it, so it leaves the inbox as it found it —
 * BEAT 1 and BEAT 3 above are absolute claims about that inbox.
 */
test('the pinned bar never covers the decision controls or the end of a page', async ({
  page,
  request,
}) => {
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
  // offset; see approval-surface.tsx, and Task 32 owns the hole.
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
