# bitnet-neo deep dive (`/Users/sofo/Projects/bitnet/bitnet-neo`)

*Research artifact, 2026-08-01. Produced by a read-only survey agent; defect claims independently verified against source. Closest working ancestor of caesar's execution core: a Go GitLab MR-review service — webhook → durable queue → leased agent workers → results posted back.*

Stack: Go 1.25, SQLite (`modernc.org/sqlite`), chi, koanf + validator/v10, official GitLab `client-go`, Prometheus, `log/slog`. Single binary, single node. 5,768 LOC, 46 files, 13 `internal/` packages; `go build`/`go test` clean (74 tests).

## 1. End-to-end flow

| Stage | Owner | Notes |
|---|---|---|
| Webhook receipt | `internal/webhook/handler.go:17-72` | `subtle.ConstantTimeCompare` on `X-Gitlab-Token`; non-MR events → 204 |
| Trigger decision | `internal/webhook/parser.go:19-32` | `open`/`reopen` need `last_commit.id`; `update` needs `oldrev != last_commit.id` |
| Dedup keys | `parser.go:34-53` | **Two distinct keys.** `DeliveryKey` = `Idempotency-Key` header, else SHA-256 of (event, project, iid, action, sha, full body). `ReviewKey` = `"{projectID}:{mrIID}:{sha}:{trigger}"` |
| Durable intake | `internal/store/jobs.go:63-155` | One tx inserts `webhook_deliveries` + `review_jobs` + `review_states`; 202 returned only after commit |
| Claim | `store/jobs.go:157-234` | Lease claim inside a tx; flips job → `running` and review_state → `running` |
| Dispatch | `internal/worker/dispatcher.go:54-153` | Poll loop + semaphore + per-job heartbeat goroutine |
| Orchestration | `internal/reviewer/reviewer.go:92-203` | Entire review lives in one `RunJob` |
| Workspace | `internal/workspace/manager.go` | Shared bare-repo cache, then full `git clone` + `checkout --force {sha}` per job |
| Agent run | `internal/backend/{supervisor,codexexec}.go` | `codex exec --json [--model] [--profile] [--sandbox] [-c …] --output-schema <tmpfile>`, prompt on stdin |
| Policy | `internal/reviewer/filters.go:29-75` | Confidence + severity filter, stable sort, `MaxFindings` truncation, inline on/off gating |
| Post back | `internal/gitlab/poster.go` | Summary note upserted by hidden marker `<!-- bitnet-neo:summary:{reviewKey} -->`; inline discussions keyed by `<!-- bitnet-neo:finding:{reviewKey}:{findingID} -->` |

The reviewer's ordering is the smart part: it fetches the MR first and **bails as `superseded` if `mr.HeadSHA != intent.HeadSHA`** (`reviewer.go:105-111`) before any expensive work. Empty diff-version or raw-diff is treated as "not ready yet" → 5s retry, correct because GitLab populates `diff_refs` asynchronously.

## 2. Durability and concurrency

States: `queued | running | retry | succeeded | failed | dead`, plus a coarser review-level state. Claiming is a lease, not a flag: a job is runnable if `queued`/`retry` with `scheduled_at <= now`, **or** `running` with expired `lease_until` — crash recovery falls out of the query, no janitor. `attempts` increments at claim time. Heartbeat goroutine extends the lease. Outcome classification uses a typed `*OutcomeError{Kind, RetryAt, ReplacementSHA, Message}` flowing up via `errors.As`; unclassified errors default to **permanent** (the safe default). Terminal writes use `context.Background()` so a cancelled job still records its outcome. Idempotency layered: delivery key → duplicate webhooks; review key → duplicate reviews; `posted_findings` + marker lookup → duplicate comments. `deriveFindingID` = SHA-256 over file+lines+severity+category+title → re-runs re-post nothing.

**Where it breaks down (verified):**
- **Max attempts never enforced** — stored, read, never checked; a retryable failure loops forever at a 60s cap.
- **Superseded collapses into failed** (`MarkSuperseded` delegates to failed-state update) — the distinction the code computed is destroyed; no `superseded` row value exists.
- **"Graceful drain" isn't**: one signal context for server and workers; SIGTERM cancels in-flight reviews immediately (work re-queued, but a 9-minute review dies at minute 10 of a deploy). `ShutdownTimeout` never read.
- **A single claim error kills the process** (propagates to main → exit 1). Safe only while a single goroutine polls; with Postgres/multi-instance it needs `SKIP LOCKED` and non-fatal claim errors.

