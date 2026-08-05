# caesar — subscription-first personal-mode auth (V1 amendment)

**Date:** 2026-08-05 · **Status:** all 6 sections approved by the user in the brainstorming session of 2026-08-05; awaiting user review of this document. · **Amends:** `2026-08-02-caesar-v1-design.md` §3 (execution & isolation — provider-credential custody) and the Plan 09 metering scope; everything else in the V1 spec stands. · **Consumed by:** Plan 07 (harness drivers), which this spec reshapes to subscription-first.

caesar V1 is a **personal, single-user, self-hosted platform**: one operator (the admin) running caesar on hardware they control — a MacBook first, possibly a dedicated Hetzner box later. The agents it orchestrates (Claude Code, Codex CLI) authenticate with the **operator's own consumer subscriptions** (Claude Max, ChatGPT) — the same way the operator uses those CLIs interactively today, with caesar as a thin orchestration layer on top. The operator is physically in the loop and re-authenticates when a login expires. No ToS workarounds: one individual, their own subscriptions, their own tasks.

---

## 1. Scope & posture

- **Subscription auth is the primary, day-one path.** API-key auth (`model_key`) remains a supported credential kind, but Plan 07's harness drivers are designed and verified subscription-first.
- **Multi-tenant / caesar-as-a-service is explicitly out of scope.** Anything that only matters when serving *other people's* credentials — gateway-side OAuth refresh, brokered provider custody as a hard requirement, per-tenant metering — is deliberately not built. This is a posture decision, not a temporary gap.
- **This amendment touches auth only.** Orchestration semantics (Plan 05 spine), the runner pipeline (Plan 06), the workflow model, and forge-credential handling are unchanged.

## 2. Credential model

Two new credential kinds join the existing encrypted store (`forge_token`, `model_key` — Plan 03 envelope encryption and use-auditing unchanged):

- **`subscription_token`** (Claude Code): a static string, ~1-year lifetime, minted by the operator via `claude setup-token` on any machine (the flow prints the token to the terminal; it is not saved anywhere by the CLI) and pasted into caesar. No refresh machinery — when it dies, the operator mints a new one.
- **`agent_auth_file`** (Codex): an opaque *mutable file* (`auth.json`) containing tokens the CLI refreshes itself. Provided by the operator via `codex login --device-auth` on the caesar box, or by logging in on a laptop and copying `~/.codex/auth.json`. caesar validates it is well-formed JSON and otherwise treats it as an opaque blob.

**Sync-back (`agent_auth_file` only).** caesar materializes a copy into the agent container; the CLI may rotate its tokens mid-run; after the agent container exits, caesar reads the file back and updates the store if the contents changed, so the next run starts from the newest tokens.

**Single-flight rule (`agent_auth_file` only).** OAuth refresh tokens may rotate on use, so two concurrent runs refreshing independently could invalidate each other's tokens. Credentials of this kind are single-flight: runs needing one queue behind each other. `subscription_token` has no such constraint — it is static and shareable across parallel runs.

## 3. Runtime delivery

