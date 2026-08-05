# caesar Plan 06 — Docker Runner & Egress Gateway Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The real execution substrate behind `runs.Executor` (spec §3): per-attempt hardened Docker containers on fresh internal networks with bind-mounted workspaces, the `caesar egress-proxy` credential broker + CONNECT proxy (dual-homed, attempt-scoped token, per-provider operation allowlists), the clone staging contract (in-memory credential path, sanitize-then-verify), hostile-`.git`-safe patch extraction in disposable networkless containers, per-attempt disk quotas + the free-space claim watermark, hard-capped log capture with `log_ref`/`failure_snapshot` artifacts, validation-container machinery for Plan 08, reconciliation against real Docker, and the runner integration suite including escape probes — plus the Plan 05 carry-forwards (plan-cache eviction, reconciling-strand sweeper).

**Architecture:** Three new units. (1) `internal/platform/dockerapi` — a minimal hand-rolled Docker Engine API client over the unix socket (typed endpoints the runner needs, stdcopy log demux, pinned API version; no moby dependency). (2) `internal/runs/runner` + `internal/runs/runner/gitops` + `internal/runs/egress` — subpackages of the runs context (the `fake` precedent): the runner implements `runs.Executor` by orchestrating a four-container pipeline per attempt — **stage** (caesar-git image, clones/sanitizes/verifies the workspace, holds the forge credential in env only) → **egress gateway** (distroless caesar image in `egress-proxy` mode, dual-homed) → **agent** (harness image, hardened, attempt network only) → **extract** (caesar-git image, networkless, computes the canonical patch against the trusted base mirror without ever executing git against the hostile workspace `.git`). Staging and extraction logic is plain Go in `gitops`, exposed as `caesar stage` / `caesar extract` subcommands so the containers run our tested code. (3) Small extensions to `runs` itself: `ExecutionRequest.Repo`, an `Admission` port consulted before every claim, `Service.AppendRuntimeEvent` as the bounded runtime-event seam, failure-path artifact persistence, and the two orchestrator hardening fixes. Production `serve` gains a `runner` config block with `enabled` defaulting **false** — `NotImplementedExecutor` remains the honest default until Plan 07's harness drivers make real phases meaningful.

**Tech Stack:** Everything from Plans 01–05. **No new Go dependencies** (the Docker client is hand-rolled over `net/http` + unix sockets; statfs via stdlib `syscall`). Docker Engine API pinned at `v1.43` (Docker 24+). One new Dockerfile stage: `git` (alpine + git + the caesar binary) published as `caesar-git`.

## Decisions this plan bakes in

Flag these to sofo if any look wrong; they are interpretations where the spec left latitude:

