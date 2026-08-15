# caesar — web foundation (Plan 11 design)

**Date:** 2026-08-15 · **Status:** §§1–8 approved by the user in the brainstorming session of 2026-08-15; §9 added afterwards, with its one open choice (primitives) since decided by the user. Sections 2, 3, 4, 7 and 8 were then corrected against the backend and the toolchain under adversarial review — §3 most heavily, where the original snapshot/delta model did not match what the server sends. · **Refines:** `2026-08-02-caesar-v1-design.md` §5 (UI architecture) and §7 (frontend testing/CI) — it resolves what §5 left open rather than changing it; everything in §5 stands. · **Consumed by:** Plan 11 (web foundation), whose task decomposition this document is the input to. · **Locked upstream:** the page map, shell A, the run-detail/approval/workflow-editor/tasks screens, and the Terminal visual direction are decided in `docs/brainstorm/2026-08-01-decisions.md` §7 with mockups in `.superpowers/brainstorm/12432-1785604290/content/`. This design does not revisit them.

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

`/approvals` is the slice for three converging reasons: it is the decisions-record phone flagship, it is the only list whose contents the attention stream actually reports on, and it is a first-class list endpoint rather than a nested one — `GET /runs` does not exist at all, runs being reachable only under `/tasks/{id}/runs`. (`GET /tasks` is also a list endpoint; it is not the slice because nothing on the stream moves it, so it would exercise the realtime layer not at all.)

**Deliberately excluded from Plan 11**, each because it belongs to a later row and none because it was forgotten: the assistant ask-bar and its socket (Plan 13 — no placeholder either; a dock with nothing in it is dead config), the ⌘K command palette, the run-detail timeline, the workflow outline editor, the approve screen's diff accordion, any second theme, and offline/service-worker behaviour (the manifest promises installability, not offline).

**Not deferred, despite looking deferrable:** serving the built UI from the Go binary, the SPA fallback, CSP and the security-header middleware. These are Plan 11's, not Plan 14's, because how the bundle is built and where it is served from are constrained by them — self-hosted fonts, same-origin, no third-party script origins. Retrofitting those onto a finished scaffold means rewriting the scaffold.

## 2. Structure and the constitution

Route-centric colocation under TanStack Router **file-based** routing. A component lives beside its only consumer and moves up the tree only when a second consumer appears.

```
web/src/
  routes/            file-based routes; routeTree.gen.ts committed + drift-gated
  api/               schema.d.ts (generated) · client.ts · errors.ts
  realtime/          sse-parser.ts · sse-connection.ts · cache-bridge.ts
  forms/             use-app-form.ts · problem-to-fields.ts · fields/
  ui/                primitives (shadcn-style over Radix)
  styles/            tokens.css · theme-dark.css · theme-light.css
```

**Enforcement is mechanical, and the split is deliberate.** Biome expresses most of the constitution directly: `noBarrelFile` and `noReExportAll` (no barrel files), `noDefaultExport` (named exports), `useFilenamingConvention`, and cross-group import bans via `overrides` scoped by `includes` plus `noRestrictedImports` patterns. GritQL plugins cover any further single-file pattern.

One rule is structurally beyond it. **Consumer-count file placement needs a cross-file import graph**, which no per-file linter has. The backend met this same gap with custom `go/analysis` analyzers; the frontend gets the twin — a small Node checker walking the TS import graph, run as its own `task web:arch` target. The rule is worthless undetected: it only bites when a second consumer appears, which is exactly the moment nobody notices.

**A "consumer" is a direct importing module**, static or dynamic, excluding tests, stories and generated files; the required home of a module with two or more of them is their nearest common ancestor directory. Route-root reachability is the tempting alternative and it is the wrong one — every shared module is reachable from every route, so it would place everything at the top and assert nothing. Naming this now matters because the two definitions disagree precisely on transitive helpers, which is most of what the rule will ever see.

**Non-semantic colors are a separate check that does not belong in the graph.** A hex literal in a `className`, a style object or a stylesheet creates no import edge, so the import-graph checker is structurally blind to it and would pass a codebase full of them. `web:arch` therefore carries two independent analyses: the import-graph rule above, and a token check over component source and CSS.

