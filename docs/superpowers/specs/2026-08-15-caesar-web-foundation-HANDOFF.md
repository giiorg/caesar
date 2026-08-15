# Handoff — Plan 11 (web foundation): spec is done, write the plan

**Written 2026-08-15. Verify this against `git log` before acting on it — a stale handoff has cost a full session on this project before.**

## State

Branch `caesar-11-web-foundation`, based on `main` at `80c0582`. `main` is untouched.

| Commit | What |
|---|---|
| `d4d0da4` | The design doc, first draft (previous session) |
| `b7b0e0c` | Corrected against the real backend after three adversarial review rounds |
| _(this commit)_ | Roadmap row 10a + the residual pointing at it |

**The next action is `superpowers:writing-plans` against `docs/superpowers/specs/2026-08-15-caesar-web-foundation-design.md`.** Nothing else is in flight. No code has been written; `web/` still contains only `package.json` (with `openapi-typescript` alone) and the generated `src/api/schema.d.ts`.

## What happened in this session, and why it matters to the plan author

The spec was reviewed three times by codex (`artifact-review`, fresh thread each round, grounded in the repo). Every round returned NO-SHIP. **Rounds 1 and 2 found that the spec described a client the backend cannot support** — not style problems, contradictions of fact. Each was verified against source before being applied, and a few of codex's prescribed *fixes* were rejected in favour of cheaper ones.

The design doc now records the corrected decisions, but the plan author should know which parts were rewritten under fire, because those are the parts where an "obvious" simplification will re-break something:

1. **§3 realtime.** The original design assumed a snapshot/delta discriminated union. There is none — `internal/realtime/stream.go` emits the opening snapshot as ordinary event frames and says so in a comment. The bridge **invalidates and never reconstructs**; `setQueryData` from event payloads is banned. If a task starts reading event payloads into the cache, that is the regression.
2. **§7 the E2E fixture.** `GET /approvals` is `ListWaitingGates` — it reads `phase_runs.status`, never `run_events`. Injecting an event cannot move the badge. The fixture must open a real gate through the production write path; raw SQL insertion desyncs the event-id counter and outbox. **This is the least-sized task in the plan** and the one most likely to blow up an estimate.
3. **§3 `forbidden` is overloaded** across revocation, deactivation, an empty scope and a grants-store outage, and a *partial* narrowing emits nothing at all. The client probes `GET /auth/me` to disambiguate, plus a bounded refresh floor. The permanent fix is **roadmap row 10a**, filed this session; do not solve it inside Plan 11.
4. **§4 session bootstrap** was missing entirely — stub routes rendered the shell to anyone. There is now a protected-route layout resolving `auth/me`, and auth calls are exempt from the global 401 redirect (login's own 401 would otherwise erase the form error).
5. **§8 the image assertion** — `publish` rebuilds independently of `ci-ok`, so the tested artifact never reached the registry. Build once, test that digest, promote that digest, and compare against the commit SHA as an external oracle.
6. **§9 versions** — all pinned exactly and **verified to resolve** from a bare manifest. Two traps: `openapi-typescript` peers on `typescript@^5.x`, so TypeScript 7 fails `ERESOLVE` outright (pin is 5.9.3); and `skipLibCheck` is forced by a broken `@tanstack/router-core` `.d.ts`, compensated by the mandatory `Register` module augmentation plus a route-inference mutant fixture. **Without that augmentation TanStack's route typing silently does not exist** — `tsc` passes and every `to` accepts any string. Verified both ways.

## Decisions the user made this session

- **Primitives: Radix (`radix-ui` 1.6.7) over Base UI**, which has sat at `1.0.0-rc.0` since 2025-12-04.
- **Stop review at round 3** and move to the plan. Rationale: rounds 1–2 found spec errors, round 3 found a backend limitation; further spec-stage rounds trend toward nitpicks. External grounding — the plan's own tests and typecheck — is the better next filter.
- **The realtime wire gap gets its own plan row (10a)** rather than living as a residual note.

## For the plan author

Read §§1–10 of the design doc in full; it is 220 lines and every section is load-bearing. Then note:

- **The governing constraint is §1**: Plan 11 ends with one thin slice — `/login` → shell → `/approvals` — running against the real backend, with the CI lane that proves it inside the plan. Every foundation piece gets exactly one real consumer.
- **One bounded backend task exists** (§4): document `401`/`403`/`429`/default on the slice's endpoints and mark the consumed properties required, in the existing contracts lane. It touches Plan 10's output; it is additive and scoped to the slice.
- **The pre-flight lesson applies.** A long plan on this project once hit ~20 execution blockers that five design rounds had missed. Scan the whole plan for executability before executing it.
- Roadmap rules: never renumber; each plan ends green under the full gate (`task backend:ci` + `contracts:check`, not a hand-picked lane list).
