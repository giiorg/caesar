# Plan 10 — Realtime SSE Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver gapless, authorization-scoped Server-Sent Event streams — one per run and one global attention stream — so the UI receives run state as it commits instead of polling.

**Architecture:** A new `internal/realtime` bounded context owns a single per-process tailer goroutine that follows `run_events` (woken by `LISTEN/NOTIFY`, slow-poll fallback) and fans out in-memory to bounded per-subscriber queues. Because the arch rules forbid one bounded context importing another, `realtime` declares outbound ports (`EventSource`, `GrantResolver`) and `internal/app` wires the `runs` and `identity` implementations into them. Gaplessness comes from the spec's ordering: subscribe to fan-out first, then read snapshot + high-watermark in one repeatable-read transaction, then replay past the watermark and splice, deduplicating on the commit-ordered `run_events.id`.

**Tech Stack:** Go 1.26.5 · pgx/v5 (`LISTEN`/`NOTIFY` on a dedicated connection) · sqlc for queries · stdlib `net/http` ServeMux · `swaggest/openapi-go` for the contract · stdlib asserts + go-cmp (no testify) · testcontainers-backed Postgres 18 for integration lanes.

---

## Global Constraints

These are project-wide and apply to **every** task below. They are copied from the locked decisions and the V1 spec; do not re-litigate them.

- **Go 1.26.5**, Postgres major **18**. No ORMs (pgx + sqlc). No DI frameworks. `log/slog` only. stdlib `flag` only. Strict JSON config via `platform/config` only. stdlib errors + `%w`.
- **Banned imports** (enforced by `internal/arch` `DenyList`, CI-failing): `testify`, `gorm.io`, `entgo.io`, `go.uber.org/fx`, `go.uber.org/dig`, `go.uber.org/zap`, `logrus`, `zerolog`, `cobra`, `viper`, `godotenv`, `pkg/errors`. Use **stdlib asserts + go-cmp**.
- **Bounded contexts never import each other** (`arch.CheckContextIsolation`). `internal/realtime` must not import `internal/runs` or `internal/identity`. Cross-context wiring happens **only** in `internal/app`.
- **Contexts never import `internal/app`** (`arch.CheckNoAppImports`). **`internal/platform` imports no business code** (`arch.CheckPlatformPurity`).
- **Go types are the source of truth for the contract.** Every endpoint is declared once as an `httpx.Operation`; `internal/contracts` reflects it into `backend/api/openapi.json`, which generates `web/src/api/schema.d.ts`. `task contracts:check` regenerates and diffs — a hand-edited artifact fails CI.
- **Errors** use the two-axis internal model (`xerrors.E(class, msg)`) and surface at the boundary as RFC 9457 `application/problem+json` via `httpx.Problem`.
- **The requester principal rides the request context into every domain read/write.** Streams are authorization-scoped: snapshot, replay and live paths all filter by the requester's current grants.
- **LISTEN/NOTIFY is a wake-up signal only, never the data** (locked decision). It is at-most-once and non-durable. Consumers read state first, then listen; the slow-poll fallback guarantees correctness. A lost notification costs latency, never correctness. **Any design where a dropped NOTIFY loses an event is wrong.**
- **Process-local fan-out is a stated monolith assumption.** Do not build cross-process fan-out.
- **Commit-ordered ids.** `run_events.id` is allocated under the `run_event_counter` row lock held until the writer commits, so id order **is** commit order. This is the property every dedup and cursor in this plan rests on. Sequences would break it; never substitute one.
- Every task ends with `scripts/gate <abs-worktree-path> <sha> <branch>` green — 11 lanes, unnarrowed.

---

## Pre-flight findings (read before Task 1)

These were verified against the tree at `64de023`. They contradict assumptions a reader of the spec alone would make.

1. **`GET /api/v1/runs/{id}/events` already exists** as a JSON poll (`listRunEvents`, cursor `?after=`, `RunEventsResponse`). **Ruling (sofo, 2026-08-10): content-negotiate on `Accept`.** The path serves SSE for `Accept: text/event-stream` and keeps the existing JSON poll otherwise. The poll is the honest non-SSE fallback and its contract must not break.
2. **`httpx.Operation` cannot express a response content type.** It has `Response any` + `Status int` only. Task 1 extends it. `contracts.Build` already has `openapi.WithContentType` available, so the generator side is a small change.
3. **There is no `LISTEN`/`NOTIFY` anywhere in the tree.** This plan introduces the first one. (`egress/gateway.go:105` matches the grep but is a TCP listener.)
4. **`run_events` needs no migration for this plan.** It already carries `id`, `run_id`, `project_id`, `kind`, `payload`, `occurred_at`, `correlation_id`, plus indexes on `(run_id, id)` and `(project_id, id)` — precisely the two access paths the two streams need.
5. **The `outbox` table has no consumer in V1 and gains none here.** The tailer follows `run_events` directly, per spec §4. Do not wire the outbox into the tailer.
6. **`internal/realtime` will be treated as a bounded context automatically** — `arch.nonContext` lists only `platform`, `app`, `arch`, `analyzers`, `contracts`. Isolation therefore applies from the first commit.
7. **The per-run route must stay owned by `runs/api`.** A `ServeMux` pattern can be registered exactly once, and `runs/api` already owns `GET /api/v1/runs/{id}/events`. It delegates to realtime through a port wired in `app`. The **global** stream `GET /api/v1/stream` is new and is owned by `realtime/api`.
8. **Grant revocation closes streams by re-check, not by callback.** Ruling (sofo, 2026-08-10): each subscriber re-resolves grants on an interval and before each delivery batch. This avoids cross-context coupling and also catches revocations made outside the API. The staleness window is bounded by the re-check interval and must be configurable.
9. **`RunEventResponse` (`id`, `kind`, `payload`, `occurred_at`) is insufficient for the global stream**, which needs `run_id` and project scope. Task 6 introduces a distinct stream frame rather than widening the poll DTO — widening it would change the poll contract, which finding 1 forbids.

---

## File Structure

**New — `internal/realtime` (the bounded context):**

- `realtime/realtime.go` — the context's domain types: `Event`, `Watermark`, `Subscription`, `Filter`. No I/O.
- `realtime/ports.go` — outbound ports: `EventSource` (snapshot + replay + tail), `GrantResolver` (scope for a principal). Implemented in `app`.
- `realtime/tailer.go` — the single tailer goroutine: `LISTEN` + slow-poll fallback, cursor advance, hand-off to the hub.
- `realtime/hub.go` — subscriber registry and in-memory fan-out; bounded queues; slow-subscriber eviction.
- `realtime/subscription.go` — one subscriber's lifecycle: queue, watermark splice, dedup, grant re-check, close reasons.
- `realtime/sse.go` — the SSE wire codec: frame writing, `id:`/`event:`/`data:`, heartbeat, immediate flush.
- `realtime/api/api.go` — the global attention stream route + handler.
- `realtime/api/dto.go` — the stream frame DTO reflected into the contract.

