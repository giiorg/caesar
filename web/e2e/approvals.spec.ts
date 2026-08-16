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
  // The two assertions below are deliberately different queries, because they
  // answer to different engines and the difference is what the rest of this
  // file depends on: a role query is served from the accessibility tree, which
  // a display:none subtree has left, so ONE Approvals link is matched even
  // though two are in the DOM. `getByTestId` is a plain attribute selector and
  // matches both regardless — which is why every badge assertion below is
  // scoped to the bar and none of them is bare.
  const bar = page.getByTestId('phone-tabs')
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
