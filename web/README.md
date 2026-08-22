# web

The caesar control plane's browser client: Vite, React 19, TypeScript in strict
mode, TanStack Router and Query, Tailwind over an OKLCH token layer. It builds
into `../backend/internal/webui/dist`, which the Go binary embeds under the
`embedui` build tag — there is no copy step and no second location, because
`//go:embed` cannot reach outside its own package.

Every lane runs through `task` from the repository root. Nothing here is meant
to be run by remembering a command.

## The Node pin, and why it is not a formality

`web/.nvmrc` says `24.19.0`. That number is repeated in `package.json`'s
`engines.node`, and both are load-bearing.

The failure it prevents is silent and it is expensive. `scripts/gate` used to
resolve whatever Node was first on the calling shell's PATH; on the machine this
was built on that default is 20.9.0, which brings npm 10.1.0, which hits
npm/cli#4828 and installs a tree with **no `@rolldown/binding-<platform>`**.
Vitest 4 and Vite are both built on rolldown, so both die at once and the tree
reads as a broken repository rather than as one wrong Node version. That
destroyed `web/node_modules` on four separate gate runs. It is not only the
install: with a tree installed correctly under 24, `./node_modules/.bin/vitest`
still dies under 20.9.0 with `SyntaxError: The requested module 'node:util' does
not provide an export named 'styleText'`. Both of those are measured, and the
measurements are recorded in `scripts/with-node`'s header comment.

Two mechanisms hold the pin, and they cover different paths:

- **`scripts/with-node`** prefixes every Node-using lane. It puts
  `$HOME/.nvm/versions/node/v<pinned>/bin` first when that directory exists (in
  CI it does not, and `actions/setup-node` has already done the job), then
  *asserts* `node -v` and refuses to exec the command if it does not match. It
  is a script rather than an inline `PATH=… cmd` prefix because `scripts/gate`
  asserts the resolved command line of every lane character-for-character, and a
  machine-specific absolute path in each of those lines would make that
  assertion unmaintainable. A Taskfile-level `env: PATH:` was tried twice and
  does not work: go-task honours injected environment variables but resolves the
  command against the parent process environment, and the injected PATH does not
  reach the child either — so even a tool invoked by explicit path runs its
  `#!/usr/bin/env node` shebang against the old PATH.
- **`web/.npmrc`'s `engine-strict=true`** is the tripwire underneath, for every
  path that does not go through a task. Without it, `engines` is advisory: npm
  prints `EBADENGINE … current: { node: 'v20.9.0' }` and then installs anyway,
  which is precisely how the corruption above happened four times.

If you hit `EBADENGINE`, install the pinned Node (`nvm install`, run from
`web/`, which reads `.nvmrc`). **Do not delete `package-lock.json`** — npm's own
error text suggests it, and it would unpin all 34 versions that `task web:pins`
exists to guard.

The `scripts` block in `package.json` (`npm run dev`, `npm run test`, …) does
**not** go through `scripts/with-node`. Running those directly puts you back
under whatever Node is first on your PATH, with no assertion — which is exactly
the condition described above. Prefer the `task` targets; the npm scripts are
there for editor integrations that insist on them.

## The dev loop, and why the proxy is not optional

```
task db:up                     # docker compose -f compose.dev.yaml up -d --wait
task backend:run               # go run ./cmd/caesar serve -config caesar.dev.json  (:8080)
cd web && ../scripts/with-node ./node_modules/.bin/vite      # :5173
```

Never `npx` and never `npm exec --`. Both reach the public registry when the
binary is not present locally, and both have been measured on this repo exiting
0 having done nothing useful: `npx biome ci .` emitted zero bytes and passed,
and `npm exec -- tsr generate` downloaded and ran "TypeScript Remove" instead of
the router CLI. Every lane in `Taskfile.yml` invokes the installed binary by
path for that reason, and a missing install then fails loudly — `../scripts/with-node:
…/web/node_modules/.bin/<tool>: No such file or directory`, from the `exec` at
the end of `with-node`. Note it comes from bash inside `with-node`, not from
go-task and not from the tool: because every lane's first word is
`../scripts/with-node`, go-task resolves a binary that exists and never emits
its own `stat …: no such file or directory`.

