# caesar — web critical path (Plan 12a design)

Roadmap row 12 ("Web features") is split. **12a is the month-one flow on a phone**:
Home, Tasks, Run detail, Approvals, and the backend those four screens need. **12b** takes
the configuration surfaces — Workflows outline editor, Repos, Admin — and is not designed here.

The split is by critical path, not by convenience. 12a's four screens are the loop a person
actually runs: create a task, watch it execute, decide its gate, see the PR. The 12b three are
things a single operator can seed through the API once and rarely revisit. 12a also carries
**row 08's live-forge smoke**, whose checklist is gate-shaped and therefore *is* this UI.

Authority for what these screens look like is `docs/brainstorm/2026-08-01-decisions.md` §7 and
the eight locked mockups in `.superpowers/brainstorm/12432-1785604290/content/`. §10 of this
document lists every place 12a departs from them and why. Nothing is dropped silently.

## 1. Scope — what "done" means

Two waves, and a human checkpoint between 12a and everything after it.

| Wave | Contents |
|---|---|
| **0 — substrate** | `GET /api/v1/runs`; artifact payload retrieval; `plan` / `review_verdict` payload schema through `openapigen`. No UI. |
| **1 — the flow** | Shell corrected to five phone tabs; Home; Tasks; Run detail; Approvals; Playwright mobile-critical paths; the auth-guard test rehomed. |

Done means: from a phone, against a real backend holding real data, a person can create a task,
watch its run advance without touching the screen, open a waiting gate, read the artifact it
examines, and approve or request changes with a note — and row 08's forge smoke checklist can be
walked end to end on that UI.

**Wave 1 ends at an operator assessment**, and the assessment is a deliverable, not a formality:
the build must be deployed and reachable from the operator's own phone against their own data.
A checkpoint conducted against mocked data cannot produce the taste feedback it exists for.

**Deliberately out of 12a**, deferred until after that assessment because the assessment should
inform their design: run cancel/retry (§10.3) and live agent log streaming (§10.4). Both are
wanted; neither is specified here.

## 2. The backend substrate

Roughly 40% of 12a is Go, which is invisible from the roadmap row. Three items, all in wave 0,
all blocking more than one screen — which is why they are a phase rather than a slice.

### 2.1 `GET /api/v1/runs`

`/runs` is a committed nav destination with compiler-checked route strings, and nothing lists
runs today: runs are reachable only as `/tasks/{id}/runs`. A client-side fan-out over every task
is an N+1 waterfall on the device least able to afford it.

Grant-scoped exactly as `GET /approvals` is, so a user sees runs for projects shared with them
and no others. Cursor-paginated from the outset — this is a new endpoint and there is no reason
to build the cliff that `GET /tasks` and `GET /approvals` already have. Filterable by status and
by task.

The existing unpaginated `GET /tasks` and `GET /approvals` are **left alone**. At month-one
volume they are correct, and changing them is not this plan's job.

### 2.2 Artifact payload retrieval

**No endpoint returns artifact content today.** `ApiArtifactResponse` is metadata only — id,
type, node_id, attempt, generation, parent_ids, created_at. This is the single hardest blocker in
12a: the decision surface exists to show the artifact a gate examines, and that artifact is
currently unreachable from the client.

Content is cheap to serve — `artifacts.payload` is a `jsonb` column, not a blob on disk. The
addition is a grant-scoped read returning the payload for one artifact of one run.

Two properties the implementation must preserve:

- **`patch` payloads carry `diff` as base64 by contract**, and `collect.go` documents why: a
  patch is arbitrary bytes, a file in a legacy encoding diffs as non-UTF-8, and JSON string
  encoding would replace those bytes with U+FFFD *silently* — the mangled patch still applies.
  The client decodes base64 once for display and never round-trips it.
- Artifact reads are grant-scoped on the **run**, not on the artifact id, so an artifact id
  cannot be used to reach across a project boundary.

### 2.3 A payload schema for `plan` and `review_verdict`

Today a declared artifact's payload is `map[string]any`. The runner checks the type name and
scans for secrets; nothing constrains the keys. The approval surface cannot render evidence it
has no shape for, so the shape is defined here and emitted through `openapigen` like every other
contract in this repo.

Minimum required sets, deliberately small:

- **`plan`** — `summary` (string), `steps` (array).
- **`review_verdict`** — `verdict` (string), `findings` (array), `tests` (test outcome; see §10.1).