**Modified:**

- `platform/httpx/operation.go` — add `ResponseContentType`.
- `contracts/contracts.go` — honour it; register the realtime route table.
- `runs/events.go` — emit `pg_notify` in the writer's transaction.
- `runs/api/api.go`, `runs/api/dto.go` — content negotiation on `/events`; delegate to the stream port.
- `app/serve.go` — construct and run the tailer/hub; shut them down before the HTTP server.
- `app/realtimeports.go` **(new)** — adapt `runs` and `identity` to realtime's ports.
- `platform/config/config.go` + `backend/caesar.dev.json` — a `realtime` config section.
- `backend/README.md` — operator-facing stream documentation.

---

### Task 1: Let an Operation declare its response content type

The contract generator hard-codes JSON. An SSE endpoint declared today would publish a lying OpenAPI document. Fix the declaration machinery before anything declares a stream.

**Files:**
- Modify: `backend/internal/platform/httpx/operation.go`
- Modify: `backend/internal/contracts/contracts.go:155-159`
- Test: `backend/internal/contracts/contracts_test.go`

**Interfaces:**
- Consumes: nothing (first task).
- Produces: `httpx.Operation.ResponseContentType string` — empty means `application/json`. Tasks 7 and 8 set it to `text/event-stream`.

- [ ] **Step 1: Write the failing test**

Append to `backend/internal/contracts/contracts_test.go`:

```go
func TestContentTypeOf_DefaultsToJSON(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		op   httpx.Operation
		want string
	}{
		{"unset means json", httpx.Operation{}, "application/json"},
		{"explicit stream", httpx.Operation{ResponseContentType: "text/event-stream"}, "text/event-stream"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := contentTypeOf(tc.op); got != tc.want {
				t.Errorf("contentTypeOf() = %q, want %q", got, tc.want)
			}
		})
	}
}

// The existing document must not move when the field is merely added.
// Every operation today is JSON, so adding the knob is a no-op on bytes.
func TestBuild_UnchangedWhenNoOperationOptsIn(t *testing.T) {
	t.Parallel()
	raw, err := Build()
	if err != nil {
		t.Fatal(err)
	}
	committed, err := os.ReadFile(filepath.Join("..", "..", "api", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(string(committed), string(raw)); diff != "" {
		t.Errorf("generated document drifted from the committed one (-committed +generated):\n%s", diff)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `cd backend && PKG=./internal/contracts/ task backend:test -- -run 'TestContentTypeOf|TestBuild_Unchanged'`
Expected: FAIL — `undefined: contentTypeOf` and `unknown field ResponseContentType`.

- [ ] **Step 3: Add the field**

In `backend/internal/platform/httpx/operation.go`, inside `Operation`, after `Status`:

```go
	// ResponseContentType overrides the success response media type.
	// Empty means "application/json". Set it for endpoints that do not
	// answer with a JSON body — SSE streams declare "text/event-stream"
	// so the published contract matches what the handler actually writes.
	ResponseContentType string
```

- [ ] **Step 4: Honour it in the generator**

In `backend/internal/contracts/contracts.go`, replace the success-response block at lines 155-159:

```go
			if op.Response != nil {
				oc.AddRespStructure(op.Response,
					openapi.WithHTTPStatus(op.Status),
					openapi.WithContentType(contentTypeOf(op)))
			} else {
				oc.AddRespStructure(nil, openapi.WithHTTPStatus(op.Status))
			}
```

and add below `requestStructure`:

```go
// contentTypeOf is the success response media type. Operations that do not
// declare one answer with JSON, which is every REST endpoint; the streams
// in Plan 10 are the only opt-outs.
func contentTypeOf(op httpx.Operation) string {
	if op.ResponseContentType == "" {
		return "application/json"
	}
	return op.ResponseContentType
}
```

- [ ] **Step 5: Run the tests and the contract gate**

Run: `cd backend && PKG=./internal/contracts/ task backend:test -- -run 'TestContentTypeOf|TestBuild_Unchanged'`
Expected: PASS.

Run: `task contracts:check`
Expected: exit 0, no diff. **If the document moved, stop** — `WithContentType("application/json")` must be a no-op against the current output. Read the diff before touching the committed artifact.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/platform/httpx/operation.go backend/internal/contracts/contracts.go backend/internal/contracts/contracts_test.go
git commit -m "feat(backend): let an Operation declare its response content type"
```

---

### Task 2: The realtime context's domain types, ports and config

No I/O, no goroutines — just the vocabulary every later task speaks, plus the config knobs. Landing this alone keeps the arch-isolation question answerable at the first commit rather than the tenth.

**Files:**
- Create: `backend/internal/realtime/realtime.go`
- Create: `backend/internal/realtime/ports.go`
- Create: `backend/internal/realtime/realtime_test.go`
- Modify: `backend/internal/platform/config/config.go`
- Modify: `backend/caesar.dev.json`
- Test: `backend/internal/platform/config/config_test.go`

**Interfaces:**
- Consumes: nothing from Task 1.
- Produces: `realtime.Event`, `realtime.Scope`, `realtime.Filter`, `realtime.EventSource`, `realtime.GrantResolver`, `config.Realtime`. Tasks 4-9 consume all of them.

- [ ] **Step 1: Write the failing test for the filter**

Create `backend/internal/realtime/realtime_test.go`:

```go
package realtime

import "testing"

func TestFilter_Matches(t *testing.T) {
	t.Parallel()
	projA, projB := uuid.New(), uuid.New()
	runA := uuid.New()
	ev := Event{ID: 7, RunID: runA, ProjectID: projA, Kind: "gate.waiting"}

	for _, tc := range []struct {
		name string
		f    Filter
		want bool
	}{
		{"run stream matches its run", Filter{RunID: &runA, Scope: Scope{Projects: []uuid.UUID{projA}}}, true},
		{"run stream rejects another run", Filter{RunID: &[]uuid.UUID{uuid.New()}[0], Scope: Scope{Projects: []uuid.UUID{projA}}}, false},
		{"attention stream takes attention kinds", Filter{Attention: true, Scope: Scope{Projects: []uuid.UUID{projA}}}, true},
		{"attention stream drops phase noise", Filter{Attention: true, Scope: Scope{Projects: []uuid.UUID{projA}}}, false},
		{"scope excludes other projects", Filter{RunID: &runA, Scope: Scope{Projects: []uuid.UUID{projB}}}, false},
		{"admin scope takes everything", Filter{RunID: &runA, Scope: Scope{All: true}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := ev
			if tc.name == "attention stream drops phase noise" {
				in.Kind = "phase.started"
			}
			if got := tc.f.Matches(in); got != tc.want {
				t.Errorf("Matches(%+v) = %v, want %v", in, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `cd backend && PKG=./internal/realtime/ task backend:test -- -run TestFilter_Matches`
Expected: FAIL — the package does not exist.

- [ ] **Step 3: Write the domain types**

Create `backend/internal/realtime/realtime.go`:

```go
// Package realtime owns the SSE streams: a single per-process tailer over
// run_events, in-memory fan-out to bounded per-subscriber queues, and the
// snapshot-then-deltas splice that makes a stream gapless (spec §4).
//
// It is a bounded context and therefore imports no other context. What it
// needs from runs and identity arrives through the ports in ports.go,
// wired in internal/app.
package realtime

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// attentionPrefixes are the event-kind namespaces the global stream
// carries. Plan 08 deliberately gave gates and publications their own
// namespaces (see runs/events.go) so this filter is a prefix test and
// never an enumeration that rots as kinds are added.
var attentionPrefixes = []string{"gate.", "publish.", "run."}