**Before step 3 works you must add `"app_origin": "http://localhost:5173"` to
the `"server"` block of `backend/caesar.dev.json`.** This is measured, not
predicted. `web/vite.config.ts` proxies `/api` to `http://127.0.0.1:8080` with
`changeOrigin: false`, and the proxy does not rewrite the browser's `Origin`
header — `changeOrigin` rewrites `Host`, not `Origin` — so the backend sees
`Origin: http://localhost:5173`. `httpx.CheckOrigin` rejects an unsafe method
whose present `Origin` differs from the configured app origin by exact string
comparison, and `caesar.dev.json` sets no `app_origin` today, so `config.go`
defaults it to `http://localhost:8080`. Measured against the real `Routes`
chain: `POST /api/v1/auth/login` with `Origin: http://localhost:5173` returns
403 `{"type":"/errors/forbidden",…,"detail":"cross-origin request rejected"}`,
while the same request with `Origin: http://localhost:8080` or with no `Origin`
passes. An operator who does not know this will read the 403 as a credential or
a cookie problem. The backend has exactly one app origin, so the Vite loop and
the embedded loop (the bundle served from `:8080`) need different values and no
single default serves both.

The proxy's *other* justification — that routing `/api` through the Vite origin
is what keeps the `__Host-` session cookie alive, because `__Host-` requires
`Secure`, browsers treat `http://localhost` as a secure context, and a
same-origin session is required for the prefix's rules to hold — is **reasoned,
not measured.** Nothing in any automated lane exercises it: it needs a real
browser's cookie jar and no test here has one. `web/vite.config.ts` carries the
seven-step manual procedure that would settle it, headed `STATUS: REASONED, NOT
YET MEASURED`, ending in deleting the proxy block and pointing the client at
`http://127.0.0.1:8080` directly. If that step does **not** drop the cookie, the
block's cookie justification is wrong and should be rewritten rather than kept
"just in case". Until someone records that outcome, treat the cookie claim as
unproven and do not quote it as an observation.

## The screens

Plan 11 built the shell and one real screen. Plan 12a filled in the critical
path — **Home, Runs, Tasks and the approval surface** — and added the `/more`
tab that lets the phone's five-tab bar reach every page the desktop rail shows.

`src/shell/nav.tsx` holds two lists, and they are deliberately different.
`PHONE_TABS` is the tab bar: Home · Runs · Approvals (badged) · Tasks · More.
`SECONDARY` is Workflows · Repos · Admin. The desktop rail is
`PHONE_TABS.slice(0, 4)` plus `SECONDARY`, and `/more` renders `SECONDARY`
itself, so a page added to the app cannot reach the rail and miss the phone.
**Both presentations are one `<nav>` switched by CSS**, not two elements: jsdom
applies no media queries, so a second `<nav>` would make the `navigation` role
ambiguous in every component test that looks for one. The consequence to know
before writing a test here is that both presentations are always in the DOM.
Under jsdom every query for a nav *link* matches twice. In Playwright the rail
is `display:none` at the only viewport the project runs (Pixel 7 is 412px wide
and `md:` is 768px), so a role query matches one link — a `display:none` subtree
leaves the accessibility tree the role engine reads — while `getByTestId`, a
plain attribute selector blind to display, still matches two. Three hiddenness
models sit in that one paragraph and no one of them subsumes another, which is
why `e2e/approvals.spec.ts` opens with three assertions and a distinct measured
mutant for each. It is **the only place in the tree where `hidden` / `md:hidden`
are falsifiable at all** — jsdom loads no Tailwind, so every component test
would pass against a rail that is visible on a phone.

**Home (`/`) suspends on nothing, and that is a fix rather than a preference.**
Its loader is `Promise.allSettled` over the approvals and runs queries, and each
section — the status line, "Needs you", "Running", "Recent" — reads its own
query's state and renders what it could not read, with the correlation id. The
shape originally specified (`ensureQueryData` + `useSuspenseQuery`) hangs the
app: `/` is where `/login`'s guard sends every authenticated visitor, so an
endpoint answering 401 while `/auth/me` stays healthy sends Home's loader into
`_app`'s error boundary, the boundary navigates to `/login`, the guard proves
the session and sends the visitor back to `/`. Measured: 44 seconds, then
`Ineffective mark-compacts near heap limit`. `src/routes/_app/index.tsx`'s
header carries the argument in full.