1. **Hand-rolled minimal Docker Engine API client** (`internal/platform/dockerapi`) instead of the moby SDK: stdlib-first house law; the runner needs ~15 endpoints; the SDK drags a very large module graph. API version pinned `v1.43`; the client speaks HTTP over the unix socket and implements the stdcopy log-frame demux itself.
2. **Disk-quota mechanism = control-plane watchdog** (the spec offered "sized volume / xfs project quota — pinned in the plan"): per-attempt workspaces are bind-mounted host directories under `data_root/attempts/<ref>/`; the runner samples a bounded walk every 2s and kills the agent with typed `workspace_quota_exceeded` on breach (byte **and** file-count caps). A Docker "sized volume" only exists for tmpfs (RAM-backed, contents lost between the stage and agent containers) and `xfs_quota` requires host filesystem control caesar's container doesn't have. XFS project quotas remain the documented prod hardening for the VM recipe (Plan 13); the watchdog is the enforced-everywhere mechanism, and the **free-space watermark** (statfs on `data_root`, checked through the new `runs.Admission` port *before* every claim) is the system-level backstop protecting the Postgres volume (which lives on a separate mount anyway, spec §8).
3. **Two published images from one Dockerfile.** The final `runtime` stage stays distroless (serve + `egress-proxy`). A new `git` stage (alpine + git + the caesar binary, `USER 65532`) is published as **caesar-git** and runs `caesar stage` / `caesar extract` / validation helpers. `task image:build` builds both.
4. **All attempt containers run as the control plane's own uid:gid** (refused if root): bind-mounted workspaces must be writable by stage/agent/extract alike, and the only uid guaranteed to own `data_root` in every deployment is the caesar process's own. The agent image's `USER` is overridden; non-root is enforced structurally because the runner refuses to start when running as uid 0.
5. **Broker token format** pinned: `cbt1.<base64url(24 random bytes)>`. The gateway holds only its SHA-256; the raw token exists in the agent's env and nowhere else. Gateway config arrives as one env var `CAESAR_EGRESS_CONFIG` (JSON, strict decode) read via the config package (envaccess law); it contains the provider credential — visible only to docker-socket holders, i.e. the trusted plane (spec §8 trust boundary).
6. **Per-provider operation allowlists** pinned by harness kind: `claude-code` → POST `/v1/messages`, POST `/v1/messages/count_tokens`, GET `/v1/models`; `codex` → POST `/v1/responses`, POST `/v1/chat/completions`, GET `/v1/models`. CONNECT is 443-only, exact-hostname allowlist, private/loopback/link-local/CGNAT/ULA targets refused, and the vetted IP is dialed directly (no re-resolve TOCTOU).
7. **Runtime observations enter `run_events` through a new seam**: `runs.Service.AppendRuntimeEvent` (own tx, new `GetRunProjectID` query) with two new kinds — `phase.log_truncated` and `phase.egress_denied` (hard cap 20 denial events per attempt). And **failure artifacts persist**: `Executor.Execute` may return artifacts *alongside* an error; `evaluateFailure` now inserts them (this is how `log_ref` + `failure_snapshot` survive abnormal ends).
8. **Metering V1 = requests/bytes/denials**, harvested from the gateway's structured stdout at collection into the `log_ref` artifact payload. Token-level usage/cost metering is Plan 09 (it owns cost ceilings).
9. **Nonzero agent exit classifies transient `harness_error`** in this plan; per-harness exit-condition mapping is Plan 07's contract. Extraction runs only on exit 0.
10. **`serve` wires the runner behind `runner.enabled` (default false).** Task intake is refused until Plan 08's forge adapters anyway (`unavailableForge`), so a prod-enabled runner is unreachable today; the flag exists so config plumbing, wiring, and validation are real now and Plan 13's compose flips one boolean. Disabled ⇒ `NotImplementedExecutor` + `AlwaysAdmit`, exactly Plan 05's posture.
11. **New test lane** build tag `runnerintegration` with its own CI job (builds both images first, then runs `./internal/runs/runner/... ./internal/runs/egress/...`). The normal `integration` lane stays Docker-cheap (testcontainers Postgres only).
12. **Validation containers are mechanism only** (`Runner.RunValidation`): same hardening, fresh internal network, CONNECT-only gateway (no provider credential), caller-supplied image/cmd/allowlist. Plan 08 consumes it on the publish path; Plan 09 supplies policy values.
13. **Plan 05 carry-forwards land here**: orchestrator plan-cache eviction (terminalization + hourly janitor prune) and the reconciling-strand sweeper (grace = 2× lease TTL, CAS lease-extension so concurrent processes don't double-reconcile).
14. **Workspace lifecycle**: `Execute` tears down containers, the attempt network, and the attempt directory on **every** exit path (logs persist under `data_root/logs/<ref>/`); `Inspect` adopts a completed container by re-running collection before teardown; a label-scoped orphan sweep (`caesar.managed=true`, older than 25h) catches processes that died holding resources. Log/snapshot **retention** sweeping is deferred to Plan 13 with the deploy stack (spec §8 places it there).

## Global Constraints

Everything in Plans 01–05's Global Constraints still applies verbatim (module path `github.com/giiorg/caesar/backend`, layout law + arch tests, dependency deny-list, strict JSON config, stdlib asserts + go-cmp, slog JSON, TDD + table-driven + behavioral names, Taskfile parity, generated code never hand-edited, commit style — one short sentence, **no AI attribution of any kind**). New pins for this plan:

- **Naming & labels** (execution identity, spec §2 — everything derives from `AttemptRef.String()` = `caesar-<run>-<node>-<attempt>`):

  | resource | name |
  |---|---|
  | agent container | `caesar-<ref-suffix>` (= `ref.String()`) |
  | gateway container | `ref.String() + "-egress"` |
  | stage container | `ref.String() + "-stage"` |
  | extract container | `ref.String() + "-extract"` |
  | validation container | `ref.String() + "-validate"` |
  | attempt network | `ref.String() + "-net"` (`internal: true`) |
  | shared egress network | `caesar-egress` (plain bridge, created idempotently, labeled) |

  Every runner-created resource carries labels `caesar.managed=true`, `caesar.run`, `caesar.node`, `caesar.attempt`. The gateway's alias on the attempt network is `egress`.

- **Filesystem layout** (all under `runner.data_root`; `hostPath()` maps the prefix to `runner.data_host_root` for daemon-side bind sources):

  ```
  <data_root>/attempts/<ref>/          # removed at teardown, every path
    stage.json  stage.report.json      # stage subcommand contract
    extract.json  extract.report.json  # extract subcommand contract
    base.git/                          # trusted bare mirror (never mounted into the agent)
    workspace/                         # the ONLY thing the agent sees, at /workspace
  <data_root>/logs/<ref>/              # persists; retention = Plan 13
    agent.log  gateway.log  validate.log
  ```

- **Container hardening profile** (every runner-created container): `CapDrop: ["ALL"]`, `SecurityOpt: ["no-new-privileges"]`, default seccomp (never unconfined), `ReadonlyRootfs: true`, tmpfs `/tmp` (`rw,size=268435456`), `User: <uid>:<gid>` of the control-plane process (refused if 0), no extra hosts (single test-only exception pinned in Task 8), `RestartPolicy: none`, `AutoRemove: false` (reconciliation needs the corpse). Per-role deltas:

  | container | network | binds | limits |
  |---|---|---|---|
  | agent | attempt net only | `<attempt>/workspace:/workspace:rw` | mem 4 GiB, cpus 2.0, pids 512 |
  | gateway | attempt net (alias `egress`) + `caesar-egress` | none | mem 256 MiB, cpus 1.0, pids 128 |
  | stage | `caesar-egress` (or `none` when the clone source is a bind) | `<attempt>:/attempt:rw` (+ test fixture `:ro`) | mem 1 GiB, cpus 1.0, pids 256 |
  | extract | `none` | `<attempt>:/attempt:rw` | mem 1 GiB, cpus 1.0, pids 256 |
  | validation | fresh internal net + its own CONNECT-only gateway | `<dir>:/workspace:rw` | mem 4 GiB, cpus 2.0, pids 512 |

- **Pinned values** (Options defaults; config-overridable where the config block lists them):

  | knob | value |
  |---|---|
  | workspace quota | 2 GiB bytes, 200000 files |
  | quota poll interval | 2s |
  | log cap (per stream) | 16 MiB |
  | log flood kill threshold | 64 MiB |
  | free-space watermark | 10 GiB free on `data_root` |
  | `.caesar/out/artifacts.json` cap | 1 MiB |
  | patch (diff) cap | 4 MiB |
  | extraction traversal caps | same as workspace quota |
  | failure-snapshot log tail | 64 KiB |
  | failure-snapshot workspace listing | 200 entries |
  | egress meter log interval | 30s |
  | egress denial events per attempt | 20 |
  | CONNECT | port 443 only, dial timeout 10s |
  | orphan sweep | every 10m, resources older than 25h |
  | Docker API version | `v1.43` |
  | stage/extract container timeout | 10m each |

- **Constructed agent env allowlist** (never inherited — the complete list; Plan 07 appends harness-specific entries through its driver):

  ```
  CAESAR_ATTEMPT=<ref.String()>
  CAESAR_WORKSPACE=/workspace
  CAESAR_BROKER_URL=http://egress:8080
  CAESAR_BROKER_TOKEN=cbt1.<...>
  HTTPS_PROXY=http://egress:8080
  HTTP_PROXY=http://egress:8080
  NO_PROXY=
  HOME=/tmp
  PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
  ```

- **New outcome codes** (extending Plan 05's table; unclassified stays permanent):

  | class | codes added |
  |---|---|
  | `transient` | `image_unavailable`, `staging_failed` |
  | `permanent` | `staging_verification_failed`, `base_sha_unavailable`, `workspace_quota_exceeded`, `log_flood`, `extraction_failed`, `patch_too_large`, `embedded_repo` |

- **New event kinds** (extending the Plan 05 vocabulary): `phase.log_truncated` `{node_id, attempt, stream, cap_bytes}` · `phase.egress_denied` `{node_id, attempt, host, reason}` (≤ 20 per attempt).

- **Artifact payloads pinned**: `log_ref` = `{"streams": {"agent": {"path","bytes","truncated"}, "gateway": {...}}, "egress": {"requests","denied","request_bytes","response_bytes","connects","connect_bytes"}}` (paths relative to `data_root`). `failure_snapshot` = `{"exit_code", "oom_killed", "error", "log_tail", "workspace": {"files","bytes","entries":[{"path","bytes"},...]}}` — log tail redacted (broker token + provider credential values scrubbed).

- **Config additions** (`runner` block; all fields read in serve — no dead config; `Bytes` is a new strict-JSON size type parsing `"16MiB"`-style strings with KiB/MiB/GiB/TiB suffixes or bare digit strings):

  ```json
  "runner": {
    "enabled": false,
    "docker_host": "/var/run/docker.sock",
    "data_root": "/var/lib/caesar",
    "data_host_root": "",
    "caesar_image": "",
    "git_image": "",
    "egress_network": "caesar-egress",
    "workspace_quota_bytes": "2GiB",
    "workspace_quota_files": 200000,
    "log_cap_bytes": "16MiB",
    "log_flood_bytes": "64MiB",
    "free_bytes_watermark": "10GiB",
    "memory_bytes": "4GiB",
    "cpus": 2.0,
    "pids_limit": 512
  }
  ```

  Validation: when `enabled`, `caesar_image` and `git_image` required, all sizes positive, `log_flood_bytes >= log_cap_bytes`, `cpus` in (0, 64], `pids_limit` 16–4096. `data_host_root` defaults to `data_root`.

- **Per-provider operation allowlists** (pinned; keyed by harness kind):

  | kind | credential header | operations |
  |---|---|---|
  | `claude-code` | `x-api-key` (no prefix) | POST `/v1/messages`, POST `/v1/messages/count_tokens`, GET `/v1/models` |
  | `codex` | `authorization`, prefix `Bearer ` | POST `/v1/responses`, POST `/v1/chat/completions`, GET `/v1/models` |

- **Subcommand contracts** (`caesar stage`, `caesar extract`, `caesar egress-proxy` join `serve`/`migrate`/`admin` in `app.Main`): stage and extract read `/attempt/stage.json` / `/attempt/extract.json`, write `/attempt/stage.report.json` / `/attempt/extract.report.json`; on typed failure they write `{"outcome": {"class","code","message"}}` into the report file and exit 1. The runner parses the report either way — container exit codes alone are never the signal.

---
### Task 1: Runner vocabulary and port extensions in `runs`

The pure-Go groundwork every later task consumes: the `Bytes` config type, the new outcome codes and event kinds, `ExecutionRequest.Repo`, the `Admission` port consulted before every claim, `Service.AppendRuntimeEvent`, and failure-path artifact persistence in `evaluateFailure`.

**Files:**
- Create: `backend/internal/platform/config/bytes.go`, `backend/internal/platform/config/bytes_test.go`
- Modify: `backend/internal/runs/outcome.go` (new codes), `backend/internal/runs/events.go` (new kinds), `backend/internal/runs/ports.go` (`ExecutionRequest.Repo`, `Admission`, `AlwaysAdmit`), `backend/internal/runs/orchestrator.go` (admission consult, `cachedPlan.repo`, `buildRequest`, failure artifacts), `backend/internal/runs/service.go` (`AppendRuntimeEvent`), `backend/internal/runs/store/queries.sql` (`GetRunProjectID`), `backend/internal/app/serve.go` (admission arg)
- Test: `backend/internal/runs/events_integration_test.go` (append runtime event), `backend/internal/runs/orchestrator_integration_test.go` (failure artifacts persist)

**Interfaces:**
- Consumes: Plan 05's `runs` package as it stands.
- Produces (later tasks rely on these exact names):
  - `config.Bytes` with `UnmarshalJSON` (strings `"16MiB"`, `"2GiB"`, `"512KiB"`, `"1TiB"`, or bare digits `"1024"`) and `func (b Bytes) Int64() int64`.
  - Outcome codes: `CodeImageUnavailable`, `CodeStagingFailed`, `CodeStagingVerificationFailed`, `CodeBaseSHAUnavailable`, `CodeWorkspaceQuotaExceeded`, `CodeLogFlood`, `CodeExtractionFailed`, `CodePatchTooLarge`, `CodeEmbeddedRepo` (string values per the Global Constraints table).
  - Event kinds: `EventPhaseLogTruncated = "phase.log_truncated"`, `EventPhaseEgressDenied = "phase.egress_denied"`.
  - `ExecutionRequest.Repo RepoRef` (populated by `buildRequest` from the task's repo).
  - `type Admission interface { AdmitClaim(ctx context.Context) error }` and `type AlwaysAdmit struct{}`; `NewOrchestrator(svc, exec, admission, cfg, log)` (signature grows one parameter).
  - `func (s *Service) AppendRuntimeEvent(ctx context.Context, ref AttemptRef, kind string, payload map[string]any) error` — own tx, resolves project via `GetRunProjectID`, correlation id = `ref.String()`.
  - **Failure-artifact contract**: `Execute` may return `(ExecutionResult{Artifacts: ...}, err)` with `err != nil`; `evaluateFailure` persists those artifacts (empty `parent_ids`) after winning the fencing CAS, before retry/terminal handling; `phase.retried`/`phase.dead` payloads gain `artifact_ids` when non-empty.

- [ ] **Step 1: Write the failing `Bytes` tests**

```go
// backend/internal/platform/config/bytes_test.go
package config

import (
	"encoding/json"
	"testing"
)

func TestBytes_ParsesBinarySuffixes(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{`"1024"`, 1024},
		{`"512KiB"`, 512 << 10},
		{`"16MiB"`, 16 << 20},
		{`"2GiB"`, 2 << 30},
		{`"1TiB"`, 1 << 40},
	}
	for _, c := range cases {
		var b Bytes
		if err := json.Unmarshal([]byte(c.in), &b); err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if b.Int64() != c.want {
			t.Errorf("%s: got %d want %d", c.in, b.Int64(), c.want)
		}
	}
}

func TestBytes_RejectsGarbage(t *testing.T) {
	for _, in := range []string{`"16MB"`, `"-1KiB"`, `""`, `"KiB"`, `16`, `"1.5GiB"`} {
		var b Bytes
		if err := json.Unmarshal([]byte(in), &b); err == nil {
			t.Errorf("%s: expected error, got %d", in, b.Int64())
		}
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd backend && go test ./internal/platform/config/ -run TestBytes -v`
Expected: FAIL (`Bytes` undefined)

- [ ] **Step 3: Implement `Bytes`**

```go
// backend/internal/platform/config/bytes.go
package config

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Bytes is a byte size that unmarshals from strict JSON strings like
// "16MiB" (binary suffixes only) or bare digits like "1024". Decimal
// suffixes (MB/GB) are rejected: one notation, no ambiguity.
type Bytes int64

var byteSuffixes = []struct {
	suffix string
	mult   int64
}{
	{"TiB", 1 << 40}, {"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10},
}

func (b *Bytes) UnmarshalJSON(raw []byte) error {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return fmt.Errorf(`byte size must be a string like "16MiB": %w`, err)
	}
	mult := int64(1)
	digits := s
	for _, sf := range byteSuffixes {
		if strings.HasSuffix(s, sf.suffix) {
			mult, digits = sf.mult, strings.TrimSuffix(s, sf.suffix)
			break
		}
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || n < 0 {
		return fmt.Errorf("invalid byte size %q", s)
	}
	*b = Bytes(n * mult)
	return nil
}

func (b Bytes) Int64() int64 { return int64(b) }
```

- [ ] **Step 4: Run to verify pass, then add the vocabulary constants**

Run: `cd backend && go test ./internal/platform/config/ -run TestBytes -v`
Expected: PASS

In `backend/internal/runs/outcome.go`, extend the pinned-codes block:

```go
	// Plan 06 runner codes.
	CodeImageUnavailable          = "image_unavailable"
	CodeStagingFailed             = "staging_failed"
	CodeStagingVerificationFailed = "staging_verification_failed"
	CodeBaseSHAUnavailable        = "base_sha_unavailable"
	CodeWorkspaceQuotaExceeded    = "workspace_quota_exceeded"
	CodeLogFlood                  = "log_flood"
	CodeExtractionFailed          = "extraction_failed"
	CodePatchTooLarge             = "patch_too_large"
	CodeEmbeddedRepo              = "embedded_repo"
```

In `backend/internal/runs/events.go`, extend the kind block:

```go
	EventPhaseLogTruncated = "phase.log_truncated"
	EventPhaseEgressDenied = "phase.egress_denied"
```

- [ ] **Step 5: Extend `ExecutionRequest` and the ports file**

In `backend/internal/runs/ports.go`: add `Repo RepoRef` to `ExecutionRequest` (after `HarnessID`), and append:

```go
// Admission gates claiming on runner capacity (spec §8: the runner
// refuses new claims under a free-space watermark — typed backpressure,
// checked BEFORE a claim burns an attempt's lease).
type Admission interface {
	AdmitClaim(ctx context.Context) error
}

// AlwaysAdmit is the no-runner posture (and the fake-driven tests').
type AlwaysAdmit struct{}

func (AlwaysAdmit) AdmitClaim(context.Context) error { return nil }
```

- [ ] **Step 6: Write the failing integration test for `AppendRuntimeEvent`**

Append to `backend/internal/runs/events_integration_test.go` (same conventions as the existing tests there — testdb helper, seeded task/run from `testutil_integration_test.go`):

```go
func TestAppendRuntimeEvent_OwnTxProjectScopedAndOrdered(t *testing.T) {
	svc, env := newTestService(t) // existing helper naming — reuse whatever Task 7/Plan 05 provides
	runID, ref := env.seedRunningRunWithPhase(t)

	err := svc.AppendRuntimeEvent(t.Context(), ref, runs.EventPhaseLogTruncated,
		map[string]any{"node_id": ref.NodeID, "attempt": ref.Attempt, "stream": "agent", "cap_bytes": 16777216})
	if err != nil {
		t.Fatalf("append runtime event: %v", err)
	}

	evs := env.eventsForRun(t, runID)
	last := evs[len(evs)-1]
	if last.Kind != runs.EventPhaseLogTruncated {
		t.Fatalf("kind = %s", last.Kind)
	}
	if last.ProjectID != env.projectID {
		t.Fatalf("project id not resolved: %s", last.ProjectID)
	}
	if last.CorrelationID != ref.String() {
		t.Fatalf("correlation = %s", last.CorrelationID)
	}
}
```

(Adapt helper names to what `testutil_integration_test.go` actually exports — the shape of the assertions is binding, the helper spelling is not. If no seeded-run helper exists, build the run through the intake service exactly as the Plan 05 orchestrator tests do.)

- [ ] **Step 7: Add the query and the service method**

`backend/internal/runs/store/queries.sql`:

```sql
-- name: GetRunProjectID :one
SELECT t.project_id FROM runs r JOIN tasks t ON t.id = r.task_id WHERE r.id = $1;
```

Run: `task db:sqlc`

`backend/internal/runs/service.go`:

```go
// AppendRuntimeEvent records one bounded runtime observation (log
// truncation, egress denial) in its own transaction. The runner calls
// this mid-attempt; volume is capped by the caller (plan: ≤20 denials
// per attempt), so bulk agent output can never bloat the dispatch
// database (spec §3 collection).
func (s *Service) AppendRuntimeEvent(ctx context.Context, ref AttemptRef, kind string, payload map[string]any) error {
	projectID, err := store.New(s.pool).GetRunProjectID(ctx, ref.RunID)
	if err != nil {
		return fmt.Errorf("runs: runtime event project: %w", err)
	}
	now := s.clock.Now()
	return pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		return appendEvents(ctx, tx, now, []Event{{
			RunID: ref.RunID, ProjectID: projectID, Kind: kind,
			CorrelationID: ref.String(), Payload: payload,
		}})
	})
}
```

Run: `cd backend && go test -race -tags integration -count=1 ./internal/runs/ -run TestAppendRuntimeEvent -v`
Expected: PASS

- [ ] **Step 8: Wire `Repo` into the orchestrator's plan cache and request**

In `backend/internal/runs/orchestrator.go`: add `repo RepoRef` to `cachedPlan`; in `loadPlan`, after loading the task row, resolve `repo, err := o.svc.repos.Repo(ctx, taskRow.RepoID)` (propagate errors like the definition path) and store it; in `buildRequest`, set `Repo: cp.repo` in the constructed `ExecutionRequest`.

- [ ] **Step 9: Admission before every claim**

Add `admission Admission` to the `Orchestrator` struct; `NewOrchestrator(svc *Service, exec Executor, admission Admission, cfg OrchestratorConfig, log *slog.Logger)` stores it (nil → `AlwaysAdmit{}`). In `workerLoop`, before `claimNext`:

```go
		if err := o.admission.AdmitClaim(ctx); err != nil {
			o.log.Warn("orchestrator: claims refused", "worker", workerID, "reason", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(o.cfg.PollInterval):
			}
			continue
		}
```

Update every `NewOrchestrator` call site (serve.go passes `runs.AlwaysAdmit{}` for now — Task 12 replaces it; the Plan 05 integration tests gain the same argument).

- [ ] **Step 10: Failure artifacts persist — failing test first**

Append to `backend/internal/runs/orchestrator_integration_test.go`: a scripted executor wrapper (local type in the test file) that returns `(ExecutionResult{Artifacts: []NewArtifact{{Type: ArtifactKindLogRef, Payload: map[string]any{"streams": map[string]any{}}}}}, OutcomeError{...transient harness_error...})` on attempt 1 and succeeds on attempt 2. Drive a run through the orchestrator (existing harness in that file). Assert: after completion the artifacts table holds a `log_ref` row for `(run, node, attempt 1)` — the failed attempt — and the `phase.retried` event payload carries its id in `artifact_ids`.

Run: `cd backend && go test -race -tags integration -count=1 ./internal/runs/ -run TestOrchestrator_FailureArtifactsPersist -v`
Expected: FAIL (no artifact row for the failed attempt)

- [ ] **Step 11: Implement failure-artifact persistence**

In `evaluateFailure`, after the fencing CAS wins (both branches — retry's `finishPhase` and terminal's), insert `res.Artifacts` exactly as `evaluateSuccess` does but with `parentIDs = []string{}`, collecting `artifactIDs`; add `"artifact_ids": artifactIDs` to the `phase.retried` / `phase.dead` payload when non-empty. This requires threading `res ExecutionResult` into `evaluateFailure` (the `evaluate` caller already has it). Extract the artifact-insertion loop from `evaluateSuccess` into a shared unexported helper `insertArtifacts(ctx, q, ref, artifacts, rawParents, now) ([]string, error)` — same code, two callers.

Run: `cd backend && go test -race -tags integration -count=1 ./internal/runs/ -v`
Expected: PASS (including all pre-existing Plan 05 tests)

- [ ] **Step 12: Full local gate and commit**

Run: `cd backend && go build ./... && task backend:test && task backend:analyze`
Expected: green

```bash
git add backend/internal/platform/config/ backend/internal/runs/ backend/internal/app/serve.go
git commit -m "feat(backend): runner vocabulary, admission port, runtime events, failure artifacts"
```

---

### Task 2: Orchestrator hardening — plan-cache eviction and the reconciling-strand sweeper

The two Plan 05 carry-forwards: the per-run plan cache currently grows forever, and a crash between `MarkPhaseReconciling` and `evaluate` leaves an attempt stuck `reconciling` until the wall deadline.

**Files:**
- Modify: `backend/internal/runs/orchestrator.go`, `backend/internal/runs/store/queries.sql`
- Test: `backend/internal/runs/orchestrator_integration_test.go`

**Interfaces:**
- Consumes: Task 1's orchestrator shape.
- Produces: `dropPlan(runID)`; janitor calls `prunePlanCache` hourly and `sweepStuckReconciling` every tick. Store queries `ListTerminalRunIDsAmong :many` (`ids uuid[]`), `ClaimStuckReconciling :many` (grace + lease seconds + limit; CAS lease-extension returning full rows). Reconcile body refactored into `reconcileAttempt(ctx context.Context, row store.PhaseRun)` shared by both sweepers.

- [ ] **Step 1: Failing test — cache eviction**

Add to `orchestrator_integration_test.go`:

```go
func TestOrchestrator_PlanCacheEvictedOnTerminalization(t *testing.T) {
	// Drive a run to succeeded through the fake executor (existing helper),
	// then assert the orchestrator's cache no longer holds the run.
	// Expose len via a test-only accessor:
	//   func (o *Orchestrator) cachedPlanCount() int  (orchestrator.go, unexported, used from this package's tests)
}
```

Concretely: run the existing happy-path scenario, then `if orch.cachedPlanCount() != 0 { t.Fatalf(...) }`. Add a second scenario: terminalize via the wall-deadline sweeper (SQL-backdate `wall_deadline`, tick `sweepExpiredRuns`) and assert eviction there too.

Run: `cd backend && go test -race -tags integration -count=1 ./internal/runs/ -run TestOrchestrator_PlanCacheEvicted -v`
Expected: FAIL

- [ ] **Step 2: Implement eviction**

```go
func (o *Orchestrator) dropPlan(runID uuid.UUID) {
	o.planMu.Lock()
	delete(o.plans, runID)
	o.planMu.Unlock()
}

func (o *Orchestrator) cachedPlanCount() int {
	o.planMu.Lock()
	defer o.planMu.Unlock()
	return len(o.plans)
}
```

Call sites, all **after** the transaction commits (a rolled-back tx must not evict a live run's cache — though even that would only cost a recompile, since eviction needs eventual truth, not precision):

- `terminalizeRun`: capture the `FinishRun` row count in a variable declared outside the tx closure; `if n > 0 { o.dropPlan(runID) }` after `pg.WithTx` returns nil.
- `evaluate`: thread a `runFinished bool` up from `evaluateSuccess`/`evaluateFailure` (both already know — they are the callers of `finishRunTx`), captured in a variable outside the closure; `if err == nil && runFinished { o.dropPlan(c.Ref.RunID) }`.

Backstop for runs terminalized by *another* process, in `janitorLoop` inside the existing hourly `lastPrune` block:

```go
			o.prunePlanCache(ctx)
```

```go
// prunePlanCache drops cache entries whose runs are terminal — covers
// runs terminalized by another process (plan 06 carry-forward: the
// cache previously grew for the process lifetime).
func (o *Orchestrator) prunePlanCache(ctx context.Context) {
	o.planMu.Lock()
	ids := make([]uuid.UUID, 0, len(o.plans))
	for id := range o.plans {
		ids = append(ids, id)
	}
	o.planMu.Unlock()
	if len(ids) == 0 {
		return
	}
	terminal, err := store.New(o.svc.pool).ListTerminalRunIDsAmong(ctx, ids)
	if err != nil {
		o.log.Error("orchestrator: prune plan cache", "error", err)
		return
	}
	for _, id := range terminal {
		o.dropPlan(id)
	}
}
```

```sql
-- name: ListTerminalRunIDsAmong :many
SELECT id FROM runs
WHERE id = ANY(@ids::uuid[]) AND status IN ('succeeded', 'failed', 'superseded', 'dead');
```

Run: `task db:sqlc`, then the Step 1 tests.
Expected: PASS

- [ ] **Step 3: Failing test — reconciling strand**

```go
func TestOrchestrator_StuckReconcilingIsSweptAndConcluded(t *testing.T) {
	// 1. Seed a running run with a claimed phase (fake executor scripted to
	//    hang; existing lease-expiry test shows the pattern).
	// 2. SQL-backdate the lease, run ONLY MarkPhaseReconciling (via a
	//    store call from the test), so the row sits 'reconciling' with an
	//    expired lease and nobody evaluating — the crash window.
	// 3. SQL-backdate leased_until past the grace (2× lease TTL).
	// 4. Tick the janitor's sweepStuckReconciling.
	// 5. Assert the attempt concluded: a new pending attempt exists
	//    (transient reconcile_lost path) and the stuck row is 'failed'.
}
```

Run: expected FAIL (row stays `reconciling` forever).

- [ ] **Step 4: Implement the strand sweeper**

```sql
-- name: ClaimStuckReconciling :many
UPDATE phase_runs SET leased_until = now() + make_interval(secs => @lease_seconds::float8)
WHERE id IN (
    SELECT id FROM phase_runs
    WHERE status = 'reconciling'
      AND leased_until < now() - make_interval(secs => @grace_seconds::float8)
    ORDER BY leased_until
    LIMIT @max_rows FOR UPDATE SKIP LOCKED
)
RETURNING *;
```

(Match the RETURNING column shape to what `MarkPhaseReconciling` returns today so both feed the same `reconcileAttempt`; defer to sqlc's emitted row type.)

Refactor `sweepExpiredLeases`'s per-row body (event append + `Inspect` + `evaluate`) into:

```go
// reconcileAttempt runs reconcile-before-retry for one row already in
// 'reconciling' (spec §3): adopt a completed execution idempotently,
// anything else concludes transient reconcile_lost.
func (o *Orchestrator) reconcileAttempt(ctx context.Context, row store.PhaseRun) { ... }
```

then add to `janitorLoop` (every tick, after `sweepExpiredLeases`):

```go
			if err := o.sweepStuckReconciling(ctx); err != nil {
				o.log.Error("orchestrator: stuck-reconciling sweep", "error", err)
			}
```

```go
// sweepStuckReconciling re-drives reconciling rows whose owner died
// between marking and evaluating (plan 06 carry-forward). The CAS
// lease-extension in ClaimStuckReconciling makes concurrent sweepers
// safe: one winner per row per grace window. Grace = 2× lease TTL.
func (o *Orchestrator) sweepStuckReconciling(ctx context.Context) error {
	rows, err := store.New(o.svc.pool).ClaimStuckReconciling(ctx, store.ClaimStuckReconcilingParams{
		LeaseSeconds: o.cfg.LeaseTTL.Seconds(), GraceSeconds: 2 * o.cfg.LeaseTTL.Seconds(), MaxRows: 16,
	})
	if err != nil {
		return err
	}
	for _, row := range rows {
		o.reconcileAttempt(ctx, row)
	}
	return nil
}
```

Note the skipped-event subtlety: `reconcileAttempt` appends `phase.reconciling` when called from the lease sweeper (first entry into the state). For the strand path the event already exists — pass an `announce bool` to `reconcileAttempt` (lease sweeper `true`, strand sweeper `false`) rather than duplicating the event.

Run: `cd backend && go test -race -tags integration -count=1 ./internal/runs/ -v`
Expected: PASS (all)

- [ ] **Step 5: Commit**

```bash
git add backend/internal/runs/
git commit -m "fix(backend): orchestrator evicts terminal plan cache entries and sweeps stuck reconciling attempts"
```

---
### Task 3: `platform/dockerapi` — the minimal Docker Engine API client

A typed, hand-rolled client over the unix socket: exactly the endpoints the runner needs, pinned API version, stdcopy log demux. No business logic (platform law).

**Files:**
- Create: `backend/internal/platform/dockerapi/client.go`, `backend/internal/platform/dockerapi/types.go`, `backend/internal/platform/dockerapi/stdcopy.go`
- Test: `backend/internal/platform/dockerapi/client_test.go`, `backend/internal/platform/dockerapi/stdcopy_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces (the runner's `Engine` interface in Task 4 mirrors this exactly):

```go
func New(socketPath string) *Client                       // http.Client with unix DialContext; base http://docker/v1.43
func (c *Client) Ping(ctx context.Context) error          // GET /_ping
func (c *Client) ImageInspect(ctx context.Context, ref string) (ImageInspect, error)
func (c *Client) ImagePull(ctx context.Context, ref string) error
func (c *Client) NetworkCreate(ctx context.Context, name string, internal bool, labels map[string]string) (string, error)
func (c *Client) NetworkRemove(ctx context.Context, id string) error
func (c *Client) NetworkInspect(ctx context.Context, name string) (NetworkInspect, error)
func (c *Client) ContainerCreate(ctx context.Context, name string, cfg ContainerConfig, host HostConfig, networks map[string]EndpointSettings) (string, error)
func (c *Client) ContainerStart(ctx context.Context, id string) error
func (c *Client) ContainerWait(ctx context.Context, id string) (int64, error)   // condition=not-running
func (c *Client) ContainerKill(ctx context.Context, id, signal string) error
func (c *Client) ContainerRemove(ctx context.Context, id string, force bool) error // always ?v=1
func (c *Client) ContainerInspect(ctx context.Context, idOrName string) (ContainerInspect, error)
func (c *Client) ContainerLogs(ctx context.Context, id string, follow bool) (io.ReadCloser, error)
func (c *Client) ContainerListByLabel(ctx context.Context, kv string) ([]ContainerSummary, error) // ?all=1&filters={"label":[kv]}
func (c *Client) NetworkListByLabel(ctx context.Context, kv string) ([]NetworkSummary, error)
func IsNotFound(err error) bool
func IsConflict(err error) bool
func DemuxLogs(r io.Reader, stdout, stderr io.Writer) error // stdcopy 8-byte frame protocol
```

Types (only the fields we read — unknown JSON fields ignored on decode, this is a client not a schema):

```go
type ImageInspect struct {
	ID     string `json:"Id"`
	Config struct {
		User string `json:"User"`
	} `json:"Config"`
	RepoDigests []string `json:"RepoDigests"`
}

type ContainerConfig struct {
	Image      string            `json:"Image"`
	Entrypoint []string          `json:"Entrypoint,omitempty"`
	Cmd        []string          `json:"Cmd,omitempty"`
	Env        []string          `json:"Env,omitempty"`
	User       string            `json:"User,omitempty"`
	WorkingDir string            `json:"WorkingDir,omitempty"`
	Labels     map[string]string `json:"Labels,omitempty"`
}

type HostConfig struct {
	Binds          []string          `json:"Binds,omitempty"`
	NetworkMode    string            `json:"NetworkMode,omitempty"`
	CapDrop        []string          `json:"CapDrop,omitempty"`
	SecurityOpt    []string          `json:"SecurityOpt,omitempty"`
	ReadonlyRootfs bool              `json:"ReadonlyRootfs"`
	Tmpfs          map[string]string `json:"Tmpfs,omitempty"`
	Memory         int64             `json:"Memory,omitempty"`
	NanoCpus       int64             `json:"NanoCpus,omitempty"`
	PidsLimit      *int64            `json:"PidsLimit,omitempty"`
	ExtraHosts     []string          `json:"ExtraHosts,omitempty"`
}

type EndpointSettings struct {
	Aliases []string `json:"Aliases,omitempty"`
}

type ContainerInspect struct {
	ID    string `json:"Id"`
	State struct {
		Status     string `json:"Status"` // created|running|paused|restarting|removing|exited|dead
		ExitCode   int64  `json:"ExitCode"`
		OOMKilled  bool   `json:"OOMKilled"`
		Error      string `json:"Error"`
		StartedAt  string `json:"StartedAt"`
		FinishedAt string `json:"FinishedAt"`
	} `json:"State"`
	Config struct {
		Env    []string          `json:"Env"`
		Image  string            `json:"Image"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
}

type ContainerSummary struct {
	ID      string            `json:"Id"`
	Names   []string          `json:"Names"`
	State   string            `json:"State"`
	Created int64             `json:"Created"`
	Labels  map[string]string `json:"Labels"`
}

type NetworkSummary struct {
	ID      string            `json:"Id"`
	Name    string            `json:"Name"`
	Created string            `json:"Created"`
	Labels  map[string]string `json:"Labels"`
}

type NetworkInspect struct {
	ID   string `json:"Id"`
	Name string `json:"Name"`
}
```

- [ ] **Step 1: Write the failing transport + endpoint tests**

The fake daemon: an `httptest.Server` whose listener is a unix socket — this exercises the real transport end to end.

```go
// backend/internal/platform/dockerapi/client_test.go
package dockerapi

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// fakeDaemon serves canned handlers on a unix socket and records requests.
func fakeDaemon(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "docker.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(handler)
	srv.Listener = l
	srv.Start()
	t.Cleanup(srv.Close)
	return sock
}

func TestContainerCreate_SendsHardeningAndName(t *testing.T) {
	var got struct {
		method, path, query string
		body                map[string]any
	}
	sock := fakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		got.method, got.path, got.query = r.Method, r.URL.Path, r.URL.RawQuery
		_ = json.NewDecoder(r.Body).Decode(&got.body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"Id":"abc123"}`))
	})
	c := New(sock)
	pids := int64(512)
	id, err := c.ContainerCreate(t.Context(), "caesar-x-y-1", ContainerConfig{
		Image: "img@sha256:deadbeef", User: "501:20", Labels: map[string]string{"caesar.managed": "true"},
	}, HostConfig{
		CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"},
		ReadonlyRootfs: true, PidsLimit: &pids,
	}, map[string]EndpointSettings{"caesar-x-y-1-net": {Aliases: []string{"egress"}}})
	if err != nil || id != "abc123" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	if got.method != http.MethodPost || got.path != "/v1.43/containers/create" || got.query != "name=caesar-x-y-1" {
		t.Fatalf("request: %s %s?%s", got.method, got.path, got.query)
	}
	host := got.body["HostConfig"].(map[string]any)
	if host["ReadonlyRootfs"] != true {
		t.Fatal("ReadonlyRootfs not sent")
	}
	nets := got.body["NetworkingConfig"].(map[string]any)["EndpointsConfig"].(map[string]any)
	if _, ok := nets["caesar-x-y-1-net"]; !ok {
		t.Fatal("endpoints config missing attempt network")
	}
}

func TestErrors_NotFoundAndConflictAreTyped(t *testing.T) {
	sock := fakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"no such container"}`))
	})
	c := New(sock)
	_, err := c.ContainerInspect(t.Context(), "nope")
	if !IsNotFound(err) {
		t.Fatalf("want typed not-found, got %v", err)
	}
	if IsConflict(err) {
		t.Fatal("not-found must not be conflict")
	}
}

func TestContainerWait_ReturnsStatusCode(t *testing.T) {
	sock := fakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("condition") != "not-running" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"StatusCode":137}`))
	})
	c := New(sock)
	code, err := c.ContainerWait(t.Context(), "abc")
	if err != nil || code != 137 {
		t.Fatalf("code=%d err=%v", code, err)
	}
}

