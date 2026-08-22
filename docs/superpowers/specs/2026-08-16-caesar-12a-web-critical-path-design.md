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
| **0 — substrate** | `GET /api/v1/runs` with a real list DTO; artifact payload retrieval; the waiting-gate decision projection (which artifact, which verdict, may-I-decide); `plan` / `review_verdict` payload schema through `openapigen`, with the producer migration and compatibility rule that schema forces. No UI. |
| **1 — the flow** | Shell corrected to five phone tabs; Home; Tasks; Run detail; Approvals; Playwright mobile-critical paths; the auth-guard test rehomed. |

Done means: from a phone, against a real backend holding real data, a person can create a task,
watch its run advance without touching the screen, open a waiting gate, read the artifact it
examines, approve or request changes with a note, **and see the resulting pull request** — and
row 08's forge smoke checklist can be walked end to end on that UI.

**Wave 1 ends at an operator assessment**, and the assessment is a deliverable, not a formality:
the build must be deployed and reachable from the operator's own phone against their own data.
A checkpoint conducted against mocked data cannot produce the taste feedback it exists for.

**Deliberately out of 12a**, deferred until after that assessment because the assessment should
inform their design: run cancel/retry (§10.3) and live agent log streaming (§10.4). Both are
wanted; neither is specified here.

## 2. The backend substrate

Roughly 40% of 12a is Go, which is invisible from the roadmap row. Four items, all in wave 0,
all blocking more than one screen — which is why they are a phase rather than a slice.

### 2.1 `GET /api/v1/runs`

`/runs` is a committed nav destination with compiler-checked route strings, and nothing lists
runs today: runs are reachable only as `/tasks/{id}/runs`. A client-side fan-out over every task
is an N+1 waterfall on the device least able to afford it.

Grant-scoped exactly as `GET /approvals` is, so a user sees runs for projects shared with them
and no others. Cursor-paginated from the outset — this is a new endpoint and there is no reason
to build the cliff that `GET /tasks` and `GET /approvals` already have. Filterable by status and
by task.

**The list entry is its own DTO, and that is the half that makes the endpoint worth building.**
`RunResponse` is reused today as a reduced list entry with `phases`, `artifacts` and
`publications` left empty (`dto.go:39`, and `api.go:349` builds exactly that projection). It
carries no task title, no repo or workflow label, and no phase summary — so a Home or Tasks card
built on it fetches again per row and reintroduces precisely the N+1 this endpoint exists to
remove. 12a defines `RunSummary`: run id, task id and title, project, repo and workflow labels,
status, attempt and generation, requester, created and updated — plus an **ordered
`phase_summary[]`**, each entry carrying node id, node type, status, attempt and generation.

The array is not a convenience. Locked Home and Tasks both show a *row* of phase dots — done,
active, gate, pending — not one dot for the current phase, and a scalar "current phase" cannot
render the run's shape. Node type is in there because a gate dot is drawn differently from a
work dot, and a client should not infer node type from a node id string.

Ordering is `(created_at DESC, id DESC)` with an opaque cursor and a `next_cursor`, so the sort
key and the cursor cannot disagree about where a page ends.

**The cache topology is part of this contract.** `cache-bridge.ts` invalidates authoritative
queries; it does not patch projections in place, and 12a does not change that — a bridge that
reconstructs run state client-side would be a second, unpoliced copy of the orchestrator's
transitions. So a `phase.` frame invalidates the list query for the affected run's screens, and
the list refetch is what redraws the dots. This is why `phase_summary[]` must ride on the list
row: without it, invalidation would fan out into one detail request per visible run, which is the
N+1 this endpoint exists to remove, now firing on every phase transition instead of once.

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

**Those are field names, not a schema.** What ships is a complete, versioned JSON Schema per
artifact type, emitted through `openapigen` like every other contract here: the element shape of
`steps` and of `findings`, the permitted values of `verdict`, the representation of `tests`
*including how it says "not reported"*, and nullability on every field. A required-set sketch
leaves the wire format to be invented per implementer, and the client renderer and the runner
validator would each invent a different one.