// Event is one streamable observation. It mirrors a run_events row; the
// id is commit-ordered (allocated under the run_event_counter lock), which
// is what makes it usable as both a cursor and a dedup key.
type Event struct {
	ID            int64
	RunID         uuid.UUID
	ProjectID     uuid.UUID
	Kind          string
	Payload       map[string]any
	OccurredAt    time.Time
	CorrelationID string
}

// Scope is a principal's current authorization footprint. All is the
// admin case; Projects is the exact grant set otherwise. An empty Scope
// matches nothing — default-deny.
type Scope struct {
	All      bool
	Projects []uuid.UUID
}

func (s Scope) allows(project uuid.UUID) bool {
	if s.All {
		return true
	}
	for _, p := range s.Projects {
		if p == project {
			return true
		}
	}
	return false
}

// Filter selects the events one subscription receives. Exactly one of
// RunID (per-run stream) or Attention (global stream) is set.
type Filter struct {
	RunID     *uuid.UUID
	Attention bool
	Scope     Scope
}

// Matches decides delivery. Scope is checked FIRST and unconditionally:
// every path — snapshot, replay and live — funnels through here, so an
// event outside the caller's grants cannot reach a subscriber by any
// route.
func (f Filter) Matches(e Event) bool {
	if !f.Scope.allows(e.ProjectID) {
		return false
	}
	if f.RunID != nil {
		return *f.RunID == e.RunID
	}
	if !f.Attention {
		return false
	}
	for _, p := range attentionPrefixes {
		if strings.HasPrefix(e.Kind, p) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Write the ports**

Create `backend/internal/realtime/ports.go`:

```go
package realtime

import (
	"context"

	"github.com/google/uuid"
)

// EventSource is the read side of run_events. internal/app implements it
// over the runs store; realtime never imports runs.
type EventSource interface {
	// SnapshotWatermark returns the highest committed event id, read in
	// the SAME repeatable-read transaction the caller's snapshot was
	// computed in. Both values must come from one tx or an event
	// committing between them is lost — the gap this whole design exists
	// to prevent.
	SnapshotWatermark(ctx context.Context, f Filter) (Watermark, error)

	// Replay returns events with id > after that match f, oldest first,
	// at most limit rows. It is the catch-up path for both the initial
	// splice and Last-Event-ID resume.
	Replay(ctx context.Context, f Filter, after int64, limit int) ([]Event, error)

	// Since returns every event with id > after regardless of filter, for
	// the tailer's cursor advance. Filtering is per-subscriber.
	Since(ctx context.Context, after int64, limit int) ([]Event, error)

	// Oldest is the lowest retained event id, used to decide whether a
	// Last-Event-ID is too old to resume from and must re-snapshot.
	Oldest(ctx context.Context) (int64, error)
}

// Watermark is the high-water event id a snapshot was computed at.
type Watermark struct {
	EventID int64
}

// GrantResolver answers "what may this principal see, right now". It is
// re-consulted on an interval for the lifetime of every subscription so a
// revoked grant closes the stream (spec §4) without coupling realtime to
// identity.
type GrantResolver interface {
	ScopeFor(ctx context.Context, userID uuid.UUID) (Scope, error)
}
```

- [ ] **Step 5: Run the filter test**

Run: `cd backend && PKG=./internal/realtime/ task backend:test -- -run TestFilter_Matches`
Expected: PASS, all six cases.

- [ ] **Step 6: Add the config section**

In `backend/internal/platform/config/config.go`, add to `Config` after `Forge`:

```go
	Realtime     Realtime     `json:"realtime"`
```

and the type beside the other sections:

```go
// Realtime tunes the SSE streams (spec §4). Heartbeat honours the tunnel's
// idle timeout; SubscriberQueue bounds a slow client's cost — it is
// disconnected rather than allowed to backpressure the tailer.
type Realtime struct {
	HeartbeatInterval  Duration `json:"heartbeat_interval"`
	PollFallback       Duration `json:"poll_fallback"`
	GrantRecheck       Duration `json:"grant_recheck"`
	SubscriberQueue    int      `json:"subscriber_queue"`
	ReplayBatch        int      `json:"replay_batch"`
}
```

- [ ] **Step 7: Write the config test, then the defaults/validation**

Follow the existing table in `config_test.go`. Assert: a `realtime` block round-trips; `heartbeat_interval` of `"0s"` is rejected; `subscriber_queue` of `0` is rejected. Then add the validation beside the sibling sections' rules, and the dev values to `backend/caesar.dev.json`:

```json
  "realtime": {
    "heartbeat_interval": "20s",
    "poll_fallback": "2s",
    "grant_recheck": "30s",
    "subscriber_queue": 256,
    "replay_batch": 500
  }
```

`heartbeat_interval` is `20s` because the spec pins "~20s" as the tunnel obligation. Do not raise it without re-reading §4.

- [ ] **Step 8: Gate and commit**

Run: `scripts/gate <abs-worktree-path> <sha> <branch>` — expected GATE PASS. The arch lane is the one that matters here: it proves `internal/realtime` is isolated.

```bash
git add backend/internal/realtime backend/internal/platform/config/config.go backend/internal/platform/config/config_test.go backend/caesar.dev.json
git commit -m "feat(backend): realtime context types, ports and config"
```

---

### Task 3: Wake the tailer with pg_notify in the writer's transaction

**Files:**
- Modify: `backend/internal/runs/events.go`
- Test: `backend/internal/runs/events_integration_test.go` (create)

**Interfaces:**
- Consumes: nothing from Tasks 1-2.
- Produces: the Postgres channel name `caesar_run_events`, consumed by Task 4's tailer.

**Why this shape:** `NOTIFY` issued inside a transaction is delivered **on commit** and discarded on rollback. Emitting it in `appendEvents` therefore gives exactly the semantics wanted: a listener is woken only for events that actually exist. The payload is deliberately **empty** — the locked decision says LISTEN/NOTIFY is a wake-up signal and never the data, and an empty payload makes it impossible for a future reader to start trusting it.

- [ ] **Step 1: Write the failing test**

Create `backend/internal/runs/events_integration_test.go`:

```go
//go:build integration

package runs_test

// TestAppendEvents_NotifiesOnCommitOnly pins the two halves of the
// contract: a committed append wakes a listener, and a rolled-back one
// does not. The second half is the one worth having — a tailer woken for
// an event that never committed would advance its cursor past a gap.
func TestAppendEvents_NotifiesOnCommitOnly(t *testing.T) {
	// 1. Take a dedicated pgx conn; LISTEN caesar_run_events.
	// 2. In another tx, append one event and COMMIT.
	//    Expect: WaitForNotification returns within 5s, channel matches,
	//    payload is "".
	// 3. In another tx, append one event and ROLLBACK.
	//    Expect: WaitForNotification with a 1s deadline returns
	//    context.DeadlineExceeded — no notification.
}
```

Write it out in full against the existing helpers in `internal/runs/testutil_integration_test.go`; use `testdb.New(t)` for the pool and `pool.Acquire` for the listener connection. The listener must be its own connection — a pooled one may be handed to another caller mid-test.

- [ ] **Step 2: Run it and watch it fail**

Run: `cd backend && PKG=./internal/runs/ task backend:test:integration -- -run TestAppendEvents_NotifiesOnCommitOnly`
Expected: FAIL — the commit case times out, because nothing notifies yet.

- [ ] **Step 3: Emit the notification**

At the end of `appendEvents` in `backend/internal/runs/events.go`, after the rows are written and before returning nil:

```go
	// Wake any tailer. Inside a tx, NOTIFY is delivered on COMMIT and
	// dropped on rollback, so a listener is only ever woken for events
	// that exist. The payload is empty on purpose: LISTEN/NOTIFY is a
	// wake-up signal and never the data (locked decision §2) — the tailer
	// re-reads from its own cursor and a lost notification costs latency,
	// never correctness.
	if _, err := tx.Exec(ctx, `NOTIFY caesar_run_events`); err != nil {
		return fmt.Errorf("runs: notify run events: %w", err)
	}
```

- [ ] **Step 4: Run the test**

Run: `cd backend && PKG=./internal/runs/ task backend:test:integration -- -run TestAppendEvents_NotifiesOnCommitOnly`
Expected: PASS, both halves.

- [ ] **Step 5: Mutate to prove the rollback half is load-bearing**

Temporarily move the `NOTIFY` to run **after** the caller commits (or issue it on a separate connection). Re-run: the rollback case must FAIL. Revert. Record the mutant and its output in the task report — a green rollback assertion that cannot go red is not evidence.

- [ ] **Step 6: Gate and commit**

```bash
git add backend/internal/runs/events.go backend/internal/runs/events_integration_test.go
git commit -m "feat(backend): notify caesar_run_events on commit"
```

---

### Task 4: The single tailer

One goroutine per process follows `run_events` and hands batches to the hub. It is woken by `LISTEN`, and it polls on a fallback interval regardless — the poll is what makes a lost notification a latency cost rather than a correctness one.

**Files:**
- Create: `backend/internal/realtime/tailer.go`
- Create: `backend/internal/realtime/tailer_test.go`

**Interfaces:**
- Consumes: `realtime.Event`, `realtime.EventSource` (Task 2).
- Produces: `realtime.Tailer`, `realtime.NewTailer(src EventSource, w Waker, sink func([]Event), cfg TailerConfig, log *slog.Logger) *Tailer` and `(*Tailer).Run(ctx) error`. Task 12 runs it from `serve.go`; Task 5's hub is the `sink`.

**Design constraints:**
- The cursor starts at the **current maximum** event id, not 0. A process starting up must not replay history into the hub — subscribers get history from their own snapshot+replay, and the hub has no subscribers older than the process.
- The tailer never blocks on a subscriber. `sink` must be non-blocking by contract (Task 5 guarantees it); document that here and depend on it.
- A `Since` error is logged and retried on the next tick. It must never kill the goroutine — a transient DB blip would otherwise silently end all realtime for the process lifetime.

- [ ] **Step 1: Write the failing tests**

Create `backend/internal/realtime/tailer_test.go` with a fake `EventSource` and a fake `Waker` (a channel). Cases:

```go
// 1. TestTailer_StartsAtHead — a source holding ids 1..5 with a tailer
//    started fresh delivers NOTHING until id 6 exists. Asserts the
//    startup cursor is max(id), not 0.
// 2. TestTailer_DeliversInIDOrder — ids arriving out of insertion order
//    from the source are handed to the sink sorted ascending, and the
//    cursor ends at the highest id seen.
// 3. TestTailer_PollsWithoutNotification — with the waker never firing,
//    a new event is still delivered within two poll intervals. This is
//    the "lost NOTIFY costs latency, never correctness" guarantee.
// 4. TestTailer_SurvivesSourceError — Since returns an error once, then
//    succeeds; the event is still delivered and Run has not returned.
// 5. TestTailer_StopsOnContextCancel — Run returns ctx.Err() promptly
//    and does not call sink afterwards.
```

- [ ] **Step 2: Run them and watch them fail**

Run: `cd backend && PKG=./internal/realtime/ task backend:test -- -run TestTailer`
Expected: FAIL — `undefined: NewTailer`.

- [ ] **Step 3: Implement the tailer**

```go
package realtime

// Waker reports database wake-ups. The pg implementation lives in app;
// a channel fake stands in for it in tests.
type Waker interface {
	// Wait blocks until a notification arrives, ctx is done, or the
	// implementation decides to return early. A nil error means "maybe
	// something happened" — never "definitely".
	Wait(ctx context.Context) error
}

type TailerConfig struct {
	PollFallback time.Duration
	Batch        int
}

// Tailer is the single goroutine that follows run_events for a process.
type Tailer struct {
	src    EventSource
	waker  Waker
	sink   func([]Event)
	cfg    TailerConfig
	log    *slog.Logger
	cursor int64
}

// Run follows the table until ctx is cancelled. The loop is: wait for a
// wake-up OR the poll deadline, then drain everything past the cursor.
// Draining is unconditional — the wake-up is only an optimisation, so a
// notification lost between LISTEN and the query costs one poll interval.
func (t *Tailer) Run(ctx context.Context) error {
	head, err := t.src.Since(ctx, math.MaxInt64, 0) // see note below
	...
}
```

Implementation notes for the engineer:

- Seed the cursor with a dedicated `Oldest`-style read. Rather than overloading `Since`, add `Head(ctx) (int64, error)` to `EventSource` in Task 2's port file **now** (and to Task 7's implementation) — reusing `Since` with a sentinel is the kind of cleverness that reads as a bug in six months.
- Drain in a loop while a full batch comes back, so a burst larger than `Batch` is not left waiting for the next tick.
- Advance the cursor **only** to the highest id actually handed to the sink.
- Wrap the wait in a `time.Timer` reset per iteration; do not allocate a ticker per loop.

- [ ] **Step 4: Run the tests**

Run: `cd backend && PKG=./internal/realtime/ task backend:test -- -run TestTailer`
Expected: PASS, all five.

- [ ] **Step 5: Race check**

Run: `cd backend && PKG=./internal/realtime/ task backend:test -- -run TestTailer -race -count=20`
Expected: PASS, no race reports.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/realtime/tailer.go backend/internal/realtime/tailer_test.go backend/internal/realtime/ports.go
git commit -m "feat(backend): single tailer over run_events"
```

---

### Task 5: The hub — fan-out with bounded queues

**Files:**
- Create: `backend/internal/realtime/hub.go`
- Create: `backend/internal/realtime/hub_test.go`

**Interfaces:**
- Consumes: `Event`, `Filter` (Task 2).
- Produces: `realtime.NewHub(cfg HubConfig) *Hub`, `(*Hub).Subscribe(f Filter) *Subscription`, `(*Hub).Publish(evs []Event)` (the tailer's `sink`), `(*Subscription).C() <-chan Event`, `(*Subscription).Close(reason CloseReason)`, `(*Subscription).Err() error`. Tasks 8 and 9 subscribe; Task 12 constructs.

**The rule this task exists to enforce:** `Publish` never blocks. A subscriber whose queue is full is **evicted** — its channel closed with `CloseSlowConsumer` — and the client reconnects with `Last-Event-ID`. Backpressuring the tailer would let one slow phone stall every stream in the process.

- [ ] **Step 1: Write the failing tests**

```go
// 1. TestHub_FansOutToMatchingSubscribersOnly — two subscriptions with
//    different filters; each receives exactly its own events.
// 2. TestHub_PublishNeverBlocks — a subscriber that never reads, queue
//    size 4, publish 1000 events. Publish must return promptly (assert
//    under a 1s deadline with the subscriber genuinely idle) and the
//    subscription must be closed with CloseSlowConsumer.
// 3. TestHub_EvictedSubscriberReportsWhy — after eviction, Err() is
//    ErrSlowConsumer, and the channel is closed exactly once.
// 4. TestHub_CloseIsIdempotent — Close twice does not panic and does not
//    double-close the channel.
// 5. TestHub_UnsubscribeRemovesFromRegistry — after Close, a Publish
//    does not touch the evicted subscription (assert via a counter and
//    the race detector).
```

- [ ] **Step 2: Run them and watch them fail**

Run: `cd backend && PKG=./internal/realtime/ task backend:test -- -run TestHub`
Expected: FAIL — `undefined: NewHub`.

- [ ] **Step 3: Implement**

Sketch the engineer must complete:

```go
// Hub fans out to process-local subscribers. Process-local is a stated
// monolith assumption (spec §4) — there is no cross-process fan-out and
// none is wanted.
type Hub struct {
	mu   sync.RWMutex
	subs map[*Subscription]struct{}
	cfg  HubConfig
}

// Publish delivers to every matching subscriber WITHOUT blocking. A full
// queue means the client cannot keep up: evict it and let it resume via
// Last-Event-ID. Never grow the queue and never wait.
func (h *Hub) Publish(evs []Event) {
	h.mu.RLock()
	targets := make([]*Subscription, 0, len(h.subs))
	for s := range h.subs {
		targets = append(targets, s)
	}
	h.mu.RUnlock()

	for _, s := range targets {
		for _, e := range evs {
			if !s.filter.Matches(e) {
				continue
			}
			select {
			case s.ch <- e:
			default:
				h.evict(s, CloseSlowConsumer)
			}
		}
	}
}
```

Note the lock discipline: collect targets under `RLock`, then deliver **outside** the lock, so an eviction (which takes the write lock) cannot deadlock against a delivery.

- [ ] **Step 4: Run the tests, then the race detector**

Run: `cd backend && PKG=./internal/realtime/ task backend:test -- -run TestHub -race -count=20`
Expected: PASS. Test 2 is the load-bearing one; if it passes with a blocking send, the test is wrong, not the code — verify by mutating `default:` away and watching it hang.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/realtime/hub.go backend/internal/realtime/hub_test.go
git commit -m "feat(backend): in-memory fan-out with bounded subscriber queues"
```

---

### Task 6: The SSE wire codec

**Files:**
- Create: `backend/internal/realtime/sse.go`
- Create: `backend/internal/realtime/sse_test.go`

**Interfaces:**
- Consumes: `Event`.
- Produces: `realtime.NewWriter(w http.ResponseWriter, hb time.Duration, clock func() time.Time) (*Writer, error)`, `(*Writer).WriteEvent(Event) error`, `(*Writer).Heartbeat() error`, `(*Writer).Flush()`. Tasks 8 and 9 use it.

**Wire obligations (spec §4 + the tunnel):**
- Headers written and flushed **immediately** on connect, before any event: `Content-Type: text/event-stream`, `Cache-Control: no-cache`, `Connection: keep-alive`, `X-Accel-Buffering: no`.
- Every frame carries `id:` set to the event id — this is what the browser echoes back as `Last-Event-ID`.
- `event:` is the event kind, so clients can `addEventListener` per kind.
- `data:` is one line of compact JSON. **Multi-line data must be split across repeated `data:` lines** or the frame is malformed; JSON compact encoding avoids this, and a test pins it.
- Heartbeat is an SSE comment line (`: hb\n\n`) — it keeps the tunnel open without appearing as an event to the client.

- [ ] **Step 1: Write the failing tests**

```go
// 1. TestWriter_WritesHeadersAndFlushesBeforeAnyEvent — using an
//    httptest.ResponseRecorder wrapped to record flush order, assert the
//    first Flush happens before the first WriteEvent.
// 2. TestWriter_FrameShape — a known Event produces exactly:
//    "id: 42\nevent: gate.waiting\ndata: {...}\n\n"
// 3. TestWriter_DataIsSingleLine — a payload containing "\n" in a string
//    value still yields exactly one data: line (JSON escapes it).
// 4. TestWriter_HeartbeatIsAComment — Heartbeat writes ": hb\n\n" and
//    nothing that could parse as an event.
// 5. TestWriter_RejectsNonFlusher — NewWriter returns an error when the
//    ResponseWriter does not implement http.Flusher, rather than
//    silently buffering a stream nobody receives.
```

- [ ] **Step 2: Run and watch fail.** `cd backend && PKG=./internal/realtime/ task backend:test -- -run TestWriter` → FAIL.

- [ ] **Step 3: Implement `sse.go`.** Keep it dependency-free: `net/http`, `encoding/json`, `fmt`, `time`. No third-party SSE library — the deny-list philosophy and the four-line frame format make one pure cost.

- [ ] **Step 4: Run the tests.** Expected PASS, all five.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/realtime/sse.go backend/internal/realtime/sse_test.go
git commit -m "feat(backend): SSE frame codec with heartbeats"
```

---

### Task 7: The EventSource implementation — snapshot and watermark in one transaction

This is where gaplessness is won or lost.

**Files:**
- Create: `backend/internal/app/realtimeports.go`
- Modify: `backend/internal/runs/store/queries.sql` (+ regenerate `queries.sql.go`)
- Create: `backend/internal/app/realtimeports_integration_test.go`

**Interfaces:**
- Consumes: `realtime.EventSource`, `realtime.GrantResolver`, `realtime.Filter`, `realtime.Watermark` (Task 2, extended with `Head` in Task 4).
- Produces: `app.realtimeEventSource` and `app.realtimeGrants`, satisfying both ports. Task 12 constructs them.

**The transaction rule:** `SnapshotWatermark` must open a **`repeatable read`** transaction, read the watermark, and — for a per-run filter — read the run's current projection in that same transaction. Two separate reads, or read-committed, permits an event to commit between them: the snapshot would already include its effect while the watermark says it has not happened, so replay skips it and the client never sees the delta. That is the exact gap the spec's "single repeatable-read transaction" clause forbids.

- [ ] **Step 1: Write the failing integration test**

```go
//go:build integration

// TestSnapshotWatermark_IsGaplessUnderConcurrentCommit is the acceptance
// evidence for this task. Shape:
//   1. Seed a run with events 1..N.
//   2. Start SnapshotWatermark, and while its transaction is open, commit
//      a new event from a second connection. Use a pg advisory lock or a
//      testdb fault hook to hold the transaction open deterministically —
//      do NOT use a sleep; a timing-based version of this test passes on
//      a fast machine and proves nothing.
//   3. Assert: the returned watermark is < the new event's id.
//   4. Replay(after: watermark) returns the new event.
// Together those two assertions mean no event can fall between the
// snapshot and the replay.
```

`backend/internal/platform/testdb/faults.go` already exists (added in Plan 08) — check whether its hooks can hold a transaction open before inventing a new mechanism.

- [ ] **Step 2: Run it and watch it fail.** `PKG=./internal/app/ task backend:test:integration -- -run TestSnapshotWatermark` → FAIL (undefined).

- [ ] **Step 3: Add the sqlc queries**

In `backend/internal/runs/store/queries.sql`:

```sql
-- name: MaxRunEventID :one
SELECT coalesce(max(id), 0)::bigint FROM run_events;

-- name: MinRunEventID :one
SELECT coalesce(min(id), 0)::bigint FROM run_events;

-- name: ListRunEventsSince :many
SELECT id, run_id, project_id, kind, payload, occurred_at, correlation_id
FROM run_events
WHERE id > @after
ORDER BY id
LIMIT @lim;

-- name: ListRunEventsForRunSince :many
SELECT id, run_id, project_id, kind, payload, occurred_at, correlation_id
FROM run_events
WHERE run_id = @run_id AND id > @after
ORDER BY id
LIMIT @lim;

-- name: ListAttentionEventsSince :many
SELECT id, run_id, project_id, kind, payload, occurred_at, correlation_id
FROM run_events
WHERE id > @after AND project_id = ANY(@project_ids::uuid[])
ORDER BY id
LIMIT @lim;
```

The first two serve `Head`/`Oldest`; the run-scoped one rides `run_events_run_id_idx`, the project-scoped one rides `run_events_project_id_idx`. Both indexes already exist — confirm with `EXPLAIN` that neither plan is a sequential scan before committing.

Regenerate: `task backend:sqlc` (check the Taskfile for the exact lane name; do not hand-write `queries.sql.go`).

- [ ] **Step 4: Implement the ports in `app/realtimeports.go`**

Mirror the existing `app/runsports.go` structure. `SnapshotWatermark` uses `pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}`.

`ScopeFor` maps the identity service's grants onto `realtime.Scope`, with `All: true` for admins — reuse exactly the branch `handleListApprovals` uses (`api.go:376-397`) so "what a stream shows" and "what the approvals list shows" cannot diverge.

- [ ] **Step 5: Run the test, then prove the isolation level matters**

Run the integration test — expected PASS.

Then **mutate**: change `RepeatableRead` to `ReadCommitted` and re-run. The test must go red. If it stays green, the test is not actually holding a concurrent commit inside the transaction window and must be fixed before this task closes. Record the mutant output in the report.

- [ ] **Step 6: Gate and commit**

```bash
git add backend/internal/app/realtimeports.go backend/internal/app/realtimeports_integration_test.go backend/internal/runs/store/queries.sql backend/internal/runs/store/queries.sql.go
git commit -m "feat(backend): realtime event source with a repeatable-read watermark"
```

---

### Task 8: The splice — subscribe, snapshot, replay, live

The ordering in this task is the whole design. Get it wrong and the stream is silently lossy under exactly the conditions nobody tests by hand.

**Files:**
- Create: `backend/internal/realtime/stream.go`
- Create: `backend/internal/realtime/stream_test.go`

**Interfaces:**
- Consumes: `Hub`, `Subscription` (Task 5), `EventSource` (Tasks 2/7), `Writer` (Task 6).
- Produces: `realtime.Serve(ctx, w http.ResponseWriter, req StreamRequest) error` where

```go
type StreamRequest struct {
	Filter      Filter
	UserID      uuid.UUID
	LastEventID int64 // 0 when absent
	HasLastID   bool
}
```

Tasks 9 and 10 (the two HTTP surfaces) both call it.

**The required order — do not reorder:**

1. **Subscribe to the hub first.** From this instant, live events are being queued.
2. **Then** `SnapshotWatermark` in its repeatable-read transaction.
3. Write the snapshot frame, then `Replay(after: watermark)` in `ReplayBatch` pages, writing each.
4. **Then** start draining the subscription channel, discarding any event whose id `<=` the highest id already written.

Subscribing first means an event committing during steps 2-3 is sitting in the queue rather than lost; the dedup in step 4 is what makes the overlap harmless. Subscribing *after* the snapshot — the intuitive order — drops exactly those events.

`Last-Event-ID` resume is the same path with the watermark replaced by the client's id, **after** checking `Oldest()`: if `LastEventID < oldest`, the tail is gone to retention and the server must fall back to a full re-snapshot rather than pretend.

- [ ] **Step 1: Write the failing tests**

```go
// 1. TestServe_SubscribesBeforeSnapshotting — an EventSource fake whose
//    SnapshotWatermark records whether a subscription already existed.
//    Assert it did. This is the ordering guard; write it first.
// 2. TestServe_DeduplicatesTheOverlap — the fake commits an event during
//    the snapshot window AND the hub delivers the same event live. The
//    client sees it EXACTLY once.
// 3. TestServe_ReplaysPastTheWatermark — events between watermark and
//    live arrive in ascending id order with no gap.
// 4. TestServe_ResumesFromLastEventID — with HasLastID, no snapshot is
//    written and replay starts at LastEventID+1.
// 5. TestServe_ResnapshotsWhenLastIDTooOld — Oldest() > LastEventID
//    yields a fresh snapshot instead of a silent partial stream.
// 6. TestServe_StopsOnClientDisconnect — cancelling ctx returns promptly
//    and unsubscribes (assert the hub's registry is empty).
```

- [ ] **Step 2: Run and watch fail.** `PKG=./internal/realtime/ task backend:test -- -run TestServe` → FAIL.

- [ ] **Step 3: Implement `Serve`.** Keep the ordering comments in the code, not only in this plan — the next reader's instinct will be to "clean up" by snapshotting first.

- [ ] **Step 4: Run the tests with the race detector.**

Run: `cd backend && PKG=./internal/realtime/ task backend:test -- -run TestServe -race -count=20`
Expected: PASS.

- [ ] **Step 5: Mutate the ordering**

Move the `Subscribe` call after `SnapshotWatermark`. Test 1 **and** test 2 must go red. If only test 1 reddens, test 2 is not actually exercising the race and must be strengthened before this task closes.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/realtime/stream.go backend/internal/realtime/stream_test.go
git commit -m "feat(backend): snapshot-then-deltas splice with watermark dedup"
```

