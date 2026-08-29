import { expect, test } from '@playwright/test'
import { openGate, signIn } from './harness'

/**
 * THE THREE CLAIMS THAT ONLY A WIDE VIEWPORT CAN JUDGE — 12a's parked geometry
 * assertions (its ledger's Task-16 finding), plus the accelerator Task 31 built.
 *
 * DESKTOP PROJECT ONLY, by `testIgnore` on the mobile project rather than by a
 * skip in this file; playwright.config.ts says why. At 412px every locator here
 * resolves to a `display:none` element and every assertion would be measuring a
 * box that is not painted.
 *
 * WHY NONE OF THIS IS A COMPONENT TEST. jsdom loads no Tailwind and applies no
 * media queries, so `md:order-first`, `md:flex` and `hidden md:flex` are all
 * inert there: a component test can assert which class names were WRITTEN and
 * nothing at all about where the elements land. `order-first` in particular has
 * no DOM signature whatsoever — it is a paint decision over an unchanged
 * document order, which is exactly why it went a whole plan without a lane that
 * could falsify it.
 *
 * NO `quiesce`, and it counts no badge. The second test opens a gate and leaves
 * it waiting, which is safe because every file that makes an absolute claim
 * about the inbox quiesces in its own `beforeEach`.
 *
 * EVERY BOX READ IS PRECEDED BY A VISIBILITY WAIT. `boundingBox()` answers
 * `null` for a locator that has not resolved, and `box!.x` on `null` throws a
 * TypeError — a crash in place of the geometry failure the test exists to
 * report. `PhaseRail` in particular renders nothing until the run has phases,
 * and `openGate` returns 202 before the orchestrator has materialized any.
 */

test('the rail paints LEFT of main', async ({ page }) => {
  await signIn(page)

  const rail = page.getByTestId('desktop-rail')
  const main = page.getByRole('main')
  await expect(rail).toBeVisible()
  await expect(main).toBeVisible()

  const railBox = (await rail.boundingBox())!
  const mainBox = (await main.boundingBox())!
  // `md:order-first`, finally falsifiable. DOM order is header, indicator,
  // main, nav — the nav is LAST in the document and must paint FIRST on the
  // row, which is the whole content of the class and the reason the reading
  // order can stay thumb-friendly on a phone without costing the desktop
  // layout. Remove `md:order-first` from nav.tsx and the rail lands to the
  // right of everything: this is the mutant.
  expect(railBox.x + railBox.width).toBeLessThanOrEqual(mainBox.x + 1)
})

test('run detail is two-pane at desktop', async ({ page, request }) => {
  await signIn(page)
  const runId = await openGate(request)
  await page.goto(`/runs/${runId}`)

  const rail = page.getByTestId('phase-rail')
  const detail = page.getByTestId('phase-detail')
  // 30s, matching the badge waits elsewhere: `open-gate` answers 202 the moment
  // CreateTask returns, and the spine this rail renders does not exist until
  // the orchestrator has materialized the root phase.
  await expect(rail).toBeVisible({ timeout: 30_000 })
  await expect(detail).toBeVisible()

  const railBox = (await rail.boundingBox())!
  const detailBox = (await detail.boundingBox())!
  // SIDE BY SIDE, NOT STACKED — decisions §7's run detail A. The container is
  // `md:flex`, so dropping that one class leaves both children full-width block
  // boxes and the rail's right edge lands far past the detail pane's left one.
  expect(railBox.x + railBox.width).toBeLessThanOrEqual(detailBox.x + 1)
})

test('⌘K opens, matches, and navigates', async ({ page }) => {
  await signIn(page)

  // THE VISIBLE TRIGGER IS PART OF THE CLAIM. decisions §7 is "desktop = full
  // sidebar + top bar with ⌘K", and the button carries `md:flex` on a `hidden`
  // base — so this is the only lane that can tell a rendered affordance from a
  // chord nobody was told about.
  await expect(page.getByRole('button', { name: '⌘K' })).toBeVisible()

  await page.keyboard.press('ControlOrMeta+KeyK')
  const dialog = page.getByRole('dialog')
  await expect(dialog).toBeVisible()

  // `combobox`, NOT `textbox`. MEASURED: the input carries an explicit
  // `role="combobox"` (the activedescendant pattern needs it), and an explicit
  // role REPLACES the implicit one — inside this dialog Playwright's role
  // engine counts 0 textboxes and 1 combobox. The plan's `getByRole('textbox')`
  // would have timed out complaining about a textbox while the input sat there.
  await dialog.getByRole('combobox').fill('workfl')

  // Enter takes the HIGHLIGHTED row, and row 0 is deterministic here without
  // seeding any cache: `paletteEntries` returns pages first in a fixed section
  // order, `Workflows` is the only one of the seven pages matching `workfl`,
  // and cache-fed recents can only append after it.
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(/\/workflows/)
  // The palette is an accelerator that gets out of the way, not a surface that
  // stays open behind the page it sent you to.
  await expect(dialog).toBeHidden()
})
