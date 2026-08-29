import { type APIRequestContext, expect, type Page } from '@playwright/test'

/**
 * What the specs share: signing in, and the six control-server calls that let a
 * spec set up and read the world the Go harness owns.
 *
 * NOT A `.spec.ts`, deliberately. Playwright's default `testMatch` collects
 * `**\/*.@(spec|test).?(c|m)[jt]s?(x)`, so this file is imported and never
 * collected — a helper file that ended in `.spec.ts` would be reported as a
 * suite with no tests, which is a green lane that ran nothing.
 */
const CONTROL = process.env.CAESAR_CONTROL_URL ?? ''

/**
 * Sign in and wait until the shell is up.
 *
 * The nav assertion is not decoration: `page.goto('/login')` returns as soon as
 * the document loads, and the click that submits the form resolves before the
 * router has replaced `/login` with `/`. Without something to wait on, the
 * caller's first locator races the redirect and fails against the login screen
 * — with a message about the element it wanted, which says nothing about why.
 * `getByRole('navigation')` is the shell's own landmark and only `_app` renders
 * one.
 */
export async function signIn(page: Page): Promise<void> {
  await page.goto('/login')
  await page.getByLabel('Email').fill(process.env.CAESAR_EMAIL ?? '')
  await page.getByLabel('Password').fill(process.env.CAESAR_PASSWORD ?? '')
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('navigation')).toBeVisible()
}

/**
 * Decide every leftover gate and wait for the run list to go quiet.
 *
 * ONE SERVER AND ONE DATABASE SERVE EVERY SPEC FILE (`webe2e_test.go` boots
 * once), so the approvals inbox is shared mutable state and a spec that opens a
 * gate leaves it waiting for whatever runs next. Every absolute assertion about
 * the badge — `toHaveCount(0)` at a first beat, `toHaveText('1')` after one
 * gate opens — is a claim about the whole world and is false the moment a
 * previous file's gate is still there.
 *
 * So each file that counts the badge starts from a known-empty inbox. The reset
 * goes through `runs.Service.DecideGate`, never SQL: see `e2eFixtures` for why
 * a hand-written write to a gate row is banned in this lane.
 *
 * The failure message carries the server's body because the interesting
 * failures are all in it — a gate that cannot be decided, a run that never
 * settles — and a bare "expected 200, got 500" would send the reader to the
 * wrong half of the stack.
 */
export async function quiesce(request: APIRequestContext): Promise<void> {
  const res = await request.post(`${CONTROL}/fixture/quiesce`)
  expect(res.status(), await res.text()).toBe(200)
}

/**
 * Open a gate through the backend transaction, and answer with the id of the
 * run it opened it on.
 *
 * THE RUN ID IS WHAT MAKES AN ASSERTION SCOPED. `getByTestId('gate-card')`
 * names every waiting gate on the screen, so it strict-fails as soon as two
 * wait and — worse — silently resolves the WRONG one when a leftover gate is
 * the only other. Callers filter the card by the run in its own link.
 */
export async function openGate(request: APIRequestContext): Promise<string> {
  const res = await request.post(`${CONTROL}/fixture/open-gate`)
  expect(res.status()).toBe(202)
  const body = (await res.json()) as { run_id: string }
  return body.run_id
}

/**
 * The labels the create-task form's three selects show, read off the seed
 * rather than repeated here. Renaming the seed repo in `webe2e_test.go` then
 * reddens nothing at all, instead of reddening this file for a reason that
 * looks like a UI bug.
 */
export async function seedLabels(
  request: APIRequestContext,
): Promise<{ project: string; repo: string; workflow: string; harness: string }> {
  const res = await request.get(`${CONTROL}/fixture/seed`)
  expect(res.status()).toBe(200)
  return (await res.json()) as {
    project: string
    repo: string
    workflow: string
    harness: string
  }
}

/**
 * What the BACKEND says about one workflow — `latest_version`, read through
 * `workflows.Service` rather than off the screen.
 *
 * IT DOES NOT GO THROUGH THE APP'S OWN API, and the reason is worth keeping:
 * Playwright's `page.request` shares the browser's cookie jar but honours
 * `Secure` strictly, while browsers exempt localhost — so the `__Host-` session
 * cookie never rides a Node-side request and `GET /api/v1/workflows/{id}` from
 * a spec answers 401 with the UI perfectly signed in. Measured, on this file's
 * first gate run.
 */
export async function workflowState(
  request: APIRequestContext,
  workflowId: string,
): Promise<{ name: string; latest_version: number }> {
  const res = await request.get(`${CONTROL}/fixture/workflow/${workflowId}`)
  expect(res.status(), await res.text()).toBe(200)
  return (await res.json()) as { name: string; latest_version: number }
}

/**
 * What the BACKEND says one run reached — the oracle for a claim the DOM can
 * fake. `run-header` rendering "generation 2" is a statement about a template;
 * this is a statement about the database, read back through `runs.Service`.
 */
export async function runState(
  request: APIRequestContext,
  runId: string,
): Promise<{ generation: number; status: string }> {
  const res = await request.get(`${CONTROL}/fixture/run/${runId}`)
  expect(res.status(), await res.text()).toBe(200)
  return (await res.json()) as { generation: number; status: string }
}

/**
 * Point the code-host doubles at a repository that was registered THROUGH THE
 * BROWSER, and answer with the head they will now serve for it.
 *
 * A repo a spec created in the UI exists in the database and nowhere else: the
 * fake forge has no head to pin as its base SHA, the fake publisher has nowhere
 * to push, and the fake executor — with no remote for it — falls back to the
 * synthetic patch that `gitops.Publish` refuses. This is the one call that
 * makes such a repo runnable, and it must precede the first task on it.
 *
 * IT TAKES THE PROJECT NAME AND THE SLUG, not an id, and the reason is the
 * `Secure`-cookie fact this file already records twice: a spec cannot ask the
 * app's API for the id, and the id is not in the DOM either. The Go side
 * resolves the pair through `assets.Service`.
 */
export async function registerRemote(
  request: APIRequestContext,
  project: string,
  slug: string,
): Promise<{ repo_id: string; head: string }> {
  const res = await request.post(`${CONTROL}/fixture/register-remote`, {
    data: { project, slug },
  })
  expect(res.status(), await res.text()).toBe(200)
  return (await res.json()) as { repo_id: string; head: string }
}
