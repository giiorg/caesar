import { expect, type Page, test } from '@playwright/test'
import { quiesce, registerRemote, runState, signIn } from './harness'

/**
 * THE MILESTONE SENTENCE, WALKED BY A MACHINE. Spec §1 says a person creates a
 * project, points caesar at a repo, defines a workflow, files a task, watches a
 * run, asks for changes, approves, and reads the pull request — with the UI as
 * the only door. Row 12b flips on an operator doing exactly that once. This
 * spec is its continuous shadow: the same walk, in the same dependency-true
 * order, on every commit.
 *
 * WHAT IS NEW HERE IS THE DOOR, NOT THE MACHINERY.
 * `goldenset_integration_test.go` already drives this whole conveyor — gate,
 * revision round, approval, publication, pull request — against the same two
 * doubles this lane now uses, over HTTP. What no test made until this one is
 * the claim the milestone actually rests on: that every asset it needs can be
 * CREATED THROUGH THE SCREENS, in an order a person can discover, and that the
 * last two clauses of the sentence are rendered rather than merely recorded.
 *
 * THE TWO CLAUSES EVERY OTHER SPEC STOPS SHORT OF, and why they are the point:
 *
 *   - `gate-decision.spec.ts` ends at badge clearance. Approval is where it
 *     stops being interesting to a person and starts being interesting to the
 *     system — the patch is published, a branch is pushed, a pull request is
 *     opened, and the row that names it must be a LINK somebody can follow.
 *   - Approvals › History existed with no end-to-end reader at all. Its rows
 *     link with the full (node, generation, attempt) triple, and a run with two
 *     decisions on ONE gate — which is what a revision round produces — is the
 *     only fixture where getting that wrong is visible.
 *
 * EIGHT ASSETS, ZERO SEED. Nothing this test uses comes from `e2eSeed`: it
 * makes its own project, forge credential, repo, subscription credential,
 * harness, workflow, version and task. That is the assertion, not the setup —
 * a walk that borrowed the seeded repo would prove the run works and say
 * nothing about whether the screens that create one do.
 *
 * PHONE ONLY, enforced in `playwright.config.ts` rather than skipped here; see
 * the note beside the desktop project for both reasons.
 */

/**
 * Everything this walk creates, suffixed so the world stays legible when the
 * lane fails and somebody reads the database.
 *
 * SUFFIXED BY PROJECT even though the config runs this on one project today.
 * `projects.name`, `credentials.name`, `harnesses.name` and `workflows.name`
 * are each UNIQUE across the single database this lane serves, so a second
 * Playwright project would make this file collide WITH ITSELF — which is
 * exactly what happened to `workflow-editor.spec.ts` when Task 32 added the
 * desktop axis, under a comment that justified a fixed name by naming how many
 * worlds there were. The suffix costs nothing and does not have to be revisited
 * when the next axis arrives.
 */
const suffix = () => test.info().project.name

/**
 * The pull-request URL shape `fake.Forge` mints (`runs/fake/forge.go:230`).
 *
 * The host is asserted rather than just "some https URL", because the weak
 * version passes against a link built from the wrong field entirely — the run's
 * own address, say — and the whole point of the beat is that the row carries
 * the FORGE's answer back to the reader.
 */
const FAKE_FORGE = 'https://fake.forge'

/**
 * Quote a literal for use inside a `RegExp`.
 *
 * The href assertion below interpolates two strings that are NOT regex-safe —
 * `https://fake.forge` has a dot, and a repo slug is `owner/name` — and an
 * unescaped `.` matches any character. Left raw, the pattern would accept
 * `https://fakeXforge/…`: a weaker claim than the one the comment above makes,
 * and weaker in the direction that matters, since the wrong host is exactly
 * what the assertion exists to catch.
 */
const quote = (literal: string) => literal.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')

/**
 * The two notes, which MUST DIFFER, and the reason is the History beat rather
 * than variety. That beat's claim is that a decision row links to the attempt
 * it was made on — and an approve carries no note at all (`GateControls`
 * approves in one click), so the request_changes note is the only text that can
 * tell the gen-1 pane from the gen-2 one. Two identical notes, or a note the
 * approve also carried, and the final assertion would pass against a link that
 * dropped its selector entirely.
 */
const CHANGE_NOTE = 'Use the existing helper instead of a new one.'