**Unknown keys are allowed through.** This is the opposite of the envelope's
`DisallowUnknownFields` and the difference is intentional: the envelope is written by our runner,
but payloads are written by an LLM harness. A harness that adds a useful key must not fail a run.

**Validation failure is a permanent attempt failure** (`CodeInvalidOutput`), consistent with how
`collect.go` already treats an undeclarable artifact type. This is the risk in wave 0 worth
stating plainly: a schema stricter than the model is reliably able to produce converts model
variance into failed runs. The required sets are minimal for exactly that reason, and the harness
prompts are ours to change.

## 3. Screens

Faithful to §7 except where §10 names a departure.

### 3.1 Shell — locked A

Phone: five bottom tabs — **Home · Runs · Approvals (badged) · Tasks · More**. Desktop: sidebar
plus top bar. Plan 11 shipped seven equal tabs; §10.5 covers the correction.

The approvals badge renders only when the count is non-zero, which `nav.tsx` already does for the
stated reason: a badge reading "0" is a call to action for nothing.

### 3.2 Home — locked A, with one named deviation

State-first. In order: a status line, "Needs you" gate cards, "Running" cards with live phase
dots, recent activity.

Gate cards **navigate to the run's expanded gate phase rather than carrying inline
approve/reject**. This is the one deliberate design deviation in 12a and §10.2 argues it.

The assistant-generated résumé and the persistent ask-bar are row 13 (§10.6). Until then the
status line is computed client-side from counts already on screen — and it is a plain count line,
not a fabricated summary. A stub never fakes success.

### 3.3 Tasks — locked B

Attention-grouped live list: **Needs you · In flight · Up next · Recently done**. Statuses move
automatically as runs progress; the groups are projections of task and run state, not a stored
field. Board and timeline views are later projections and not in 12a.

New task is a FAB opening a short form (project, repo, workflow, title, instructions) built on
Plan 11's typed form abstraction. Assistant-drafted tasks are row 13 (§10.6).

### 3.4 Run detail — locked A

The vertical phase timeline is the spine, and the run reads top to bottom as its phase graph.
**Each phase expands in place**: artifacts and verdicts when done, and at a gate, the examined
artifact followed by the decision controls.

A gate phase expands into **the approval surface of §3.5** — the same component, not a second
implementation of it. Locked §7 decides twice that a gate is decidable where its evidence is
shown: once in run detail A (inline at the gate) and once in approval A (the summary-first
screen). Those are one surface with two hosts, and building it once is what keeps them from
drifting apart. Home links here; Approvals hosts the same component full-screen.

**The phase spine is typed; the event log is not.** `ApiRunResponse.phases[]` carries `node_id`,
`status`, `attempt`/`max_attempts`, `generation`, `outcome` and `decision` — a real contract. But
`ApiRunEventResponse` is `{id, kind, occurred_at, payload}` with **no `node_id` column**; the node
lives inside the untyped payload. Events therefore render as a flat chronological log, not grouped
under their phase. Grouping would mean depending on a field `contracts:check` cannot police.

The sticky header shows workflow, repo and attempt. Cancel and retry are §10.3.

### 3.5 Approvals — locked A

The cross-run gate inbox, plus the **approval surface** itself — one component, hosted here
full-screen and inside a run-detail gate phase (§3.4). It is specified in this section and built
once.

Order: agent summary (from the `plan` / `review_verdict` payload of §2.3), a hard-facts row, then
a file-by-file accordion expanding into real diffs, then sticky actions in the thumb zone —
**approve · request changes · reject**, all three.

The hard-facts row ships as **files · ±lines · tests · duration**. Files and line counts are
parsed from the decoded patch; tests comes from the `review_verdict` schema; duration is
`execution_used_ms`. **Cost is absent because it does not exist** (§10.1).

`request_changes` requires a note. This is not a UI nicety: `gates.go` states that the note is fed
to the revision target as a context file and bounds it at 8KB — "sized like an instruction, not
like a comment."

Plan 11's `ApprovalList` already exists and its two empty states must stay distinct: "Nothing is
waiting on you" is a statement about the inbox, "No projects are shared with you yet" is a
statement about grants. Every list added in 12a inherits that rule.

## 4. Server state and realtime