**That token check cannot be a literal search, and this is the trap it has to be written around.** `<div className="bg-white text-red-500" />` bypasses every semantic token and contains no hex, no `rgb()`, no `oklch()` — nothing to find. Tailwind's built-in palette is the hole, so the fix closes it at the source rather than chasing it: §6's theme resets the default color namespace (`--color-*: initial`) so the only color utilities that resolve at all are the semantic ones, and the checker then validates *names* — utility and custom-property — instead of hunting literals. The mutant fixtures both checks ship with have to include `bg-white` and a palette class alongside the hex literal, an arbitrary-value utility, a named CSS color, and an inline style, because §6's "a new theme is a token file and never a component edit" rests entirely on this check and a guard that passes its own fixture proves nothing.

**`routeTree.gen.ts` is generated, so it is committed and regenerated-and-diffed** in the web lane, exactly as `schema.d.ts` is in the contracts lane. No generated artifact in this repo is trusted to be hand-current.

**The router's type safety is opt-in, and a scaffold that omits it is green and unsafe.** TanStack Router only knows the route tree through a `declare module '@tanstack/react-router' { interface Register { router: typeof router } }` augmentation. Without it, `tsc` passes and every `to` accepts an arbitrary string — verified: `router.buildLocation({ to: '/does-not-exist' })` compiles silently unregistered, and fails `TS2322: Type '"/does-not-exist"' is not assignable to type '"/" | "/approvals" | "." | ".."'` once registered. Since typed navigation is most of why this router was chosen, the registration is a task-1 requirement and the web lane carries a mutant fixture — a deliberate bad `to` that must fail to compile — because the absence of this augmentation is invisible in every other signal CI produces.

## 3. Server state and realtime

All server data flows through TanStack Query over the **generated** client. Hand-written `fetch` calls against the API are banned and the ban is a lint rule, not a convention. URL-as-state for every filter and tab via Zod `validateSearch` with `.catch`.

Realtime is three layers, separated so each is testable at a different level and none can hide a bug in another:

```
sse-parser.ts      Pure incremental SSE framing → typed frames. No I/O.
sse-connection.ts  fetch + ReadableStream; backoff; heartbeat watchdog.
                   Owns reconnection, never correctness.
cache-bridge.ts    Typed frame → query invalidation. No network, no writes.
```

**`fetch` + `ReadableStream`, not `EventSource`.** `EventSource` reconnects on a fixed interval the page cannot control, surfaces errors opaquely, and hides the HTTP status — so it can deliver neither §5's "exponential backoff" nor its "heartbeat watchdog surfacing staleness honestly", and it cannot distinguish a 401 from a dropped tunnel. The cost is re-implementing `Last-Event-ID` resume by hand, which this design wants explicit anyway.

**The wire contract is one frame shape, and the client parses one shape.** This is the server's deliberate choice, not an omission: `backend/internal/realtime/stream.go` emits the opening snapshot as ordinary event frames — *"Each event is its own frame. The snapshot is the most recent N matching events, ascending … not a projection — so the client parses one shape all stream."* Every frame is `{id, run_id, kind, payload, occurred_at}` with `id:` a monotonic int64 and `event:` carrying the dotted `kind` (`gate.waiting`, `gate.decided`, `publish.*`, `run.*`). There is no snapshot discriminator and no client-visible watermark, so a client cannot be built on one.

**Therefore the bridge invalidates; it never reconstructs.** A frame in the attention kind-set invalidates the `/approvals` query and the badge follows from the refetched list. This is not a fallback for a missing discriminator — it is the only correct reading of what the server sends, for two reasons the server states outright. The opening frames are the most recent 500 events, *not* a projection of open gates, so a gate still waiting from before that window simply is not on the stream and cannot be derived from it. And the resume path deliberately re-sends a backlog rather than risk a gap — "one duplicated backlog rather than a gap", the same direction taken twice in `prime` — so duplicate delivery is the documented normal case, not an anomaly. Invalidation is idempotent under both; `setQueryData` from event payloads is wrong under both, and is banned. Bursts are coalesced with a short trailing debounce so a run emitting twenty events costs one refetch.

