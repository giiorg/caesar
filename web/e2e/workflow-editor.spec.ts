import { expect, test } from '@playwright/test'
import { seedLabels, signIn, workflowState } from './harness'

/**
 * AUTHORING A WORKFLOW AGAINST THE REAL BACKEND — spec §8's "one Playwright
 * e2e that authors and saves a workflow", and the only place the editor's
 * derivation is judged by the engine through the door a person uses.
 *
 * WHAT THIS COVERS THAT THE UNIT SUITE CANNOT. The component tests drive the
 * editor against MSW, so their verdict on a derived document is a fixture
 * somebody wrote; the corpus test judges real documents but posts none. Here
 * the bytes go through `createWorkflowVersion` — `ParseDocument`, `Validate`,
 * the content hash, the version row — and the assertion at the end is read back
 * out of the API rather than off the screen.
 *
 * NO `quiesce`. This file opens no gate and counts no badge; it creates a
 * workflow of its own and asserts only about that workflow, so it neither
 * disturbs nor is disturbed by the other specs sharing this one database.
 */
test('authors a workflow definition in the editor and saves it as v1', async ({
  page,
  request,
}) => {
  const seed = await seedLabels(request)
  await signIn(page)

  // --- the first of the two writes: name the workflow ---
  //
  // A name unique to this spec: one server and one database serve every spec
  // file, and `CreateWorkflow` refuses a duplicate name with a 409.
  const name = 'e2e-editor-authored'
  await page.goto('/workflows')
  await page.getByRole('button', { name: 'New workflow' }).click()
  await page.getByLabel('Name').fill(name)
  await page.getByRole('button', { name: 'Create workflow' }).click()

  // Creating a workflow NAVIGATES INTO THE EDITOR rather than closing a sheet:
  // a workflow with no version is half a thing, and the second write is the
  // one that makes it runnable.
  await expect(page.getByRole('heading', { name: 'New version' })).toBeVisible()
  const workflowId = new URL(page.url()).pathname.split('/')[2] ?? ''
  expect(workflowId).not.toBe('')

  // --- the outline: implement · implement · review · gate · git-publish ---
  //
  // Each menu offers exactly what the grammar accepts at its position, so
  // picking a type that is not on offer is not a thing this spec can do — the
  // add menu IS the structural enforcement (§6.3).
  await page.getByLabel('Add the first step').selectOption('implement')
  for (const type of ['implement', 'review', 'gate', 'git-publish']) {
    await page.getByLabel('Add a step at the end').selectOption(type)
  }
  const steps = page.getByRole('list', { name: 'Steps' }).getByRole('listitem')
  await expect(steps).toHaveCount(5)

  // --- the fields the validator requires of an agentic node ---
  //
  // The harness name comes off the SEED rather than being spelled here:
  // `checkHarnesses` resolves every agentic node's harness at task intake, so a
  // definition naming one that is not registered is a document that saves and
  // then cannot run.
  // The ids are auto-derived, so this pins WHICH cards the loop below fills
  // before it fills them — by position, which is also the order the outline
  // posts its nodes in.
  const stepIds = page.getByRole('textbox', { name: 'Step id' })
  for (const [index, id] of ['impl-1', 'impl-2', 'review-1'].entries()) {
    await expect(stepIds.nth(index)).toHaveValue(id)
    const card = steps.nth(index)
    await card.getByLabel('Harness').selectOption(seed.harness)
    await card.getByLabel('Instructions').fill(`step ${index + 1}: do the work`)
  }

  // --- a reorder, through the control a phone actually has ---
  //
  // THE ARROWS, NOT THE GRIP. HTML5 drag-and-drop emits nothing from a touch,
  // and this project runs at a Pixel 7 viewport, so the grip is dead here by
  // design — `outline-view.tsx` says as much where it renders both. The drag
  // path is covered by `outline-view.test.tsx`, which fires the drag events
  // directly, and the desktop project (Task 32) is where a real pointer drag
  // belongs.
  await page.getByRole('button', { name: 'Move impl-2 up' }).click()
  await expect(page.getByRole('textbox', { name: 'Step id' }).nth(0)).toHaveValue('impl-2')
  await expect(page.getByRole('textbox', { name: 'Step id' }).nth(1)).toHaveValue('impl-1')

  // --- save: the derived document through the real write door ---
  await page.getByRole('button', { name: 'Save version' }).click()

  // v1's read view, with the DAG drawn from the document the SERVER stored —
  // this page re-reads it over HTTP rather than rendering what was posted.
  await expect(page.getByRole('heading', { name: 'v1' })).toBeVisible()
  await expect(page.getByRole('img', { name: /5 steps/ })).toBeVisible()

  // THE ORACLE IS THE BACKEND, not the screen. A version view that rendered
  // from a stale cache would satisfy every assertion above; `latest_version` is
  // a database fact, and `workflowState` reads it through `workflows.Service`.
  const workflow = await workflowState(request, workflowId)
  expect(workflow.name).toBe(name)
  expect(workflow.latest_version).toBe(1)
})
