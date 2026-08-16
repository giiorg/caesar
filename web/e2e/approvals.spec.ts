import { expect, test } from '@playwright/test'

const CONTROL = process.env.CAESAR_CONTROL_URL ?? ''

test('a gate opened by the backend moves the approvals badge with no user action', async ({
  page,
  request,
}) => {
  // --- login -> shell ---
  await page.goto('/login')
  await page.getByLabel('Email').fill(process.env.CAESAR_EMAIL ?? '')
  await page.getByLabel('Password').fill(process.env.CAESAR_PASSWORD ?? '')
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('navigation')).toBeVisible()

  // --- the two presentations, at the ONE viewport this project runs ---
  //
  // The shell renders the phone tab bar and the desktop rail into the same
  // document and lets CSS choose; playwright.config.ts defines a single mobile
  // project (Pixel 7, 412px), so `md:` never matches and the rail is
  // display:none. THIS IS THE ONLY PLACE THOSE CLASSES ARE FALSIFIABLE — jsdom
  // loads no Tailwind, so every component test sees both presentations fully
  // present and would pass against a rail that is visible on a phone.
  //
  // The three assertions below are deliberately three different engines asked
  // about the same links, and the ORDER is what makes each one falsifiable:
  //
  //   1. a raw attribute selector — the DOM holds TWO Approvals links.
  //   2. `toBeHidden` on the rail — one of the two is display:none.
  //   3. a role query — so the accessibility tree, which a display:none
  //      subtree has left, offers exactly ONE. That is what lets the click
  //      below stay unscoped while every badge assertion is scoped to the bar:
  //      `getByTestId` is a plain attribute selector and matches both.
  //
  // (1) IS NOT REDUNDANT WITH (2). `toBeHidden` passes on a locator that
  // resolves to nothing at all — node_modules/playwright/types/test.d.ts:9049
  // says so outright ("either does not resolve to any DOM node, or resolves to
  // a non-visible one") — so on its own it cannot tell "hidden by CSS" from
  // "deleted", and this comment's two-in-the-DOM claim would be unencoded.
  // Deleting the rail block kills (1) first; unhiding it kills (2) first.
  //
  // (3) has no mutant of its own: anything that puts the rail in the
  // accessibility tree also makes it visible, so (2) fails first. Stated
  // rather than engineered around — it is here because it is the property
  // BEAT 1's unscoped click depends on, not because it is independently
  // guarded.
  const bar = page.getByTestId('phone-tabs')
  await expect(page.locator('a[href="/approvals"]')).toHaveCount(2)
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
  const opened = await request.post(`${CONTROL}/fixture/open-gate`)
  expect(opened.status()).toBe(202)

  // --- BEATS 3 and 4: SSE invalidates; REST returns N+1 ---
  // No click, no reload, no navigation between here and the assertion. The
  // assertion is on the RENDERED badge, never on a request count: a refetch is
  // the mechanism, and forbidding it would assert against the design.
  await expect(bar.getByTestId('approvals-badge')).toHaveText('1', { timeout: 30_000 })
  await expect(page.getByText('E2E approval')).toBeVisible()
})