---

### Task 9: Grant re-check closes revoked streams

**Files:**
- Modify: `backend/internal/realtime/stream.go`
- Modify: `backend/internal/realtime/stream_test.go`

**Interfaces:**
- Consumes: `GrantResolver` (Task 2), `Serve` (Task 8).
- Produces: `CloseForbidden` close reason; `StreamRequest` gains nothing — the resolver is a `Serve` dependency.

**Behaviour:** before each delivery batch, and on a `GrantRecheck` ticker while idle, re-resolve the principal's scope. If the new scope no longer allows an event's project, drop the event; if the subscription's own scope has become empty (or, for a per-run stream, no longer covers that run's project), close the stream with a `403`-shaped terminal frame and stop. The client's reconnect then fails authorization at the handler, which is the correct end state.

- [ ] **Step 1: Write the failing tests**

```go
// 1. TestServe_ClosesWhenGrantRevoked — resolver returns a scope, then
//    an empty one. Assert the stream closes within one GrantRecheck
//    interval and the close reason is CloseForbidden.
// 2. TestServe_DropsEventsOutsideRefreshedScope — scope narrows from two
//    projects to one; events from the dropped project stop arriving
//    while the kept project's continue.
// 3. TestServe_ResolverErrorDoesNotCloseTheStream — a transient resolver
//    error keeps the last known scope and retries. Closing on a blip
//    would make every DB hiccup look like a permissions change.
```

