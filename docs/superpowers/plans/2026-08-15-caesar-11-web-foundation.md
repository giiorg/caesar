# Plan 11 — Web Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stand up caesar's web client — scaffold, constitution, server-state/forms/theming abstractions, app shell — and prove it with one real vertical slice (`/login` → shell → `/approvals`) whose approvals badge moves from a gate opened by the real backend, with no user action.

**Architecture:** Route-centric colocation under TanStack Router file-based routing. All server data flows through TanStack Query over the client generated from `backend/api/openapi.json`; hand-written `fetch` against the API is a lint error. Realtime is three separately-testable layers — a pure SSE frame parser, a transport that owns reconnection but never correctness, and a cache bridge that **invalidates and never reconstructs**. The built bundle is embedded into the Go binary behind `//go:build embedui` and served same-origin, so there is no CORS and no reverse proxy.

**Tech Stack:** Node 24.19.0 · React 19.2.8 · TypeScript 5.9.3 · Vite 8.2.1 · TanStack Router 1.170.29 + Query 5.101.4 · react-hook-form 7.85.0 + Zod 4.4.3 · Tailwind CSS 4.3.3 · Radix (`radix-ui` 1.6.7) + vaul · Biome 2.5.8 · Vitest 4.1.10 + Testing Library + MSW 2.15.0 · Playwright 1.62.1 · Go 1.26.5 backend (embed, contracts, E2E harness).

**Source spec:** `docs/superpowers/specs/2026-08-15-caesar-web-foundation-design.md`. Read §§1–10 in full before Task 1 — it is 220 lines and every section is load-bearing.

---

## Global Constraints

Project-wide. They apply to **every** task below and are not re-litigated inside one.

- **Every dependency is pinned exactly.** No `^`, no `~`, no `*`, no ranges, anywhere in `web/package.json`. Task 1 adds the check that enforces this; every later task inherits it.
- **Node is 24.19.0 exactly**, stated in exactly one authoritative file, `web/.nvmrc`. Every CI lane reads it via `node-version-file:` and no lane restates a literal. `engines` in `package.json` stays as documentation only (npm merely warns on it).
- **TypeScript is 5.9.3 and may not be raised in this plan.** `openapi-typescript@7.13.0` declares `peerDependencies: {typescript: "^5.x"}`; TypeScript 6 or 7 fails `npm install` with `ERESOLVE`, and `--legacy-peer-deps` would break the `npm ci` the contracts lane already runs.
- **`skipLibCheck: true`**, because `@tanstack/router-core@1.171.24`'s own `.d.ts` does not typecheck (`TS2339: Property '__beforeLoadContext' does not exist on type 'MakeRouteMatch'`). This weakens `tsc --noEmit`, so the `Register` augmentation and the route-inference mutant of Task 1 are what keep application-level route typing falsifiable. **Never delete the mutant to make a build green.**
- **Named exports only. No barrel files. No default exports.** Enforced by Biome (`noBarrelFile`, `noReExportAll`, `noDefaultExport`, `useFilenamingConvention`) — with the single documented exception that Vite's config and Playwright's config require default exports and are listed as overrides.
- **A module with two or more direct importers must live at their nearest common ancestor directory.** A "consumer" is a direct importing module, static or dynamic, excluding tests, stories and generated files. Enforced by `task web:arch`, not by convention.
- **Non-semantic colors are unrepresentable, not merely discouraged.** `--color-*: initial` in the theme block removes Tailwind's default palette; `bg-white` and `text-red-500` stop resolving. `task web:arch` validates token *names*, never hunts for literals.
- **Every generated artifact is committed and regen-diffed**: `web/src/routeTree.gen.ts` in the web lane, `web/src/api/schema.d.ts` and `backend/api/openapi.json` in the contracts lane. No generated artifact in this repo is trusted to be hand-current.
- **Hand-written `fetch` against `/api` is banned by lint**, not by convention. All server data goes through the generated client and TanStack Query.
- **The cache bridge invalidates; it never reconstructs.** `setQueryData` from an SSE event payload is banned. The server emits its opening snapshot as ordinary event frames and re-sends a backlog on resume, so duplicate delivery is the documented normal case; invalidation is idempotent under it and reconstruction is not.
- **`correlation_id` is rendered on every error surface.** It is the only thread between a user's complaint and a server log line.
- **Go-side constraints carry over from the backend plans**: no ORMs, no DI frameworks, `log/slog` only, stdlib `flag` only, stdlib asserts + go-cmp (no testify), bounded contexts never import each other, contexts never import `internal/app`, `internal/platform` imports no business code.
- **Every task ends with `scripts/gate <abs-worktree-path> <sha> <branch>` green.** Tasks 12 and 14 change what that script asserts; until Task 14 lands, run the new web targets by hand alongside it.

---

## Pre-flight findings (read before Task 1)

Verified against the tree at `8314e51`. Several contradict what a reader of the spec alone would assume — the pre-flight lesson on this project is that five design rounds still missed twenty execution blockers, so these were established by reading source, not by reasoning.

1. **A gate can never be a root node, and this reshapes the E2E fixture.** `validateConsumption` in `internal/workflows/validate.go:207` requires *"a gate examines exactly one plan or patch"*. Every producer of a `plan` or `patch` artifact is agentic (`NodeType.Agentic()` — plan, implement, review). So the minimal gate-bearing workflow is `implement → gate`, and reaching `waiting_decision` means an agentic phase must actually complete. The spec's §7 assumed a cheaper path existed; it does not.

2. **Consequently the E2E harness fakes exactly one thing: `runs.Executor`.** *(Ruling — sofo, 2026-08-15.)* The Playwright lane boots the production `runServe` in-process from a Go test with a `fake.Executor` injected through an unexported composition-root seam. Production `Routes`, the real `Orchestrator`, the real `materializeNode` + `appendEvents` transaction, the real outbox and the real SSE stream all participate; only the agent container is substituted. All four of §7's beats survive. The rejected alternative — the shipped binary with `runner.enabled=true` against `caesar-fakecli:dev` — additionally needs a forge API stub, a container-reachable git remote on the egress network, a codex-kind harness and a materialized subscription credential, and is roughly five tasks of work for a substitution one layer lower.

3. **The runner suites are not a template for this.** `internal/app/credential_lifecycle_runnerintegration_test.go:180` and `runner_e2e_runnerintegration_test.go` reach a real container only through `localBareGit`, `fake.NewForge` and (in one case) `scriptedResolver` — seams that do **not** exist in the production `serve` path. Do not read those tests as proof that a real binary can execute a phase in CI.

4. **`ResolveHead` is a real HTTP call and the E2E harness must answer it.** `runs.Service.CreateTask` → `resolveIntake` → `forge.ResolveHead` → `GET {forge.github_base_url}/repos/{slug}/commits/{branch}` expecting `{"sha": "..."}` (`internal/platform/forge/github.go:72`). `config.forgeBaseProblems` permits scheme `http` ("or http for a lab"), so an `httptest.Server` base URL is a valid config value. No fake Forge port is needed.

5. **`httpx.Operation` already has an `Errors []int` field** (`internal/platform/httpx/operation.go`), and `contracts.Build` already renders each entry as a `ProblemDetails` response (`internal/contracts/contracts.go:209`). The §4 contracts task is therefore two small edits, not a generator change.

6. **Nothing in `backend/api/openapi.json` has a `required` array today**, so `openapi-typescript` renders every property optional — including `email` and `password` on `ApiLoginRequest`. The mechanism to fix it is the struct tag `required:"true"`, read by `swaggest/jsonschema-go` at `reflect.go:1144`. It defaults to false.

7. **`Routes` has no `/` handler at all.** `internal/app/server.go:22` registers `GET /healthz`, a problem+json catch-all on `/api/`, and the mounted operations. An unknown non-API path falls through to `net/http`'s plain-text 404. That fall-through is the seam the SPA fallback replaces.

8. **`scripts/gate` asserts the resolved command line of every lane, character for character** (`LANE_CMDS`, `RUNNER_CMDS`). Adding a tag to `backend:vet` / `backend:analyze` / `backend:vuln`, or adding a web lane, **requires a matching edit to `scripts/gate` in the same commit** or the gate refuses. This is deliberate: what the gate runs is stated in one place.

9. **`//go:embed` cannot reach outside its own package directory**, so the built bundle needs a Go package to live in. This plan creates `internal/webui` and points Vite's `build.outDir` straight at `backend/internal/webui/dist` — one artifact, one location, no copy step to go stale. `internal/webui` is not in `arch.rules.nonContext`, so it is treated as a bounded context; it imports only `internal/platform/*`, which contexts are permitted to do, so it passes as written. If `task backend:analyze` disagrees, add `webui` to `nonContext` with a comment saying it is serving infrastructure and not a domain.

10. **`internal/arch.CheckDeadConfig` requires every leaf config field to be read outside its defining package.** Any config field this plan adds must have a real reader or the arch tests fail.

11. **The contracts CI job pins `node-version: '20'`, which is EOL.** Task 1 moves it onto `.nvmrc` together with every new web lane — two lanes on different majors is a class of failure that only ever appears in CI.

12. **`.dockerignore` does not exclude `web/node_modules`.** It lists `.git`, `.task`, `.superpowers`, `docs`, `backend/bin`, `compose.dev.yaml`. Task 12 adds `web/node_modules` and the built `dist` before the Dockerfile gains a UI stage.

13. **Login really answers 401 and 429.** `internal/identity/login.go:19,21` classify `errBadCredentials` as `ClassUnauthenticated` and `errLoginLocked` as `ClassRateLimited`. The session cookie is `__Host-caesar_session` (`internal/platform/authn/authn.go:64`).

14. **`GET /auth/me` returns `{user, grants}`; `POST /auth/login` returns only the user.** So login success invalidates the `auth/me` query rather than seeding it from the login response, which would cache an entry missing the grants the rest of the app reads.

15. **`tsr` is published by `@tanstack/router-cli`, not by `@tanstack/router-plugin`.** `npm view @tanstack/router-plugin@1.168.32 bin` is empty; `npm view @tanstack/router-cli@1.167.30 bin` is `{ tsr: 'bin/tsr.cjs' }`. So a manifest carrying only the plugin has no `tsr` on its path, and **`npx tsr generate` would silently install `tsr` from the public registry — an unrelated package, "TypeScript Remove"**. Hence `@tanstack/router-cli@1.167.30` is a pinned devDependency.

    **`npm exec --` is NOT the protection here — corrected 2026-08-15, this bullet previously claimed it was.** Measured on npm 11.17.0 / node 24.19.0 against a copy of `web/` with `node_modules` removed: `npm exec -- tsr generate` fetched "TypeScript Remove" and ran it (`Project has 8 files … ✖ delete 2 files, remove 5 exports`), and `npx biome ci .` **exited 0 having emitted zero bytes** — a silently vacuous lint. `npx --no` does not close it once the npx cache is warm. Both invocation forms reach the network. The protection is therefore mechanical, not lexical: **every web lane in `Taskfile.yml` invokes the installed binary by path** — `./node_modules/.bin/biome`, `./node_modules/.bin/tsc`, `./node_modules/.bin/tsr`, `./node_modules/.bin/vite` — and `web/tools/check-mutants.mjs` spawns `node_modules/.bin/tsc` resolved from `import.meta.url`. A missing install then fails the lane with `stat …/node_modules/.bin/<tool>: no such file or directory`, exit 127. Task 14 mirrors these exact command lines into `scripts/gate`'s `LANE_CMDS`; `npx`/`npm exec` must not reappear in a web lane.

    The CLI version is not arbitrary — plugin 1.168.32 and cli 1.167.30 both depend on `@tanstack/router-generator@1.167.30` exactly, so the CLI and the Vite plugin emit the identical route tree. That shared generator is the whole premise of the `web:routes:check` drift gate; if you bump one, `npm view <pkg> dependencies` and confirm the generator still matches, or the drift check starts failing on a diff neither tool is wrong about.

16. **The `forbidden` frame is `event: forbidden\ndata: {"error":"forbidden"}\n\n` with no `id:` line** (`internal/realtime/stream.go:48`), and the idle heartbeat is the SSE **comment** `: hb\n\n` at `config.Realtime.HeartbeatInterval`, default 20s (`internal/realtime/sse.go:183`). Every event frame is `id: <int64>` / `event: <dotted kind>` / `data: <compact StreamFrame JSON>`.

---

## File Structure

**New — `web/` (the client):**

- `web/.nvmrc` — the single authoritative Node version.
- `web/biome.json` — the constitution's lint half: named exports, no barrels, filename convention, cross-group import bans, the `fetch` ban.
- `web/tsconfig.json` / `web/tsconfig.mutants.json` — strict compiler config, and the config the deliberate-failure fixtures compile under.
- `web/vite.config.ts` — plugins, `build.outDir` into `internal/webui/dist`, the dev `/api` proxy, the commit-stamp `define`.
- `web/vitest.config.ts` — jsdom environment, setup file, the two project shapes (pure/transport vs component).
- `web/playwright.config.ts` — mobile-viewport project only.
- `web/tools/arch/` — `main.ts` (entry), `import-graph.ts` (consumer-count rule + the cross-group matrix), `tokens.ts` (token-name check), `fixtures/` (the mutants every check must catch).
- `web/src/main.tsx` — the entry: providers, router mount.
- `web/src/router.ts` — the router instance **and** the `Register` module augmentation.
- `web/src/routes/` — file-based routes. The generated tree is `web/src/routeTree.gen.ts`, one level UP: it is `@tanstack/router-generator`'s default (`generatedRouteTree: './src/routeTree.gen.ts'`), and placing it inside `routes/` would both need configuring in two places and hand the generator its own output as a route file. Committed and regen-diffed.
- `web/src/api/schema.d.ts` — generated (exists today).
- `web/src/api/client.ts` — the `openapi-fetch` client and the auth-call exemption list.
- `web/src/api/errors.ts` — `ApiError`, built from the HTTP status and the `problem+json` body.
- `web/src/api/query-client.ts` — the `QueryClient`, its defaults, and the authorization-scoped refresh floor.
- `web/src/realtime/sse-parser.ts` — pure incremental SSE framing → typed frames. No I/O.
- `web/src/realtime/sse-connection.ts` — `fetch` + `ReadableStream`, backoff, heartbeat watchdog, `Last-Event-ID`, the `forbidden` probe.
- `web/src/realtime/cache-bridge.ts` — typed frame → query invalidation. No network, no writes.
- `web/src/forms/use-app-form.ts` — the typed form abstraction.
- `web/src/forms/problem-to-fields.ts` — `ApiError` → `setError` calls, by flat field name.
- `web/src/forms/fields/` — the field-component registry.
- `web/src/ui/` — shadcn-style primitives over Radix.
- `web/src/styles/tokens.css`, `theme-dark.css`, `theme-light.css` — semantic OKLCH custom properties.

**New — backend:**

- `backend/internal/webui/ui_embed.go` (`//go:build embedui`) — the embedded bundle and its SPA fallback.
- `backend/internal/webui/ui_noembed.go` (`//go:build !embedui`) — the twin that returns a typed `problem+json` saying no UI is embedded.
- `backend/internal/platform/httpx/security.go` — CSP, HSTS and the standard security headers.
- `backend/internal/app/webe2e_test.go` (`//go:build webe2e`) — the E2E harness: boots `runServe`, stands up the forge stub and the control server, drives Playwright.

**Modified — backend:**

- `internal/identity/api/api.go`, `internal/identity/api/dto.go`, `internal/runs/api/api.go`, `internal/runs/api/dto.go`, `internal/realtime/api/api.go` — `Errors` entries and `required:"true"` tags on the slice's endpoints.
- `internal/app/server.go` — `Routes` gains the UI handler.
- `internal/app/serve.go` — the unexported `serveOptions` seam.

**Modified — repo:**

- `Taskfile.yml` — `web:install`, `web:lint`, `web:typecheck`, `web:arch`, `web:test`, `web:build`, `web:routes:check`, `web:pins`, `web:e2e`, `web:ci`.
- `.github/workflows/ci.yml` — the `web` job, `ci-ok`'s `needs`, the `contracts` job's Node version, and the build-once/test-that-digest/promote-that-digest publish rewrite.
- `Dockerfile`, `.dockerignore` — the UI build stage and the `embedui` tag.
- `scripts/gate` — the new lanes and any changed resolved command line.
- `docs/superpowers/plans/2026-08-02-caesar-v1-roadmap.md` — row 11.

---

### Task 1: The scaffold, the pinned toolchain, and falsifiable route typing

The router's type safety is opt-in. Without the `Register` augmentation `tsc` passes and every `to` accepts an arbitrary string — verified both ways in the spec's §2. Since typed navigation is most of why this router was chosen, the augmentation and the mutant that proves it lands in the first task, not a later one.

**Files:**
- Create: `web/.nvmrc`, `web/biome.json`, `web/tsconfig.json`, `web/tsconfig.mutants.json`, `web/vite.config.ts`, `web/index.html`
- Create: `web/src/main.tsx`, `web/src/router.ts`, `web/src/routes/__root.tsx`, `web/src/routes/index.tsx`
- Create: `web/src/routeTree.gen.ts` (generated, committed)
- Create: `web/tools/check-pins.mjs`, `web/tools/mutants/bad-route-to.tsx`
- Modify: `web/package.json`
- Modify: `Taskfile.yml`, `.github/workflows/ci.yml` (contracts job Node version only)

**Interfaces:**
- Consumes: nothing.
- Produces: `router` (from `web/src/router.ts`, typed `Router` instance, registered via `declare module`); the task targets `web:install`, `web:lint`, `web:typecheck`, `web:routes:check`, `web:pins`; the route ids `/` and `/login` exist from Task 6 onward.

- [ ] **Step 1: Pin the toolchain**

`web/.nvmrc` — the file every lane reads. One line, no `v` prefix:

```
24.19.0
```

`web/package.json` — extend the existing file; do not start a new one. Every version exact.

```json
{
  "name": "caesar-web",
  "private": true,
  "type": "module",
  "engines": { "node": "24.19.0" },
  "scripts": {
    "api:gen": "openapi-typescript ../backend/api/openapi.json -o src/api/schema.d.ts",
    "dev": "vite",
    "build": "vite build",
    "lint": "biome ci .",
    "format": "biome check --write .",
    "typecheck": "tsc --noEmit",
    "test": "vitest run",
    "arch": "node --experimental-strip-types tools/arch/main.ts",
    "pins": "node tools/check-pins.mjs",
    "routes:gen": "tsr generate"
  },
  "dependencies": {
    "@tanstack/react-query": "5.101.4",
    "@tanstack/react-router": "1.170.29",
    "@hookform/resolvers": "5.8.0",
    "lucide-react": "1.31.0",
    "openapi-fetch": "0.17.0",
    "radix-ui": "1.6.7",
    "react": "19.2.8",
    "react-dom": "19.2.8",
    "react-hook-form": "7.85.0",
    "vaul": "1.1.2",
    "zod": "4.4.3"
  },
  "devDependencies": {
    "@biomejs/biome": "2.5.8",
    "@fontsource-variable/inter": "5.3.0",
    "@fontsource-variable/jetbrains-mono": "5.3.0",
    "@playwright/test": "1.62.1",
    "@tailwindcss/vite": "4.3.3",
    "@tanstack/router-cli": "1.167.30",
    "@tanstack/router-plugin": "1.168.32",
    "@testing-library/dom": "10.4.1",
    "@testing-library/react": "16.3.2",
    "@testing-library/user-event": "14.6.4",
    "@types/node": "24.13.3",
    "@types/react": "19.2.18",
    "@types/react-dom": "19.2.4",
    "@vitejs/plugin-react": "6.0.5",
    "@vitest/coverage-v8": "4.1.10",
    "jsdom": "30.0.1",
    "msw": "2.15.0",
    "openapi-typescript": "7.13.0",
    "tailwindcss": "4.3.3",
    "typescript": "5.9.3",
    "vite": "8.2.1",
    "vitest": "4.1.10"
  }
}
```

The split between the two blocks is not cosmetic: `openapi-fetch` ships in the bundle and is a `dependency`, while `openapi-typescript` is the build-time generator the contracts lane runs and stays a `devDependency`. `openapi-typescript` is the reason TypeScript is pinned at 5.9.3 (Global Constraints), so it is the one entry here that constrains another.

- [ ] **Step 2: Write the pin checker and run it against a deliberately broken manifest**

`web/tools/check-pins.mjs`:

```js
// A rule nobody checks is a rule that has already been broken. This one fails
// the lane on any version string that is not an exact pin — the same reason
// routeTree.gen.ts is regen-diffed rather than trusted.
import { readFileSync } from 'node:fs'

const pkg = JSON.parse(readFileSync(new URL('../package.json', import.meta.url), 'utf8'))
const blocks = ['dependencies', 'devDependencies', 'optionalDependencies', 'peerDependencies']
const bad = []
for (const block of blocks) {
  for (const [name, range] of Object.entries(pkg[block] ?? {})) {
    if (!/^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/.test(range)) {
      bad.push(`${block}.${name} = ${JSON.stringify(range)}`)
    }
  }
}
if (bad.length > 0) {
  console.error('web: every dependency must be pinned exactly (no ^, ~, *, or ranges):')
  for (const line of bad) console.error(`  - ${line}`)
  process.exit(1)
}
console.log(`web: ${blocks.reduce((n, b) => n + Object.keys(pkg[b] ?? {}).length, 0)} dependencies, all exactly pinned`)
```

Run: `cd web && node tools/check-pins.mjs` → expect exit 0 and a count.
Then temporarily change one version to `^19.2.8` and run again → expect exit 1 naming that entry. Revert.

- [ ] **Step 3: TypeScript config**

`web/tsconfig.json`:

```json
{
  "compilerOptions": {
    "target": "ES2023",
    "lib": ["ES2023", "DOM", "DOM.Iterable"],
    "module": "ESNext",
    "moduleResolution": "bundler",
    "jsx": "react-jsx",
    "types": ["vite/client", "node"],
    "strict": true,
    "noUncheckedIndexedAccess": true,
    "noImplicitOverride": true,
    "noFallthroughCasesInSwitch": true,
    "exactOptionalPropertyTypes": true,
    "verbatimModuleSyntax": true,
    "isolatedModules": true,
    "noEmit": true,
    "skipLibCheck": true,
    "baseUrl": ".",
    "paths": { "@/*": ["src/*"] }
  },
  "include": ["src", "vite.config.ts", "vitest.config.ts", "playwright.config.ts", "tools"],
  "exclude": ["tools/mutants"]
}
```

`skipLibCheck: true` is the stated decision from Global Constraints, not a copied default. `tools/mutants` is excluded here because those files exist to fail.

`web/tsconfig.mutants.json` — the config the deliberate failures compile under:

```json
{
  "extends": "./tsconfig.json",
  "include": ["src", "tools/mutants"],
  "exclude": []
}
```

- [ ] **Step 4: Vite config and the entry document**

`web/vite.config.ts` (default export is required by Vite and is one of Biome's two documented exceptions):

```ts
import { tanstackRouter } from '@tanstack/router-plugin/vite'
import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// COMMIT is the external oracle §8 needs: the workflow knows the SHA
// independently of the image, so a stale build cannot grade itself as fresh.
// "dev" is the local default and is never what CI publishes.
const commit = process.env.CAESAR_COMMIT ?? 'dev'

export default defineConfig({
  plugins: [tanstackRouter({ target: 'react', autoCodeSplitting: true }), react(), tailwindcss()],
  define: { __CAESAR_COMMIT__: JSON.stringify(commit) },
  resolve: { alias: { '@': new URL('./src', import.meta.url).pathname } },
  build: {
    // //go:embed cannot reach outside its own package, so the bundle is built
    // straight into the Go package that embeds it. One artifact, one location,
    // no copy step to go stale.
    outDir: '../backend/internal/webui/dist',
    emptyOutDir: true,
  },
  server: {
    // __Host- cookies require Secure, and browsers treat http://localhost as a
    // secure context — but only if the session stays same-origin. A dev setup
    // that talked to :8080 directly would silently drop the cookie. Task 12
    // asserts this proxy exists.
    proxy: { '/api': { target: 'http://127.0.0.1:8080', changeOrigin: false } },
  },
})
```

`web/index.html`:

```html
<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover" />
    <title>caesar</title>
  </head>
  <body>
    <div id="root"></div>
    <script type="module" src="/src/main.tsx"></script>
  </body>
</html>
```

Add to `web/src/vite-env.d.ts`:

```ts
/// <reference types="vite/client" />
declare const __CAESAR_COMMIT__: string
```

- [ ] **Step 5: The router, with the augmentation that makes its types exist**

`web/src/router.ts`:

```ts
import { createRouter } from '@tanstack/react-router'
import { routeTree } from './routeTree.gen'

export const router = createRouter({ routeTree })

// WITHOUT THIS BLOCK the router's type safety does not exist: tsc passes and
// every `to` accepts an arbitrary string. It is invisible in every other signal
// CI produces, which is why tools/mutants/bad-route-to.tsx exists.
declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}
```

`web/src/routes/__root.tsx`:

```tsx
import { Outlet, createRootRoute } from '@tanstack/react-router'

export const Route = createRootRoute({ component: RootLayout })

function RootLayout() {
  return <Outlet />
}
```

`web/src/routes/index.tsx`:

```tsx
import { createFileRoute } from '@tanstack/react-router'

export const Route = createFileRoute('/')({ component: Home })

function Home() {
  return <p>caesar</p>
}
```

`web/src/main.tsx`:

```tsx
import { RouterProvider } from '@tanstack/react-router'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { router } from './router'

const root = document.getElementById('root')
if (root === null) throw new Error('web: #root is missing from index.html')
createRoot(root).render(
  <StrictMode>
    <RouterProvider router={router} />
  </StrictMode>,
)
```

- [ ] **Step 6: Write the route-inference mutant and prove it fails**

`web/tools/mutants/bad-route-to.tsx` — this file MUST NOT COMPILE. That is its entire job.

```tsx
// MUTANT — this file is required to fail `tsc -p tsconfig.mutants.json`.
//
// It is the only signal that src/router.ts's `declare module` block is doing
// anything. Delete the augmentation and this file compiles clean, tsc --noEmit
// passes, and every typed navigation in the app silently degrades to `string`.
// If this file ever compiles, the router's type safety is gone; do not "fix"
// it by relaxing the expectation.
import { Link } from '@tanstack/react-router'

export function BadLink() {
  return <Link to="/does-not-exist">no such route</Link>
}
```

Run: `cd web && ./node_modules/.bin/tsc --noEmit -p tsconfig.mutants.json`
Expected: FAIL, with `TS2322` naming `"/does-not-exist"` as not assignable to the union of real route ids.

Now temporarily comment out the `declare module` block in `src/router.ts` and re-run.
Expected: PASS — which is the failure mode this mutant exists to catch. Restore the block and confirm FAIL again. **Do not skip this second half; a guard that passes its own fixture proves nothing.**

- [ ] **Step 7: Wrap the mutant in a script that asserts the failure**

`web/tools/check-mutants.mjs`:

```js
// Inverted assertions need a runner: a lane that merely ran `tsc` on the
// mutants would go red for the RIGHT outcome. This one fails when they compile.
import { spawnSync } from 'node:child_process'
import { existsSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

// Invoke the installed compiler by path, never `npx tsc`: npx falls back to the
// public registry when the binary is absent, and the package it lands on is not
// the TypeScript compiler. A missing install must be a named error here, not a
// mutant that "failed for an unrelated reason". (Pre-flight 15.)
const webRoot = fileURLToPath(new URL('..', import.meta.url))
const tsc = fileURLToPath(new URL('../node_modules/.bin/tsc', import.meta.url))
if (!existsSync(tsc)) {
  console.error(`web: ${tsc} is missing — run \`task web:install\` before the mutant check`)
  process.exit(1)
}

