import { expect, test } from '@playwright/test'
import { seedLabels, signIn } from './harness'

/**
 * NO `quiesce` HERE, and the absence is deliberate. Every assertion in this
 * spec is scoped to the run this test just created — the timeline it is looking
 * at belongs to that run and nothing else can write to it — so leftover gates
 * from another file change none of them. The two files that DO reset are the
 * two that count the approvals badge, which is global.
 *
 * This spec leaves a waiting gate of its own; that is what their reset is for.
 */
test('creating a task produces a run whose timeline advances live', async ({ page, request }) => {
  await signIn(page)
  const seed = await seedLabels(request)

  await page.getByRole('link', { name: 'Tasks' }).click()
  await page.getByRole('button', { name: 'New task' }).click()
  // Project first, and not for tidiness: the repo select is disabled until a
  // project is chosen, because the only way to list repos is the
  // project-scoped endpoint.
  await page.getByLabel('Project').selectOption({ label: seed.project })
  await page.getByLabel('Repo').selectOption({ label: seed.repo })
  await page.getByLabel('Workflow').selectOption({ label: seed.workflow })
  await page.getByLabel('Title').fill('Live timeline')
  await page.getByLabel('Instructions').fill('Write the change.')
  await page.getByRole('button', { name: 'Create task' }).click()

  // Landing on the run is the form's own contract, and it is what makes the
  // next assertions about a LIVE timeline rather than about a fresh page load.
  await expect(page.getByTestId('phase-timeline')).toBeVisible({ timeout: 30_000 })

  // ADVANCES WITHOUT A RELOAD. Both rows start not-started and must reach these
  // states on this same DOM, driven by the stream through the cache bridge.
  // Asserted on `data-state` because the whole content of a dot is colour.
  await expect(page.getByTestId('phase-impl')).toHaveAttribute('data-state', 'succeeded', {
    timeout: 60_000,
  })
  await expect(page.getByTestId('phase-gate')).toHaveAttribute('data-state', 'waiting_decision', {
    timeout: 60_000,
  })
})