**Runs is two sibling routes, and the file names are the reason.**
`routes/_app/runs.index.tsx` is the list and `runs.$runId.tsx` the detail. A
`runs.tsx` beside `runs.$runId.tsx` makes `tsr generate` emit the list as the
detail's *parent layout* — and a list that renders no `<Outlet/>` then swallows
the detail screen whole. Filters and the open phase are both URL state:
`?status=` on the list, `?phase=` on the detail. A `?status=` the endpoint does
not know is not clamped to "no filter" here; the server answers 400 naming the
field and that reaches the layout's error boundary, because showing every run to
somebody who asked for one kind is the failure that looks like success.

Run detail renders one timeline in two presentations: a disclosure list on the
phone, and a `phase-rail` (`hidden … md:flex`) with the open phase's detail
beside it on a wide viewport. **What is duplicated is each phase's one-line
summary; the body renders once**, inside the pane — the obvious two-subtree
build was measured and dropped, because its unscoped screen queries
double-match. The phone row is a `<button>` with `aria-expanded` (it owns what
it opens); the rail row is a `<Link>` that *sets* `?phase=` (it selects what
fills a pane elsewhere, and that is a destination somebody can copy). Below the
timeline sit the approval surface for a waiting gate, the publication panel, and
a **flat, chronological** event log. Flat is a contract fact rather than a
layout choice: `ApiRunEventResponse` carries no `node_id` column — the node
lives inside the untyped payload — so grouping would depend on a field
`contracts:check` cannot police.

**Tasks (`/tasks`) is four attention groups over seven run statuses.** `tasks`
has no status column; `TaskResponse.Status` *is* the latest run's status, so the
groups — Needs you · In flight · Up next · Recently done — are projections of
run state and the mockup's `draft → ready → in review` lifecycle has nothing
behind it. Two entries in `src/tasks/task-groups.ts` are not obvious and both
are deliberate: `pending` is *Up next* (a group that is empty most of the time
on a single-operator system, which is why its empty state is designed rather
than defaulted), and `superseded` is *Recently done* rather than skipped —
skipping takes the whole task off the list, because a task's status is its
newest run's. The default arm is not padding: a status added server-side must
degrade to visible, since a task that vanishes leaves nothing on screen to be
wrong about. The FAB opens the create form in a `vaul` drawer.

**The gate decision kit is built once and hosted three times.**
`src/gates/gate-controls.tsx` renders all three decisions or none;
`src/approvals/approval-surface.tsx` wraps it with the summary, the hard facts
and the examined diff. Home hosts `GateControls` directly, and run detail and
the full-screen `/approvals/$runId/$nodeId` both host `ApprovalSurface`. Because
three screens inherit the same refusal rules, those rules are asserted by a
shared testkit (`src/gates/decision-rules.testkit.ts`) that each host re-runs
against its *own* mount rather than trusting the component's own suite. It ends
with a `cleanup()` that unmounts the host's whole tree — worth knowing before
you add assertions after a call to it.

## Two things a next reader will otherwise get wrong

Both are backend facts, and both change what this client can assume.

**`phase.` is on the global attention stream now.** `attentionPrefixes`
(`backend/internal/realtime/realtime.go`) is `gate.`, `publish.`, `run.` and, as
of Plan 12a, `phase.` — which means every kind `runs/events.go` defines is
attention-worthy. It joined because live phase dots and a live run timeline need
transitions, and the alternative — a per-run stream on run detail — would either
open a second connection against the one-stream-per-tab invariant or replace the
global one and silently kill the cross-run approvals badge on the very screen
where a decision is about to be made.

**What it costs is real, and the client is what pays it.** A run in flight emits
far more phase transitions than gate or publish events, and
`phase.log_truncated` / `phase.egress_denied` are capped per attempt rather than
rare. The 250ms debounce in `src/realtime/cache-bridge.ts` is what turns that
into one invalidation per window instead of one per frame, so **that debounce is
load-bearing rather than an optimization** — changing it is a product decision
about refetch volume, not a tuning knob. `ATTENTION_KINDS` in the same file did
*not* gain `phase.*` and must not: it decides one thing, whether the window also
invalidates `['approvals']`, and the inbox does not change when a phase ticks. A
kind added there that the server emits in bulk turns a chatty run into a refetch
loop on the one query rendered on every page.

