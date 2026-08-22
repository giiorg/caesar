import { expect, test } from '@playwright/test'
import { openGate, quiesce, runState, signIn } from './harness'

/**
 * The gate, end to end on a phone: it arrives, it opens to the artifact it
 * examines, and the two decisions these tests cover — approve, and
 * request_changes with its note — do what they say. `GateControls` offers a
 * third from idle, Reject, which nothing here exercises.
 *
 * Every assertion below that names a gate names the run it belongs to. One
 * database serves the whole Playwright run, and a bare
 * `getByTestId('gate-card')` is a claim about every waiting gate in it.
 */
test.beforeEach(async ({ request }) => {
  // The badge assertions in both tests are ABSOLUTE — 0, then 1, then 0 — and
  // an absolute count is a claim about the whole inbox. This is what makes the
  // claim true regardless of which spec file ran before this one.
  await quiesce(request)
})

test('a gate opened by the backend reaches Home, opens to its evidence, and approving clears the badge', async ({
  page,
  request,
}) => {
  await signIn(page)
  // The badge renders in BOTH presentations — `nav.tsx` does that deliberately,
  // and `approvals.spec.ts` records why — so an unscoped `toHaveText` is a
  // strict-mode violation the moment the count is nonzero. `toHaveCount` is the
  // one badge assertion that is safe unscoped, and there it is the STRONGER
  // form: "0 in the tab bar" would pass against a badge stuck in the rail.
  const bar = page.getByTestId('phone-tabs')

  // BEAT 1: nothing is waiting. The empty state comes first because
  // `toHaveCount(0)` passes on a page that has not finished loading — it is
  // true of a screen with no badge and of a screen with nothing on it at all.
  // `InboxEmpty`'s sentence renders only after the approvals query resolves,
  // so it is what makes the count below a reading rather than a race.
  await expect(page.getByText('Nothing is waiting on you.')).toBeVisible()
  await expect(page.getByTestId('approvals-badge')).toHaveCount(0)

  // BEAT 2: the BACKEND TRANSACTION opens the gate. A spec that skipped this
  // and appended an event instead would invalidate the query, trigger the
  // refetch, and get the identical list back — the badge would not move and the
  // spec would prove nothing while looking like it passed.
  const runId = await openGate(request)

  // BEAT 3: it arrives with NO USER ACTION. No click, no reload, no navigation
  // between beat 2 and this assertion.
  //
  // 30 SECONDS IS A CEILING, NOT A CUSHION, and raising it would vacate this
  // assertion. The approvals query carries `refetchInterval: 60_000`
  // (`api/query-client.ts`, AUTH_SCOPED_QUERY_OPTIONS), so a budget at or past
  // a minute is satisfied by the poll whether or not the stream works at all.
  // MEASURED: with `gate.waiting` removed from ATTENTION_KINDS this failed here
  // at 30s, while this file's other test — waiting on the same query under the
  // test's own budget — went green at ~60s.
  await expect(bar.getByTestId('approvals-badge')).toHaveText('1', { timeout: 30_000 })
  // The card for THIS run, found by the run in its own link — which is also the
  // §2.2 property under test: the id on the inbox row is what the card links
  // to. `GateCard` makes the title the only link (a <button> inside an <a> is
  // invalid HTML), so `getByRole('link')` inside the card is that title.
  const card = page
    .getByTestId('gate-card')
    .filter({ has: page.locator(`a[href^="/runs/${runId}"]`) })
  await expect(card).toBeVisible()

  // BEAT 4: the card opens to its EXAMINED ARTIFACT. The file row's accessible
  // name is its path and its two counts, so the regex anchors on the path
  // alone: the counts are separate spans and the removed count is rendered with
  // U+2212, neither of which this assertion is about. The path is the fake's
  // per-node file, `CAESAR-<node id>.md`.
  await card.getByRole('link').first().click()
  await expect(page.getByTestId('hard-facts')).toBeVisible()
  await page.getByRole('button', { name: /^CAESAR-impl\.md/ }).click()
  // THE ADDED LINE'S TEXT, not the presence of a "+". The collapsed header
  // already renders "+1" and an expanded add-line renders its marker as its own
  // span, so an assertion on `/^\+/` passes with the accordion entirely broken.
  // This string is only in the DOM after the row expands, and it is
  // deterministic: the fake executor writes `<first line of instructions>
  // (<node id>)` into the patch it returns.
  await expect(page.getByTestId('file-accordion')).toContainText('Write the change. (impl)')

  // BEAT 5: approve, and the badge clears with no user action.
  await page.getByRole('button', { name: 'Approve', exact: true }).click()
  await expect(page.getByTestId('approvals-badge')).toHaveCount(0, { timeout: 30_000 })
})

test('request_changes with a note opens a new generation, visible on the timeline', async ({
  page,
  request,
}) => {
  await signIn(page)
  const runId = await openGate(request)
  await page
    .getByTestId('gate-card')
    .filter({ has: page.locator(`a[href^="/runs/${runId}"]`) })
    .getByRole('link')
    .first()
    .click()

  await page.getByRole('button', { name: /request changes/i }).click()
  // The note is REQUIRED and the requirement is part of the path: `gates.go`
  // feeds it to the revision target as a context file, and a revision with no
  // note re-runs the same work and reaches the same patch.
  //
  // THE COUNT IS THE ASSERTION THAT SAYS WHOSE REFUSAL THIS IS, and it is here
  // because the text alone cannot: the server refuses the same note with a
  // message that also contains "note", so `toContainText` is satisfied by
  // either. `FormError` renders whenever there is a message OR a correlation
  // id, and only a server answer carries a correlation id — so ONE alert means
  // the client refused and nothing was sent, while the server's refusal shows
  // the field error and a reference beside it. MEASURED both ways: the green
  // lane makes exactly two decision POSTs, both 200 (the click below makes
  // none), and deleting `noteSchema`'s `.min(1)` fails this line with
  // "Expected: 1, Received: 2".
  await page.getByRole('button', { name: /^send$/i }).click()
  await expect(page.getByRole('alert')).toHaveCount(1)
  await expect(page.getByRole('alert')).toContainText(/note/i)
  await page.getByLabel('Note').fill('Use the existing helper instead.')
  await page.getByRole('button', { name: /^send$/i }).click()

  // The DOM says a new round opened...
  await expect(page.getByTestId('run-header')).toContainText(/generation 2/i, { timeout: 60_000 })
  // ...and the BACKEND agrees. This is not a restatement: a header rendering
  // `run.generation + 1` satisfies the line above and fails here, which is a
  // mutant this task ran rather than a hypothetical.
  expect((await runState(request, runId)).generation).toBe(2)
})
