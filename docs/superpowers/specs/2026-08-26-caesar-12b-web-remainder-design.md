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
curl and no psql — create a project, add a repo and a credential, register a harness, define a
linear workflow in the outline editor, run a task through it, request changes with a note the
harness actually receives, approve, open the PR, and afterwards read the decision in Approvals
history.** That single walk exercises every 12b screen plus the top product fix.

Fix-first ordering, decided against the alternatives (screens-first; a two-plan split) because
the backlog items are the ones that bit the operator during the 12a checkpoint, and fixing them
first means every manual pass over the new screens happens on a UI that no longer lies about
auth failures or ignores gate notes. One plan, one branch; if the wave-0 fixes are wanted in the
deploy before the screens finish, merging early from the branch at the wave boundary stays open.

| Wave | Contents |
|---|---|
| **0 — conveyor truth** | The checkpoint backlog, backend and web: gate-note injection into the harness prompt; honest failure display (`auth_expired`, attempt counters); identical-revision warning; fixed phone tab bar; sticky-header Retry; linkified event rows and phase cards; refetch-fan tuning; wordmark→Home; FAB restyle; git in the runtime image + in-image publish assertion; runs-list index migration; deadline expiry in `cannot_decide_reason`; go-mod-tidy CI lane. |
| **1 — Repos + Admin** | The two screens, the two read endpoints (`GET /api/v1/audit`; the decided arm of `GET /api/v1/approvals`), and the History tab the decided arm exists for on the 12a Approvals screen. |
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
`runs` / `me` refetches while a run is in flight — each streamed event fans into collection
refetches through the cache bridge, stacked on the poll fallback; the 250ms debounce
(`realtime/cache-bridge.ts`) is not containing it. The fix: invalidations batch per debounce
window into one targeted set scoped by event kind, and `me` leaves the fan entirely. Observable
screen behavior stays identical — this is a network-panel fix, and its test asserts request
counts under an event burst through the bridge's injected timer, not UI state.

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
already false. Minimal V1 surface: cursor + limit, no filters until a use appears.

### 3.2 The decided arm of `GET /api/v1/approvals`

The existing endpoint gains a `state=waiting|decided` parameter defaulting to `waiting`, so
current behavior — waiting-gates-only, by design, unpaginated — is untouched. The decided arm is
cursor-paginated and returns cross-run decision rows: decision, note, actor, `acted_at`, and
run/task/gate identifiers for linking. Same grant scoping as the waiting arm.

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
(forge kind `github|gitlab`, slug, default branch, credential picker filtered to `forge_token`
kind). Deletes surface the API's RESTRICT refusals as readable errors ("this repo has runs")
rather than raw problem+json. Card-first on the phone, table projection on desktop, per the
locked mobile-first standard.

## 5. Admin

The `admin.tsx` stub becomes the screen, four sections:

- **Users** — list, create, role edit: the account-level half of authz (the project-level half,
  grants, lives on the project detail, §4).
- **Harnesses** — the registry: name, kind `claude-code|codex`, image ref, provider endpoint,
  credential binding, defaults JSON.
- **Credentials** — list showing kind, fingerprint, `secret_version`, `last_used_at`, and
  **`last_auth_expired_at`** — the column that made the expired-token finding invisible gets a
  face. Create, rotate secret via the existing `PUT /credentials/{id}/secret`, delete.
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
git-publish) → fill the type's fields, generated from the engine's schema — agentic cards get a
harness picker (from the registry) and an instructions textarea; gate cards get the revision
loop (target restricted to preceding agentic nodes, max rounds). Node IDs auto-derive
(`plan-1`, `impl-1`), editable for taste. Drag to reorder by the grip, per the mockup.

### 6.3 Edges are derived, not drawn

For a linear chain the typed artifact bindings follow mechanically from node-type adjacency —
and the derivation rules are written against Plan 04's validator invariants
(gate-dominates-exact-patch included), with a property test as the acceptance bar: **any chain
the editor can produce parses and validates server-side.** The client mirrors only the cheap
checks inline (missing instructions, gate target ordering); the server's validator remains the
single truth, and a save rejection maps field errors back onto the offending card.

### 6.4 Save and drafts

Save posts to the existing `createWorkflowVersion` door — no new backend write path. Drafts live
client-side only (localStorage per workflow, so a phone tab death doesn't eat ten cards of
typing); the backend never sees an invalid document as a row.

## 7. Desktop

**⌘K** lands scoped to what a single operator actually jumps to: the seven pages, "New task",
and recent tasks/runs pulled from the query cache — client-side matching only, no new endpoint.

**The desktop Playwright project** 12a parked arrives: the same critical-path specs at a desktop
viewport, plus the three waiting geometry assertions and the split-view and ⌘K checks. The
sidebar already exists from Plan 11 and simply gets the two real screens instead of stubs.

## 8. Testing

- **The two endpoints**: table-driven handler tests plus testcontainers integration, with
  pagination edges (empty page, exact-boundary cursor) covered.
- **Gate-note injection**: the acceptance test asserts on the assembled
  `ExecutionRequest.Instructions` for a revision generation; red-then-revert is deleting the
  injection. The existing `gatenote_runner_test` staging assertions stay as the
  artifact-contract half.
- **The editor**: a property test over the derivation rules (any editor-produced chain validates
  server-side), plus one Playwright e2e that authors and saves a workflow against the real
  backend — the Task 17 harness already runs one.
- **The refetch fan**: request counts under an event burst through the bridge's injected timer.
- **The milestone's automated shadow**: one Playwright spec walking project → repo → credential
  → harness → workflow → task → gate → decision against the fake harness, so "UI is the only
  door" is CI-checked continuously, not just walked once at the checkpoint.
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
4. **Cost and `tests` hard-facts fields stay absent** — no data source exists (12a §10.1,
   unchanged).
5. **The task-lifecycle Kanban board stays absent** — `draft`/`ready`/`in review` still do not
   exist as data (12a §10.8, unchanged).
6. **⌘K arrives; nothing of §10.9 remains deferred** — run detail's split view shipped in 12a
   (Task 16), and this plan brings the palette.

## 11. Residuals

- **`GET /tasks` and the waiting arm of `GET /approvals` remain unpaginated** — still correct at
  this volume, still a cliff later; the decided arm and audit paginate from birth (§3).
- **Row 08's both-forges live smoke is untouched by this plan** and stays an operator event; the
  Approvals history gives it one more surface to be walked on, not a closure.
- **Multi-arch and the TLS/phone-reachable deploy shape are row 14's**, moved there by this
  spec's scope decision 1; until then the deploy remains localhost-plus-tunnel.
- **Editor drafts are client-only**: a cleared localStorage loses an unsaved draft. Accepted for
  a single-operator system; server-side drafts would be a new write path for a problem the
  operator can see.
- **The refetch-fan fix tunes the existing polling+stream design, it does not redesign it.** If
  volume grows past what batching absorbs, per-entity subscriptions are a later plan's shape.