- [ ] **Step 2: Run and watch fail.** Expected FAIL.
- [ ] **Step 3: Implement.** Use a `time.Ticker` at `cfg.GrantRecheck`; re-resolve lazily (only when the ticker has fired since the last check) so a busy stream does not hit the DB per event.
- [ ] **Step 4: Run the tests.** Expected PASS.
- [ ] **Step 5: Commit**

```bash
git add backend/internal/realtime/stream.go backend/internal/realtime/stream_test.go
git commit -m "feat(backend): close streams when grants are revoked"
```

---

### Task 10: The two HTTP surfaces

**Files:**
- Create: `backend/internal/realtime/api/api.go`, `backend/internal/realtime/api/dto.go`
- Modify: `backend/internal/runs/api/api.go` (the `listRunEvents` operation + `handleListRunEvents`)
- Modify: `backend/internal/contracts/contracts.go` (register the realtime table)
- Test: `backend/internal/realtime/api/api_test.go`, `backend/internal/runs/api/api_integration_test.go`

**Interfaces:**
- Consumes: `Serve` (Tasks 8-9).
- Produces: route `GET /api/v1/stream` (`streamAttention`); the existing `GET /api/v1/runs/{id}/events` gains a `text/event-stream` response. `runs/api` gains a `StreamServer` port field:

```go
// StreamServer is runs/api's outbound seam to the realtime context.
// runs must not import realtime (bounded contexts are peers), so app
// wires realtime.Serve in behind this interface.
type StreamServer interface {
	ServeRunStream(ctx context.Context, w http.ResponseWriter, runID, userID uuid.UUID, lastEventID int64, hasLastID bool) error
}
```

**Content negotiation (the ruling from pre-flight finding 1):** `handleListRunEvents` inspects `Accept`. If it contains `text/event-stream`, delegate to `ServeRunStream`; otherwise run the existing poll path **unchanged**. Parse `Accept` properly — a browser's `EventSource` sends exactly `text/event-stream`, but a hand-rolled client may send a q-list. Do not use `strings.Contains` on the raw header for the negative case; a header of `application/json, text/event-stream;q=0` must take the JSON branch.

**Declaring the streams in the contract:**

```go
{
	Pattern: "GET /api/v1/stream", ID: "streamAttention",
	Summary: "Server-sent stream of attention events (gates, publications, run lifecycle) " +
		"on the projects the caller holds any grant on. Snapshot-then-deltas; " +
		"resume with Last-Event-ID.",
	Response: StreamFrame{}, Status: http.StatusOK,
	ResponseContentType: "text/event-stream",
	Handler: a.authenticated(a.handleStreamAttention),
},
```