**Enabling validation breaks today's producers, so wave 0 carries the migration or it carries
nothing.** `fake.Executor` emits `plan: {content}` and `review_verdict: {verdict, notes}`
(`fake/executor.go:176`) — neither satisfies the sets above, and that executor is what §7's
Playwright lane runs on, so a validator merged without it fails 12a's own acceptance path. The
real harnesses are further off: `docker/harness/*/entrypoint.sh` hands the node's stored
instructions to the CLI and appends nothing, so **nothing today tells a model what shape to
emit** — the schema would be enforced against producers that were never told it exists. Wave 0
therefore includes, alongside the schema itself: a runner-owned output-shape instruction appended
for every harness, updated fake and fixture payloads, and a compatibility rule. Without those
three, a deploy converts into a fleet of permanently failed runs.

**The compatibility rule, stated rather than deferred.** An earlier draft asked the implementation
to invent one, which is the same buck-passing this section complains about. It needs no new
column, because the durable selector already exists: a workflow definition document carries
`schema_version` (`definition.go:20`), a `workflow_versions` row is immutable (`content_hash` is
unique, `schema.sql:182`), and a run pins its definition at intake via the task's
`workflow_version_id` and the run's own `definition_hash`. So:

- **Payload validation is a property of the document schema version.** `schema_version: 1`
  documents are never validated — exactly today's behaviour, preserved. Payload schemas arrive
  with `schema_version: 2`, and `ParseDocument` already branches on the version, which is what
  the constant's own comment says it is for.
- **The prompt and the validator read the same pin.** A node is asked for the shape its own
  pinned document defines and judged against that same one. This is the property that matters:
  the failure mode being avoided is a node instructed in one shape and validated against another.
- **In-flight attempts are safe by construction, so no drain is needed.** An attempt's run pinned
  a v1 definition at intake and a deploy cannot retroactively change it. A restarted collector
  reads the pin, not the binary's opinion.
- **Existing workflows opt in by registering a new version**, which is already how any graph
  change happens here. Nothing is migrated in place and nothing is rewritten.
- **Rollback is deploying the old binary.** v1 runs are unaffected; a v2 document is refused by
  the old `ParseDocument` at registration rather than mis-executed.

**Unknown keys are allowed through.** This is the opposite of the envelope's
`DisallowUnknownFields` and the difference is intentional: the envelope is written by our runner,
but payloads are written by an LLM harness. A harness that adds a useful key must not fail a run.

**Validation failure is a permanent attempt failure** (`CodeInvalidOutput`), consistent with how
`collect.go` already treats an undeclarable artifact type. This is the risk in wave 0 worth
stating plainly: a schema stricter than the model is reliably able to produce converts model
variance into failed runs. The required sets are minimal for exactly that reason, and the harness
prompts are ours to change.

### 2.4 What a waiting gate's decision surface needs

§2.2 makes artifact content fetchable. It does not make it **findable**, and without this item the
approval surface cannot open the evidence for a gate that has not been decided yet.

`ApprovalResponse` carries run, task, project, task title, node, attempt, generation, requester and
waiting-since (`dto.go:126`) — no artifact id and no artifact type. `ArtifactResponse` carries no
gate association in the other direction. The mapping is `plan.ExaminedInput(nodeID)`, which lives
in the server's compiled definition and is resolved inside `DecideGate` immediately before the
stamp (`gates.go:193`). It is not on the wire anywhere.

Guessing is not available. "The newest artifact on the run" is wrong across attempts and
generations, and wrong again for a plan gate whose examined origin is not the last node to have
produced anything. A client that guesses shows one artifact and stamps another.

**The examined artifact is not the only thing the surface needs, and it is not the only thing
that is unfindable.** §3.5 orders the approval surface *agent summary first*, and that summary
comes from a `review_verdict` — a different artifact from the patch the gate examines.
`ExaminedInput` deliberately selects the plan-or-patch and nothing else (`compile.go:122`), so a
client that has the examined id still cannot find the verdict, and picking "the newest
`review_verdict` on the run" is the same guess this section just rejected, made worse by
revisions: a gate on generation 3 must not summarize generation 1's review.

Worse, **the verdict may not exist at all.** `validate.go:207` requires a gate to examine exactly
one plan or patch and permits **zero** review verdicts — `verdicts > 1` is the only violation. A
patch gate with no review node upstream is a legal workflow, and `collect.go:259` makes declaring
artifacts optional besides. So "no verdict" is a production state, not an error, and the surface
renders it honestly: the hard-facts row it can still compute from the patch (files, ±lines,
duration), `tests` as not reported, and no summary block rather than an empty one pretending to
be a summary. A stub never fakes success.