The widening also has a consequence that is easy to miss on the server side: a
fresh stream primes from the most recent N *matching* events, so phase traffic on
a busy run can push older gate and publish events out of the window a client is
primed with. The snapshot has never been a history — the client fetches its
lists over HTTP and the stream only invalidates them — but it is why
`SnapshotEvents` is a configured number rather than a small one.

**Document schema 2 is what turns artifact payload validation on.**
`ValidateArtifactPayload` (`backend/internal/runs/payload.go`) returns nil for
any document below `payloadSchemaVersion`, which is 2 — so a workflow definition
at schema 1 is validated exactly as it was before Plan 12a, and one at schema 2
has its `plan` and `review_verdict` payloads checked against a required-key set.
The same pin decides `OutputShapeInstruction`, which is appended to a node's
instructions: **the prompt a node is given and the validator that judges it read
the same constant**, and the failure being avoided is a node instructed in one
shape and judged against another. That is why the typed payloads
(`ApiPlanPayload`, `ApiReviewVerdictPayload`) the approval surface reads are
worth anything at all — below schema 2 nothing enforces them, and a stub must
not render an empty block that reads as a summary. Unknown keys are allowed
through deliberately: the envelope is written by our runner, payloads are
written by an LLM harness, and a harness that adds a useful key must not fail a
run.

## What `task web:arch` enforces

`web/tools/arch/main.ts` runs three checkers under
`node --experimental-strip-types` — the type stripping is the runtime's, not a
compiler's, which is another place the Node 24 requirement is explicit. Each
violation prints as `file:line: message`, then a count, then exit 1. A clean run
prints `web:arch: clean`.

These are the rules no per-file linter can express, because each needs the whole
import graph or the whole token vocabulary:

**Misplacement** — a module with two or more direct consumers must live at their
nearest common ancestor directory, clamped to the module's own top-level group.

```
routes/a/helper.ts:1: routes/a/helper.ts has 4 consumers (routes/a/page.ts, routes/b/page.ts, routes/c/page.ts, routes/d/page.ts) so it must live in routes
```

Read it as an instruction: move the named file to the named directory. A
"consumer" is a direct importing module, static or dynamic, `@/`-aliased or
relative, excluding tests, stories and generated files. The clamp is the rule's
scope, not a softening: `api/`, `realtime/`, `forms/` and `ui/` are declared
permanent homes, so a module already at its group root is never reported, and a
module below it is told to move up to that root at most — never out of its
group. Without the clamp the checker demands that `api/errors.ts` move to `src`
because its consumers span four groups, which contradicts the structure it is
supposed to enforce.

**Cross-group direction** — two edges are banned, because only two are
load-bearing.

```
api/bad-client.ts:1: api/ must not import routes/ — the client is the bottom of the stack and must not depend on the chrome
ui/bad-widget.tsx:1:  ui/ must not import api/ — ui is presentational; a component that fetches belongs to a route
```

`ui/` may not import `api/`, `realtime/`, `auth/` or `routes/`; `api/` may not
import `routes/`, `shell/` or `ui/`. The fix is always to move the code, never
to relax the matrix — the line number points at the offending import.

**Colour vocabulary** — the third checker (`tools/arch/tokens.ts`) validates
*names* rather than hunting literals, which is the whole design: `<div
className="bg-white text-red-500" />` bypasses every semantic token and contains
no hex, no `rgb()`, no `oklch()`, so there is nothing for a literal search to
find. The theme resets Tailwind's colour namespace, which makes a name check
sufficient. Three shapes:

```
bad.tsx:4: non-semantic colour utility "bg-white"
bad.css:1: literal colour value; use a semantic token
bad.css:3: named CSS colour; use a semantic token
```

The permitted names are the `SEMANTIC` list in `tokens.ts` — a hand-copy of the
`--color-*` declarations in `src/styles/tokens.css`, held against it by
`arch.test.ts`, since silent drift would make the checker reject legitimate
utilities. `bg-accent/10` is the token at 10% opacity and is fine; `bg-[#fff]`
and `bg-(--some-var)` are the deliberate escape hatch out of the token system
and are therefore the bug. The theme files and tests are skipped: they are where
colour is allowed to be spelled out. Known over-firing, recorded rather than
fixed: arbitrary non-colour values (`text-[13px]`, `shadow-[0_1px_2px]`) trip
the utility pass, and because the literal passes are line-based, a comment in
`src/` that merely names a banned utility turns the lane red.