func TestImagePull_SurfacesStreamErrors(t *testing.T) {
	sock := fakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"Pulling"}` + "\n" + `{"errorDetail":{"message":"manifest unknown"},"error":"manifest unknown"}` + "\n"))
	})
	c := New(sock)
	if err := c.ImagePull(t.Context(), "ghcr.io/nope/nope:latest"); err == nil {
		t.Fatal("want error from pull stream")
	}
}

func TestContainerListByLabel_EncodesFilter(t *testing.T) {
	sock := fakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("all") != "1" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var f map[string][]string
		if err := json.Unmarshal([]byte(r.URL.Query().Get("filters")), &f); err != nil || f["label"][0] != "caesar.managed=true" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`[{"Id":"c1","Names":["/caesar-a-b-1"],"State":"exited","Created":100}]`))
	})
	c := New(sock)
	list, err := c.ContainerListByLabel(t.Context(), "caesar.managed=true")
	if err != nil || len(list) != 1 || list[0].ID != "c1" {
		t.Fatalf("list=%v err=%v", list, err)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd backend && go test ./internal/platform/dockerapi/ -v`
Expected: FAIL (package empty)

- [ ] **Step 3: Implement the client**

`client.go` core shape (the rest of the endpoints are mechanical repetitions of `do`):

```go
// Package dockerapi is a minimal, typed Docker Engine API client over
// the unix socket — exactly the endpoints the runner consumes, API
// version pinned. Hand-rolled to honor the dependency deny-list: the
// official SDK drags the moby module graph for what is, here, a dozen
// HTTP calls (plan 06 decision 1).
package dockerapi

const apiVersion = "v1.43"

type Client struct {
	http *http.Client
}

func New(socketPath string) *Client {
	return &Client{http: &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
	}}
}

type apiError struct {
	Status  int
	Message string
}

func (e *apiError) Error() string { return fmt.Sprintf("docker api: %d: %s", e.Status, e.Message) }

func IsNotFound(err error) bool { var ae *apiError; return errors.As(err, &ae) && ae.Status == 404 }
func IsConflict(err error) bool { var ae *apiError; return errors.As(err, &ae) && ae.Status == 409 }

// do issues one request; 2xx decodes into out (when non-nil), anything
// else becomes a typed apiError carrying the daemon's message.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, in, out any) error {
	u := "http://docker/" + apiVersion + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("docker api: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var msg struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&msg)
		return &apiError{Status: resp.StatusCode, Message: msg.Message}
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}
```

Endpoint notes (implement each as a thin `do` call):
- `ContainerCreate`: POST `/containers/create?name=`, body `{**ContainerConfig fields inline**, "HostConfig": ..., "NetworkingConfig": {"EndpointsConfig": networks}}` — build with an anonymous struct embedding `ContainerConfig`.
- `ContainerWait`: POST `/containers/{id}/wait?condition=not-running`, decode `{StatusCode, Error{Message}}`; non-empty `Error.Message` → error. **No client timeout may apply** — the shared transport has none and the request context carries the deadline; that is deliberate, document it.
- `ContainerLogs`: GET `/containers/{id}/logs?stdout=1&stderr=1&follow=0|1&timestamps=0`; return `resp.Body` directly (caller demuxes + closes). This one bypasses `do` (streaming).
- `ImagePull`: POST `/images/create?fromImage=<ref>` — the body is a JSONL progress stream; decode line-objects until EOF; any line with non-empty `error` fails the pull. Bypasses `do`.
- `NetworkCreate`: POST `/networks/create` body `{"Name", "Internal", "Labels", "CheckDuplicate": true}`.
- `ContainerRemove`: DELETE `/containers/{id}?v=1&force=0|1`.
- `ContainerKill`: POST `/containers/{id}/kill?signal=`.
- List endpoints: `filters` = `url.Values{"filters": {json.Marshal(map[string][]string{"label": {kv}})}}`, plus `all=1` for containers.

- [ ] **Step 4: Run to verify pass**

Run: `cd backend && go test ./internal/platform/dockerapi/ -run 'TestContainer|TestErrors|TestImage' -v`
Expected: PASS

- [ ] **Step 5: stdcopy demux — failing test, then implement**

```go
// backend/internal/platform/dockerapi/stdcopy_test.go
package dockerapi

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func frame(stream byte, payload string) []byte {
	h := make([]byte, 8)
	h[0] = stream
	binary.BigEndian.PutUint32(h[4:], uint32(len(payload)))
	return append(h, payload...)
}

func TestDemuxLogs_SplitsStreamsAcrossChunkedFrames(t *testing.T) {
	var in bytes.Buffer
	in.Write(frame(1, "out-1 "))
	in.Write(frame(2, "err-1 "))
	in.Write(frame(1, "out-2"))
	var stdout, stderr strings.Builder
	// Feed one byte at a time to prove header/payload reassembly.
	if err := DemuxLogs(iotest.OneByteReader(&in), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "out-1 out-2" || stderr.String() != "err-1 " {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestDemuxLogs_RejectsUnknownStreamByte(t *testing.T) {
	if err := DemuxLogs(bytes.NewReader(frame(7, "x")), io.Discard, io.Discard); err == nil {
		t.Fatal("want error on unknown stream id")
	}
}
```

```go
// stdcopy.go — the daemon multiplexes stdout/stderr over one connection
// when TTY is off: 8-byte header {stream, 0,0,0, len:be32} then payload.
func DemuxLogs(r io.Reader, stdout, stderr io.Writer) error {
	head := make([]byte, 8)
	buf := make([]byte, 32*1024)
	for {
		if _, err := io.ReadFull(r, head); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("dockerapi: log frame header: %w", err)
		}
		var w io.Writer
		switch head[0] {
		case 0, 1:
			w = stdout
		case 2:
			w = stderr
		default:
			return fmt.Errorf("dockerapi: unknown log stream %d", head[0])
		}
		if _, err := io.CopyBuffer(w, io.LimitReader(r, int64(binary.BigEndian.Uint32(head[4:]))), buf); err != nil {
			return fmt.Errorf("dockerapi: log frame payload: %w", err)
		}
	}
}
```

(`io.ErrUnexpectedEOF` mid-header/payload is an error — a torn stream must not pass silently. `iotest` is `testing/iotest`.)

Run: `cd backend && go test ./internal/platform/dockerapi/ -v`
Expected: PASS

- [ ] **Step 6: Lint, analyze, commit**

Run: `cd backend && task backend:lint backend:analyze`
Expected: green (note: platform may not import bounded contexts — this package imports stdlib only, keeping the arch tests happy by construction)

```bash
git add backend/internal/platform/dockerapi/
git commit -m "feat(backend): minimal docker engine api client with stdcopy demux"
```

---
### Task 4: Images, runner skeleton, and the RunSpec builder

The `caesar-git` image stage, the `internal/runs/runner` package skeleton (options, ports, naming, paths), the broker token, the free-space admission check, and the pure RunSpec builder (digest pinning, constructed env, limits).

**Files:**
- Modify: `Dockerfile` (new `git` stage), `Taskfile.yml` (`image:build` builds both)
- Create: `backend/internal/runs/runner/runner.go` (options, ports, constructor), `backend/internal/runs/runner/naming.go`, `backend/internal/runs/runner/spec.go`, `backend/internal/runs/runner/token.go`, `backend/internal/runs/runner/admission.go`, `backend/internal/runs/runner/statfs_unix.go`
- Test: `backend/internal/runs/runner/naming_test.go`, `backend/internal/runs/runner/spec_test.go`, `backend/internal/runs/runner/token_test.go`, `backend/internal/runs/runner/admission_test.go`

**Interfaces:**
- Consumes: Task 1's vocabulary, Task 3's `dockerapi` types.
- Produces (Tasks 8–12 rely on these exact names):

```go
// Engine is the runner's port onto the container runtime; dockerapi.Client
// satisfies it, unit tests fake it. Method set = Task 3's client minus Ping.
type Engine interface {
	ImageInspect(ctx context.Context, ref string) (dockerapi.ImageInspect, error)
	ImagePull(ctx context.Context, ref string) error
	NetworkCreate(ctx context.Context, name string, internal bool, labels map[string]string) (string, error)
	NetworkRemove(ctx context.Context, id string) error
	NetworkInspect(ctx context.Context, name string) (dockerapi.NetworkInspect, error)
	ContainerCreate(ctx context.Context, name string, cfg dockerapi.ContainerConfig, host dockerapi.HostConfig, networks map[string]dockerapi.EndpointSettings) (string, error)
	ContainerStart(ctx context.Context, id string) error
	ContainerWait(ctx context.Context, id string) (int64, error)
	ContainerKill(ctx context.Context, id, signal string) error
	ContainerRemove(ctx context.Context, id string, force bool) error
	ContainerInspect(ctx context.Context, idOrName string) (dockerapi.ContainerInspect, error)
	ContainerLogs(ctx context.Context, id string, follow bool) (io.ReadCloser, error)
	ContainerListByLabel(ctx context.Context, kv string) ([]dockerapi.ContainerSummary, error)
	NetworkListByLabel(ctx context.Context, kv string) ([]dockerapi.NetworkSummary, error)
}

type HarnessSpec struct {
	Kind             string
	ImageRef         string
	ProviderEndpoint string
	CredentialID     uuid.UUID
	Entrypoint       []string // nil in production (image default); test resolvers script it
	Cmd              []string
}
type HarnessResolver interface {
	HarnessSpec(ctx context.Context, id uuid.UUID) (HarnessSpec, error)
}
type CredentialOpener interface {
	OpenCredential(ctx context.Context, id uuid.UUID, purpose string) ([]byte, error)
}
type CloneSpec struct {
	URL        string // credential-free always (token-in-URL banned)
	AuthHeader string // full header line value for http.extraheader, "" = none
	BindDir    string // non-empty = mount RO at /remote (test fixtures; URL then file:///remote/...)
}
type GitSource interface {
	CloneSpec(ctx context.Context, repo runs.RepoRef) (CloneSpec, error)
}
type EventSink interface {
	AppendRuntimeEvent(ctx context.Context, ref runs.AttemptRef, kind string, payload map[string]any) error
}

type Options struct { // zero fields take the pinned defaults from the Global Constraints table
	DataRoot, DataHostRoot, CaesarImage, GitImage, EgressNetwork string
	WorkspaceQuotaBytes, WorkspaceQuotaFiles, LogCapBytes, LogFloodBytes int64
	FreeBytesWatermark, MemoryBytes, NanoCPUs, PidsLimit int64
	QuotaPollInterval time.Duration
	ConnectAllow []string
}

func New(engine Engine, harnesses HarnessResolver, creds CredentialOpener, git GitSource,
	events EventSink, opts Options, log *slog.Logger) (*Runner, error)
// New fails when os.Getuid() == 0 (decision 4) or CaesarImage/GitImage are empty.

func (r *Runner) AdmitClaim(ctx context.Context) error // satisfies runs.Admission

// naming.go
func agentName(ref runs.AttemptRef) string      // ref.String()
func gatewayName(ref runs.AttemptRef) string    // + "-egress"
func stageName(ref runs.AttemptRef) string      // + "-stage"
func extractName(ref runs.AttemptRef) string    // + "-extract"
func validateName(ref runs.AttemptRef) string   // + "-validate"
func networkName(ref runs.AttemptRef) string    // + "-net"
func labels(ref runs.AttemptRef) map[string]string
func (r *Runner) attemptDir(ref runs.AttemptRef) string  // DataRoot/attempts/<ref>
func (r *Runner) logsDir(ref runs.AttemptRef) string     // DataRoot/logs/<ref>
func (r *Runner) hostPath(p string) string               // DataRoot prefix -> DataHostRoot

// token.go
func newBrokerToken() (token string, sha256hex string, err error) // "cbt1." + base64url(24B)

// spec.go
type RunSpec struct {
	Ref             runs.AttemptRef
	ImageDigest     string
	Harness         HarnessSpec
	User            string   // "<uid>:<gid>" of this process
	Env             []string // the pinned allowlist
	BrokerToken     string
	BrokerTokenSHA  string
	ProviderOps     []egress.Op        // from the pinned per-kind table (Task 7 defines egress.Op)
	CredentialHeader, CredentialPrefix string
	Deadline        time.Time
}
func (r *Runner) buildSpec(ctx context.Context, req runs.ExecutionRequest, hs HarnessSpec) (RunSpec, error)
func providerProfile(kind string) (header, prefix string, ops []egress.Op, err error) // pinned table
```