const cases = [
  {
    config: 'tsconfig.mutants.json',
    file: 'tools/mutants/bad-route-to.tsx',
    expect: 'TS2322',
    what: 'route-inference mutant (src/router.ts Register augmentation)',
  },
]
let failed = false
for (const c of cases) {
  const r = spawnSync(tsc, ['--noEmit', '--pretty', 'false', '-p', c.config], {
    cwd: webRoot,
    encoding: 'utf8',
  })
  const out = `${r.stdout}${r.stderr}`
  // The assertion must identify the MUTANT, not the run: `cases` is written to
  // grow, and a second fixture emitting the same code anywhere in the output
  // would otherwise keep this green with the guarded augmentation deleted.
  // tsc --pretty false emits `<path>(<line>,<col>): error TSxxxx: ...`.
  const named = out
    .split('\n')
    .some((line) => line.startsWith(`${c.file}(`) && line.includes(`error ${c.expect}:`))
  if (r.status === 0) {
    console.error(`web: ${c.what} COMPILED — the guard it protects is not active`)
    failed = true
  } else if (!named) {
    console.error(
      `web: ${c.what} failed, but ${c.file} did not report ${c.expect} — the mutant may be failing for an unrelated reason:\n${out}`,
    )
    failed = true
  } else {
    console.log(`web: ${c.what} correctly rejected (${c.file}: ${c.expect})`)
  }
}
process.exit(failed ? 1 : 0)
```

Add `"mutants": "node tools/check-mutants.mjs"` to `package.json` scripts.

- [ ] **Step 8: Biome — the constitution's lint half**

`web/biome.json`:

```json
{
  "$schema": "https://biomejs.dev/schemas/2.5.8/schema.json",
  "files": {
    "includes": ["**", "!**/node_modules/**", "!src/api/schema.d.ts", "!src/routeTree.gen.ts", "!**/dist/**"]
  },
  "formatter": { "enabled": true, "indentStyle": "space", "indentWidth": 2, "lineWidth": 100 },
  "javascript": { "formatter": { "quoteStyle": "single", "semicolons": "asNeeded" } },
  "linter": {
    "enabled": true,
    "rules": {
      "recommended": true,
      "style": {
        "noDefaultExport": "error",
        "useFilenamingConvention": { "level": "error", "options": { "filenameCases": ["kebab-case"] } },
        "noRestrictedImports": {
          "level": "error",
          "options": {
            "paths": {
              "node:fs": "the client runs in a browser",
              "node:path": "the client runs in a browser"
            }
          }
        }
      },
      "performance": { "noBarrelFile": "error", "noReExportAll": "error" }
    }
  },
  "overrides": [
    {
      "includes": ["vite.config.ts", "vitest.config.ts", "playwright.config.ts"],
      "linter": { "rules": { "style": { "noDefaultExport": "off" } } }
    },
    {
      "includes": ["src/routes/**"],
      "linter": { "rules": { "style": { "useFilenamingConvention": "off" } } }
    }
  ]
}
```

The `src/routes/**` override exists because TanStack's file-based routing derives route ids from filenames, so the router — not the convention — owns those names. The `fetch` ban is added in Task 5, when there is something to ban. **The cross-group import matrix is Task 3's**, in `tools/arch` rather than in Biome: it is a rule about directions between directories, the same shape as the existing consumer-count rule, and it wants a fixture and a mutant that a lint config cannot carry.

Run: `cd web && ./node_modules/.bin/biome ci .` → expect exit 0.

- [ ] **Step 9: Task targets**

Add to `Taskfile.yml`:

```yaml
  # Every web lane invokes the installed binary by path. `npx` and `npm exec --`
  # both fall back to the public registry when the binary is not in
  # node_modules: `npx biome ci .` then downloads an unrelated `biome` package
  # and exits 0 having checked nothing, and `npm exec -- tsr generate` runs
  # "TypeScript Remove" instead of the router CLI. A missing install has to be a
  # hard error, not a silently vacuous pass. (Pre-flight 15.)
  web:install:
    dir: web
    cmds:
      - npm ci

  web:lint:
    dir: web
    cmds:
      - ./node_modules/.bin/biome ci .

  web:typecheck:
    dir: web
    cmds:
      - ./node_modules/.bin/tsc --noEmit
      - node tools/check-mutants.mjs

  web:pins:
    dir: web
    cmds:
      - node tools/check-pins.mjs

  web:routes:check:
    desc: "Regenerate routeTree.gen.ts and fail on drift"
    dir: web
    cmds:
      - ./node_modules/.bin/tsr generate
      - git diff --exit-code -- src/routeTree.gen.ts
      # git diff is blind to an untracked or already-staged path, so the drift
      # gate would be vacuous exactly when the generated tree is not committed.
      # Same companion assertion as contracts:check.
      - test -z "$(git status --porcelain -- src/routeTree.gen.ts)"

  web:build:
    dir: web
    cmds:
      - ./node_modules/.bin/vite build
```

- [ ] **Step 10: Move the contracts lane onto `.nvmrc`**

In `.github/workflows/ci.yml`, the `contracts` job currently pins `node-version: '20'`, which is EOL. Replace:

```yaml
      - uses: actions/setup-node@v4
        with:
          node-version-file: web/.nvmrc
          cache: npm
          cache-dependency-path: web/package-lock.json
```

Every web lane added later uses this identical block. No lane restates a literal version.

- [ ] **Step 11: Install, generate, and verify the whole task**

```bash
cd web
npm install                          # writes package-lock.json; expect exit 0, no ERESOLVE
./node_modules/.bin/tsr generate     # writes src/routeTree.gen.ts (the generator's default)
./node_modules/.bin/tsc --noEmit     # expect 0 errors
node tools/check-mutants.mjs         # expect "correctly rejected (TS2322)"
node tools/check-pins.mjs            # expect the pinned-count line
./node_modules/.bin/biome ci .       # expect exit 0
./node_modules/.bin/vite build       # expect a bundle in ../backend/internal/webui/dist
```

If `npm install` reports `ERESOLVE`, do **not** reach for `--legacy-peer-deps`: it would break the `npm ci` the contracts lane runs. Read the conflict — the known one is TypeScript against `openapi-typescript`'s `^5.x` peer, which the 5.9.3 pin already answers.

- [ ] **Step 12: Ignore the build output and commit**

Add to `backend/.gitignore` (create it if absent) or the root `.gitignore`:

```
backend/internal/webui/dist/
```

```bash
git add web .github/workflows/ci.yml Taskfile.yml .gitignore
git commit -m "feat(web): pin the toolchain and make route typing falsifiable"
```

---

### Task 2: Theming — semantic OKLCH tokens with Tailwind's palette removed

§6's promise that "a new theme is a token file and never a component edit" is only true if the default palette is *unrepresentable*. Leaving it in place would let a component couple itself to Tailwind's colors while passing every other check, and the third theme would be a component edit after all.

**Files:**
- Create: `web/src/styles/tokens.css`, `web/src/styles/theme-dark.css`, `web/src/styles/theme-light.css`, `web/src/styles/app.css`
- Create: `web/src/theme/use-theme.ts`
- Create: `web/src/theme/use-theme.test.ts`
- Create: `web/vitest.config.ts`, `web/src/test/setup.ts`
- Create: `web/public/manifest.webmanifest`
- Modify: `web/src/main.tsx`, `web/index.html`

**Interfaces:**
- Consumes: the scaffold from Task 1.
- Produces: the semantic token names (`--color-ground`, `--color-surface`, `--color-surface-raised`, `--color-border`, `--color-text`, `--color-text-muted`, `--color-accent`, `--color-accent-contrast`, `--color-danger`, `--color-warning`, `--color-success`) that Task 3's checker validates against and every component uses; `useTheme(): { theme: Theme; setTheme(t: Theme): void }` where `type Theme = 'system' | 'dark' | 'light'`.

- [ ] **Step 1: Convert the mockup's hexes to OKLCH and verify the conversion**

The approved Terminal mockup's values are authoritative: ground `#0d1117`, surface `#161b22`, border `#21262d`, body `#c9d1d9`, accent `#58d6a3`. Verify the OKLCH values below rather than trusting them — write `web/tools/oklch-check.mjs`, run it once, and delete it after Step 2:

```js
// sRGB -> OKLab -> OKLCH (CSS Color 4). One-shot verification that the values
// committed in theme-dark.css really are the mockup's hexes.
const lin = (c) => (c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4)
function oklch(hex) {
  const [r, g, b] = [1, 3, 5].map((i) => lin(Number.parseInt(hex.slice(i, i + 2), 16) / 255))
  const l = Math.cbrt(0.4122214708 * r + 0.5363325363 * g + 0.0514459929 * b)
  const m = Math.cbrt(0.2119034982 * r + 0.6806995451 * g + 0.1073969566 * b)
  const s = Math.cbrt(0.0883024619 * r + 0.2817188376 * g + 0.6299787005 * b)
  const L = 0.2104542553 * l + 0.793617785 * m - 0.0040720468 * s
  const A = 1.9779984951 * l - 2.428592205 * m + 0.4505937099 * s
  const B = 0.0259040371 * l + 0.7827717662 * m - 0.808675766 * s
  const h = ((Math.atan2(B, A) * 180) / Math.PI + 360) % 360
  return `oklch(${L.toFixed(3)} ${Math.hypot(A, B).toFixed(3)} ${h.toFixed(1)})`
}
for (const hex of ['#0d1117', '#161b22', '#21262d', '#c9d1d9', '#58d6a3']) {
  console.log(hex, '->', oklch(hex))
}
```

Run: `cd web && node tools/oklch-check.mjs`. Use its output verbatim in Step 2; if it disagrees with the values written there, **the script wins** — it is deriving from the authoritative hexes.

- [ ] **Step 2: The token files**

`web/src/styles/tokens.css` — the theme block and the palette reset:

```css
@import 'tailwindcss';
@import '@fontsource-variable/inter';
@import '@fontsource-variable/jetbrains-mono';

@theme {
  /* THE PALETTE RESET. This is what makes "a new theme is a token file"
     true rather than aspirational: bg-white and text-red-500 stop resolving
     entirely, so a component CANNOT couple itself to Tailwind's colors.
     Removing this line silently un-enforces §6 and web:arch's token check. */
  --color-*: initial;

  --color-ground: var(--caesar-ground);
  --color-surface: var(--caesar-surface);
  --color-surface-raised: var(--caesar-surface-raised);
  --color-border: var(--caesar-border);
  --color-text: var(--caesar-text);
  --color-text-muted: var(--caesar-text-muted);
  --color-accent: var(--caesar-accent);
  --color-accent-contrast: var(--caesar-accent-contrast);
  --color-danger: var(--caesar-danger);
  --color-warning: var(--caesar-warning);
  --color-success: var(--caesar-success);

  --font-sans: 'Inter Variable', ui-sans-serif, system-ui, sans-serif;
  --font-mono: 'JetBrains Mono Variable', ui-monospace, monospace;

  /* Console-honest density, squarish corners (§6). */
  --radius-sm: 0.125rem;
  --radius-md: 0.25rem;
  --radius-lg: 0.375rem;
}
```

`web/src/styles/theme-dark.css` — the flagship, derived from the mockup:

```css
:root[data-theme='dark'] {
  --caesar-ground: oklch(0.177 0.014 259.5);          /* #0d1117 */
  --caesar-surface: oklch(0.226 0.017 261.4);         /* #161b22 */
  --caesar-surface-raised: oklch(0.279 0.017 262.2);  /* #21262d */
  --caesar-border: oklch(0.279 0.017 262.2);          /* #21262d */
  --caesar-text: oklch(0.845 0.017 259.0);            /* #c9d1d9 */
  --caesar-text-muted: oklch(0.650 0.018 259.0);
  --caesar-accent: oklch(0.799 0.135 165.0);          /* #58d6a3 */
  --caesar-accent-contrast: oklch(0.177 0.014 259.5);
  --caesar-danger: oklch(0.640 0.190 25.0);
  --caesar-warning: oklch(0.780 0.140 80.0);
  --caesar-success: oklch(0.799 0.135 165.0);
}
```

`web/src/styles/theme-light.css` — the twin over the identical semantic names:

```css
:root[data-theme='light'] {
  --caesar-ground: oklch(0.985 0.002 259.5);
  --caesar-surface: oklch(1.000 0.000 0);
  --caesar-surface-raised: oklch(0.960 0.004 259.5);
  --caesar-border: oklch(0.900 0.006 259.5);
  --caesar-text: oklch(0.250 0.014 259.5);
  --caesar-text-muted: oklch(0.500 0.014 259.5);
  --caesar-accent: oklch(0.560 0.130 165.0);
  --caesar-accent-contrast: oklch(0.995 0.002 165.0);
  --caesar-danger: oklch(0.520 0.200 25.0);
  --caesar-warning: oklch(0.600 0.140 80.0);
  --caesar-success: oklch(0.560 0.130 165.0);
}
```

`web/src/styles/app.css`:

```css
@import './tokens.css';
@import './theme-dark.css';
@import './theme-light.css';

html { color-scheme: dark light; }

body {
  background-color: var(--color-ground);
  color: var(--color-text);
  font-family: var(--font-sans);
  /* Safe-area insets: the phone is the flagship surface (§6). */
  padding: env(safe-area-inset-top) env(safe-area-inset-right)
           env(safe-area-inset-bottom) env(safe-area-inset-left);
}

/* 44px minimum targets, stated once rather than remembered per component. */
button, a[role='button'], [role='tab'] { min-height: 2.75rem; }
```

- [ ] **Step 3: Write the failing theme test**

`web/vitest.config.ts` first:

```ts
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vitest/config'

export default defineConfig({
  plugins: [react()],
  resolve: { alias: { '@': new URL('./src', import.meta.url).pathname } },
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
    globals: false,
    restoreMocks: true,
  },
})
```

`web/src/test/setup.ts`:

```ts
import '@testing-library/jest-dom/vitest'
```

(Add `@testing-library/jest-dom` at `6.9.1` to `devDependencies` and re-run `node tools/check-pins.mjs`.)

`web/src/theme/use-theme.test.ts`:

```ts
import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { useTheme } from './use-theme'

function mockPrefersDark(dark: boolean) {
  vi.stubGlobal('matchMedia', (q: string) => ({
    matches: dark && q.includes('dark'),
    media: q,
    addEventListener: () => {},
    removeEventListener: () => {},
  }))
}

describe('useTheme', () => {
  afterEach(() => {
    document.documentElement.removeAttribute('data-theme')
    localStorage.clear()
  })

  it('defaults to the system preference and resolves it onto data-theme', () => {
    mockPrefersDark(true)
    const { result } = renderHook(() => useTheme())
    expect(result.current.theme).toBe('system')
    expect(document.documentElement.dataset.theme).toBe('dark')
  })

  it('an explicit choice overrides the system preference and persists', () => {
    mockPrefersDark(true)
    const { result } = renderHook(() => useTheme())
    act(() => result.current.setTheme('light'))
    expect(document.documentElement.dataset.theme).toBe('light')
    expect(localStorage.getItem('caesar.theme')).toBe('light')
  })
})
```

- [ ] **Step 4: Run it and watch it fail**

Run: `cd web && npx vitest run src/theme/use-theme.test.ts`
Expected: FAIL — `Failed to resolve import "./use-theme"`.

- [ ] **Step 5: Implement `useTheme`**

`web/src/theme/use-theme.ts`:

```ts
import { useCallback, useEffect, useState } from 'react'

export type Theme = 'system' | 'dark' | 'light'

const KEY = 'caesar.theme'

function stored(): Theme {
  const v = localStorage.getItem(KEY)
  return v === 'dark' || v === 'light' ? v : 'system'
}

function resolve(theme: Theme): 'dark' | 'light' {
  if (theme !== 'system') return theme
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

export function useTheme() {
  const [theme, setThemeState] = useState<Theme>(stored)

  useEffect(() => {
    document.documentElement.dataset.theme = resolve(theme)
  }, [theme])

  const setTheme = useCallback((next: Theme) => {
    if (next === 'system') localStorage.removeItem(KEY)
    else localStorage.setItem(KEY, next)
    setThemeState(next)
  }, [])

  return { theme, setTheme }
}
```

- [ ] **Step 6: Run the tests and confirm they pass**

Run: `cd web && npx vitest run src/theme/use-theme.test.ts`
Expected: PASS, 2 tests.

- [ ] **Step 7: Prove the palette reset actually removed the palette**

This is the load-bearing half and it is not a unit test — it is a property of the built CSS. Run:

```bash
cd web
printf '%s\n' 'export function Bad() { return <div className="bg-white text-red-500" /> }' > src/palette-probe.tsx
npx vite build
grep -c 'bg-white\|text-red-500' ../backend/internal/webui/dist/assets/*.css || echo "0 matches — palette is gone"
rm src/palette-probe.tsx
```

Expected: **0 matches.** Tailwind cannot generate a utility for a namespace that was reset to `initial`, so the class is inert. If it emits a rule, `--color-*: initial` is not taking effect and Task 3's token check will be enforcing nothing.

- [ ] **Step 8: Wire the styles and the manifest**

`web/src/main.tsx` — add `import './styles/app.css'` as the first import.

`web/public/manifest.webmanifest`:

```json
{
  "name": "caesar",
  "short_name": "caesar",
  "start_url": "/",
  "display": "standalone",
  "background_color": "#0d1117",
  "theme_color": "#0d1117",
  "icons": []
}
```

The manifest promises installability, not offline behaviour — there is no service worker in this plan and adding one is not in scope.

`web/index.html` — add inside `<head>`:

```html
    <link rel="manifest" href="/manifest.webmanifest" />
    <meta name="theme-color" content="#0d1117" />
```

- [ ] **Step 9: Add `web:test` and commit**

```yaml
  web:test:
    dir: web
    cmds:
      - npx vitest run
```

```bash
rm web/tools/oklch-check.mjs
git add web Taskfile.yml
git commit -m "feat(web): semantic OKLCH tokens with Tailwind's palette removed"
```

---

### Task 3: `task web:arch` — the two checks no per-file linter can do

Consumer-count file placement needs a cross-file import graph, which no per-file linter has; the backend met this same gap with custom `go/analysis` analyzers and the frontend gets the twin. The token check is separate and rides along because a hex in a `className` creates no import edge — the graph checker is structurally blind to it.

**Files:**
- Create: `web/tools/arch/main.ts`, `web/tools/arch/import-graph.ts`, `web/tools/arch/tokens.ts`
- Create: `web/tools/arch/fixtures/` (the mutants both checks must catch)
- Create: `web/tools/arch/arch.test.ts`
- Modify: `Taskfile.yml`
- Modify: `web/tsconfig.json` — `allowImportingTsExtensions: true` and `exclude: ["tools/mutants", "tools/arch/fixtures"]`. Both are load-bearing, MEASURED on node 24.19.0 / tsc 5.9.3: `node --experimental-strip-types` will not complete an extensionless TypeScript specifier (`ERR_MODULE_NOT_FOUND`), so `main.ts` must import `./import-graph.ts` **with** the extension, and tsc then refuses that spelling with `TS5097` unless the flag is on. Separately, `fixtures/misplaced/routes/c/page.ts` imports `@/routes/a/helper`, which resolves to `src/routes/a/helper` and does not exist — `TS2307` unless the fixtures are excluded.
- Modify: `web/biome.json` — the two `files.includes` exclusions of Step 7. This file is also edited by Task 5; the two exclusions belong to **this** commit, because this task's own fixtures are what turn `web:lint` red without them.

**Interfaces:**
- Consumes: the token names from Task 2.
- Produces: `task web:arch`; `findMisplacedModules(root: string): Violation[]` and `findNonSemanticColors(root: string): Violation[]`, both exported, both `Violation = { file: string; line: number; message: string }`.

- [ ] **Step 1: Write the fixtures the checks must catch — before either check exists**

The rule is worthless undetected: consumer-count only bites when a second consumer appears, which is exactly the moment nobody notices. So the fixtures come first.

`web/tools/arch/fixtures/misplaced/routes/a/helper.ts`:

```ts
export const shared = 1
```

`web/tools/arch/fixtures/misplaced/routes/a/page.ts`:

```ts
import { shared } from './helper'
export const a = shared
```

`web/tools/arch/fixtures/misplaced/routes/b/page.ts`:

```ts
// The second consumer. helper.ts must now live at routes/, not routes/a/.
import { shared } from '../a/helper'
export const b = shared
```

`web/tools/arch/fixtures/misplaced/routes/d/page.ts` — the dynamic consumer. Without it the `import\s*\(` half of `IMPORT_RE` is unfalsifiable: MEASURED, deleting it from the regex left the whole suite green, while the Global Constraint at the top of this plan ("static or dynamic") and the checker's own comment both name that clause as load-bearing.

```ts
export const d = async () => (await import('../a/helper')).shared
```

`web/tools/arch/fixtures/misplaced/routes/a/helper.test.ts`:

```ts
// A test is NOT a consumer. If this file counts, every single-consumer module
// with a test would be reported and the rule would be noise.
import { shared } from './helper'
export const t = shared
```

`web/tools/arch/fixtures/colors/bad.tsx` — every evasion at once, because a checker that only catches hex literals catches almost nothing:

```tsx
export function Bad() {
  return (
    <>
      <div className="bg-white" />
      <div className="text-red-500" />
      <div className="bg-[#ff0000]" />
      <div className="text-[rgb(1,2,3)]" />
      <div style={{ color: 'rebeccapurple' }} />
      <div style={{ backgroundColor: '#0d1117' }} />
    </>
  )
}
```

`web/tools/arch/fixtures/colors/bad.css`:

```css
.x { color: #ff0000; }
.y { background: rgb(1 2 3); }
.z { border-color: rebeccapurple; }
```

`web/tools/arch/fixtures/colors/good.tsx` — the OVER-firing fixture, and it is the half that catches the likelier bug. A checker that keys on the prefix alone rejects `text-sm`, `border-t`, `border-dashed` and `shadow-sm`, none of which are colours; Task 6 adds `text-sm` three tasks later, so the failure would surface as an unrelated task going red for a reason nobody would look here for. Every line below must produce zero violations:

```tsx
export function Good() {
  return (
    <div className="bg-surface text-text-muted border-border border-t border-dashed text-sm shadow-sm bg-accent/10 text-danger/70 rounded-md p-2 to-warning">
      <span className="from-accent via-surface-raised" />
    </div>
  )
}
```

- [ ] **Step 2: Write the failing test**

`web/tools/arch/arch.test.ts`:

```ts
import { describe, expect, it } from 'vitest'
import { findCrossGroupImports, findMisplacedModules } from './import-graph'
import { findNonSemanticColors } from './tokens'

const fx = (p: string) => new URL(`./fixtures/${p}`, import.meta.url).pathname

describe('import-graph consumer-count rule', () => {
  it('reports a module with two consumers that is not at their common ancestor', () => {
    const v = findMisplacedModules(fx('misplaced'))
    expect(v).toHaveLength(1)
    expect(v[0]?.file).toContain('routes/a/helper.ts')
    expect(v[0]?.message).toContain('routes')
  })

  it('does not count tests as consumers', () => {
    // helper.test.ts imports helper.ts. If tests counted, the message would
    // name a different required home and the count above would still be 1 —
    // so assert on the required directory, not just the violation count.
    const v = findMisplacedModules(fx('misplaced'))
    expect(v[0]?.message).not.toContain('helper.test')
  })
})

describe('token check', () => {
  it('catches every non-semantic colour form, not only hex literals', () => {
    const v = findNonSemanticColors(fx('colors'))
    const lines = v.map((x) => `${x.file}:${x.line}`)
    // 6 in bad.tsx, 3 in bad.css. bg-white and text-red-500 are the ones a
    // literal-hunting checker misses entirely — they contain no colour at all.
    expect(v).toHaveLength(9)
    expect(v.some((x) => x.message.includes('bg-white'))).toBe(true)
    expect(v.some((x) => x.message.includes('text-red-500'))).toBe(true)
    expect(lines.every((l) => !l.includes('good.tsx'))).toBe(true)
  })

  it('reports each offending token exactly once', () => {
    // Without this, `bg-[#ff0000]` counts twice — once as a utility, once as a
    // hex literal — and every count assertion here becomes unreadable. Nine
    // offending lines, nine violations: the mapping has to be one to one, or
    // the count above passes for a reason nobody intended.
    const v = findNonSemanticColors(fx('colors'))
    const perLine = new Map<string, number>()
    for (const x of v) {
      const k = `${x.file}:${x.line}`
      perLine.set(k, (perLine.get(k) ?? 0) + 1)
    }
    expect([...perLine.values()].filter((n) => n > 1)).toEqual([])
    expect(perLine.size).toBe(9)
  })

  it('flags the named CSS colour for being named, not for its property', () => {
    // `.z { border-color: rebeccapurple }` is the trap: a prefix-only matcher
    // "catches" it by reading `border-color` as the utility `border-` + the
    // value `color`. It would then pass this fixture while being blind to
    // every named colour on a property it does not recognise.
    const v = findNonSemanticColors(fx('colors/bad.css'))
    const z = v.filter((x) => x.line === 3)
    expect(z).toHaveLength(1)
    expect(z[0]?.message).toContain('named CSS colour')
  })

  it('passes semantic utilities and every same-prefix non-colour utility', () => {
    // text-sm, border-t, border-dashed, shadow-sm and the /opacity forms all
    // share a prefix with a real colour utility and none of them is one.
    const v = findNonSemanticColors(fx('colors/good.tsx'))
    expect(v).toEqual([])
  })
})
```

- [ ] **Step 3: Run it and watch it fail**

Run: `cd web && ./node_modules/.bin/vitest run tools/arch/arch.test.ts` (never `npx` — pre-flight finding 15)
Expected: FAIL — `Failed to resolve import "./import-graph.ts"`.

- [ ] **Step 4: Implement the import-graph rule**

`web/tools/arch/import-graph.ts`:

```ts
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { dirname, extname, join, relative, resolve, sep } from 'node:path'

export type Violation = { file: string; line: number; message: string }

const SOURCE = new Set(['.ts', '.tsx'])
const GENERATED = /(routeTree\.gen\.ts|schema\.d\.ts)$/
const NOT_A_CONSUMER = /\.(test|spec|stories)\.tsx?$/

function walk(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir)) {
    const p = join(dir, e)
    if (statSync(p).isDirectory()) {
      if (e !== 'node_modules') walk(p, out)
    } else if (SOURCE.has(extname(p)) && !GENERATED.test(p)) {
      out.push(p)
    }
  }
  return out
}

// Static AND dynamic: a lazily-imported module is still a consumer, and
// counting only static edges would let a second consumer hide behind import().
const IMPORT_RE = /(?:from\s*|import\s*\(\s*)['"](\.[^'"]+)['"]/g

function resolveSpecifier(fromFile: string, spec: string): string | null {
  const base = resolve(dirname(fromFile), spec)
  for (const cand of [base, `${base}.ts`, `${base}.tsx`, join(base, 'index.ts'), join(base, 'index.tsx')]) {
    try {
      if (statSync(cand).isFile()) return cand
    } catch {
      /* not this candidate */
    }
  }
  return null
}

function commonAncestor(paths: string[]): string {
  const parts = paths.map((p) => dirname(p).split(sep))
  const head = parts[0] ?? []
  let i = 0
  while (i < head.length && parts.every((p) => p[i] === head[i])) i++
  return head.slice(0, i).join(sep)
}

/**
 * A module with two or more direct consumers must live at their nearest common
 * ancestor directory.
 *
 * "Consumer" is a DIRECT importing module — static or dynamic — excluding
 * tests, stories and generated files. Route-root reachability is the tempting
 * alternative and it is the wrong one: every shared module is reachable from
 * every route, so it would place everything at the top and assert nothing. The
 * two definitions disagree precisely on transitive helpers, which is most of
 * what this rule will ever see.
 *
 * THE REQUIRED HOME IS CLAMPED TO THE TARGET'S OWN GROUP ROOT — corrected
 * 2026-08-15; this block previously used the unclamped ancestor below and was
 * wrong. Spec §2 asks for colocation *within* the route tree, while `api/`,
 * `realtime/`, `forms/` and `ui/` are declared permanent homes, so a module
 * reaching them is shared by construction. MEASURED against a tree built from
 * this plan's own literal import lines, the unclamped rule reported
 * `api/errors.ts has 5 consumers (…) so it must live in .` — it contradicts the
 * File Structure it is meant to enforce. See the shipped
 * `web/tools/arch/import-graph.ts` for `groupRootOf` and `isWithin`, which this
 * block does not define; that file is authoritative, not this snippet.
 */
export function findMisplacedModules(root: string): Violation[] {
  const files = walk(root)
  const consumers = new Map<string, string[]>()

  for (const file of files) {
    if (NOT_A_CONSUMER.test(file)) continue
    const src = readFileSync(file, 'utf8')
    for (const m of src.matchAll(IMPORT_RE)) {
      const target = resolveSpecifier(file, m[1] as string)
      if (target === null || GENERATED.test(target)) continue
      const list = consumers.get(target) ?? []
      if (!list.includes(file)) list.push(file)
      consumers.set(target, list)
    }
  }

  const out: Violation[] = []
  for (const [target, importers] of consumers) {
    if (importers.length < 2) continue
    const groupRoot = groupRootOf(root, target)
    const ancestor = commonAncestor(importers)
    const required = isWithin(groupRoot, ancestor) ? ancestor : groupRoot
    if (dirname(target) === required) continue
    out.push({
      file: relative(root, target),
      line: 1,
      message:
        `${relative(root, target)} has ${importers.length} consumers ` +
        `(${importers.map((i) => relative(root, i)).join(', ')}) ` +
        `so it must live in ${relative(root, required) || '.'}`,
    })
  }
  return out.sort((a, b) => a.file.localeCompare(b.file))
}

/**
 * THE CROSS-GROUP MATRIX. §2 of the spec asks for the directions between
 * top-level groups to be mechanical rather than remembered, and this is where
 * that lives — not in Biome, because a lint config cannot carry a fixture and
 * a rule with no failing mutant is a rule nobody has tested.
 *
 * Only two directions are stated, because only two are load-bearing:
 *
 *   ui/**       imports NOTHING from api, realtime, auth or routes.
 *               `ui` is the presentational layer. The moment a button knows
 *               how to fetch, it stops being reusable and starts being a
 *               route, and the misplacement rule above cannot see the
 *               difference — a widget imported twice is legal wherever it sits.
 *
 *   api/**      imports NOTHING from routes, shell or ui.
 *               The generated client and its error type are the bottom of the
 *               stack. An import upward makes the contract layer depend on the
 *               chrome, and it is exactly the edge that turns `unwrap` into
 *               something that cannot be tested without rendering.
 *
 * realtime -> shell is deliberately NOT banned but IS eliminated: the
 * transport in Task 10 imports `ConnectionState` from
 * `shell/connection-indicator`, which is upside down — the transport produces
 * that state and the shell displays it. Task 8 declares the type in
 * `src/realtime/connection-state.ts` and the shell imports it from there.
 * Stating the rule and leaving the violation in the tree would teach the next
 * reader that the rules are decorative.
 */
