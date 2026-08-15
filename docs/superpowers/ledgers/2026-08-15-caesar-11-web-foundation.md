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
| — | wave-5 plan corrections + this ledger entry | _(this commit)_ | docs only |

**Done: Tasks 1, 2, 3, 4, 5, 6, 7, 9, 10, 12. Remaining: 8 → 11 → 13 → 14 (a chain).**

The gate is `scripts/gate <abs-worktree> <sha> <branch>` and it ran green at each wave boundary,
not after each task. Within a wave, tasks are committed in dependency order and their individual
commits' greenness is asserted by construction rather than measured — stating that plainly because
the plan's own rule is per-task.

All web lanes green at `8a40eae`: `web:pins` (34 pinned), `web:lint`, `web:typecheck` (tsc + the
route-inference mutant), `web:arch`, `web:test` (**113** tests, 12 files), `web:routes:check`,
`web:build`. Both Go twins green: `go test ./internal/...` and `go test -tags embedui
./internal/webui/`. The gate itself still covers only the backend four — **the web lanes are run
by hand until Task 14 mirrors them into `scripts/gate`.**

### DIAGNOSED — `scripts/gate` is what breaks `web/node_modules`, every single run

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
`pluginutils`. Reproduced twice. `npm ci` under npm 11.17.0 *or* 11.6.2 restores it and leaves
`package-lock.json` byte-identical; the error message's own advice — delete the lock — would
unpin all 34 versions `web:pins` exists to guard, and must not be followed.

**This is Task 14's problem before it is anyone else's.** Task 14 mirrors the web lanes into
`scripts/gate`; wired naively, `contracts:check` corrupts `node_modules` and then the web lanes
run against the wreckage *inside the same gate invocation*. The ordering comment at
`Taskfile.yml:362` already assumes `contracts:ts` is what provides `web/node_modules` — it is
also what destroys it.

Recommended fix, not applied here because it is outside Task 7 and changes gate behaviour:
`engine-strict=true` in `web/.npmrc`, which turns EBADENGINE from a warning into a refusal, so a
wrong-node `npm ci` fails loudly instead of silently shipping a broken tree. Whatever the fix,
the acceptance criterion is that a full gate run leaves `web:test` green.

## The dependency graph, which is mostly a chain

`4 → 1 → 2 → {3, 5, 9} → {6, 10, 12} → 7 → 8 → 11 → 13 → 14`

Only the two braces parallelize; the file sets inside each are disjoint. Tasks 4 and 1 cannot
overlap — Task 4 regenerates `schema.d.ts` out of `web/node_modules` while Task 1 rewrites
`package.json` and reinstalls that same tree.

**Both braces are now done. Everything left is strictly serial**, one task per wave: 8 → 11 → 13
→ 14. There is no more parallelism to find in this plan.

Wave 5 (Task 7) was executed **inline by the orchestrator**, not through the per-wave Workflow the
operating model below describes. With one task left per wave there is no fan-out to buy, and the
quality bar it existed to enforce — write the test, watch it fail, then mutate every guard and
watch it die — was applied directly. Three of this task's findings came out of that mutation pass
and would not have come out of a green suite.

## What Task 8 inherits from wave 5, and must not undo

- **`web/src/shell/app-shell.tsx` EXISTS as a stub** — a `<nav aria-label="Main" />` plus
  `{children}`, typed `{ me: Me; children: ReactNode }` where `Me` is
  `components['schemas']['ApiMeResponse']`. Task 8 replaces the file wholesale, but **keep the
  `me` prop**: the session is resolved by the `_app` loader and handed down, so no shell
  component ever fetches it. `_app.test.tsx` asserts `role="navigation"` to mean "the shell
  rendered"; a real shell must keep a `<nav>` or three tests fail for the wrong reason.
- **`web/src/routes/_app/tasks.tsx` is COMPONENT-ONLY on purpose.** `_app.test.tsx`'s first test
  navigates there precisely because it issues no request — the case a redirect-on-401 design
  cannot catch. Giving it a loader destroys the property that test exists to prove.
- **`useMe()` is exported from `routes/_app.tsx`** and is currently unused. It has zero consumers,
  so the arch misplacement rule (which needs two) says nothing about it yet; a second consumer
  will pull it toward a shared home.
- **`_app.tsx` renders `<ApiErrorPanel>` for every non-401 route error.** Task 8's stub routes
  inherit it. The panel is in `errors/`, not `ui/`, because it does `error instanceof ApiError`
  and Task 3's matrix bans `ui/ -> api/`.

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

Also measured and NOT adopted: the plan's second, disabled observer
(`useQuery({ ...meQueryOptions, enabled: false })`) for background expiry. Both observers read
the same cache entry and report the same error, so reading `error` off the existing
`useSuspenseQuery` is equivalent — one hook fewer, no change to any test or fetch count.

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

**For sofo — a taste call, not blocking.** In the light theme `accent-contrast` on `accent`
measures **4.23:1** (below WCAG AA 4.5) where dark measures 10.43:1, and `warning` on `ground` is
**3.84:1** vs 9.33:1. A token pair whose whole purpose is contrast fails in one theme. Proposal:
darken the light-theme `accent` until both clear 4.5:1, folded into Task 8 where the theme toggle
first makes light mode reachable.

**Also for Task 8.** `app.css` declares `html { color-scheme: dark light }`, which follows the OS,
while `data-theme` (defaulting to dark) is authoritative for the tokens. Until the toggle exists,
a light-OS user gets a dark page with light UA scrollbars and native controls.

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

- `_app.tsx` — **`AppErrorBoundary`'s 401 branch is untested.** The first load's 401 is the
  loader's, and a background expiry is AppLayout's; nothing today produces a 401 that arrives
  through the boundary, because that needs a child route with its own loader (Task 11's
  `/approvals`). The branch is reasoned, not measured, and Task 11 is where it becomes testable.
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