`StreamFrame` is the DTO documenting one frame's `data:` payload — `id`, `run_id`, `kind`, `payload`, `occurred_at`. It is deliberately **not** `RunEventResponse`: that type is the poll's contract and widening it would break pre-flight finding 1.

The `{id}`-bearing run stream needs no new `requestOverrides` entry — `listRunEvents` already has `runEventsParams`. Add `LastEventID` to it as a **header** parameter so the resume mechanism is visible in the contract.

- [ ] **Step 1: Write the failing tests**

```go
// realtime/api:
// 1. TestStreamAttention_RequiresAuthentication — no cookie → 401
//    problem+json, and NOT a half-open stream.
// 2. TestStreamAttention_SetsStreamHeaders — 200 with
//    Content-Type: text/event-stream and no Content-Length.
//
// runs/api (integration, real handler):
// 3. TestListRunEvents_AcceptJSONStillPolls — the existing poll response
//    is byte-identical to before this plan. Guard against regression.
// 4. TestListRunEvents_AcceptEventStreamStreams — Accept:
//    text/event-stream yields the SSE content type.
// 5. TestListRunEvents_QZeroTakesJSON — "application/json,
//    text/event-stream;q=0" takes the poll branch.
```

- [ ] **Step 2: Run and watch fail.** Expected FAIL.
- [ ] **Step 3: Implement both handlers and the port.**
- [ ] **Step 4: Register the realtime table** in `contracts.operationTables()` with nil dependencies, matching the existing four entries.
- [ ] **Step 5: Regenerate and inspect the contract**

Run: `task contracts:gen && git diff backend/api/openapi.json web/src/api/schema.d.ts`

Read the diff. Expected: one new path (`/api/v1/stream`) with a `text/event-stream` response, and a second content type on the existing `/events` operation. **If `listRunEvents`'s `application/json` response disappeared, the negotiation is declared wrong** — both must be present.