REST is the source of truth for list *and* badge. The stream's entire job is to say "something you are showing has changed" — never to say what it changed to.

**Staleness is stated, never implied.** The server heartbeats an idle stream every 20s (`config.Realtime.HeartbeatInterval`) as an SSE **comment** line, `: hb`, which the parser must consume as a keepalive rather than skip as junk. A client watchdog at 45s flips the shell's connection indicator to "reconnecting". The UI never renders stale data as live — a frozen badge that looks authoritative is worse than an honest one that admits it lost the stream.

**Authorization arrives in-band, and `forbidden` is ambiguous on purpose.** `NewWriter` commits the 200 with the first byte, so a grant lost mid-stream can no longer be an HTTP status; the server instead writes `event: forbidden` / `data: {"error":"forbidden"}` and closes. That frame deliberately carries **no `id:`** — it is not an event and a client that adopted it as a resume point would store a position naming nothing. So `forbidden` is its own parser variant, and it always ends the connection.

What it does **not** do is tell the client what happened, and the backend says so outright. Three different conditions send those identical bytes: a real revocation; an empty scope at the 30s re-check, which is also exactly what a *deactivated* principal resolves to and also what an active member with zero grants resolves to; and **a grants-store outage**, after three consecutive resolver failures — roughly 90 seconds — where the comment is explicit that "the client gets the same frame a revocation would send". Treating the frame as proof of lost authorization is therefore wrong in two of the three cases: a member with no grants would be shown an authorization error instead of an empty inbox, and a database blip would leave the tab permanently realtime-dead, falsely claiming a permission problem, while REST worked fine the whole time.

**So the client probes rather than concludes.** On `forbidden` it stops the stream and issues one `GET /api/v1/auth/me`, which returns the user together with their grants and is the only thing on the wire that can decide this:

- **401** — the session is gone. Redirect to `/login` (§4).
- **200 with grants** — the frame was not about authorization at all; it was a resolver outage. Evict nothing, reconnect under ordinary exponential backoff.
- **200 with no grants** — an active principal who legitimately sees nothing. Render the empty inbox, not the authorization panel, and do not reconnect until the user acts.

This costs one request on a rare path and is the difference between an honest UI and a confidently wrong one. Any other close — slow consumer, shutdown — is an ordinary reconnect with `Last-Event-ID`.

**The stream cannot be the only refresh path, because a narrowing is silent.** Losing one grant out of several does not close the stream and emits no event: the server keeps it open, drops frames from the departed project, and its own test asserts that "a narrowing is not a close". Grant deletion writes an audit record and no `run_event`, so nothing on the wire will ever mention it. A cache refreshed only by attention frames would therefore keep rendering an approval the user can no longer act on, indefinitely, with no bug anywhere in the client. Authorization-scoped queries consequently carry a bounded refresh floor — refetch on window focus plus a bounded interval — so the exposure is a stated number of seconds rather than an unbounded one. The server still rejects every action on that row; what is at stake is the UI telling the truth, which §4 already refuses to compromise in the other direction.

## 4. Errors

The `problem+json` envelope becomes a typed `ApiError` carrying `type`, `title`, `status`, `detail`, `correlation_id`, and the backend's field-error list. It has exactly three consumers, and they are the whole error design:

1. **Forms** map field errors onto fields by **flat field name** (§5). Not by JSON pointer: `xerrors.FieldError` is `{field, message}` and the name is a plain identifier, so a pointer-shaped mapper would match nothing on every real validation failure.
2. **A 401 from a call that required an existing session** redirects to `/login`. **The authentication calls themselves are exempt, and the exemption is explicit rather than emergent** — `POST /auth/login` answers 401 for bad credentials, so a blanket redirect would fire on the single most common login outcome, remount the route, and erase the form-level error §5 requires it to show. The exemption is a property of the request, not a check the interceptor infers from the current route, and its test asserts that bad credentials leave the user on the mounted form with the problem detail and correlation id rendered and no navigation at all. A 403 renders a typed "you do not have this grant" panel rather than an empty list — an authorization failure that presents as "no results" is a lie the UI tells on the server's behalf.
3. **`correlation_id` is rendered on every error surface.** §4 makes it the only thread between a user's complaint and a server log line; a UI that swallows it severs that thread permanently.