const FORBIDDEN: ReadonlyArray<{ from: string; to: readonly string[]; why: string }> = [
  { from: 'ui', to: ['api', 'realtime', 'auth', 'routes'], why: 'ui is presentational; a component that fetches belongs to a route' },
  { from: 'api', to: ['routes', 'shell', 'ui'], why: 'the client is the bottom of the stack and must not depend on the chrome' },
]

const groupOf = (root: string, file: string): string => relative(root, file).split('/')[0] ?? ''

export function findCrossGroupImports(root: string): Violation[] {
  const out: Violation[] = []
  for (const file of walk(root)) {
    if (NOT_A_CONSUMER.test(file)) continue
    const from = groupOf(root, file)
    const rule = FORBIDDEN.find((r) => r.from === from)
    if (rule === undefined) continue
    readFileSync(file, 'utf8')
      .split('\n')
      .forEach((line, i) => {
        for (const m of line.matchAll(IMPORT_RE)) {
          const target = resolveSpecifier(file, m[1] as string)
          if (target === null) continue
          const to = groupOf(root, target)
          if (!rule.to.includes(to)) continue
          out.push({
            file: relative(root, file),
            line: i + 1,
            message: `${from}/ must not import ${to}/ — ${rule.why}`,
          })
        }
      })
  }
  return out.sort((a, b) => a.file.localeCompare(b.file))
}
```

Fixtures — **one per forbidden edge**, because a matrix tested by a single example is a matrix with one row anybody can trust and six nobody has.

> **EVERY IMPORT TARGET MUST EXIST AS A REAL FILE.** `resolveSpecifier` (above) `statSync`s each candidate and returns `null` when nothing is there, and `findCrossGroupImports` skips a `null` target. A fixture importing `../api/client` with no `fixtures/groups/api/client.ts` on disk therefore produces **zero** violations — the test fails, and the tempting repair is to change the expected count to 0, which leaves a permanently inert guard that passes forever. Create all nine files — six targets and three importers.

Targets, each one line, contents irrelevant beyond having an export:

```
fixtures/groups/api/client.ts               export const api = 1
fixtures/groups/realtime/sse-connection.ts  export const openAttentionStream = 1
fixtures/groups/auth/me-query.ts            export const meQueryOptions = 1
fixtures/groups/routes/index.tsx            export const Route = 1
fixtures/groups/shell/app-shell.tsx         export const AppShell = 1
fixtures/groups/ui/button.tsx               export const Button = 1
```

`fixtures/groups/ui/bad-widget.tsx` — four violations, one per banned target:

```tsx
import { api } from '../api/client'
import { openAttentionStream } from '../realtime/sse-connection'
import { meQueryOptions } from '../auth/me-query'
import { Route } from '../routes/index'
export const BadWidget = () => null
```

`fixtures/groups/api/bad-client.ts` — three:

```ts
import { AppShell } from '../shell/app-shell'
import { Button } from '../ui/button'
import { Route } from '../routes/index'
export const bad = 1
```

`fixtures/groups/ui/good-widget.tsx` — the over-firing half; imports within its own group and must yield zero:

```tsx
import { Button } from './button'
export const GoodWidget = () => Button
```

Add to `arch.test.ts`:

```ts
describe('cross-group imports', () => {
  it('names every forbidden edge, once per import', () => {
    const v = findCrossGroupImports(fx('groups'))
    // 4 from ui/bad-widget + 3 from api/bad-client. If this is 0, the fixture
    // import targets are missing from disk and the checker resolved nothing —
    // do NOT "fix" it by changing this number.
    expect(v).toHaveLength(7)
    for (const to of ['api', 'realtime', 'auth', 'routes']) {
      expect(v.some((x) => x.file.includes('bad-widget') && x.message.includes(`import ${to}/`))).toBe(true)
    }
    for (const to of ['shell', 'ui', 'routes']) {
      expect(v.some((x) => x.file.includes('bad-client') && x.message.includes(`import ${to}/`))).toBe(true)
    }
  })

  it('permits imports within a group', () => {
    // The root stays the GROUPS DIRECTORY and the filter is applied to the
    // result. Passing `fx('groups/ui/good-widget.tsx')` instead would make
    // `relative(root, file)` the empty string, so `groupOf` returns '', no
    // rule matches, and the assertion passes without the checker having
    // looked at anything — a green that means nothing.
    const v = findCrossGroupImports(fx('groups')).filter((x) => x.file.includes('good-widget'))
    expect(v).toEqual([])
  })
})
```

- [ ] **Step 5: Implement the token check**

`web/tools/arch/tokens.ts`:

```ts
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { extname, join, relative } from 'node:path'
import type { Violation } from './import-graph'

// The semantic namespace, and the ONLY colour names that may appear. It is a
// list of NAMES, not a hunt for literals — see below for why that inversion is
// the whole design.
const SEMANTIC = [
  'ground', 'surface', 'surface-raised', 'border', 'text', 'text-muted',
  'accent', 'accent-contrast', 'danger', 'warning', 'success',
] as const

const SEMANTIC_SET = new Set<string>(SEMANTIC)

/**
 * A utility is `prefix-value`, and the prefix ALONE does not make it a colour:
 * `text-sm`, `border-t`, `border-dashed` and `shadow-sm` share prefixes with
 * `text-muted`, `border-border` and `shadow-accent`. So this regex only
 * NOMINATES; classifyUtility decides. Getting that split wrong is not a
 * theoretical risk — a checker that flags on the prefix rejects `text-sm`,
 * which Task 6 adds three tasks later, and the failure surfaces as an
 * unrelated task going red.
 */
const UTILITY = /\b(bg|text|border|ring|fill|stroke|from|via|to|outline|decoration|shadow|accent|caret|divide|placeholder)-([A-Za-z0-9[\]#(),.%/_-]+)/g

/** Tailwind's default hues. The theme reset deletes them; naming one is the bug. */
const TW_HUE = /^(?:slate|gray|grey|zinc|neutral|stone|red|orange|amber|yellow|lime|green|emerald|teal|cyan|sky|blue|indigo|violet|purple|fuchsia|pink|rose)-(?:50|\d{3})$/
const TW_BARE = /^(?:white|black|transparent|current|inherit)$/

const CSS_COLOUR_FN = /#[0-9a-fA-F]{3,8}\b|\brgba?\(|\bhsla?\(|\boklch\(|\bcolor\(/
const NAMED = '(?:white|black|red|blue|green|rebeccapurple|gray|grey|silver|maroon|olive|lime|aqua|teal|navy|fuchsia|purple|yellow|orange)'
/** A named colour in a CSS *declaration value* — after `:`, before `;` or `}`. */
const CSS_NAMED_DECL = new RegExp(`:[^;{}]*\\b${NAMED}\\b`)
/** A named colour in a JSX inline style — `color: 'red'`, `backgroundColor: "navy"`. */
const JSX_NAMED_DECL = new RegExp(`(?:color|Color)\\s*:\\s*['"]\\s*${NAMED}\\s*['"]`)

/**
 * Three answers, not two. 'ok' is a semantic token, 'bad' is a colour that
 * evades the tokens, and 'not-a-colour' is the sizing/layout utility that
 * merely shares a prefix — the case a prefix-only matcher gets wrong.
 */
function classifyUtility(value: string): 'ok' | 'bad' | 'not-a-colour' {
  // `bg-accent/10` is the token at 10% opacity: still semantic.
  const base = value.replace(/\/(?:\d{1,3}|\[[^\]]*\])$/, '')
  if (SEMANTIC_SET.has(base)) return 'ok'
  // `bg-[#fff]`, `text-[rgb(1,2,3)]`, `bg-(--some-var)` — arbitrary values are
  // the deliberate escape hatch out of the token system, so they are the bug.
  if (base.startsWith('[') || base.startsWith('(')) return 'bad'
  if (TW_BARE.test(base) || TW_HUE.test(base)) return 'bad'
  return 'not-a-colour'
}

const SCAN = new Set(['.ts', '.tsx', '.css'])
const SKIP = /(tokens\.css|theme-dark\.css|theme-light\.css|routeTree\.gen\.ts|schema\.d\.ts)$/

function walk(dir: string, out: string[] = []): string[] {
  if (statSync(dir).isFile()) return [dir]
  for (const e of readdirSync(dir)) {
    const p = join(dir, e)
    if (statSync(p).isDirectory()) {
      if (e !== 'node_modules') walk(p, out)
    } else if (SCAN.has(extname(p)) && !SKIP.test(p)) {
      out.push(p)
    }
  }
  return out
}

/**
 * THE TRAP THIS IS WRITTEN AROUND: `<div className="bg-white text-red-500" />`
 * bypasses every semantic token and contains no hex, no rgb(), no oklch() —
 * there is nothing for a literal search to find. §6 closes the hole at the
 * source by resetting Tailwind's colour namespace, which lets this check
 * validate NAMES instead of hunting literals. The theme files themselves are
 * skipped: they are where colour is allowed to be spelled out.
 */
export function findNonSemanticColors(root: string): Violation[] {
  const out: Violation[] = []
  for (const file of walk(root)) {
    const isCSS = extname(file) === '.css'
    readFileSync(file, 'utf8')
      .split('\n')
      .forEach((line, i) => {
        const at = (message: string) => out.push({ file: relative(root, file), line: i + 1, message })

        // ONE violation per offending token. `bg-[#ff0000]` is a single
        // mistake, and a checker that reports it twice — once as a utility,
        // once as a hex literal — makes every count assertion in the test
        // below meaningless. So the utility pass runs first and BLANKS what
        // it consumed; the literal passes then see only what is left.
        let rest = ''
        let cursor = 0
        for (const m of line.matchAll(UTILITY)) {
          const verdict = classifyUtility(m[2] as string)
          if (verdict === 'not-a-colour') continue
          const start = m.index as number
          rest += line.slice(cursor, start)
          rest += ' '.repeat(m[0].length)
          cursor = start + m[0].length
          if (verdict === 'bad') at(`non-semantic colour utility "${m[0]}"`)
        }
        rest += line.slice(cursor)

        // Inline styles and stylesheet declarations both, since neither
        // creates an import edge the graph checker could see.
        if (CSS_COLOUR_FN.test(rest) && !rest.includes('var(--color-')) {
          at('literal colour value; use a semantic token')
        }
        if (isCSS ? CSS_NAMED_DECL.test(rest) : JSX_NAMED_DECL.test(rest)) {
          at('named CSS colour; use a semantic token')
        }
      })
  }
  return out
}
```

`web/tools/arch/main.ts`:

The `.ts` extensions below are mandatory, not stylistic. MEASURED on node 24.19.0: under `--experimental-strip-types`, `import { x } from './dep'` dies with `ERR_MODULE_NOT_FOUND: Cannot find module …/dep` — node's ESM resolver does not complete an extensionless specifier onto a `.ts` file. With the extension, tsc 5.9.3 then requires `allowImportingTsExtensions` (`TS5097`), which is why the Files block above modifies `tsconfig.json`.

```ts
import { findCrossGroupImports, findMisplacedModules } from './import-graph.ts'
import { findNonSemanticColors } from './tokens.ts'

const root = new URL('../../src', import.meta.url).pathname
const violations = [
  ...findMisplacedModules(root),
  ...findCrossGroupImports(root),
  ...findNonSemanticColors(root),
]
for (const v of violations) console.error(`${v.file}:${v.line}: ${v.message}`)
if (violations.length > 0) {
  console.error(`web:arch: ${violations.length} violation(s)`)
  process.exit(1)
}
console.log('web:arch: clean')
```

- [ ] **Step 6: Run the tests and confirm they pass**

Run: `cd web && ./node_modules/.bin/vitest run tools/arch/arch.test.ts` (never `npx` — pre-flight finding 15)
Expected: PASS. The count grew past the four tests drafted here as the fixtures grew — the `@/` alias consumer, the `import()` consumer, the group-root clamp and the token/CSS drift check are each a separate `it`. If the colour count is not exactly 9, fix the checker rather than the expectation — every one of those six lines in `bad.tsx` is a real evasion someone will write.

- [ ] **Step 7: Add the target, exclude fixtures from lint, and commit**

```yaml
  web:arch:
    desc: "The two constitution rules no per-file linter can express"
    dir: web
    cmds:
      - node --experimental-strip-types tools/arch/main.ts
```

Add `"!tools/arch/fixtures"` and `"!tools/mutants"` to `biome.json`'s `files.includes` — those directories exist to be wrong. **No trailing `/**`**: Task 1 established that a trailing `/**` does not match the directory itself. **These two lines must be in THIS task's commit.** MEASURED with the repo's Biome 2.5.8 against `HEAD`'s `biome.json` plus this task's `tools/` tree: `tools/arch/fixtures/colors/bad.css format ✖ File content differs from formatting output … Found 1 error.` With the two lines added and nothing else, `Checked 8 files … No fixes applied`, exit 0. Task 5 edits the same file; if the commits are split by hunk, do **not** drag Task 5's `"plugins"` line along without `web/tools/lint/no-qualified-fetch.grit` — MEASURED, that combination fails the lane with `Error(s) during loading of plugins: Cannot read file.`

Run: `task web:arch` → expect `web:arch: clean`.

```bash
git add web Taskfile.yml
git commit -m "feat(web): enforce file placement and semantic colour by import graph"
```

---

### Task 4: Close the contract gap on the slice's endpoints (backend)

`ApiError` is built from the HTTP status rather than the generated response union, because the document declares only `200` and `400` on these endpoints while login really answers `401` and `429`. That gap is worth closing rather than only routing around — but **scope discipline is the risk here, not correctness**. This task covers the slice's endpoints and stops. Widening it to every endpoint is Plan 12's to do as those screens arrive.

**Files:**
- Modify: `backend/internal/contracts/contracts_test.go` — the exact-response assertion for `listApprovals`
- Modify: `backend/internal/identity/api/api.go`, `backend/internal/identity/api/dto.go`
- Modify: `backend/internal/runs/api/api.go`, `backend/internal/runs/api/dto.go`
- Modify: `backend/internal/realtime/api/api.go`
- Regenerate: `backend/api/openapi.json`, `web/src/api/schema.d.ts`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `components.schemas.ApiLoginRequest` with `required: ["email","password"]`; `ApiMeResponse` with `required: ["user","grants"]`; `ApiUserResponse` and `ApiGrantResponse` fully required; `ApiApprovalListResponse.approvals` and `ApiApprovalResponse` fully required — so Task 5's generated types are non-optional where the client reads them.

- [ ] **Step 1: Write the failing contract test**

`backend/internal/contracts/slice_test.go`:

```go
package contracts

import (
	"encoding/json"
	"testing"
)

// TestSliceEndpointsDocumentTheirRealAnswers pins what Plan 11's web slice
// consumes. The client reads the STATUS, not the generated union, so these
// entries are documentation of what the handlers already do — but a login body
// whose email and password are both optional is not a contract the form layer
// can lean on, and openapi-typescript renders an unmarked property optional.
func TestSliceEndpointsDocumentTheirRealAnswers(t *testing.T) {
	raw, err := Build()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			Responses map[string]any `json:"responses"`
		} `json:"paths"`
		Components struct {
			Schemas map[string]struct {
				Required []string `json:"required"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}

	// ONLY statuses the handlers actually produce. 403 was listed here for
	// /approvals and /stream on the assumption that an unauthorized caller is
	// refused; neither handler does that. handleListApprovals SCOPES to the
	// grants the caller holds and answers 200 with an empty list when there
	// are none (runs/api/api.go), and the stream resolves a possibly-empty
	// scope the same way — its `forbidden` is a terminal FRAME over a 200 that
	// was already committed, not an HTTP status. Documenting a status the
	// server cannot send teaches the client to write a branch that never runs,
	// which is worse than silence.
	wantStatuses := map[string]map[string][]string{
		"/api/v1/auth/login": {"post": {"200", "400", "401", "429"}},
		"/api/v1/auth/me":    {"get": {"200", "400", "401"}},
		"/api/v1/approvals":  {"get": {"200", "400", "401"}},
		"/api/v1/stream":     {"get": {"200", "400", "401"}},
	}
	for path, methods := range wantStatuses {
		for method, codes := range methods {
			op, ok := doc.Paths[path][method]
			if !ok {
				t.Fatalf("%s %s is not in the document", method, path)
			}
			for _, code := range codes {
				if _, ok := op.Responses[code]; !ok {
					t.Errorf("%s %s: no %s response documented", method, path, code)
				}
			}
		}
	}

	wantRequired := map[string][]string{
		"ApiLoginRequest":          {"email", "password"},
		"ApiMeResponse":            {"user", "grants"},
		"ApiUserResponse":          {"id", "email", "role", "active", "created_at", "updated_at"},
		"ApiGrantResponse":         {"project_id", "level"},
		"ApiApprovalListResponse":  {"approvals"},
		"ApiApprovalResponse":      {"run_id", "task_id", "task_title", "node_id", "project_id", "requester_id", "attempt", "generation", "waiting_since"},
	}
	for name, want := range wantRequired {
		got := map[string]bool{}
		for _, r := range doc.Components.Schemas[name].Required {
			got[r] = true
		}
		for _, w := range want {
			if !got[w] {
				t.Errorf("%s: property %q is not required — openapi-typescript will render it optional", name, w)
			}
		}
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `cd backend && go test ./internal/contracts/ -run TestSliceEndpointsDocumentTheirRealAnswers -v`
Expected: FAIL — every `required` assertion, plus the missing `401`/`429` responses.

- [ ] **Step 3: Declare the statuses the handlers really answer**

`internal/identity/api/api.go` — on the `login` operation add `Errors: []int{http.StatusUnauthorized, http.StatusTooManyRequests}`; on `me` add `Errors: []int{http.StatusUnauthorized}`.

```go
		{
			Pattern: "POST /api/v1/auth/login", ID: "login", Summary: "Authenticate and set the session cookie",
			Request: LoginRequest{}, Response: UserResponse{}, Status: http.StatusOK,
			// identity/login.go classifies bad credentials as
			// ClassUnauthenticated and lockout as ClassRateLimited, so 401 is
			// the single most common outcome of this endpoint and 429 is the
			// second. A client switching on the generated union would have no
			// branch for either.
			Errors:  []int{http.StatusUnauthorized, http.StatusTooManyRequests},
			Handler: http.HandlerFunc(a.handleLogin),
		},
```

`internal/runs/api/api.go` — on `listApprovals` add `Errors: []int{http.StatusUnauthorized}`.
`internal/realtime/api/api.go` — on `streamAttention` add the same.

**This breaks an existing test, and it must be fixed in THIS commit.** `internal/contracts/contracts_test.go:113` asserts that `listApprovals` carries *exactly* two responses:

```go
	listed := doc.Paths["/api/v1/approvals"]["get"]
	if got := sortedKeys(listed.Responses); len(got) != 2 || got[0] != "200" || got[1] != "400" {
		t.Errorf("listApprovals responses = %v, want only 200 and 400", got)
	}
```

It was written as the negative half of Plan 08's 409 work — proof that the `Errors` loop documents a status only where it is declared — and `/approvals` was the operation that declared none. It now declares one. Update the assertion to `200, 400, 401` and keep it an EXACT comparison; the new slice test in Step 1 only checks that expected statuses are *present*, so exactness lives here and nowhere else. Loosening it to a subset check would delete the only guard against the loop documenting statuses everywhere.

Add `backend/internal/contracts/contracts_test.go` to this task's Modify list.

**Not 403 on either, and the reason is worth stating because the opposite is the intuitive guess.** Neither endpoint refuses an authenticated caller. `handleListApprovals` scopes to the projects the caller holds any grant on and returns 200 with an empty `approvals` array when that set is empty — seeing nothing and being refused are different answers, and this endpoint only ever gives the first. `streamAttention` resolves the same possibly-empty scope; its `forbidden` is a terminal frame written over a 200 that `NewWriter` committed long before the scope was known, which is precisely why that frame exists instead of a status. Verify before writing either line rather than trusting this paragraph. `rg -n 'ClassForbidden' internal/runs/api internal/realtime/api` does hit `runs/api/api.go` — but on `authorize`, the shared grant check that `decideGate` and its siblings call and that `handleListApprovals` does not. Read the two handler bodies and confirm neither reaches it.

- [ ] **Step 4: Mark the consumed properties required**

`internal/identity/api/dto.go`:

```go
type LoginRequest struct {
	Email    string `json:"email" required:"true"`
	Password string `json:"password" required:"true"`
}

type UserResponse struct {
	ID        string    `json:"id" required:"true"`
	Email     string    `json:"email" required:"true"`
	Role      string    `json:"role" required:"true"`
	Active    bool      `json:"active" required:"true"`
	CreatedAt time.Time `json:"created_at" required:"true"`
	UpdatedAt time.Time `json:"updated_at" required:"true"`
}

type GrantResponse struct {
	ProjectID string `json:"project_id" required:"true"`
	Level     string `json:"level" required:"true"`
}

type MeResponse struct {
	User   UserResponse    `json:"user" required:"true"`
	Grants []GrantResponse `json:"grants" required:"true"`
}
```

`internal/runs/api/dto.go` — the same treatment for `ApprovalResponse` (`run_id`, `task_id`, `task_title`, `node_id`, `project_id`, `requester_id`, `attempt`, `generation`, `waiting_since`) and `ApprovalListResponse.Approvals`.

**Do not touch any other DTO.** `grants` and `approvals` stay `type: ["array","null"]` — required and nullable are independent, and the handlers really can emit `null`. Task 5's client handles that with `?? []`.

- [ ] **Step 5: Run the test and confirm it passes**

Run: `cd backend && go test ./internal/contracts/ -run TestSliceEndpointsDocumentTheirRealAnswers -v`
Expected: PASS.

- [ ] **Step 6: Regenerate and confirm the change is additive**

```bash
task contracts:gen
git diff --stat backend/api/openapi.json web/src/api/schema.d.ts
git diff backend/api/openapi.json | grep '^-' | grep -v '^---'
```

Expected: the last command prints **nothing but reformatting context** — no removed response, no removed property. The change is additive by construction and this is what proves it. If a `-` line removes a schema or a status, something outside the slice was touched; revert it.

Then confirm the generated types really tightened:

```bash
grep -A 6 'ApiLoginRequest:' web/src/api/schema.d.ts
```

Expected: `email: string` and `password: string`, without `?`.

- [ ] **Step 7: Run the full contracts lane and commit**

Run: `task contracts:check` → expect exit 0 (it regenerates and diffs; a hand-edited artifact fails).

```bash
git add backend web/src/api/schema.d.ts
git commit -m "feat(contracts): document the web slice's real answers and required fields"
```

---

### Task 5: The generated client, `ApiError`, and the 401 interceptor with its exemption

The `problem+json` envelope becomes a typed error with exactly three consumers, and they are the whole error design. The exemption for auth calls is a property of the request, not a check the interceptor infers from the current route — a blanket redirect would fire on the single most common login outcome, remount the route, and erase the form-level error §5 requires it to show.

**Files:**
- Create: `web/src/api/errors.ts`, `web/src/api/errors.test.ts`
- Create: `web/src/api/client.ts`, `web/src/api/client.test.ts`
- Create: `web/src/api/query-client.ts`
- Create: `web/src/test/msw.ts`
- Modify: `web/biome.json`, `web/src/main.tsx`

**Interfaces:**
- Consumes: `web/src/api/schema.d.ts` (tightened by Task 4).
- Produces:
  - `class ApiError extends Error` with `type: string`, `title: string`, `status: number`, `detail: string`, `correlationId: string`, `fields: Array<{ field: string; message: string }>`, and `static async fromResponse(res: Response): Promise<ApiError>`.
  - `api` — the `openapi-fetch` client typed on `paths`.
  - `unwrap<T>(result: { data?: T; error?: unknown; response: Response }): Promise<T>` — returns `data` or throws `ApiError`.
  - `onUnauthenticated(handler: () => void): void` — registers the redirect the router installs in Task 7.
  - `queryClient` — the configured `QueryClient`.
  - `AUTH_SCOPED_QUERY_OPTIONS` — the bounded refresh floor of §3, spread into authorization-scoped queries.

- [ ] **Step 1: Write the failing error tests**

`web/src/api/errors.test.ts`:

```ts
import { describe, expect, it } from 'vitest'
import { ApiError } from './errors'

function problem(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/problem+json' },
  })
}

describe('ApiError.fromResponse', () => {
  it('carries the whole envelope, including the correlation id', async () => {
    const err = await ApiError.fromResponse(
      problem(400, {
        type: 'https://caesar.dev/problems/invalid',
        title: 'Invalid request',
        status: 400,
        detail: 'validation failed',
        correlation_id: 'abc123',
        errors: [{ field: 'email', message: 'must be an email' }],
      }),
    )
    expect(err.status).toBe(400)
    expect(err.title).toBe('Invalid request')
    expect(err.detail).toBe('validation failed')
    expect(err.correlationId).toBe('abc123')
    expect(err.fields).toEqual([{ field: 'email', message: 'must be an email' }])
  })

  it('takes the status from the RESPONSE, not the body', async () => {
    // The document declares only 200 and 400 on these endpoints while login
    // really answers 401. A client that trusted the body — or the generated
    // union — would have no branch for the most common login outcome.
    const err = await ApiError.fromResponse(problem(401, { title: 'Authentication required' }))
    expect(err.status).toBe(401)
  })

  it('survives a body that is not problem+json at all', async () => {
    const err = await ApiError.fromResponse(new Response('<html>502</html>', { status: 502 }))
    expect(err.status).toBe(502)
    expect(err.title).toBe('Request failed')
    expect(err.fields).toEqual([])
  })

  // The case above only covers UNPARSEABLE bodies, which the try/catch already
  // handles. These are the ones that PARSE and still are not problem
  // documents — the gap a catch block cannot see. Each threw a TypeError
  // before the shape guards existed, which does not merely lose the
  // correlation id: it escapes fromResponse entirely, so unwrap never throws
  // an ApiError and the 401 interceptor and error panel are both bypassed.
  it.each([
    ['null', null],
    ['an array', []],
    ['a bare string', 'nope'],
    ['a number', 7],
    ['errors as an object', { title: 'x', errors: {} }],
    ['errors as a string', { title: 'x', errors: 'none' }],
    ['a null entry in errors', { title: 'x', errors: [null] }],
  ])('does not throw on %s', async (_name, body) => {
    const err = await ApiError.fromResponse(problem(500, body))
    expect(err).toBeInstanceOf(ApiError)
    expect(err.status).toBe(500)
    expect(err.fields).toEqual([])
  })
})
```

- [ ] **Step 2: Run it and watch it fail**

Run: `cd web && npx vitest run src/api/errors.test.ts`
Expected: FAIL — `Failed to resolve import "./errors"`.

- [ ] **Step 3: Implement `ApiError`**

`web/src/api/errors.ts`:

```ts
import type { components } from './schema'

type Problem = components['schemas']['HttpxProblemDetails']

export type FieldError = { field: string; message: string }

/**
 * The problem+json envelope as a typed error. It has exactly three consumers
 * and they are the whole error design:
 *
 *  1. Forms map `fields` onto fields by FLAT NAME (xerrors.FieldError is
 *     {field, message} and the name is a plain identifier — a pointer-shaped
 *     mapper would match nothing on every real validation failure).
 *  2. A 401 from a call that required an existing session redirects to /login.
 *  3. `correlationId` is rendered on every error surface: it is the only thread
 *     between a user's complaint and a server log line, and a UI that swallows
 *     it severs that thread permanently.
 */
export class ApiError extends Error {
  readonly type: string
  readonly title: string
  readonly status: number
  readonly detail: string
  readonly correlationId: string
  readonly fields: FieldError[]

  constructor(init: {
    type: string
    title: string
    status: number
    detail: string
    correlationId: string
    fields: FieldError[]
  }) {
    super(init.detail !== '' ? init.detail : init.title)
    this.name = 'ApiError'
    this.type = init.type
    this.title = init.title
    this.status = init.status
    this.detail = init.detail
    this.correlationId = init.correlationId
    this.fields = init.fields
  }

  /**
   * Built from the HTTP STATUS, never from the generated response union. The
   * body is best-effort: a proxy 502 or a truncated stream is still an error
   * the UI must render honestly rather than crash on.
   */
  static async fromResponse(res: Response): Promise<ApiError> {
    // THIS FUNCTION MUST NOT THROW. It is the last thing standing between a
    // malformed response and the user, and every caller — unwrap, the 401
    // interceptor, the error panel — assumes it returns an ApiError. A
    // TypeError escaping here does not merely lose the correlation id; it
    // bypasses the whole error path and surfaces as an unhandled rejection.
    //
    // `JSON.parse` succeeding does NOT mean the body is a problem document.
    // `null`, `[]` and `"text"` are all valid JSON, and `null` in particular
    // survives the try/catch and then throws on the first property read —
    // which is why the check is `typeof === 'object' && !== null` and not a
    // truthiness test.
    let body: Partial<Problem> = {}
    try {
      const parsed: unknown = await res.clone().json()
      if (typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed)) {
        body = parsed as Partial<Problem>
      }
    } catch {
      /* not JSON; the status still is */
    }
    // Same reasoning one level down, twice. A server that sends `errors: {}`
    // or `errors: "none"` would otherwise reach `.map` on a non-array — and
    // an array is not enough either: `[null]` passes Array.isArray and then
    // yields a `{field: '', message: ''}` entry, an empty field error the
    // form layer would render as a blank bullet. Entries that are not objects
    // are dropped, not defaulted.
    const errors = (Array.isArray(body.errors) ? body.errors : []).filter(
      (e): e is { field?: string; message?: string } => typeof e === 'object' && e !== null,
    )
    return new ApiError({
      type: body.type ?? 'about:blank',
      title: body.title ?? 'Request failed',
      status: res.status,
      detail: body.detail ?? '',
      correlationId: body.correlation_id ?? res.headers.get('X-Correlation-ID') ?? '',
      fields: errors.map((e) => ({ field: e.field ?? '', message: e.message ?? '' })),
    })
  }
}
```

- [ ] **Step 4: Run the error tests and confirm they pass**

Run: `cd web && npx vitest run src/api/errors.test.ts`
Expected: PASS, 10 tests (3 envelope cases + the 7 malformed-body cases).

- [ ] **Step 5: Write the failing interceptor test**

`web/src/test/msw.ts`:

```ts
import { setupServer } from 'msw/node'