/**
 * Reach a secondary page the way a phone does.
 *
 * Workflows, Repos and Admin are not tabs — `nav.tsx` keeps four tabs and More,
 * and More is what holds the rest (`SECONDARY`). Going through it rather than
 * `page.goto` is the difference between walking the product and driving the
 * router: a page that became unreachable from the shell would still answer its
 * URL.
 */
async function viaMore(page: Page, label: 'Workflows' | 'Repos' | 'Admin'): Promise<void> {
  await page.getByRole('link', { name: 'More' }).click()
  await page.getByRole('link', { name: label }).click()
}

/** Admin's four sections are tabs over one route (`admin-nav.ts`). */
async function adminSection(page: Page, label: 'Harnesses' | 'Credentials'): Promise<void> {
  await viaMore(page, 'Admin')
  await page.getByRole('tab', { name: label }).click()
}

/**
 * The open bottom sheet, and EVERY form field below is scoped through it.
 *
 * Not tidiness. `FormSheet` is a vaul drawer over Radix's dialog, and the
 * screen it opens over keeps rendering: on Admin › Harnesses the submit button
 * reads "Register harness" and so does the button that opened the sheet, which
 * is a strict-mode violation and — worse if strict mode ever relaxed — an
 * ambiguity that would resolve to the trigger and reopen the sheet instead of
 * submitting it. Scoping also keeps `getByLabel('Name')` meaning the field in
 * front of the person rather than any field on the route.
 */
const sheet = (page: Page) => page.getByRole('dialog')

/**
 * Store one credential. The kind is chosen before anything else that depends on
 * it, which is the order the screens enforce elsewhere too.
 */
async function addCredential(page: Page, name: string, kind: string): Promise<void> {
  await page.getByRole('button', { name: 'Add credential' }).click()
  const form = sheet(page)
  await form.getByLabel('Name').fill(name)
  await form.getByLabel('Kind').selectOption(kind)
  // Any non-empty blob: only `agent_auth_file` is parsed, and only for its
  // fingerprint (`assets/credentials.go`, validateSecret).
  await form.getByLabel('Secret').fill(`secret-for-${name}`)
  await form.getByRole('button', { name: 'Store credential' }).click()
  // The sheet closing IS the success state on this screen, and waiting for the
  // row is what makes the next beat's picker a reading rather than a race: the
  // repo and harness forms both filter their credential options off a query
  // this create invalidates.
  await expect(page.getByTestId('credential-rows')).toContainText(name)
}

test.beforeEach(async ({ request }) => {
  // The History beat reads the TOP TWO rows of a global list, so this file has
  // the same interest in a quiet world that the badge-counting files have: a
  // spec that left a gate waiting is a spec whose gate some later quiesce will
  // decide, and a decision landing between these two would push one of them
  // down. Quiescing first also means this walk's own two decisions are the
  // newest things in the history when it reads it.
  await quiesce(request)
})

