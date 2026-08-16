# Ledger — Plan 11 (web foundation), execution

**Branch `caesar-11-web-foundation`. `main` untouched.** Started 2026-08-15 from `9bc191b` (the
reviewed plan). Verify this against `git log` before acting on it — a stale handoff has cost a
full session on this project.

## Where execution stands

| Wave | Task | Commit | Gate |
|---|---|---|---|
| 1 | 4 — contracts: the slice's real answers + required fields | `8869d25` | — |
| 1 | 1 — scaffold, pinned toolchain, falsifiable route typing | `c0b8b35` | **PASS** |
| 2 | 2 — semantic OKLCH theming, Tailwind's palette removed | `703b05f` | **PASS** |
| 3 | 9 — pure incremental SSE frame parser | `71b8d4c` | — |
| 3 | 5 — generated client, `ApiError`, 401 interceptor | `dfdc667` | — |
| 3 | 3 — `task web:arch` | `940a428` | — |
| 3 | — plan corrections from what wave 3 measured | `144afe9` | **PASS** |
| — | this ledger + the Task 3 plan-snippet correction | `a8bb138` | docs only |
| 4 | 6 — typed form abstraction + `/login` | `a987927` | — |
| 4 | 10 — SSE transport, backoff/resume/watchdog/probe | `43143b2` | — |
| 4 | 12 — embedded bundle behind `embedui`, CSP on | `acec71d` | **PASS** |
| — | wave-4 plan corrections + ledger entry | `a4be81a` | docs only |
| 5 | 7 — session bootstrap: `_app` guard, error panel, `tsr.config.json` | `8a40eae` | **PASS** |
| — | wave-5 plan corrections + ledger entry | `def3554` | docs only |
| — | the `scripts/gate` / `node_modules` diagnosis | `30bd099` | docs only |
| 6 | 8 — app shell, the V1 page map, the connection indicator | `5073519` | **PASS** |
| — | wave-6 plan corrections + ledger entry | `ea92410` | docs only |
| 7 | 11 — invalidate-only cache bridge, the real `/approvals` | `63bbb1f` | **PASS** |
| — | wave-7 plan corrections + ledger entry | `82e126f` | docs only |
| 8 | 13 — the E2E lane, four beats against the real backend | `12c7d35` | **PASS** |
| — | wave-8 plan corrections + ledger entry | `7c2c5f6` | docs only |
| 9 | 14 (part) — the node_modules blocker, FIXED at the root | `beee9ba` | **PASS** |
| 9 | 14 (part) — the web lanes folded into the gate | `116ffdc` | **PASS (21 lanes)** |

**Done: Tasks 1–13, and Task 14's Steps 4–5. Remaining: Task 14 Steps 1–3 and 6** — the CI
workflow (the `web` job, build-once/test-that-digest/promote-that-digest, and the mutation proving
the image assertion can fail), plus the roadmap row and `web/README.md`.

The gate is `scripts/gate <abs-worktree> <sha> <branch>` and it ran green at each wave boundary,
not after each task. Within a wave, tasks are committed in dependency order and their individual
commits' greenness is asserted by construction rather than measured — stating that plainly because
the plan's own rule is per-task.

All web lanes green at `116ffdc`: `web:pins` (34 pinned), `web:lint`, `web:typecheck` (tsc + the
route-inference mutant), `web:arch`, `web:test` (**178** tests, 16 files), `web:routes:check`,
`web:build`. Both Go twins green: `go test ./internal/...` and `go test -tags embedui
./internal/webui/`. `task web:e2e` green too.

**The gate now runs all of them itself — 21 lanes.** Up to and including wave 8 it ran four
backend lanes, so every web lane had to be run by hand and, worse, re-run AFTER the gate, because
the gate corrupted `node_modules` on the way through. Both facts are now historical: the
corruption is fixed at its root (below), and a full gate run leaves `web:test` green with no
repair. Anyone reading an older revision of this file will find the opposite instruction there.

### ~~DIAGNOSED~~ — **FIXED in `beee9ba`**, and the fix is measured both ways

The diagnosis below stood for four waves and is kept because the mechanism is worth knowing. What
closed it, and what the diagnosis got slightly wrong:

- **It is NOT only the install.** With a `node_modules` installed correctly under 24,
  `./node_modules/.bin/vitest` still dies under node 20.9.0 — `SyntaxError: The requested module
  'node:util' does not provide an export named 'styleText'`, because rolldown needs a newer Node.
  So every node-using lane needs the pinned runtime, not merely the ones that say `npm`.
- **`env: PATH:` in the Taskfile does not work, and that was measured rather than assumed.**
  go-task honours injected env vars (a canary arrives intact) but resolves commands against the
  PARENT environment, and the injection does not reach the child either — a tool invoked by
  explicit path still ran its `#!/usr/bin/env node` shebang against the old PATH. A global
  `env: PATH:` looks correct and changes nothing.
- The fix is **`scripts/with-node`**, which every node lane now goes through. An inline
  `PATH="…" cmd` also works but bakes a machine-specific nvm path into every echoed command line —
  and `scripts/gate` asserts those lines character-for-character, so the wrapper is what keeps them
  assertable. `web/.npmrc`'s `engine-strict=true` is the tripwire underneath.
- **Both halves are falsifiable.** `with-node` exits 1 naming the pin when it cannot resolve it
  (measured with a HOME that has no nvm), and a bare `npm ci` under node 20 now refuses with
  `EBADENGINE` leaving the tree intact, where it used to install a broken one.

**Acceptance criterion met**: a full `scripts/gate` run from a node-20 shell now leaves
`@rolldown/binding-darwin-arm64` in place and `web:test` green (178/178) with no `npm ci` repair —
the first time in this plan's history. The gate now runs **21 lanes**, web and E2E included.

### The original diagnosis — `scripts/gate` is what breaks `web/node_modules`, every single run

The rolldown-binding failure that ate an hour in wave 4 and recurred in wave 5 is not npm being
flaky and not a stale checkout. **`scripts/gate` causes it, deterministically, and this is
measured, not inferred:**

1. `scripts/gate` runs `task contracts:check`, which depends on `contracts:ts`, whose first
   command is **`npm ci` in `web/`** (`Taskfile.yml:234-240`).
2. go-task spawns that through `sh`, which on this machine resolves node to **v20.9.0 / npm
   10.1.0** — not the nvm 24.19.0 every web lane needs and not the login shell's node either.
   The gate scrubs `PKG`/`GOFLAGS`-style variables but **does not touch `PATH`**.