// MSW intercepts at the NETWORK level, so the generated client is exercised
// rather than stubbed. A hand-rolled fetch mock would prove the test's own
// double and nothing about openapi-fetch's request shaping.
export const server = setupServer()
```

Add to `web/src/test/setup.ts`:

```ts
import { afterAll, afterEach, beforeAll } from 'vitest'
import { server } from './msw'

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }))
afterEach(() => server.resetHandlers())
afterAll(() => server.close())
```

`web/src/api/client.test.ts`:

```ts
import { HttpResponse, http } from 'msw'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { api, onUnauthenticated, unwrap } from './client'
import { ApiError } from './errors'
import { server } from '../test/msw'

const problem = (status: number, body: Record<string, unknown>) =>
  HttpResponse.json(body, { status, headers: { 'content-type': 'application/problem+json' } })

describe('the generated client', () => {
  const redirect = vi.fn()
  beforeEach(() => {
    redirect.mockClear()
    onUnauthenticated(redirect)
  })

  it('returns typed data on success', async () => {
    server.use(
      http.get('/api/v1/auth/me', () =>
        HttpResponse.json({ user: { id: 'u1', email: 'a@b.c', role: 'admin', active: true, created_at: '', updated_at: '' }, grants: [] }),
      ),
    )
    const me = await unwrap(await api.GET('/api/v1/auth/me'))
    expect(me.user.email).toBe('a@b.c')
  })

  it('throws ApiError and redirects on a 401 from a session-requiring call', async () => {
    server.use(http.get('/api/v1/approvals', () => problem(401, { title: 'Authentication required', correlation_id: 'cid-1' })))
    await expect(unwrap(await api.GET('/api/v1/approvals'))).rejects.toBeInstanceOf(ApiError)
    expect(redirect).toHaveBeenCalledTimes(1)
  })

  it('DOES NOT redirect on a 401 from login itself', async () => {
    // POST /auth/login answers 401 for bad credentials. A blanket redirect
    // would fire on the most common login outcome, remount the route and erase
    // the form-level error §5 requires it to show. The exemption is a property
    // of the REQUEST, not a check inferred from the current route.
    server.use(
      http.post('/api/v1/auth/login', () =>
        problem(401, { title: 'Authentication required', detail: 'invalid email or password', correlation_id: 'cid-2' }),
      ),
    )
    const err = await unwrap(
      await api.POST('/api/v1/auth/login', { body: { email: 'a@b.c', password: 'nope' } }),
    ).catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).correlationId).toBe('cid-2')
    expect(redirect).not.toHaveBeenCalled()
  })

  it('does not redirect on a 401 from /auth/me either', async () => {
    // The session bootstrap ASKS whether a session exists. A redirect here
    // would be the guard fighting its own probe.
    server.use(http.get('/api/v1/auth/me', () => problem(401, { title: 'Authentication required' })))
    await expect(unwrap(await api.GET('/api/v1/auth/me'))).rejects.toBeInstanceOf(ApiError)
    expect(redirect).not.toHaveBeenCalled()
  })
})
```

- [ ] **Step 6: Run it and watch it fail**

Run: `cd web && npx vitest run src/api/client.test.ts`
Expected: FAIL — `Failed to resolve import "./client"`.

- [ ] **Step 7: Implement the client**

`web/src/api/client.ts`:

```ts
import createClient from 'openapi-fetch'
import type { paths } from './schema'
import { ApiError } from './errors'

// Same-origin by construction (§8): no CORS, no reverse proxy, and the
// __Host- session cookie rides every request because there is only one origin.
// The graduation path to a separate deployment is this one string.
export const api = createClient<paths>({ baseUrl: '', credentials: 'same-origin' })

/**
 * The authentication calls, exempt from the global 401 redirect.
 *
 * EXPLICIT rather than emergent. POST /auth/login answers 401 for bad
 * credentials and GET /auth/me answers 401 to mean "no session" — which is the
 * question the bootstrap is asking. Inferring the exemption from the current
 * route would break the moment a login form is rendered anywhere else.
 */
const AUTH_EXEMPT: ReadonlySet<string> = new Set([
  '/api/v1/auth/login',
  '/api/v1/auth/me',
])

let redirectToLogin: () => void = () => {}

/** Registered by the router in Task 7; a bare module cannot navigate. */
export function onUnauthenticated(handler: () => void): void {
  redirectToLogin = handler
}

export async function unwrap<T>(result: { data?: T; error?: unknown; response: Response }): Promise<T> {
  const { data, response } = result as { data?: T; response: Response }
  if (response.ok && data !== undefined) return data

  const err = await ApiError.fromResponse(response)
  if (err.status === 401 && !AUTH_EXEMPT.has(new URL(response.url, 'http://x').pathname)) {
    redirectToLogin()
  }
  throw err
}
```

**This was a decision procedure in an earlier draft and is now settled — do not reintroduce `FetchResponse` here.** At `openapi-fetch@0.17.0` the type is `FetchResponse<T extends Record<string | number, any>, Options, Media extends MediaType>`: the FIRST parameter is the operation's response map, not the success payload. The draft's `FetchResponse<never, T, \`${string}/${string}\`>` puts the payload in the `Options` slot and `never` where the response map belongs, so `data` resolves to `never` and every caller of `unwrap` silently loses its type — a signature that compiles and means nothing, which is worse than one that fails.

The structural shape above is what the client actually returns and is what `unwrap` already casts to on the next line. The property being protected is that callers get the success type, not that the helper mirrors the library's internal generics. Verify with `npm view openapi-fetch@0.17.0` and the shipped `dist/index.d.ts` if you touch it.

- [ ] **Step 8: Run the client tests and confirm they pass**

Run: `cd web && npx vitest run src/api/client.test.ts`
Expected: PASS, 4 tests.

- [ ] **Step 9: Ban the thing this replaced**

Add to `web/biome.json` under `linter.rules`:

Both rules live under **`style`**, not `nursery` or `suspicious`. Verified rather than assumed — `npx @biomejs/biome@2.5.8 explain noRestrictedGlobals` and `... explain noRestrictedImports` both report `Diagnostic category: lint/style/…`. A rule nested under the wrong group is not a typo Biome shrugs off: the config is rejected, or worse the key is ignored and the ban silently never applies.

```json
      "style": {
        "noRestrictedImports": {
          "level": "error",
          "options": {
            "paths": {
              "node:fs": "the client runs in a browser",
              "node:path": "the client runs in a browser"
            }
          }
        },
        "noRestrictedGlobals": {
          "level": "error",
          "options": { "deniedGlobals": { "fetch": "use the generated client in src/api/client.ts — a hand-written fetch bypasses the contract, the 401 interceptor and the correlation id" } }
        }
      }
```

Add an override exempting the three files that legitimately hold a `fetch`: `src/api/client.ts` (it builds the client), `src/realtime/sse-connection.ts` (§3 uses `fetch` + `ReadableStream` deliberately, because `EventSource` can deliver neither backoff nor a heartbeat watchdog), and `src/test/**`.

```json
    {
      "includes": ["src/api/client.ts", "src/realtime/sse-connection.ts", "src/test/**"],
      "linter": { "rules": { "style": { "noRestrictedGlobals": "off" } } }
    }
```

Verify the ban bites — this is the step that catches a misplaced group, since a rule Biome never applied looks exactly like a codebase with no violations. Add `const x = fetch('/api/v1/auth/me')` to `src/routes/index.tsx`, run `npx biome ci .`, expect a failure naming `lint/style/noRestrictedGlobals`, then remove it. If it passes, the rule is inert: re-run `npx @biomejs/biome@2.5.8 explain noRestrictedGlobals` and put it under whatever group that prints.

- [ ] **Step 10: The QueryClient and the bounded refresh floor**

`web/src/api/query-client.ts`:

```ts
import { QueryClient } from '@tanstack/react-query'

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // The stream says "something you are showing has changed" and never what
      // it changed to, so REST is the source of truth for list AND badge.
      // Staleness is therefore cheap to resolve and never guessed at.
      staleTime: 5_000,
      retry: (failureCount, error) => {
        // A 4xx will not become a 2xx by asking again, and retrying a 401
        // multiplies the redirect. Unclassified failures surface as permanent,
        // matching the backend's safe direction.
        const status = (error as { status?: number }).status ?? 0
        return status >= 500 && failureCount < 2
      },
    },
  },
})

/**
 * THE BOUNDED REFRESH FLOOR (§3). The stream cannot be the only refresh path,
 * because a NARROWING is silent: losing one grant out of several does not close
 * the stream and emits no event — the server keeps it open, drops frames from
 * the departed project, and its own test asserts that "a narrowing is not a
 * close". Grant deletion writes an audit record and no run_event, so nothing on
 * the wire will ever mention it.
 *
 * A cache refreshed only by attention frames would keep rendering an approval
 * the user can no longer act on, indefinitely, with no bug anywhere in the
 * client. This caps the exposure at a stated number of seconds. The server
 * still rejects every action on that row; what is at stake is the UI telling
 * the truth.
 *
 * Roadmap row 10a makes this relaxable — when the wire can express a scope
 * change, the interval can go.
 */
export const AUTH_SCOPED_QUERY_OPTIONS = {
  refetchOnWindowFocus: true,
  refetchInterval: 60_000,
} as const
```

`web/src/main.tsx` — wrap the router in `<QueryClientProvider client={queryClient}>`.

- [ ] **Step 11: Verify and commit**

```bash
cd web && npx vitest run && npx tsc --noEmit && npx biome ci . && node tools/arch/main.ts
```

```bash
git add web && git commit -m "feat(web): typed client, ApiError, and the 401 exemption for auth calls"
```

---

### Task 6: The form abstraction and its first real consumer, `/login`

The shared typed form abstraction lands **before** any form that has a feature behind it. This ordering is the explicit lesson of the 72-hand-wired-forms survey in decisions §10, and inverting it is how that outcome happens again. The abstraction and its consumer are one task because §5 requires the abstraction to ship having satisfied a real endpoint with real validation failures, not a fixture.

**Files:**
- Create: `web/src/forms/problem-to-fields.ts`, `web/src/forms/problem-to-fields.test.ts`
- Create: `web/src/forms/use-app-form.ts`
- Create: `web/src/forms/fields/text-field.tsx`
- Create: `web/src/forms/form-error.tsx`
- Create: `web/src/routes/login.tsx`, `web/src/routes/login.test.tsx`

**Interfaces:**
- Consumes: `ApiError`, `api`, `unwrap` (Task 5).
- Produces:
  - `applyProblemToForm<T>(err: ApiError, setError: UseFormSetError<T>, knownFields: readonly string[]): { formError: string | null }`
  - `useAppForm<S extends ZodType>(opts: { schema: S; onSubmit: (values: z.output<S>) => Promise<void> })` returning react-hook-form's return value plus `formError: string | null`, `correlationId: string | null`, `submit: FormEventHandler`, and **`formProps`** (see below).
  - `<TextField name label type? autoComplete? />` — the field-component registry's first entry.
  - `<FormError message correlationId />` — uniform error rendering.
  - Route `/login`.

> **CORRECTED AGAINST WHAT EXECUTION MEASURED (wave 4). The shipped files in
> `web/src/forms/` are authoritative where this section still disagrees.** Three claims
> below were false as written and each was caught by running it:
>
> 1. The login fixture email `a@b.c` is **rejected** by the pinned zod 4.4.3 — its TLD
>    group is `[A-Za-z]{2,}`, so a single-character TLD fails. Five of the six login tests
>    could never have passed. Corrected throughout to `user@example.com`.
> 2. `<form onSubmit={form.submit}>` around an `<input type="email">` means the browser's
>    own interactive constraint validation blocks submission and React's `onSubmit` never
>    fires — so the zod message and the `role="alert"` the form exists to show are
>    unreachable in a real browser, while a `type="text"` field would hide the bug
>    entirely. `noValidate` therefore lives in the abstraction: **every form must spread
>    `<form {...form.formProps}>`**, and Tasks 7, 8 and 11 must do the same. `submit` is
>    still returned, so writing `onSubmit={form.submit}` silently reintroduces the bug and
>    only a form with a constrained input type would catch it.
> 3. The `use-app-form.ts` doc comment's error count is wrong: the `<S extends ZodType>`
>    variant yields **8** errors under the pinned toolchain, not five, and `Object.keys` is
>    not among them. Its `ZodType<T, T>` conclusion is correct and was re-verified —
>    `ZodType<T>` alone fails with TS2769 on `_zod.input`.

- [ ] **Step 1: Write the failing mapper test**

`web/src/forms/problem-to-fields.test.ts`:

```ts
import { describe, expect, it, vi } from 'vitest'
import { ApiError } from '../api/errors'
import { applyProblemToForm } from './problem-to-fields'

const err = (init: Partial<ConstructorParameters<typeof ApiError>[0]>) =>
  new ApiError({ type: '', title: 'Invalid request', status: 400, detail: '', correlationId: 'cid', fields: [], ...init })

describe('applyProblemToForm', () => {
  it('maps field errors by FLAT NAME, not by JSON pointer', () => {
    // xerrors.FieldError is {field, message} and the name is a plain
    // identifier. A pointer-shaped mapper ("/email") would match nothing on
    // every real validation failure the backend produces.
    const setError = vi.fn()
    const out = applyProblemToForm(err({ fields: [{ field: 'email', message: 'must be an email' }] }), setError, ['email', 'password'])
    expect(setError).toHaveBeenCalledWith('email', { type: 'server', message: 'must be an email' })
    expect(out.formError).toBeNull()
  })

  it('falls back to a form-level error when the problem names no field', () => {
    // The 401 login really returns for bad credentials carries NO field at
    // all, so this is the primary path, not the edge.
    const setError = vi.fn()
    const out = applyProblemToForm(err({ status: 401, detail: 'invalid email or password' }), setError, ['email', 'password'])
    expect(setError).not.toHaveBeenCalled()
    expect(out.formError).toBe('invalid email or password')
  })

  it('routes an unknown field name to the form level rather than dropping it', () => {
    const setError = vi.fn()
    const out = applyProblemToForm(err({ fields: [{ field: 'tenant', message: 'no such tenant' }] }), setError, ['email', 'password'])
    expect(setError).not.toHaveBeenCalled()
    expect(out.formError).toContain('no such tenant')
  })
})
```

- [ ] **Step 2: Run it and watch it fail**

Run: `cd web && npx vitest run src/forms/problem-to-fields.test.ts`
Expected: FAIL — module not found.

- [ ] **Step 3: Implement the mapper**

`web/src/forms/problem-to-fields.ts`:

```ts
import type { FieldValues, Path, UseFormSetError } from 'react-hook-form'
import type { ApiError } from '../api/errors'

export function applyProblemToForm<T extends FieldValues>(
  err: ApiError,
  setError: UseFormSetError<T>,
  knownFields: readonly string[],
): { formError: string | null } {
  const unroutable: string[] = []
  for (const f of err.fields) {
    if (knownFields.includes(f.field)) {
      setError(f.field as Path<T>, { type: 'server', message: f.message })
    } else {
      // Never dropped. A message the form cannot place is still the server's
      // explanation of why the submit failed, and swallowing it leaves the
      // user staring at a form that refused with no reason given.
      unroutable.push(f.message)
    }
  }
  if (unroutable.length > 0) return { formError: unroutable.join('; ') }
  const routed = err.fields.length > unroutable.length
  if (routed) return { formError: null }
  return { formError: err.detail !== '' ? err.detail : err.title }
}
```

- [ ] **Step 4: Run the mapper tests and confirm they pass**

Run: `cd web && npx vitest run src/forms/problem-to-fields.test.ts`
Expected: PASS, 3 tests.

- [ ] **Step 5: Implement `useAppForm` and the field registry**

`web/src/forms/use-app-form.ts`:

```ts
import { zodResolver } from '@hookform/resolvers/zod'
import { useState } from 'react'
import { type DefaultValues, type FieldValues, type Resolver, type UseFormReturn, useForm } from 'react-hook-form'
import type { ZodType } from 'zod'
import { ApiError } from '../api/errors'
import { applyProblemToForm } from './problem-to-fields'

/**
 * Parameterized on the VALUES type, not on the schema type.
 *
 * `<S extends ZodType>` reads more naturally and does not compile: bare
 * `ZodType` in Zod 4 has `unknown` for both input and output, so `z.output<S>`
 * is `unknown`, and react-hook-form needs a `FieldValues` (a record) at every
 * position. The pinned toolchain reports five separate errors — at
 * `zodResolver`, `defaultValues`, `onSubmit(values)`, `Object.keys` and the
 * final cast. `ZodType<T, T>` rather than `ZodType<T>` because @hookform
 * /resolvers 5.8.0 constrains the schema's INPUT to FieldValues too; a plain
 * `z.object({...})` satisfies it with no cast at the call site.
 */
export type AppForm<T extends FieldValues> = UseFormReturn<T> & {
  formError: string | null
  correlationId: string | null
  submit: (e?: React.BaseSyntheticEvent) => Promise<void>
}

/**
 * react-hook-form + Zod resolver + the generated request types, with one field
 * registry, uniform error rendering, and a submit orchestrator that catches
 * ApiError and calls setError per FLAT field name.
 *
 * The wrapper is also what makes react-hook-form 8 survivable: v8 is in beta
 * and a migration touches THIS FILE rather than every form — the same argument
 * §5 makes for building the abstraction before any feature form.
 */
export function useAppForm<T extends FieldValues>(opts: {
  schema: ZodType<T, T>
  defaultValues: DefaultValues<T>
  onSubmit: (values: T) => Promise<void>
}): AppForm<T> {
  const [formError, setFormError] = useState<string | null>(null)
  const [correlationId, setCorrelationId] = useState<string | null>(null)
  const form = useForm<T>({
    // The one cast, and it is narrow: the resolver's own generics carry a
    // third TContext parameter this wrapper does not use.
    resolver: zodResolver(opts.schema) as Resolver<T>,
    defaultValues: opts.defaultValues,
  })

  const submit = form.handleSubmit(async (values) => {
    setFormError(null)
    setCorrelationId(null)
    try {
      await opts.onSubmit(values)
    } catch (e) {
      if (!(e instanceof ApiError)) throw e
      // Rendered on EVERY error surface: it is the only thread between a
      // user's complaint and a server log line.
      setCorrelationId(e.correlationId !== '' ? e.correlationId : null)
      const { formError: fe } = applyProblemToForm(e, form.setError, Object.keys(opts.defaultValues))
      setFormError(fe)
    }
  })

  return Object.assign(form, { formError, correlationId, submit })
}
```

`web/src/forms/fields/text-field.tsx`:

```tsx
import { useFormContext } from 'react-hook-form'

export function TextField(props: {
  name: string
  label: string
  type?: 'text' | 'email' | 'password'
  autoComplete?: string
}) {
  const { register, formState } = useFormContext()
  const error = formState.errors[props.name]
  const id = `field-${props.name}`
  return (
    <div className="flex flex-col gap-1">
      <label htmlFor={id} className="text-text-muted text-sm">
        {props.label}
      </label>
      <input
        id={id}
        type={props.type ?? 'text'}
        autoComplete={props.autoComplete}
        aria-invalid={error !== undefined}
        aria-describedby={error !== undefined ? `${id}-error` : undefined}
        className="bg-surface text-text border-border rounded-md border px-3 py-2"
        {...register(props.name)}
      />
      {error !== undefined && (
        <p id={`${id}-error`} role="alert" className="text-danger text-sm">
          {String(error.message)}
        </p>
      )}
    </div>
  )
}
```

`web/src/forms/form-error.tsx`:

```tsx
export function FormError(props: { message: string | null; correlationId: string | null }) {
  // Returning null on a null MESSAGE would drop the correlation id in the most
  // common server-rejection shape there is: applyProblemToForm returns
  // formError null whenever every error mapped to a known field, so a 400 with
  // per-field messages would render field errors and no reference at all —
  // exactly the case a user reports and support cannot trace.
  if (props.message === null && props.correlationId === null) return null
  return (
    <div role="alert" className="border-danger text-danger rounded-md border px-3 py-2 text-sm">
      {props.message !== null && <p>{props.message}</p>}
      {props.correlationId !== null && (
        <p className="text-text-muted font-mono text-xs">reference: {props.correlationId}</p>
      )}
    </div>
  )
}
```

- [ ] **Step 6: Write the failing login test**

`web/src/routes/login.test.tsx`:

```tsx
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HttpResponse, http } from 'msw'
import { describe, expect, it, vi } from 'vitest'
import { server } from '../test/msw'
import { LoginForm } from './login'

const problem = (status: number, body: Record<string, unknown>) =>
  HttpResponse.json(body, { status, headers: { 'content-type': 'application/problem+json' } })

describe('the login form', () => {
  it('rejects a malformed email before any request is made', async () => {
    render(<LoginForm onSuccess={vi.fn()} />)
    await userEvent.type(screen.getByLabelText(/email/i), 'not-an-email')
    await userEvent.type(screen.getByLabelText(/password/i), 'hunter2hunter2')
    await userEvent.click(screen.getByRole('button', { name: /sign in/i }))
    expect(await screen.findByRole('alert')).toHaveTextContent(/email/i)
  })

  it('shows the problem detail and correlation id on bad credentials, and does NOT navigate', async () => {
    // This is the single most common outcome of this endpoint. A blanket 401
    // redirect would remount the route and erase exactly this message.
    server.use(
      http.post('/api/v1/auth/login', () =>
        problem(401, { title: 'Authentication required', detail: 'invalid email or password', correlation_id: 'cid-login' }),
      ),
    )
    const onSuccess = vi.fn()
    render(<LoginForm onSuccess={onSuccess} />)
    await userEvent.type(screen.getByLabelText(/email/i), 'user@example.com')
    await userEvent.type(screen.getByLabelText(/password/i), 'wrongpassword')
    await userEvent.click(screen.getByRole('button', { name: /sign in/i }))

    expect(await screen.findByText('invalid email or password')).toBeInTheDocument()
    expect(screen.getByText(/cid-login/)).toBeInTheDocument()
    expect(onSuccess).not.toHaveBeenCalled()
    expect(screen.getByLabelText(/email/i)).toHaveValue('user@example.com')
  })

  it('surfaces lockout as its own message rather than as bad credentials', async () => {
    server.use(
      http.post('/api/v1/auth/login', () =>
        problem(429, { title: 'Rate limited', detail: 'too many failed attempts, try again later', correlation_id: 'cid-lock' }),
      ),
    )
    render(<LoginForm onSuccess={vi.fn()} />)
    await userEvent.type(screen.getByLabelText(/email/i), 'user@example.com')
    await userEvent.type(screen.getByLabelText(/password/i), 'wrongpassword')
    await userEvent.click(screen.getByRole('button', { name: /sign in/i }))
    expect(await screen.findByText(/too many failed attempts/i)).toBeInTheDocument()
  })

  it('calls onSuccess on a 200', async () => {
    server.use(
      http.post('/api/v1/auth/login', () =>
        HttpResponse.json({ id: 'u1', email: 'user@example.com', role: 'admin', active: true, created_at: '', updated_at: '' }),
      ),
    )
    const onSuccess = vi.fn()
    render(<LoginForm onSuccess={onSuccess} />)
    await userEvent.type(screen.getByLabelText(/email/i), 'user@example.com')
    await userEvent.type(screen.getByLabelText(/password/i), 'correcthorsebattery')
    await userEvent.click(screen.getByRole('button', { name: /sign in/i }))
    await vi.waitFor(() => expect(onSuccess).toHaveBeenCalledTimes(1))
  })
})
```

- [ ] **Step 7: Run it and watch it fail**

Run: `cd web && npx vitest run src/routes/login.test.tsx`
Expected: FAIL — `LoginForm` is not exported from `./login`.

- [ ] **Step 8: Implement the login route**

`web/src/routes/login.tsx`:

```tsx
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { FormProvider } from 'react-hook-form'
import { z } from 'zod'
import { api, unwrap } from '../api/client'
import { FormError } from '../forms/form-error'
import { TextField } from '../forms/fields/text-field'
import { useAppForm } from '../forms/use-app-form'

export const Route = createFileRoute('/login')({ component: LoginPage })

// Zod 4 promotes string formats to top-level factories: z.email(), not
// z.string().email().
const schema = z.object({
  email: z.email('enter a valid email address'),
  password: z.string().min(1, 'enter your password'),
})

export function LoginForm(props: { onSuccess: () => void }) {
  const form = useAppForm({
    schema,
    defaultValues: { email: '', password: '' },
    onSubmit: async (values) => {
      await unwrap(await api.POST('/api/v1/auth/login', { body: values }))
      props.onSuccess()
    },
  })

  return (
    <FormProvider {...form}>
      <form {...form.formProps} className="flex w-full max-w-sm flex-col gap-4">
        <FormError message={form.formError} correlationId={form.correlationId} />
        <TextField name="email" label="Email" type="email" autoComplete="username" />
        <TextField name="password" label="Password" type="password" autoComplete="current-password" />
        <button
          type="submit"
          disabled={form.formState.isSubmitting}
          className="bg-accent text-accent-contrast rounded-md px-4 py-2 font-medium"
        >
          Sign in
        </button>
      </form>
    </FormProvider>
  )
}

function LoginPage() {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  return (
    <main className="flex min-h-dvh items-center justify-center p-6">
      <LoginForm
        onSuccess={async () => {
          // POST /auth/login returns only the user; GET /auth/me returns the
          // user AND their grants. Seeding the cache from the login response
          // would create an auth/me entry missing the grants the rest of the
          // app reads, so this invalidates and lets the loader refetch.
          await queryClient.invalidateQueries({ queryKey: ['auth', 'me'] })
          await navigate({ to: '/' })
        }}
      />
    </main>
  )
}
```

- [ ] **Step 9: Run the login tests and confirm they pass**

Run: `cd web && npx vitest run src/routes/login.test.tsx`
Expected: PASS, 4 tests.

- [ ] **Step 10: Regenerate routes, verify, commit**

```bash
cd web && npm exec -- tsr generate && npx tsc --noEmit && npx vitest run && npx biome ci . && node tools/arch/main.ts
```

```bash
git add web && git commit -m "feat(web): typed form abstraction with /login as its first consumer"
```

---

### Task 7: Session bootstrap — the shell renders only after `/auth/me` resolves

The design as first written specified only one auth transition, a redirect *after* a request came back 401, which leaves every stub route rendering the full shell to an unauthenticated visitor — because a stub makes no request to be rejected.

**Files:**
- Create: `web/src/auth/me-query.ts`
- Create: `web/src/routes/_app.tsx`
- Move: `web/src/routes/index.tsx` → `web/src/routes/_app/index.tsx`
- Create: `web/src/routes/_app/tasks.tsx` — a placeholder, replaced by Task 8
- Create: `web/src/routes/_app.test.tsx`
- Create: `web/src/errors/api-error-panel.tsx`, `web/src/errors/api-error-panel.test.tsx`
- Modify: `web/src/main.tsx`, `web/src/router.ts`

**Interfaces:**
- Consumes: `api`, `unwrap`, `queryClient`, `onUnauthenticated` (Task 5); the `/login` route (Task 6).
- Produces: `meQueryOptions` — `{ queryKey: ['auth','me'], queryFn }`; the `_app` layout route every protected route nests under; `useMe()` returning the resolved `{ user, grants }`; `<ApiErrorPanel error>` — the `errorComponent` every protected route inherits, and the only surface outside form submission that renders `correlation_id`.

- [ ] **Step 1: Write the three failing guard tests**

`web/src/routes/_app.test.tsx`:

```tsx
import { QueryClientProvider } from '@tanstack/react-query'
import { RouterProvider, createMemoryHistory, createRouter } from '@tanstack/react-router'
import { render, screen } from '@testing-library/react'
import { HttpResponse, http } from 'msw'
import { describe, expect, it } from 'vitest'
import { queryClient } from '../api/query-client'
import { routeTree } from '../routeTree.gen'
import { server } from '../test/msw'

const me = () =>
  HttpResponse.json({
    user: { id: 'u1', email: 'a@b.c', role: 'admin', active: true, created_at: '', updated_at: '' },
    grants: [{ project_id: 'p1', level: 'run' }],
  })
const noSession = () =>
  HttpResponse.json({ title: 'Authentication required', status: 401 }, { status: 401, headers: { 'content-type': 'application/problem+json' } })

function renderAt(path: string) {
  queryClient.clear()
  // BOTH arguments are load-bearing and neither is optional.
  //
  // createMemoryHistory with an initial entry, because a jsdom router has no
  // URL to read and `router.navigate` after construction races the first
  // render — the guard's whole job happens during that first load.
  //
  // context: { queryClient }, because the routeTree is created with
  // `createRootRouteWithContext<{ queryClient: QueryClient }>()` (Task 1) and
  // the _app loader dereferences `context.queryClient` to call
  // ensureQueryData. Omitting it is a type error, and past the cast a runtime
  // one — and the failure lands in the guard's own test, so it reads as the
  // guard being broken rather than the harness.
  const router = createRouter({
    routeTree,
    context: { queryClient },
    history: createMemoryHistory({ initialEntries: [path] }),
  })
  return render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

describe('the protected layout', () => {
  it('sends a direct unauthenticated visit to a STUB route to /login', async () => {
    // The whole reason this layout exists: a stub makes no request to be
    // rejected, so without a loader it would render the full shell to anyone.
    server.use(http.get('/api/v1/auth/me', noSession))
    renderAt('/tasks')
    expect(await screen.findByRole('button', { name: /sign in/i })).toBeInTheDocument()
  })

  it('renders the shell on a reload with a valid cookie', async () => {
    server.use(http.get('/api/v1/auth/me', me))
    server.use(http.get('/api/v1/approvals', () => HttpResponse.json({ approvals: [] })))
    renderAt('/')
    expect(await screen.findByRole('navigation')).toBeInTheDocument()
  })

  it('sends an expired session back to /login', async () => {
    let calls = 0
    server.use(
      http.get('/api/v1/auth/me', () => {
        calls += 1
        return calls === 1 ? me() : noSession()
      }),
    )
    server.use(http.get('/api/v1/approvals', () => HttpResponse.json({ approvals: [] })))
    renderAt('/')
    await screen.findByRole('navigation')
    // INVALIDATE ONLY. `queryClient.clear()` first would remove the entry
    // outright, leaving invalidateQueries nothing to mark stale — the refetch
    // that this test exists to observe would then be an artefact of the
    // cache being empty rather than of the session having expired, which is a
    // different code path and the one that is NOT in production.
    await queryClient.invalidateQueries({ queryKey: ['auth', 'me'] })
    expect(await screen.findByRole('button', { name: /sign in/i })).toBeInTheDocument()
    expect(calls).toBeGreaterThanOrEqual(2)
  })
})
```

- [ ] **Step 2: Run it and watch it fail**

Run: `cd web && npx vitest run src/routes/_app.test.tsx`
Expected: FAIL — the `_app` route does not exist, so `/tasks` and `/` are unguarded.

- [ ] **Step 3: Implement the query and the layout route**

`web/src/auth/me-query.ts`:

```ts
import { queryOptions } from '@tanstack/react-query'
import { api, unwrap } from '../api/client'
import { AUTH_SCOPED_QUERY_OPTIONS } from '../api/query-client'

export const meQueryOptions = queryOptions({
  queryKey: ['auth', 'me'],
  queryFn: async () => unwrap(await api.GET('/api/v1/auth/me')),
  ...AUTH_SCOPED_QUERY_OPTIONS,
  // The session's own probe: a failure here is an ANSWER ("no session"), not a
  // transport problem to retry into.
  retry: false,
})
```

`web/src/routes/_app.tsx`:

```tsx
import { useQuery, useSuspenseQuery } from '@tanstack/react-query'
import { type ErrorComponentProps, Outlet, createFileRoute, redirect, useNavigate } from '@tanstack/react-router'
import { useEffect } from 'react'
import { meQueryOptions } from '../auth/me-query'
import { ApiError } from '../api/errors'
import { AppShell } from '../shell/app-shell'

/**
 * Every protected route nests under this layout, and its loader resolves the
 * session BEFORE anything renders. Without it a stub route makes no request,
 * so nothing would be rejected and the full shell would render to an
 * unauthenticated visitor.
 */
export const Route = createFileRoute('/_app')({
  loader: async ({ context }) => {
    try {
      await context.queryClient.ensureQueryData(meQueryOptions)
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) throw redirect({ to: '/login' })
      throw e
    }
  },
  component: AppLayout,
  errorComponent: AppErrorBoundary,
})

function AppLayout() {
  const { data } = useSuspenseQuery(meQueryOptions)

  /**
   * THE SECOND OBSERVER, and it is not redundant — it is the only thing that
   * can see a session expiring in the background.
   *
   * useSuspenseQuery does NOT throw once the query holds data. It hard-sets
   * `throwOnError` to its own default AFTER spreading the caller's options
   * (react-query 5.101.4, useSuspenseQuery.js), and that default is
   * `(_error, query) => query.state.data === undefined`. So passing
   * `throwOnError: true` through meQueryOptions is silently discarded, and a
   * refetch that 401s leaves the stale `data` in place with nothing surfacing
   * anywhere. The shell keeps rendering to an expired session indefinitely —
   * exactly what this layout exists to prevent, deferred past the first load.
   *
   * `enabled: false` makes this a pure subscription: it reports the query's
   * state and never fetches or schedules a refetch of its own, so
   * AUTH_SCOPED_QUERY_OPTIONS' interval and focus refetch do not double up.
   * Verified against the pinned version — a disabled observer on a query whose
   * refetch failed reports status 'error' with the error, and the fetch count
   * is unchanged.
   */
  const { error } = useQuery({ ...meQueryOptions, enabled: false, throwOnError: false })
  const navigate = useNavigate()
  const expired = error instanceof ApiError && error.status === 401
  useEffect(() => {
    if (expired) void navigate({ to: '/login' })
  }, [expired, navigate])
  if (expired) return null

  return (
    <AppShell me={data}>
      <Outlet />
    </AppShell>
  )
}

/**
 * Route-level errors: a child loader that failed, or a query that threw
 * because it had no data to fall back on. The FIRST load's 401 is caught by
 * the loader above and redirected there; a background expiry is caught by the
 * disabled observer in AppLayout, because useSuspenseQuery will not throw
 * over cached data. This handles the rest.
 *
 * The 401 branch is kept here anyway — a child route's own loader can 401
 * without /auth/me having been refetched, and that is a real expiry too.
 *
 * The redirect is a navigate-in-effect rather than `throw redirect` because
 * this is a render, not a loader; throwing a redirect from a component is not
 * how the router accepts one.
 */
function AppErrorBoundary({ error }: ErrorComponentProps) {
  const navigate = useNavigate()
  const expired = error instanceof ApiError && error.status === 401
  useEffect(() => {
    if (expired) void navigate({ to: '/login' })
  }, [expired, navigate])
  // Render nothing while the navigation lands; a flash of an error panel on
  // the way to the login form reads as a bug to the person it happens to.
  if (expired) return null
  // Everything else is somebody else's problem until Step 6, which replaces
  // this line with the panel that renders correlation_id. Left as a rethrow so
  // THIS step is independently green: the panel does not exist yet.
  throw error
}

export function useMe() {
  return useSuspenseQuery(meQueryOptions).data
}
```

Move `src/routes/index.tsx` to `src/routes/_app/index.tsx` and change its path to `createFileRoute('/_app/')`.

`web/src/router.ts` — thread the query client through router context and install the redirect:

```ts
import { createRouter } from '@tanstack/react-router'
import { onUnauthenticated } from './api/client'
import { queryClient } from './api/query-client'
import { routeTree } from './routeTree.gen'

export const router = createRouter({
  routeTree,
  context: { queryClient },
  defaultPreload: 'intent',
})

// A bare module cannot navigate; the router can. This is the other half of the
// 401 interceptor — the exemption list decides WHETHER, this decides WHERE.
onUnauthenticated(() => {
  void router.navigate({ to: '/login' })
})

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}
```

Add the context type to `__root.tsx` via `createRootRouteWithContext<{ queryClient: QueryClient }>()(...)`.

- [ ] **Step 4: Add the redirect-inward case**

An existing session at `/login` redirects inward. In `src/routes/login.tsx`, add to the route definition:

```ts
export const Route = createFileRoute('/login')({
  beforeLoad: async ({ context }) => {
    try {
      await context.queryClient.ensureQueryData(meQueryOptions)
      throw redirect({ to: '/' })
    } catch (e) {
      if (e instanceof ApiError) return // no session: stay on the form
      throw e
    }
  },
  component: LoginPage,
})
```

- [ ] **Step 5: Run the guard tests and confirm they pass**

Run: `cd web && npx vitest run src/routes/_app.test.tsx`
Expected: PASS, 3 tests. (Step 6 adds a fourth to this file.)

**Two placeholders this task owes itself, because it is executed before Task 8 exists.** Both are named in the Files list above; neither is optional, and a test that cannot resolve them fails in a way that looks like the guard is broken.

- `AppShell` — stub it as a `<nav>` plus `{children}`. The assertions above are about the guard, not the chrome, and Task 8 replaces the file wholesale.
- `web/src/routes/_app/tasks.tsx` — the first test navigates to `/tasks` *precisely because* it is a route that issues no request of its own, which is the case a redirect-on-401 design cannot catch. Task 8 creates the real one. Until then the route must exist or file-based routing resolves the path to nothing and the test asserts against a blank page. A default `<StubPanel>`-shaped component with no loader is enough; keep it component-only, since a loader here would make the route request something and destroy the property being tested.

- [ ] **Step 6: The route error boundary — the only place `correlation_id` reaches a user who did not submit a form**

The global constraint says `correlation_id` is rendered on **every** error surface, and after Task 6 exactly one surface renders it: form submission. Every route that reads data through Suspense has no error path of its own, so a 403 or a 500 on `/approvals` reaches TanStack's default error component — generic text, no correlation id, and for a grant failure no explanation of what the user is missing. The thread between a user's complaint and a server log line breaks precisely where the user is most likely to complain.

Step 3 already installed `AppErrorBoundary` as the `_app` route's `errorComponent` and had it rethrow anything that is not an expired session. This step creates `web/src/errors/api-error-panel.tsx` and replaces that rethrow:

```tsx
// in _app.tsx — add the import and swap the last line of AppErrorBoundary
import { ApiErrorPanel } from '../errors/api-error-panel'
// ...
  if (expired) return null
  return <ApiErrorPanel error={error} />
```

**It goes in `errors/`, not `ui/`.** The panel does `error instanceof ApiError`, which is a value import from `api/`, and Task 3's matrix bans `ui/ -> api/` — `ui` is the presentational layer and a component that knows the shape of a transport error is not presentational. `errors/` is its own group, carries no inbound restriction, and is where the next error surface belongs too. Getting this wrong is not a lint annoyance: it is the edge that makes every button in `ui/` untestable without a client.

```tsx
// web/src/errors/api-error-panel.tsx
import { ApiError } from '../api/errors'

export function ApiErrorPanel({ error }: { error: unknown }) {
  if (!(error instanceof ApiError)) throw error // not ours; let the root handle it

  return (
    <section role="alert" className="bg-surface text-text border-border">
      <h2>{error.title}</h2>
      {error.status === 403 ? (
        // A 403 is not a malfunction and must not read like one. The user is
        // authenticated and simply lacks a grant, so the panel says which
        // project and what to ask for rather than inviting a retry that
        // cannot succeed.
        <p>You do not have access to this project. Ask an admin for a grant.</p>
      ) : (
        <p>{error.detail}</p>
      )}
      {/* UNCONDITIONAL, and last. Whatever else the panel says, this is the
          string support asks for, and it is on the 500 path too — where
          `detail` is deliberately vague and the id is the only thing that can
          find the log line. */}
      <p>
        Reference: <code>{error.correlationId}</code>
      </p>
    </section>
  )
}
```

Tests, in `web/src/errors/api-error-panel.test.tsx` — one per status class, because a panel tested only on 403 silently drops the id on the path where it matters most:

```tsx
import { render, screen } from '@testing-library/react'
import { expect, it } from 'vitest'
import { ApiError } from '../api/errors'
import { ApiErrorPanel } from './api-error-panel'

// A 500's `detail` is deliberately vague, so the id is the only thread back to
// a log line — which is exactly why both statuses are asserted, not just 403.
const base = { type: '', title: 'Something failed', detail: 'detail', correlationId: 'cid', fields: [] }

it.each([403, 500])('renders the correlation id for a %s', (status) => {
  render(<ApiErrorPanel error={new ApiError({ ...base, status, correlationId: 'cid-42' })} />)
  expect(screen.getByText('cid-42')).toBeInTheDocument()
})

it('explains a 403 as a missing grant, not a failure', () => {
  render(<ApiErrorPanel error={new ApiError({ ...base, status: 403 })} />)
  expect(screen.getByRole('alert')).toHaveTextContent(/grant/i)
})

it('rethrows what it does not own', () => {
  expect(() => render(<ApiErrorPanel error={new TypeError('boom')} />)).toThrow('boom')
})
```

Then add the route-level assertion in `_app.test.tsx`, since a component nobody installed protects nothing:

```tsx
it('renders the correlation id when a protected route 500s', async () => {
  server.use(http.get('/api/v1/auth/me', me))
  server.use(http.get('/api/v1/approvals', () =>
    HttpResponse.json({ title: 'Internal error', status: 500, correlation_id: 'cid-99' },
      { status: 500, headers: { 'content-type': 'application/problem+json' } })))
  renderAt('/approvals')
  expect(await screen.findByText('cid-99')).toBeInTheDocument()
})
```

- [ ] **Step 7: Verify and commit**

```bash
cd web && npm exec -- tsr generate && npx tsc --noEmit && npx vitest run && npx biome ci . && node tools/arch/main.ts
git add web && git commit -m "feat(web): resolve the session before the shell renders"
```

---

### Task 8: The app shell, the stub routes, and the honest connection indicator

Shell A and the page map are locked in `docs/brainstorm/2026-08-01-decisions.md` §7 with mockups in `.superpowers/brainstorm/12432-1785604290/content/`. This task does not revisit them. What it adds is the one thing the shell owes the realtime layer: a connection indicator that admits when the stream is lost.

**Files:**
- ~~Create~~ **CONSUME** `web/src/realtime/connection-state.ts` — `export type ConnectionState = 'live' | 'reconnecting' | 'stopped'`, and nothing else. **Task 10 already created it** and the file is committed: creating it here scheduled it two waves after `sse-connection.ts`, which imports it, so Task 10 could not compile. The placement argument below is unchanged and is why `realtime/` — not `shell/` — is where it went.
- Create: `web/src/shell/app-shell.tsx`, `web/src/shell/nav.tsx`, `web/src/shell/connection-indicator.tsx`, `web/src/shell/theme-toggle.tsx`
- Create: `web/src/shell/app-shell.test.tsx`
- Create: `web/src/routes/_app/runs.tsx`, `workflows.tsx`, `repos.tsx`, `admin.tsx`, `approvals.tsx`
- Replace: `web/src/routes/_app/tasks.tsx` — Task 7 left a placeholder so its guard test had a request-free route to visit; this is the real one
- Create: `web/src/ui/stub-panel.tsx`

**Interfaces:**
- Consumes: `useTheme` (Task 2), the `_app` layout (Task 7).
- Produces: `<AppShell me={MeResponse} connection={ConnectionState}>`; `<StubPanel plan="Plan 12" what="…" />`.

**The connection state reaches the shell as a PROP, not as a hook of its own.** An earlier draft promised a `useConnectionState()` here as well; two mechanisms for one value means one of them is dead, and the dead one is the one a reader will reach for. `AppShell` takes `connection` as a prop because `AppLayout` already holds it — it is the single caller and the value is one hop away. Everything *below* the layout that needs the stream's state reads `useStreamStatus()` from `src/realtime/stream-status.tsx` (Task 11), which is the same context and the only hook. There is no `useConnectionState`.

**`ConnectionState` is declared in `src/realtime/connection-state.ts`, not beside the indicator that renders it.** The transport PRODUCES this state and the shell DISPLAYS it, so declaring it in `shell/` points the dependency backwards and makes `realtime` — a layer with no DOM in it — import from the chrome. The cross-group rule added in Task 3 does not currently ban that edge, which is exactly why it has to be got right by construction here: a type in the wrong directory is the cheapest possible thing to fix now and an entangled layer later.

- [ ] **Step 1: Write the failing shell test**

`web/src/shell/app-shell.test.tsx`:

```tsx
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { AppShell } from './app-shell'

const me = {
  user: { id: 'u1', email: 'a@b.c', role: 'admin', active: true, created_at: '', updated_at: '' },
  grants: [],
}

describe('the app shell', () => {
  it('offers every route in the V1 page map', async () => {
    render(<AppShell me={me}>body</AppShell>)
    const nav = screen.getByRole('navigation')
    for (const label of ['Home', 'Tasks', 'Runs', 'Approvals', 'Workflows', 'Repos', 'Admin']) {
      expect(nav).toHaveTextContent(label)
    }
  })

  it('switches theme by writing data-theme, never by editing a component', async () => {
    render(<AppShell me={me}>body</AppShell>)
    await userEvent.click(screen.getByRole('button', { name: /light/i }))
    expect(document.documentElement.dataset.theme).toBe('light')
  })

  it('says so out loud when the stream is lost', () => {
    // The UI never renders stale data as live. A frozen badge that looks
    // authoritative is worse than an honest one that admits it lost the stream.
    render(
      <AppShell me={me} connection="reconnecting">
        body
      </AppShell>,
    )
    expect(screen.getByRole('status')).toHaveTextContent(/reconnecting/i)
  })

  it('is silent when the stream is live', () => {
    render(
      <AppShell me={me} connection="live">
        body
      </AppShell>,
    )
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
  })
})
```

- [ ] **Step 2: Run it and watch it fail**

Run: `cd web && npx vitest run src/shell/app-shell.test.tsx`
Expected: FAIL — module not found.

- [ ] **Step 3: Implement the shell**

`web/src/shell/connection-indicator.tsx`:

```tsx
export type ConnectionState = 'live' | 'reconnecting' | 'stopped'

export function ConnectionIndicator(props: { state: ConnectionState }) {
  if (props.state === 'live') return null
  return (
    <p role="status" className="bg-warning/10 text-warning px-3 py-1 text-center font-mono text-xs">
      {props.state === 'reconnecting'
        ? 'reconnecting — this view may be out of date'
        : 'live updates stopped — reload to resume'}
    </p>
  )
}
```

`web/src/shell/theme-toggle.tsx`:

```tsx
import { useTheme } from '../theme/use-theme'

export function ThemeToggle() {
  const { theme, setTheme } = useTheme()
  const next = theme === 'dark' ? 'light' : 'dark'
  return (
    <button type="button" onClick={() => setTheme(next)} className="text-text-muted px-3 text-sm">
      {next === 'light' ? 'Light' : 'Dark'}
    </button>
  )
}
```

`web/src/shell/nav.tsx` — one entry per row of §1's route table:

```tsx
import { Link } from '@tanstack/react-router'

const ITEMS = [
  { to: '/', label: 'Home' },
  { to: '/tasks', label: 'Tasks' },
  { to: '/runs', label: 'Runs' },
  { to: '/approvals', label: 'Approvals' },
  { to: '/workflows', label: 'Workflows' },
  { to: '/repos', label: 'Repos' },
  { to: '/admin', label: 'Admin' },
] as const

export function Nav(props: { approvalCount: number }) {
  return (
    <nav aria-label="Sections" className="border-border bg-surface flex overflow-x-auto border-t">
      {ITEMS.map((item) => (
        <Link
          key={item.to}
          to={item.to}
          className="text-text-muted flex flex-1 items-center justify-center gap-1 px-3 py-2 text-sm"
          activeProps={{ className: 'text-accent' }}
        >
          {item.label}
          {item.to === '/approvals' && props.approvalCount > 0 && (
            <span data-testid="approvals-badge" className="bg-accent text-accent-contrast rounded-sm px-1 font-mono text-xs">
              {props.approvalCount}
            </span>
          )}
        </Link>
      ))}
    </nav>
  )
}
```

Every `to` above is checked by the compiler because of Task 1's `Register` augmentation. **If a typo here compiles, the augmentation is gone** — that is the mutant's whole subject.

`web/src/shell/app-shell.tsx`:

```tsx
import type { ReactNode } from 'react'
import type { components } from '../api/schema'
import type { ConnectionState } from '../realtime/connection-state'
import { ConnectionIndicator } from './connection-indicator'
import { Nav } from './nav'
import { ThemeToggle } from './theme-toggle'

export function AppShell(props: {
  me: components['schemas']['ApiMeResponse']
  children: ReactNode
  connection?: ConnectionState
  approvalCount?: number
}) {
  return (
    <div className="bg-ground text-text flex min-h-dvh flex-col">
      <header className="border-border bg-surface flex items-center justify-between border-b px-4 py-2">
        <span className="font-mono text-sm">caesar</span>
        <div className="flex items-center gap-2">
          <span className="text-text-muted font-mono text-xs">{props.me.user.email}</span>
          <ThemeToggle />
        </div>
      </header>
      <ConnectionIndicator state={props.connection ?? 'live'} />
      <main className="flex-1 p-4">{props.children}</main>
      <Nav approvalCount={props.approvalCount ?? 0} />
    </div>
  )
}
```

- [ ] **Step 4: Run the shell tests and confirm they pass**

Run: `cd web && npx vitest run src/shell/app-shell.test.tsx`
Expected: PASS, 4 tests.

- [ ] **Step 5: The typed stubs**

`web/src/ui/stub-panel.tsx`:

```tsx
export function StubPanel(props: { plan: string; what: string }) {
  // The backend's rule, kept on this side of the wire: stubs never fake
  // success. A stub that rendered plausible empty content would read as a
  // working screen with no data.
  return (
    <section className="border-border text-text-muted rounded-md border border-dashed p-6 text-sm">
      <p className="text-text font-medium">{props.what}</p>
      <p className="font-mono text-xs">not built yet — {props.plan}</p>
    </section>
  )
}
```

`web/src/routes/_app/tasks.tsx` (and the identical shape for `runs`, `workflows`, `repos`, `admin`):

```tsx
import { createFileRoute } from '@tanstack/react-router'
import { StubPanel } from '../../ui/stub-panel'

export const Route = createFileRoute('/_app/tasks')({ component: Tasks })

function Tasks() {
  return <StubPanel plan="Plan 12" what="Tasks" />
}
```

Repeat verbatim for each, changing the route path and the `what` string. Do not factor these into a shared generator: five three-line files are cheaper to read than one abstraction, and `web:arch` would want the generator hoisted anyway.

`web/src/routes/_app/approvals.tsx` is a `StubPanel` for now; Task 11 replaces its body.

- [ ] **Step 6: Verify and commit**

```bash
cd web && npm exec -- tsr generate && npx tsc --noEmit && npx vitest run && npx biome ci . && node tools/arch/main.ts
git add web && git commit -m "feat(web): app shell, the V1 page map, and an honest connection indicator"
```

---

### Task 9: `sse-parser.ts` — pure incremental framing

Level one of three. It sees bytes and produces typed frames; it has no I/O, no clock and no opinion about reconnection, so a bug here cannot hide inside the transport and vice versa.

**Files:**
- Create: `web/src/realtime/sse-parser.ts`, `web/src/realtime/sse-parser.test.ts`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type StreamFrame = { id: number; run_id: string; kind: string; payload: Record<string, unknown>; occurred_at: string }`
  - `type ParsedFrame = { kind: 'event'; id: number; event: string; frame: StreamFrame } | { kind: 'heartbeat' } | { kind: 'forbidden' }`
  - `function createSseParser(): { push(chunk: string): ParsedFrame[] }`

- [ ] **Step 1: Write the failing parser tests**

`web/src/realtime/sse-parser.test.ts`:

```ts
import { describe, expect, it } from 'vitest'
import { createSseParser } from './sse-parser'

const eventFrame = (id: number, kind = 'gate.waiting') =>
  `id: ${id}\nevent: ${kind}\ndata: ${JSON.stringify({ id, run_id: 'r1', kind, payload: {}, occurred_at: '2026-08-15T00:00:00Z' })}\n\n`

describe('createSseParser', () => {
  it('parses one complete frame', () => {
    const p = createSseParser()
    const out = p.push(eventFrame(7))
    expect(out).toHaveLength(1)
    expect(out[0]).toMatchObject({ kind: 'event', id: 7, event: 'gate.waiting' })
  })

  it('reassembles a frame split across chunks at an arbitrary byte', () => {
    // The transport hands over whatever the network gave it. A parser that
    // assumed chunk boundaries were frame boundaries would work on a fast
    // localhost and drop frames in production.
    const whole = eventFrame(1)
    for (let cut = 1; cut < whole.length; cut++) {
      const p = createSseParser()
      const out = [...p.push(whole.slice(0, cut)), ...p.push(whole.slice(cut))]
      expect(out.filter((f) => f.kind === 'event')).toHaveLength(1)
    }
  })

  it('consumes ": hb" comment heartbeats as keepalives rather than skipping them as junk', () => {
    // The server pads an idle stream every 20s with an SSE COMMENT. The
    // watchdog resets on it, so the parser must SURFACE it, not silently drop
    // it — a parser that dropped it would leave the watchdog firing on a
    // perfectly healthy idle stream.
    const p = createSseParser()
    expect(p.push(': hb\n\n')).toEqual([{ kind: 'heartbeat' }])
  })

  it('reports the forbidden frame as its own variant, with no id', () => {
    // The terminal frame carries NO id: — it is not an event, and a client
    // that adopted it as a resume point would store a position naming nothing.
    const p = createSseParser()
    expect(p.push('event: forbidden\ndata: {"error":"forbidden"}\n\n')).toEqual([{ kind: 'forbidden' }])
  })

  it('handles heartbeats interleaved between frames', () => {
    const p = createSseParser()
    const out = p.push(`${eventFrame(1)}: hb\n\n${eventFrame(2)}`)
    expect(out.map((f) => f.kind)).toEqual(['event', 'heartbeat', 'event'])
  })

  it('emits a replayed backlog as-is and never dedupes on its own', () => {
    // The server deliberately re-sends a backlog on resume — "one duplicated
    // backlog rather than a gap". Dedup is not the parser's job; the bridge's
    // invalidation is idempotent under duplicates and that is the design.
    const p = createSseParser()
    const out = p.push(eventFrame(5) + eventFrame(5) + eventFrame(6))
    expect(out.map((f) => (f.kind === 'event' ? f.id : null))).toEqual([5, 5, 6])
  })
})
```

- [ ] **Step 2: Run it and watch it fail**

Run: `cd web && npx vitest run src/realtime/sse-parser.test.ts`
Expected: FAIL — module not found.

- [ ] **Step 3: Implement the parser**

`web/src/realtime/sse-parser.ts`:

```ts
export type StreamFrame = {
  id: number
  run_id: string
  kind: string
  payload: Record<string, unknown>
  occurred_at: string
}

export type ParsedFrame =
  | { kind: 'event'; id: number; event: string; frame: StreamFrame }
  | { kind: 'heartbeat' }
  | { kind: 'forbidden' }

/**
 * Pure incremental SSE framing. No I/O, no clock, no reconnection.
 *
 * THE WIRE IS ONE SHAPE AND THIS PARSES ONE SHAPE. The server emits its
 * opening snapshot as ordinary event frames — there is no snapshot
 * discriminator and no client-visible watermark, so a client cannot be built
 * on one. Do not add a "snapshot" variant here; there is nothing on the wire
 * to populate it from.
 */
export function createSseParser() {
  let buffer = ''

  return {
    push(chunk: string): ParsedFrame[] {
      buffer += chunk
      const out: ParsedFrame[] = []

      let sep = buffer.indexOf('\n\n')
      while (sep !== -1) {
        const block = buffer.slice(0, sep)
        buffer = buffer.slice(sep + 2)
        const parsed = parseBlock(block)
        if (parsed !== null) out.push(parsed)
        sep = buffer.indexOf('\n\n')
      }
      return out
    },
  }
}