**Custody mode joins the harness driver contract**: `materialized` (the real credential enters the agent container) or `brokered` (Plan 06's gateway header-injection; the container holds only an attempt-scoped broker token). Claude Code and Codex drivers both pin **`materialized`** — an explicit, user-ruled relaxation of the V1 spec §3 "no reusable secret in the agent container" property for *provider* credentials in personal mode. `brokered` remains implemented and is the mode API-key-based drivers use. **Forge credentials are untouched**: they never enter agent containers today and still don't (staging contract unchanged).

Per-harness mechanics:

- **Claude Code**: `CLAUDE_CODE_OAUTH_TOKEN` set in the agent container env. Accepted trade-off, recorded: the value is visible to `docker inspect` on the host — which in personal mode is the operator and no one else.
- **Codex**: `auth.json` written to the container home directory **outside the workspace bind**, so the diff/extract pipeline and failure snapshots structurally never see it. Read back after exit for sync-back (§2).

**Egress gateway keeps its role, minus provider header-injection for these two drivers**: CONNECT proxy, per-harness FQDN allowlist, default-deny, denials logged as `phase.egress_denied` events. Allowlist requirements:

- Codex's token-refresh endpoints (`auth.openai.com` family) **must** be allowlisted, or the CLI cannot refresh mid-run and long runs die opaquely.
- CLI telemetry endpoints (Statsig, Sentry for Claude Code) default **denied**. The CLIs tolerate this; denial events make any future CLI-update breakage diagnosable.
- Exact FQDN lists per harness are pinned in Plan 07 (they drift with CLI releases; the plan records the verified-current set and the denial events catch drift).

## 4. Cap-hit and auth-death handling

Both are **typed transient/terminal outcomes riding the existing Plan 05 retry machinery** — no new spine states, no transition-table changes:

- **`rate_limited`** (subscription cap window exhausted — e.g. Claude's rolling 5-hour window, ChatGPT message limits): the attempt requeues with a *not-before* time — the provider-surfaced window reset when available, otherwise a conservative backoff. The run stays alive and resumes unattended. The wall-clock deadline still bounds total life; nothing waits forever. Caps are a fact of life on subscriptions, so this is a first-class waiting condition, not an error.
- **`auth_expired`** (token dead or revoked): the run **fails fast with that typed outcome, naming the credential**. Deliberately *not* a parked awaiting-credential state: the operator is around by posture (preamble, §1), re-auth plus rerun is a 30-second loop, and park-and-auto-resume machinery is v1 bureaucracy. Parking can be added later without schema churn if practice demands it.

The asymmetry is deliberate: **caps heal themselves, so runs wait; dead auth needs human hands, so runs fail fast.** Mapping each CLI's exit codes and output to these outcomes is per-harness driver logic — the Plan 07 "exit→outcome mapping" work, with these two rows now specified.

**Per-provider concurrency note:** the `agent_auth_file` single-flight rule (§2) already serializes Codex. No additional per-provider concurrency limiter is specified for v1; if parallel Claude runs burn the cap window too fast in practice, a limiter is a policy knob to add later, not a spine change.

## 5. Operator loop

- **Credentials in**: the existing admin credentials API grows the two new kinds — paste the Claude token or the `auth.json` contents exactly like a `model_key` today (encrypted at rest, use-audited). **No embedded OAuth flows in caesar** — minting happens on the operator's laptop or via device-auth on the box.
- **Knowing when they die**, two cheap signals:
  1. Any run failing `auth_expired` names the credential in its typed outcome — visible in run detail and the event stream.
  2. The credentials view shows passive freshness: last successful use, last `auth_expired`, and for `subscription_token` the recorded provided-at date (a ~1-year token's age is visible before it dies mid-run).
- **No proactive token-probing cron**: the CLIs are the source of truth for whether auth works, and a probe would spend cap window to ask.

## 6. Verification & plan mapping

**Testing boundaries (honest about what CI can prove):**

- Driver-level unit tests run against recorded CLI behavior (exit codes, output shapes) — no live subscription calls in CI, ever.
- The `runnerintegration` lane proves materialization mechanics with fakes: credential lands outside the workspace bind; diff, extract report, and failure snapshots never contain credential material; sync-back captures a mutated `auth.json`; single-flight queueing holds for `agent_auth_file`.
- One **manual smoke checklist** in Plan 07, run once by the operator on real subscriptions, validates the two live paths end to end. That is the boundary between what CI can prove and what only a real token can.

**Plan mapping:**

- Everything above lands in **Plan 07** (harness drivers, subscription-first). Sequencing: PR #6 goes green and merges → this spec is committed → Plan 07 is authored from it.
- **Plan 09 scope note**: token/cost metering is demoted for subscription runs (flat-rate; nothing meaningful to meter). Request/byte/denial counts from Plan 06 stay. Cap awareness (§4) replaces cost accounting as the interesting signal.

## 7. Decision log (user rulings, 2026-08-05)

1. Personal single-user platform; subscription auth first-class from day one; multi-tenant out of scope.
2. Operator is the credential custodian and the human refresh loop; caesar surfaces auth death, never runs OAuth flows itself.
3. Cap-hit = waiting state with auto-resume, bounded by the run deadline. Auth-death = fail fast with typed outcome.
4. **Custody = `materialized` for both harnesses** (user ruling over the assistant's keep-Claude-brokered lean; uniformity and laptop-equivalent trust posture on a personal box). `brokered` stays implemented for API-key kinds; decision is a per-harness flag, reversible.
5. Codex `agent_auth_file` is single-flight; Claude `subscription_token` shareable across parallel runs.