## 3. Agent backend abstraction

One method: `Review(ctx, ReviewInput) (ReviewResult, ExecutionMeta, error)`; consumer-side narrow interfaces throughout (reviewer declares five), so fakes are trivial.

**The supervisor does not supervise.** Declared `StartupTimeout`/`ParseIdleTimeout`/`MaxOutputBytes`/`HealthcheckPeriod`; only `MaxOutputBytes` is read, and only *after* `cmd.Run()` buffered everything (caps nothing). No process group, no idle timer, no concurrent draining, no terminal-event validation. Parsing is a post-hoc JSONL scan for the last `item.completed`/`agent_message`; `turn.completed`, `turn.failed`, top-level `error` are ignored — a failed turn with an earlier message parses as success.

**Reviews have no timeout at all** (config `backend.timeout` validated then dropped; heartbeat extends the lease indefinitely; a hung codex holds a worker slot forever).

**`cmd.Env = os.Environ()`** (`supervisor.go:58`) hands the agent every secret the service holds. Worst line in the repo — and the exact bug three planning docs flagged as P0 before implementation.

`Preflight` only does `exec.LookPath`; `backendHealthy` set once, never mutated → `/readyz` backend check permanently green. `skills-lock.json` pins an authoring-time agent skill by SHA-256 (reproducibility lock for agent instructions, unrelated to runtime).

## 4. Storage schema

Four tables via `CREATE TABLE IF NOT EXISTS` in an ordered slice — no version table, no way to alter a column. `webhook_deliveries` (insert-only dedup log, never pruned), `review_jobs` (payload JSON, attempts, lease columns; one index on `(status, scheduled_at)`), `review_states` (summary note id/marker; two columns never written by any code path), `posted_findings` (`(review_key, finding_id)` PK, three post states, upsert). Append-mostly, no retention, no FKs. WAL + `busy_timeout` set at open.

## 5. Config, auth, observability, testing

Config: koanf `StrictMerge` + reflection walk **rejecting unknown keys** + validator tags + env overrides — excellent. But ~⅓ of the surface is inert: `max_body_bytes` (unbounded `io.ReadAll` — DoS), `shutdown_timeout`, `logging.*`, `skip_patterns`, `auto_review_*`, `update_summary_note`, all `budget.*`, `backend.timeout`/`parse_idle_timeout`/`healthcheck_interval`, effective `max_attempts`.

Auth: webhook secret constant-time; repo token via `git -c http.extraHeader` (better than URL-embedded, still `ps`-visible). One shared `http.Client` applies the **2-second webhook budget to every GitLab call** including multi-MB raw-diff fetches — will bite on any nontrivial MR.

Observability: 14 well-named Prometheus metrics registered with pre-created label values — **not one is incremented anywhere in production code**. slog JSON but call sites pass only `"err"` — no job/review/MR correlation. `/healthz` static; `/readyz` pings SQLite + the frozen backend flag.

Testing is the real strength: 74 strictly behavioral tests (`TestLeaseExpiryMakesJobRunnableAgain`, `TestRetryRepairsSummaryMetadataFromMarkerLookup`); real SQLite temp dirs, real `git init` in workspace tests, `httptest` GitLab, fakes only at the edges; full pipeline test webhook→store→worker→reviewer→poster. Gaps: no integration tag despite CLAUDE.md advertising it; no concurrency > 1 test; no Codex negative-path tests; vestigial `main_test.go` on a no-op stub.

Deploy: systemd unit with `ProtectSystem=strict`/`NoNewPrivileges`/`PrivateTmp` gates on `command -v codex`; **the Docker image doesn't install codex** — the two artifacts disagree.

## 6. The working docs (`docs/working/`)

