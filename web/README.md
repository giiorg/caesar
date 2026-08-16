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

## The E2E lane, and the one thing it fakes

`task web:e2e` is `go test -tags "integration,webe2e,embedui" -count=1 -timeout
15m ./internal/app/ -run TestWebE2E`, run from `backend/`. The harness is
`backend/internal/app/webe2e_test.go`; the browser half is
`web/e2e/approvals.spec.ts` under a single mobile project (Pixel 7).

It boots the **production** `runServe` against a real Postgres and takes exactly
two seams. The first is a listener: the test binds `127.0.0.1:0` itself and
hands the `*net.TCPListener` in, because `AppOrigin` must be known before boot
(`CheckOrigin` compares the browser's `Origin` by exact equality and a browser
always sends the port) and port 0 keeps the lane from colliding. That is a seam,
not a fake — production `Serve` receives the same listener it would have bound.

The second is the only substitution in the lane: **`runs.Executor`, and nothing
else.** `Routes`, every handler, the Orchestrator, `materializeNode`,
`appendEvents` — ordered ids under the counter lock, the outbox envelope and the
`NOTIFY`, all in one transaction — the tailer, the hub and the SSE wire are all
real. The seed builds its world through the real services (`identity.Bootstrap`,
`assets`, `workflows`); nothing in it writes SQL.

The substitution exists because of a workflow invariant, not for convenience. A
gate cannot be a root node: `workflows.validateConsumption` requires a gate to
examine exactly one plan or patch. So the only way to reach a *waiting* gate is
for an upstream agentic node to have produced a patch, and the fake executor is
what produces one without a container, a harness image or a model call.

**Raw SQL insertion of a gate row is banned, and `webe2e_test.go` must never
grow one.** It is the obvious shortcut and it is the wrong one: `appendEvents`
allocates the event ids that the resume path depends on, so a hand-written
`INSERT` desynchronises the counter and leaves later resume behaviour broken in
a way no assertion in this lane would catch. The lane would stay green while
quietly certifying a database state the system can never actually be in.

A control server (`httptest`, never part of caesar's routes) exposes
`POST /fixture/open-gate`, which Playwright calls *between* assertions. That
ordering is the point: the gate opens while the browser is already watching, so
the badge moving is attributable to the SSE stream and not to a page load. The
spec asserts one path — log in, see `/approvals` empty with no badge, POST to
the control server, then with no click, no reload and no navigation, watch
`approvals-badge` reach `1` within 30s and the run's title appear. The knob that
keeps it fast is `Orchestrator.PollInterval: 200ms`; the recorded wall time is
about 3.2 seconds against that 30s timeout, over three consecutive clean runs.

Two honest limits. It asserts one happy path and no failure mode — a stream that
never opens, a `forbidden` frame, a 500 from `/approvals`, a second gate on the
same run are all unexercised, because the lane's cost is a browser download and
a Docker Postgres. And it never *asserts* that the executor substitution took;
it infers it, on the grounds that `NotImplementedExecutor` cannot produce a
patch, so a succeeded `impl` node could only have come from the fake. True, but
reasoning rather than an assertion.

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
