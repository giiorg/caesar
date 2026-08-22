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