## The E2E lane, and the two things it fakes

`task web:e2e` is `go test -tags "integration,webe2e,embedui" -count=1 -timeout
15m ./internal/app/ -run TestWebE2E`, run from `backend/`. The harness is
`backend/internal/app/webe2e_test.go`; the browser half is three specs under a
single mobile project (Pixel 7) — `approvals.spec.ts` (the badge arriving live),
`gate-decision.spec.ts` (opening a gate to its evidence and approving it, and a
`request_changes` round that opens a new generation), and `task-timeline.spec.ts`
(creating a task from the FAB and watching its timeline advance). `e2e/harness.ts`
holds what they share and is deliberately **not** a `.spec.ts`: Playwright's
default `testMatch` would collect it as a suite with no tests, which is a green
lane that ran nothing.

It boots the **production** `runServe` against a real Postgres. The listener is
a seam rather than a fake: the test binds `127.0.0.1:0` itself and hands the
`*net.TCPListener` in, because `AppOrigin` must be known before boot
(`CheckOrigin` compares the browser's `Origin` by exact equality and a browser
always sends the port) and port 0 keeps the lane from colliding — production
`Serve` receives the same listener it would have bound.

**What is faked is `runs.Executor` and a forge stub, and nothing else.**
`Routes`, every handler, the Orchestrator, `materializeNode`, `appendEvents` —
ordered ids under the counter lock, the outbox envelope and the `NOTIFY`, all in
one transaction — the tailer, the hub and the SSE wire are all real. The seed
builds its world through the real services (`identity.Bootstrap`, `assets`,
`workflows`); nothing in it writes SQL. The forge stub exists because
`resolveIntake` calls `ResolveHead` on the way to every task; it answers the head
SHA of the same bare repository the executor is pointed at.

The executor substitution exists because of a workflow invariant, not for
convenience. A gate cannot be a root node: `workflows.validateConsumption`
requires a gate to examine exactly one plan or patch. So the only way to reach a
*waiting* gate is for an upstream agentic node to have produced a patch, and the
fake executor is what produces one without a container, a harness image or a
model call.

**The fake is pointed at a real bare repository, and that is load-bearing rather
than tidy.** The fake has two implement branches: with no remote registered it
emits a historical *synthetic* patch whose `diff` is raw text, while the wire
form is base64 (`fake/git.go`, `collect.go`) because a patch is arbitrary bytes
and JSON string encoding would mangle a file in a legacy encoding silently. The
client decodes accordingly (`src/patch/decode-patch.ts`, `atob` then
`TextDecoder`) and returns an empty patch when that throws. Measured: against
the synthetic patch the approval surface rendered `0 files · +0 · −0` and no file
rows at all for a patch that adds a line, so the gate screen showed nothing to
examine. A lane whose acceptance path is "open the gate and read what it
examines" cannot run against a payload shape production never produces. So
`fake.InitBareRepo` runs first and the remote is registered — which makes **`git`
on `PATH` a dependency of this lane**, alongside Docker.

**Raw SQL insertion of a gate row is banned, and `webe2e_test.go` must never
grow one.** It is the obvious shortcut and it is the wrong one: `appendEvents`
allocates the event ids that the resume path depends on, so a hand-written
`INSERT` desynchronises the counter and leaves later resume behaviour broken in
a way no assertion in this lane would catch. The lane would stay green while
quietly certifying a database state the system can never actually be in. **The
ban covers the reset as much as the setup** — `/fixture/quiesce` decides
leftover gates through `runs.Service.DecideGate`.