So the waiting-gate projection gains **`examined_artifact_id` and `examined_artifact_type`**,
resolved by the same `ExaminedInput` call the decision path uses — one function, so the artifact
the screen renders cannot drift from the artifact the decision records. The type is needed as well
as the id, because the surface renders a `plan` and a `patch` differently and must know which it
is before it fetches. It lands on `ApprovalResponse` **and** on the gate phase of `RunResponse`,
since §3.4 and §3.5 are one component with two hosts and both hosts need it. Home's gate cards
need it too (§3.2), for the artifact *type* if nothing else — a card that offers a decision should
say whether the thing being decided is a plan or a patch.

…plus **`summary_artifact_id`, nullable**: the `review_verdict` the gate actually consumes,
resolved from the same compiled gate inputs, or null where the graph has none. Nullable and not
omitted, so "this gate has no verdict" is a value the client handles rather than a key it fails
to find. The graph guarantees at most one (`verdicts > 1` is a validation violation), so this is
a single id and never a list.

**And `can_decide`, with a refusal reason.** Home's gate cards carry live controls (§3.2), and
they cannot tell whether to offer them: `ApprovalResponse` has no policy snapshot (`dto.go:126`),
so `allow_self_approval` is reachable only by fetching each run — the N+1 this section exists to
prevent, on the one screen that must not have it. Nor is self-approval the only refusal.
`decideGate` requires `GrantApprove` (`api.go:403`) while `GET /approvals` deliberately lists
gates for **any** grant on the project, and an integration test pins that asymmetry
(`api_integration_test.go:1256`): a view-only principal is *supposed* to see a gate it cannot
decide. Two independent 403s, neither visible from the list.

Both belong on the server, which knows the grant and the snapshot and can answer once per row.
The projection therefore carries `can_decide` and, when false, a reason the screen can state —
`not_approver` or `self_approval_forbidden`. §5's rule then has a data source on every host.

Wave 0 also corrects `schema.sql:347`, whose comment calls `examined_artifact_id` the artifact the
human examined. It is not, and §10.2 explains what it actually is.

## 3. Screens

Faithful to §7 except where §10 names a departure.

### 3.1 Shell — locked A

Phone: five bottom tabs — **Home · Runs · Approvals (badged) · Tasks · More**. Desktop: sidebar
plus top bar. Plan 11 shipped seven equal tabs; §10.5 covers the correction.

The approvals badge renders only when the count is non-zero, which `nav.tsx` already does for the
stated reason: a badge reading "0" is a call to action for nothing.

### 3.2 Home — locked A

State-first. In order: a status line, "Needs you" gate cards, "Running" cards with live phase
dots, recent activity.

Gate cards carry **inline approve · request changes · reject**, as locked. Tapping the card body
opens the run's expanded gate phase, where the same three controls sit under the full evidence —
so the detail page is a choice, not a toll. §10.2 records the deviation this document proposed
here and withdrew.

Home's inline controls inherit §5 whole: where `allow_self_approval` is false and the viewer is
the requester, a gate card offers **none** of the three, with the reason on the card. A card is
the surface most likely to get this wrong, because suppressing only approve looks obviously right
and 403s on the other two.

The assistant-generated résumé and the persistent ask-bar are row 13 (§10.6). Until then the
status line is computed client-side from counts already on screen — and it is a plain count line,
not a fabricated summary. A stub never fakes success.

### 3.3 Tasks — locked B

Attention-grouped live list: **Needs you · In flight · Up next · Recently done**. Statuses move
automatically as runs progress; the groups are projections of run state, not a stored field.
Board and timeline views are later projections and not in 12a — as decisions §7 itself already
says, so that is not a departure.

**The grouping needs its mapping written down, because there is no task lifecycle to lean on.**
`tasks` has no status column (`schema.sql:197`); `TaskResponse.Status` *is* the latest run's
status (`dto.go:190`), one of `pending · running · waiting · succeeded · failed · superseded ·
dead`. Seven values, four groups, and two of them are not obvious:

- **Up next** is `pending` — a run created at intake that the orchestrator has not started. On a
  single-operator system that is a state measured in seconds, so this group is **usually empty**,
  and its empty state is the one on screen most of the time. It gets designed, not defaulted.
- **`superseded`** is a run replaced by a re-run (§10.3). It belongs in *Recently done* or nowhere;
  what it must not do is fall through the switch and vanish, taking its task off the list.