- **`strategic-assessment.md`** (pre-implementation, 6-agent swarm): "~80% of what BitNet Neo plans already exists in PR-Agent"; the novel 15-20% (multi-agent CLI orchestration) unvalidated; differentiators "thin"; directions — kill the platform vision, validate multi-agent with a shell script on 20 real diffs, ship as CI job not webhook server. Named the security P0s that later shipped as real bugs (`os.Environ()`, payload URL validation, output sanitization, config-file stripping).
- **`ai-augmented-comparison.md`**: the ai-augmented PoC already solves ~70%; "fork it, fix the security gaps and heartbeat bug, add multi-agent as a Temporal fan-out" (Path A, 2-3 weeks vs 5-7). Names the PoC's defects: no `--allowedTools`, `cmd.Env` never set, plain `==` webhook token compare, token in clone URL, `HeartbeatTimeout: 5m` with no `RecordHeartbeat()` anywhere. Walks back its own "production-grade / horizontally scalable" claims (activities pass local paths → pinned to one worker).
- **`2026-03-10-mainline-vs-foundation-assessment.md`**: mainline (SQLite) vs foundation spec (Temporal + dual-backend + MCP): "mainline is more likely to survive first contact with production," but import five things: validate `git_http_url` against config, strip repo-local AI config before invoking the agent, `GIT_ASKPASS`, `/review` manual trigger with cooldown, MR-visible note on terminal failure. **None of the five shipped.**
- **`implementation-review.md`** (reviews the plan): "over-builds infrastructure, under-builds product." Best parts: durable-before-acknowledge, diff-position validation, seven-class failure taxonomy, vertical-slice order. Worst bet: ~500-800 lines of bespoke dispatcher "easy to write and hard to get right." Two subsystem-deleting recommendations: GitLab **Draft Notes API + `bulk_publish`** (atomic comment publication eliminates `posted_findings` entirely), and dropping the bare-repo cache (~30s saved on a 5-10 min review).
- **`code-review.md`** (reviews the code; claims independently re-verified and they hold): 1 critical + 6 high security, 3 high functional bugs, "significant dead-config surface area"; metrics "completely inert." Subtle B10: response JSON schema marks `end_line`/`suggestion` required while Go tags them `omitempty`, and tests never pass `OutputSchema` — the mismatch can only surface in production.

The arc across all five docs: three warned about the same security P0s and over-building *before* a line was written; the code shipped with those exact P0s and a third of the config inert.

## 7. Verdict for caesar

**Copy these ten:**
1. **Durable-before-acknowledge** — one tx writes delivery + job + state; only then 202.
2. **Two-level dedup keys** — transport (`DeliveryKey`) vs semantic (`ReviewKey`) are different questions.
3. **Leases over status flags** — runnable = queued/retry and due, OR running with expired lease; port to Postgres with `FOR UPDATE SKIP LOCKED`.
4. **Typed outcome errors** any layer can raise via `errors.As`; retry/dead decision in one place; unclassified → permanent.
5. **Supersede-before-work** — check the trigger's SHA is still current before cloning or spending tokens; re-check between phases.
6. **"Not ready yet" as first-class retry** — upstream-hasn't-materialized ≠ upstream failed.
7. **Deterministic content-hash IDs for emitted artifacts** + hidden markers → retries reconcile instead of duplicating; abstract marker format per forge.
8. **Reconcile-from-remote crash recovery** — repair local rows by searching the forge for markers (but paginate).
9. **Strict config with unknown-key rejection** — a YAML typo becomes a startup error naming the key.
10. **Consumer-side interfaces + behavioral tests** — narrow per-consumer interfaces; test names describe behavior.

**Do differently:**
1. **Never `cmd.Env = os.Environ()`** — build env from an explicit allowlist; one agent-spawn helper as the only process-creation point.
2. **No dead config** — a field lands in the same commit as its first read.
3. **No metrics without call sites** — emit first, register second; a page of zeroes is worse than no endpoint.
4. **Attempt limits in the claim query** (`attempts < max_attempts` in WHERE) + a real dead-letter terminal state.
5. **`superseded` gets its own state**; design posting around **atomic batch publish** where the forge supports it (GitLab Draft Notes), per-artifact tracking as fallback.

**Structural notes:** backend importing the scheduler's error type is a layering inversion — put shared error types in a leaf package. SQLite SELECT-then-UPDATE claiming is single-poller-safe only; claim errors must not be fatal.