Unchanged from Plan 11: TanStack Query over the generated client, hand-written fetches banned,
SSE deltas into the cache through the existing `cache-bridge`, `Last-Event-ID` resume, heartbeat
watchdog surfacing staleness honestly. 12a points that machinery at new cache keys; it does not
extend it.

Run detail mounts a per-run view of the stream. Frames carry `run_id`, so the bridge routes them
without new plumbing. The live phase dots on Home and the advancing timeline on run detail are the
same mechanism the approvals badge already uses — which Plan 11 proved end to end, through a gate
opened on the production write path.

URL-as-state for filters and tabs, per the constitution.

## 5. Failure — four cases, three of them routine

**`403` self-approval is prevented, not caught.** `policy_snapshot.allow_self_approval` is on the
run, so the client knows before render whether approving is permitted. If it is not and the viewer
is the requester, the approve control is not offered and the screen says why. On a single-operator
system the operator is always the requester, so this is the normal path rather than an edge case.

**`409` already decided** — refetch and show the decision that won. `ApiGateDecisionSummary`
carries `acted_by_email`, `decision`, `note` and `acted_at`, so the screen can say what happened
rather than just failing.

**`409` wall deadline passed** — a run past `wall_deadline` can no longer be decided. Because
`wall_deadline` is on the run, this is displayable *before* expiry, not only after.

**Field errors** reuse Plan 11's `problem-to-fields.ts` and `api-error-panel.tsx` unchanged.

## 6. The auth-guard test must be rehomed, as its own task

`_app.test.tsx` navigates to `/tasks` **because that route issues no request** — it proves the
401 redirect fires in the case no server rejection can produce. `tasks.tsx` carries a comment
saying so and forbidding a loader while Plan 12 is unbuilt.

Wave 1 gives Tasks a loader and destroys that property. After 12a, plausibly no loader-free route
remains.

Inventing a route to keep the test green would be a seam with no production caller — a mistake
this project has already paid for once, where a test-only affordance made all five of its own
tests vacuous. The correct fix is to assert `_app`'s `beforeLoad` guard directly rather than route
it through a stub. This is its own task with its own mutation proof, not a line inside the Tasks
task.

## 7. Testing

| Level | What it covers |
|---|---|
| Vitest + Testing Library + MSW | Each screen against the generated client: attention grouping, the two distinct empty states per list, hard-facts computation from a decoded patch, the three decision paths, and the `allow_self_approval` suppression |
| Vitest, pure | Patch decode and diff parsing — base64 → files → ±line counts, including a non-UTF-8 payload, which is the case §2.2's contract exists for |
| Go | The three wave-0 items: grant scoping on the runs list and on artifact reads, cursor pagination, and payload schema acceptance — including a payload with unknown extra keys, which must pass, and one missing a required key, which must fail the attempt |
| Playwright, mobile viewport, real binary | The three critical paths below |

Playwright extends Plan 11's existing rig — Postgres up, migrations applied, admin bootstrapped,
Vite build embedded into a `-tags embedui` binary, specs run against it, `runs.Executor` faked.
Plan 11 built that lane precisely so 12a could add specs to something that already works.

The three mobile-critical paths:

1. A gate opened through the production write path appears on Home, opens to its examined
   artifact, and approving clears the badge **with no user action** elsewhere.
2. Create a task; its run appears and the timeline advances live.
3. `request_changes` with a note opens a new generation, visible on the timeline.

**Paths 1 and 3 are row 08's forge smoke.** Run once against a real forge with a real token, that
is row 08's acceptance evidence and closes the row.

House rule applies to every new guard: mutation is the acceptance criterion — red-then-revert, on
new tests as well as on production code. A test that cannot be made to fail has not been shown to
test anything.

## 8. Dependencies and order

Wave 0 lands complete, with `task contracts:gen` run and `contracts:check` green, before wave 1
begins. Every screen then builds against a generated, typed client rather than a moving target —
the discipline that made Plan 11's contract layer worth having.

Within wave 1 the shell correction lands first (it touches routing that everything else builds
on), and the auth-guard rehoming lands with or before Tasks, since Tasks is what breaks it.

## 9. What 12a does not touch

Workflows outline editor, Repos, Admin — all 12b. Row 09's kill-switch panel stays deferred, so
Admin ships without it whenever 12b lands. Row 10a's realtime scope signalling is unchanged and
unblocked by this plan. The assistant is row 13.

## 10. Deviations from locked §7, named