The mockup's caption describes tasks flowing `draft → ready → running → in review → done`. Three
of those five have no representation in the schema. §10.8 names it.

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

**A publish phase expands into its publication**, which is where "see the PR" is delivered — and
the first draft of this document specified nowhere. `PublicationResponse` already carries what the
surface needs (`dto.go:163`): a four-value `status` (`pending · pushed · published · halted`),
`branch` and `target_branch`, `base_sha` / `head_sha`, `pr_number` and `pr_url`, and — the one
that matters most — `halt_reason`. A publication that halted is the case the operator most needs
to see and the easiest to render as a blank space, so `halted` gets a stated appearance rather
than falling out of a switch. `pr_url` is the run's exit to the forge.

The sticky header shows workflow, repo and attempt. Cancel and retry are §10.3.

### 3.5 Approvals — locked A

The cross-run gate inbox, plus the **approval surface** itself — one component, hosted here
full-screen and inside a run-detail gate phase (§3.4). It is specified in this section and built
once.

Order: agent summary (from the `plan` / `review_verdict` payload of §2.3, located by §2.4's
`summary_artifact_id` and **absent when that is null** — a gate needs no verdict to be legal), a
hard-facts row, then
a file-by-file accordion expanding into real diffs, then sticky actions in the thumb zone —
**approve · request changes · reject**, all three.

The hard-facts row ships as **files · ±lines · tests · duration**. Files and line counts are
parsed from the decoded patch; tests comes from the `review_verdict` schema; duration is
`execution_used_ms`. **Cost is absent because it does not exist** (§10.1).

`request_changes` requires a note. This is not a UI nicety: `gates.go` states that the note is fed
to the revision target as a context file and bounds it at 8KB — "sized like an instruction, not
like a comment."

The inbox lists **waiting** gates only. Locked shell A also puts "History of past decisions" under
Approvals; §10.7 names that as a departure and defers it.

Plan 11's `ApprovalList` already exists and its two empty states must stay distinct: "Nothing is
waiting on you" is a statement about the inbox, "No projects are shared with you yet" is a
statement about grants. Every list added in 12a inherits that rule.

## 4. Server state and realtime

The transport and the discipline are Plan 11's, unchanged: TanStack Query over the generated
client, hand-written fetches banned, SSE deltas into the cache, `Last-Event-ID` resume, heartbeat
watchdog surfacing staleness honestly.

**The routing is not, and 12a extends it.** An earlier draft of this document claimed 12a only
points existing machinery at new cache keys. That was wrong, and it understated wave 1. Two facts:

- The global attention stream carries the `gate.`, `publish.` and `run.` prefixes and nothing else
  (`realtime.go:18`). **`phase.` is not among them**, so no phase transition reaches a client on
  that stream today.
- `cache-bridge.ts` invalidates exactly `['approvals']` and reads nothing off a frame but its
  kind. It cannot route by `run_id` because it never looks at one.

So live phase dots on Home and Tasks, and a live timeline on run detail, need both halves built: a
transport that carries phase transitions, and a bridge that routes a frame to the run it belongs
to. What each screen needs differs:

**All three screens use the one global stream.** A per-run transport does exist —
`GET /runs/{id}/events` answers a server-sent stream on `Accept: text/event-stream`
(`api.go:109`) — and an earlier draft had run detail subscribe to it route-scoped. **That
violates a Plan 11 invariant.** `_app.tsx:63` owns "THE ONE STREAM THIS TAB OPENS" and warns that
a second call to the hook is a second connection; `_app.test.tsx:98` asserts exactly one. Mounting
a per-run stream on run detail either opens a second connection against that invariant, or
replaces the global one and silently kills the cross-run approvals badge on the very screen where
a decision is about to be made.

So the change is one change, not two: **`phase.` joins `attentionPrefixes`** — a one-line edit to
a filter deliberately built as a prefix test rather than an enumeration, "so this filter never
rots as kinds are added" — and the bridge routes frames by `run_id`. Home and Tasks invalidate
their list queries; run detail invalidates the run it is showing. `GET /runs/{id}/events` stays
what it is today: the poll and resume path, not a second live socket.

The bridge's own house rule governs the extension. `cache-bridge.ts` records that a test-only
`flush()` left the shipped scheduling arm unasserted and that deleting it kept all five plan tests
green. New routing is proved through the injected timer, the way production fires it, and the
proof asserts an **intermediate phase transition** — not gate arrival or run completion, which is
what Plan 11 already covers and what would pass with phase routing entirely absent.

