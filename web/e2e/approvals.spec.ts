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

  // --- BEAT 1: REST returns N ---
  await page.getByRole('link', { name: 'Approvals' }).click()
  await expect(page.getByText('Nothing is waiting on you.')).toBeVisible()
  await expect(page.getByTestId('approvals-badge')).toHaveCount(0)

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
  await expect(page.getByTestId('approvals-badge')).toHaveText('1', { timeout: 30_000 })
  await expect(page.getByText('E2E approval')).toBeVisible()
})