- [ ] **Step 1: Dockerfile `git` stage and `image:build`**

Insert between the `build` stage and the final stage (the final stage must remain last so a bare `docker build .` still produces the runtime image):

```dockerfile
FROM alpine:3.22 AS git
RUN apk add --no-cache git
COPY --from=build /out/caesar /usr/local/bin/caesar
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/caesar"]
```

Taskfile `image:build` gains a second build (same args, shared cache):

```yaml
      - docker build --build-arg VERSION={{.VERSION}} --build-arg COMMIT={{.COMMIT}} --build-arg DATE={{.DATE}} -t caesar:dev .
      - docker build --target git --build-arg VERSION={{.VERSION}} --build-arg COMMIT={{.COMMIT}} --build-arg DATE={{.DATE}} -t caesar-git:dev .
```

Run: `task image:build`
Expected: both images build; `docker run --rm caesar-git:dev version` prints build info; `docker run --rm --entrypoint git caesar-git:dev --version` prints a git version.

- [ ] **Step 2: Failing tests — naming, token, admission**

```go
// naming_test.go
func TestNaming_AllDerivedFromAttemptRef(t *testing.T) {
	ref := runs.AttemptRef{RunID: uuid.MustParse("018f0000-0000-7000-8000-000000000001"), NodeID: "implement", Attempt: 2}
	base := ref.String()
	if agentName(ref) != base || gatewayName(ref) != base+"-egress" || networkName(ref) != base+"-net" {
		t.Fatal("names must derive from the attempt identity")
	}
	l := labels(ref)
	if l["caesar.managed"] != "true" || l["caesar.run"] != ref.RunID.String() || l["caesar.node"] != "implement" || l["caesar.attempt"] != "2" {
		t.Fatalf("labels = %v", l)
	}
}

func TestHostPath_MapsDataRootPrefix(t *testing.T) {
	r := &Runner{opts: Options{DataRoot: "/var/lib/caesar", DataHostRoot: "/srv/caesar-data"}}
	if got := r.hostPath("/var/lib/caesar/attempts/x/workspace"); got != "/srv/caesar-data/attempts/x/workspace" {
		t.Fatalf("hostPath = %s", got)
	}
}
```

```go
// token_test.go
func TestBrokerToken_FormatAndEntropy(t *testing.T) {
	tok, sha, err := newBrokerToken()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok, "cbt1.") || len(tok) != len("cbt1.")+32 { // 24B base64url = 32 chars
		t.Fatalf("token = %q", tok)
	}
	sum := sha256.Sum256([]byte(tok))
	if sha != hex.EncodeToString(sum[:]) {
		t.Fatal("sha mismatch")
	}
	tok2, _, _ := newBrokerToken()
	if tok == tok2 {
		t.Fatal("tokens must be random")
	}
}
```

```go
// admission_test.go — statfs is injectable so the watermark logic is unit-testable
func TestAdmitClaim_RefusesUnderWatermark(t *testing.T) {
	r := &Runner{opts: Options{FreeBytesWatermark: 10 << 30, DataRoot: "/x"}, log: slog.New(slog.NewJSONHandler(io.Discard, nil))}
	r.freeBytes = func(string) (int64, error) { return 5 << 30, nil }
	if err := r.AdmitClaim(t.Context()); err == nil {
		t.Fatal("want refusal under watermark")
	}
	r.freeBytes = func(string) (int64, error) { return 50 << 30, nil }
	if err := r.AdmitClaim(t.Context()); err != nil {
		t.Fatalf("want admit above watermark: %v", err)
	}
}
```

Run: `cd backend && go test ./internal/runs/runner/ -v` — Expected: FAIL (package empty)

- [ ] **Step 3: Implement skeleton, naming, token, admission**

`runner.go` holds `type Runner struct { engine Engine; harnesses HarnessResolver; creds CredentialOpener; git GitSource; events EventSink; opts Options; log *slog.Logger; uid, gid int; freeBytes func(string) (int64, error) }`, `Options.withDefaults()` filling every zero field from the pinned table, and `New` validating (non-root, images set, `DataHostRoot` defaulted to `DataRoot`, flood ≥ cap). `admission.go`:

```go
// AdmitClaim is the free-space watermark (spec §8): typed backpressure
// BEFORE a claim exists, so refusal costs no attempt and no lease.
func (r *Runner) AdmitClaim(context.Context) error {
	free, err := r.freeBytes(r.opts.DataRoot)
	if err != nil {
		return fmt.Errorf("runner: statfs %s: %w", r.opts.DataRoot, err)
	}
	if free < r.opts.FreeBytesWatermark {
		return fmt.Errorf("runner: %d bytes free on %s, watermark %d", free, r.opts.DataRoot, r.opts.FreeBytesWatermark)
	}
	return nil
}
```

`statfs_unix.go` (build tag `//go:build linux || darwin`): `func diskFreeBytes(path string) (int64, error)` via `syscall.Statfs` (`int64(st.Bavail) * int64(st.Bsize)`); `New` sets `freeBytes: diskFreeBytes`.

Run the Step 2 tests. Expected: PASS

- [ ] **Step 4: Failing spec-builder tests**

```go
// spec_test.go
type fakeEngine struct {
	Engine // embed nil; only override what the test needs — calls to the rest panic loudly
	inspect func(string) (dockerapi.ImageInspect, error)
	pulled  []string
}

func (f *fakeEngine) ImageInspect(_ context.Context, ref string) (dockerapi.ImageInspect, error) {
	return f.inspect(ref)
}
func (f *fakeEngine) ImagePull(_ context.Context, ref string) error {
	f.pulled = append(f.pulled, ref)
	return nil
}

func TestBuildSpec_PinsDigestAndConstructsEnv(t *testing.T) {
	missOnce := true
	eng := &fakeEngine{inspect: func(ref string) (dockerapi.ImageInspect, error) {
		if missOnce { // absent locally: buildSpec pulls, then re-inspects
			missOnce = false
			return dockerapi.ImageInspect{}, errors.New("no such image")
		}
		return dockerapi.ImageInspect{RepoDigests: []string{"ghcr.io/x/claude@sha256:beef"}}, nil
	}}
	r := newSpecRunner(t, eng) // helper wiring fakes + Options{CaesarImage: "caesar:dev", GitImage: "caesar-git:dev"}
	spec, err := r.buildSpec(t.Context(), runs.ExecutionRequest{
		Ref:  testRef(), Kind: runs.KindImplement, HarnessID: uuid.New(),
		Repo: runs.RepoRef{Forge: "github", Slug: "giiorg/demo"}, BaseSHA: "abc",
	}, HarnessSpec{Kind: "claude-code", ImageRef: "ghcr.io/x/claude:latest", ProviderEndpoint: "https://api.anthropic.com"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.ImageDigest != "ghcr.io/x/claude@sha256:beef" {
		t.Fatalf("digest = %s", spec.ImageDigest)
	}
	if len(eng.pulled) != 1 {
		t.Fatal("image should have been pulled once")
	}
	envMap := envToMap(spec.Env)
	for _, k := range []string{"CAESAR_ATTEMPT", "CAESAR_WORKSPACE", "CAESAR_BROKER_URL", "CAESAR_BROKER_TOKEN", "HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "HOME", "PATH"} {
		if _, ok := envMap[k]; !ok {
			t.Errorf("env missing %s", k)
		}
	}
	if len(spec.Env) != 9 {
		t.Fatalf("env allowlist must be EXACTLY the pinned nine, got %v", spec.Env)
	}
	if spec.CredentialHeader != "x-api-key" || spec.CredentialPrefix != "" {
		t.Fatal("claude-code credential profile wrong")
	}
}

func TestBuildSpec_AlreadyDigestPinnedSkipsPull(t *testing.T) { /* inspect returns image; assert no pull, digest = the given ref */ }

func TestBuildSpec_PullFailureClassifiesImageUnavailable(t *testing.T) {
	// engine: inspect always errors, pull returns error →
	// assert runs.OutcomeError{Class: transient, Code: image_unavailable}
}

func TestProviderProfile_UnknownKindRejected(t *testing.T) { /* "mystery" → error */ }
```

Run: expected FAIL.

- [ ] **Step 5: Implement `spec.go`**

Digest resolution: inspect the ref; on **any** inspect error, `ImagePull` then inspect once more (this keeps the code free of daemon-error-shape guessing — a genuinely broken daemon surfaces on the second inspect). Then: `ImageRef` already contains `@sha256:` → use it as-is; else take `RepoDigests[0]`; empty `RepoDigests` (locally built, never pushed — the test images!) → fall back to the ref itself and set `spec.ImageDigest = ImageRef`, documenting in the field comment that dev images carry no registry digest. A failed pull or a failed second inspect → `runs.OutcomeError{Outcome: runs.Outcome{Class: runs.OutcomeTransient, Code: runs.CodeImageUnavailable, Message: ...}}`. Env construction exactly the pinned nine (broker URL fixed `http://egress:8080`). `providerProfile`:

```go
func providerProfile(kind string) (header, prefix string, ops []egress.Op, err error) {
	switch kind {
	case "claude-code":
		return "x-api-key", "", []egress.Op{
			{Method: "POST", PathPrefix: "/v1/messages"},
			{Method: "POST", PathPrefix: "/v1/messages/count_tokens"},
			{Method: "GET", PathPrefix: "/v1/models"},
		}, nil
	case "codex":
		return "authorization", "Bearer ", []egress.Op{
			{Method: "POST", PathPrefix: "/v1/responses"},
			{Method: "POST", PathPrefix: "/v1/chat/completions"},
			{Method: "GET", PathPrefix: "/v1/models"},
		}, nil
	}
	return "", "", nil, fmt.Errorf("runner: unknown harness kind %q", kind)
}
```

(`/v1/messages` prefix-matches `/v1/messages/count_tokens` too — keep both rows anyway: the table is documentation, and prefix semantics may tighten in Plan 09.)

Note: `egress.Op` doesn't exist until Task 7 — define it there; for THIS task declare the struct in a small `backend/internal/runs/egress/egress.go` stub containing only `type Op struct { Method string \`json:"method"\`; PathPrefix string \`json:"path_prefix"\` }` with a package comment saying Task 7 fills the package in. (Tasks are ordered; if executing out of order, Task 7 already provides it.)

Run: `cd backend && go test ./internal/runs/runner/ -v`
Expected: PASS

- [ ] **Step 6: Gate and commit**

Run: `cd backend && task backend:test backend:lint backend:analyze`
Expected: green

```bash
git add Dockerfile Taskfile.yml backend/internal/runs/runner/ backend/internal/runs/egress/
git commit -m "feat(backend): caesar-git image, runner skeleton, runspec builder with digest pinning"
```

---
### Task 5: The clone staging contract — `gitops.Stage` and `caesar stage`

Clone at a pinned SHA through an in-memory credential path, then sanitize and verify before any agent starts (spec §3 isolation properties). Pure Go driving the `git` binary — unit-tested natively (dev and CI have git), containerized by Task 8.

**Files:**
- Create: `backend/internal/runs/runner/gitops/gitops.go` (shared exec-git helper + constructed env), `backend/internal/runs/runner/gitops/stage.go`, `backend/internal/app/stage.go` (subcommand)
- Modify: `backend/internal/app/main.go` (dispatch `stage`), `backend/internal/platform/config/config.go` (`StageEnvFromProcess`)
- Test: `backend/internal/runs/runner/gitops/stage_test.go`

**Interfaces:**
- Consumes: `fake.InitBareRepo` (test fixtures), Task 1's outcome codes.
- Produces:

```go
// gitops.go
type TypedError struct{ Outcome runs.Outcome }        // marshals into report files; runner re-wraps as OutcomeError
func (e TypedError) Error() string
func runGit(ctx context.Context, dir string, env []string, args ...string) (string, error) // constructed env ONLY
func baseEnv(homeDir string) []string                 // PATH, HOME, GIT_CONFIG_GLOBAL=/dev/null, GIT_CONFIG_NOSYSTEM=1, GIT_TERMINAL_PROMPT=0

// stage.go
type StageInput struct {
	CloneURL string `json:"clone_url"`
	BaseSHA  string `json:"base_sha"`
	QuotaBytes int64 `json:"quota_bytes"`
	QuotaFiles int64 `json:"quota_files"`
}
type StageReport struct {
	Outcome  *runs.Outcome `json:"outcome,omitempty"` // set on typed failure
	HeadSHA  string        `json:"head_sha,omitempty"`
	Files    int64         `json:"files,omitempty"`
	Bytes    int64         `json:"bytes,omitempty"`
	Verified bool          `json:"verified,omitempty"`
}
func Stage(ctx context.Context, dir string, in StageInput, authEnv []string) (StageReport, error)
// dir gains base.git/ and workspace/ on success.

// config addition (envaccess law: only config touches the process env)
func StageEnvFromProcess() []string // forwards GIT_CONFIG_COUNT/GIT_CONFIG_KEY_0/GIT_CONFIG_VALUE_0 when set
```

Subcommand contract: `caesar stage` reads `/attempt/stage.json`, calls `Stage(ctx, "/attempt", in, config.StageEnvFromProcess())`, writes `/attempt/stage.report.json`; `TypedError` → report with `outcome` + exit 1; other errors → outcome `{permanent, staging_failed... }` — no: unexpected errors report `{transient, staging_failed}` (a daemon hiccup must not kill a run; the verify failures below are explicitly permanent) + exit 1.

**The contract being implemented** (test names must cover every clause):
1. Auth: never in the URL (userinfo in `CloneURL` → typed `staging_verification_failed` before any git runs), only via env config `http.extraheader` — which git never persists to disk.
2. Clone: `git clone --bare <url> base.git` (no submodule recursion — bare clones have none; tags come along, harmless), then verify the pinned SHA: `git -C base.git cat-file -e <sha>^{commit}` → miss = typed `base_sha_unavailable` (permanent — the pin is gone, retrying cannot help).
3. Materialize: `git clone --no-hardlinks <dir>/base.git workspace` + `git -C workspace checkout --detach <sha>` (a real local `.git` so harnesses can run git; it is hostile-by-assumption from here on and extraction never trusts it).
4. Sanitize: `git -C workspace remote set-url origin <CloneURL>` (credential-free by clause 1); assert neither config contains `extraheader`, `askpass`, or `credential` keys (`git config --list --local` scan).
5. Verify (negative scan): no `.git-credentials` anywhere under `dir`; when `authEnv` carries a credential, its header value must appear in **no file** under `base.git` and `workspace/.git` (excluding `objects/` — packfiles hold repo content, not clone metadata); count workspace files+bytes (excluding `.git`) and fail typed `workspace_quota_exceeded` if the clone alone breaches the quota. Any verify failure = `staging_verification_failed` (permanent) — **a workspace that fails verification is never handed to an agent.**

- [ ] **Step 1: Failing tests**

```go
// stage_test.go (native git; no containers, no build tags)
package gitops

func stageFixture(t *testing.T) (remoteDir, headSHA string) {
	t.Helper()
	dir := t.TempDir()
	sha, err := fake.InitBareRepo(dir)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "bare.git"), sha
}

func TestStage_HappyPath_ClonesSanitizesVerifies(t *testing.T) {
	remote, sha := stageFixture(t)
	dir := t.TempDir()
	rep, err := Stage(t.Context(), dir, StageInput{CloneURL: remote, BaseSHA: sha, QuotaBytes: 1 << 30, QuotaFiles: 1000}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Verified || rep.HeadSHA != sha || rep.Files < 1 {
		t.Fatalf("report = %+v", rep)
	}
	// Workspace is detached at the pinned SHA.
	out, err := runGit(t.Context(), dir, baseEnv(dir), "-C", filepath.Join(dir, "workspace"), "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(out) != sha {
		t.Fatalf("workspace head = %q err=%v", out, err)
	}
}

func TestStage_TokenInURLRefusedBeforeAnyGitRuns(t *testing.T) {
	dir := t.TempDir()
	_, err := Stage(t.Context(), dir, StageInput{CloneURL: "https://x-access-token:sekret@github.com/a/b.git", BaseSHA: "abc", QuotaBytes: 1, QuotaFiles: 1}, nil)
	var te TypedError
	if !errors.As(err, &te) || te.Outcome.Code != runs.CodeStagingVerificationFailed {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "base.git")); !os.IsNotExist(statErr) {
		t.Fatal("no clone may happen with a token-bearing URL")
	}
}

func TestStage_MissingBaseSHAIsTyped(t *testing.T) {
	remote, _ := stageFixture(t)
	_, err := Stage(t.Context(), t.TempDir(), StageInput{CloneURL: remote, BaseSHA: strings.Repeat("a", 40), QuotaBytes: 1 << 30, QuotaFiles: 1000}, nil)
	var te TypedError
	if !errors.As(err, &te) || te.Outcome.Code != runs.CodeBaseSHAUnavailable || te.Outcome.Class != runs.OutcomePermanent {
		t.Fatalf("err = %v", err)
	}
}

func TestStage_CloneFailureIsTransientStagingFailed(t *testing.T) {
	_, err := Stage(t.Context(), t.TempDir(), StageInput{CloneURL: filepath.Join(t.TempDir(), "nope.git"), BaseSHA: "abc", QuotaBytes: 1, QuotaFiles: 1}, nil)
	var te TypedError
	if !errors.As(err, &te) || te.Outcome.Code != runs.CodeStagingFailed || te.Outcome.Class != runs.OutcomeTransient {
		t.Fatalf("err = %v", err)
	}
}

func TestVerify_PlantedCredentialIsCaught(t *testing.T) {
	remote, sha := stageFixture(t)
	dir := t.TempDir()
	secret := "Authorization: Basic c2VrcmV0"
	// Stage clean first, then plant the secret and re-verify directly.
	if _, err := Stage(t.Context(), dir, StageInput{CloneURL: remote, BaseSHA: sha, QuotaBytes: 1 << 30, QuotaFiles: 1000}, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "base.git", "FETCH_HEAD"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	err := verifyStaging(dir, secret, 1<<30, 1000)
	var te TypedError
	if !errors.As(err, &te) || te.Outcome.Code != runs.CodeStagingVerificationFailed {
		t.Fatalf("planted credential survived verification: %v", err)
	}
}

func TestStage_CloneExceedingQuotaIsTyped(t *testing.T) {
	remote, sha := stageFixture(t)
	_, err := Stage(t.Context(), t.TempDir(), StageInput{CloneURL: remote, BaseSHA: sha, QuotaBytes: 1, QuotaFiles: 1000}, nil)
	var te TypedError
	if !errors.As(err, &te) || te.Outcome.Code != runs.CodeWorkspaceQuotaExceeded {
		t.Fatalf("err = %v", err)
	}
}

func TestStage_AuthHeaderNeverPersistsToDisk(t *testing.T) {
	remote, sha := stageFixture(t)
	dir := t.TempDir()
	authEnv := []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.extraheader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic ZmFrZXRva2Vu",
	}
	rep, err := Stage(t.Context(), dir, StageInput{CloneURL: remote, BaseSHA: sha, QuotaBytes: 1 << 30, QuotaFiles: 1000}, authEnv)
	if err != nil || !rep.Verified {
		t.Fatalf("rep=%+v err=%v", rep, err)
	}
	// The verify scan ran with the real header value — belt and braces here:
	found := grepTree(t, dir, "ZmFrZXRva2Vu")
	if len(found) != 0 {
		t.Fatalf("credential persisted in: %v", found)
	}
}
```

