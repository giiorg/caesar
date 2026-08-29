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
  //
  // WHICH ELEMENT PROVES THE LANDING IS PER-PROJECT, and this is the one place
  // in the shared critical path where that is true. MEASURED on the desktop
  // project's first run: `phase-timeline` resolves at 1280px and is HIDDEN.
  // Every CLOSED row carries `md:hidden` on purpose — on a wide viewport a
  // closed row is the rail's job, so the pane holds the one open body instead
  // of a stack of empty bordered strips (`runs.$runId.tsx`, PhaseTimeline) —
  // and with no `?phase=` yet there is no open row, so the `<ol>` is a real
  // element with an empty box. The rail is what a person is looking at there.
  //
  // The two `data-state` assertions below need no branch: the rows stay in the
  // DOM at both viewports and `toHaveAttribute` does not ask for a box, which
  // is exactly what that component's comment promises the live-update tests.
  const landed =
    test.info().project.name === 'desktop'
      ? page.getByTestId('phase-rail')
      : page.getByTestId('phase-timeline')
  await expect(landed).toBeVisible({ timeout: 30_000 })

  // ADVANCES WITHOUT A RELOAD: both rows reach these states on the same DOM the
  // create-task navigation landed on, driven by the stream through the cache
  // bridge. The gate row is not even in the DOM when this starts — the timeline
  // maps the phases the run HAS, and the gate phase materializes only once impl
  // finishes.
  //
  // `data-state` is the attribute mutant M2b killed both of these on: pinning
  // the row's state to a constant in `runs.$runId.tsx` fails them.
  await expect(page.getByTestId('phase-impl')).toHaveAttribute('data-state', 'succeeded', {
    timeout: 60_000,
  })
  await expect(page.getByTestId('phase-gate')).toHaveAttribute('data-state', 'waiting_decision', {
    timeout: 60_000,
  })
})