A control server (`httptest`, never part of caesar's routes) exposes four
endpoints: `POST /fixture/open-gate` (which answers with the run id it opened
the gate on), `GET /fixture/seed`, `GET /fixture/run/{id}` and
`POST /fixture/quiesce`. Playwright calls `open-gate` *between* assertions, and
that ordering is the point: the gate opens while the browser is already
watching, so the badge moving is attributable to the SSE stream and not to a
page load. `GET /fixture/run/{id}` is the **oracle** for the revision path — "a
new generation opened" is a backend fact, and asserting it only from the DOM
would let a screen that re-rendered the same generation pass. The knob that
keeps the lane fast is `Orchestrator.PollInterval: 200ms`.

**One server and one database serve every spec file, which is why
`playwright.config.ts` sets `workers: 1`.** The approvals inbox is a global
count over that one world, so every absolute badge assertion is a claim about
the whole world and is false the moment a previous file's gate is still waiting.
The two files that count the badge call `quiesce` first; `task-timeline.spec.ts`
deliberately does not, because every assertion in it is scoped to the run it just
created. The default worker-per-file would have three files opening and deciding
gates in each other's counts, and each worker's `quiesce` clearing the others'
gates.

`timeout: 120_000` is there because Playwright's 30s default silently caps every
`{ timeout: 60_000 }` an assertion states — a stated timeout that cannot outlive
the test containing it is a fiction. It does not cover their *sum*:
`task-timeline.spec.ts` states 150s across three waits, so a test that spent
every one of them in full is still cut off. That is a test already failing, and
a ceiling above 150s would put hung tests within reach of the lane's own 15m,
where the failure arrives as a killed lane with no Playwright report rather than
as a named assertion.

Three honest limits. The specs walk the happy paths and no failure mode — a
stream that never opens, a `forbidden` frame, a 500 from `/approvals` are all
unexercised, because the lane's cost is a browser download and a Docker
Postgres. `Reject` is the one gate decision no spec walks. And the lane never
*asserts* that the executor substitution took; it infers it, on the grounds that
`NotImplementedExecutor` cannot produce a patch, so a succeeded `impl` node could
only have come from the fake. True, but reasoning rather than an assertion.

When it fails, read the output in this order: Playwright's own report, then
`e2eDiagnose`'s `phase_runs` dump (a gate that never reached `waiting_decision`
is a backend or seed fault; one that did is a client fault), then the server
log.

## The lanes

| Target | What it runs |
| --- | --- |
| `web:install` | `npm ci` |
| `web:pins` | `tools/check-pins.mjs` — every dependency, override and resolution must be an exact version; ranges fail the lane |
| `web:lint` | `biome ci .` |
| `web:arch` | `tools/arch/main.ts` — the three rules above |
| `web:typecheck` | `tsc --noEmit`, then `tools/check-mutants.mjs`, which compiles deliberately-broken fixtures and **fails when they compile** |
| `web:test` | `vitest run` — two projects: `node` (`tools/**/*.test.ts`, no DOM globals) and `dom` (jsdom + the React plugin, `src/**`) |
| `web:routes:check` | `tsr generate`, then `git diff --exit-code` on `src/routeTree.gen.ts`, plus a `git status --porcelain` companion, because `git diff` is blind to an untracked or already-staged path and the drift gate would be vacuous exactly when the generated tree is not committed |
| `web:build` | `vite build` into `../backend/internal/webui/dist` |
| `web:ci` | The seven above, in that order — `web:pins` first because a drifted pin makes every later failure a red herring, `web:build` last because it is the slowest and the only one another lane consumes |
| `web:e2e` | The Go harness described above |

`scripts/gate` runs `task web:ci` and then `task web:e2e`, after
`contracts:check` — `contracts:ts` regenerates `web/src/api/schema.d.ts`, so a
web lane running first would be checking the previous generation.

`web:e2e` deliberately has **no `deps:`**, unlike `backend:test:embedui`. It
needs two prerequisites, and one of them is a roughly 95MB browser download that
no lane should be able to trigger by accident on a developer's machine or in a
job that never declared it. Run them by hand first:

```
cd web && ./node_modules/.bin/playwright install chromium     # --with-deps on Linux; CI uses that form
task web:build
```

No `task db:up`: the harness gets Postgres from `internal/platform/testdb`,
which starts its own `postgres:18-alpine` testcontainer and migrates it from
scratch. Docker must be running; a compose stack need not be.

Both prerequisites fail legibly if you skip them — a missing bundle fails at
compile time, since `//go:embed all:dist` has nothing to embed, and a missing
browser fails inside Playwright with its own install instructions. Neither is
silent, which is why the ordering is documented rather than automated.