(`grepTree` is a small test helper walking every regular file. `verifyStaging(dir, credential, quotaBytes, quotaFiles) error` is the exported-for-test unexported seam — the same function `Stage` calls.)

Run: `cd backend && go test ./internal/runs/runner/gitops/ -v` — Expected: FAIL

- [ ] **Step 2: Implement `gitops.go` + `stage.go`**

`runGit` mirrors `fake.InitBareRepo`'s discipline — `exec.CommandContext`, `cmd.Env = env` (constructed, never inherited), `CombinedOutput`, errors wrap args + output. `baseEnv(home)`:

```go
func baseEnv(home string) []string {
	gitBin, _ := exec.LookPath("git")
	return []string{
		"PATH=" + filepath.Dir(gitBin),
		"HOME=" + home,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
	}
}
```

`Stage` implements the six contract clauses in order; classification: URL userinfo (`url.Parse` → `u.User != nil`) and every verify miss → `staging_verification_failed` (except the pinned-SHA miss → `base_sha_unavailable` and the quota breach → `workspace_quota_exceeded`); clone/checkout process failures → transient `staging_failed`. The credential value scanned for is extracted from `authEnv`'s `GIT_CONFIG_VALUE_0=` entry when present. The workspace walk is shared with Task 8's watchdog — put it here:

```go
// WalkStats bounds a directory walk: it stops early once either cap is
// exceeded (never an unbounded traversal, spec §3 collection).
type WalkStats struct {
	Files, Bytes int64
	Breached     bool
}
func BoundedWalk(dir string, skipTopGit bool, maxFiles, maxBytes int64) (WalkStats, error)
```

- [ ] **Step 3: Run tests to pass**

Run: `cd backend && go test ./internal/runs/runner/gitops/ -v`
Expected: PASS

- [ ] **Step 4: The `stage` subcommand**

`backend/internal/app/stage.go`:

```go
// runStageCmd is the trusted stage container's entrypoint: read
// /attempt/stage.json, stage under the contract, write the report.
// The forge credential arrives ONLY via GIT_CONFIG_* env (config pkg).
func runStageCmd(stdout, stderr io.Writer) int {
	in, err := readJSONFile[gitops.StageInput]("/attempt/stage.json")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	rep, err := gitops.Stage(ctx, "/attempt", in, config.StageEnvFromProcess())
	if err != nil {
		var te gitops.TypedError
		if !errors.As(err, &te) {
			te.Outcome = runs.Outcome{Class: runs.OutcomeTransient, Code: runs.CodeStagingFailed, Message: err.Error()}
		}
		rep = gitops.StageReport{Outcome: &te.Outcome}
		_ = writeJSONFile("/attempt/stage.report.json", rep)
		return 1
	}
	if err := writeJSONFile("/attempt/stage.report.json", rep); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	return 0
}
```

(`readJSONFile`/`writeJSONFile` are small strict-decode generics in the same file — `DisallowUnknownFields`, 1 MiB read cap. `app` importing `runs` + `gitops` is composition-root-legal.) Add `case "stage":` to `app.Main`'s dispatch and one line to its usage text. `config.StageEnvFromProcess`:

```go
// StageEnvFromProcess forwards the git auth env config into a
// constructed child env (envaccess law: this package alone reads env).
func StageEnvFromProcess() []string {
	var out []string
	for _, k := range []string{"GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0"} {
		if v := os.Getenv(k); v != "" {
			out = append(out, k+"="+v)
		}
	}
	return out
}
```

Test (in `main_test.go` style — table entry asserting `caesar stage` with a missing input file exits 2). 

Run: `cd backend && go test ./internal/app/ -run TestMain -v && task backend:analyze`
Expected: PASS (envaccess stays green — env reads live in config only)

- [ ] **Step 5: Commit**

```bash
git add backend/internal/runs/runner/gitops/ backend/internal/app/ backend/internal/platform/config/
git commit -m "feat(backend): clone staging contract with sanitize-then-verify and the stage subcommand"
```

---

### Task 6: Hostile-`.git` extraction — `gitops.Extract` and `caesar extract`

The canonical patch, computed without ever executing git against the agent's `.git` (spec §3 collection): trusted `GIT_DIR=base.git`, the workspace as a plain work tree, a temp index, bounded traversal, gitlinks rejected.

**Files:**
- Create: `backend/internal/runs/runner/gitops/extract.go`, `backend/internal/app/extract.go`
- Modify: `backend/internal/app/main.go` (dispatch `extract`)
- Test: `backend/internal/runs/runner/gitops/extract_test.go`

**Interfaces:**
- Consumes: Task 5's `runGit`/`baseEnv`/`BoundedWalk`/`TypedError`, a staged attempt dir (`base.git` + `workspace`).
- Produces:

```go
type ExtractInput struct {
	BaseSHA       string `json:"base_sha"`
	MaxFiles      int64  `json:"max_files"`
	MaxBytes      int64  `json:"max_bytes"`
	MaxPatchBytes int64  `json:"max_patch_bytes"`
}
type ExtractReport struct {
	Outcome  *runs.Outcome `json:"outcome,omitempty"`
	Diff     string        `json:"diff,omitempty"`
	BaseSHA  string        `json:"base_sha,omitempty"`
	TreeHash string        `json:"tree_hash,omitempty"`
	Changed  bool          `json:"changed"`
}
func Extract(ctx context.Context, dir string, in ExtractInput) (ExtractReport, error)
```

**Mechanics (binding):** every git call runs with `baseEnv` + `GIT_DIR=<dir>/base.git`, `GIT_WORK_TREE=<dir>/workspace`, `GIT_INDEX_FILE=<dir>/extract.index` — the workspace's own `.git` is never the git dir, so its config/hooks/fsmonitor are inert data (a directory literally named `.git` is also never added by `git add`). Sequence: (1) `BoundedWalk(workspace, skipTopGit=true, MaxFiles, MaxBytes)` → breach = typed `patch_too_large`; (2) `git read-tree <BaseSHA>`; (3) `git add -A`; (4) `git ls-files -s` → any mode `160000` entry = typed `embedded_repo` (a nested repo became a gitlink; spec: gitlinks rejected); (5) `git write-tree` → `TreeHash`; (6) `Changed = TreeHash != base tree` (`git rev-parse <BaseSHA>^{tree}`); (7) when changed: `git diff-tree -p --binary --full-index -M50 <BaseSHA> <TreeHash>` → `Diff`, length > `MaxPatchBytes` = typed `patch_too_large`. Mechanical git failures = typed `extraction_failed` (permanent — same inputs will fail the same way). New blobs written by `add` land in `base.git/objects` (trusted dir; the attempt dir is deleted at teardown, so no GC concern).

- [ ] **Step 1: Failing tests — the hostile suite**

```go
// extract_test.go (native git)
// stagedDir(t) stages the fixture via Task 5's Stage and returns (dir, baseSHA).

func TestExtract_NoChangesYieldsUnchanged(t *testing.T) {
	dir, sha := stagedDir(t)
	rep, err := Extract(t.Context(), dir, defaultCaps(sha))
	if err != nil || rep.Changed {
		t.Fatalf("rep=%+v err=%v", rep, err)
	}
}

func TestExtract_ModifiedAndNewFilesProduceApplyingPatch(t *testing.T) {
	dir, sha := stagedDir(t)
	ws := filepath.Join(dir, "workspace")
	os.WriteFile(filepath.Join(ws, "README.md"), []byte("# changed\n"), 0o600)
	os.WriteFile(filepath.Join(ws, "new.txt"), []byte("hello\n"), 0o600)
	rep, err := Extract(t.Context(), dir, defaultCaps(sha))
	if err != nil || !rep.Changed || rep.TreeHash == "" {
		t.Fatalf("rep=%+v err=%v", rep, err)
	}
	// The canonical patch applies cleanly to a pristine checkout at base.
	assertPatchApplies(t, dir, sha, rep.Diff) // helper: fresh clone of base.git, git apply --index, compare write-tree to rep.TreeHash
}

func TestExtract_PoisonedWorkspaceGitConfigIsInert(t *testing.T) {
	dir, sha := stagedDir(t)
	ws := filepath.Join(dir, "workspace")
	marker := filepath.Join(dir, "PWNED")
	poison := fmt.Sprintf("[diff]\n\texternal = /bin/sh -c 'touch %s'\n[core]\n\tfsmonitor = /bin/sh -c 'touch %s'\n", marker, marker)
	appendFile(t, filepath.Join(ws, ".git", "config"), poison)
	writeExecutable(t, filepath.Join(ws, ".git", "hooks", "post-checkout"), "#!/bin/sh\ntouch "+marker+"\n")
	os.WriteFile(filepath.Join(ws, "README.md"), []byte("# changed\n"), 0o600)
	rep, err := Extract(t.Context(), dir, defaultCaps(sha))
	if err != nil || !rep.Changed {
		t.Fatalf("rep=%+v err=%v", rep, err)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatal("hostile .git executed code in the trusted extraction")
	}
	// And the poisoned .git itself never enters the patch.
	if strings.Contains(rep.Diff, ".git/config") {
		t.Fatal("workspace .git leaked into the diff")
	}
}

func TestExtract_EmbeddedRepoRejectedTyped(t *testing.T) {
	dir, sha := stagedDir(t)
	inner := filepath.Join(dir, "workspace", "vendor", "evil")
	// A real nested repo: git init + one commit (runGit with baseEnv).
	mkNestedRepo(t, inner)
	_, err := Extract(t.Context(), dir, defaultCaps(sha))
	var te TypedError
	if !errors.As(err, &te) || te.Outcome.Code != runs.CodeEmbeddedRepo {
		t.Fatalf("err = %v", err)
	}
}

func TestExtract_SymlinkModeAndBinaryAndRenameSurvive(t *testing.T) {
	dir, sha := stagedDir(t)
	ws := filepath.Join(dir, "workspace")
	os.Symlink("README.md", filepath.Join(ws, "link"))
	os.Chmod(filepath.Join(ws, "README.md"), 0o755)
	os.WriteFile(filepath.Join(ws, "blob.bin"), []byte{0, 1, 2, 255}, 0o600)
	rep, err := Extract(t.Context(), dir, defaultCaps(sha))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"120000", "100755", "GIT binary patch"} {
		if !strings.Contains(rep.Diff, want) {
			t.Errorf("diff missing %q", want)
		}
	}
	assertPatchApplies(t, dir, sha, rep.Diff)
}

func TestExtract_CapsAreTyped(t *testing.T) {
	// files cap: MaxFiles=1 → patch_too_large; diff cap: MaxPatchBytes=10 → patch_too_large
}
```

Run: expected FAIL.

- [ ] **Step 2: Implement `extract.go`**

Follow the binding mechanics verbatim. Core:

```go
func Extract(ctx context.Context, dir string, in ExtractInput) (ExtractReport, error) {
	ws := filepath.Join(dir, "workspace")
	stats, err := BoundedWalk(ws, true, in.MaxFiles, in.MaxBytes)
	if err != nil {
		return ExtractReport{}, err
	}
	if stats.Breached {
		return ExtractReport{}, TypedError{Outcome: runs.Outcome{
			Class: runs.OutcomePermanent, Code: runs.CodePatchTooLarge,
			Message: fmt.Sprintf("workspace exceeds extraction caps (%d files / %d bytes seen)", stats.Files, stats.Bytes),
		}}
	}
	env := append(baseEnv(dir),
		"GIT_DIR="+filepath.Join(dir, "base.git"),
		"GIT_WORK_TREE="+ws,
		"GIT_INDEX_FILE="+filepath.Join(dir, "extract.index"),
	)
	// read-tree, add -A, ls-files gitlink scan, write-tree, tree compare,
	// diff-tree — each via runGit(ctx, dir, env, ...); mechanical failure
	// wraps as extraction_failed (permanent).
	...
}
```

- [ ] **Step 3: Run to pass**

Run: `cd backend && go test ./internal/runs/runner/gitops/ -v`
Expected: PASS

- [ ] **Step 4: The `extract` subcommand + commit**

`backend/internal/app/extract.go` mirrors `runStageCmd` exactly (input `/attempt/extract.json`, report `/attempt/extract.report.json`, unexpected errors classify `{permanent, extraction_failed}`); dispatch case + usage line; a `main_test.go` table entry (missing input → exit 2).

Run: `cd backend && task backend:test backend:lint backend:analyze`
Expected: green

```bash
git add backend/internal/runs/runner/gitops/ backend/internal/app/
git commit -m "feat(backend): hostile-git-safe patch extraction and the extract subcommand"
```

---
### Task 7: The egress gateway — broker, CONNECT proxy, `caesar egress-proxy`

The per-attempt dual-homed gateway (spec §3): credential broker (attempt-scoped token in, provider credential injected gateway-side, operations allowlisted, usage metered) + CONNECT proxy (exact-host allowlist, 443 only, private ranges refused, deny logged). One process, one listener, `CAESAR_EGRESS_CONFIG` env config.