function parseBlock(block: string): ParsedFrame | null {
  if (block === '') return null

  let id: number | null = null
  let event = ''
  let data = ''
  let sawComment = false

  for (const line of block.split('\n')) {
    if (line.startsWith(':')) {
      // The keepalive. Surfaced rather than skipped: the watchdog resets on it.
      sawComment = true
      continue
    }
    const colon = line.indexOf(':')
    if (colon === -1) continue
    const field = line.slice(0, colon)
    const value = line.slice(colon + 1).replace(/^ /, '')
    if (field === 'id') id = Number.parseInt(value, 10)
    else if (field === 'event') event = value
    else if (field === 'data') data = data === '' ? value : `${data}\n${value}`
  }

  if (event === 'forbidden') {
    // Its own variant, and it always ends the connection. It carries no id by
    // design — see sse-connection.ts for what the client does about it, which
    // is emphatically not "conclude the user lost access".
    return { kind: 'forbidden' }
  }
  if (id === null || Number.isNaN(id)) {
    return sawComment ? { kind: 'heartbeat' } : null
  }

  let frame: StreamFrame
  try {
    frame = JSON.parse(data) as StreamFrame
  } catch {
    return null
  }
  return { kind: 'event', id, event, frame }
}
```

- [ ] **Step 4: Run the parser tests and confirm they pass**

Run: `cd web && npx vitest run src/realtime/sse-parser.test.ts`
Expected: PASS, 6 tests (the split-chunk case runs one assertion per byte offset).

- [ ] **Step 5: Commit**

```bash
cd web && npx tsc --noEmit && npx biome ci .
git add web && git commit -m "feat(web): pure incremental SSE frame parser"
```

---

### Task 10: `sse-connection.ts` — the transport that owns reconnection and never correctness

This layer carries five behaviours a parser test cannot see and the one E2E happy path never exercises: backoff, resume, the watchdog, terminal-vs-transient, and the cache effect of each `forbidden` outcome. A connection that reconnected after `forbidden`, forgot its `Last-Event-ID`, or failed to reset the watchdog on a comment heartbeat would pass every other check in this plan.

**Files:**
- Create: `web/src/realtime/sse-connection.ts`, `web/src/realtime/sse-connection.test.ts`
- Create: `web/src/realtime/connection-state.ts` — **moved here from Task 8.** The plan
  scheduled it two waves after the file that imports it, so Task 10 could not compile.
  Task 8 now CONSUMES it. Its own argument (Task 8's prose) is why it belongs in
  `realtime/`: the transport produces the state and the shell only displays it.

**Interfaces:**
- Consumes: `createSseParser`, `ParsedFrame` (Task 9); `api`, `unwrap`, `ApiError` (Task 5).
- Produces:
  - `type ConnectionEvent = { type: 'frame'; frame: ParsedFrame & { kind: 'event' } } | { type: 'state'; state: ConnectionState } | { type: 'scope-lost' } | { type: 'session-lost' }`
  - `function openAttentionStream(opts: { onEvent(e: ConnectionEvent): void; fetchImpl?: typeof fetch; now?: () => number; setTimeoutImpl?: typeof setTimeout; probeMe(): Promise<MeProbe> }): { close(): void }`
  - `type MeProbe = { grants: readonly unknown[] | null }` — **nullable deliberately**,
    because `ApiMeResponse.grants` is generated as `ApiGrantResponse[] | null`. Task 8
    wires this: `unwrap(api.GET('/api/v1/auth/me'))` satisfies it directly. Do not narrow
    it to `unknown[]` to make a signature line up, and do not paper over it with `?? []`
    at the call site — the transport owns that decision.

> **CORRECTED AGAINST WHAT EXECUTION MEASURED (wave 4). `web/src/realtime/sse-connection.ts`
> and its test are authoritative where this section still disagrees.** The code block below
> was wrong on nine counts, and the ones that change behaviour rather than style are:
>
> - **`globalThis.fetch` is banned by the repo's own GritQL plugin** (Task 5's
>   `no-qualified-fetch.grit`), and the seam must be a wrapper — `opts.fetchImpl ?? fetch`
>   resolves the global once at `openAttentionStream()` time, so a fetch swapped later is
>   never seen. Measured: with `?? fetch`, tsc and all tests passed until a mid-connection
>   swap test existed.
> - **The watchdog `setInterval`/`clearInterval` must be injected too.** The plan injects
>   `now` and `setTimeout` and then reaches for a real interval, which no test can drive.
> - **`me.grants.length` dereferences a nullable contract** — see `MeProbe` above.
> - **Three of the plan's own tests were vacuous or backwards**: the heartbeat test asserted
>   nothing that could fail, the "forbidden does not reconnect" test asserted the opposite
>   of the code, and the `stream()` helper was blind to aborts.
> - **Two constants were pinned only loosely by the plan's suite.** `STABLE_MS` passed at
>   any value in `(0, 30_000]` — including `1`, which reintroduces verbatim the hot loop
>   its own docstring says it exists to prevent, because the guard test left its injected
>   clock frozen at 0 and `0 >= STABLE_MS` is false for every positive threshold. The test
>   now steps to one millisecond short of the threshold, bracketing it to `[25_000, 30_000]`.
> - The terminal-outcome test must assert a timer **delta**, not `getTimerCount() === 0`:
>   the count is global, so an absolute assertion couples it to every earlier test in the
>   file and manufactures a phantom second failure under unrelated mutants — which
>   misdirects exactly the mutation sweep this project verifies with.

- [ ] **Step 1: Write the failing transport tests**

`web/src/realtime/sse-connection.test.ts`:

```ts
import { describe, expect, it, vi } from 'vitest'
import { openAttentionStream } from './sse-connection'
import { ApiError } from '../api/errors'

/** A chunked ReadableStream response, driven by the test. */
function stream() {
  let push!: (s: string) => void
  let close!: () => void
  const body = new ReadableStream<Uint8Array>({
    start(c) {
      const enc = new TextEncoder()
      push = (s) => c.enqueue(enc.encode(s))
      close = () => c.close()
    },
  })
  return { response: new Response(body, { status: 200, headers: { 'content-type': 'text/event-stream' } }), push, close }
}

const evt = (id: number, kind = 'gate.waiting') =>
  `id: ${id}\nevent: ${kind}\ndata: ${JSON.stringify({ id, run_id: 'r1', kind, payload: {}, occurred_at: '' })}\n\n`

describe('sse-connection', () => {
  it('sends Last-Event-ID on resume, advanced only by frames that carry an id', async () => {
    const seen: (string | null)[] = []
    const s1 = stream()
    const s2 = stream()
    let call = 0
    const fetchImpl = vi.fn(async (_url: string, init?: RequestInit) => {
      seen.push(new Headers(init?.headers).get('Last-Event-ID'))
      call += 1
      return call === 1 ? s1.response : s2.response
    })

    const conn = openAttentionStream({ onEvent: () => {}, fetchImpl: fetchImpl as never, probeMe: async () => ({ grants: [{}] }) })
    s1.push(evt(41))
    s1.push(': hb\n\n')          // a heartbeat must NOT advance the cursor
    await vi.waitFor(() => expect(seen).toHaveLength(1))
    s1.close()
    await vi.waitFor(() => expect(seen).toHaveLength(2))
    expect(seen[0]).toBeNull()
    expect(seen[1]).toBe('41')
    conn.close()
  })

  it('resets the watchdog on a comment heartbeat and fires without one', async () => {
    let clock = 0
    const s = stream()
    const states: string[] = []
    const conn = openAttentionStream({
      onEvent: (e) => e.type === 'state' && states.push(e.state),
      fetchImpl: (async () => s.response) as never,
      now: () => clock,
      probeMe: async () => ({ grants: [{}] }),
    })
    await vi.waitFor(() => expect(states).toContain('live'))

    clock = 30_000
    s.push(': hb\n\n')
    await vi.waitFor(() => expect(states.at(-1)).toBe('live'))

    clock = 90_000                       // 45s past the last heartbeat
    await vi.waitFor(() => expect(states.at(-1)).toBe('reconnecting'), { timeout: 2000 })
    conn.close()
  })

  it('an ordinary close reconnects; a forbidden frame does not', async () => {
    const opened: number[] = []
    const s1 = stream()
    const s2 = stream()
    let call = 0
    const conn = openAttentionStream({
      onEvent: () => {},
      fetchImpl: (async () => {
        opened.push(++call)
        return call === 1 ? s1.response : s2.response
      }) as never,
      probeMe: async () => ({ grants: [{}] }),
    })
    s1.close()
    await vi.waitFor(() => expect(opened).toHaveLength(2))
    s2.push('event: forbidden\ndata: {"error":"forbidden"}\n\n')
    await new Promise((r) => setTimeout(r, 50))
    expect(opened).toHaveLength(2)        // no third open
    conn.close()
  })

  it('forbidden + 200-with-grants is a RESOLVER OUTAGE: evict nothing, reconnect', async () => {
    // The backend says so outright: after three consecutive resolver failures
    // "the client gets the same frame a revocation would send". Treating the
    // frame as proof of lost authorization would leave the tab permanently
    // realtime-dead, falsely claiming a permission problem, while REST worked
    // fine the whole time.
    const events: string[] = []
    const s1 = stream()
    const s2 = stream()
    let call = 0
    const conn = openAttentionStream({
      onEvent: (e) => events.push(e.type === 'state' ? `state:${e.state}` : e.type),
      fetchImpl: (async () => (++call === 1 ? s1.response : s2.response)) as never,
      probeMe: async () => ({ grants: [{ project_id: 'p1' }] }),
    })
    s1.push('event: forbidden\ndata: {"error":"forbidden"}\n\n')
    await vi.waitFor(() => expect(call).toBe(2))
    expect(events).not.toContain('scope-lost')
    expect(events).not.toContain('session-lost')
    conn.close()
  })

  it('forbidden + 200-with-NO-grants is an empty inbox, not an authorization error', async () => {
    // An active principal who legitimately sees nothing. Rendering the
    // authorization panel here would be the UI lying on the server's behalf.
    const events: string[] = []
    const s = stream()
    const conn = openAttentionStream({
      onEvent: (e) => events.push(e.type === 'state' ? `state:${e.state}` : e.type),
      fetchImpl: (async () => s.response) as never,
      probeMe: async () => ({ grants: [] }),
    })
    s.push('event: forbidden\ndata: {"error":"forbidden"}\n\n')
    await vi.waitFor(() => expect(events).toContain('scope-lost'))
    expect(events).toContain('state:stopped')
    conn.close()
  })

  it('forbidden + 401 is a dead session', async () => {
    const events: string[] = []
    const s = stream()
    const conn = openAttentionStream({
      onEvent: (e) => events.push(e.type),
      fetchImpl: (async () => s.response) as never,
      probeMe: async () => {
        throw new ApiError({ type: '', title: '', status: 401, detail: '', correlationId: '', fields: [] })
      },
    })
    s.push('event: forbidden\ndata: {"error":"forbidden"}\n\n')
    await vi.waitFor(() => expect(events).toContain('session-lost'))
    conn.close()
  })

  it('backs off exponentially between reconnects', async () => {
    const delays: number[] = []
    let call = 0
    const conn = openAttentionStream({
      onEvent: () => {},
      fetchImpl: (async () => {
        call += 1
        const s = stream()
        queueMicrotask(() => s.close())
        return s.response
      }) as never,
      setTimeoutImpl: ((fn: () => void, ms: number) => {
        delays.push(ms)
        return setTimeout(fn, 0)
      }) as never,
      probeMe: async () => ({ grants: [{}] }),
    })
    await vi.waitFor(() => expect(delays.length).toBeGreaterThanOrEqual(3))
    expect(delays[1]).toBeGreaterThan(delays[0] as number)
    expect(delays[2]).toBeGreaterThan(delays[1] as number)
    conn.close()
  })

  it('does NOT reset backoff for a connection that only opened', async () => {
    // The test above is the one that catches this, and it is worth saying why
    // it does: every response here is a 200 that closes at once. A transport
    // that reset `attempt` on res.ok would emit 500, 500, 500 and still pass a
    // weaker assertion, so the delays must be checked as STRICTLY increasing.
    // This is the proxy-in-front-of-a-restarting-backend shape, and reading it
    // as success is how a client DDoSes a server that is already unwell.
    const delays: number[] = []
    const conn = openAttentionStream({
      onEvent: () => {},
      fetchImpl: (async () => {
        const s = stream()
        queueMicrotask(() => s.close())
        return s.response
      }) as never,
      setTimeoutImpl: ((fn: () => void, ms: number) => {
        delays.push(ms)
        return setTimeout(fn, 0)
      }) as never,
      probeMe: async () => ({ grants: [{}] }),
    })
    await vi.waitFor(() => expect(delays.length).toBeGreaterThanOrEqual(4))
    expect(new Set(delays.slice(0, 4)).size).toBe(4)
    conn.close()
  })

  it('resets backoff after a connection that lasted', async () => {
    // The other half: without it, `attempt` would be write-only and the delay
    // would climb to 30s and stay there for the rest of the session, however
    // healthy the server became. The injected clock is what makes a 25s
    // threshold testable in milliseconds.
    const delays: number[] = []
    let t = 0
    const conn = openAttentionStream({
      onEvent: () => {},
      now: () => t,
      fetchImpl: (async () => {
        const s = stream()
        // Each connection "lasts" past STABLE_MS on the injected clock.
        queueMicrotask(() => {
          t += 30_000
          s.close()
        })
        return s.response
      }) as never,
      setTimeoutImpl: ((fn: () => void, ms: number) => {
        delays.push(ms)
        return setTimeout(fn, 0)
      }) as never,
      probeMe: async () => ({ grants: [{}] }),
    })
    await vi.waitFor(() => expect(delays.length).toBeGreaterThanOrEqual(3))
    expect(delays.slice(0, 3)).toEqual([500, 500, 500])
    conn.close()
  })

  it('stops the watchdog on a terminal outcome', async () => {
    // Without this the suite proves nothing about termination: every other
    // test calls conn.close() immediately after seeing the terminal event,
    // which clears the interval by hand and hides a timer that would
    // otherwise fire `reconnecting` 45s after the stream was declared dead.
    // Deliberately does NOT call close().
    const events: string[] = []
    let t = 0
    openAttentionStream({
      onEvent: (e) => events.push(e.type),
      now: () => t,
      fetchImpl: (async () => new Response('{"status":401}', { status: 401 })) as never,
      probeMe: async () => ({ grants: [] }),
    })
    await vi.waitFor(() => expect(events).toContain('session-lost'))
    const after = events.length
    t += 120_000
    await new Promise((r) => setTimeout(r, 1_100)) // let the 1s watchdog tick
    expect(events).toHaveLength(after)
  })

  it('treats an opening 401 as a dead session, not a retryable fault', async () => {
    // realtime/api authenticates BEFORE the handler so this is a real
    // problem+json 401 rather than a stream that opens and says nothing. A
    // transport that folds it into `throw new Error(...)` retries a dead
    // session until the tab closes.
    const events: string[] = []
    const delays: number[] = []
    const conn = openAttentionStream({
      onEvent: (e) => events.push(e.type),
      fetchImpl: (async () => new Response('{"status":401}', { status: 401 })) as never,
      setTimeoutImpl: ((fn: () => void, ms: number) => {
        delays.push(ms)
        return setTimeout(fn, 0)
      }) as never,
      probeMe: async () => ({ grants: [] }),
    })
    await vi.waitFor(() => expect(events).toContain('session-lost'))
    expect(delays).toEqual([])
    conn.close()
  })
})
```

- [ ] **Step 2: Run it and watch it fail**

Run: `cd web && npx vitest run src/realtime/sse-connection.test.ts`
Expected: FAIL — module not found.

- [ ] **Step 3: Implement the connection**

`web/src/realtime/sse-connection.ts`:

```ts
import { ApiError } from '../api/errors'
import type { ConnectionState } from './connection-state'
import { type ParsedFrame, createSseParser } from './sse-parser'

export type EventFrame = Extract<ParsedFrame, { kind: 'event' }>

export type ConnectionEvent =
  | { type: 'frame'; frame: EventFrame }
  | { type: 'state'; state: ConnectionState }
  | { type: 'scope-lost' }
  | { type: 'session-lost' }

const WATCHDOG_MS = 45_000     // the server heartbeats an idle stream at 20s
const BACKOFF_BASE_MS = 500
const BACKOFF_MAX_MS = 30_000
// How long a connection must survive before it counts as having worked. Above
// the server's 20s heartbeat, so a stream that stayed open across at least one
// idle beat qualifies; see reconnect() for why elapsed time is the signal.
const STABLE_MS = 25_000

/**
 * fetch + ReadableStream, NOT EventSource.
 *
 * EventSource reconnects on a fixed interval the page cannot control, surfaces
 * errors opaquely, and hides the HTTP status — so it can deliver neither
 * exponential backoff nor a heartbeat watchdog that surfaces staleness
 * honestly, and it cannot distinguish a 401 from a dropped tunnel. The cost is
 * re-implementing Last-Event-ID resume by hand, which this design wants
 * explicit anyway.
 *
 * This layer owns RECONNECTION and never CORRECTNESS. It decides when to
 * reopen and what to say about staleness; what a frame means is the bridge's
 * question, and what the data is is REST's.
 */
export function openAttentionStream(opts: {
  onEvent(e: ConnectionEvent): void
  probeMe(): Promise<{ grants: unknown[] }>
  fetchImpl?: typeof fetch
  now?: () => number
  setTimeoutImpl?: typeof setTimeout
}): { close(): void } {
  const doFetch = opts.fetchImpl ?? globalThis.fetch
  const now = opts.now ?? (() => Date.now())
  const later = opts.setTimeoutImpl ?? setTimeout

  let closed = false
  let attempt = 0
  let lastEventId: string | null = null
  let lastActivity = now()
  // When the current connection went live, or null if none is. reconnect()
  // reads it to decide whether the attempt counter has earned a reset.
  let openedAt: number | null = null
  let controller: AbortController | null = null

  const emit = (e: ConnectionEvent) => {
    if (!closed) opts.onEvent(e)
  }

  const watchdog = setInterval(() => {
    if (closed) return
    // The UI never renders stale data as live. A frozen badge that looks
    // authoritative is worse than an honest one that admits it lost the stream.
    if (now() - lastActivity > WATCHDOG_MS) {
      emit({ type: 'state', state: 'reconnecting' })
      controller?.abort()
    }
  }, 1_000)

  /**
   * A TERMINAL outcome, and the reason it is a named function rather than a
   * bare `return`.
   *
   * session-lost and scope-lost end the stream for good: there is nothing left
   * to reconnect to and no caller action that would change the answer. But the
   * watchdog is a setInterval, and an early `return` out of connect() leaves it
   * running — so 45 seconds after the connection is declared stopped it starts
   * emitting `reconnecting`, contradicting the terminal state it just reported
   * and keeping a timer alive for the life of the page. Every existing test
   * happens to call conn.close() right after observing the terminal event,
   * which hides this completely.
   */
  function stop(): void {
    closed = true
    clearInterval(watchdog)
    controller?.abort()
  }

  /**
   * THE PROBE. `forbidden` does not say what happened, and three different
   * conditions send those identical bytes: a real revocation; an empty scope
   * at the 30s re-check (which is also what a DEACTIVATED principal and an
   * active member with ZERO grants both resolve to); and a grants-store
   * outage after three consecutive resolver failures.
   *
   * So the client probes rather than concludes. One GET /auth/me is the only
   * thing on the wire that can decide this, and it costs one request on a rare
   * path. Roadmap row 10a makes it unnecessary.
   */
  async function onForbidden(): Promise<void> {
    try {
      const me = await opts.probeMe()
      if (me.grants.length > 0) {
        // Not about authorization at all — a resolver outage. Evict nothing.
        // Through reconnect(), NOT connect(): a grants store that is down
        // stays down for a while, and reopening with no delay would hammer it
        // through the same three-failure cycle that produced this frame. The
        // outage path is exactly where backoff earns its keep.
        reconnect()
        return
      }
      // An active principal who legitimately sees nothing.
      emit({ type: 'scope-lost' })
      emit({ type: 'state', state: 'stopped' })
      stop()
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) {
        emit({ type: 'session-lost' })
        stop()
        return
      }
      // The probe itself failed for some other reason. Reconnecting is the
      // safe direction: REST still enforces authorization on every request —
      // and under backoff, since a failing probe means the server is unwell.
      reconnect()
    }
  }

  function connect(): void {
    if (closed) return
    controller = new AbortController()
    const headers: Record<string, string> = { Accept: 'text/event-stream' }
    if (lastEventId !== null) headers['Last-Event-ID'] = lastEventId

    void (async () => {
      try {
        const res = await doFetch('/api/v1/stream', { headers, signal: controller.signal })

        // CLASSIFY BEFORE RETRYING. realtime/api authenticates ahead of the
        // handler precisely so an unauthenticated client gets a real 401
        // problem+json rather than a stream that opens and says nothing
        // (realtime/api/api.go). Collapsing that into a generic Error throws
        // away the one distinction the backend went out of its way to make,
        // and reconnecting forever against a dead session is the result.
        if (res.status === 401) {
          emit({ type: 'session-lost' })
          stop()
          return
        }
        if (!res.ok || res.body === null) throw new Error(`stream: ${res.status}`)

        lastActivity = now()
        openedAt = now()
        emit({ type: 'state', state: 'live' })

        const parser = createSseParser()
        const reader = res.body.pipeThrough(new TextDecoderStream()).getReader()
        for (;;) {
          const { done, value } = await reader.read()
          if (done) break
          lastActivity = now()
          for (const f of parser.push(value)) {
            if (f.kind === 'heartbeat') continue
            if (f.kind === 'forbidden') {
              // Always ends the connection, and carries no id, so the cursor
              // is untouched.
              controller?.abort()
              await onForbidden()
              return
            }
            // ONLY frames that carry an id advance the resume point.
            lastEventId = String(f.id)
            emit({ type: 'frame', frame: f })
          }
        }
        reconnect()
      } catch {
        if (!closed) reconnect()
      }
    })()
  }

  /**
   * Backoff resets on a connection that LASTED, never on one that merely
   * opened.
   *
   * The distinction is the whole point. A server that accepts and immediately
   * closes — a proxy in front of a restarting backend does exactly this —
   * answers 200 every time, so resetting on `res.ok` would hold the delay at
   * BACKOFF_BASE_MS forever and turn the retry loop into a hot loop against a
   * server already in trouble. Resetting on first byte has the same failure
   * one step later, because the opening snapshot arrives before the close.
   * Elapsed time is the only signal that separates "this connection worked"
   * from "this connection was accepted".
   */
  function reconnect(): void {
    if (closed) return
    if (openedAt !== null && now() - openedAt >= STABLE_MS) attempt = 0
    openedAt = null
    emit({ type: 'state', state: 'reconnecting' })
    const delay = Math.min(BACKOFF_BASE_MS * 2 ** attempt, BACKOFF_MAX_MS)
    attempt += 1
    later(connect, delay)
  }

  connect()

  return { close: stop }
}
```

- [ ] **Step 4: Run the transport tests and confirm they pass**

Run: `cd web && npx vitest run src/realtime/sse-connection.test.ts`
Expected: PASS, 11 tests.

These are timing-shaped and the injected clock is what keeps them honest. If one flakes, **do not add a sleep** — find which of `now`, `setTimeoutImpl` or `fetchImpl` the code is reaching around, and inject it. A test that passes because it waited long enough proves nothing about the watchdog.

- [ ] **Step 5: Commit**

```bash
cd web && npx tsc --noEmit && npx biome ci . && node tools/arch/main.ts
git add web && git commit -m "feat(web): SSE transport with backoff, resume, watchdog and the forbidden probe"
```

---

### Task 11: `cache-bridge.ts` and the real `/approvals`

Level three. REST is the source of truth for list **and** badge; the stream's entire job is to say "something you are showing has changed" — never to say what it changed to.

**Files:**
- Create: `web/src/realtime/cache-bridge.ts`, `web/src/realtime/cache-bridge.test.ts`
- Create: `web/src/realtime/use-attention-stream.ts`, `web/src/realtime/stream-status.tsx`
- Create: `web/src/approvals/approvals-query.ts`, `web/src/approvals/approval-list.tsx`
- Modify: `web/src/routes/_app/approvals.tsx`, `web/src/routes/_app.tsx`
- Create: `web/src/routes/_app/approvals.test.tsx`

**Interfaces:**
- Consumes: `EventFrame`, `openAttentionStream` (Task 10); `queryClient`, `AUTH_SCOPED_QUERY_OPTIONS` (Task 5).
- Produces:
  - `ATTENTION_KINDS: ReadonlySet<string>`
  - `createCacheBridge(qc: QueryClient, opts?: { debounceMs?: number; setTimeoutImpl?: typeof setTimeout }): { onFrame(f: EventFrame): void; flush(): void }`
  - `approvalsQueryOptions` — `{ queryKey: ['approvals'], queryFn, ...AUTH_SCOPED_QUERY_OPTIONS }`
  - `useAttentionStream()` — the hook the `_app` layout mounts once; `StreamStatusProvider` / `useStreamStatus()` — how every other route reads `connection` and `scopeEmpty` without opening a second stream.

- [ ] **Step 1: Write the failing bridge tests**

`web/src/realtime/cache-bridge.test.ts`:

```ts
import { QueryClient } from '@tanstack/react-query'
import { describe, expect, it, vi } from 'vitest'
import { createCacheBridge } from './cache-bridge'
import type { EventFrame } from './sse-connection'

const frame = (id: number, kind: string): EventFrame => ({
  kind: 'event',
  id,
  event: kind,
  frame: { id, run_id: 'r1', kind, payload: { node_id: 'gate' }, occurred_at: '' },
})

describe('createCacheBridge', () => {
  it('invalidates the approvals query on an attention frame', () => {
    const qc = new QueryClient()
    const spy = vi.spyOn(qc, 'invalidateQueries')
    const b = createCacheBridge(qc, { debounceMs: 0 })
    b.onFrame(frame(1, 'gate.waiting'))
    b.flush()
    expect(spy).toHaveBeenCalledWith({ queryKey: ['approvals'] })
  })

  it('NEVER writes cache data from an event payload', () => {
    // setQueryData from an event payload is wrong under BOTH of the server's
    // stated behaviours: the opening frames are the most recent 500 events,
    // not a projection of open gates, so a gate still waiting from before that
    // window is not on the stream at all; and resume deliberately re-sends a
    // backlog. Invalidation is idempotent under both. Reconstruction is not.
    const qc = new QueryClient()
    const spy = vi.spyOn(qc, 'setQueryData')
    const b = createCacheBridge(qc, { debounceMs: 0 })
    b.onFrame(frame(1, 'gate.waiting'))
    b.flush()
    expect(spy).not.toHaveBeenCalled()
  })

  it('coalesces a burst into one refetch', () => {
    const qc = new QueryClient()
    const spy = vi.spyOn(qc, 'invalidateQueries')
    const b = createCacheBridge(qc, { debounceMs: 50, setTimeoutImpl: ((fn: () => void) => setTimeout(fn, 0)) as never })
    for (let i = 0; i < 20; i++) b.onFrame(frame(i, 'run.started'))
    b.flush()
    expect(spy).toHaveBeenCalledTimes(1)
  })

  it('is idempotent under a replayed backlog', () => {
    const qc = new QueryClient()
    const spy = vi.spyOn(qc, 'invalidateQueries')
    const b = createCacheBridge(qc, { debounceMs: 0 })
    b.onFrame(frame(5, 'gate.waiting'))
    b.flush()
    b.onFrame(frame(5, 'gate.waiting'))   // the same event, re-delivered on resume
    b.flush()
    expect(spy).toHaveBeenCalledTimes(2)  // two invalidations, one truth
  })

  it('ignores kinds outside the attention set', () => {
    const qc = new QueryClient()
    const spy = vi.spyOn(qc, 'invalidateQueries')
    const b = createCacheBridge(qc, { debounceMs: 0 })
    b.onFrame(frame(1, 'phase.materialized'))
    b.flush()
    expect(spy).not.toHaveBeenCalled()
  })
})
```

- [ ] **Step 2: Run it and watch it fail**

Run: `cd web && npx vitest run src/realtime/cache-bridge.test.ts`
Expected: FAIL — module not found.

- [ ] **Step 3: Implement the bridge**

`web/src/realtime/cache-bridge.ts`:

```ts
import type { QueryClient } from '@tanstack/react-query'
import type { EventFrame } from './sse-connection'

/**
 * The kinds that move the approvals inbox. Mirrors backend/internal/runs/
 * events.go; a kind added there and not here costs a refresh, never
 * correctness, because the bounded refresh floor still catches it.
 */
export const ATTENTION_KINDS: ReadonlySet<string> = new Set([
  'gate.waiting',
  'gate.decided',
  'run.started',
  'run.succeeded',
  'run.failed',
  'run.dead',
])

/**
 * Typed frame → query invalidation. No network, no writes.
 *
 * THE BRIDGE INVALIDATES; IT NEVER RECONSTRUCTS. This is not a fallback for a
 * missing snapshot discriminator — it is the only correct reading of what the
 * server sends. A frame in the attention set invalidates the approvals query
 * and the badge follows from the refetched list.
 */
