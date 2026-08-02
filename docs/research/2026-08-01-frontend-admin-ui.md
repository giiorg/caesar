# bitnet-frontend-admin — UI taste survey (reference for caesar's web app)

*Research artifact, 2026-08-01. Produced by a read-only survey agent over `/Users/sofo/Projects/bitnet/repos/bitnet/bitnet-frontend-admin` (1218 commits, HEAD 2026-07-29; the similarly-named repo one level up is a stale 122-commit fork). ~203k LOC, 796 .tsx, 290 colocated test files, 52 routes.*

## Stack

React 19.2 with the **React Compiler** enabled (manual `useMemo`/`useCallback` discouraged). Vite 8, TypeScript 5.9 strict, Bun as runtime/package manager. Tailwind **v4.3 CSS-first** — no `tailwind.config.js`; the entire design system lives in `src/index.css` (680 lines). shadcn/ui "new-york" **on Base UI** (Radix still present mid-migration). lucide-react icons. Inter + Noto Sans Georgian + JetBrains Mono via Google Fonts CDN `@import` (render-blocking — self-host in caesar). Also: Apollo Client 4, TanStack Router + Table, react-hook-form, Zod 4, Zustand 5, CASL, i18next, Recharts, sonner, dnd-kit, decimal.js, Sentry.

## Structure

TanStack Router, **code-based** — all 52 routes in one 884-line `routes.tsx` (the one place colocation breaks; caesar: use file-based routes). Single shell layout: SidebarProvider + AppSidebar + SiteHeader + Outlet; command palette (⌘K), AI-assistant drawer, passkey nudge, toaster. Navigation tree data-driven (456 lines): icon, CASL `(action, subject)` permission, `canAccess` predicate, `helpGlossaryTerm` per item. **The architecture constitution is codified law**: CLAUDE.md + `docs/standards/` (6) + ADRs (4) — page groups never import from other groups; `app/` is the only directory that may import `pages/`; file placement by consumer count; **no barrel files, named exports only, one component per file**; tests colocated; all strings through i18next.

## Data layer

GraphQL only via Apollo 4; schema **owned by the Go backend and mirrored** (`schema:sync`; never hand-edit); client-preset codegen with fragment masking. `apollo-client.ts` (718 lines) is the most sophisticated file — link chain `errorLink → stepUpAutoAttach → stepUp → tokenRefresh → auth → http`: JWT access+refresh in a multi-account localStorage bundle, proactive refresh in a 30s window, singleton refresh promise, bounded retry on server-told `UNAUTHENTICATED`, and a **mid-session re-auth modal that preserves the operator's place** instead of bouncing to /login. `fetchWithTimeout` converts black-holed connections into catchable aborts. Every non-obvious decision carries a comment naming the failure it prevents.

**Step-up authentication is the standout pattern:** the backend declares `@stepUp` (GraphQL directive) or registers a REST path; codegen fetches the registry and emits lookup tables; Apollo middleware and the CSV helper auto-attach the WebAuthn ceremony at runtime. Adding a step-up'd mutation = backend change + codegen — zero frontend wiring. **Directly the right shape for caesar's approvals: an approval requirement is a backend fact, never a frontend `if`.**

**Realtime is the weak spot:** GraphQLWsLink fully wired but **zero subscriptions exist** — dead plumbing; actual freshness = pollInterval at 5 sites + manual refetch in 134 files. The one real streaming implementation is the **AI assistant**: own WebSocket, JWT in query string, typed `ServerFrame`/`ClientFrame` wire protocol, deliberately dumb socket wrapper with reconnect policy owned by the Zustand store, streaming bubbles, a `block-registry` for server-driven UI blocks, confirmation cards for pending tool calls. **That subsystem is caesar's realtime + assistant reference.**

## State

Four tiers: Apollo cache (server state; `cache-first` default + hand-maintained keyArgs — both wrong for live process data, documented footgun); Zustand (7 single-concern stores: auth, UI, assistant, step-up, reauth, palette, sidebar); **table state in URL search params** validated by Zod with `.catch(undefined)` so stale bookmarks degrade instead of throwing (ADR-004); CASL authorization in practice wrapped by feature-scoped permission hooks (CASL itself vestigial — one direct import; caesar: pick one mechanism).

## Design system