**Files:**
- Create: `backend/internal/runs/egress/egress.go` (config, Op — extends Task 4's stub), `backend/internal/runs/egress/gateway.go`, `backend/internal/runs/egress/broker.go`, `backend/internal/runs/egress/connect.go`, `backend/internal/runs/egress/meter.go`, `backend/internal/app/egressproxy.go`
- Modify: `backend/internal/app/main.go` (dispatch `egress-proxy`), `backend/internal/platform/config/config.go` (`LoadEgressProxy`)
- Test: `backend/internal/runs/egress/broker_test.go`, `backend/internal/runs/egress/connect_test.go`, `backend/internal/runs/egress/meter_test.go`

**Interfaces:**
- Consumes: nothing from the runner (the runner consumes THIS: Task 8 serializes `egress.Config` into the gateway container's env).
- Produces:

```go
type Op struct {
	Method     string `json:"method"`
	PathPrefix string `json:"path_prefix"`
}
type Provider struct {
	Endpoint         string `json:"endpoint"`          // "" = CONNECT-only mode (validation gateways)
	Credential       string `json:"credential"`
	CredentialHeader string `json:"credential_header"` // "x-api-key" | "authorization"
	CredentialPrefix string `json:"credential_prefix"` // "" | "Bearer "
	Ops              []Op   `json:"ops"`
}
type Config struct {
	Attempt      string   `json:"attempt"`
	ListenAddr   string   `json:"listen_addr"` // default ":8080"
	TokenSHA256  string   `json:"token_sha256"`
	Provider     Provider `json:"provider"`
	ConnectAllow []string `json:"connect_allow"`
	MeterSeconds int      `json:"meter_seconds"` // default 30
}
func NewGateway(cfg Config, log *slog.Logger) (*Gateway, error)
func (g *Gateway) Run(ctx context.Context) error // ListenAndServe until ctx done; graceful close; final summary log
// test seams (fields on Gateway, defaulted in NewGateway):
//   resolver func(ctx, host string) ([]netip.Addr, error)
//   dial     func(ctx, addrPort string) (net.Conn, error)
//   upstream *http.Client (broker forwarding)

// config addition
func LoadEgressProxy() (egress.Config, error) // CAESAR_EGRESS_CONFIG env, strict decode, validate
```

**Behavior pins (each is a test):**
1. **Auth:** the token is read from `x-api-key` or `Authorization: Bearer …` (harness CLIs present their "API key" through their native header — the broker accepts both); compare `sha256(token)` to `TokenSHA256` with `crypto/subtle.ConstantTimeCompare`; mismatch/absence → 401 JSON `{"error":"invalid broker token"}`, metered as denied. Every broker/CONNECT request requires it — the CONNECT path reads `Proxy-Authorization: Bearer …`.
2. **Operations:** method + path must prefix-match an entry in `Provider.Ops`; miss → 403, `egress.denied` log `{kind:"op", method, path}`. `Provider.Endpoint == ""` → every broker path is 403 `{kind:"no_provider"}` (CONNECT-only mode).
3. **Forwarding:** upstream URL = `Endpoint` + original path + query; inbound auth headers stripped (`x-api-key`, `Authorization`, `Proxy-Authorization`); credential injected as `CredentialHeader: CredentialPrefix + Credential`; `Host` rewritten; hop-by-hop headers dropped; request and response bodies streamed with byte counting (SSE-safe: `http.Flusher` after each write, no buffering of the response). Response-header timeout 60s; no overall timeout.
4. **CONNECT vetting:** target port must be 443 → else 403 `{kind:"port"}`; hostname must exact-match (case-insensitive) `ConnectAllow` → else 403 `{kind:"host", host}`; resolve, and **every** address must be public unicast — reject loopback, private (RFC 1918), link-local v4/v6 (169.254/16 covers the metadata endpoint), CGNAT 100.64/10, ULA fc00::/7, unspecified, multicast → 403 `{kind:"private_ip"}`; dial the **vetted** address (never re-resolve), timeout 10s; success → `200 Connection Established`, hijack, bidirectional copy with counting.
5. **Metering:** counters `{requests, denied, request_bytes, response_bytes, connects, connect_bytes}`; a structured `egress.meter` log line every `MeterSeconds` and a final `egress.summary` line on shutdown (Task 8 harvests the last `egress.summary` from the gateway log). Every denial ALSO logs `egress.denied` with its `kind` — Task 8 tails these into `phase.egress_denied` run events.
6. **Open-CONNECT mode:** `TokenSHA256 == ""` means the CONNECT path requires no `Proxy-Authorization` — the posture validation gateways use (Task 10), where the toolchain is arbitrary package managers that can't be taught a proxy credential. It is safe precisely because such a gateway carries **no provider credential** (`Provider.Endpoint == ""` ⇒ every broker path 403s) and its allowlist is the only thing it can reach. `NewGateway` rejects the dangerous combination — a config with an empty `TokenSHA256` **and** a non-empty `Provider.Endpoint` is a configuration error, not a permitted mode.
7. **Config hygiene:** strict decode (unknown fields rejected), `TokenSHA256` either empty (pin 6) or 64 hex, `Attempt` required; the credential value never appears in any log line (the meter/denial loggers receive no credential — enforce by construction: `Config.Provider` is not passed to the logging helpers; add a test greping a captured log buffer for the credential).

- [ ] **Step 1: Failing broker tests**

```go
// broker_test.go
package egress

func testGateway(t *testing.T, cfg Config) (*Gateway, *bytes.Buffer) {
	t.Helper()
	var logBuf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logBuf, nil))
	g, err := NewGateway(cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	return g, &logBuf
}

const tok = "cbt1.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func shaOf(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func TestBroker_InjectsCredentialAndStripsToken(t *testing.T) {
	var gotAuth, gotAPIKey string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotAPIKey = r.Header.Get("Authorization"), r.Header.Get("x-api-key")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	g, _ := testGateway(t, Config{
		Attempt: "caesar-r-n-1", TokenSHA256: shaOf(tok),
		Provider: Provider{Endpoint: upstream.URL, Credential: "real-key", CredentialHeader: "x-api-key",
			Ops: []Op{{Method: "POST", PathPrefix: "/v1/messages"}}},
	})
	srv := httptest.NewServer(g.handler())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/messages?beta=1", strings.NewReader(`{}`))
	req.Header.Set("x-api-key", tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("status=%v err=%v", resp, err)
	}
	if gotAPIKey != "real-key" || gotAuth != "" {
		t.Fatalf("upstream saw api-key=%q auth=%q", gotAPIKey, gotAuth)
	}
}

func TestBroker_WrongTokenIs401(t *testing.T)           { /* x-api-key: "cbt1.wrong" → 401; meter.denied == 1 */ }
func TestBroker_MissingTokenIs401(t *testing.T)         { /* no header → 401 */ }
func TestBroker_DisallowedOperationIs403Logged(t *testing.T) {
	/* DELETE /v1/messages with valid token → 403; logBuf contains "egress.denied" with kind=op */
}
func TestBroker_BearerFormAccepted(t *testing.T)        { /* Authorization: Bearer <tok> → 200 */ }
func TestBroker_NoProviderModeRefusesAll(t *testing.T)  { /* Endpoint "" → 403 kind=no_provider */ }
func TestBroker_CredentialNeverLogged(t *testing.T) {
	/* run the happy + denied cases, then: if strings.Contains(logBuf.String(), "real-key") { t.Fatal } */
}
func TestBroker_StreamsResponseBody(t *testing.T) {
	/* upstream writes 3 chunks with flushes; client reads them incrementally (httptest + http.NoBody trick:
	   assert first chunk arrives before upstream finishes via channel handshake) */
}
```

- [ ] **Step 2: Failing CONNECT + vetting tests**

```go
// connect_test.go
func TestVetTarget_Table(t *testing.T) {
	g, _ := testGateway(t, Config{Attempt: "a", TokenSHA256: shaOf(tok), ConnectAllow: []string{"registry.npmjs.org"}})
	cases := []struct {
		host, port string
		ips        []string
		wantKind   string // "" = allowed
	}{
		{"registry.npmjs.org", "443", []string{"104.16.0.1"}, ""},
		{"REGISTRY.NPMJS.ORG", "443", []string{"104.16.0.1"}, ""},        // case-insensitive
		{"evil.example", "443", []string{"104.16.0.1"}, "host"},           // not allowlisted
		{"registry.npmjs.org", "80", []string{"104.16.0.1"}, "port"},      // non-TLS
		{"registry.npmjs.org", "443", []string{"10.0.0.5"}, "private_ip"}, // rfc1918
		{"registry.npmjs.org", "443", []string{"127.0.0.1"}, "private_ip"},
		{"registry.npmjs.org", "443", []string{"169.254.169.254"}, "private_ip"}, // metadata
		{"registry.npmjs.org", "443", []string{"100.64.0.9"}, "private_ip"},      // cgnat
		{"registry.npmjs.org", "443", []string{"fd00::1"}, "private_ip"},         // ula
		{"registry.npmjs.org", "443", []string{"104.16.0.1", "10.0.0.5"}, "private_ip"}, // ANY private addr taints
	}
	for _, c := range cases { /* g.resolver returns c.ips; call g.vetConnect(host,port); assert kind */ }
}

func TestConnect_TunnelsBytesToVettedAddrOnly(t *testing.T) {
	// resolver: allowed.test -> 93.184.216.34 (never dialed for real);
	// dial seam records the addr and returns net.Pipe() to an echo loop.
	// CONNECT allowed.test:443 with Proxy-Authorization → 200, write "ping", read "ping".
	// Assert dialed == "93.184.216.34:443" (the vetted IP, not the hostname).
}

func TestConnect_DeniedIsLoggedWithHost(t *testing.T) { /* CONNECT other.test:443 → 403; logBuf has egress.denied kind=host host=other.test */ }
func TestConnect_RequiresProxyAuth(t *testing.T)      { /* no Proxy-Authorization → 401, no dial */ }

func TestConnect_OpenModeSkipsAuthWhenNoProviderConfigured(t *testing.T) {
	// Config{TokenSHA256: "", Provider: Provider{}, ConnectAllow: [...]}:
	// CONNECT to an allowlisted host with NO Proxy-Authorization tunnels;
	// the broker path still 403s kind=no_provider.
}

func TestNewGateway_RejectsOpenModeWithAProvider(t *testing.T) {
	// TokenSHA256 "" + Provider.Endpoint set → NewGateway returns an error:
	// an unauthenticated broker holding a real credential must be
	// unconstructable, not merely undocumented.
}
```

Run: `cd backend && go test ./internal/runs/egress/ -v` — Expected: FAIL

- [ ] **Step 3: Implement the gateway**

`gateway.go`: `handler()` routes `r.Method == http.MethodConnect` → connect, else broker. `Run` builds `http.Server{Handler: g.handler()}` on `ListenAddr`, starts the meter ticker goroutine, shuts down on ctx cancel, logs `egress.summary` last. `broker.go` implements pins 1–3 (hand-rolled forward — build the upstream `*http.Request` yourself; `httputil.ReverseProxy` buffers/rewrites more than we want and hides byte counts; copy loop = 32 KiB buffer, count, flush). `connect.go` implements pin 4 with `netip.Addr` classification helpers:

```go
func isPublicUnicast(a netip.Addr) bool {
	if a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsMulticast() || a.IsUnspecified() || !a.IsValid() {
		return false
	}
	if cgnat := netip.MustParsePrefix("100.64.0.0/10"); a.Is4() && cgnat.Contains(a) {
		return false
	}
	if ula := netip.MustParsePrefix("fc00::/7"); a.Is6() && ula.Contains(a) {
		return false
	}
	return true
}
```

`meter.go`: an atomic-counter struct with `snapshot()` for the log lines. The final summary line shape (harvested by Task 8 — binding): `{"msg":"egress.summary","attempt":"...","requests":N,"denied":N,"request_bytes":N,"response_bytes":N,"connects":N,"connect_bytes":N}`.

Run: `cd backend && go test ./internal/runs/egress/ -v`
Expected: PASS

- [ ] **Step 4: The subcommand + config loader**

`config.LoadEgressProxy()`: read `CAESAR_EGRESS_CONFIG`, strict-decode into `egress.Config`, apply defaults (`ListenAddr: ":8080"`, `MeterSeconds: 30`), validate (attempt + token sha shape; endpoint http(s) URL when set). `backend/internal/app/egressproxy.go`:

```go
func runEgressProxyCmd(stdout, stderr io.Writer) int {
	cfg, err := config.LoadEgressProxy()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	log := slog.New(slog.NewJSONHandler(stdout, nil))
	g, err := egress.NewGateway(cfg, log)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := g.Run(ctx); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
```

Dispatch case `"egress-proxy"` + usage line + a `main_test.go` entry (missing env → exit 1). Note the import direction: `app → runs/egress` (composition root, legal); `runs/egress` imports platform only.

Run: `cd backend && task backend:test backend:lint backend:analyze`
Expected: green

- [ ] **Step 5: Commit**

```bash
git add backend/internal/runs/egress/ backend/internal/app/ backend/internal/platform/config/
git commit -m "feat(backend): egress gateway with credential broker, connect allowlist, and metering"
```

---
### Task 8: `Runner.Execute` — the four-container pipeline

Stage → gateway → agent → extract, with construction, log streaming, collection, and teardown on every path. Ends with the first real-Docker integration tests (build tag `runnerintegration`).

**Files:**
- Create: `backend/internal/runs/runner/execute.go`, `backend/internal/runs/runner/containers.go` (create/start/wait/remove helpers over `Engine`), `backend/internal/runs/runner/logs.go` (capped streamer), `backend/internal/runs/runner/collect.go`
- Test: `backend/internal/runs/runner/testutil_runner_test.go`, `backend/internal/runs/runner/execute_runner_test.go` (both `//go:build runnerintegration`)

**Interfaces:**
- Consumes: Tasks 3–7 as produced.
- Produces:
  - `func (r *Runner) Execute(ctx context.Context, req runs.ExecutionRequest) (runs.ExecutionResult, error)` — satisfies `runs.Executor` (Task 10 adds `Inspect`).
  - `collectResult(r *Runner, ctx, req, spec, exitCode, oom) (runs.ExecutionResult, error)` — shared with Task 10's adoption path.
  - The pipeline steps as named unexported methods, so Task 10's reconcile tests can reconstruct a half-finished attempt without any production test hook: `(*Runner).ensureNetworks(ctx, ref) error`, `(*Runner).stageAttempt(ctx, ref, spec, clone) error`, `(*Runner).runAgentToCompletion(ctx, ref, spec) (exitCode int64, oom bool, err error)`, `(*Runner).teardown(ctx, ref)`.
  - `type logStreamer struct{...}`; `func (r *Runner) streamLogs(ctx, containerID, path string, ref runs.AttemptRef, stream string, onDenied func(host, reason string)) *logStreamer`; `(*logStreamer).wait() (bytes int64, truncated bool, err error)`.
  - Failure artifacts on error paths are Task 9 — here every failure is a plain typed `OutcomeError`.

**Pipeline (binding order; each step's failure classification in parentheses):**
1. `os.MkdirAll` attempt dir + logs dir; **defer full teardown** — remove (force) extract/agent/gateway/stage containers by name, remove the attempt network, `os.RemoveAll(attemptDir)`; logs dir stays. Teardown tolerates not-found everywhere and logs anything else; it runs on **every** return path (`defer` with named cleanup func).
2. Resolve `HarnessSpec` (permanent `harness_error` if the resolver fails — the orchestrator already re-resolved at request build; a vanished harness here is a registry mutation mid-flight), `buildSpec` (its own classifications), `CloneSpec` (transient `staging_failed`), provider credential via `OpenCredential(ctx, hs.CredentialID, "attempt:"+ref.String())` (transient `staging_failed` — brokered creds are infrastructure here).
3. Ensure the shared egress network exists (`NetworkInspect` → 404 → `NetworkCreate(name, internal=false, labels)`; tolerate the create race via `IsConflict`).
4. Create the attempt network (`internal=true`).
5. Write `stage.json` (`StageInput{CloneURL, BaseSHA: req.BaseSHA, QuotaBytes, QuotaFiles}` — from `CloneSpec` + options); run the **stage container**: image `GitImage`, `Cmd: ["stage"]`, binds `<attempt>:/attempt` (+ `<BindDir>:/remote:ro` when set), network `caesar-egress` (or `"none"` when `BindDir != ""`), env = `GIT_CONFIG_COUNT/KEY_0/VALUE_0` iff `AuthHeader != ""` (`GIT_CONFIG_KEY_0=http.extraheader`, value = the header), hardening profile, 10m timeout (`context.WithTimeout`). Wait; read `stage.report.json`; report outcome → that typed outcome; unreadable report/timeout → transient `staging_failed`.
6. Start the **gateway container**: image `CaesarImage`, `Cmd: ["egress-proxy"]`, env `CAESAR_EGRESS_CONFIG=<json>` built from the spec (`egress.Config{Attempt: ref.String(), TokenSHA256: spec.BrokerTokenSHA, Provider: {Endpoint: hs.ProviderEndpoint, Credential: <opened>, CredentialHeader/Prefix, Ops: spec.ProviderOps}, ConnectAllow: opts.ConnectAllow}`), networks: attempt net (alias `egress`) + egress net, gateway limits. Start its log streamer (with `onDenied` → `EventSink` `phase.egress_denied`, capped 20 — the cap lives in `Execute`'s closure counter).
7. Create + start the **agent container**: `spec.ImageDigest`, `Entrypoint/Cmd` from `HarnessSpec`, `spec.Env`, `User: spec.User`, bind `<attempt>/workspace:/workspace`, attempt network only, full hardening + agent limits. Start its log streamer. Start the **quota watchdog** (Task 9 — here a stub goroutine that only samples; the kill lands there. Keep the seam: `watchWorkspace(ctx, ref, cancelCause)`).
8. `ContainerWait(ctx, agentID)` — ctx here is the orchestrator's `execCtx` (deadline = two clocks): ctx expiry → `ContainerKill(agent, "KILL")` → return `ctx.Err()` (classifies transient `timeout` upstream); a watchdog/flood kill (Task 9) surfaces its own typed outcome via `context.WithCancelCause`.
9. Stop the gateway (`ContainerKill(gateway, "TERM")`, bounded wait 5s, then KILL) so its final `egress.summary` flushes; `wait()` both streamers.
10. **Collect** (`collect.go`): exit != 0 → transient `harness_error` (message: exit code + OOMKilled flag; per-harness mapping is Plan 07). Exit 0 → write `extract.json` (caps from options: `MaxFiles/MaxBytes` = quota, `MaxPatchBytes` = 4 MiB), run the **extract container** (image `GitImage`, `Cmd: ["extract"]`, bind `<attempt>:/attempt`, network `"none"`, 10m timeout), read report → typed outcome or `{Diff, TreeHash, Changed}`. Then read `workspace/.caesar/out/artifacts.json` when present (≤ 1 MiB; strict decode `[]struct{Type string; Payload map[string]any}`; unknown/patch types → permanent `invalid_output`); assemble:
    - patch artifact iff `Changed`: `{Type: runs.ArtifactKindPatch, Payload: {"diff", "base_sha", "tree_hash"}}`
    - declared artifacts from `artifacts.json`
    - `log_ref` always (Task 9 fills the egress summary; here streams only)
11. Return `runs.ExecutionResult{Artifacts: ...}`.

- [ ] **Step 1: `containers.go` + `logs.go` unit-shaped pieces first**

The capped streamer is unit-testable without Docker (it consumes any `io.ReadCloser` via `Engine.ContainerLogs` — fake it):

```go
// logs.go — write up to capBytes to path; past the cap, count only and
// fire onTruncated once; DemuxLogs feeds both streams into one file.
// Denial tailing: each written line is scanned for `"msg":"egress.denied"`
// and surfaced through onDenied (gateway streamer only).
```

Write `logs_test.go` (no build tag): feed a synthetic multiplexed stream (reuse Task 3's `frame` helper) exceeding the cap; assert file size == cap, `truncated == true`, `onTruncated` fired exactly once, byte count == total produced, and a fabricated `egress.denied` JSON line reaches `onDenied` with its host/kind fields.

Run: `cd backend && go test ./internal/runs/runner/ -run TestLogStreamer -v` — red → implement → green.

- [ ] **Step 2: Implement `execute.go`, `containers.go`, `collect.go`**

Mechanical assembly of the pipeline above. `containers.go` helpers keep `execute.go` readable:

```go
func (r *Runner) runToCompletion(ctx context.Context, name string, cfg dockerapi.ContainerConfig,
	host dockerapi.HostConfig, networks map[string]dockerapi.EndpointSettings, timeout time.Duration) (int64, error)
// create+start+wait+remove(force) with the hardening profile merged in;
// used by stage/extract/validation. The agent/gateway lifecycles stay
// explicit in execute.go (streaming + kill windows differ).

func (r *Runner) hardenedHost(binds []string, network string, mem, cpus, pids int64) dockerapi.HostConfig
// CapDrop ALL, no-new-privileges, ReadonlyRootfs, tmpfs /tmp, limits.
```

- [ ] **Step 3: The runner integration harness**

`testutil_runner_test.go` (build tag `runnerintegration`):

```go
//go:build runnerintegration

package runner

// Suite prerequisites (CI job builds these; locally: `task image:build`):
//   caesar:dev, caesar-git:dev images; a reachable Docker daemon at the
//   default socket. The suite SKIPS (t.Skip) with a clear message when
//   the images are missing — absence of Docker itself fails loudly.

type sinkEvent struct {
	Ref     runs.AttemptRef
	Kind    string
	Payload map[string]any
}
type recordingSink struct {
	mu  sync.Mutex
	evs []sinkEvent
}
func (s *recordingSink) AppendRuntimeEvent(_ context.Context, ref runs.AttemptRef, kind string, payload map[string]any) error

type staticResolver struct{ spec HarnessSpec }
func (r staticResolver) HarnessSpec(context.Context, uuid.UUID) (HarnessSpec, error) { return r.spec, nil }

type staticCreds struct{ value []byte }
func (c staticCreds) OpenCredential(context.Context, uuid.UUID, string) ([]byte, error) { return c.value, nil }

type fixtureGit struct{ dir string } // fake.InitBareRepo output dir
func (g fixtureGit) CloneSpec(context.Context, runs.RepoRef) (CloneSpec, error) {
	return CloneSpec{URL: "file:///remote/bare.git", BindDir: g.dir}, nil
}

// scriptedHarness returns a HarnessSpec running `script` via sh inside
// caesar-git:dev (it has busybox sh, git, wget — everything the probes
// need). endpoint is the fake provider's URL, or "" when the test makes
// no provider calls.
func scriptedHarness(endpoint, script string) HarnessSpec {
	return HarnessSpec{
		Kind: "claude-code", ImageRef: "caesar-git:dev",
		ProviderEndpoint: endpoint,
		CredentialID:     uuid.New(),
		Entrypoint:       []string{"/bin/sh", "-c"},
		Cmd:              []string{script},
	}
}

// startFakeProvider runs an httptest.Server on the HOST that records the
// credential header it was called with, and returns
// "http://host.docker.internal:<port>". Reaching it needs the ONE
// test-only ExtraHosts exception (host-gateway mapping), applied to the
// GATEWAY container alone when the unexported Options.testHostAccess is
// set — unexported, unset in production wiring, documented at its
// declaration.
func startFakeProvider(t *testing.T) (endpoint string, seen *credentialLog)

// newDockerRunner is the runner-suite constructor. (Named distinctly from
// Task 4's newSpecRunner: both files are package runner, and with the
// runnerintegration tag on they compile together.)
func newDockerRunner(t *testing.T, opts Options, hs HarnessSpec, sink EventSink) *Runner
// wires dockerapi.New("/var/run/docker.sock"), fixture git source, static
// creds "real-provider-key", tight defaults (quota 64MiB/2000 files,
// log cap 256KiB, flood 1MiB, poll 200ms), t.TempDir() DataRoot,
// unique EgressNetwork per test run (parallel safety), Cleanup sweeping
// leftover caesar.managed resources labeled with this test's ref.
```

- [ ] **Step 4: Failing happy-path integration test**

```go
//go:build runnerintegration

func TestExecute_HappyPath_PatchDeclaredArtifactsAndLifecycle(t *testing.T) {
	script := `
set -e
echo hello-from-agent
echo "artifact content" > /workspace/NEW.md
mkdir -p /workspace/.caesar/out
cat > /workspace/.caesar/out/artifacts.json <<'EOF'
[{"type":"plan","payload":{"content":"scripted plan"}}]
EOF
`
	sink := &recordingSink{}
	r := newDockerRunner(t, Options{}, scriptedHarness("", script), sink)
	ref := testRef()
	res, err := r.Execute(t.Context(), runs.ExecutionRequest{
		Ref: ref, Kind: runs.KindImplement, HarnessID: uuid.New(),
		Repo: runs.RepoRef{Forge: "github", Slug: "giiorg/demo"}, BaseSHA: fixtureSHA(t),
		Deadline: time.Now().Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}

	byType := artifactsByType(res.Artifacts)
	patch := byType[runs.ArtifactKindPatch].Payload
	if !strings.Contains(patch["diff"].(string), "NEW.md") || patch["base_sha"] != fixtureSHA(t) || patch["tree_hash"] == "" {
		t.Fatalf("patch = %v", patch)
	}
	if byType[runs.ArtifactKindPlan].Payload["content"] != "scripted plan" {
		t.Fatal("declared artifact lost")
	}
	logRef := byType[runs.ArtifactKindLogRef].Payload
	agentLog := readLogFile(t, r, ref, "agent.log")
	if !strings.Contains(agentLog, "hello-from-agent") {
		t.Fatal("agent stdout not captured")
	}
	if logRef["egress"] == nil {
		t.Fatal("log_ref missing egress summary") // Task 9 fills counters; key must exist
	}

	// Lifecycle: containers + attempt network + attempt dir gone; logs remain.
	assertNoContainer(t, agentName(ref))
	assertNoContainer(t, gatewayName(ref))
	assertNoContainer(t, stageName(ref))
	assertNoNetwork(t, networkName(ref))
	if _, err := os.Stat(r.attemptDir(ref)); !os.IsNotExist(err) {
		t.Fatal("attempt dir must be removed")
	}
}

func TestExecute_BrokeredProviderCall(t *testing.T) {
	// fakeProvider records the x-api-key header. Script:
	//   wget -qO- --header "x-api-key: $CAESAR_BROKER_TOKEN" "$CAESAR_BROKER_URL/v1/messages" ... hmm wget cannot POST bodies well; use GET /v1/models (allowlisted).
	script := `set -e
wget -qO /workspace/models.json --header "x-api-key: $CAESAR_BROKER_TOKEN" "$CAESAR_BROKER_URL/v1/models"
`
	// Assert: Execute succeeds; fakeProvider saw EXACTLY "real-provider-key"
	// and NEVER the broker token; the workspace patch contains models.json.
}

func TestExecute_AgentNonzeroExitClassifiesHarnessError(t *testing.T) {
	// script: `exit 3` → OutcomeError{transient, harness_error}, message contains "exit 3".
}

func TestExecute_StageReportOutcomePropagates(t *testing.T) {
	// fixtureGit with a bogus BaseSHA in the request → base_sha_unavailable (permanent),
	// and no agent container was ever created (assertNoContainer during... assert via
	// docker events? simpler: assert the typed code and that workspace/ was never handed
	// out — attempt dir removed, and the sink saw no events).
}
```

Run: `cd backend && go test -race -tags runnerintegration -count=1 -timeout 15m ./internal/runs/runner/ -run TestExecute -v`
Expected: FAIL → implement remaining glue → PASS

- [ ] **Step 5: Taskfile lane + commit**

Add to `Taskfile.yml`:

```yaml
  backend:test:runner:
    desc: "Runner integration suite (needs Docker + task image:build)"
    dir: backend
    cmds:
      - go test -race -tags runnerintegration -count=1 -timeout 20m ./internal/runs/runner/... ./internal/runs/egress/...
```

Update `backend:vet` and `backend:vuln` tag lists to `integration,property,runnerintegration`, and add `- runnerintegration` to `run.build-tags` in `backend/.golangci.yml` (it currently lists `integration` and `property`) — golangci must lint the new suite.

Run: `task image:build && task backend:test:runner`
Expected: green

```bash
git add backend/internal/runs/runner/ Taskfile.yml backend/.golangci.yml
git commit -m "feat(backend): docker executor pipeline with staged workspaces and brokered egress"
```

---
### Task 9: Enforcement — quota watchdog, log flood, failure snapshots

The runner's resource police: workspace quota kill, log truncation + flood kill, and the redacted `failure_snapshot` + `log_ref` artifacts riding failure returns (Task 1's contract).

**Files:**
- Create: `backend/internal/runs/runner/watchdog.go`, `backend/internal/runs/runner/snapshot.go`
- Modify: `backend/internal/runs/runner/execute.go` (wire watchdog + flood + failure artifacts), `backend/internal/runs/runner/logs.go` (flood threshold)
- Test: `backend/internal/runs/runner/snapshot_test.go` (unit), `backend/internal/runs/runner/enforce_runner_test.go` (`runnerintegration`)

**Interfaces:**
- Consumes: Tasks 5 (`gitops.BoundedWalk`), 8.
- Produces:
  - `watchWorkspace(ctx, dir string, quotaBytes, quotaFiles int64, poll time.Duration, breach func(gitops.WalkStats))` — goroutine body; `execute.go` runs it with `breach` = kill-agent + `context.WithCancelCause(OutcomeError{workspace_quota_exceeded})`.
  - `logStreamer` gains `floodBytes int64` + `onFlood func()`: total produced (written + counted-past-cap) crossing it fires once; `execute.go`'s `onFlood` kills the agent with cause `OutcomeError{log_flood}`.
  - `buildFailureSnapshot(inspect dockerapi.ContainerInspect, logPath string, tailBytes int64, wsDir string, redact []string) map[string]any` — the pinned payload, pure and unit-tested.
  - `Execute`'s error returns now carry `runs.ExecutionResult{Artifacts: [log_ref, failure_snapshot]}` for every post-agent-start failure (pre-agent failures — staging, image — return artifact-free errors: nothing ran, nothing to snapshot).
  - `log_ref.egress` populated: parse the LAST `egress.summary` line from `gateway.log` into the pinned counter map (missing/torn line → zeroes plus `"parse_error": true`).

- [ ] **Step 1: Unit tests for the snapshot builder — red, then green**

```go
// snapshot_test.go
func TestFailureSnapshot_ShapeTailAndRedaction(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "agent.log")
	big := strings.Repeat("x", 100<<10) + "SECRET-cbt1.abc token line\nlast line\n"
	os.WriteFile(logPath, []byte(big), 0o600)
	ws := filepath.Join(dir, "workspace")
	os.MkdirAll(ws, 0o755)
	os.WriteFile(filepath.Join(ws, "a.txt"), []byte("aaa"), 0o600)

	var ins dockerapi.ContainerInspect
	ins.State.ExitCode, ins.State.OOMKilled = 137, true
	snap := buildFailureSnapshot(ins, logPath, 64<<10, ws, []string{"SECRET-cbt1.abc"})

	if snap["exit_code"] != int64(137) || snap["oom_killed"] != true {
		t.Fatalf("state: %v", snap)
	}
	tail := snap["log_tail"].(string)
	if len(tail) > 64<<10 || !strings.HasSuffix(tail, "last line\n") {
		t.Fatal("tail must be the LAST 64KiB")
	}
	if strings.Contains(tail, "SECRET-cbt1.abc") || !strings.Contains(tail, "[redacted]") {
		t.Fatal("secret not redacted")
	}
	wsInfo := snap["workspace"].(map[string]any)
	if wsInfo["files"] != int64(1) || len(wsInfo["entries"].([]map[string]any)) != 1 {
		t.Fatalf("workspace listing: %v", wsInfo)
	}
}

func TestFailureSnapshot_ListingCapsAt200Entries(t *testing.T) { /* 250 files → 200 entries, files == 250 */ }
```

Implement `snapshot.go` (redaction = `strings.ReplaceAll(tail, secret, "[redacted]")` over every redact value; listing via `BoundedWalk`-style traversal collecting the first 200 relative paths).

Run: `cd backend && go test ./internal/runs/runner/ -run TestFailureSnapshot -v` → PASS

- [ ] **Step 2: Failing integration tests — the enforcement trio**

```go
//go:build runnerintegration

func TestExecute_WorkspaceFillIsQuotaKilled(t *testing.T) {
	// Options quota: 8MiB / 2000 files; script writes forever:
	script := `i=0; while true; do dd if=/dev/zero of=/workspace/f$i bs=1M count=1 2>/dev/null; i=$((i+1)); done`
	res, err := r.Execute(...)
	var oe runs.OutcomeError
	if !errors.As(err, &oe) || oe.Outcome.Code != runs.CodeWorkspaceQuotaExceeded || oe.Outcome.Class != runs.OutcomePermanent {
		t.Fatalf("err = %v", err)
	}
	// Failure artifacts ride the error return (Task 1 contract):
	byType := artifactsByType(res.Artifacts)
	if byType[runs.ArtifactKindFailureSnapshot] == nil || byType[runs.ArtifactKindLogRef] == nil {
		t.Fatal("failure artifacts missing")
	}
	assertNoContainer(t, agentName(ref)) // teardown ran
}

func TestExecute_LogFloodTruncatesThenKills(t *testing.T) {
	// Options: cap 256KiB, flood 1MiB; script: `while true; do echo AAAAAAAA...; done`
	// Assert: typed log_flood (permanent); agent.log size == cap;
	// sink recorded exactly one phase.log_truncated for stream=agent;
	// snapshot log_tail ends with captured (pre-cap) content.
}

func TestExecute_DeadlineKillClassifiesTimeout(t *testing.T) {
	// ctx with 5s deadline; script `sleep 600`. Expect ctx.Err() surfaced so
	// runs.ClassifyOutcome maps transient/timeout: assert errors.Is(err, context.DeadlineExceeded).
	// Assert wall time < 60s (the kill actually happened) and teardown ran.
}

func TestExecute_EgressDenialsBecomeCappedEvents(t *testing.T) {
	// ConnectAllow empty; script loops 30 CONNECT attempts:
	//   wget -q -O- https://denied.example/ (through HTTPS_PROXY) — each denied.
	// Assert: sink has phase.egress_denied events, COUNT <= 20, payload has host;
	// log_ref.egress.denied >= 30 (the meter counted them all).
}
```

- [ ] **Step 3: Implement watchdog, flood, failure-artifact assembly**

`watchdog.go`:

```go
// watchWorkspace samples a bounded walk; the walk itself is capped at
// the quota (a breach returns early — never an unbounded traversal of
// an adversarial tree). Fires breach at most once, then returns.
func watchWorkspace(ctx context.Context, dir string, quotaBytes, quotaFiles int64,
	poll time.Duration, breach func(gitops.WalkStats)) {
	tick := time.NewTicker(poll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			stats, err := gitops.BoundedWalk(dir, true, quotaFiles, quotaBytes)
			if err != nil {
				continue // transient FS races during agent writes are expected
			}
			if stats.Breached {
				breach(stats)
				return
			}
		}
	}
}
```

`execute.go` wiring: `execCtx, cancelCause := context.WithCancelCause(ctx)` around the agent wait; watchdog breach → `r.engine.ContainerKill(agent, "KILL")` + `cancelCause(OutcomeError{...quota...})`; flood → same with `log_flood`; after `ContainerWait` returns, `context.Cause(execCtx)` (when not `ctx.Err()`) takes precedence over exit-code classification. On any post-agent-start error path: inspect the agent corpse (`ContainerInspect` before teardown), build `log_ref` (streams + egress summary parse) + `failure_snapshot` (redact = broker token + provider credential), return them in the result alongside the error.

Run: `task backend:test:runner`
Expected: PASS (all Task 8 + 9 tests)

- [ ] **Step 4: Commit**

```bash
git add backend/internal/runs/runner/
git commit -m "feat(backend): workspace quota watchdog, log flood kill, redacted failure snapshots"
```

---

### Task 10: Reconciliation against real Docker, orphan sweep, validation containers

`Inspect` (adopt-or-report over the attempt-named corpse), the label-scoped orphan sweep with its maintenance loop, and `RunValidation` for Plan 08.

**Files:**
- Create: `backend/internal/runs/runner/inspect.go`, `backend/internal/runs/runner/sweep.go`, `backend/internal/runs/runner/validate.go`
- Test: `backend/internal/runs/runner/reconcile_runner_test.go`, `backend/internal/runs/runner/validate_runner_test.go` (both `runnerintegration`)

**Interfaces:**
- Consumes: Tasks 8–9 (`collectResult`, teardown, hardening helpers).
- Produces:
  - `func (r *Runner) Inspect(ctx context.Context, ref runs.AttemptRef) (runs.InspectResult, error)` — completes `runs.Executor`.
  - `func (r *Runner) SweepOrphans(ctx context.Context, olderThan time.Duration) error`
  - `func (r *Runner) Run(ctx context.Context) error` — maintenance loop: `SweepOrphans(ctx, 25h)` every 10m until ctx done (serve starts it when enabled; named `Run` to match the orchestrator's lifecycle idiom).
  - `type ValidationSpec struct { Ref runs.AttemptRef; Dir string; Image string; Cmd []string; ConnectAllow []string; Timeout time.Duration }`
  - `type ValidationResult struct { ExitCode int64; LogPath string; Truncated bool }`
  - `func (r *Runner) RunValidation(ctx context.Context, v ValidationSpec) (ValidationResult, error)` — infrastructure errors only; a failing build is `ExitCode != 0`, not an error (Plan 08 decides what that means).

**`Inspect` semantics (binding, spec §3 reconciling):** look up `agentName(ref)`:
- not found → `{Completed: false}` (nothing adoptable; caller concludes `reconcile_lost`).
- `State.Status == "running"` → **kill-then-inspect**: `ContainerKill(KILL)`, `ContainerWait`, re-inspect, fall through.
- exited `0` **and** the attempt dir still exists → adopt: run `collectResult` (identical collection path — extract container et al.), then teardown; `{Completed: true, Result: …}`. Collection failure → teardown → `{Completed: false}` (adoption must be idempotent-or-absent, never half).
- exited non-zero, or exited 0 with the attempt dir gone (Execute finished its teardown; the worker died between return and evaluation) → teardown remnants → `{Completed: false}`.

**Orphan sweep semantics:** list containers and networks labeled `caesar.managed=true` older than `olderThan` (containers: `Created` unix seconds; networks: `Created` RFC3339) → force-remove containers first, then networks; then remove `data_root/attempts/*` dirs with mtime older than the cutoff. 25h default = wall-clock ceiling (24h) + slack: anything older is unreachable by any live run. The sweep never touches younger resources — reconciliation owns those.

- [ ] **Step 1: Failing reconcile tests**

```go
//go:build runnerintegration

func TestInspect_NoContainerReportsLost(t *testing.T) {
	// Fresh ref, nothing created → {Completed:false}, no error.
}

func TestInspect_CompletedContainerIsAdopted(t *testing.T) {
	// Reproduce the crash window — an attempt whose container finished but
	// whose worker died before collecting — by calling Task 8's unexported
	// pipeline steps directly (same-package test, so no production test
	// hook is needed and none may be added):
	//   r.ensureNetworks(ctx, ref); r.stageAttempt(ctx, ref, spec, clone)
	//   r.runAgentToCompletion(ctx, ref, spec)   // script writes NEW.md + artifacts.json
	// then, with no collection and no teardown having run:
	//   res, err := r.Inspect(ctx, ref)
	// Assert: Completed true, the patch artifact contains NEW.md, and
	// teardown completed (no containers, no attempt dir, logs remain).
}

func TestInspect_RunningContainerIsKilledThenInspected(t *testing.T) {
	// stage + start agent with `sleep 600`. Inspect → kill-then-inspect →
	// exited nonzero → {Completed:false}; corpse and network removed.
}

func TestSweepOrphans_RemovesOnlyOldLabeledResources(t *testing.T) {
	// Create two labeled containers (exited) + networks: one with
	// olderThan=0 sweep semantics... Created timestamps can't be faked
	// against a real daemon — so invert: sweep with olderThan=0 removes
	// BOTH; then recreate one and sweep with olderThan=24h — removes
	// NEITHER. Both assertions together prove the age predicate is
	// honored in each direction. Unlabeled bystander container (plain
	// busybox-style caesar-git:dev sleeper WITHOUT labels) survives both sweeps.
}
```

- [ ] **Step 2: Implement `inspect.go` + `sweep.go`**

Straight transcription of the binding semantics. Factor `execute.go`'s deferred cleanup into `func (r *Runner) teardown(ctx context.Context, ref runs.AttemptRef)` (both paths + sweep use it; sweep passes names it discovered instead of a ref — give it `removeByName(ctx, containerNames []string, networkNames []string)`).

Run: `task backend:test:runner` → the reconcile tests PASS.

- [ ] **Step 3: Failing validation tests, then implement**

```go
//go:build runnerintegration

func TestRunValidation_HardenedExitCodeAndLog(t *testing.T) {
	// Dir: a temp dir with a file. Spec: image caesar-git:dev,
	// Cmd ["/bin/sh","-c","ls /workspace && echo validated && exit 7"].
	// Assert ExitCode == 7, log contains "validated", container + its
	// network removed, dir itself UNTOUCHED (validation never deletes
	// the caller's dir).
}

func TestRunValidation_NetworkIsGatewayOnly(t *testing.T) {
	// Cmd: wget with 3s timeout against a public IP → must fail (internal
	// net); CONNECT through the validation gateway to a non-allowlisted
	// host → 403 in the validation gateway log.
}
```

`validate.go`: a fresh internal network `validateName(ref)+"-net"`, plus a CONNECT-only gateway in Task 7's open-CONNECT mode (`Provider{}` zero — no credential to protect — `TokenSHA256: ""`, `ConnectAllow` from the spec), so arbitrary package managers reach allowlisted registries through `HTTPS_PROXY=http://egress:8080` without needing to send `Proxy-Authorization`. The validation container gets the same hardening profile, its `Dir` bind-mounted at `/workspace`, and runs via `runToCompletion`; its logs stream to `validate.log` under the caller ref's logs dir.

Run: `task backend:test:runner` → PASS

- [ ] **Step 4: Commit**

```bash
git add backend/internal/runs/runner/ backend/internal/runs/egress/
git commit -m "feat(backend): docker reconciliation, orphan sweep, validation containers"
```

---
### Task 11: The isolation suite — escape probes, hardening assertions, credential-staging probes

The tests the spec names explicitly (§7 "Runner integration against real Docker"). No new production code is expected; anything a probe catches gets fixed here.

**Files:**
- Create: `backend/internal/runs/runner/isolation_runner_test.go` (`runnerintegration`)
- Modify: whatever a failing probe proves wrong

**Interfaces:** consumes Tasks 8–10 only.

**Every probe below is binding** — a reviewer rejects this task if one is missing. The agent script writes its findings to `/workspace/probe.out`; the test reads the file from the extracted patch (or directly from the attempt dir before teardown — prefer the patch, it proves collection too). Probe scripts use `wget` (present in `caesar-git:dev` via busybox) with `-T 3` timeouts so a blackholed route fails fast rather than hanging to the deadline.

- [ ] **Step 1: Write the hardening-assertion probe**

```go
func TestIsolation_ContainerHardeningApplied(t *testing.T) {
	// Script probes its own container from inside:
	//   id -u                      → non-zero (never root)
	//   touch /root-probe          → must FAIL (read-only rootfs)
	//   touch /tmp/ok              → must SUCCEED (tmpfs)
	//   touch /workspace/ok        → must SUCCEED (rw workspace)
	//   cat /proc/self/status | grep CapEff → 0000000000000000
	//   grep NoNewPrivs /proc/self/status  → 1
	// Plus a host-side assertion via ContainerInspect BEFORE teardown is
	// impossible (Execute tears down), so assert on the probe output only —
	// the in-container view IS the property that matters.
}
```

- [ ] **Step 2: Write the escape probes**

```go
func TestIsolation_EscapeProbesAllDenied(t *testing.T) {
	// One script, one line of probe.out per target, each expected to FAIL:
	//   1. host gateway         : wget -T 3 -qO- http://$(ip route | awk '/default/{print $3}'):8080/
	//   2. Postgres on the host : wget -T 3 -qO- http://<host-gw>:5432/
	//   3. metadata service     : wget -T 3 -qO- http://169.254.169.254/latest/meta-data/
	//   4. public internet direct (proxy bypassed): env -u HTTPS_PROXY -u HTTP_PROXY wget -T 3 -qO- https://example.com/
	//   5. the docker socket    : test -S /var/run/docker.sock  → absent
	//   6. the caesar API       : wget -T 3 -qO- http://host.docker.internal:8080/healthz  (no ExtraHosts on the agent → DNS failure)
	//   7. the provider endpoint directly, no gateway: wget -T 3 -qO- https://api.anthropic.com/v1/models
	// Assert: probe.out has exactly 7 "DENIED" lines and zero "REACHED".
	// NOTE probe 1 is the documented Docker caveat (spec §3): an internal
	// network still exposes the host gateway. Docker Desktop and CI hosts
	// generally have no listener there, so the probe asserts connection
	// failure; the VM firewall baseline (Plan 13 runbook) is what makes it
	// structural in prod. If this probe ever REACHES something, that is a
	// real finding — fail loudly rather than relax it.
}

func TestIsolation_BrokerIsTheOnlyEgressPath(t *testing.T) {
	// Script: GET $CAESAR_BROKER_URL/v1/models with the token → SUCCEEDS
	//         GET the same path WITHOUT the token             → 401
	//         GET $CAESAR_BROKER_URL/v1/admin (not allowlisted) → 403
	// Assert from probe.out, and cross-check log_ref.egress counters:
	//   requests >= 1 and denied >= 2.
}
```

- [ ] **Step 3: Write the credential-staging probes**

```go
func TestIsolation_AgentSeesNoForgeCredential(t *testing.T) {
	// Stage with an auth header (fixtureGit variant returning
	// CloneSpec{URL: file:///remote/bare.git, AuthHeader: "Authorization: Basic " + b64("x:FORGE-SEKRET"), BindDir: fixture}).
	// Script writes to probe.out:
	//   env                                  → must not contain FORGE-SEKRET
	//   cat /proc/self/cmdline               → must not contain it
	//   grep -r FORGE-SEKRET /workspace      → no hits (incl. /workspace/.git)
	//   git -C /workspace remote -v          → credential-free URL
	//   git -C /workspace config --list --local → no extraheader/askpass/credential keys
	//   ls /workspace/.git-credentials       → absent
	//   test -d /workspace/../base.git       → absent (base mirror never mounted)
	// Assert: zero hits, and the extracted patch does not contain the secret.
}
```

- [ ] **Step 4: Run the suite, fix what it finds**

Run: `task image:build && task backend:test:runner`
Expected: every probe passes. If a probe fails, fix the runner/gateway — never the probe. Record in the commit message anything the probes changed.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/runs/runner/
git commit -m "test(backend): runner isolation suite with escape and credential-staging probes"
```

---

### Task 12: Config, wiring, and the runner-backed e2e

The `runner` config block, `serve` wiring behind `runner.enabled`, the app-level port adapters (harness resolver over `assets`, credential opener over `assets.UseCredential`, git source over the repo's forge URL + credential), and an app e2e proving the orchestrator drives a real container.

**Files:**
- Modify: `backend/internal/platform/config/config.go`, `backend/internal/platform/config/config_test.go`, `backend/internal/app/serve.go`
- Leave alone: `backend/caesar.dev.json` — it carries no `orchestrator` block either, relying on defaults; the runner ships disabled, so adding a block would be dead config.
- Create: `backend/internal/app/runnerports.go`, `backend/internal/app/runner_e2e_runnerintegration_test.go`
- Test: as above

**Interfaces:**
- Consumes: everything.
- Produces:
  - `config.Runner` struct + validation (Global Constraints block verbatim; `Enabled bool`, `Cpus float64`, `Bytes` fields, `PidsLimit int64`, `WorkspaceQuotaFiles int64`).
  - `appHarnessResolver{svc *assets.Service}` → `HarnessSpec` from `assets.Harness` (`Kind`, `ImageRef`, `ProviderEndpoint`, `CredentialID`; `Entrypoint`/`Cmd` nil — the image's own default, per Plan 07's contract).
  - `appCredentialOpener{svc *assets.Service}` → `OpenCredential` = `svc.UseCredential(ctx, id, purpose)` (the audited path — spec §6 "every use audited").
  - `appGitSource{svc *assets.Service}` → `CloneSpec`: HTTPS URL built from `RepoRef.Forge` + `Slug` (`github` → `https://github.com/<slug>.git`, `gitlab` → `https://gitlab.com/<slug>.git`), `AuthHeader = "Authorization: Basic " + base64("x-access-token:" + token)` where token = `UseCredential(ctx, repo.CredentialID, "clone:"+repo.Slug)`. Self-hosted forge base URLs are Plan 08 (the forge port owns URL construction then) — document the interim.
  - `buildRunner(cfg config.Runner, assetsSvc, runsSvc, log) (*runner.Runner, error)` in `runnerports.go`, called from `runServe` only when `cfg.Runner.Enabled`.

- [ ] **Step 1: Failing config tests**

Append to `config_test.go`:

```go
func TestConfig_RunnerDefaultsAndValidation(t *testing.T) {
	// 1. Minimal config file (no runner block) → Runner.Enabled false,
	//    DataRoot "/var/lib/caesar", WorkspaceQuotaBytes 2GiB, LogCapBytes 16MiB,
	//    FreeBytesWatermark 10GiB, Cpus 2.0, PidsLimit 512, EgressNetwork "caesar-egress".
	// 2. enabled true without caesar_image → error mentioning runner.caesar_image.
	// 3. log_flood_bytes below log_cap_bytes → error.
	// 4. cpus 0 / 65 → error; pids_limit 8 → error.
	// 5. unknown key inside "runner" → strict-decode error (default-deny).
	// 6. data_host_root empty → defaults to data_root.
}
```

Run: `cd backend && go test ./internal/platform/config/ -run TestConfig_Runner -v` → FAIL → implement `Runner` + `validate` clauses + defaults → PASS.

- [ ] **Step 2: Wire serve**

In `runServe`, replace the fixed executor/admission with:

```go
	// Plan 06: the real Docker runner when configured. Disabled by default —
	// production intake is refused until Plan 08's forge adapters, so the
	// honest stub stays the default posture (stubs never fake success).
	var executor runs.Executor = runs.NotImplementedExecutor{}
	var admission runs.Admission = runs.AlwaysAdmit{}
	if cfg.Runner.Enabled {
		rnr, err := buildRunner(cfg.Runner, assetsSvc, runsSvc, log)
		if err != nil {
			return fmt.Errorf("serve: runner: %w", err)
		}
		executor, admission = rnr, rnr
		runnerDone := make(chan struct{})
		go func() { defer close(runnerDone); _ = rnr.Run(ctx) }()
		defer func() { <-runnerDone }()
		log.Info("docker runner enabled", "data_root", cfg.Runner.DataRoot)
	}

	orch := runs.NewOrchestrator(runsSvc, executor, admission, runs.OrchestratorConfig{...}, log)
```

(`defer func(){ <-runnerDone }()` sits before the HTTP block so shutdown waits for the maintenance loop, matching `orchDone`'s pattern — put it alongside the existing `<-orchDone`, not in a defer, if that reads cleaner; the requirement is that serve doesn't return while the loop runs.)

`runnerports.go` holds `buildRunner` (translating `config.Runner` → `runner.Options`, `int64(cfg.Cpus * 1e9)` for `NanoCPUs`) and the three adapters. Note `runsSvc` satisfies `runner.EventSink` via Task 1's `AppendRuntimeEvent` — no adapter needed; assert it compiles with `var _ runner.EventSink = (*runs.Service)(nil)` in `runnerports.go`.

Run: `cd backend && go build ./... && task backend:analyze`
Expected: green (arch tests: `app` may import both contexts and the runner; `runs/runner` importing `runs` is intra-context)

- [ ] **Step 3: Failing app e2e**

`backend/internal/app/runner_e2e_runnerintegration_test.go` (build tag `runnerintegration`; testcontainers Postgres like the other app e2e tests, plus Docker images):

```go
//go:build runnerintegration

// TestRunnerE2E_TaskThroughRealContainerToPatch drives the whole spine
// with the REAL runner: create a project/repo/credential/harness through
// the services, publish a one-node workflow whose instructions script the
// agent, create the task through the runs service with a local bare repo
// as the forge (fake forge for ResolveHead — Plan 08 owns the real one),
// then run the orchestrator until the run is terminal.
//
// Asserts:
//   1. run status = succeeded
//   2. a patch artifact exists whose diff contains the scripted file
//   3. a log_ref artifact exists and its agent log file is on disk
//   4. run_events include phase.started and phase.succeeded for the node
//   5. an audit row records the credential use (assets' UseCredential path)
//   6. no caesar.managed containers or networks survive the run
```

The harness registration points at `caesar-git:dev` with the workflow's `instructions` carrying the script — but production `HarnessSpec` has nil `Entrypoint`/`Cmd`, so the container would run `caesar` with no args. Resolve honestly: this e2e registers the harness with `image_ref: caesar-git:dev` and the **app adapter** stays production-shaped, so the agent container runs `caesar` bare (usage text, exit 2 → `harness_error`). That proves plumbing but not the patch path. So: **the e2e uses a test-local `runner.HarnessResolver`** wrapping the app adapter and injecting `Entrypoint: ["/bin/sh","-c"], Cmd: [script]` — the seam exists precisely because Plan 07's drivers will fill it. Document that in a comment; the production adapter stays untouched.

Run: `cd backend && go test -race -tags runnerintegration -count=1 -timeout 20m ./internal/app/ -run TestRunnerE2E -v`
Expected: PASS

- [ ] **Step 4: Extend the runner lane to `./internal/app/` and commit**

`Taskfile.yml` `backend:test:runner` gains `./internal/app/` to its package list.

Run: `task backend:test:runner`
Expected: green

```bash
git add backend/internal/platform/config/ backend/internal/app/ Taskfile.yml
git commit -m "feat(backend): wire the docker runner into serve behind config"
```

---

### Task 13: CI lane, docs, roadmap, final green

**Files:**
- Modify: `.github/workflows/ci.yml` (new `runner` job), `.github/workflows/nightly.yml` (runner lane too), `backend/README.md`, `docs/superpowers/plans/2026-08-02-caesar-v1-roadmap.md`
- Create (commit): `docs/superpowers/plans/2026-08-04-caesar-06-docker-runner.md` (this document — the Plans 03/04/05 convention: the plan records itself)

- [ ] **Step 1: The CI job**

Add to `.github/workflows/ci.yml` a `runner` job (peer of `backend`, same composite required check): checkout → Go setup identical to `backend` → `task image:build` → `task backend:test:runner`. Ubuntu runners ship Docker, so no service container is needed; give the job `timeout-minutes: 30`. Mirror the same job into `nightly.yml` (it exercises the probes daily against a fresh runner image).

Run: `task lint:actions`
Expected: green

- [ ] **Step 2: README section**

Add "Docker runner and egress gateway (Plan 06)" to `backend/README.md`: the four-container pipeline in one diagram-ish list; the two networks and why the gateway is dual-homed; the staging contract's five clauses; why extraction never trusts `.git`; the enforcement table (quota / log cap / flood / watermark) with the pinned values; how to run the suite (`task image:build && task backend:test:runner`); the honest posture (`runner.enabled` default false; intake still refused until Plan 08); and the host-firewall requirement for prod (agent subnets denied everything but the gateway — pointer to the Plan 13 runbook).

- [ ] **Step 3: Roadmap row**

Row 06 status → `**done** — 2026-08-04-caesar-06-docker-runner.md`.

- [ ] **Step 4: Final green and commit**

Run: `task ci` then `task backend:test:runner`
Expected: green (the `task secrets` linked-worktree caveat from Plans 03–05 still applies; note it rather than fighting it)

```bash
git add .github/workflows/ backend/README.md docs/superpowers/plans/
git commit -m "docs(repo): plan 06 recorded, readme covers the docker runner"
```

---

## Execution notes

- **Prose-specified tests are binding.** Tasks 8–12 enumerate several scenarios as prose or as test stubs with comment bodies instead of full listings; every named scenario must exist as a real test — reviewers reject a task that drops one. The comment bodies describe *what* to assert; the implementer writes the code.
- **Sequencing:** Tasks 1–2 are Postgres-only (no Docker). 3 is pure unit. 4 needs the images built. 5–7 are native-git / httptest — no Docker. 8 needs 3–7. 9–11 need 8. 12 needs everything. 13 last. Only 4, 8, and 12 touch `Taskfile.yml`; only 1 and 2 touch `queries.sql` — don't run those pairs in parallel.
- **Docker is required from Task 4 onward.** `docker info` must succeed; the runner suite skips only when the two images are missing, never when the daemon is absent.
- **Never add test-only hooks to production types.** `Options` carries no `skipTeardown`-style fields; same-package integration tests call unexported functions directly instead. The one sanctioned exception is the unexported `testHostAccess` field enabling `ExtraHosts` on the *gateway* container so the fake provider is reachable — it is unexported, unset in production wiring, and documented at its declaration.
- **Timing-sensitive tests** use bounded polling on observable state (container status, file contents, DB rows), never bare sleeps as correctness gates. Deadline tests assert "finished well under the ceiling", not exact durations.
- **Secrets in tests** use obviously-fake values (`FORGE-SEKRET`, `real-provider-key`) that gitleaks won't flag; if the secrets lane complains, change the fixture, never the gitleaks config.
- **What Plan 07 picks up:** harness drivers filling `HarnessSpec.Entrypoint/Cmd` and the filesystem contract (`prompt` and context files mounted in, `.caesar/out/` parsed per harness), per-harness exit-condition → outcome mapping (replacing this plan's blanket `harness_error`), per-harness FQDN allowlists feeding `Options.ConnectAllow`, and artifact-lineage materialization (the ordered patch chain into the staged workspace — this plan stages base SHA only).
- **What Plan 08 picks up:** `RunValidation` on the publish path, the forge port replacing `appGitSource`'s hardcoded URL construction, and gate/publish nodes at intake.
- **What Plan 09 picks up:** kill-switch integration (the gateway is the first kill target — `Runner` already exposes container kill by attempt name via `Inspect`'s kill path; Plan 09 adds an explicit `KillAttempt`), token-level provider metering into cost ceilings, policy-supplied quota/limit values replacing `Options` constants, and validation-CONNECT authentication.
- **Deferred deliberately:** log/snapshot retention sweeping (Plan 13, with the deploy stack that owns the volume), XFS project quotas (Plan 13 VM recipe), the k8s adapter (Plan 15 — it renders the same RunSpec).