test('the milestone sentence, end to end: eight assets through the UI, a revision round, a published pull request, and both decisions in History', async ({
  page,
  request,
}) => {
  const s = suffix()
  const projectName = `walk-${s}`
  const repoSlug = `walk/${s}`
  const forgeCred = `walk-forge-${s}`
  const agentCred = `walk-subscription-${s}`
  const harnessName = `walk-harness-${s}`
  const workflowName = `walk-flow-${s}`

  // ITS OWN CEILING, and for the reason the config states about the global one:
  // an assertion's stated budget is a fiction if the test containing it is cut
  // off first. This walk states 60 + 60 + 90 + 60 seconds across four waits and
  // does forty-odd interactions besides, so under the 120s default a slow host
  // would report "test timeout" — a killed test with no named assertion —
  // rather than the beat that actually stalled. Raised HERE and not in the
  // config: every other spec is a handful of beats and should keep the tighter
  // ceiling, and the lane's own budget is 15 minutes for everything.
  test.setTimeout(300_000)

  await signIn(page)

  // ---------------------------------------------------------------- BEAT 1
  // A project. It is first because everything else hangs off it: repos are
  // created on a project-scoped endpoint, and the task form's repo select stays
  // disabled until one is picked.
  await viaMore(page, 'Repos')
  await page.getByRole('button', { name: 'New project' }).click()
  await sheet(page).getByLabel('Name').fill(projectName)
  await sheet(page).getByRole('button', { name: 'Create project' }).click()
  await expect(
    page.getByTestId('project-cards').getByRole('link', { name: projectName }),
  ).toBeVisible()

  // ---------------------------------------------------------------- BEAT 2
  // A forge credential, BEFORE the repo, because the repo form cannot be
  // completed without one — its picker offers `forge_token` and nothing else,
  // and the server refuses any other kind on `credential_id`. This ordering is
  // the "dependency-true order" §1 asks the walk to prove is discoverable.
  await adminSection(page, 'Credentials')
  await addCredential(page, forgeCred, 'forge_token')

  // ---------------------------------------------------------------- BEAT 3
  // The repo, bound to that credential.
  await viaMore(page, 'Repos')
  await page.getByTestId('project-cards').getByRole('link', { name: projectName }).click()
  await page.getByRole('button', { name: 'New repo' }).click()
  {
    const form = sheet(page)
    await form.getByLabel('Forge').selectOption('github')
    await form.getByLabel('Slug').fill(repoSlug)
    // `default_branch` arrives pre-filled with `main` and is left standing —
    // the form shows the server's substitution rather than hiding it.
    await form.getByLabel('Credential').selectOption({ label: forgeCred })
    await form.getByRole('button', { name: 'Register repo' }).click()
  }
  // SCOPED TO THE CARDS, because this screen renders its repos TWICE — as cards
  // and as a table, with CSS choosing (`repos.$projectId.tsx`). An unscoped
  // `getByText(repoSlug)` matches both and strict-fails; `.first()` would pass
  // against a phone projection that had stopped rendering the slug at all.
  await expect(page.getByTestId('repo-cards')).toContainText(repoSlug)

  // The one thing a browser cannot do: tell the code-host doubles that this
  // repository exists. Nothing about it is asserted here — the walk's next
  // beats are the assertion, and every one of them fails without this.
  const remote = await registerRemote(request, projectName, repoSlug)
  expect(remote.head).not.toBe('')

  // ---------------------------------------------------------------- BEAT 4
  // The agent's credential, then the harness that binds it. Two beats, not one:
  // `claude-code` accepts a subscription token or a model key and the picker
  // filters to those, so the credential has to exist first — and the harness
  // form CLEARS the credential when the kind changes, which is why the kind is
  // chosen before the credential below rather than after.
  await adminSection(page, 'Credentials')
  await addCredential(page, agentCred, 'subscription_token')

  await adminSection(page, 'Harnesses')
  await page.getByRole('button', { name: 'Register harness' }).click()
  {
    const form = sheet(page)
    await form.getByLabel('Name').fill(harnessName)
    // KIND BEFORE CREDENTIAL, and the form makes that mandatory rather than
    // conventional: changing the kind CLEARS `credential_id`, because the two
    // bindable sets overlap only at `model_key`. Filled the other way round the
    // picker would look right and submit empty.
    await form.getByLabel('Kind').selectOption('claude-code')
    // Never pulled: this lane's executor is a fake and the image reference is a
    // string the run records. `provider_endpoint` must be https unless it is
    // localhost (`assets/harnesses.go`), so port 1 is the honest dead end.
    await form.getByLabel('Image reference').fill('unused-by-the-fake-executor')
    await form.getByLabel('Provider endpoint').fill('http://localhost:1')
    // THE TWO CREDENTIAL PICKERS LABEL THEIR OPTIONS DIFFERENTLY, and it is not
    // an inconsistency to paper over. The repo form's picker offers exactly one
    // kind, so a bare name is unambiguous; this one offers TWO — a `claude-code`
    // harness binds a subscription token or a model key — so `bindableOptions`
    // annotates each with its kind. MEASURED: `{ label: agentCred }` hangs here
    // until the test times out, with the select resolved and "did not find some
    // options" in the call log.
    await form.getByLabel('Credential').selectOption({ label: `${agentCred} (subscription_token)` })
    await form.getByRole('button', { name: 'Register harness' }).click()
  }
  await expect(page.getByTestId('harness-rows')).toContainText(harnessName)

  // ---------------------------------------------------------------- BEAT 5
  // The workflow, authored in the editor: implement · review · gate ·
  // git-publish. That is the seeded definition plus the node no e2e spec has
  // ever reached, and the derivation wires it — gate to publish on `patch` —
  // without anybody drawing an edge.
  await viaMore(page, 'Workflows')
  await page.getByRole('button', { name: 'New workflow' }).click()
  await sheet(page).getByLabel('Name').fill(workflowName)
  await sheet(page).getByRole('button', { name: 'Create workflow' }).click()
  await expect(page.getByRole('heading', { name: 'New version' })).toBeVisible()

  await page.getByLabel('Add the first step').selectOption('implement')
  for (const type of ['review', 'gate', 'git-publish']) {
    await page.getByLabel('Add a step at the end').selectOption(type)
  }
  const steps = page.getByRole('list', { name: 'Steps' }).getByRole('listitem')
  await expect(steps).toHaveCount(4)

  // The two agentic cards. Their labels repeat once per card, so each is scoped
  // to its own step — an unscoped `getByLabel('Harness')` is a strict-mode
  // violation the moment there are two.
  //
  // THE INSTRUCTIONS ARE LOAD-BEARING TEXT, not filler: the fake executor
  // writes `<first line> (<node id>)` into the patch it returns, so this string
  // is what the gate screen shows as the change under examination.
  for (const [index, id] of ['impl-1', 'review-1'].entries()) {
    const card = steps.nth(index)
    await expect(card.getByRole('textbox', { name: 'Step id' })).toHaveValue(id)
    await card.getByLabel('Harness').selectOption(harnessName)
    await card.getByLabel('Instructions').fill(`Write the change (${id}).`)
  }

  // THE REVISION LOOP IS OFF BY DEFAULT AND THIS WALK NEEDS IT. A gate card
  // starts at "No revision loop", and without a target `request_changes` has
  // nowhere to send the work — the whole middle of this walk, the beat §9 names
  // as its first witness, would not exist. MEASURED as a mutant: drop the line
  // below and the rounds control never appears, because it is rendered only
  // once a target is chosen.
  const gate = steps.nth(2)
  await gate.getByLabel('Revision loop sends work back to').selectOption('impl-1')
  // TWO ROUNDS, AND THIS WALK CANNOT FALSIFY THE NUMBER — said out loud rather
  // than left to look load-bearing. Choosing a target defaults the count to 1,
  // and one round is exactly what this walk spends, so the mutant that leaves
  // the default SURVIVES (measured). It is set anyway for two reasons: it
  // matches the seeded definition this workflow is otherwise a copy of, and it
  // is the only place the rounds control is driven at all.
  await gate.getByLabel('Maximum rounds').fill('2')

  await page.getByRole('button', { name: 'Save version' }).click()
  await expect(page.getByRole('heading', { name: 'v1' })).toBeVisible()

  // ---------------------------------------------------------------- BEAT 6
  // The task, through the FAB, naming the three things this walk just made.
  await page.getByRole('link', { name: 'Tasks' }).click()
  await page.getByRole('button', { name: 'New task' }).click()
  await page.getByLabel('Project').selectOption({ label: projectName })
  await page.getByLabel('Repo').selectOption({ label: repoSlug })
  await page.getByLabel('Workflow').selectOption({ label: workflowName })
  await page.getByLabel('Title').fill(`Milestone walk (${s})`)
  await page.getByLabel('Instructions').fill('Ship the change end to end.')
  await page.getByRole('button', { name: 'Create task' }).click()

  // Landing on the run is the form's contract. The id comes off the address
  // because every backend-oracle call below needs it.
  await expect(page).toHaveURL(/\/runs\/[0-9a-f-]{36}/)
  const runId = new URL(page.url()).pathname.split('/')[2] ?? ''
  expect(runId).not.toBe('')

  // ---------------------------------------------------------------- BEAT 7
  // The gate arrives on a live screen — no reload between the create and here —
  // and opens to what it examines.
  const gateRow = page.getByTestId('phase-gate-1')
  await expect(gateRow).toHaveAttribute('data-state', 'waiting_decision', { timeout: 60_000 })
  await gateRow.getByRole('button').click()
  await expect(page.getByTestId('hard-facts')).toBeVisible()

  // ---------------------------------------------------------------- BEAT 8
  // Request changes, with the note that is the whole reason a revision round
  // differs from a retry.
  await page.getByRole('button', { name: /request changes/i }).click()
  await page.getByLabel('Note').fill(CHANGE_NOTE)
  await page.getByRole('button', { name: /^send$/i }).click()

  // A NEW GENERATION, ASSERTED IN THIS ORDER ON PURPOSE. The DOM first, then
  // the backend — a header rendering `generation + 1` satisfies the first and
  // fails the second, which is a mutant `gate-decision.spec.ts` measured.
  await expect(page.getByTestId('run-header')).toContainText(/generation 2/i, { timeout: 60_000 })
  expect((await runState(request, runId)).generation).toBe(2)
  // AND THIS THIRD LINE IS A DIAGNOSTIC, NOT AN ASSERTION — an understood
  // survivor rather than an oversight. Deleting it leaves this test GREEN
  // (measured), because the Approve click below auto-waits for a button that
  // exists only while the gen-2 gate carries evidence, which is the same
  // condition. It is not subsumed by the header above it, though: `generation
  // 2` is true the instant the revision OPENS, long before the second gate is
  // ready. What it buys is the failure MESSAGE — in a walk this long, "gate-1
  // was superseded" names the beat that stalled, where a bare "waiting for the
  // Approve button" could be any of four earlier things going wrong.
  await expect(gateRow).toHaveAttribute('data-state', 'waiting_decision', { timeout: 60_000 })

  // ---------------------------------------------------------------- BEAT 9
  // Approve. The row is still open — `toggle` writes `{phase}` alone, so an
  // open row always shows its node's newest attempt — so the controls that
  // appear are gen 2's, and no second navigation is needed.
  await page.getByRole('button', { name: 'Approve', exact: true }).click()

  // ---------------------------------------------------------------- BEAT 10
  // THE CLAUSE NOTHING SHADOWED: the patch is published and the pull request is
  // reachable. The backend oracle first, because "the run finished" is a
  // database fact and a screen can be behind it; `succeeded` is only reachable
  // through the publish node, so it is also the assertion that the publication
  // happened at all.
  await expect
    .poll(async () => (await runState(request, runId)).status, { timeout: 90_000 })
    .toBe('succeeded')

  // ...and the row a person reads. `EventDestination` renders the link only for
  // a payload whose `pr_url` passes an `^https?://` scheme guard, and its text
  // is `#<number>` — so this asserts the ANCHOR's href rather than any text
  // that happens to look like a URL. The host is the fake forge's, which is
  // what makes it the forge's answer rather than something the client composed.
  const prLink = page.getByTestId('event-log').getByRole('link', { name: /^#\d+$/ })
  await expect(prLink).toBeVisible({ timeout: 60_000 })
  await expect(prLink).toHaveAttribute(
    'href',
    new RegExp(`^${quote(FAKE_FORGE)}/${quote(repoSlug)}/pull/\\d+$`),
  )

  // ---------------------------------------------------------------- BEAT 11
  // THE OTHER UNSHADOWED CLAUSE: both decisions are in Approvals › History,
  // newest first, and the request_changes row carries its note.
  await page.getByRole('link', { name: 'Approvals' }).click()
  // A `tab`, NOT a `link`, even though it is an `<a>` with an href. An explicit
  // `role` REPLACES the implicit one, so `getByRole('link', …)` matches nothing
  // here — the same trap the ⌘K palette's input set when its explicit
  // `combobox` hid the textbox everyone expected. `ApprovalTabs` gives both
  // halves `role="tab"` deliberately: they are navigation, but they present as
  // a tablist.
  await page.getByRole('tab', { name: 'History' }).click()
  const rows = page.getByTestId('decision-history').getByRole('listitem')
  // ATTEMPT 2 ON THE APPROVAL, AND THAT IS MEASURED RATHER THAN ASSUMED. A
  // revision round does not restart the gate's attempt counter: opening
  // generation 2 supersedes the gen-1 phase rows and materializes the gate
  // again as attempt 2. Writing `attempt 1` here — the intuitive reading, that
  // each generation counts its own attempts — fails against a system that is
  // behaving correctly.
  await expect(rows.nth(0)).toContainText('approved gate-1, attempt 2.')
  await expect(rows.nth(1)).toContainText('requested changes on gate-1, attempt 1.')
  await expect(rows.nth(1)).toContainText(CHANGE_NOTE)

  // ATTEMPT-PRECISE, AND THIS IS THE ONLY FIXTURE THAT CAN SHOW IT. Two
  // decisions on ONE gate is what a revision round produces and nothing else
  // does, so this is the only run in the suite where "which attempt" is a
  // question with two answers. Run detail's spine collapses each node to its
  // NEWEST attempt, so a card that linked with `?phase=gate-1` and dropped the
  // rest of the triple would send both rows to the approval — and the note is
  // what tells the reader which one they got, since an approve carries none.
  await rows.nth(1).getByRole('link').first().click()
  await expect(page).toHaveURL(/generation=1/)
  await expect(page.getByTestId('phase-gate-1')).toContainText(CHANGE_NOTE)
})
