# caesar — web remainder (Plan 12b design)

Plan 12b closes roadmap row 12. 12a shipped the month-one flow on a phone (task → gate →
revision → approve → PR, milestone achieved 2026-08-26); 12b takes everything row 12 still
owes: the 12a checkpoint's backlog, the three configuration surfaces — Workflows outline
editor, Repos, Admin — and desktop. When it lands, the API stops being the only door.

Authority for what these screens look like is `docs/brainstorm/2026-08-01-decisions.md` §7 and
the eight locked mockups in `.superpowers/brainstorm/12432-1785604290/content/`
(`04-workflow-editor` is 12b's own). §10 of this document lists every place 12b departs from
them and why. Nothing is dropped silently.

Code citations below are against `main` at `fdbc8de`; line numbers drift, function names less.

## 1. Scope — what "done" means

**The milestone, verified by the closing operator walk: from a fresh browser session, with no
curl and no psql — create a project, add a forge credential and a repo bound to it, add a
harness credential and register a harness, define a linear workflow in the outline editor, run
a task through it, request changes with a note the harness actually receives, approve, open the PR, and afterwards read the decision in Approvals
history.** That single walk exercises every 12b screen plus the top product fix. The order is
dependency-true, and it takes **two credentials**: a repo binds a `forge_token` at creation
(`CreateRepoRequest.credential_id` is required; `assets/repos.go` enforces the kind) and a
harness binds a model/subscription/auth-file credential (`assets/harnesses.go`,
`harnessCredentialKinds`) — disjoint kinds, so one credential can never serve both.

Fix-first ordering, decided against the alternatives (screens-first; a two-plan split) because
the backlog items are the ones that bit the operator during the 12a checkpoint, and fixing them
first means every manual pass over the new screens happens on a UI that no longer lies about
auth failures or ignores gate notes. One plan, one branch; if the wave-0 fixes are wanted in the
deploy before the screens finish, merging early from the branch at the wave boundary stays open.

| Wave | Contents |
|---|---|
| **0 — conveyor truth** | The checkpoint backlog, backend and web: gate-note injection into the harness prompt; honest failure display (`auth_expired`, attempt counters); identical-revision warning; fixed phone tab bar; sticky-header Retry; linkified event rows and phase cards; refetch-fan tuning; wordmark→Home; FAB restyle; git in the runtime image + in-image publish assertion; runs-list index migration; deadline expiry in `cannot_decide_reason`; go-mod-tidy CI lane. |
| **1 — Repos + Admin** | The two screens, the two read endpoints (`GET /api/v1/audit`; `GET /api/v1/approvals/decisions`), and the History tab the decision history exists for on the 12a Approvals screen. |
| **2 — Workflows** | List, version history, version read view with DAG preview for any document; linear-first outline authoring. |
| **3 — desktop** | ⌘K command palette; the desktop Playwright project with the parked geometry, split-view and ⌘K assertions. |
| **checkpoint** | The operator walk above, on a real deploy, phone + desktop; taste verdicts recorded in the ledger. Row 12b flips only on that record. |

**Scope decisions, all sofo 2026-08-26:**

1. Of the three deploy/packaging backlog items, **git-in-the-runtime-image stays in 12b** (the
   published artifact cannot publish — a correctness bug); **multi-arch and the TLS /
   phone-reachable deploy shape move to row 14**, which owns the deploy story. The roadmap is
   amended in the same commit as this spec.
2. **Admin ships users/roles, harness config, credentials, and global audit** (a new read
   endpoint over the `audit_entries` table, written since Plan 02 and never readable). Policy
   baselines and the kill switch stay row 09's, and the deferral was already accepted in the
   session-65 ordering decision.
3. **The outline editor is linear-first.** It authors ordered chains; a branching document
   authored via the API still opens read-only with the DAG preview rendering it faithfully.
   Branch-as-indent editing arrives with the later canvas/code projections, as the locked
   decision itself anticipates ("a canvas can still come later as another projection").
4. The milestone is the walk above — screens existing gate-green is not enough to close the row.

**Explicitly out, each with an owner:** multi-arch image and TLS/phone-reachable deploy →
row 14; policy baselines, kill switch, and run cancel → row 09; every assistant surface
(including "drafts a workflow from a sentence") → row 13; branch editing in the outline → the
later projections; pagination of `GET /tasks` and the waiting arm of `GET /approvals` → stays a
named residual at month-one volume (§11).

## 2. Wave 0 — conveyor truth

The organizing principle: everything the checkpoint proved the system does *right* but shows
wrong — or silently drops — is fixed before any new screen is built.

### 2.1 The gate-note reaches the harness prompt

The 12a checkpoint's best finding: `openRevision` seeds a request-changes note as a `gate_note`
artifact and the runner stages it at `.caesar/in/1-gate_note.json`, but the harness prompt is
the node's instructions verbatim, so generation 2 never learns the file exists — the revision
loop's only new input, silently ignored (proven live: a byte-identical generation-2 patch).

The fix seam is `buildRequest` (`internal/runs/orchestrator.go`), the one place every harness's
instructions are assembled — Docker runner, the fake executor the acceptance lane runs on, and
any future harness are told the same thing from the same source. When the attempt's seed inputs
include a `gate_note` artifact, its text is appended to `req.Instructions` as a clearly marked
requested-changes section — **appended, never substituted**, the same pattern the
`OutputShapeInstruction` comment on that function already blesses. The staged file stays: it is
the artifact contract. The note is already length-bounded upstream (`maxGateNoteLen`, 8 KiB,
`gates.go`), so prompt growth is bounded by construction.

Two consequences are part of the fix. The `walk` workflow's v2 workaround — instructions that
name the staging path — is retired; the definition goes back to plain instructions, and the
closing checkpoint witnesses a note honored *without* the workaround (§9). And the acceptance
test asserts on the assembled `ExecutionRequest.Instructions` of a revision generation, not on
staging: the wired-lane lesson is the test's design constraint, and its red-then-revert proof is
deleting the injection and watching the assertion fail.

### 2.2 Honest failure display

One rule fixes three findings: **a phase card's status line derives from the outcome class, not
a bare counter.**

- Transient with retry budget → `attempt n/cap`, as today.
- Permanent → the terminal code, and never a counter that promises retries (`auth_expired`
  names the credential — the run screen finally distinguishes an expired token from a crashed
  harness; the checkpoint's finding 7 showed the engine doing everything right behind
  `impl dead attempt 1/3`).
- Resumable past the chargeable cap → "resumable — not chargeable" instead of the bug-shaped
  `attempt 4/3` (finding 2).

Plan-time verification: whether the run's phase projection already carries outcome class and
code, or the DTO gains them (with the `required` markup + regen + pin treatment any DTO change
gets in this repo).

### 2.3 An identical revision is visible

One hash comparison between the examined patch artifact of generation N and N−1; if equal, the
gate screen says prominently that the revision is byte-identical to what was already reviewed
(finding 6 — without it, request-changes can silently loop an operator into approving work that
ignored their review). Plan-time verification: whether an artifact content hash is already
client-visible or the artifact DTO gains one field.

### 2.4 Shell and navigation

- **The phone tab bar becomes actually fixed** (`nav.tsx`'s phone-tabs div has no
  `fixed`/`sticky`; the FAB's `bottom-20` comment was written assuming a fixed bar — finding 14).
- **Sticky-header Retry on failed runs only**, wired to the existing `rerunTask`
  (`POST /api/v1/tasks/{id}/runs`). Rerun keeps title, inputs and pinned version — right for
  transient failures, and rejected runs end `failed`, so one status rule covers both (sofo's
  checkpoint ruling, verbatim "Retry on failed runs only").
- **Event rows and phase cards that name a destination become links**: `publish.pr_opened` → the
  PR URL, `gate.decided` → the gate decision, phases with artifacts → the artifact view
  (finding 13 / mockup-gap entry thirteen).
- The **wordmark links Home** (finding 15); the **FAB is restyled** (finding 16, taste verdict
  at the checkpoint).

### 2.5 The refetch fan collapses

Measured at the checkpoint: a steady several-per-second drumbeat of `events` / `approvals` /
`runs` / `me` refetches while a run is in flight. The bridge's existing machinery is NOT the
gap — `realtime/cache-bridge.ts` already pools frames per 250ms window, dedups run ids, scopes
`['approvals']` by `ATTENTION_KINDS`, and never invalidates `me` — so "batch and scope the
invalidations" would restate the status quo and change nothing. The real fan is what every
window fires *unconditionally*: `['runs']` + `['tasks']` + one `['run', id]` per dirty run,
so a chatty phase stream refetches the collections up to every 250ms, stacked on the 60s
refresh floor and focus refetches (`api/query-client.ts`). The fix, two parts:

1. **Two-tier collection invalidation.** Windows containing a status-moving kind (the
   existing `ATTENTION_KINDS` set, mirrored from `events.go` — checked, not remembered) keep
   invalidating the collections immediately, as today. Phase-only windows stop firing the
   collections every 250ms and instead schedule a trailing collection refresh on a slower
   throttle — single-digit seconds, exact value picked at plan time and asserted in the test.
   NOT an outright drop: the collections *render* phase progress — `RunCard` and the Tasks
   rows draw `PhaseDots` from `run.phase_summary` (`runs/run-card.tsx`,
   `routes/_app/tasks.tsx`), and Home's "live phase dots" are locked (decisions §7) — so
   deferring phase frames to the 60s floor would be a locked-design regression dressed as
   tuning. The bound: status changes stay immediate, phase dots advance within the throttle,
   and the drumbeat collapses from several collection refetches per second to at most one
   set per throttle interval (per-run `['run', id]` invalidation stays per-window, so run
   detail keeps its current liveness).
2. **Find `me`'s initiator before touching it.** Nothing in the bridge fires it, and the only
   realtime `/auth/me` is the rare forbidden-stream probe (`sse-connection.ts`) — so the
   drumbeat's source (the 60s `refetchInterval` floor, `refetchOnWindowFocus`, or something
   else) is identified in the network panel at plan time; its fix is whatever the measurement
   names, not a guess.

The acceptance test counts real HTTP requests under an event burst — including `/auth/me` —
through the bridge's injected timer plus a request-recording fetch layer, and it asserts the
liveness half too: under a pure-`phase.*` burst, a rendered runs/tasks list advances its phase
dot within the throttle bound. Asserting on invalidation calls alone would pass against
today's bridge and prove nothing.

### 2.6 Packaging and CI

- **git is baked into the runtime image.** `git-publish` shells out to `git` in-process and the
  shipped distroless image has none — the deploy's first blocking finding: the published image
  cannot publish. With it comes a CI assertion that executes inside the *built image* (at
  minimum `git version`; better, a publish step against a local bare repo), so "no lane executes
  a publish from inside the shipped image" can never again be true silently.
- **The runs-list index migration** lands early in wave 0 (parked by the 12a final review).
- **`cannot_decide_reason` gains a server-computed deadline-expiry value.** `decidability`
  (`internal/runs/api/api.go`) currently returns `not_approver` / `self_approval_forbidden` /
  `evidence_missing` in a deliberate order (each names the refusal `decideGate` would actually
  give, fixable-first); the expiry value slots into that order under the same rule. Plan-time
  verification: the exact expiry semantics the gate deadline machinery already has.
- **A go-mod-tidy lane joins CI** (parked by the 12a final review for the roadmap).

## 3. Two read endpoints

Both are read-only projections riding the existing contract pipeline (Go types → OpenAPI 3.1 →
generated TS client, regen-diff gates), so the client halves are typed for free. No new write
paths anywhere in 12b's backend.

### 3.1 `GET /api/v1/audit`

Admin-only, newest-first, **cursor-paginated from day one** — `audit_entries` grows without
bound, and this table is the one place where "unpaginated is fine at month-one volume" is
already false. Minimal V1 surface: cursor + limit, no filters until a use appears. The cursor
key is `(occurred_at DESC, id DESC)` — `occurred_at` is not unique (writers stamp it from the
injected clock, and one transaction can emit several entries, e.g. the login sweep in
`identity/login.go`), and the table's only index is `audit_entries_occurred_idx (occurred_at)`
(`schema.sql`), so the same change adds the matching composite index. Equal-timestamp rows
straddling a page boundary are in the pagination tests, same as §3.2's.

### 3.2 Decision history: `GET /api/v1/approvals/decisions`

An earlier draft put this on the existing operation as `state=waiting|decided`; that shape does
not survive the contract pipeline — one operation carries one response schema, and the waiting
arm's `ApprovalListResponse` (`approvals: []ApprovalResponse`, `runs/api/dto.go`) has no seam
for decision rows or a cursor. So the history is a sibling operation and the existing operation
changes by zero bytes: `GET /api/v1/approvals/decisions`, cursor-paginated newest-first,
returning cross-run decision rows — decision, note, actor, `acted_at`, and run/task/gate
identifiers for linking — in its own envelope with a `next_cursor`. Same grant scoping as the
waiting arm. The cursor key is `(acted_at DESC, id DESC)`, and the same change adds the matching
composite index — `gate_decisions`' only index today is `(run_id, node_id)` (`schema.sql`),
which cannot serve a global newest-first read. Pagination tests include equal-`acted_at` rows
straddling a page boundary.

The UI half lands in the same wave: the Approvals screen grows a **History tab** beneath the
waiting list, newest-first, load-more on the cursor, each row linking into its run. This closes
12a §10.7 — the locked shell's "History of past decisions" under Approvals, dropped there
because no endpoint existed.

## 4. Repos

The `repos.tsx` stub becomes the screen. **Projects are the organizing unit**: a card list of
projects, each opening to a project detail that holds its repos and its grants — grants live
here rather than in Admin because the API is project-nested (`/projects/{id}/grants`) and a
grant is a property of a project.

Create/edit through Plan 11's shared typed form abstraction: project (name, description), repo
(forge kind `github|gitlab` chosen at create and immutable after — `UpdateRepoRequest` carries
only slug, default branch and credential (`assets/api/dto.go`), and 12b adds no write paths, so
the edit form renders forge read-only; slug, default branch, credential picker filtered to
`forge_token` kind). Deletes surface the API's RESTRICT refusals as readable errors ("this
repo has runs") rather than raw problem+json. Card-first on the phone, table projection on desktop, per the
locked mobile-first standard.

## 5. Admin

The `admin.tsx` stub becomes the screen, four sections:

- **Users** — list, create, role edit: the account-level half of authz (the project-level half,
  grants, lives on the project detail, §4).
- **Harnesses** — the registry: name, kind `claude-code|codex`, image ref, provider endpoint,
  credential binding, defaults JSON.
- **Credentials** — list showing kind, fingerprint, `secret_version`, `last_used_at`, and
  **`last_auth_expired_at`** — the column that made the expired-token finding invisible gets a
  face (it is already in `CredentialResponse`; only the UI ever hid it). Fingerprint is a small
  read-path contract addition: the column is stored at create but absent from the metadata
  queries, domain projection and `CredentialResponse` (`assets/store/queries.sql`,
  `assets/api/dto.go`), so the read path gains it with the usual required-markup + regen + pin
  treatment. Create, rotate secret via the existing `PUT /credentials/{id}/secret`, delete.
- **Audit** — the §3.1 endpoint, newest-first, load-more on the cursor.

A visible placeholder card names what arrives with row 09 — policy baselines and the kill
switch — so the page's eventual shape is honest about what is missing rather than silent.

## 6. Workflows

### 6.1 The screen's spine: list → detail → version

The list is workflow cards (name, description, latest version number). A workflow detail shows
metadata (PATCH-editable name/description) and its immutable version history, newest first, each
row carrying its content hash. Opening a version shows the two projections the locked design
names:

- **DAG preview** on top — nodes badged by type in the mockup's color language (agent / gate /
  git), a simple dependency-free SVG layout, no graph library. Every document renders here,
  including branching ones authored via the API.