export function createCacheBridge(
  qc: QueryClient,
  opts?: { debounceMs?: number; setTimeoutImpl?: typeof setTimeout },
) {
  const debounceMs = opts?.debounceMs ?? 250
  const later = opts?.setTimeoutImpl ?? setTimeout
  let pending = false
  let scheduled = false

  function fire() {
    scheduled = false
    if (!pending) return
    pending = false
    void qc.invalidateQueries({ queryKey: ['approvals'] })
  }

  return {
    onFrame(f: EventFrame): void {
      if (!ATTENTION_KINDS.has(f.frame.kind)) return
      pending = true
      // A run emitting twenty events costs one refetch.
      if (debounceMs > 0 && !scheduled) {
        scheduled = true
        later(fire, debounceMs)
      }
    },
    flush(): void {
      fire()
    },
  }
}
```

- [ ] **Step 4: Run the bridge tests and confirm they pass**

Run: `cd web && npx vitest run src/realtime/cache-bridge.test.ts`
Expected: PASS, 5 tests.

- [ ] **Step 5: Wire the stream into the layout**

`web/src/realtime/use-attention-stream.ts`:

```ts
import { useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { api, unwrap } from '../api/client'
import type { ConnectionState } from './connection-state'
import { createCacheBridge } from './cache-bridge'
import { openAttentionStream } from './sse-connection'

export function useAttentionStream(): { connection: ConnectionState; scopeEmpty: boolean } {
  const qc = useQueryClient()
  const navigate = useNavigate()
  const [connection, setConnection] = useState<ConnectionState>('reconnecting')
  const [scopeEmpty, setScopeEmpty] = useState(false)

  useEffect(() => {
    const bridge = createCacheBridge(qc)
    const conn = openAttentionStream({
      probeMe: async () => unwrap(await api.GET('/api/v1/auth/me')),
      onEvent: (e) => {
        if (e.type === 'frame') bridge.onFrame(e.frame)
        else if (e.type === 'state') setConnection(e.state)
        else if (e.type === 'scope-lost') setScopeEmpty(true)
        else void navigate({ to: '/login' })
      },
    })
    return () => conn.close()
  }, [qc, navigate])

  return { connection, scopeEmpty }
}
```

`web/src/realtime/stream-status.tsx` — the context that PUBLISHES what the hook produces, so consumers read the stream's state without opening one:

```tsx
import { createContext, useContext } from 'react'
import type { ConnectionState } from './connection-state'

export type StreamStatus = { connection: ConnectionState; scopeEmpty: boolean }

// Defaults describe a tab that has not connected yet, not an error: a consumer
// rendered outside the provider should read as "still connecting", never as
// "your scope is empty" — which is a claim about the user's grants.
const StreamStatusContext = createContext<StreamStatus>({ connection: 'reconnecting', scopeEmpty: false })

export const StreamStatusProvider = StreamStatusContext.Provider
export const useStreamStatus = (): StreamStatus => useContext(StreamStatusContext)
```

`web/src/routes/_app.tsx` — in `AppLayout`, call `useAttentionStream()` **once** (one stream per tab, not one per route), pass `connection` and the approvals count into `AppShell`, and wrap the `<Outlet />` in `<StreamStatusProvider value={status}>` so descendant routes can read `scopeEmpty` without opening a second connection. Task 8's `AppShell` takes `connection` as a prop from this same call; `useStreamStatus()` is the only hook, and there is deliberately no second one.

- [ ] **Step 6: The approvals query and list**

`web/src/approvals/approvals-query.ts`:

```ts
import { queryOptions } from '@tanstack/react-query'
import { api, unwrap } from '../api/client'
import { AUTH_SCOPED_QUERY_OPTIONS } from '../api/query-client'

export const approvalsQueryOptions = queryOptions({
  queryKey: ['approvals'],
  queryFn: async () => {
    const res = await unwrap(await api.GET('/api/v1/approvals'))
    // The contract marks `approvals` required AND nullable: the handler really
    // can emit null for an empty list. Required and nullable are independent.
    return res.approvals ?? []
  },
  ...AUTH_SCOPED_QUERY_OPTIONS,
})
```

`web/src/approvals/approval-list.tsx` — card-first, per §6, with the desktop table projection deferred to Plan 12:

```tsx
import type { components } from '../api/schema'

type Approval = components['schemas']['ApiApprovalResponse']

export function ApprovalList(props: { approvals: Approval[]; scopeEmpty: boolean }) {
  if (props.approvals.length === 0) {
    return (
      <p className="text-text-muted text-sm">
        {props.scopeEmpty ? 'No projects are shared with you yet.' : 'Nothing is waiting on you.'}
      </p>
    )
  }
  return (
    <ul className="flex flex-col gap-2">
      {props.approvals.map((a) => (
        <li key={`${a.run_id}:${a.node_id}:${a.attempt}`} className="border-border bg-surface rounded-md border p-3">
          <p className="text-text font-medium">{a.task_title}</p>
          <p className="text-text-muted font-mono text-xs">
            {a.node_id} · attempt {a.attempt} · waiting since {a.waiting_since}
          </p>
        </li>
      ))}
    </ul>
  )
}
```

`web/src/routes/_app/approvals.tsx`:

```tsx
import { useSuspenseQuery } from '@tanstack/react-query'
import { createFileRoute } from '@tanstack/react-router'
import { ApprovalList } from '../../approvals/approval-list'
import { approvalsQueryOptions } from '../../approvals/approvals-query'
import { useStreamStatus } from '../../realtime/stream-status'

export const Route = createFileRoute('/_app/approvals')({
  loader: ({ context }) => context.queryClient.ensureQueryData(approvalsQueryOptions),
  component: Approvals,
})

function Approvals() {
  const { data } = useSuspenseQuery(approvalsQueryOptions)
  // scopeEmpty comes from the stream, not from a literal. Hardcoding false
  // makes "No projects are shared with you yet" unreachable and tells a user
  // with no grants that nothing is waiting on them — a different and wrong
  // statement. It is READ FROM CONTEXT, never by calling useAttentionStream()
  // again: that hook opens a connection, and a second caller would open a
  // second stream per tab.
  const { scopeEmpty } = useStreamStatus()
  return <ApprovalList approvals={data} scopeEmpty={scopeEmpty} />
}
```

- [ ] **Step 7: Write the badge-moves test and run it**

`web/src/routes/_app/approvals.test.tsx` — the component-level rehearsal of what Task 13 proves end to end:

```tsx
import { render, screen } from '@testing-library/react'
import { HttpResponse, http } from 'msw'
import { describe, expect, it } from 'vitest'
import { server } from '../../test/msw'
import { renderApp } from '../../test/render-app'

const approval = {
  run_id: 'r1', task_id: 't1', task_title: 'Add a thing', node_id: 'gate',
  project_id: 'p1', requester_id: 'u1', attempt: 1, generation: 1, waiting_since: '2026-08-15T00:00:00Z',
}

describe('/approvals', () => {
  it('renders the list and moves the badge when a frame arrives, with no user action', async () => {
    let count = 0
    server.use(
      http.get('/api/v1/approvals', () => HttpResponse.json({ approvals: count === 0 ? [] : [approval] })),
      http.get('/api/v1/stream', () =>
        new HttpResponse(
          new ReadableStream({
            start(c) {
              const enc = new TextEncoder()
              setTimeout(() => {
                count = 1
                c.enqueue(enc.encode(`id: 1\nevent: gate.waiting\ndata: ${JSON.stringify({ id: 1, run_id: 'r1', kind: 'gate.waiting', payload: {}, occurred_at: '' })}\n\n`))
              }, 10)
            },
          }),
          { headers: { 'content-type': 'text/event-stream' } },
        ),
      ),
    )
    await renderApp('/approvals')
    expect(await screen.findByText(/nothing is waiting on you/i)).toBeInTheDocument()
    // No click, no refresh: the assertion is on the RENDERED badge, never on a
    // request count. A refetch is the mechanism, and forbidding it would assert
    // against the design.
    expect(await screen.findByTestId('approvals-badge')).toHaveTextContent('1')
    expect(await screen.findByText('Add a thing')).toBeInTheDocument()
  })
})
```

Write `web/src/test/render-app.tsx` as the shared harness that mounts the router at a path inside a `QueryClientProvider` with an authenticated `/auth/me` handler — the `_app.test.tsx` from Task 7 should be refactored onto it in this task, since it now has two consumers and `web:arch` will say so.

Run: `cd web && npx vitest run src/routes/_app/approvals.test.tsx`
Expected: PASS.

- [ ] **Step 7: Verify and commit**

```bash
cd web && npm exec -- tsr generate && npx tsc --noEmit && npx vitest run && npx biome ci . && node tools/arch/main.ts
git add web && git commit -m "feat(web): invalidate-only cache bridge and the real /approvals slice"
```

---

### Task 12: Serve the bundle from the Go binary, behind a build tag, with the headers on

Not deferred to Plan 14, despite looking deferrable: how the bundle is built and where it is served from are constrained by CSP, self-hosted fonts and same-origin. Retrofitting those onto a finished scaffold means rewriting the scaffold.

**Files:**
- Create: `backend/internal/webui/ui_embed.go`, `backend/internal/webui/ui_noembed.go`, `backend/internal/webui/ui_test.go`, `backend/internal/webui/ui_embed_test.go`
- Create: `backend/internal/platform/httpx/security.go`, `backend/internal/platform/httpx/security_test.go`
- Modify: `backend/internal/app/server.go`, `backend/internal/app/server_test.go`
- Modify: `Dockerfile`, `.dockerignore`, `Taskfile.yml`

**Interfaces:**
- Consumes: `web:build` (Task 1) writing into `backend/internal/webui/dist`.
- Produces: `webui.Handler() http.Handler` — the SPA-serving handler under `embedui`, the typed refusal without it; `httpx.SecurityHeaders(dev bool) Middleware`; `app.Routes(log, db, ops, appOrigin, ui http.Handler)`.

- [ ] **Step 1: Write the failing security-header test**

`backend/internal/platform/httpx/security_test.go`:

```go
package httpx_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/giiorg/caesar/backend/internal/platform/httpx"
)

func TestSecurityHeaders(t *testing.T) {
	h := httpx.Chain(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), httpx.SecurityHeaders(false))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	csp := rec.Header().Get("Content-Security-Policy")
	for _, want := range []string{
		"default-src 'self'",
		"script-src 'self'",
		"font-src 'self'",
		"connect-src 'self'",
		"frame-ancestors 'none'",
		"base-uri 'none'",
		"object-src 'none'",
	} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP missing %q: %s", want, csp)
		}
	}
	// script-src is the property that matters and it admits no unsafe source:
	// the self-hosted fonts of the theme mean no third-party origin needs
	// allowing, which is what keeps this policy short.
	if strings.Contains(csp, "script-src 'self' 'unsafe") {
		t.Errorf("script-src must admit no unsafe source: %s", csp)
	}
	for header, want := range map[string]string{
		"X-Content-Type-Options":    "nosniff",
		"Referrer-Policy":           "same-origin",
		"Cross-Origin-Opener-Policy": "same-origin",
		"Strict-Transport-Security": "max-age=31536000; includeSubDomains",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}