3. npm 10.1.0 hits the optional-dependency bug (npm/cli#4828) and installs a tree with no
   `@rolldown/binding-darwin-arm64`. **vitest and vite both die**, because vitest 4 is built on
   rolldown — so it presents as a catastrophic repo failure rather than one missing package.
4. `web/package.json` DOES declare `"engines": { "node": "24.19.0" }`, and npm prints
   `EBADENGINE … current: { node: 'v20.9.0' }` and **installs anyway**. The constraint exists and
   enforces nothing.

Proof: binding present and 113 tests green → `scripts/gate` (PASS) → `@rolldown/` holds only
`pluginutils`. Reproduced FOUR times (waves 4, 5, 7, 8). `npm ci` under npm 11.17.0 *or* 11.6.2 restores it and leaves
`package-lock.json` byte-identical; the error message's own advice — delete the lock — would
unpin all 34 versions `web:pins` exists to guard, and must not be followed.

~~**This is Task 14's problem before it is anyone else's.**~~ **CLOSED in `beee9ba` / `116ffdc`.**
The concern was exact: wired naively, `contracts:check` corrupts `node_modules` and the web lanes
then run against the wreckage inside the same gate invocation. That is why the blocker was fixed
BEFORE the lanes were folded in, and why the gate orders them after `contracts:check`.

The recommended `engine-strict=true` was necessary but **not sufficient on its own** — it converts
a silent corruption into a hard refusal, which would have left the gate failing rather than green,
so it does not meet its own stated acceptance criterion without something that actually supplies
the pinned Node. sofo's ruling (2026-08-16) was lanes-self-resolve **plus** engine-strict, which is
what shipped.

## The dependency graph, which is mostly a chain

`4 → 1 → 2 → {3, 5, 9} → {6, 10, 12} → 7 → 8 → 11 → 13 → 14`

Only the two braces parallelize; the file sets inside each are disjoint. Tasks 4 and 1 cannot
overlap — Task 4 regenerates `schema.d.ts` out of `web/node_modules` while Task 1 rewrites
`package.json` and reinstalls that same tree.

**Both braces are now done. Everything left is strictly serial**: the rest of Task 14 alone.
There is no more parallelism to find in this plan.

Wave 5 (Task 7) was executed **inline by the orchestrator**, not through the per-wave Workflow the
operating model below describes. With one task left per wave there is no fan-out to buy, and the
quality bar it existed to enforce — write the test, watch it fail, then mutate every guard and
watch it die — was applied directly. Three of this task's findings came out of that mutation pass
and would not have come out of a green suite.

## What Task 13 inherits from wave 7, and must not undo

- **`web/src/test/render-app.tsx` is the harness now**, and `_app.test.tsx` is refactored onto it.
  Handlers are PASSED to it, never installed with a bare `server.use` beforehand — msw ranks a
  later `server.use` call above an earlier one, so a test that stubbed a 401 itself would silently
  get the harness's authenticated default and still pass. Within one call the EARLIER argument
  wins, which is why the defaults are spread last.
- **Three defaults are installed on every render**: an authenticated `/auth/me`, an empty
  `/api/v1/approvals`, and an open-but-silent `/api/v1/stream`. The third is load-bearing — the
  protected layout now opens a connection on every mount, and `onUnhandledRequest: 'error'` turns a
  missing stream handler into a failure far from its cause.
- **`pendingStream()` exists for one reason**: `silentStream` has already answered by the time
  anything renders, so any assertion about the connection state is vacuous against it. Measured.
- **`AppLayout` holds the badge query.** `/approvals` and the layout both observe `['approvals']`,
  so a test that counts requests to that endpoint is counting two observers, not one.
- **The stream is inside the protected layout, deliberately.** A visitor with no session opens none,
  and `_app.test.tsx` asserts it.

## What Task 11 inherited from wave 6, and did not undo

- **`web/src/routes/_app/approvals.tsx` EXISTS as a `StubPanel`.** Task 11 replaces its body. It
  is component-only today; giving it a loader is the point of Task 11, and it is also what finally
  makes `AppErrorBoundary`'s 401 branch testable (see the untested-branch list).
- **`AppShell` takes `connection` and `approvalCount` as OPTIONAL props**, defaulting to `'live'`
  and `0`. `AppLayout` passes neither yet. Task 11 wires the real values in, and both defaults
  have a test asserting the silent case — a shell that warned before the transport had spoken
  would be worse than one that stayed quiet.
- **There is no `useConnectionState`, deliberately.** Everything *below* the layout reads
  `useStreamStatus()` from `src/realtime/stream-status.tsx` (Task 11). `AppShell` takes a prop
  because `AppLayout` is its single caller and already holds the value; two mechanisms for one
  value means one of them is dead, and the dead one is the one a reader reaches for.
- **`web/src/routes/_app/tasks.tsx` is STILL COMPONENT-ONLY on purpose**, `StubPanel` and all.
  `_app.test.tsx`'s first test navigates there precisely because it issues no request — the case a
  redirect-on-401 design cannot catch. Giving it a loader destroys the property that test proves.
- **`_app.test.tsx` asserts `role="navigation"` to mean "the shell rendered"** in three places. The
  shell has exactly one `<nav>` (`aria-label="Sections"`); adding a second breaks those three tests
  for a reason that reads as the guard being broken.
- **`useMe()` is exported from `routes/_app.tsx`** and is STILL unused — Task 8 did not need it,
  because the shell is handed `me` as a prop. Zero consumers, so the arch misplacement rule (which
  needs two) says nothing about it yet.
- **`_app.tsx` renders `<ApiErrorPanel>` for every non-401 route error.** The stub routes inherit
  it. The panel is in `errors/`, not `ui/`, because it does `error instanceof ApiError` and Task
  3's matrix bans `ui/ -> api/`.
- **`tools/arch/tokens.ts` no longer scans tests.** If Task 11 adds a test that names a colour, it
  will not be reported — and that is correct, because a test styles nothing. The component it
  renders is still scanned.
- **The shell test's harness is `RouterContextProvider`, not `RouterProvider`.** Any later test
  that wants to render chrome without running loaders should copy `renderShell` rather than
  rediscover why a bare `render()` of anything containing a `<Link>` throws.

## What Tasks 8, 11 and 14 must not rediscover the hard way

Wave 4 changed interfaces the later tasks were written against. These are the contracts, not
suggestions:

- **Every form spreads `<form {...form.formProps}>`** (Tasks 8, 11). `noValidate` lives in
  the abstraction because native constraint validation otherwise eats the submit and the error
  the form exists to show. `useAppForm` still returns `submit`, so `onSubmit={form.submit}`
  compiles and silently reintroduces the bug — and only a form with a constrained input type
  (`type="email"`, `required`, `pattern`) would catch it. If that hole should be closed
  structurally, the options are dropping `submit` from the return or an arch rule banning
  `onSubmit={` under `src/routes/**`; wave 4 did neither, deliberately.
- **Task 8 consumes `web/src/realtime/connection-state.ts`; it does not create it.** Already
  committed by Task 10.
- **Task 8 wires `probeMe` against `MeProbe = { grants: readonly unknown[] | null }`.** The
  null is real — `ApiMeResponse.grants` is generated nullable. `unwrap(api.GET('/api/v1/auth/me'))`
  satisfies it as-is; do not narrow the type or add `?? []` at the call site.
- **Task 14 must wire `backend:test:embedui` and `backend:static:embedui` into `scripts/gate`
  by name, with their lines mirrored into `LANE_CMDS`.** Today **3 embedui test functions and
  4 static checks run in ZERO automated lanes** — including the no-inline-script assertion
  against the real Vite bundle and the build-stamp check. Verified non-vacuous by hand
  (poisoning the embedded `index.html` does make it fail), but nothing automated runs them.
  Two traps: `backend/internal/webui/dist` is **gitignored**, so a bare `go test -tags embedui`
  fails to compile on a clean checkout — the lane declares `deps: [web:build]` and the gate
  line must keep it; and it must NOT fold into `backend:ci`, whose Node-free-ness is the whole
  reason the tagless twin exists (CI's `backend` job has no `setup-node`).

## Wave 4's file sets, for the record

Task 6 (`web/src/forms/**`, `web/src/routes/login.*`, `routeTree.gen.ts`), Task 10
(`web/src/realtime/**`), Task 12 (`backend/internal/webui/**`,
`backend/internal/platform/httpx/security*.go`, `backend/internal/app/server*.go` + three
integration tests for the `Routes()` signature, `Dockerfile`, `.dockerignore`, `Taskfile.yml`,
the proxy comment in `web/vite.config.ts`).

## The operating model — this is the part worth keeping

One background Workflow per wave. Per task: **implementer → adversarial reviewers → fix round**,
looping at most twice, each fix re-reviewed. Reviewers get distinct lenses rather than being
redundant copies.

Hard rules, all of which earned their place:

- **Agents never run git write commands, `npm install`, dependency changes, or `scripts/gate`.**
  The orchestrator alone commits, and gates on a clean tree. A missing dependency is an
  escalation, not something an implementer resolves — that rule produced the correct jest-dom
  escalation in wave 2.
- **Reviewers are read-only.** Scratch files go in `/tmp`. A reviewer editing the worktree
  corrupted a gate run on this project once already.
- **Within a wave, agents touch only their own files** and are told their wave-mates' paths. A
  whole-project lane failing in someone else's file is reported, not chased.
- **Every agent carries an explicit dissent licence**: establish truth by running something,
  implement what actually works, report the deviation with evidence. Every single wave used it
  correctly.
- **Mutation is the acceptance criterion.** Implementers must watch each mutant fail; reviewers
  must reproduce it independently and ask "what would make this guard pass while the thing it
  protects is broken?"
- **Node**: `export PATH="$HOME/.nvm/versions/node/v24.19.0/bin:$PATH"` before any node/npm
  command. The shell default is 20.9.0 and is wrong.
- **Name the escalation channel, and close the others.** "Report it, don't fix it" is not a
  channel. In wave 4 a blocked implementer escalated a genuine, correctly-diagnosed problem by
  `SendMessage` to an unrelated Claude session working on a different repository — because
  `ListAgents` showed it live peers and its parent looked unaddressable from inside a workflow.
  Briefs now say: report in your RETURN VALUE, in the `escalations` field; never use
  SendMessage; never contact another agent or session. Pair it with a structured-output field
  actually named `escalations` so there is somewhere obvious to put it.
- **Two fix rounds is a real cap, not a formality.** Task 10 exhausted both and still ended
  `changes-required` with two verified majors open. The orchestrator fixed them by hand and
  re-measured. Budget for that: a wave is not done when the workflow returns.

## What execution found that four plan review rounds did not

Every wave found a load-bearing plan claim to be false, and each was caught by running something.

1. **`npx` / `npm exec --` reach the public registry** when the binary is missing locally —
   pre-flight 15 asserted the opposite. `npx biome ci .` then **exits 0 having checked nothing**,
   so `task web:lint` was a silent vacuous pass. Every lane now calls `./node_modules/.bin/<tool>`
   by path. Task 14 mirrors these command lines into `scripts/gate`, which asserts them
   character-for-character — never reintroduce `npx`.
2. **`git diff --exit-code` is blind to untracked and staged paths**, so every regen-diff gate is
   vacuous exactly when the artifact is not committed. `web:routes:check` now carries the
   `test -z "$(git status --porcelain -- …)"` companion that `contracts:check` already had.
3. **The plan's palette proof was vacuous in the plan's own step order** — nothing imported
   `app.css` yet, so Vite emitted no Tailwind CSS and the grep returned 0 for the wrong reason.
   Reordered, with a positive control: delete the reset, and `.bg-white{…}` reappears.
4. **A `//` comment in `biome.json` silently voids the entire config** (falls back to defaults and
   starts linting `routeTree.gen.ts`). It is not an error. That file must stay strict JSON.
5. **Vitest stubs CSS imports to `''`, and the stub catches `?raw`.** A test whose subject is a CSS
   file asserts against an empty string. `css.include` matches the full module id, so the pattern
   must tolerate the surviving query — `(\?|$)`, not `\.css$`.
6. **`restoreMocks` does not undo `vi.stubGlobal`**; `unstubGlobals: true` is required or a stubbed
   `matchMedia` leaks into every later test in the file.
7. **npm 11 does not run install scripts**, so msw's postinstall never wrote
   `web/public/mockServiceWorker.js`. Irrelevant to vitest (`setupServer` from `msw/node` needs no
   worker file); only a real browser would.
8. **Node 24 + `--experimental-strip-types` requires explicit `.ts` import extensions**
   (extensionless dies with `ERR_MODULE_NOT_FOUND`), which in turn requires
   `allowImportingTsExtensions: true` in `tsconfig.json` — legal only because the project is
   `noEmit`. Only the pair makes node and tsc agree.
9. **openapi-fetch captures `globalThis.fetch` once at `createClient()` time**, before any
   `beforeAll` runs, so MSW never saw the requests. The client resolves the global per call
   instead. It also needs `baseUrl: window.location.origin` rather than `''`: under vitest the
   global `Request` is Node's, and `new Request('/api/v1/auth/me')` throws `Failed to parse URL`.
10. **The bare `fetch` spelling is the one `noRestrictedGlobals` sees.** Writing `globalThis.fetch`
    made the file's own lint exemption vacuous — measured — so the qualified spelling was the one
    hole in the ban. A GritQL plugin (`web/tools/lint/no-qualified-fetch.grit`) closes the
    qualified spellings; see open items for what it does not close.
11. **A cross-task catch:** a reviewer proposed an inline `<script>` to stamp the theme before
    paint. Task 12's CSP test (plan lines 4459-4495) fails the build on any inline script. The
    reason is recorded in `theme-dark.css` so nobody re-adds it.
12. **Task 3's fixtures would have broken `web:test`** the moment they landed: the `node` vitest
    project's glob collects `tools/**/*.test.ts`, and the fixture deliberately named
    `helper.test.ts` declares no suite. Excluded pre-emptively in wave 2, measured.

Wave 4 added six more, and again every one came from running something:

13. **The plan's login fixture email `a@b.c` is rejected by the pinned zod 4.4.3** — its TLD
    group is `[A-Za-z]{2,}`, so a one-character TLD fails. **Five of the six login tests could
    never have passed as written.**
14. **`<form onSubmit={form.submit}>` around an `<input type="email">` never submits.** The
    browser's interactive constraint validation blocks it and React's handler never fires, so
    the zod message and the `role="alert"` the form exists to render are unreachable in a real
    browser. Measured three ways in jsdom: `type=email` without `noValidate` → submit does not
    fire; with `noValidate` → fires; `type=text` → fires. A `type="text"` form would have
    hidden this completely, which is why the fix belongs in the abstraction (`formProps`) and
    not in `login.tsx`.
15. **`STABLE_MS` passed its whole suite at any value in `(0, 30_000]`, including `1`** — a
    value that reintroduces verbatim the hot loop the constant's own docstring says it exists
    to prevent. Cause: the guard test left its injected clock frozen at 0, and `0 >= STABLE_MS`
    is false for every positive threshold, so the test was satisfied by any of them. This is
    the same defect class as items 3 and 12: a comment asserting a property no test can
    falsify. Now bracketed to `[25_000, 30_000]` by bisect.
16. **`expect(vi.getTimerCount()).toBe(0)` reads a GLOBAL count**, so it couples a test to
    every test before it in the file. Three unrelated source mutants each produced *two*
    failures, the second pointing at a timer-clearing default that was fine. On a project whose
    acceptance criterion is a mutation sweep, that misdirects the one workflow that matters.
    Assert the delta instead; the kill power is unchanged.
17. **An undeclared Docker build arg is silently ignored, with no warning at all** — quieter
    than the plan claimed. `--build-arg GOTAGS=` against a Dockerfile that never declares
    `GOTAGS` leaves `-tags embedui` standing, so Task 14's deliberately-UI-less mutant would
    have served the UI and reported success. Declared, the difference is observable: 15.9MB
    with the tag, 15.1MB without.
18. **`ui_test.go`'s "200 or 501" assertion cannot tell the twins apart** — proven, not argued:
    a tagless handler returning 200 with a stub HTML page passes it, which is exactly the
    "a forgotten `vite build` ships a silent stub" failure the build tag exists to prevent.
    `ui_noembed_test.go` is what actually catches it (`GET / = 200, want 501`). That is why the
    file the plan never asked for was kept.

Wave 5 added three, and the first two came from mutating guards that were already green:

19. **`ensureQueryData` resolves from a cache entry whose refetch already 401d — without
    issuing a request at all.** MEASURED on the pinned react-query 5.101.4: a failed refetch
    leaves `data` in place, and `ensureQueryData` hands it back. The plan used it for `/login`'s
    redirect-inward guard, so the guard concluded "signed in" from an answer the server had
    already rejected, redirected to `/`, and `_app`'s expiry check bounced it straight back —
    **an infinite redirect loop rendering an empty page.** Adding Step 4 as written turned the
    already-passing expired-session test red, which is the only reason it was caught in the
    minute rather than in a browser. Now `fetchQuery({ ...meQueryOptions, staleTime: 0 })`: a
    guard that sends someone *away* from the login form has to prove the session, not recall one.
    `_app`'s loader keeps `ensureQueryData` deliberately — accepting cached data is what makes a
    reload instant, and the layout's expiry check is what makes it correct.
20. **Task 7's three guard tests were vacuous for the thing the task exists to build.** Neuter
    the `_app` loader entirely and all three still pass, because `useSuspenseQuery` then throws
    the same 401 and the error boundary navigates to the same place. Same visible outcome, two
    different mechanisms. The discriminator is the history: a loader `throw redirect()` during
    the initial load REPLACES the entry, a boundary `navigate()` PUSHES one — so the mutant
    leaves the unreachable route sitting behind `/login` in the back stack. `expect(router.
    history.length).toBe(1)` kills it. Same defect class as items 3, 12 and 15: a property
    asserted in a comment that no test could falsify.
21. **A test file under `src/routes/` that exports a `Route` becomes a real route and ships.**
    The ledger left this open "for Task 7 to decide" and it was worth deciding: measured, such a
    file lands 13 references in `routeTree.gen.ts`. `web/tsr.config.json` now sets
    `routeFileIgnorePattern`, and the Vite plugin and the `tsr` CLI were confirmed to read it
    identically by generating with each and diffing — otherwise `web:build` and
    `web:routes:check` could disagree about what the route tree is.

Wave 6 added five. Three are the plan's own Task 8 snippets failing to run at all, and the pattern
across them is worth naming: **a component test harness is as much a subject as the component.**
Two of the three could never have passed for any correct implementation.

22. **`<Link>` cannot render outside a router, so the plan's shell test could not pass for ANY
    correct shell.** `render(<AppShell me={me}>body</AppShell>)` dies in `useLinkProps` with
    `Cannot read properties of null (reading 'isServer')`. The first test asserts that the nav
    offers every route in the V1 page map — i.e. it *demands* the links its own harness cannot
    render. The fix is `RouterContextProvider` rather than `RouterProvider`: it publishes the
    router on context and renders its own children, where `RouterProvider` renders the router's
    matches instead. That keeps the subject the shell-as-component — no loader, no query, no
    server — while the links still resolve against the real route tree.
23. **The plan's theme toggle was two-state over a three-state `Theme`, and its own test proved
    it.** `useTheme` starts at `'system'`, so `theme === 'dark' ? 'light' : 'dark'` labels the
    button "Dark" on first render while the test clicks `{ name: /light/i }`. Beyond the test: it
    stranded `'system'` permanently, leaving `use-theme.test.ts`'s `removeItem` leg — whose comment
    reads "Task 8's ThemeToggle is built directly on this leg" — with no production caller at all.
    A test can cover a branch that production can no longer reach. And on a dark OS the first click
    was invisible, since `system` already resolved to dark. Now a cycle, labelled with what you get.
24. **The plan's theme assertion was vacuous under the shared harness — the third time this exact
    shape has appeared** (items 3, 12, 15, 20). `src/test/setup.ts` gives every DOM test a
    `matchMedia` reporting no preference, so `system` resolves to LIGHT and
    `dataset.theme === 'light'` holds *before* the click, against a component with no button in it.
    Stubbing a dark preference is what makes switching to light an event. Related: the plan's
    fourth test ("silent when the stream is live") **passed against Task 7's stub**, which renders
    no indicator at all.
25. **`web:arch` cannot tell a regex that PARSES colours from a colour.** The WCAG suite reads the
    shipped tokens out of the theme files, and the substring `oklch(` in its parser tripped the
    literal-colour pass — while the theme files themselves are exempt for exactly that reason.
    `tools/arch/tokens.ts` now skips tests, matching `import-graph.ts`'s existing `NOT_A_CONSUMER`
    exclusion. For this rule that is not a softening at all: its subject is what ships, a test
    styles nothing, and the component whose classes a test asserts on is itself scanned — so a test
    can only ever produce a false positive. `fixtures/colors/bad.test.tsx` keeps it falsifiable.
26. **`theme-dark.css` had claimed since Task 2 that every hex-annotated value "was produced by the
    sRGB -> OKLab -> OKLCH conversion of that hex, not typed by hand", and nothing held it to
    that.** It is now the WCAG suite's own self-check, which needed one anyway: every assertion
    routes through one colour converter, so a converter returning a constant would report a
    comfortable ratio for every pair and the whole suite would be worth nothing. All seven hexes
    round-trip exactly. Greyscale anchors alone would not do — a transposed or mis-signed a/b term
    is invisible on them. (The first draft of that self-check asserted a *remembered* ratio for
    mid-grey and was simply wrong; the run caught it.)

Wave 7 added four. Three are again the plan's own snippets, and the first is the sharpest instance
yet of the pattern: **a seam with no production caller is not a seam, it is a second implementation
that only the tests exercise.**

27. **`createCacheBridge`'s `flush()` had no production caller, and it made all five of the plan's
    bridge tests vacuous.** Every one of them closes the debounce window by calling `flush()`;
    `useAttentionStream` never would, so the only path the browser runs — the scheduled timer — was
    unasserted. MEASURED: delete the scheduling arm entirely and all five plan tests stay green
    while the shipped bridge never invalidates anything at all. Four of the five also pass
    `debounceMs: 0`, which under the plan's own `if (debounceMs > 0 && !scheduled)` guard means
    "never schedule" — a mode production does not have, so the suite ran a configuration that does
    not ship. `flush` is gone and `setTimeoutImpl` is the seam: a test that wants the window closed
    fires the timer. Related and also invisible to the plan's set: `fire()` must clear `scheduled`
    BEFORE invalidating, or the bridge goes permanently deaf after its first burst — no
    single-burst test can see it, and every plan test is single-burst.
28. **The plan started the connection at `'reconnecting'`, which puts "this view may be out of
    date" on screen at the first paint of every page load** — over data the route loader fetched
    milliseconds earlier. The indicator's own doc comment is what decides it: a banner that is
    always present trains people to stop reading it. It starts `'live'`, which renders nothing.
    The exposure is bounded and small: a REFUSED stream announces `reconnecting` in milliseconds,
    and the only case silence covers is a server that accepts and then says nothing, which the
    transport's own `OPEN_TIMEOUT_MS` converts into the same announcement after 20s. Throughout
    that window REST is still the source of truth and the 60s refresh floor is still running.
29. **The harness could not assert either state, and the test I wrote for it passed with the
    initial state flipped back.** `silentStream` is already open before anything renders, so the
    transport has said `'live'` and "no banner" holds for a reason unrelated to the value under
    test. A handler that never answers (`await delay('infinite')`) is what makes the opening window
    long enough to assert about. Fifth appearance of this shape (items 3, 12, 15, 20, 24) and the
    second time in two waves that the harness, not the component, was the defective subject.
30. **`f.event` and `f.frame.kind` are the same string on the wire, so no fixture carrying both
    could say which one the bridge reads.** The swap survived the entire sweep. They are not
    interchangeable in principle — `event` is the SSE routing label and the parser defaults it to
    `''` when the field is absent, while `frame.kind` is the record the backend stored and the
    thing `GET /approvals` is a projection of — so the fixture now makes them DISAGREE.

Wave 8 added five, and the headline is a new shape: **the lane's own green first run proved
nothing, because `go test` hides a passing subprocess's output.** Playwright's result was invisible
until the run was repeated with `-v`.

31. **The plan's harness shelled out to `npx playwright test`** — pre-flight finding 1's exact ban,
    reintroduced inside a Go snippet. Wave 5 rewrote every *verify line* off `npx` and this one
    survived precisely because it is not a verify line. Now `./node_modules/.bin/playwright`.
    Worth generalising: a ban enforced by rewriting one syntactic category leaks into every other
    category.
32. **The plan's blast-radius test asserted that Go zeroes struct fields.** `var zero serveOptions;
    if zero.executor != nil` is true of every program ever compiled, and stays green after someone
    changes `runServe` to pass `serveOptions{executor: …}` — which is the entire property the test
    is named for. It reads `serve.go`'s AST now and is falsifiable: adding any field to that
    literal turns it red. A second fact fell out of trying to kill it — **`go test -overlay` cannot
    falsify a test that reads its subject from disk at runtime.** The overlay is a compiler
    substitution; `parser.ParseFile` opens the real file and reported the unmutated source, so the
    mutant passed and looked like a surviving guard. Sixth appearance of the green-for-the-wrong-
    reason shape (items 3, 12, 15, 20, 24, 27), and the first where the MUTATION TECHNIQUE, not the
    test, was the defective instrument.
33. **`//go:build webe2e` alone does not compile.** The harness uses `syncBuffer` from
    `serve_integration_test.go`, which is `integration`-tagged, so a bare `-tags webe2e` fails as an
    undefined symbol — a broken package rather than a missing tag. Tagged `webe2e && integration`.
34. **`e2e/` was typechecked by nothing, and `test-results/` broke `web:lint`.** `tsconfig.json`
    listed `playwright.config.ts` but not `e2e`, and vitest's globs are `src/**` / `tools/**` — so
    the spec was checked by no lane at all. MEASURED both ways with a probe type error. Separately,
    Playwright writes `test-results/.last-run.json` on every run INCLUDING a passing one, and
    **biome does not read `.gitignore`** — measured, removing the `files.includes` exclusion brings
    the formatting error straight back. Two files needed telling, not one.
35. **The `listener` field's justification is now measured rather than reasoned.** The plan argued
    at length that `CheckOrigin`'s exact-equality comparison forces bind-before-configure. Setting
    `AppOrigin` to a host the browser will not send produces a real `403` on
    `POST /api/v1/auth/login`, and the spec dies at `getByRole('navigation')`. The argument was
    right, and it is now evidence — the same standard the `__Host-` cookie comment is still waiting
    on.

**What the lane is worth, stated as a mutant.** Making `createCacheBridge.onFrame` return early —
a bridge that receives every frame and invalidates nothing — fails the spec at the badge assertion
after 30s, while the harness's own diagnostic reports `impl = succeeded` and
`gate = waiting_decision`. That is the whole point of the construct: the backend did its entire job
through production code, and the failure is isolated to the client. No mocked stream can produce
that discrimination. Beat 1's message is measured too — `scopeEmpty` is false for a bootstrapped
admin, so "Nothing is waiting on you." is the right string rather than the grant-less one.

Two smaller ones, both about claims rather than code. **The `/approvals` loader does not make
`AppErrorBoundary`'s 401 branch reachable** — measured, removing the loader leaves every test in the
file green, because `useSuspenseQuery` throws to the same boundary; what makes the branch reachable
is the route fetching anything at all. The comment claiming otherwise was corrected rather than
kept, and the loader's real payoff (a router-level load that `defaultPreload: 'intent'` can start on
hover) is recorded as unfalsifiable. And **msw does not run a response body's `cancel` callback when
the client aborts**, so the obvious close-on-unmount test reports failure for a correct cleanup;
`request.signal`'s `abort` event is what observes it.

Also measured and NOT adopted: the plan's second, disabled observer
(`useQuery({ ...meQueryOptions, enabled: false })`) for background expiry. Both observers read
the same cache entry and report the same error, so reading `error` off the existing
`useSuspenseQuery` is equivalent — one hook fewer, no change to any test or fetch count.

## What Task 14 inherits from wave 8

- **`task web:e2e` runs in ZERO automated lanes today.** It is not in `scripts/gate` and not in CI —
  the `web` job does not exist yet, which is Task 14's work. It is the third thing in that
  position, alongside `backend:test:embedui` and `backend:static:embedui`.
- **It deliberately has no `deps:`**, unlike `backend:test:embedui`. It needs TWO prerequisites —
  the built bundle and a ~95MB browser download — and a lane that silently downloads a browser is
  not one a developer should be able to trigger by accident. Task 14 makes
  `./node_modules/.bin/playwright install --with-deps chromium` and `vite build` visible steps in
  the workflow. `--with-deps` is the Linux form and is what CI needs; on macOS use the bare form.
- **`webe2e` is now in the tag lists of `backend:vet`, `backend:analyze` and `backend:vuln`**, and
  the three matching `LANE_CMDS` lines in `scripts/gate` were updated with them. `embedui` is still
  deliberately absent from all three: `//go:embed all:dist` will not compile without a build, and
  those lanes must stay Node-free.
- **The E2E lane is fast and stable where it stands**: ~3.2s wall, three consecutive clean runs,
  against a 30s assertion timeout. The knob is `PollInterval: 200ms`.
- **`scripts/gate` ate `web/node_modules` a fourth time**, exactly as diagnosed. Same recovery,
  `package-lock.json` byte-identical, every web lane re-run green afterwards.

## Open items

**FOR THE OPERATOR — Task 12 Step 11 is undone and cannot be closed by an agent.** The
`__Host-` cookie must be shown to survive across the Vite dev proxy, and to be *dropped*
without it. `web/vite.config.ts`'s proxy block carries a comment headed **"STATUS: REASONED,
NOT YET MEASURED"** listing the six steps: `task db:up`; `go run ./cmd/caesar serve -config
caesar.dev.json`; `./node_modules/.bin/vite`; log in and reload at the Vite origin, expecting
the session to survive; then delete the proxy block, point at `http://127.0.0.1:8080` directly
and repeat, expecting the cookie to be dropped; restore. **If step 5 does not drop the cookie,
the block's justification is wrong and should be rewritten, not kept.** Task 12 is not fully
verified until someone records that outcome in the comment.

**A real UX hole, deliberately left, belonging to whoever owns the global error surface.**
`useAppForm` rethrows anything that is not an `ApiError`, so a network-level failure — offline,
DNS, CORS, an aborted request — escapes `handleSubmit`'s async callback into a floating
promise. React does not await it, so **the user sees the form do nothing, with no message.**
Swallowing it would be worse (a TypeError presented as though the server had spoken), so the
behaviour stands, but it is untested and it is a hole.

**~~For Task 7 to decide~~ — CLOSED in `8a40eae`.** `web/tsr.config.json` now sets
`routeFileIgnorePattern`, the `tsr generate` warnings are gone, and the leak is measured rather
than assumed (finding 21). One thing it does NOT close: deleting that file leaves every lane
green today, because no test file currently exports a `Route`. The guard is latent, and the
protection against removing it is this note plus the comment in `vite.config.ts`.

**~~For sofo — a taste call~~ — DECIDED AND CLOSED in `5073519`.** sofo chose to darken both and
add the test. The four ratios were re-derived independently before asking and matched the recorded
ones exactly (10.43 / 4.23 / 9.33 / 3.84); the ask also turned up a fifth, worse number the ledger
did not have — the connection indicator renders `text-warning` on `bg-warning/10`, which
composited over `ground` is **3.55:1**, not 3.84. The banner that exists to be noticed was the
least readable thing in the theme. `--caesar-accent` 0.56 → 0.52 (`success` follows) and
`--caesar-warning` 0.6 → 0.54; every text pair in both themes now clears AA, and
`theme-default.test.ts` holds them there. Reverting either token to its shipped value turns the
suite red, so the fix is pinned rather than remembered.

**~~Also for Task 8~~ — CLOSED in `5073519`.** `color-scheme` moved off `html` and into the two
theme blocks. It had to move as a pair: setting it in one file only leaves the other theme
inheriting whatever the first stamped.

**A contracts follow-up, deliberately out of Task 4's scope.** `changePassword`
(`backend/internal/identity/api/api.go`) has no `Errors: []int{http.StatusUnauthorized}` entry, so
`openapi.json` and `schema.d.ts` both claim 401 is impossible there while the handler really
returns it (`internal/identity/session.go:188`). The client's `AUTH_EXEMPT` list is currently the
only record of that fact. Fixing it is a contracts-lane change (regen + diff), not a web change.

**Known-unfalsifiable branches**, all raised as minors by reviewers and left rather than spun into
more rounds. Recorded so they are not mistaken for tested code:

- `sse-parser.ts` — the multi-line `data:` accumulation branch and the empty-block guard both
  survive mutation with the suite green. The CRLF paragraph in its doc comment is factually wrong
  in both its claims.
- `import-graph.ts` — the `stories` exclusion, the GENERATED skip, the importer dedupe and the
  result sort are unfalsifiable.
- `tokens.ts` — over-fires on arbitrary non-colour arbitrary values (`text-[13px]`,
  `shadow-[0_1px_2px]`, `border-[2px]`, `from-[10%]`); the `var(--color-` exemption is line-scoped,
  so a real hex sharing a line with a token reference is missed; the check is line-text based, so a
  comment in `src/` that merely names a banned utility turns `web:arch` red.
- `no-qualified-fetch.grit` — bans **all** destructuring of `window`/`globalThis`/`self`, not just
  the `fetch` key, and reports it with the fetch message. It has measured evasions, and its
  diagnostics are suppressible with an ordinary `biome-ignore` comment.
- `unwrap` treats `response.ok` as success, so a 200 with an empty body returns `undefined` as `T`.
  Documented in the code as reachable only via 204/HEAD/`Content-Length: 0`, since openapi-fetch
  lets a JSON parse error reject.

From wave 4:

- `use-app-form.ts` — the non-`ApiError` rethrow branch (see the UX hole above). `TextField`'s
  `error.message !== ''` narrowing is uncovered: nothing produces an empty message today.
- `login.tsx` — **`LoginPage` itself is untested.** The suite renders only the exported
  `LoginForm`, which is why it needs neither a router nor a `QueryClientProvider`. So `Route`,
  `useNavigate`, `useQueryClient` and the invalidate-then-navigate ordering are unexercised, and
  the comment explaining why login's 200 body must not seed the `auth/me` cache is an untested
  assertion.
- `sse-connection.ts` — every default seam (`fetchImpl`, `now`, and the three timer defaults) is
  injected by every test, so no test exercises any default; the browser is their first execution.
  `res.body === null` is not observable through the public surface. The abort path is asserted
  against a fake in jsdom, not a real browser. `WATCHDOG_MS` and `STABLE_MS` are pinned relative
  to the values the tests choose, so a coordinated change of both constant and test still passes
  — the mutants prove the rules, not the numbers.
From wave 5:

- ~~`_app.tsx` — **`AppErrorBoundary`'s 401 branch is untested.**~~ **CLOSED in `63bbb1f`.**
  `/approvals` fetching something of its own is what produces a 401 arriving from below.
  `/auth/me` deliberately stays HEALTHY in that test: expire the session instead and three
  mechanisms could produce the same redirect (the layout loader, AppLayout's expiry check, this
  boundary), which is the trap wave 5 fell into once already. The full round trip is the
  assertion — the boundary sends the user to `/login`, whose guard proves the session and sends
  them back inward.
- `_app.tsx` — the loader's non-401 rethrow IS covered (the 500 test), but its `redirect` on 401
  is only covered on the stub-route path; no test drives a 401 into `_app` from `/`.
- `api-error-panel.tsx` — the non-403 branch renders `error.detail`, and no test asserts the
  detail text itself, only the correlation id. A panel that rendered an empty `<p>` would pass.
- `app-shell.tsx` is a stub whose `me` prop is accepted and never read, so nothing yet proves the
  session actually reaches the shell. Task 8 makes that real.
- `useMe()` has no consumers and no test.
- `ui_embed.go` — both panics (`dist` not embedded, `index.html` missing) are unreachable
  whenever `//go:embed all:dist` compiled, and untested.
- `security.go` — **the entire `dev=true` CSP branch is live only in tests.** `Routes()`
  hardcodes `SecurityHeaders(false)` and no call site passes true.
- Content-hashed assets are served with **no explicit `Cache-Control`**. The plan does not ask
  for it and wave 4 declined to ship an untested branch; worth a decision later.

From wave 6:

- **`AppShell` is never rendered through a real route in any test.** The shell suite mounts it
  directly under `RouterContextProvider`, and `_app.test.tsx` only ever asserts
  `role="navigation"` — so nothing exercises the composition of layout and shell together, and
  `AppLayout` passing `me` but neither `connection` nor `approvalCount` is asserted nowhere.
  Task 11 is the first task with a real reason to.
- **`activeProps={{ className: 'text-accent' }}` on the nav is untested.** The harness never
  navigates, so no link is ever active. A `<Link>` that highlighted the wrong row, or none, would
  pass the whole suite.
- **The six stub routes and `StubPanel` have no tests of their own** — they are reached only
  through `web:build` and `web:routes:check`. Deliberate: a test asserting that a stub says "not
  built yet" pins a string, not a behaviour. It does mean `StubPanel`'s two-paragraph shape is
  unguarded.
- **The WCAG suite covers token PAIRS, not usage.** It knows `text-muted` on `surface` clears AA;
  it does not know which pairs a component actually renders, so a component pairing `warning` on
  `accent` would be invisible to it. The `TEXT_PAIRS` list is hand-maintained and will drift from
  what the tree does. The one composited case (`bg-warning/10`) is spelled out for that reason and
  is itself hand-copied from `connection-indicator.tsx`.
- **`color-scheme` is asserted by regex over the CSS text**, not by anything that renders. It
  proves the declaration is present in each theme block and nothing about the UA honouring it.
  `app.css` is not read by that test at all — `vitest.config.ts`'s `css.include` covers only the
  two theme files, and widening it would pull the whole Tailwind build onto every DOM test's
  critical path. So re-adding `color-scheme: dark light` to `app.css` would go unnoticed.
- **`ThemeToggle`'s `capitalize` class is the only thing making the label read as a word rather
  than a lowercase token**, and no test asserts rendered case — jsdom does not apply it.
- ~~**`AppShell` is never rendered through a real route**~~ — **CLOSED in `63bbb1f`.** `AppLayout`
  now passes `connection` and `approvalCount` from live values, and mutating either is caught.

From wave 8:

- **The E2E asserts ONE happy path and no failure mode.** A stream that never opens, a `forbidden`
  frame, a 500 from `/approvals`, a second gate on the same run — none are exercised. The lane's
  cost is a browser download and a Docker Postgres, so breadth here is expensive by design.
- **Nothing asserts the gate row's CONTENT**, only that the badge reads `1` and the title renders.
  A gate materialized against the wrong run or node would satisfy the spec.
- **`e2eDiagnose` runs only on failure**, so its own SQL is exercised only when something else is
  already broken — it was measured once, by the cache-bridge mutant, and that is its only run.
- **The lane never asserts the executor substitution took**, it infers it: `NotImplementedExecutor`
  cannot produce a patch, so `impl = succeeded` could only have come from the fake. True, but it is
  reasoning rather than an assertion.
- **`awaitListening` duplicates the polling loop in `TestRunServe_BootsMigratesAndShutsDownOnCancel`
  and the `servedProcess` method of the same name.** Three readiness waits in one package, behind
  three different build tags.

From wave 7:

- **The `/approvals` loader is unfalsifiable.** Removing it leaves the whole suite green, because
  `useSuspenseQuery` reaches the same cache and the same boundary and jsdom resolves the suspension
  inside the same act() flush. Its real payoff is a router-level load that `defaultPreload:
  'intent'` can start on hover — and `AppLayout`'s badge query has already requested the same key,
  so even a request-counting test could not see it.
- **`ApprovalList`'s composite key is unfalsifiable through the DOM.** Two waiting gates on one run
  under a `run_id`-only key produce a React warning, not a failure, and both rows still render. The
  key is right — one run can hold several gates and a retry gives the same node a new attempt — but
  nothing holds it there.
- **`useAttentionStream` has no seams**, deliberately: it is exercised end to end through msw, so
  the bridge, the transport and the real `fetch`/`TextDecoderStream` path all run. That path had
  never executed before this wave — every `sse-connection` test injects `fetchImpl` — and it works
  under this jsdom, which is measured now rather than assumed.
- **Nothing asserts the debounce is 250ms in the app**, only in the bridge's own suite. The hook
  constructs the bridge with no options, so a changed default would show up as a slower badge and
  no failure.
- **The badge's failure rendering is now tested, but its ambiguity is inherent**: a failing
  approvals endpoint and an empty inbox look identical from the nav. `/approvals` is where the
  difference is visible.

## Plan divergences, all FIXED in the plan text

The rule this project follows: when execution proves a plan snippet wrong, **correct the plan**,
because the next person to execute it will otherwise reintroduce exactly what was just removed.

- **Task 3** (wave 3). The snippet carried the unclamped `const required = commonAncestor(importers)`
  that Task 3 deliberately replaced with a group-root clamp. It now matches the shipped code and
  says plainly that `web/tools/arch/import-graph.ts` is authoritative — it defines `groupRootOf`
  and `isWithin`, which the plan block does not.
- **Task 6** (wave 4). The four `a@b.c` fixtures typed into the form are now `user@example.com`
  (the other five occurrences elsewhere in the plan never reach `z.email()` and are untouched);
  `<form onSubmit={form.submit}>` is now `<form {...form.formProps}>`; and a corrective block
  names all three false claims, including the doc comment's wrong error count.
- **Task 10** (wave 4). A corrective block names the nine wrong counts, `connection-state.ts`
  moved into Task 10's file list, and `probeMe` retyped to the nullable `MeProbe`.
- **Task 8** (wave 4). Its "Create `connection-state.ts`" line is now "CONSUME" — Task 10 owns
  the file. The placement argument is unchanged and is why it lives in `realtime/`.
- **Task 11** (wave 7). A corrective block at the top names all four false claims. Step 1's suite
  is rewritten around an injected timer with `flush()` gone; Step 3's implementation drops `flush`
  and the `pending` flag and schedules unconditionally; Step 4 lists the eleven mutants; Step 5's
  hook starts at `'live'`, switches exhaustively on the event type and carries the
  `navigate`-stability note, `stream-status.tsx`'s default follows it, and the prose now says where
  the badge count comes from and why it is a plain `useQuery`; Step 6 gained the note on what the
  loader does and does not buy; Step 7's harness is documented as synchronous and handler-taking,
  with msw's precedence, `pendingStream`, the abort-signal fact and the retry timeout; the Files
  list gained `stream-status.test.tsx`, `render-app.tsx` and the `_app.test.tsx` modification; and
  the second Step 7 is now Step 8.
- **Task 13** (wave 8). A corrective block at the top names all five false claims. Step 1's
  blast-radius test is rewritten as an AST assertion over `serve.go` with the `-overlay` caveat
  stated; Step 2's build tag is `webe2e && integration`, its Playwright invocation is by path, its
  `e2eSeed` takes `(ctx, t, cfg)` and reads the workflow version back off `CreateVersion`, and
  `e2eFixture` gained `workflowVersion`; Step 6 drops `--with-deps` for the macOS form, names the
  three config files that need the new directory, and says to confirm with `-v` once because a
  passing subprocess prints nothing. The Files list gained `serve_integration_test.go`,
  `scripts/gate`, `web/tsconfig.json`, `web/biome.json` and `web/.gitignore`.
- **Task 8** (wave 6). Step 1's harness is now `RouterContextProvider` over the real route tree
  with a `renderShell` helper, and its theme case stubs a dark OS preference; Step 2's expectation
  is FAIL-on-content rather than module-not-found, and names the test that passes against Task 7's
  stub; Step 3's `connection-indicator.tsx` imports `ConnectionState` instead of re-declaring it,
  its `ThemeToggle` is the three-state cycle, and an order note says `nav.tsx` cannot typecheck
  until Step 5 exists; the Files list gained `_app/index.tsx`, the four theme files and the three
  arch files; new Steps 5a and 5b carry the contrast/`color-scheme` fixes and the token-rule
  exclusion; Step 6 notes that `web:routes:check` needs a clean tree and lists the 22 mutants. A
  corrective block at the top of the task names all five false claims.
- **Task 7** (wave 5). Step 4's `ensureQueryData` is now `fetchQuery({ ...meQueryOptions,
  staleTime: 0 })` with the redirect moved outside the `try`, and carries the loop it caused;
  Step 3's second disabled observer is gone in favour of reading `error` off the existing
  `useSuspenseQuery`; Step 1's harness returns the router and asserts `history.length === 1`,
  without which the whole task is unfalsifiable; Step 6's route-level assertion moved off
  `/approvals` (a Task 11 route) onto a 500 from `/auth/me`; the Files list gained
  `shell/app-shell.tsx`, `tsr.config.json` and the real set of modified files, and lost the
  `main.tsx` change, which turned out to be unnecessary.
- **Every task's verify line** (wave 5). The remaining `npx` / `npm exec --` invocations across
  Tasks 2, 5, 6, 7, 8, 11, 12, 13 and 14 are now `./node_modules/.bin/<tool>`, matching the rule
  pre-flight 15 states and the Taskfile and Dockerfile already follow. The prose explaining *why*
  the ban exists still says `npx`, deliberately, as does the one intentional versioned registry
  call (`npx @biomejs/biome@2.5.8 explain`).