URL-as-state for filters and tabs, per the constitution.

## 5. Failure — two refusals to prevent, two to catch

**`403` self-approval is prevented, not caught — and it suppresses all three controls, not just
approve.** `policy_snapshot.allow_self_approval` is on the run, so the client knows before render
whether deciding is permitted. Two things must be got right, and an earlier draft got both wrong.

**The refusal is whole-surface, not approve-only.** `gates.go:188` runs the requester check and
returns `ClassForbidden` **before** the branch on decision type at line 204, so `request_changes`
and `reject` are refused identically to `approve`. Hiding only approve would ship two controls
guaranteed to 403 — and it is the plausible-looking mistake, which is why §7 asserts against it.

**Self-approval is not the only 403.** `decideGate` requires `GrantApprove` (`api.go:403`), while
`GET /approvals` lists gates for any grant the caller holds on the project — deliberately, and
`api_integration_test.go:1256` pins it: seeing a gate is not being allowed to decide it. A
view-only principal therefore gets a full inbox of undecidable gates.

Both are answered by §2.4's `can_decide` and its reason, computed server-side where the grant and
the policy snapshot both live. Where it is false the entire decision surface is suppressed and the
stated reason distinguishes the two cases, while the artifact and its evidence still render —
reading is a view grant and is unaffected. On a single-operator system the operator is always the
requester, so the self-approval branch is the normal path rather than an edge case.

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
tests vacuous.

But "assert the guard directly" is not the fix either, and an earlier draft named it wrongly twice
over. `_app` authenticates in its **`loader`**, not in `beforeLoad` (`_app.tsx:24`) — and a direct
callback assertion would drop the router-and-history behaviour the current test actually proves:
that exactly one redirect fires and `/login` replaces the entry rather than stacking on it.

The rehoming therefore keeps a **router-level** unauthorized-navigation test, retargeted at the
now-loader-bearing Tasks route: navigate unauthenticated, assert one redirect to `/login`, and
assert that Tasks' own loader failure does not add a second. The property being preserved is the
guard's observable behaviour under the router, not the absence of a request. This is its own task
with its own mutation proof, not a line inside the Tasks task.

## 7. Testing