func TestSecurityHeadersDevRelaxesOnlyWhatHMRNeeds(t *testing.T) {
	h := httpx.Chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), httpx.SecurityHeaders(true))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "ws:") {
		t.Errorf("dev CSP must allow the HMR websocket: %s", csp)
	}
	if rec.Header().Get("Strict-Transport-Security") != "" {
		t.Error("dev must not send HSTS: a stuck HSTS entry on localhost breaks every other local http service")
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `cd backend && go test ./internal/platform/httpx/ -run TestSecurityHeaders -v`
Expected: FAIL — `undefined: httpx.SecurityHeaders`.

- [ ] **Step 3: Implement the middleware**

`backend/internal/platform/httpx/security.go`:

```go
package httpx

import (
	"net/http"
	"strings"
)

// SecurityHeaders sets the response headers that make the served UI safe to
// run, and joins the existing CheckOrigin belt rather than replacing it.
//
// style-src carries 'unsafe-inline' and script-src deliberately does not. The
// primitives set inline STYLE ATTRIBUTES for positioning and scroll-lock, which
// style-src governs; refusing them would break every dialog and sheet. Inline
// style is not a script-execution vector, and script-src 'self' is the property
// this policy exists for.
//
// No third-party origins appear anywhere, and that is a consequence of a
// decision made elsewhere: the fonts are self-hosted, so nothing needs allowing.
func SecurityHeaders(dev bool) Middleware {
	directives := []string{
		"default-src 'self'",
		"script-src 'self'",
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data:",
		"font-src 'self'",
		"connect-src 'self'",
		"form-action 'self'",
		"frame-ancestors 'none'",
		"base-uri 'none'",
		"object-src 'none'",
	}
	if dev {
		// Vite's HMR client opens a websocket and injects its own module
		// graph. Relaxed HERE and only here, so production is not shaped by
		// the dev loop's needs.
		directives[1] = "script-src 'self' 'unsafe-inline' 'unsafe-eval'"
		directives[5] = "connect-src 'self' ws: wss:"
	}
	csp := strings.Join(directives, "; ")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("Content-Security-Policy", csp)
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Referrer-Policy", "same-origin")
			h.Set("Cross-Origin-Opener-Policy", "same-origin")
			if !dev {
				// Browsers ignore HSTS over plain http, so this is inert on a
				// lab origin — but it is still gated, because a developer who
				// once loaded https://localhost would poison every other local
				// service for a year.
				h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			}
			next.ServeHTTP(w, r)
		})
	}
}
```

- [ ] **Step 4: Run and confirm the header tests pass**

Run: `cd backend && go test ./internal/platform/httpx/ -run TestSecurityHeaders -v`
Expected: PASS, 2 tests.

- [ ] **Step 5: Write the failing embed tests — both twins**

`backend/internal/webui/ui_test.go` (no build tag; runs in every lane):

```go
package webui_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/giiorg/caesar/backend/internal/webui"
)

// TestHandlerIsAlwaysConstructible pins the property the build tag exists for:
// whichever twin compiled, there IS a handler and it answers. A tagless binary
// says out loud that no UI is embedded — it never serves a stub page that a
// forgotten `vite build` left behind, which is the failure mode a committed
// placeholder dist would have.
func TestHandlerIsAlwaysConstructible(t *testing.T) {
	rec := httptest.NewRecorder()
	webui.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK && rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 200 (embedui) or 501 (tagless)", rec.Code)
	}
	if rec.Code == http.StatusNotImplemented {
		if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
			t.Errorf("tagless refusal Content-Type = %q, want application/problem+json", ct)
		}
	}
}
```

`backend/internal/webui/ui_embed_test.go` (`//go:build embedui`):

```go
//go:build embedui

package webui_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/giiorg/caesar/backend/internal/webui"
)

func TestEmbeddedIndexIsServedAndCarriesNoInlineScript(t *testing.T) {
	rec := httptest.NewRecorder()
	webui.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", ct)
	}
	// ASSERTED, NOT REASONED ABOUT. Vite's module-preload polyfill is often
	// assumed to break script-src 'self'; it does not — it is injected as an
	// import into the entry's proxy module and lands in bundled application
	// JavaScript the policy already permits. That is a reason not to
	// pre-emptively disable it and NO reason at all to trust the outcome: this
	// assertion holds whatever any given Vite version chooses to emit, and the
	// reasoning does not. A hash-based script-src is the fallback if some
	// inline emission ever does survive.
	//
	// Substring-matching "<script>" is NOT good enough and would be the more
	// dangerous kind of green: Vite emits `<script type="module">`, so the
	// literal form this policy forbids in practice never appears as the bare
	// tag. Parse instead, and judge each tag by whether it has a src.
	// Every opening <script ...> tag, judged by whether it has a src. Stdlib
	// only — golang.org/x/net/html would parse this more properly and is not
	// worth a new module dependency for one assertion over a document Vite
	// generates. `<script` followed by a tag-name boundary avoids matching
	// nothing else; a src-less tag carries its body inline.
	body := rec.Body.String()
	for rest := body; ; {
		i := strings.Index(rest, "<script")
		if i < 0 {
			break
		}
		rest = rest[i+len("<script"):]
		if rest == "" || (rest[0] != '>' && rest[0] != ' ' && rest[0] != '\t' && rest[0] != '\n') {
			continue // <scriptfoo…>, not a script tag
		}
		end := strings.IndexByte(rest, '>')
		if end < 0 {
			t.Fatalf("unterminated <script tag in index.html:\n%s", body)
		}
		if !strings.Contains(rest[:end], "src=") {
			t.Errorf("index.html contains an inline <script%s>, which script-src 'self' forbids whatever its type attribute says:\n%s", rest[:end], body)
		}
		rest = rest[end+1:]
	}
}

func TestSPAFallbackServesIndexForUnknownPaths(t *testing.T) {
	for _, path := range []string{"/approvals", "/tasks", "/deep/link/that/does/not/exist"} {
		rec := httptest.NewRecorder()
		webui.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200 (SPA fallback)", path, rec.Code)
		}
	}
}

func TestAssetsAreServedWithTheirOwnContentType(t *testing.T) {
	// The fallback must not swallow a genuinely missing asset into index.html:
	// a 200 of HTML where a .js was requested is a broken page with no error.
	rec := httptest.NewRecorder()
	webui.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/does-not-exist.js", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET a missing asset = %d, want 404", rec.Code)
	}
}
```

**Prove this one can fail before you trust it** — it is the guard most likely to be green for the wrong reason, since the document it inspects is generated and normally contains exactly what is expected. Temporarily add `<script type="module">console.log(1)</script>` to `web/index.html`, rebuild the bundle, and run this test:

```bash
cd web && npm run build && cd ../backend && go test -tags embedui ./internal/webui/ -run TestIndexHasNoInlineScript
```

Expected: **FAIL**, naming the offending tag. The predecessor of this check matched the literal string `"<script>"` and would have passed that document, because Vite never emits the bare form — which is the entire reason the tag is parsed rather than substring-matched. Revert `index.html` and confirm green.

- [ ] **Step 6: Run them and watch them fail**

Run: `cd backend && go test ./internal/webui/`
Expected: FAIL — the package does not exist.

- [ ] **Step 7: Implement both twins**

`backend/internal/webui/ui_noembed.go`:

```go
//go:build !embedui

// Package webui serves the built web client. Which of its two files compiles
// is decided by the embedui build tag: the embedded bundle, or the honest
// refusal below.
package webui

import (
	"net/http"

	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

// Handler is the tagless twin: it says out loud that no UI is embedded.
//
// This shape is chosen over a committed placeholder dist specifically for its
// failure mode. A placeholder that a forgotten `vite build` leaves in place
// ships a stub page silently; this says so — the same rule the architecture
// tests already enforce on the backend, where stubs return typed
// NotImplemented and never fake success.
//
// It also keeps //go:embed from failing to compile against an absent dist, so
// `task backend:test` stays Node-free and fast.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.Problem(w, r, xerrors.NotImplemented("webui: built without the embedui tag"))
	})
}
```

`backend/internal/webui/ui_embed.go`:

```go
//go:build embedui

package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// dist is the Vite build, written straight here by web/vite.config.ts's
// build.outDir. One artifact, one location, no copy step to go stale.
//
//go:embed all:dist
var dist embed.FS

// Handler serves the bundle and falls back to index.html for unknown paths, so
// a deep link into a client route reloads correctly.
//
// The fallback is deliberately NOT applied under /assets: those names are
// content-hashed, so a miss there is a broken build, and answering it with
// HTML would turn a 404 anyone can diagnose into a script that silently parses
// as a document.
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic("webui: dist is not embedded: " + err.Error())
	}
	files := http.FileServerFS(sub)

	index, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		panic("webui: dist/index.html is missing: " + err.Error())
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := strings.TrimPrefix(r.URL.Path, "/")
		if clean != "" && !strings.HasPrefix(clean, "assets/") {
			if _, err := fs.Stat(sub, clean); err == nil {
				files.ServeHTTP(w, r)
				return
			}
		} else if strings.HasPrefix(clean, "assets/") {
			files.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(index)
	})
}
```

- [ ] **Step 8: Mount it in `Routes`**

`backend/internal/app/server.go`:

```go
// Routes assembles caesar's HTTP surface: health, the mounted /api/v1
// operations, a problem+json 404 for unknown API paths, and the web client at
// everything else.
//
// The UI handler is passed IN rather than constructed here so that internal/app
// does not have to care which twin of internal/webui compiled — and so the
// tests can mount a recogniser instead of a bundle.
func Routes(log *slog.Logger, db Pinger, ops []httpx.Operation, appOrigin string, ui http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /healthz", handleHealthz(db))
	mux.Handle("/api/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.Problem(w, r, xerrors.E(xerrors.ClassNotFound, "no such endpoint"))
	}))
	httpx.Mount(mux, ops)
	// Registered LAST and at the root, so every /api/ path and /healthz keeps
	// its own handler: ServeMux prefers the more specific pattern, and the
	// /api/ catch-all above is what keeps an unknown API path a typed
	// problem+json rather than an HTML page a client cannot parse.
	mux.Handle("/", ui)
	return httpx.Chain(mux, httpx.WithCorrelationID, httpx.AccessLog(log),
		httpx.CheckOrigin(appOrigin), httpx.SecurityHeaders(false))
}
```

Add to `backend/internal/app/server_test.go`:

```go
func TestUnknownAPIPathStaysProblemJSONRatherThanBecomingTheSPAFallback(t *testing.T) {
	// The regression this guards is quiet and total: a root handler registered
	// without the /api/ catch-all would answer every mistyped endpoint with
	// index.html, and a client would parse HTML as JSON forever.
	h := app.Routes(discard(), okPinger{}, nil, "http://caesar.test",
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>ui</html>")) }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json", ct)
	}
}
```

Update `runServe` to pass `webui.Handler()`, and fix every other `Routes(` call site (the integration tests).

- [ ] **Step 9: Run both twins**

```bash
cd backend
go test ./internal/webui/ ./internal/app/ -run 'TestHandler|TestUnknownAPIPath'   # tagless
cd ../web && npx vite build
cd ../backend && go test -tags embedui ./internal/webui/                          # embedded
```

Expected: PASS in both modes. The second run is the one that proves the SPA fallback and the no-inline-script assertion.

- [ ] **Step 10: The Docker UI stage**

`.dockerignore` — add, before the Dockerfile grows a UI stage:

```
web/node_modules
backend/internal/webui/dist
```

`Dockerfile` — insert as the first stage:

```dockerfile
FROM node:24.19.0-alpine AS ui
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
ARG COMMIT=none
# The commit stamp is baked into the bundle here so that §8's image assertion
# can compare it against the SHA the workflow holds independently. Reading a
# build stamp out of the candidate's own index.html and confirming the asset it
# names exists would be satisfied perfectly by a stale-but-consistent build —
# which is the exact thing being tested for.
RUN CAESAR_COMMIT=${COMMIT} npx vite build
```

and in the Go build stage, after `COPY backend/ ./`:

```dockerfile
COPY --from=ui /backend/internal/webui/dist ./internal/webui/dist
```

The `ui` stage's `build.outDir` is `../backend/internal/webui/dist` relative to `/web`, so it lands at `/backend/internal/webui/dist`. In the Go build stage, declare the tag as a build argument and consume it:

```dockerfile
ARG GOTAGS=embedui
RUN ... go build -tags "$GOTAGS" ...
```

**The tag is carried by default rather than opted into**: a build that forgets to opt *out* is the safe direction, and the tagless twin exists for the lanes that deliberately do. It is an `ARG` rather than a literal for exactly one reason — Step 3 of Task 14 builds a deliberately UI-less image to watch the release assertion go red, and **`docker build --build-arg GOTAGS=` against a Dockerfile that never declares `GOTAGS` does not mutate anything.** Docker emits a warning nobody reads, the literal `-tags embedui` stands, the "no-UI" image serves the UI, and the mutant reports success while proving nothing. A guard whose mutant cannot fail is the failure mode this project treats as a defect in its own right.

- [ ] **Step 11: Verify the dev proxy really keeps the cookie**

The `__Host-` prefix requires `Secure`, and browsers treat `http://localhost` as a secure context — so the dev loop works, but only same-origin. Verify rather than assume:

```bash
task db:up
cd backend && go run ./cmd/caesar serve -config caesar.dev.json &
cd ../web && npm run dev
```

In the browser at the Vite origin: log in, then reload. The session must survive. Then temporarily delete the `server.proxy` block from `vite.config.ts`, point the client at `http://127.0.0.1:8080` directly, and confirm the cookie is **dropped** — that is the failure this proxy prevents, and it is silent. Restore the proxy.

Record the outcome in a comment on the proxy block.

- [ ] **Step 12: Commit**

```bash
git add backend Dockerfile .dockerignore Taskfile.yml
git commit -m "feat: serve the web client from the binary behind the embedui tag"
```

---

### Task 13: The E2E lane — four beats against the real backend

The most expensive task in this plan and the only construct that can prove the SSE-into-cache path at all. A mocked stream proves the parser and nothing about the server's actual framing or its resume.

**Read pre-flight findings 1–4 before starting.** A gate cannot be a root node, so the only substitution in this harness is `runs.Executor`; everything else — `Routes`, the `Orchestrator`, `materializeNode`, `appendEvents`, the outbox, the SSE stream — is production code.

**Files:**
- Modify: `backend/internal/app/serve.go` (the `serveOptions` seam)
- Create: `backend/internal/app/webe2e_test.go` (`//go:build webe2e`)
- Create: `web/playwright.config.ts`, `web/e2e/approvals.spec.ts`
- Modify: `Taskfile.yml`

**Interfaces:**
- Consumes: everything above.
- Produces: `task web:e2e`; the harness's control server exposing `POST /fixture/open-gate` and `GET /fixture/credentials`.

- [ ] **Step 1: Add the composition-root seam and prove it is unreachable from config**

`backend/internal/app/serve.go`:

```go
// serveOptions are overrides no production path can set.
//
// It is UNEXPORTED and constructed only inside package app, so no config file,
// flag or environment variable can reach it — the language is the enforcement,
// not a convention. It exists for one reason: a gate can never be a root node
// (workflows.validateConsumption requires it to examine exactly one plan or
// patch), so reaching phase_runs.status = 'waiting_decision' means an agentic
// phase must complete, and the E2E lane substitutes the agent CONTAINER so the
// orchestrator's real gate materialization can be observed. Everything below
// that substitution is production code.
type serveOptions struct {
	executor runs.Executor
	// listener, when non-nil, is served INSTEAD of binding cfg.Server.Addr.
	//
	// It exists because of a genuine ordering deadlock, not for convenience.
	// Routes is constructed with cfg.Server.AppOrigin before net.Listen runs
	// (serve.go), and httpx.CheckOrigin compares a browser's Origin header to
	// that string by EXACT equality on every POST. With addr ":0" the port is
	// the operating system's answer, known only after the bind — so a test that
	// lets the OS choose cannot state the origin the browser will send, and
	// Playwright's login POST from http://127.0.0.1:<port> gets a 403 from
	// production middleware that is behaving exactly as designed.
	//
	// Handing the bound listener in inverts the order: the test binds, reads
	// the real port, sets AppOrigin to match, and only then boots. A fixed port
	// would be the other way out and is rejected — the lane must not collide
	// with a developer's own server or with a parallel package under test.
	listener net.Listener
}

func runServe(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	return runServeWith(ctx, cfg, log, serveOptions{})
}

func runServeWith(ctx context.Context, cfg config.Config, log *slog.Logger, opts serveOptions) error {
	// ... unchanged, up to the executor choice ...

	var executor runs.Executor = runs.NotImplementedExecutor{}
	var admission runs.Admission = runs.AlwaysAdmit{}
	// ... the cfg.Runner.Enabled branch, unchanged ...

	if opts.executor != nil {
		// Last word, and deliberately after the runner branch: a lane that set
		// both is a test that meant this one.
		executor = opts.executor
	}
	// ... rest unchanged, down to the listener ...

	// Replaces the bare `ln, err := net.Listen(...)`. The log line, the
	// deferred Close and everything after are untouched, so the injected
	// listener travels the identical path — including msgListening, which is
	// still where readiness is announced.
	ln := opts.listener
	if ln == nil {
		var err error
		ln, err = net.Listen("tcp", cfg.Server.Addr)
		if err != nil {
			return fmt.Errorf("http server: listen %s: %w", cfg.Server.Addr, err)
		}
	}
	// ... rest unchanged ...
}
```

Extend the blast-radius test to cover the second field — a seam with two overrides needs both pinned, or the next one gets added without one:

```go
	var zero serveOptions
	if zero.executor != nil {
		t.Fatal("the zero serveOptions must select the production executor")
	}
	if zero.listener != nil {
		t.Fatal("the zero serveOptions must bind cfg.Server.Addr itself")
	}
```

Add to `backend/internal/app/serve_integration_test.go`:

```go
// TestProductionServeNeverOverridesTheExecutor pins the seam's blast radius:
// runServe — the only path cmd/caesar reaches — passes an empty serveOptions,
// so no deployment can be talked into a fake executor by any config it accepts.
func TestProductionServeNeverOverridesTheExecutor(t *testing.T) {
	var zero serveOptions
	if zero.executor != nil {
		t.Fatal("the zero serveOptions must select the production executor")
	}
}
```

Run: `cd backend && go test -tags integration ./internal/app/ -run TestProductionServeNeverOverrides -v` → expect PASS.

- [ ] **Step 2: Write the harness — boot the real server, stub only the forge**

`backend/internal/app/webe2e_test.go`:

```go
//go:build webe2e

package app

// The web E2E harness. It boots the PRODUCTION runServe against a real
// Postgres, stands up a forge stub for the one outbound HTTP call task intake
// makes, and exposes a control server Playwright calls between assertions so
// the gate opens WHILE the browser is watching.
//
// What is real here: Routes, every handler, the Orchestrator, materializeNode,
// appendEvents (ordered ids under the counter lock, the outbox envelope and the
// NOTIFY, all in one transaction), the tailer, the hub and the SSE wire.
//
// What is faked: runs.Executor, and nothing else. Raw SQL insertion of a gate
// row is BANNED and this file must never grow one — appendEvents allocates the
// ids the resume path depends on, so a hand-written INSERT desynchronises the
// counter and leaves later resume behaviour broken in a way no assertion in
// this lane would catch.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/assets"
	"github.com/giiorg/caesar/backend/internal/identity"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/config"
	"github.com/giiorg/caesar/backend/internal/platform/secrets"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
	"github.com/giiorg/caesar/backend/internal/runs"
	"github.com/giiorg/caesar/backend/internal/runs/fake"
	"github.com/giiorg/caesar/backend/internal/workflows"
)

// If `goimports` disagrees with this list, believe goimports — it is stated in
// full here rather than as "plus what the body needs" because an import block
// a reader has to reconstruct is a step that cannot be executed as written,
// and this file's body reaches into twelve non-stdlib packages and a dozen stdlib ones. `io` and `log/slog` in particular are needed only by `e2eLogger`, stated further down — the kind of import that is easy to leave out because the expression that needs it is nowhere near the block.


const e2eWorkflow = `{
  "schema": 1,
  "nodes": [
    {"id": "impl", "type": "implement", "harness": "e2e", "instructions": "Write the change."},
    {"id": "gate", "type": "gate"}
  ],
  "edges": [{"from": "impl", "to": "gate", "artifact": "patch"}]
}`

func TestWebE2E(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. The forge stub. resolveIntake calls ResolveHead on the way to every
	//    task, and the GitHub client wants {"sha": ...} from
	//    GET {base}/repos/{slug}/commits/{branch}.
	forgeStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"sha": "0000000000000000000000000000000000000000"})
	}))
	defer forgeStub.Close()

	// 2. Bind FIRST, then configure. The port must be known before AppOrigin
	//    is set: CheckOrigin compares the browser's Origin header to it by
	//    exact equality on the login POST, and a browser always sends the port.
	//    Port 0 lets the OS pick, so the lane never collides.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve listener: %v", err)
	}
	baseURL := "http://" + ln.Addr().String()
	cfg := e2eConfig(t, testdb.NewURL(t), forgeStub.URL, baseURL)

	// 3. Boot the production server with the ONE substitution. The listener is
	//    a test seam, not a second fake: production Serve receives the same
	//    *net.TCPListener it would have bound itself.
	logBuf := &syncBuffer{}
	done := make(chan error, 1)
	go func() {
		done <- runServeWith(ctx, cfg, e2eLogger(logBuf), serveOptions{
			executor: fake.NewExecutor(),
			listener: ln,
		})
	}()
	// The socket accepts the moment it is bound, so a connection would queue in
	// the backlog while migrations run and Playwright would see a hang rather
	// than a refusal. Wait for the server to say it is serving.
	awaitListening(t, logBuf, done)

	// 4. Bootstrap the admin THROUGH the production path, then seed the assets
	//    the slice needs. Every one of these goes through the real services.
	seed := e2eSeed(t, ctx, cfg) // admin email/password, project, credential,
	                             // harness, repo, workflow version

	// 5. The control server. Playwright calls this BETWEEN assertions, which is
	//    what makes beat 2 happen while the browser is watching. It is part of
	//    the harness and never part of caesar's routes.
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fixture/open-gate" {
			http.NotFound(w, r)
			return
		}
		// CreateTask -> insertRun (pending) -> the Orchestrator's
		// startPendingRuns materializes the root -> a worker claims it -> the
		// FAKE executor returns a patch -> the orchestrator records it and
		// materializes the GATE, inserted 'waiting_decision', with its
		// gate.waiting event in the same appendEvents transaction.
		if _, _, err := seed.runsSvc.CreateTask(r.Context(), seed.admin, runs.NewTask{
			RepoID: seed.repoID, WorkflowID: seed.workflowID, WorkflowVersion: 1,
			Title: "E2E approval", Inputs: map[string]any{},
		}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer control.Close()

	// 6. Hand it to Playwright.
	cmd := exec.CommandContext(ctx, "npx", "playwright", "test")
	cmd.Dir = "../../../web"
	cmd.Env = append(os.Environ(),
		"CAESAR_BASE_URL="+baseURL,
		"CAESAR_CONTROL_URL="+control.URL,
		"CAESAR_EMAIL="+seed.email,
		"CAESAR_PASSWORD="+seed.password,
	)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("playwright: %v", err)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("runServe did not shut down")
	}
}
```

`syncBuffer` already exists in `serve_integration_test.go` in this same package and is reachable as-is. **`awaitListening` does not** — the name appears in the package, but as a method on `servedProcess` in `serve_realtime_integration_test.go:107`, behind the `runnerintegration` build tag and with a different signature; `serve_integration_test.go` has only an inline polling loop. Write it. It is a readiness wait here rather than a way to learn the port, because step 2 already knows the port:

```go
// awaitListening blocks until runServe reports that it is serving, or fails
// the test. It watches done as well as the log so that a boot failure — a
// migration that will not apply, an unreachable database — surfaces as that
// error rather than as a 60-second timeout that says nothing.
func awaitListening(t *testing.T, log *syncBuffer, done <-chan error) {
	t.Helper()
	want := `"` + msgListening + `"`
	deadline := time.Now().Add(60 * time.Second)
	for {
		if strings.Contains(log.String(), want) {
			return
		}
		select {
		case err := <-done:
			t.Fatalf("runServe returned before listening: %v\n%s", err, log.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never reported listening:\n%s", log.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}
```

The two config helpers this file adds:

```go
// appOrigin is the browser-visible base URL, and it must be character-for-
// character what Playwright's Origin header will carry — CheckOrigin does not
// normalize.
//
// This struct omits Runner entirely, which leaves Cpus at 0 and PidsLimit
// below the floor that config.validate() rejects. That is FINE and is not an
// oversight: validate() is called only from config.Load (config.go:260), and
// nothing here goes through Load — the same shape serve_integration_test.go
// has always used. Stated because it looks like a bug on every reading and
// has already been reported as one.
func e2eConfig(t *testing.T, dbURL, forgeBase, appOrigin string) config.Config {
	t.Helper()
	return config.Config{
		Server:   config.Server{Addr: "127.0.0.1:0", ShutdownTimeout: config.Duration(10 * time.Second), AppOrigin: appOrigin},
		Database: config.Database{URL: dbURL},
		Log:      config.Log{Level: "info"},
		Auth:     config.Auth{SessionIdleTTL: config.Duration(24 * time.Hour), SessionAbsoluteTTL: config.Duration(24 * time.Hour)},
		Security: config.Security{MasterKey: "e2e:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="},
		// A poll interval well under the spec's 30s timeout, so the gate
		// materializes while Playwright is still watching rather than after it
		// has given up. This is the one knob that decides whether this lane is
		// fast or flaky.
		Orchestrator: config.Orchestrator{
			Workers: 2, PollInterval: config.Duration(200 * time.Millisecond),
			LeaseTTL: config.Duration(time.Minute), HeartbeatInterval: config.Duration(10 * time.Second),
			OutboxRetention: config.Duration(time.Hour),
		},
		// Only the API bases matter: nothing in this lane clones or pushes, and
		// resolveIntake's ResolveHead is the single outbound call.
		Forge: config.Forge{
			GitHubBaseURL: forgeBase, GitLabBaseURL: forgeBase,
			GitHubCloneBaseURL: forgeBase, GitLabCloneBaseURL: forgeBase,
			PublishWorkDir: t.TempDir(), RequestTimeout: config.Duration(5 * time.Second),
		},
		Realtime: config.Realtime{
			HeartbeatInterval: config.Duration(20 * time.Second), PollFallback: config.Duration(time.Second),
			GrantRecheck: config.Duration(30 * time.Second), SubscriberQueue: 64,
			ReplayBatch: 500, SnapshotEvents: 500,
		},
	}
}

type e2eFixture struct {
	admin              authn.Principal
	email, password    string
	repoID, workflowID uuid.UUID
	runsSvc            *runs.Service
}

// e2eSeed builds the world through the REAL services — the same doors the admin
// CLI and the HTTP handlers use. Nothing here writes SQL directly.
func e2eSeed(t *testing.T, ctx context.Context, cfg config.Config) e2eFixture {
	t.Helper()
	pool, err := pgxpool.New(ctx, cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	clk := clock.System{}
	rec := audit.NewRecorder(clk)
	keys, err := secrets.ParseKeyset(cfg.Security.MasterKey)
	if err != nil {
		t.Fatal(err)
	}
	identitySvc := identity.NewService(pool, clk, rec, identity.Params{
		SessionIdleTTL: cfg.Auth.SessionIdleTTL.Std(), SessionAbsoluteTTL: cfg.Auth.SessionAbsoluteTTL.Std(),
	})
	const email, password = "e2e@caesar.test", "a long enough e2e password"
	user, err := identitySvc.Bootstrap(ctx, email, password)
	if err != nil {
		t.Fatal(err)
	}
	admin := authn.Principal{UserID: user.ID, Email: user.Email, Role: authn.RoleAdmin}

	assetsSvc := assets.NewService(pool, clk, rec, keys)
	workflowsSvc := workflows.NewService(pool, clk, rec)

	project, err := assetsSvc.CreateProject(ctx, admin, "e2e", "")
	if err != nil {
		t.Fatal(err)
	}
	modelCred, err := assetsSvc.CreateCredential(ctx, admin, "e2e-model", assets.CredentialModelKey, []byte("sk-e2e"))
	if err != nil {
		t.Fatal(err)
	}
	forgeCred, err := assetsSvc.CreateCredential(ctx, admin, "e2e-forge", assets.CredentialForgeToken, []byte("ghp-e2e"))
	if err != nil {
		t.Fatal(err)
	}
	// The workflow names harness "e2e", and checkHarnesses resolves every
	// agentic node's harness at intake — an unregistered one is a 400 at
	// CreateTask, not a hang, which is worth knowing when this lane fails.
	if _, err := assetsSvc.RegisterHarness(ctx, admin, assets.NewHarness{
		Name: "e2e", Kind: assets.HarnessCodex, ImageRef: "unused-by-the-fake-executor",
		ProviderEndpoint: "http://localhost:1", CredentialID: modelCred.ID,
	}); err != nil {
		t.Fatal(err)
	}
	repo, err := assetsSvc.CreateRepo(ctx, admin, project.ID, assets.NewRepo{
		Forge: assets.ForgeGitHub, Slug: "caesar/e2e", DefaultBranch: "main", CredentialID: forgeCred.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	wf, err := workflowsSvc.CreateWorkflow(ctx, admin, "e2e", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := workflowsSvc.CreateVersion(ctx, admin, wf.ID, []byte(e2eWorkflow)); err != nil {
		t.Fatal(err)
	}

	// A SECOND runs.Service over the same pool, for the control server only.
	// The one inside runServe owns the orchestrator; this one owns intake. They
	// share the database, which is the only state that matters.
	runsSvc := runs.NewService(pool, clk, rec,
		runsDefinitionSource{svc: workflowsSvc}, runsAssetSource{svc: assetsSvc}, runsAssetSource{svc: assetsSvc},
		newForgeAdapter(assetsSvc, forgeEndpointsFrom(cfg.Forge), &http.Client{Timeout: 5 * time.Second}),
		gitPublisher{assets: assetsSvc, workDir: cfg.Forge.PublishWorkDir, endpoints: forgeEndpointsFrom(cfg.Forge)},
		principalSource{svc: identitySvc}, runs.Params{Snapshot: runs.DefaultSnapshot()})

	return e2eFixture{admin: admin, email: email, password: password, repoID: repo.ID, workflowID: wf.ID, runsSvc: runsSvc}
}
```

`e2eLogger` is `slog.New(slog.NewJSONHandler(io.MultiWriter(buf, os.Stderr), nil))` — the buffer is what `awaitListening` reads the bound port out of, and the stderr copy is what makes a failing lane diagnosable in CI.

`fake.Executor` with **no** `Remote` registered produces the synthetic (non-publishable) patch, which is exactly right: the gate only examines the patch, and this plan never publishes.

- [ ] **Step 3: Write the Playwright spec — the four beats, named**

`web/playwright.config.ts`:

```ts
import { defineConfig, devices } from '@playwright/test'

export default defineConfig({
  testDir: './e2e',
  // Mobile-viewport project only: the phone is the flagship surface, and a
  // desktop project here would double the plan's slowest lane for a viewport
  // Plan 12 owns.
  projects: [{ name: 'mobile', use: { ...devices['Pixel 7'] } }],
  use: { baseURL: process.env.CAESAR_BASE_URL },
  // The Go harness owns the server's lifecycle, so there is no webServer here.
  forbidOnly: true,
  retries: 0,
  reporter: [['list']],
})
```

`web/e2e/approvals.spec.ts`:

```ts
import { expect, test } from '@playwright/test'

const CONTROL = process.env.CAESAR_CONTROL_URL ?? ''

test('a gate opened by the backend moves the approvals badge with no user action', async ({ page, request }) => {
  // --- login -> shell ---
  await page.goto('/login')
  await page.getByLabel('Email').fill(process.env.CAESAR_EMAIL ?? '')
  await page.getByLabel('Password').fill(process.env.CAESAR_PASSWORD ?? '')
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('navigation')).toBeVisible()

  // --- BEAT 1: REST returns N ---
  await page.getByRole('link', { name: 'Approvals' }).click()
  await expect(page.getByText('Nothing is waiting on you.')).toBeVisible()
  await expect(page.getByTestId('approvals-badge')).toHaveCount(0)

  // --- BEAT 2: the BACKEND TRANSACTION opens the gate ---
  // A test that skips this beat is the failure mode §7 exists to prevent.
  // GET /approvals is ListWaitingGates — it reads phase_runs.status and never
  // run_events — so appending an event and nothing else would invalidate the
  // query, trigger the refetch, and return the identical list: the badge would
  // not move and the spec would prove nothing while looking like it passed.
  const opened = await request.post(`${CONTROL}/fixture/open-gate`)
  expect(opened.status()).toBe(202)

  // --- BEATS 3 and 4: SSE invalidates; REST returns N+1 ---
  // No click, no reload, no navigation between here and the assertion. The
  // assertion is on the RENDERED badge, never on a request count: a refetch is
  // the mechanism, and forbidding it would assert against the design.
  await expect(page.getByTestId('approvals-badge')).toHaveText('1', { timeout: 30_000 })
  await expect(page.getByText('E2E approval')).toBeVisible()
})
```

- [ ] **Step 4: Add the task target**

```yaml
  web:e2e:
    desc: "Playwright against the real backend (needs Docker for Postgres)"
    dir: backend
    cmds:
      # embedui because the binary must serve the built UI; webe2e because the
      # harness is tagged so it never runs in backend:test.
      - go test -tags "integration,webe2e,embedui" -count=1 -timeout 15m ./internal/app/ -run TestWebE2E
```

Add a `web:e2e` prerequisite in the CI job rather than as a Taskfile `deps`, so `vite build` and `npx playwright install` stay visible steps in the workflow.

**Which CI job, and when.** The `web` job does not exist yet — Task 14 creates it. So this task adds the Taskfile target and the `scripts/gate` edit that goes with it, and Task 14 wires the workflow step when it writes the job. Do not edit `.github/workflows/ci.yml` here: there is nothing to edit, and a half-written job is worse than none. `.github/workflows/ci.yml` is deliberately absent from this task's file list for that reason.

- [ ] **Step 5: Keep the new tag inside the vetted set**

Add `webe2e` to the tag lists in `backend:vet`, `backend:analyze` and `backend:vuln`:

```yaml
      - go vet -tags integration,property,runnerintegration,webe2e ./...
```

**Do not add `embedui`** to those lists: `//go:embed all:dist` fails to compile against an absent `dist`, and those lanes must stay Node-free.

Then update `scripts/gate`'s `LANE_CMDS` to the new resolved lines — the script asserts them character for character and will refuse otherwise (pre-flight finding 8).

- [ ] **Step 6: Run the lane**

```bash
task db:up
cd web && npx playwright install --with-deps chromium && npx vite build
cd .. && task web:e2e
```

Expected: PASS. When it fails, read the failure in this order — Playwright's own output, then the server log the harness captured, then `SELECT status FROM phase_runs` on the test database. If the gate never reached `waiting_decision`, the fault is in the seed (a harness the workflow references but does not exist is a 400 at `CreateTask`, not a hang), not in the client.

- [ ] **Step 7: Commit**

```bash
git add backend web Taskfile.yml scripts/gate
git commit -m "test(web): prove a real gate moves the badge with no user action"
```

---

### Task 14: CI — the web job, the digest that is actually published, and the roadmap row

**The image assertion has to follow the artifact that is actually published, and today's CI shape would let it not.** `publish` runs after `ci-ok` and rebuilds from `context: .` on its own; no image or digest crosses that boundary. An `embedui` wired only into `task image:build` and the E2E lane would leave every required check green while the pushed image carried a UI-less binary — the precise failure this task exists to make impossible.

**Files:**
- Modify: `.github/workflows/ci.yml`
- Modify: `scripts/gate`, `Taskfile.yml`
- Modify: `docs/superpowers/plans/2026-08-02-caesar-v1-roadmap.md`
- Modify: `README.md`

- [ ] **Step 1: Add the `web` job**

```yaml
  web:
    runs-on: ubuntu-latest
    timeout-minutes: 25
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: backend/go.mod
          cache-dependency-path: backend/go.sum
      - uses: actions/setup-node@v4
        with:
          node-version-file: web/.nvmrc
          cache: npm
          cache-dependency-path: web/package-lock.json
      - uses: arduino/setup-task@v2
        with:
          version: 3.x
          repo-token: ${{ secrets.GITHUB_TOKEN }}
      - run: task web:install
      - run: task web:pins
      - run: task web:lint
      - run: task web:arch
      - run: task web:typecheck
      - run: task web:test
      - run: task web:routes:check
      - run: task web:build
      - run: npx playwright install --with-deps chromium
        working-directory: web
      - run: task web:e2e
```

Add `web` to `ci-ok`'s `needs` list. That is the line that makes all of the above required.

- [ ] **Step 2: Build once, test that digest, promote that digest**

Replace the `publish` job so the artifact under test is the artifact published:

```yaml
  image:
    needs: [ci-ok]
    if: github.event_name == 'push' && github.ref == 'refs/heads/main'
    runs-on: ubuntu-latest
    permissions:
      contents: read
      packages: write
    outputs:
      digest: ${{ steps.build.outputs.digest }}
    steps:
      - uses: actions/checkout@v4
      - uses: docker/setup-buildx-action@v3
      - uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      # ONE build. It is pushed under a candidate tag nobody deploys, tested by
      # digest, and only then promoted. The previous shape rebuilt here from
      # `context: .` with nothing crossing the ci-ok boundary, so the tested
      # image and the pushed image were never the same bytes.
      - id: build
        uses: docker/build-push-action@v6
        with:
          context: .
          push: true
          tags: ghcr.io/giiorg/caesar:candidate-${{ github.sha }}
          build-args: |
            VERSION=sha-${{ github.sha }}
            COMMIT=${{ github.sha }}
            DATE=${{ github.event.head_commit.timestamp }}

  image-serves-ui:
    needs: [image]
    runs-on: ubuntu-latest
    permissions:
      contents: read
      packages: read
    # runServe connects to Postgres and applies migrations BEFORE it binds the
    # listener (serve.go). So "the SPA fallback is static, therefore the binary
    # needs no database" is FALSE for this check: without a reachable database
    # nothing ever listens on 8080, and the job fails for a reason that has
    # nothing to do with whether the UI is embedded. A green run has to mean
    # the bundle is there, not merely that the container refused to boot.
    services:
      postgres:
        image: postgres:17
        env:
          POSTGRES_PASSWORD: caesar
          POSTGRES_USER: caesar
          POSTGRES_DB: caesar
        ports: ['5432:5432']
        options: >-
          --health-cmd pg_isready --health-interval 5s
          --health-timeout 5s --health-retries 10
    steps:
      - uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      - name: the candidate DIGEST serves this commit's UI
        run: |
          set -euo pipefail
          IMAGE="ghcr.io/giiorg/caesar@${{ needs.image.outputs.digest }}"
          # config.Load opens the path and fails if it is absent, so the file
          # must exist; everything else this check needs comes from defaults
          # and the two environment overrides Load already honours.
          # NOT '{}'. With an empty document the defaults apply, and
          # forge.publish_work_dir defaults to /var/lib/caesar/publish —
          # which runServe creates AND write-probes before it binds HTTP.
          # The final image is distroless:nonroot running as uid 65532, and
          # /var/lib is root-owned 0755, so MkdirAll fails, nothing ever
          # listens, and both this check and its no-UI mutant die before
          # reaching the thing they test. /tmp is 01777 in that image.
          echo '{"forge":{"publish_work_dir":"/tmp/caesar-publish"}}' > /tmp/config.json
          docker run -d --name candidate --network host \
            -v /tmp/config.json:/etc/caesar/config.json:ro \
            -e DATABASE_URL='postgres://caesar:caesar@127.0.0.1:5432/caesar?sslmode=disable' \
            -e CAESAR_MASTER_KEY='dev:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=' \
            "$IMAGE" serve -config /etc/caesar/config.json
          # Boot first, UI second — and report them separately. /healthz answers
          # only once migrations have applied and the listener is bound, so a
          # timeout here is a BOOT failure and the logs say which; conflating it
          # with the UI assertion is what made the previous shape unreadable.
          for _ in $(seq 1 60); do curl -sf http://127.0.0.1:8080/healthz >/dev/null && break; sleep 2; done
          if ! curl -sf http://127.0.0.1:8080/healthz >/dev/null; then
            echo "candidate never became healthy — this is a boot failure, not a UI failure"
            docker logs candidate
            exit 1
          fi
          BODY=$(curl -sS -D /tmp/h http://127.0.0.1:8080/)
          grep -qi 'content-type: text/html' /tmp/h
          # THE ORACLE COMES FROM OUTSIDE THE IMAGE. Reading a build stamp out
          # of the candidate's own index.html and confirming the asset it names
          # exists is satisfied perfectly by a stale-but-consistent build — the
          # exact thing being tested for. So the comparison is against the SHA
          # this workflow holds independently.
          echo "$BODY" | grep -q '${{ github.sha }}'
          # "Some HTML" is too weak on its own: the SPA fallback satisfies it
          # from almost any state, including a UI-less binary's typed refusal.
          echo "$BODY" | grep -qv 'not implemented'

  publish:
    needs: [image, image-serves-ui]
    runs-on: ubuntu-latest
    permissions:
      packages: write
    steps:
      - uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      # Promotion is a re-tag of the SAME digest — no rebuild can slip in
      # between the assertion and the release tags.
      - name: promote the tested digest
        run: |
          set -euo pipefail
          docker buildx imagetools create \
            --tag ghcr.io/giiorg/caesar:main \
            --tag ghcr.io/giiorg/caesar:sha-${{ github.sha }} \
            ghcr.io/giiorg/caesar@${{ needs.image.outputs.digest }}
          echo "**image digest:** \`${{ needs.image.outputs.digest }}\`" >> "$GITHUB_STEP_SUMMARY"
```

For `github.sha` to appear in the served document, `index.html` must carry the stamp. Add to `web/index.html`:

```html
    <meta name="caesar-commit" content="%CAESAR_COMMIT%" />
```

and a tiny Vite `transformIndexHtml` plugin (or `define` + a script-free `<meta>` replacement in `vite.config.ts`) substituting `%CAESAR_COMMIT%`. **It must not be an inline `<script>`** — Task 12's assertion forbids one, and rightly.

- [ ] **Step 3: Prove the assertion can fail**

An oracle nobody has seen fail is a rubber stamp. On a scratch branch, build the image **without** the `embedui` tag and run the `image-serves-ui` script against it locally:

```bash
# GOTAGS is declared by the Dockerfile (Task 12 Step 10). Confirm the mutation
# actually took before trusting the result: a build-arg Docker does not consume
# is a warning, not an error, and the image would quietly still embed the UI.
docker build --build-arg COMMIT=deadbeef --build-arg GOTAGS= -t caesar:noui .

# The same boot the CI check performs — config file, database, master key —
# because a container that never listens proves nothing either way.
docker run -d --name pg -e POSTGRES_PASSWORD=caesar -e POSTGRES_USER=caesar \
  -e POSTGRES_DB=caesar -p 5432:5432 postgres:17
# See the CI job: '{}' would default publish_work_dir to /var/lib/caesar/publish,
# which the distroless:nonroot uid cannot create, so the container never listens.
echo '{"forge":{"publish_work_dir":"/tmp/caesar-publish"}}' > /tmp/config.json
docker run -d --name noui --network host \
  -v /tmp/config.json:/etc/caesar/config.json:ro \
  -e DATABASE_URL='postgres://caesar:caesar@127.0.0.1:5432/caesar?sslmode=disable' \
  -e CAESAR_MASTER_KEY='dev:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=' \
  caesar:noui serve -config /etc/caesar/config.json
until curl -sf http://127.0.0.1:8080/healthz >/dev/null; do sleep 1; done
curl -sS -D - http://127.0.0.1:8080/
```

Expected: `/healthz` answers 200 — the binary boots fine, it simply has no UI — and `/` returns the typed `problem+json` refusal, `not implemented`, with no commit stamp. That is **the check failing for the one reason it exists to catch**, which is the point: a red run caused by a container that never started would have looked identical while testing nothing. Then build normally and confirm it passes. Record both outcomes in the commit message.

- [ ] **Step 4: Fold the web lanes into the gate**

Add a `web:ci` aggregate to `Taskfile.yml`:

```yaml
  web:ci:
    cmds:
      - task: web:pins
      - task: web:lint
      - task: web:arch
      - task: web:typecheck
      - task: web:test
      - task: web:routes:check
      - task: web:build
```

and add `web:ci` plus `web:e2e` to the root `ci` task. Then teach `scripts/gate` about them: a new log file, a new status variable, a new `fail` line, and one `LANE_CMDS`-style entry per new lane asserting its **resolved** command. Follow the existing pattern exactly — the script's whole thesis is that what it runs is stated in one place rather than implied.

- [ ] **Step 5: Run the whole gate**

```bash
task db:up
scripts/gate "$(pwd)" "$(git rev-parse HEAD)" caesar-11-web-foundation
```

Expected: `gate: passed`. Run the **full** gate, not a hand-picked lane list — a narrowed run has missed three lanes on this project before, which is why the script scrubs `PKG` and friends out of the environment.

- [ ] **Step 6: Update the roadmap and the README**

In `docs/superpowers/plans/2026-08-02-caesar-v1-roadmap.md`, flip row 11 to `**done** — 2026-08-15-caesar-11-web-foundation.md`. Do not renumber anything. Row 10a stays `pending`; its work is not in this plan and §3's client-side mitigations are explicitly the V1 answer, not the permanent one.

Add a short `web/README.md` covering: the pinned toolchain and why `.nvmrc` is the single source; how to run the dev loop (`task db:up`, `task backend:run`, `npm run dev`) and why the proxy is not optional; what `task web:arch` enforces and how to read its two failure messages; and the one paragraph a future reader most needs — **that the E2E lane fakes `runs.Executor` and only `runs.Executor`, why (a gate cannot be a root node), and that raw SQL insertion of a gate row is banned.**

- [ ] **Step 7: Commit**

```bash
git add .github/workflows/ci.yml Taskfile.yml scripts/gate docs web/README.md web/index.html web/vite.config.ts
git commit -m "ci(web): gate the web lane and publish only the digest that was tested"
```

---

## Self-review

Run against the spec after the plan is written, before execution begins.

**Spec coverage.** §1 route surface → Tasks 6, 8, 11. §2 structure and enforcement → Tasks 1, 3. §3 realtime, all three layers plus the probe and the refresh floor → Tasks 5, 9, 10, 11. §4 errors, `ApiError`, the 401 exemption, session bootstrap, the contracts task → Tasks 4, 5, 7. §5 forms before any feature form → Task 6. §6 theming, palette reset, hybrid typography, mobile-first → Task 2 (with the shell's density in Task 8). §7 the four test levels and the fixture → Tasks 9–11 and 13. §8 embed, SPA fallback, CSP, the image assertion → Tasks 12, 14. §9 versions → Task 1's Global Constraints. §10 residuals → the dev-proxy verification is Task 12 Step 11; row 10a is left open deliberately.

**Three things a reviewer should push on.**

1. **The E2E substitution is the plan's largest deviation from the spec as written.** §7 says "real binary"; this plan says "the production composition root, in-process, with one port faked". The reasoning is in pre-flight findings 1–3 and the decision is recorded as the user's. If a reviewer disagrees, the argument to have is whether faking `runs.Executor` weakens the four beats — not whether the spec's literal wording was followed.

2. **Task 1 is large.** It carries the manifest, three configs, the router, two checker scripts and a mutant. It resisted splitting because none of its pieces is independently verifiable: a manifest with no `tsc` proves nothing, and the mutant needs the router. If it needs splitting during execution, the seam is after Step 8 (Biome), with the mutant machinery as a second task.

3. **The `noRestrictedGlobals` fetch ban is the right Biome rule at 2.5.8, under the `style` group** — `explain` reports `lint/style/noRestrictedGlobals`, and it accepts `options.deniedGlobals`, so the GritQL fallback §2 sanctions is not needed. The GROUP was wrong in an earlier draft (`suspicious`), which is the failure worth remembering: the option name was right the whole time and the rule would still never have fired. Verify the ban actually bites (Task 5 Step 9) rather than trusting the config — a rule that is configured but inert looks identical to one that works.

**Placeholder scan.** No "TBD", no "add error handling", no "similar to Task N". One step still states a decision procedure rather than an outcome — Task 14 Step 2, the `%CAESAR_COMMIT%` substitution mechanism — because the exact Vite surface at this pin was not verified against an install. It names the property to preserve and the fallback, which is the honest shape for a fact the plan author could not check.

Two others used to. Both are now resolved against the published packages: `unwrap`'s signature is the structural shape, because `openapi-fetch@0.17.0`'s `FetchResponse` takes the operation's response map first (Task 5 Step 7); and the `fetch` ban is `style.noRestrictedGlobals` with `options.deniedGlobals`, which Biome 2.5.8 does support, so the GritQL fallback §2 sanctioned is not needed here.

**Type consistency.** `ApiError` fields (`correlationId`, `fields`) are used identically in Tasks 5, 6 and 10. `ParsedFrame`/`EventFrame` flow unchanged from Task 9 into 10 into 11. `ConnectionState` is defined once in Task 8 and imported by Tasks 10 and 11. `meQueryOptions`/`approvalsQueryOptions` both spread `AUTH_SCOPED_QUERY_OPTIONS` from Task 5. `Routes` gains its fifth parameter in Task 12 and every call site is named there.