- **Outline** of step cards below. For non-chain documents the outline is read-only with an
  "authored outside the editor" notice.

### 6.2 Authoring is "new version", never "edit in place"

The document model is immutable content-hashed versions and the editor honors it: a new version
starts from scratch or prefilled from an existing chain-shaped version. Editor state is an
ordered list of typed cards: add step → pick type (plan / implement / review / gate /
git-publish) → fill the type's fields. The card forms are hand-authored typed forms mirroring
`workflows/definition.go`'s node types — there is no client-visible schema to generate from,
because the definition document rides the transport as `json.RawMessage` by design
(`workflows/api/dto.go`: schema enforced by the write door, not the contract); §10 departure 9
records this and names the drift gate that replaces generation. Agentic cards get a
harness picker (from the registry) and an instructions textarea; gate cards get the revision
loop (max rounds; targets are the preceding **plan/implement** cards only — review is agentic
(`definition.go`, `Agentic()`) yet never a legal target in the conveyor, because the gate's
patch edge bypasses the review, so review does not dominate the gate and the validator refuses
it (`validate.go`, `validateRevisions`; `genConveyor`'s comment records the same exclusion)).
Node IDs auto-derive (`plan-1`, `impl-1`), editable for taste. Drag to reorder by the grip, per
the mockup. The read-only DAG preview renders above the
outline **in the editor too**, updating live from the derived edges as cards change — locked
variant B puts the live preview in the editor, not only on the version read view (§6.1); the
derivation is a pure client function, so live costs nothing.

### 6.3 Edges are derived, not drawn

Derivation works from the whole card sequence, not from adjacency — adjacency is provably
wrong for the ordinary conveyor: in `implement → review → gate → git-publish`, the validator's
consumption table (`workflows/validate.go`, `validateConsumption`) makes the review consume the
implement's patch, the gate examine that *same* patch — a bypass edge past the review — while
also consuming the review's verdict, and publish take the gate's pass-through patch. The repo's
own conveyor generator builds exactly those edges (`workflows/property_test.go`, `genConveyor`).
So the rules are per-type consumption rules over the ordered cards: a plan feeds the first
agentic card; each implement takes the previous implement's patch; a review takes the nearest
preceding patch; a gate examines the nearest preceding plan-or-patch producer and consumes an
immediately preceding review's verdict; git-publish takes the gate's patch — all written against
Plan 04's validator invariants (gate-dominates-exact-patch included). One consequence is
load-bearing: an editor-produced document is **not edge-linear** (the implement above has two
outgoing edges), so "chain-shaped" is defined by **round-trip**, not graph shape — a version
opens editable iff deriving edges from its node sequence reproduces the stored edge set exactly,
and anything else renders read-only (§6.1).

The editor's V1 sequence grammar is exactly the proven conveyor family:
`plan? · implement+ · review? · gate · git-publish`, optional revision loop on the gate — the
same domain `genConveyor` draws from, so the property test extends the repo's own generator
instead of inventing a second one. The grammar is enforced structurally: the add-step menu
offers only types the position accepts, and a reorder that breaks the grammar is refused with
the reason on the card. This is what makes the acceptance bar honest — a standalone or
misplaced review/gate/publish card fails the validator's *mandatory* consumption rules
(`validateConsumption`: review needs exactly one patch, a gate exactly one plan-or-patch,
publish exactly one patch), so the grammar IS the client-side check, not a nicety on top. The
acceptance bar is the property test: **random editor operation sequences — add, reorder,
retarget, all within the grammar — derive documents that parse and validate server-side.**
The client additionally mirrors the cheap field checks inline (missing instructions, gate
target ordering); the server's validator remains the single truth, and a save rejection maps
field errors back onto the offending card.

### 6.4 Save and drafts

Save posts to the existing `createWorkflowVersion` door — no new backend write path. Drafts live
client-side only (localStorage per workflow, so a phone tab death doesn't eat ten cards of
typing); the backend never sees an invalid document as a row.

## 7. Desktop

**⌘K** lands scoped to what a single operator actually jumps to: the seven pages, "New task",
and recent tasks/runs pulled from the query cache — client-side matching only, no new endpoint.

**The desktop Playwright project** 12a parked arrives: the same critical-path *behavior* at a
desktop viewport, plus the three waiting geometry assertions and the split-view and ⌘K checks.
"Same specs" cannot mean unchanged files — the existing specs assert phone shell geometry
(`e2e/approvals.spec.ts` expects `desktop-rail` hidden), which must fail at a desktop viewport —
so shell/geometry assertions become per-project (viewport-aware) while the shared critical path
stays one set of specs. The sidebar already exists from Plan 11 and simply gets the two real
screens instead of stubs.

## 8. Testing

- **The two endpoints**: table-driven handler tests plus testcontainers integration, with
  pagination edges (empty page, exact-boundary cursor) covered.
- **Gate-note injection**: the acceptance test asserts on the assembled
  `ExecutionRequest.Instructions` for a revision generation; red-then-revert is deleting the
  injection. The existing `gatenote_runner_test` staging assertions stay as the
  artifact-contract half.
- **The editor**: the §6.3 property test — random in-grammar editor operation sequences derive
  documents the server validator accepts, extending `genConveyor` — plus the §10.9 fixture
  drift gate, plus one Playwright e2e that authors and saves a workflow against the real
  backend — the Task 17 harness already runs one.
- **The refetch fan**: the §2.5 pair — real HTTP request counts under an event burst through
  the bridge's injected timer plus a request-recording fetch layer, and the liveness bound (a
  rendered list advances its phase dot within the throttle under a pure-`phase.*` burst).
- **The milestone's automated shadow**: one Playwright spec walking project → forge credential
  → repo → harness credential → harness → workflow → task → gate → decision against the fake
  harness (the §1 dependency-true order), so "UI is the only door" is CI-checked continuously,
  not just walked once at the checkpoint.
- House rules apply throughout: mutation is the acceptance criterion for any test-shaped claim;
  verification is `task backend:ci` + `contracts:check` + `web:ci`, never a hand-picked lane
  list.

## 9. The closing checkpoint

Mirrors 12a's: after merge, a real deploy, and the operator walks the milestone sentence from a
fresh browser session — phone for the conveyor half, desktop for a deliberate pass over the new
surfaces. Two things it must specifically witness:

1. A request-changes note honored by the harness **without** the `walk` v2 workaround, which
   this plan retires (§2.1).
2. The Approvals history showing the walk's own decisions afterwards (§3.2).

Taste verdicts recorded in the ledger as before, including the two 12a deferred to "once it's
built": the FAB, and the texty-ness/linking texture (checkpoint finding 17 — sofo's framing was
polish once correctness and comprehensiveness are achieved, which is this plan's exit). Row 12b
flips only on that recorded walk.

## 10. Departures from locked designs, named

The mockups are authoritative and departures must be explicit (the 12a rule, kept).

1. **Branches-as-indents is deferred** (§1 decision 3). The locked outline B names it; 12b
   authors chains and renders everything. Sofo, 2026-08-26.
2. **"The assistant can draft one from a sentence"** — the `04-workflow-editor` subtitle
   promises it in every variant; every assistant surface is row 13's.
3. **Admin ships without policy baselines and the kill switch** (row 09), behind a visible
   placeholder rather than silence.
4. **The cost hard-fact stays absent** — no usage accounting exists anywhere in the API or
   schema (12a §10.1). `tests` is NOT absent and never was after 12a: 12a opened the
   `review_verdict` schema and shipped the field (`runs/api/payload_dto.go`, rendered by
   `approvals/approval-surface.tsx`); 12b keeps it. The earlier draft of this entry repeated
   12a's pre-build wording as if still true — the exact stale-rationale mistake the house
   rules name.
5. **The task-lifecycle Kanban board stays absent** — `draft`/`ready`/`in review` still do not
   exist as data (12a §10.8, unchanged).
6. **⌘K arrives; nothing of 12a §10.9 remains deferred** — run detail's split view shipped in 12a
   (Task 16), and this plan brings the palette.
7. **Workflows' "Run history per definition" (locked page map, `01-app-skeleton`) is deferred.**
   `GET /runs` filters by `status`/`task_id` only — the surface needs a workflow filter on the
   runs list. Read-only and small, but the milestone walk doesn't need it; named here rather
   than dropped silently, and pulling it into 12b is a plan-time call sofo can make.
8. **Repos' "Per-repo policy + allowed workflows" (locked page map) → row 09** with the rest of
   the policy surface. **"Recent activity per repo" is deferred** on the same missing runs-list
   filter as departure 7.
9. **Locked outline B's "fields generated from the engine's schema" ships as hand-authored
   typed forms** (§6.2). The definition rides the transport as `json.RawMessage` by design, so
   there is no client-visible schema to generate from short of a new cross-language generation
   pipeline. The drift the locked wording guards against is closed by an exhaustive gate
   instead: a Go test emits a committed fixture enumerating node types, per-type editable
   fields, limits and requiredness from `definition.go`; a web unit test asserts the editor's
   card registry covers the fixture exactly — a Go-side field addition breaks CI until the
   editor learns it. §6.3's property test proves validity of what the editor produces; this
   gate proves coverage of what the engine accepts.

## 11. Residuals

- **`GET /tasks` and the waiting arm of `GET /approvals` remain unpaginated** — still correct at
  this volume, still a cliff later; the decision history and audit paginate from birth (§3).
- **Row 08's both-forges live smoke is untouched by this plan** and stays an operator event; the
  Approvals history gives it one more surface to be walked on, not a closure.
- **Multi-arch and the TLS/phone-reachable deploy shape are row 14's**, moved there by this
  spec's scope decision 1; until then the deploy remains localhost-plus-tunnel.
- **Editor drafts are client-only**: a cleared localStorage loses an unsaved draft. Accepted for
  a single-operator system; server-side drafts would be a new write path for a problem the
  operator can see.
- **The refetch-fan fix tunes the existing polling+stream design, it does not redesign it.** If
  volume grows past what batching absorbs, per-entity subscriptions are a later plan's shape.