Run: `task contracts:check` → exit 0.

- [ ] **Step 6: Gate and commit**

```bash
git add backend/internal/realtime/api backend/internal/runs/api backend/internal/contracts/contracts.go backend/api/openapi.json web/src/api/schema.d.ts
git commit -m "feat(backend): per-run and attention SSE endpoints"
```

---

### Task 11: Wire it into the process

**Files:**
- Modify: `backend/internal/app/serve.go`
- Modify: `backend/internal/app/realtimeports.go` (the pg `Waker`)
- Test: `backend/internal/app/serve_integration_test.go`

**Interfaces:**
- Consumes: everything above.
- Produces: a running tailer + hub for the process lifetime.

**The pg Waker:** a dedicated `*pgx.Conn` — **not** a pooled one — issuing `LISTEN caesar_run_events` and blocking in `WaitForNotification`. A pooled connection can be handed to another caller, which silently unsubscribes the listener. On connection loss, reconnect with backoff and re-issue `LISTEN`; until it succeeds the poll fallback carries the stream, which is exactly why the fallback is not optional.

**Shutdown order** (this is the part that gets it wrong): cancel the tailer's context and close the hub **before** `srv.Shutdown`. Shutting the HTTP server first makes it wait for every open SSE connection to drain, and an SSE connection does not drain on its own — the process would hang until `ShutdownTimeout` on every deploy. Closing the hub first terminates the handlers, which lets `Shutdown` complete promptly.

- [ ] **Step 1: Write the failing test**

```go
//go:build integration

// TestServe_StreamsAnEventEndToEnd — boot the app against a testdb,
// open GET /api/v1/runs/{id}/events with Accept: text/event-stream,
// append an event through the ordinary domain path, and assert the frame
// arrives on the wire within 5s. This is the plan's acceptance evidence:
// it exercises NOTIFY → tailer → hub → splice → codec → socket.
//
// TestServe_ShutdownDoesNotWaitForOpenStreams — with a stream open,
// shutdown completes in well under ShutdownTimeout.
```

- [ ] **Step 2: Run and watch fail.** Expected FAIL.
- [ ] **Step 3: Wire it up**, following the existing `go func() { defer close(done); _ = x.Run(ctx) }()` shape at `serve.go:181-193`.
- [ ] **Step 4: Run the tests.** Expected PASS.
- [ ] **Step 5: Mutate the shutdown order.** Move the hub close after `srv.Shutdown`; the second test must go red (it will hang to the timeout). Revert.
- [ ] **Step 6: Gate and commit**

```bash
git add backend/internal/app
git commit -m "feat(backend): run the tailer and hub for the process lifetime"
```

---

### Task 12: Gaplessness under load, docs, and the roadmap row

**Files:**
- Create: `backend/internal/app/realtime_e2e_integration_test.go`
- Modify: `backend/README.md`
- Modify: `docs/superpowers/plans/2026-08-02-caesar-v1-roadmap.md`

- [ ] **Step 1: Write the gaplessness suite**

```go
// 1. TestStream_NoGapsUnderConcurrentWriters — N=8 writers appending
//    events to M=4 runs while a subscriber streams one run. Assert the
//    received ids for that run are exactly the committed ids, in
//    ascending order, with no duplicates and no holes. This is the test
//    the whole plan exists to pass.
// 2. TestStream_SlowClientIsEvictedAndResumes — a client that stops
//    reading is disconnected; reconnecting with Last-Event-ID yields
//    every missed event exactly once.
// 3. TestStream_ScopedToGrants — a member of project A streaming the
//    attention endpoint never receives a project B event, including
//    during replay.
// 4. TestStream_HeartbeatArrives — an idle stream emits a comment frame
//    within ~heartbeat_interval and the connection stays open.
```

Test 1 must assert **set equality against what was actually committed**, read back from `run_events` — not against what the test intended to write. A test that compares the stream to its own intentions cannot detect a writer that failed.

- [ ] **Step 2: Run the suite.** Expected PASS. Run it `-count=10 -race`; a flake here is a real bug, never a flaky test — investigate before proceeding.

- [ ] **Step 3: Document for operators**

Add a `## Realtime streams` section to `backend/README.md`: the two endpoints, the `Accept` negotiation, `Last-Event-ID` resume, what a client sees when it is evicted (and that reconnecting is correct behaviour, not an error), the config knobs from Task 2, and the single named limitation — **process-local fan-out**, so a second caesar process serves its own subscribers only. State that plainly; a future operator scaling out horizontally needs to meet it in the README, not in production.

- [ ] **Step 4: Flip the roadmap row**

In `docs/superpowers/plans/2026-08-02-caesar-v1-roadmap.md`, row 10 → `**done** — 2026-08-10-caesar-10-realtime-sse.md`.

Unlike rows 07 and 08, **row 10 has no operator-smoke dependency**: every assertion here runs against a real Postgres and a real HTTP socket in CI. There is no live third party. Do not invent a manual checklist for it.

- [ ] **Step 5: Final gate and commit**

```bash
scripts/gate <abs-worktree-path> <sha> <branch>   # expect GATE PASS, 11 lanes
git add backend/internal/app backend/README.md docs/superpowers/plans/2026-08-02-caesar-v1-roadmap.md
git commit -m "test(backend): gapless streaming under concurrent writers"
```

---

## Self-review

**Spec coverage** — every clause of §4's SSE paragraph maps to a task:

| Spec clause | Task |
|---|---|
| Two stream kinds, per-run + global attention | 10 |
| Snapshot carries the event-id high-watermark | 7 |
| Subscribe first, replay past watermark, splice, dedup | 8 |
| Snapshot + watermark in one repeatable-read transaction | 7 |
| `Last-Event-ID` resume, re-snapshot when too old | 8 |
| Streams authorization-scoped; revocation closes them | 2, 7, 9 |
| Single tailer, `LISTEN/NOTIFY` + slow-poll fallback | 3, 4, 11 |
| In-memory fan-out, no per-connection DB polling | 5 |
| Bounded queues; slow client disconnected, never backpressure | 5 |
| Immediate flush + ~20s heartbeat | 2, 6 |
| Process-local fan-out stated as an assumption | 5, 12 |

**Known gaps carried deliberately:**

- **The web client is not in this plan.** Spec §5's "Realtime client" (SSE abstraction, backoff, heartbeat watchdog) belongs to Plan 11/12, which own `web/`. Plan 10 stops at the wire.
- **`Head` was added to `EventSource` in Task 4**, after Task 2 defined the port. Task 2's file is edited again in Task 4 — that is intentional and called out in Task 4's commit, not an oversight.
- **The `outbox` still has no consumer.** Unchanged by this plan, by design.

**Open risk the executor should watch:** `Task 10`'s content negotiation is the one place a mistake is invisible in tests that only exercise the new path. Test 3 (the poll response is byte-identical to before) is the guard; do not let it be weakened to "returns 200".
