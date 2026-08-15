# caesar — web foundation (Plan 11 design)

**Date:** 2026-08-15 · **Status:** all sections approved by the user in the brainstorming session of 2026-08-15. · **Refines:** `2026-08-02-caesar-v1-design.md` §5 (UI architecture) and §7 (frontend testing/CI) — it resolves what §5 left open rather than changing it; everything in §5 stands. · **Consumed by:** Plan 11 (web foundation), whose task decomposition this document is the input to. · **Locked upstream:** the page map, shell A, the run-detail/approval/workflow-editor/tasks screens, and the Terminal visual direction are decided in `docs/brainstorm/2026-08-01-decisions.md` §7 with mockups in `.superpowers/brainstorm/12432-1785604290/content/`. This design does not revisit them.

Plan 11 is a foundation plan, and foundation plans have a characteristic failure mode: every abstraction ships green against its own fake and is first met by reality one plan later, when the cost of being wrong has multiplied by the number of features built on top. This program has already paid for that lesson twice — a fake recorder more capable than the real thing hid a latent critical through three tasks and every gate, and rows 07 and 08 still hang open on manual checklists that only an operator can run. So the governing decision here is §1: **Plan 11 ends with one thin slice running against the real backend**, and the CI lane that proves it is part of the plan rather than a promise made to Plan 12.

---

## 1. Scope — what "done" means

Plan 11 delivers the scaffold, the constitution and its enforcement, three abstractions (server-state, forms, theming), the app shell, and **one real vertical slice**: `/login` → shell → `/approvals`, with a live badge fed by the attention stream. Every foundation piece acquires exactly one real consumer, so a broken abstraction fails visibly inside Plan 11.

**Route surface at the plan boundary:**

| Route | State at end of Plan 11 |
|---|---|
| `/login` | Real. Typed form, `POST /api/v1/auth/login`, `__Host-` session cookie. |
| `/` (Home) | Shell renders; body is a typed stub naming Plan 12. |
| `/approvals` | **Real.** `GET /api/v1/approvals` through the generated client; live badge from `GET /api/v1/stream`. |
| `/runs`, `/tasks`, `/workflows`, `/repos`, `/admin` | Route files rendering the same typed stub. |

`/approvals` is the slice for three converging reasons: it is the only list endpoint that exists (there is no `GET /runs` — runs are reachable only under `/tasks/{id}/runs`), it is the decisions-record phone flagship, and it is the natural consumer of the attention badge. Choosing it is not a preference.

**Deliberately excluded from Plan 11**, each because it belongs to a later row and none because it was forgotten: the assistant ask-bar and its socket (Plan 13 — no placeholder either; a dock with nothing in it is dead config), the ⌘K command palette, the run-detail timeline, the workflow outline editor, the approve screen's diff accordion, any second theme, and offline/service-worker behaviour (the manifest promises installability, not offline).

**Not deferred, despite looking deferrable:** serving the built UI from the Go binary, the SPA fallback, CSP and the security-header middleware. These are Plan 11's, not Plan 14's, because "no inline scripts" is a constraint on how the bundle is built — discovering it after the scaffold is written means rewriting the scaffold.

## 2. Structure and the constitution

Route-centric colocation under TanStack Router **file-based** routing. A component lives beside its only consumer and moves up the tree only when a second consumer appears.

```
web/src/
  routes/            file-based routes; routeTree.gen.ts committed + drift-gated
  api/               schema.d.ts (generated) · client.ts · errors.ts
  realtime/          sse-parser.ts · sse-connection.ts · cache-bridge.ts
  forms/             use-app-form.ts · problem-to-fields.ts · fields/
  ui/                primitives (shadcn-style over Base UI)
  styles/            tokens.css · theme-dark.css · theme-light.css
```

**Enforcement is mechanical, and the split is deliberate.** Biome expresses most of the constitution directly: `noBarrelFile` and `noReExportAll` (no barrel files), `noDefaultExport` (named exports), `useFilenamingConvention`, and cross-group import bans via `overrides` scoped by `includes` plus `noRestrictedImports` patterns. GritQL plugins cover any further single-file pattern.

One rule is structurally beyond it. **Consumer-count file placement needs a cross-file import graph**, which no per-file linter has. The backend met this same gap with custom `go/analysis` analyzers; the frontend gets the twin — a small Node checker walking the TS import graph, run as its own `task web:arch` target. The rule is worthless undetected: it only bites when a second consumer appears, which is exactly the moment nobody notices. `web:arch` is also where later graph-shaped assertions land, including "components reference semantic token names, never raw color values" (§6).