All color **OKLCH** with a semantic token layer: surfaces, primary/secondary/muted/accent, semantic states each with `-foreground` and `-strong`, an 11-step chart ramp, 7-step shadow scale, typography scale tuned for admin density, and **z-layer tokens with a documented ordering rationale** (popover must outrank dialog — the comment names every screen that broke). Dark mode is a full re-authored `.dark` block, not an inversion; theme flip freezes CSS transitions for one frame to avoid a mixed-theme midstep. Policy doc: light-first, dark-available, tokens before raw values, accessibility as contract.

**Mobile reality check: desktop-only with an accidental mobile veneer.** 320 of 400 page files contain no responsive prefix; `sm:` (640px — a tablet decision) does most of the remaining work. Working on phone: the stock drawer sidebar, a `renderMobileCard` table fallback **passed at 1 of 25 call sites**, one chart height swap. No bottom nav, no `side="bottom"` sheets, zero `safe-area-inset`, no manifest/service worker, no mobile viewport in Playwright, 28px tap targets against a 44px minimum, and the assistant FAB is `hidden md:inline-flex` — **unreachable on a phone**.

## Forms and tables

**No shared form abstraction and it shows**: 72 hand-wired `useForm` sites; only a presentational field wrapper + a GraphQL fieldViolations→setError mapper. Two exceptional subsystems though:
1. **`src/shared/lib/form-logic/` is a real DSL** — a hand-ported parity twin of a Go evaluator: 5-shape JSON AST, closed operator set, decimal.js everywhere, Kleene three-valued logic, asymmetric failure modes (unevaluable visibility fails safe to hidden; unevaluable requiredness fails open to not-required), and **a 111-case conformance corpus vendored from the Go repo with a CI drift check**. The discipline (shared AST + cross-language conformance corpus) is directly applicable to caesar's workflow definitions if the TS side ever evaluates them.
2. **`src/pages/compliance-questionnaires/` is a full visual definition-builder UI**: 21-type item registry with per-type Zod editor schemas, dnd-kit canvas, a logic-builder editing the condition AST in place, validation editor, translations tab, versions tab, publish panel with readiness checklist, live preview in desktop and mobile skins. **The prior art for caesar's workflow editor.**

Tables: one 721-line wrapper over TanStack Table used *minimally on purpose* — core row model only, **no internal state** (URL owns it); per-column skeletons via ColumnMeta; `getRowHref` so ⌘-click works; interactive-target click guard; offset and cursor pagination (cursor stack in the URL so back/forward work); CSV export with auto step-up; composable filter-chip bar with **saved views**.

## Quality

**Biome 2.4 via Ultracite — no ESLint/Prettier**; PostToolUse hook auto-fixes on edit. CI gate = composite `bun run check`: typecheck, lint, no-mock-in-production, codegen drift, operation validation, **and local-schema-matches-deployed-schema**. Vitest 4 + Testing Library + **MSW at network level**, factories, accessible queries mandated over test-ids. Playwright 1.60, 24 e2e specs incl. a **virtual authenticator fixture for passkey flows**. No Storybook.

## Verdict for caesar

**Keep:** the constitution itself (copy the standards/ADR discipline on day one, before the first feature); URL-as-state (matters *more* on mobile); the step-up auto-attach registry shape for approvals; codegen + schema drift CI gates; the OKLCH token layer incl. z-layer rationale and theme-flip freeze; the audit ceremony (form captures *what*, confirmation modal captures *why* + per-field diff — works as a mobile bottom sheet); Biome/Ultracite + composite check as definition of done; the questionnaire builder + form-logic conformance-corpus discipline as workflow-editor prior art.

**Upgrade/replace:** mobile — start clean, structural not optional (44px targets, bottom nav, `side="bottom"` sheets default, safe-area, `viewport-fit=cover`, manifest, mobile viewport in Playwright CI; an *available* mobile fallback gets skipped 24/25 times); realtime — model on the assistant subsystem (dedicated socket, typed frames, store-owned reconnect) or real subscriptions, never polling-as-realtime, and don't default live data to `cache-first`; build the typed form abstraction first; **invert the table story** (card-first with desktop table projection — on a phone an approvals queue is a list of cards); pick one authorization mechanism; file-based routing; self-host fonts; treat assistant + approvals as the phone-native surface (the exact inversion of this codebase).

**Key paths:** `CLAUDE.md`, `docs/standards/`, `docs/adr/`, `src/shared/lib/apollo-client.ts`, `src/index.css`, `src/shared/lib/ai-assistant/`, `src/shared/lib/form-logic/`, `src/pages/compliance-questionnaires/`, `src/shared/components/data-table.tsx`, `src/app/routes.tsx`, `src/app/navigation.ts`.