**The session is resolved before the shell renders, and `/auth/me` is what resolves it.** The design as first written specified only one auth transition — a redirect *after* a request came back 401 — which leaves every stub route in §1 rendering the full shell to an unauthenticated visitor, because a stub makes no request to be rejected. So the protected routes sit under a layout route whose loader resolves an `auth/me` query first: no session redirects to `/login`, and an existing session at `/login` redirects inward. `POST /auth/login` returns only the user, while `GET /auth/me` returns the user *and* their grants — so login success invalidates the `auth/me` query rather than trying to hydrate it from the login response, which would seed a cache entry missing the grants the rest of the app reads. Three tests pin it: direct unauthenticated navigation to a stub route, reload with a valid cookie, and navigation on an expired session.

**`ApiError` is built from the HTTP status, not from the generated response union.** The OpenAPI document currently declares only `200` and `400` on the endpoints this slice touches, while `POST /auth/login` really answers `401` for bad credentials and `429`-class for lockout (`identity/login.go` classifies both). A client that switched on the generated union would therefore have no branch for the single most common login outcome. The status is read from the `Response`, the body is parsed as `problem+json`, and the union is used for success shapes only.

That gap is worth closing rather than only routing around, so **Plan 11 carries one bounded contracts task**: document the `401`/`403`/`429`/default problem responses on the endpoints in the slice, and mark the request and response properties it consumes as required, since `openapi-typescript` renders an unmarked property optional and a login body whose `email` and `password` are both optional is not a contract the form layer can lean on. It runs in the existing contracts lane, which already regenerates and diffs `schema.d.ts`, so the change is verified the same way every other generated artifact is. It is deliberately scoped to the slice — widening it to every endpoint is Plan 12's to do as those screens arrive.

Unclassified failures surface as permanent, matching the backend's safe direction.

## 5. Forms — built before any feature form

The shared typed form abstraction lands before any form that has a feature behind it. This ordering is the explicit lesson of the 72-hand-wired-forms survey in decisions §10, and inverting it is how that outcome happens again.

`useAppForm` composes react-hook-form + Zod resolver + the generated request types, with one field-component registry, uniform error rendering, and a submit orchestrator that catches `ApiError` and calls `setError` per field name (§4 — flat names, not pointers), with a form-level fallback for problems that name no field. The 401 that login actually returns for bad credentials carries no field at all, so that fallback is on the primary path rather than the edge, and the login task's tests cover it as such.

**The login form is its first consumer.** That is the point of putting it in Plan 11: the abstraction ships having satisfied a real endpoint with real validation failures, not a fixture. Its flagship consumer remains the Plan 12 workflow editor, whose step-card fields are generated from the engine's step schemas so that new step types need no UI code.

## 6. Theming

Semantic OKLCH custom properties, `data-theme` on the root element, system preference as the default, exactly one dark (flagship) and one light shipped.

The dark theme's values derive from the approved Terminal mockup — `#0d1117` ground, `#161b22` surface, `#21262d` border, `#c9d1d9` body, `#58d6a3` accent — converted to OKLCH and named semantically. Light is its twin over the identical semantic names.

**Tailwind's default palette is reset, not merely unused.** `--color-*: initial` in the theme block removes the built-in namespace, so `bg-white` and `text-red-500` stop resolving and the semantic tokens are the only colors the codebase can express. This is what makes **a new theme a token file and never a component edit** true rather than aspirational: leaving the default palette in place would let a component couple itself to Tailwind's colors while passing every check §2 describes, and the third theme would then be a component edit after all.