The user's instruction was that the mockups are authoritative and departures must be explicit.
Nine, in four classes.

### 10.1 No data source exists — cost, and `tests`

The locked approval mockup's hard-facts row reads `4 files · +120 −8 · tests ✓ · $0.84`.

**Cost cannot be built.** There is no cost, usage, token or price field anywhere in the API *or*
the database schema. Only `execution_used_ms` exists, which is time, not money. The fact is
dropped and duration takes its place. Restoring it requires usage accounting that does not exist
and is not in this plan.

**`tests` is buildable only because §2.3 is already opening the schema**, so a test-outcome field
joins `review_verdict`'s required set. It populates only when a harness actually reports test
results; the row renders the fact as unknown rather than inventing a tick.

### 10.2 Deliberate correctness deviation — Home's inline approve

Locked Home A puts approve/reject directly on gate cards. 12a makes those cards navigate to the
run's expanded gate phase instead.

The reason is in `gates.go`: the server resolves the examined artifact from the workflow
definition and stamps `examined_artifact_id` onto the decision **whether or not a human looked at
it**. An inline approve on a list card records an examination that did not happen — in a system
whose entire purpose is holding publication until a person decides.

This deviation is narrow. Locked **run detail** A already expands a phase to show artifacts and
verdicts *before* its inline gate controls, so the inline decision survives exactly where the
evidence is present. Only the evidence-free surface loses its buttons, and it loses them to a
single tap.

### 10.3 Deferred pending the operator assessment — cancel / retry

Locked run detail A has a sticky header with cancel and retry. **No endpoint exists**; `/runs/{id}`
is GET-only and nothing in the API mutates a run's lifecycle. Building it is real orchestrator
work, because cancellation has to reach a running container rather than flip a row.

Wanted, and specified after the wave 1 checkpoint, on the argument that using the UI will inform
what the control should do.

### 10.4 Deferred, and needs its own design pass — live agent output

Locked run detail A streams live agent output into the active phase. **No log-serving route
exists.** Agent logs live on the runner's disk under `logs/`, reachable only as a `log_ref`
artifact pointing at a path, and the run event vocabulary is lifecycle-only — `phase.started`,
`phase.succeeded`, `phase.egress_denied`, `gate.waiting`. No event carries stdout.

A live *phase-status* spine is buildable and 12a builds it. The terminal box is not.

This one is separated from §10.3 for a reason beyond cost: logs are quarantined and redacted
*specifically* because secrets reach them — there is already a `Sensitive.scan` and a
`CodeSecretInArtifact` policy violation that refuses to persist an artifact containing one.
Serving logs over HTTP means choosing which copy is served and being right about it. That is a
safety decision, and it deserves its own design pass rather than a task in someone else's plan.

### 10.5 Already-shipped deviation, corrected here — the shell

Locked shell A is five phone tabs: Home · Runs · Approvals · Tasks · **More**. Plan 11 shipped
**seven equal tabs**. The cause is visible in `nav.tsx`'s comment, which cites "the V1 page map
(decisions §7)" — the page map is indeed seven *pages*, but the shell decision was five *tabs*
plus More. Page map and tab bar were conflated.

12a corrects it. Nothing later would revisit the shell, so left alone the phone navigation stays
off-design permanently.

### 10.6 Deferred to row 13 — everything assistant-shaped

Four locked elements depend on the assistant, which is row 13 and unbuilt: Home's
assistant-generated situation résumé, Home's persistent ask-bar docked above the tabs, the
per-file "explain" spark on the approval accordion, and "assistant drafts it" as a task-creation
path. Each has a known unblock and none is redesigned away — the surfaces simply ship without
them, and the short-form task creation that §3.3 specifies is the locked design's own stated
alternative, not a substitute invented here.

## 11. Residuals

- **The wave 1 checkpoint is a gate, not a milestone.** Waves for cancel/retry and logs are
  specified after it, informed by it.
- **`GET /tasks` and `GET /approvals` remain unpaginated.** Correct at month-one volume, a cliff
  later; named so the next person meets it deliberately.
- **Row 08's forge smoke closes on paths 1 and 3 of §7**, run once against a real forge. Row 12a
  is not itself blocked by that run.
- **Plan 11's one open operator item is unrelated and still open**: proving the `__Host-` cookie
  survives the Vite dev proxy, per the six steps in `web/vite.config.ts`.