| Level | What it covers |
|---|---|
| Vitest + Testing Library + MSW | Each screen against the generated client: attention grouping over all seven run statuses, the two distinct empty states per list, hard-facts computation from a decoded patch, a gate with **no** `review_verdict` rendering honestly, the three decision paths, and `can_decide` suppression in both of its reasons — the decision assertions repeated on **all three** surfaces that offer controls (Home's card, the run-detail gate phase, Approvals), since §5's all-or-nothing rule is what a card is likeliest to get wrong |
| Vitest, pure | Patch decode and diff parsing — base64 → files → ±line counts, including a non-UTF-8 payload, which is the case §2.2's contract exists for |
| Go | The four wave-0 items: grant scoping on the runs list and on artifact reads; cursor pagination and its ordering; the examined-artifact projection agreeing with what `DecideGate` stamps, asserted on a gate whose examined origin is *not* the newest artifact on the run; and payload schema acceptance — a payload with unknown extra keys, which must pass, and one missing a required key, which must fail the attempt |
| Playwright, mobile viewport, real binary | The three critical paths below |

Playwright extends Plan 11's existing rig — Postgres up, migrations applied, admin bootstrapped,
Vite build embedded into a `-tags embedui` binary, specs run against it, `runs.Executor` faked.
Plan 11 built that lane precisely so 12a could add specs to something that already works.

The three mobile-critical paths:

1. A gate opened through the production write path appears on Home, opens to its examined
   artifact, and approving clears the badge **with no user action** elsewhere.
2. Create a task; its run appears and the timeline advances live.
3. `request_changes` with a note opens a new generation, visible on the timeline.

**Paths 1 and 3 are the operator interface for row 08's smoke — not a substitute for it.** An
earlier draft claimed running them once against a real forge closes the row. It does not. The
checklist in `backend/README.md` is forge-side and Playwright can observe none of it: a throwaway
repo on **each** forge (GitHub *and* GitLab, because the two adapters' response shapes and error
mappings are invented until a live call proves them); a branch named `caesar/<approved-artifact-id>`
whose head is the reported `head_sha`; the commit's author being the requester and its committer
being `caesar <caesar@invalid>`; all four `caesar-*` trailers and **no** `Co-authored-by`; a pull
request whose base is the default branch and whose number and URL match the publication
projection; and the two broken-credential cases at the publish step, in the stated order.

What 12a changes is that the checklist's operator half — reach the gate, one revision round,
approve — is walked on a phone instead of with `curl`. **Row 08 closes when every checklist item
is recorded against both forges**, which stays an operator event, as the roadmap says.

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
Twelve entries below; **eleven are departures**. §10.2 is a departure this document proposed and
then withdrew, and it keeps its number because the reasoning is worth more than the tidiness.

Four entries came from re-reading the mockups *after* the first pass claimed to be complete —
§10.2's rebuttal and §10.7 from one review round, §10.9 from a second, §10.8 from a sweep those
prompted. The fidelity claim above is only as good as the pass behind it, and this one took four
passes to stop finding things. That is the number to remember the next time a document says
"nothing is dropped silently" after a single read.

### 10.1 No data source exists — cost, and `tests`

The locked approval mockup's hard-facts row reads `4 files · +120 −8 · tests ✓ · $0.84`.

**Cost cannot be built.** There is no cost, usage, token or price field anywhere in the API *or*
the database schema. Only `execution_used_ms` exists, which is time, not money. The fact is
dropped and duration takes its place. Restoring it requires usage accounting that does not exist
and is not in this plan.

**`tests` is buildable only because §2.3 is already opening the schema**, so a test-outcome field
joins `review_verdict`'s required set. It populates only when a harness actually reports test
results; the row renders the fact as unknown rather than inventing a tick.

### 10.2 A deviation proposed and withdrawn — Home's inline approve

**Outcome: Home ships locked.** Gate cards carry inline approve · request changes · reject, and
the run's expanded gate phase carries the same three under the full evidence. This entry records
why the document first said otherwise, because the mistake is a reusable one.

The proposal was to make Home's cards navigate instead of deciding. Its argument came from
`gates.go:193`: the server resolves the examined artifact from the stored workflow definition and
stamps `examined_artifact_id` onto the decision **whether or not a human looked at it**. An inline
approve on an evidence-free card, the argument went, records an examination that did not happen.

**The argument does not survive.** Navigating to a screen is not examining what is on it either.
Requiring the trip makes `examined_artifact_id` no truer; it only rules out the case where the
payload was never fetched — and it buys that by deleting a locked interaction. "Evidence
rendered" is not "evidence read", and no reachable design closes that gap:

- Echoing `examined_artifact_id` back with the decision proves nothing, because §2.4 puts that id
  on `ApprovalResponse` — the *list* response. Any client has it without fetching a payload.
- Requiring a content digest from §2.2's endpoint would genuinely prove retrieval — and buy
  "the bytes reached the device", still not cognition, for an ETag, a digest field, a comparison
  and a 409 path. On a single-operator system, where the requester and the approver and the only
  reader of the audit trail are one person, that is a provenance mechanism whose sole job is to
  keep the operator honest with themselves.

So the field is what it factually is: a **server-selected evidence binding** — the artifact the
decision was *about*, resolved from the definition — and not a record of human examination.
`schema.sql:347` currently reads as the latter, and that comment is corrected as part of wave 0.
The system's real guarantee is that publication waits for a person to act, which holds wherever
the person acts from.

**What settles the interaction is the checkpoint, not this argument.** Whether a gate is worth
deciding from a list without opening it is a taste question, and §1 already builds the place to
answer taste questions: wave 1 ends on the operator's own phone with their own data. Ship the
locked affordance, find out whether it gets used, and remove it then with evidence rather than
now with reasoning.

### 10.3 Deferred pending the operator assessment — cancel / retry

Locked run detail A has a sticky header with cancel and retry. An earlier draft said flatly that
no endpoint exists and nothing in the API mutates a run's lifecycle. That is wrong, and the two
controls are not in the same position.

**Retry largely exists.** `POST /api/v1/tasks/{id}/runs` (`rerunTask`) is a shipped, contracted
endpoint, and `Service.Rerun` supersedes a pending predecessor, supersedes its phases and emits
the lifecycle event (`tasks.go:313`). It refuses a `running` or `waiting` run, and the refusal's
own message says why: *"run is in flight; cancel arrives in Plan 09."* So the sticky header's
retry, for terminal and pending runs, is a button on an endpoint that already works. What does
not exist is a **phase-local** retry — re-running one node rather than the task.

**Cancel does not exist, and it is already assigned elsewhere.** Nothing mutates a live run's
lifecycle, cancellation has to reach a running container rather than flip a row, and the code
above names **Plan 09** as where it arrives — the same row §9 records as deferred for its
kill-switch panel. Cancel is row 09's to build; 12a claiming it would be taking another row's
work, not deferring its own.

12a ships neither control, on the argument that using the UI will inform what the header should
offer. The correction is to the reasoning, not the outcome — but see §11: whole-task retry is now
the cheapest thing in this document, and deferring it is a choice rather than a constraint.

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

### 10.7 No endpoint exists — the Approvals history

Locked shell A lists **"History of past decisions"** under Approvals
(`01-app-skeleton.html:65`). 12a's Approvals screen is waiting gates only.

`GET /approvals` is waiting-gates-only *by design* — its handler summary says so — and nothing
lists decided gates across runs. A single run's decision is not lost: `ApiGateDecisionSummary`
projects it onto the run's gate phase with `decision`, `note`, `acted_by_email` and `acted_at`, so
§3.4 shows it in place. What is missing is the cross-run list, and it needs an endpoint.

Deferred to 12b with the other surfaces a person visits rarely. Named here because §1's claim is
that nothing is dropped silently, and this one was.

### 10.8 No data source exists — the task lifecycle

The Tasks mockup's caption has tasks flowing `draft → ready → running → in review → done`.
**`draft`, `ready` and `in review` do not exist.** `tasks` has no status column at all
(`schema.sql:197`) and `TaskResponse.Status` is simply the latest run's status (`dto.go:190`), so
a task is born already running: `createTask` makes a run.

The four locked *groups* survive this — §3.3 maps all seven run statuses onto them — but the
five-stage lifecycle behind the caption does not, and a Kanban board (mockup A, a later toggle)
would have three columns with no source. Nothing in 12a needs those stages; naming it here is so
that 12b's board does not get planned against a lifecycle that was never built.

### 10.9 Deferred with the rest of desktop — ⌘K, and run detail's split view

Two locked *desktop* behaviours are absent from §3 and were absent from this list. Locked shell A
is "full sidebar + top bar **with ⌘K**" (decisions §7), and §3.1 says only sidebar plus top bar.
Locked run detail A ends "desktop = timeline left rail + detail right", and §3.4 specifies the
phone spine only.

12a is the month-one flow **on a phone** (§1) and its checkpoint is conducted on one, so neither
blocks anything 12a is for. Both are named because §1 claims nothing is dropped silently, and
these were the last two that were. Desktop layout and the command palette land in 12b, where the
configuration surfaces make a desktop worth designing for.

## 11. Residuals

- **The wave 1 checkpoint is a gate, not a milestone.** Waves for cancel/retry and logs are
  specified after it, informed by it.
- **Whole-task retry is available and deferred anyway** (§10.3). `rerunTask` ships today; a retry
  button on terminal and pending runs is a call, not a feature. It stays out so the checkpoint
  decides what the sticky header offers as a whole — but if the answer at the checkpoint is
  "obviously retry", the cost of adding it was never the reason to wait.
- **`phase.` on the global attention stream widens what every connected client receives** (§4). A
  run in flight emits far more phase transitions than gate or publish events, and the bridge's
  debounce is what keeps that from becoming a refetch storm. Named because the one-line filter
  change is the smallest edit in wave 1 and the easiest to make without thinking about volume.
- **Home's inline approve is on trial, not settled** (§10.2). It ships because it is locked and
  because the checkpoint is the right instrument for the question. If the operator finds they
  always open the run first, the card's buttons come off with evidence behind the removal.
- **`GET /tasks` and `GET /approvals` remain unpaginated.** Correct at month-one volume, a cliff
  later; named so the next person meets it deliberately.
- **Row 08 closes when every item of `backend/README.md`'s checklist is recorded against both
  forges** (§7), which is an operator event. Paths 1 and 3 are the interface that event is walked
  on, not the event. An earlier draft of this residual said the paths close the row and
  contradicted §7 on the same page. **Row 12a is not itself blocked by that run.**
- **Plan 11's one open operator item is unrelated and still open**: proving the `__Host-` cookie
  survives the Vite dev proxy, per the six steps in `web/vite.config.ts`.