**Typography is hybrid, not the mockup's full monospace.** Spec §5 pins monospace for data, logs, IDs and statuses with phone-legible body text, and the mockup's all-mono treatment is a poster for the aesthetic rather than a specification of it. JetBrains Mono (the mockup's own face) carries data; Inter carries body and labels. Both self-hosted variable fonts — no third-party font origin, which is also what makes the §8 CSP simple.

Mobile-first structurally: bottom sheets as the default modal on phone, safe-area insets, 44px minimum targets, squarish corners, console-honest density, card-first lists with desktop table projections, and a web app manifest.

## 7. Testing and CI

| Level | What it covers |
|---|---|
| Vitest, pure | SSE frame parsing — split chunks, interleaved `: hb` comment heartbeats, a `forbidden` frame with no `id:`, and a replayed backlog that must not double anything — plus `problem+json` → field mapping and the token contract |
| Vitest, transport | `sse-connection.ts` against raw chunked `ReadableStream` responses with an injected clock: watchdog resets on a `: hb` comment and fires without one, backoff intervals, `Last-Event-ID` sent on resume and advanced only by frames that carry an id, ordinary close reconnects, `forbidden` does not — and each of the three `forbidden` probe outcomes of §3 reaches its own cache effect |
| Vitest + Testing Library + MSW | Generated client, Query wiring, form submit and error paths, session bootstrap and the three route-guard cases of §4, shell navigation, theme switching |
| Playwright, mobile-viewport project, **real binary** | login → shell → approvals list loads → a gate opened through the production path moves the badge with no user action |

MSW intercepts at the network level so the generated client is exercised rather than stubbed. The transport row exists because §3's connection carries five behaviours — backoff, resume, watchdog, terminal-vs-transient, and cache effect — that a parser test cannot see and the one E2E happy path never exercises: a connection that reconnected after `forbidden`, forgot its `Last-Event-ID`, or failed to reset the watchdog on a comment heartbeat would pass every other check in this table.

"No user action" rather than "no refetch": §3's bridge reaches the badge by invalidating the approvals query, so a refetch is the mechanism and forbidding it would assert against the design. What the spec proves is that the client learns of a server-side change without the user touching anything — which is the property the badge exists for. It asserts on the rendered badge, never on request counts.

**The fixture opens a real gate, and this is the whole difficulty of the spec.** `GET /approvals` is `ListWaitingGates` — `SELECT … FROM phase_runs WHERE pr.status = 'waiting_decision'`. It does not read `run_events` at all. So appending an event and nothing else invalidates the query, triggers the refetch, and returns the identical list: the badge does not move and the spec proves nothing while looking like it passes. Under the discarded `setQueryData` model an injected event *would* have moved the badge, which is worth saying plainly — §3's correction is what turned this from a passing test into a visibly broken one.

The fixture therefore transitions a `phase_run` into `waiting_decision` **through the production write path**, so that the authoritative row and its `gate.waiting` event land in the one transaction that `appendEvents` already guarantees. Raw SQL insertion is banned outright and the plan says so: `appendEvents` allocates ordered ids under the counter lock, writes the outbox envelope and issues the `NOTIFY`, so a hand-written `INSERT` desynchronises the id counter and leaves later resume behaviour broken in a way no assertion in this lane would catch. The spec's four beats are explicit — REST returns N; the backend transaction opens the gate; SSE invalidates; REST returns N+1 with no user action — because a test that skips the second beat is the failure mode this whole section exists to prevent.

If the production path turns out not to be reachable from the E2E harness, the fallback is the existing fake-CLI runner machinery (`caesar-fakecli:dev`, already built by `task image:build` and already driving `backend:test:runner`) rather than a shortcut, and that is a task-sizing question for the plan, not a licence to inject.

**The Playwright lane drives the real thing**: Postgres up, migrations applied, first admin created through `caesar admin bootstrap`, Vite build embedded into a `-tags embedui` binary, binary served, specs run against it. This is the most expensive task in Plan 11 and the only construct that can prove the SSE-into-cache path at all — a mocked stream proves the parser and nothing about the server's actual framing, its snapshot-then-deltas splice, or its resume. Plan 12 then adds specs to a lane that already works.

A new `web` job joins CI's `ci-ok` composite check: `biome ci` · `task web:arch` · `tsc --noEmit` (with the route-inference mutant of §2, since §9 forces `skipLibCheck`) · `vitest run` · `routeTree.gen.ts` regen-diff · `vite build` · `task web:e2e`.

## 8. Serving and hardening

**The embed is behind `//go:build embedui`.** A twin file behind `//go:build !embedui` compiles in its place and returns a typed `problem+json` at non-API routes. Every path that produces a shippable image builds with the tag; `task backend:test` does not, so the backend lane stays Node-free and fast, and `//go:embed` never fails to compile against an absent `dist`.

This shape is chosen over a committed placeholder `dist` specifically for its failure mode. A placeholder that a forgotten `vite build` leaves in place ships a stub page silently; a tagless binary says out loud that no UI is embedded, which is the same rule the architecture tests already enforce on the backend — stubs return typed `NotImplemented`, never fake success.

**The image assertion has to follow the artifact that is actually published, and today's CI shape would let it not.** `publish` runs after `ci-ok` and rebuilds from `context: .` on its own; no image or digest crosses that boundary. So an `embedui` wired only into `task image:build` and the E2E lane would leave every required check green while the pushed image carried a UI-less binary — the precise failure this section exists to make impossible. Plan 11 therefore builds once, tests **that digest**, and promotes the same digest to the release tags.

The assertion needs an oracle from outside the image, or it grades the image against itself. Reading a build stamp out of the candidate's own `index.html` and confirming the asset it names exists is satisfied perfectly by a stale-but-consistent build — the exact thing being tested for. So the oracle is **the commit SHA CI already knows**, and the plan names its whole path: workflow input → Docker build argument → a Vite `define` constant baked into the bundle → served in the document → compared by the test against the SHA the workflow holds independently. The check is then `200`, `text/html`, and *this commit's* stamp, rather than the typed "ui not embedded" problem document. "Some HTML" is too weak on its own — the SPA fallback would satisfy it from almost any state.

Because the embed is a build tag rather than a task argument, the Dockerfile carries it by default; a build that forgets to opt out is the safe direction, and the tagless twin exists for the lanes that deliberately do.

SPA fallback serves `index.html` for unknown non-`/api` paths. Production CSP is `script-src 'self'`, and the self-hosted fonts of §6 mean no third-party origins need allowing; the dev server relaxes it for HMR.

**The plan verifies this rather than reasoning about it.** Vite's default module-preload polyfill is sometimes assumed to break a `script-src 'self'` policy; it does not — it is injected as an import into the HTML entry's proxy module, so it lands in bundled application JavaScript that the policy already permits. That is a reason not to pre-emptively disable it, and no reason at all to trust the outcome: the plan asserts the built `index.html` contains no inline `<script>`, because the assertion holds whatever any given Vite version chooses to emit and the reasoning does not. A hash-based `script-src` is the fallback if some inline emission ever does survive. Standard security headers and HSTS land in the same middleware, joining the existing `appOrigin` check on unsafe methods.

**Same-origin by construction**, so no CORS and no reverse-proxy configuration. The graduation path to separate deployment stays cheap through a configurable client base URL, and is not built in V1.

## 9. Dependency versions

Every version below is the newest stable release as of 2026-08-15 **that the rest of the set can actually resolve against**, and each is **pinned exactly** — no `^`, no ranges. Three entries are deliberately not the `latest` dist-tag, and each says why below; taking `latest` blindly for those produces a set that does not install. A foundation plan whose dependencies float cannot reproduce its own CI failures, and every plan after this one inherits whatever drifted. `web/package.json` already exists, carrying `openapi-typescript` for the contracts lane; Plan 11 extends that file rather than starting a new one.

This table is verified, not asserted: the full set resolves clean from a bare manifest (`npm install`, exit 0, no `ERESOLVE`), and the TypeScript and TanStack entries below were each established by reproducing the failure that rules the alternative out.

| Role | Package | Version |
|---|---|---|
| Runtime | Node | 24.19.0 — exact, in `.nvmrc`; see below |
| Framework | `react` / `react-dom` / `@types/react` / `@types/react-dom` | 19.2.8 / 19.2.8 / 19.2.18 / 19.2.4 |
| Language | `typescript` | 5.9.3 — see below |
| Build | `vite` / `@vitejs/plugin-react` | 8.2.1 / 6.0.5 |
| Routing | `@tanstack/react-router` / `@tanstack/router-plugin` | 1.170.29 / 1.168.32 |
| Server state | `@tanstack/react-query` | 5.101.4 |
| Forms | `react-hook-form` / `@hookform/resolvers` | 7.85.0 / 5.8.0 |
| Schemas | `zod` | 4.4.3 |
| API types/client | `openapi-typescript` / `openapi-fetch` | 7.13.0 / 0.17.0 |
| Styling | `tailwindcss` / `@tailwindcss/vite` | 4.3.3 / 4.3.3 |
| Primitives | `radix-ui` / `vaul` | 1.6.7 / 1.1.2 |
| Icons | `lucide-react` | 1.31.0 |
| Lint + format | `@biomejs/biome` | 2.5.8 |
| Unit test | `vitest` / `@vitest/coverage-v8` / `jsdom` | 4.1.10 / 4.1.10 / 30.0.1 |
| Component test | `@testing-library/react` / `-dom` / `-user-event` | 16.3.2 / 10.4.1 / 14.6.4 |
| Network mock | `msw` | 2.15.0 |
| E2E | `@playwright/test` | 1.62.1 |
| Fonts | `@fontsource-variable/inter` / `-jetbrains-mono` | 5.3.0 / 5.3.0 |

Tailwind is on the list because §2 says "shadcn-style" and §6 specifies semantic custom properties: v4's CSS-first `@theme` is that model natively, so this follows from decisions already taken rather than adding one. It is named here because leaving the CSS engine implicit is how a plan author invents one.

**The primitives are Radix, not Base UI.** `@base-ui-components/react` has never shipped a 1.0 — it has sat at `1.0.0-rc.0` since 2025-12-04 with no release in the eight months since — and `ui/` is the layer every screen in the program stands on. `radix-ui` is stable, still shipping (1.6.7, with 1.7.0 release candidates in July 2026), and is what shadcn/ui is actually built over, so "shadcn-style" becomes the literal path rather than an analogy. `vaul` supplies §6's phone bottom sheet over the same Radix Dialog primitive. Base UI is by the same authors and is where the ecosystem is drifting; the migration is a rewrite of `ui/` and nothing above it, which is precisely the property §2's structure exists to give.

**TypeScript is 5.9.3, and this is a hard constraint rather than conservatism.** `openapi-typescript@7.13.0` declares `peerDependencies: {typescript: "^5.x"}`, and no published version of it declares anything wider. Pinning TypeScript 7.0.2 — or 6.0.3 — fails `npm install` outright with `ERESOLVE`, which `--legacy-peer-deps` would only silence at the cost of the `npm ci` the contracts lane already runs. So the newest TypeScript is not installable alongside the generator this repo's contract pipeline is built on. The alternatives are worse than the pin: isolating the generator in a second workspace to run TS 5 buys a split toolchain for a compiler the rest of the stack has no proven need for, and TanStack Router's inference is exactly the workload a reimplemented checker is likeliest to diverge on anyway. TypeScript 7 becomes a one-line change whenever `openapi-typescript` widens that peer range; until then it is not a decision this plan gets to make.

**`skipLibCheck` is `true`, as a stated decision rather than a copied default.** At these exact versions the router plugin resolves `@tanstack/router-core@1.171.24`, whose own shipped declarations do not typecheck: `tsc --noEmit` with `skipLibCheck: false` fails on `router-core/dist/esm/ssr/types.d.ts` with `TS2339: Property '__beforeLoadContext' does not exist on type 'MakeRouteMatch'`. That is a defect in a dependency's `.d.ts`, not in this codebase, and no combination of the current published versions avoids it. Turning `skipLibCheck` on is what the ecosystem does by default and what the failure forces here — but it weakens the §7 gate, so §2 compensates where it actually matters: the `Register` augmentation plus a route-inference mutant keep application-level route typing falsifiable, which is the property `tsc --noEmit` was in that list to protect. Verified in both directions: valid code compiles clean, the mutant fails.

**`react-hook-form` 8 is in beta** (`8.0.0-beta.3`). §5's `useAppForm` wraps v7's stable API. The wrapper is what makes that survivable: a v8 migration touches one file rather than every form, which is the same argument §5 makes for building the abstraction first.

**Pinning is enforced, and Node is pinned to a patch.** The web lane fails on a `package.json` version string containing `^`, `~`, or `*`, for the same reason `routeTree.gen.ts` is regen-diffed: a rule nobody checks is a rule that has already been broken.

Node is the version most easily left half-pinned, so it gets stated twice over. A bare major is not a pin: `setup-node` resolves `24` to whatever patch it finds, and `jsdom@30.0.1` requires `^22.22.2 || ^24.15.0 || >=26.0.0`, so an earlier 24 satisfies the major and fails the dependency — a CI-only failure that reproduces nowhere. The exact version therefore lives in **one** authoritative file, `.nvmrc`, and every lane reads it via `node-version-file` rather than restating a literal. `engines` stays as documentation, because npm only warns on it. The contracts lane currently pins `node-version: '20'`, which is EOL; Plan 11 moves it and every new web lane onto that one file together, because two lanes on different majors is a class of failure that only ever appears in CI.

## 10. Residuals

Named rather than hidden, in the house style:

- **`__Host-` cookies require `Secure`.** Browsers treat `http://localhost` as a secure context, so the dev loop works, but the Vite dev server must proxy `/api` rather than cross origins — a cross-origin dev setup would silently drop the session cookie. Plan 11 verifies this in the dev-proxy task rather than assuming it.
- **Playwright's real-backend lane is the plan's slowest CI job** and its most likely source of flakes, since it composes Postgres, a migration, a bootstrap, two builds and a server start. It is worth the cost for exactly one spec; growing it carelessly in Plan 12 would not be.
- **Radix is the primitive set the ecosystem is drifting away from**, even though it is the one that has actually shipped (§9). The bet is that a stable dependency with a known successor beats a pre-1.0 one that has been silent for eight months, and that §2's structure keeps the eventual migration inside `ui/`. If Base UI ships 1.0 during Plan 12, revisiting is a `ui/` rewrite and not a program-wide one — which is the whole reason that boundary exists.
- **The wire cannot express a scope change, and Plan 11 does not fix that.** The stream has one authorization signal, `forbidden`, and it is overloaded across revocation, deactivation, a legitimately empty scope and a grants-store outage — while a *partial* narrowing produces no signal at all. §3 answers both with client-side work: a probe to disambiguate the frame, and a bounded refresh floor to cap how long a narrowing can go unnoticed. That is the right V1 answer because it needs no change to a merged plan's realtime layer and because the server enforces authorization on every request regardless. It is not the right permanent answer. A later plan should give the stream a distinguishable no-id control frame — scope-changed versus unavailable versus terminal — at which point the client's probe becomes redundant and its refresh floor can relax. Recording it here so that the next person to touch `internal/realtime` finds the reason rather than rediscovering it.
- **The contracts task in §4 touches a merged plan's output.** Documenting `401`/`403`/`429` and marking required properties changes `backend/api/openapi.json` and regenerates `schema.d.ts`, both of which Plan 10 produced. It is additive — no existing response shape changes — and the contracts lane's regen-diff is what proves that. Scope discipline is the risk here, not correctness: the task covers the slice's endpoints and stops.
- **The badge fixture is the plan's least-sized task.** §7 pins what it must do — open a real gate through the production write path — but not how much scaffolding reaching that path costs from an E2E harness, and the honest answer is that nobody knows until the plan's author looks. It is named here rather than estimated. What it is *not* is a licence to fall back to injecting an event: that is the thing §7 proves does not work.