**`routeTree.gen.ts` is generated, so it is committed and regenerated-and-diffed** in the web lane, exactly as `schema.d.ts` is in the contracts lane. No generated artifact in this repo is trusted to be hand-current.

## 3. Server state and realtime

All server data flows through TanStack Query over the **generated** client. Hand-written `fetch` calls against the API are banned and the ban is a lint rule, not a convention. URL-as-state for every filter and tab via Zod `validateSearch` with `.catch`.

Realtime is three layers, separated so each is testable at a different level and none can hide a bug in another:

```
sse-parser.ts      Pure incremental SSE framing → typed events. No I/O.
sse-connection.ts  fetch + ReadableStream; backoff; heartbeat watchdog.
                   Owns reconnection, never correctness.
cache-bridge.ts    Typed event → queryClient.setQueryData. No network.
```

**`fetch` + `ReadableStream`, not `EventSource`.** `EventSource` reconnects on a fixed interval the page cannot control, surfaces errors opaquely, and hides the HTTP status — so it can deliver neither §5's "exponential backoff" nor its "heartbeat watchdog surfacing staleness honestly", and it cannot distinguish a 401 from a dropped tunnel. The cost is re-implementing `Last-Event-ID` resume by hand, which this design wants explicit anyway.

**Snapshot versus delta is a typed distinction, never an inference.** The server may answer a resume with a fresh snapshot when the supplied id is too old — this is not an edge case but the documented degradation path, reachable after a restore from backup while clients hold higher ids (Plan 10's first late bug). The bridge **replaces** on snapshot and **merges** on delta, and the two arrive as separate variants of a discriminated union at the parser boundary. Deciding which one happened by inspecting payload shape downstream is precisely how a list silently doubles.

**Staleness is stated, never implied.** The connection heartbeats every ~20s; a watchdog at 45s flips the shell's connection indicator to "reconnecting". The UI never renders stale data as live — a frozen badge that looks authoritative is worse than an honest one that admits it lost the stream.

**Grant revocation closes streams server-side** (§4). The client treats a server-initiated close as an ordinary reconnect, and a 401 or 403 on the reconnect attempt as an authorization change — not as a transport failure to retry forever.

## 4. Errors

The `problem+json` envelope becomes a typed `ApiError` carrying `type`, `title`, `status`, `detail`, `correlation_id`, and `errors[]`. It has exactly three consumers, and they are the whole error design:

1. **Forms** map `errors[]` onto fields by pointer (§5).
2. **A 401 from any call** redirects to `/login`. A 403 renders a typed "you do not have this grant" panel rather than an empty list — an authorization failure that presents as "no results" is a lie the UI tells on the server's behalf.
3. **`correlation_id` is rendered on every error surface.** §4 makes it the only thread between a user's complaint and a server log line; a UI that swallows it severs that thread permanently.

Unclassified failures surface as permanent, matching the backend's safe direction.

## 5. Forms — built before any feature form

The shared typed form abstraction lands before any form that has a feature behind it. This ordering is the explicit lesson of the 72-hand-wired-forms survey in decisions §10, and inverting it is how that outcome happens again.

`useAppForm` composes react-hook-form + Zod resolver + the generated request types, with one field-component registry, uniform error rendering, and a submit orchestrator that catches `ApiError` and calls `setError` per field pointer, with a form-level fallback for problems that name no field.

**The login form is its first consumer.** That is the point of putting it in Plan 11: the abstraction ships having satisfied a real endpoint with real validation failures, not a fixture. Its flagship consumer remains the Plan 12 workflow editor, whose step-card fields are generated from the engine's step schemas so that new step types need no UI code.

## 6. Theming

Semantic OKLCH custom properties, `data-theme` on the root element, system preference as the default, exactly one dark (flagship) and one light shipped.

The dark theme's values derive from the approved Terminal mockup — `#0d1117` ground, `#161b22` surface, `#21262d` border, `#c9d1d9` body, `#58d6a3` accent — converted to OKLCH and named semantically. Light is its twin over the identical semantic names. **A new theme is a token file and never a component edit**, which holds only if components reference semantic names exclusively; `web:arch` asserts it (§2).

**Typography is hybrid, not the mockup's full monospace.** Spec §5 pins monospace for data, logs, IDs and statuses with phone-legible body text, and the mockup's all-mono treatment is a poster for the aesthetic rather than a specification of it. JetBrains Mono (the mockup's own face) carries data; Inter carries body and labels. Both self-hosted variable fonts — no third-party font origin, which is also what makes the §8 CSP simple.

Mobile-first structurally: bottom sheets as the default modal on phone, safe-area insets, 44px minimum targets, squarish corners, console-honest density, card-first lists with desktop table projections, and a web app manifest.

## 7. Testing and CI

| Level | What it covers |
|---|---|
| Vitest, pure | SSE frame parsing (including split chunks and interleaved heartbeats), `problem+json` → field mapping, the token contract |
| Vitest + Testing Library + MSW | Generated client, Query wiring, form submit and error paths, shell navigation, theme switching |
| Playwright, mobile-viewport project, **real binary** | login → shell → approvals list loads → an injected `run_event` moves the badge with no refetch |

MSW intercepts at the network level so the generated client is exercised rather than stubbed.

**The Playwright lane drives the real thing**: Postgres up, migrations applied, first admin created through `caesar admin bootstrap`, Vite build embedded into a `-tags embedui` binary, binary served, specs run against it. This is the most expensive task in Plan 11 and the only construct that can prove the SSE-into-cache path at all — a mocked stream proves the parser and nothing about the server's framing, its watermark splice, or its resume. Plan 12 then adds specs to a lane that already works.

A new `web` job joins CI's `ci-ok` composite check: `biome ci` · `task web:arch` · `tsc --noEmit` · `vitest run` · `routeTree.gen.ts` regen-diff · `vite build` · `task web:e2e`.

## 8. Serving and hardening

**The embed is behind `//go:build embedui`.** A twin file behind `//go:build !embedui` compiles in its place and returns a typed `problem+json` at non-API routes. `task image:build` and `task web:e2e` pass the tag; `task backend:test` does not, so the backend lane stays Node-free and fast, and `//go:embed` never fails to compile against an absent `dist`.

This shape is chosen over a committed placeholder `dist` specifically for its failure mode. A placeholder that a forgotten `vite build` leaves in place ships a stub page silently; a tagless binary says out loud that no UI is embedded, which is the same rule the architecture tests already enforce on the backend — stubs return typed `NotImplemented`, never fake success. **CI asserts the published image actually carries a UI** by requesting a non-API route from the built image and requiring HTML rather than the typed "ui not embedded" problem document — an assertion about observed behaviour, not about the build command someone believes was run. The loud failure therefore cannot reach a release either.

SPA fallback serves `index.html` for unknown non-`/api` paths. Production CSP is `script-src 'self'`, and the self-hosted fonts of §6 mean no third-party origins need allowing; the dev server relaxes it for HMR.

One Vite default stands in the way and the plan must handle it explicitly: `build.modulePreload.polyfill` is on by default and injects its polyfill as an **inline** script into `index.html`, which `script-src 'self'` blocks outright. Turning the polyfill off is the intended fix — its target is browsers old enough to be irrelevant here — but the plan verifies the built `index.html` contains no inline script rather than trusting the setting. A hash-based `script-src` is the fallback if some other inline emission survives. Standard security headers and HSTS land in the same middleware, joining the existing `appOrigin` check on unsafe methods.

**Same-origin by construction**, so no CORS and no reverse-proxy configuration. The graduation path to separate deployment stays cheap through a configurable client base URL, and is not built in V1.

## 9. Residuals

Named rather than hidden, in the house style:

- **`__Host-` cookies require `Secure`.** Browsers treat `http://localhost` as a secure context, so the dev loop works, but the Vite dev server must proxy `/api` rather than cross origins — a cross-origin dev setup would silently drop the session cookie. Plan 11 verifies this in the dev-proxy task rather than assuming it.
- **Playwright's real-backend lane is the plan's slowest CI job** and its most likely source of flakes, since it composes Postgres, a migration, a bootstrap, two builds and a server start. It is worth the cost for exactly one spec; growing it carelessly in Plan 12 would not be.
- **The badge assertion needs an injected `run_event`.** Plan 11 injects one directly rather than driving a real run — a real run means a real agent and a live subscription, which no CI lane may ever hold (§7 of the V1 spec). This proves the client's half of the contract; the server's half is already proven by Plan 10's integration suite.
