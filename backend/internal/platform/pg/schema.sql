-- caesar desired-state schema (single source for Atlas authoring and, from
-- Plan 2, sqlc). Empty until the first domain tables land in Plan 2.
-- Workflow: edit this file, then `task db:new -- <migration_name>`.

-- Identity & access (spec §2, §6). Plan 02.

CREATE TABLE users (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    email         text NOT NULL,
    password_hash text NOT NULL,
    role          text NOT NULL CHECK (role IN ('admin', 'member')),
    active        boolean NOT NULL DEFAULT true,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX users_email_key ON users (lower(email));

-- Server-side sessions: token never stored, only its SHA-256. State is
-- derived (active/revoked/expired) — see the plan's session status model.
CREATE TABLE sessions (
    id           uuid PRIMARY KEY DEFAULT uuidv7(),
    user_id      uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash   bytea NOT NULL UNIQUE,
    created_at   timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL,
    expires_at   timestamptz NOT NULL,
    revoked_at   timestamptz
);

CREATE INDEX sessions_user_id_idx ON sessions (user_id);

-- Assets (spec §2). Plan 03: projects grow their API columns; repos,
-- credentials, harnesses land.
CREATE TABLE projects (
    id          uuid PRIMARY KEY DEFAULT uuidv7(),
    name        text NOT NULL UNIQUE,
    description text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- One grant row per (user, project); ladder view ⊂ run ⊂ approve ⊂ manage.
CREATE TABLE grants (
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    project_id uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    level      text NOT NULL CHECK (level IN ('view', 'run', 'approve', 'manage')),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, project_id)
);

-- Append-only who-did-what (spec §2). Writers pass occurred_at from the
-- injected clock; actor NULL = system (bootstrap).
CREATE TABLE audit_entries (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    occurred_at    timestamptz NOT NULL,
    actor_user_id  uuid REFERENCES users (id),
    action         text NOT NULL,
    subject_kind   text NOT NULL,
    subject_id     text NOT NULL,
    correlation_id text NOT NULL DEFAULT '',
    detail         jsonb NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX audit_entries_occurred_idx ON audit_entries (occurred_at);

-- DB-backed login rate limiting: scope is 'account:<lower(email)>',
-- 'ip:<host>', or 'pwchange:<user_id>'. Parameters pinned in the plan
-- headers (02: login scopes; 03: pwchange).
CREATE TABLE login_rate (
    scope             text PRIMARY KEY,
    failures          integer NOT NULL DEFAULT 0,
    window_started_at timestamptz NOT NULL,
    locked_until      timestamptz
);

-- Encrypted-at-rest secrets (spec §6): per-row AES-256-GCM envelope —
-- wrapped_dek under master key key_id, ciphertext under the DEK, AAD =
-- the credential id. Plaintext never lands here; every use is audited.
CREATE TABLE credentials (
    id                   uuid PRIMARY KEY,
    name                 text NOT NULL UNIQUE,
    kind                 text NOT NULL CHECK (kind IN ('forge_token', 'model_key', 'subscription_token', 'agent_auth_file')),
    key_id               text NOT NULL,
    wrapped_dek          bytea NOT NULL,
    ciphertext           bytea NOT NULL,
    secret_version       bigint NOT NULL DEFAULT 1 CHECK (secret_version >= 1),
    fingerprint          text,
    last_used_at         timestamptz,
    last_auth_expired_at timestamptz,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now()
);

-- Two active agent_auth_file rows sharing one refresh token would hold
-- independent single-flight leases against the same underlying credential
-- (spec §2 "import is exclusive custody transfer").
CREATE UNIQUE INDEX credentials_fingerprint_key ON credentials (fingerprint)
    WHERE fingerprint IS NOT NULL;

-- The single-flight lease: the primary key IS the mechanism (one row per
-- credential). The epoch fences a paused owner from its own reconciler —
-- identity alone cannot, because they are the same phase run (spec §2.1).
CREATE TABLE credential_leases (
    credential_id  uuid PRIMARY KEY REFERENCES credentials (id) ON DELETE RESTRICT,
    attempt_ref    text NOT NULL,
    epoch          bigint NOT NULL DEFAULT 1 CHECK (epoch >= 1),
    secret_version bigint NOT NULL,
    acquired_at    timestamptz NOT NULL DEFAULT now(),
    heartbeat_at   timestamptz NOT NULL DEFAULT now()
);

-- The lease row, indexed by owning attempt, is the recovery authority:
-- reconciliation consults it even when no manifest and no container exist
-- (spec §2.3). UNIQUE because one attempt holds at most one credential
-- lease by design — a second row would make the recovery lookup return an
-- arbitrary one of them and silently leak the rest.
CREATE UNIQUE INDEX credential_leases_attempt_ref_key ON credential_leases (attempt_ref);

-- One receipt per (credential, attempt): the commit-then-crash retry is
-- recognizable as already applied and can never clobber an operator
-- rotation (spec §2.6).
CREATE TABLE credential_sync_receipts (
    credential_id uuid NOT NULL REFERENCES credentials (id) ON DELETE CASCADE,
    attempt_ref   text NOT NULL,
    epoch         bigint NOT NULL,
    from_version  bigint NOT NULL,
    to_version    bigint NOT NULL,
    committed_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (credential_id, attempt_ref)
);

-- Forge repos (spec §2): forge kind + slug + default branch + credential
-- ref. RESTRICT FKs: a project with repos and a referenced credential
-- cannot be deleted.
CREATE TABLE repos (
    id             uuid PRIMARY KEY DEFAULT uuidv7(),
    project_id     uuid NOT NULL REFERENCES projects (id) ON DELETE RESTRICT,
    forge_kind     text NOT NULL CHECK (forge_kind IN ('github', 'gitlab')),
    slug           text NOT NULL,
    default_branch text NOT NULL DEFAULT 'main',
    credential_id  uuid NOT NULL REFERENCES credentials (id) ON DELETE RESTRICT,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project_id, forge_kind, slug)
);

CREATE INDEX repos_project_id_idx ON repos (project_id);

-- Admin-registered harness registry (spec §2): kind, image ref, provider
-- endpoint, the model-key credential the egress broker will use, and
-- free-form defaults.
CREATE TABLE harnesses (
    id                uuid PRIMARY KEY DEFAULT uuidv7(),
    name              text NOT NULL UNIQUE,
    kind              text NOT NULL CHECK (kind IN ('claude-code', 'codex')),
    image_ref         text NOT NULL,
    provider_endpoint text NOT NULL,
    credential_id     uuid NOT NULL REFERENCES credentials (id) ON DELETE RESTRICT,
    defaults          jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

-- Workflow definitions (spec §2). Plan 04: a workflow names a family of
-- immutable, content-hashed versions; the API write door validates every
-- document before insert, and version rows are never updated. Plan 05
-- references workflow_versions (id) from tasks with ON DELETE RESTRICT,
-- so a workflow with runs can no longer be deleted; without them the
-- delete still cascades its versions.
CREATE TABLE workflows (
    id          uuid PRIMARY KEY DEFAULT uuidv7(),
    name        text NOT NULL UNIQUE,
    description text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- content_hash = hex SHA-256 of the canonical document bytes (computed
-- app-side from the parsed struct — jsonb storage re-normalizes bytes,
-- so the hash is never recomputed from this column directly).
CREATE TABLE workflow_versions (
    id           uuid PRIMARY KEY DEFAULT uuidv7(),
    workflow_id  uuid NOT NULL REFERENCES workflows (id) ON DELETE CASCADE,
    version      integer NOT NULL CHECK (version > 0),
    content_hash text NOT NULL,
    document     jsonb NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workflow_id, version),
    UNIQUE (workflow_id, content_hash)
);

-- Execution spine (spec §2). Plan 05: PhaseRun IS the queue — immutable
-- attempt rows claimed FOR UPDATE SKIP LOCKED; no separate jobs table.
-- All FKs RESTRICT: spine rows are history, never cascade-deleted.

CREATE TABLE tasks (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    project_id          uuid NOT NULL REFERENCES projects (id) ON DELETE RESTRICT,
    repo_id             uuid NOT NULL REFERENCES repos (id) ON DELETE RESTRICT,
    workflow_version_id uuid NOT NULL REFERENCES workflow_versions (id) ON DELETE RESTRICT,
    title               text NOT NULL,
    inputs              jsonb NOT NULL DEFAULT '{}'::jsonb,
    requester_id        uuid NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    created_at          timestamptz NOT NULL
);

CREATE INDEX tasks_project_id_idx ON tasks (project_id);

-- A run pins definition hash, policy snapshot (jsonb record), and the
-- denormalized budget/deadline columns every deciding statement
-- predicates on against the DATABASE clock (spec §3 Budgets).
CREATE TABLE runs (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    task_id             uuid NOT NULL REFERENCES tasks (id) ON DELETE RESTRICT,
    status              text NOT NULL CHECK (status IN ('pending', 'running', 'waiting', 'succeeded', 'failed', 'superseded', 'dead')),
    definition_hash     text NOT NULL,
    base_sha            text NOT NULL,
    policy_snapshot     jsonb NOT NULL,
    -- Generations (spec §3): a gate's request_changes rewinds a slice and
    -- opens the next one. Dispatch never reads this — readiness is the
    -- node's NEWEST attempt row — but markers, approval invalidation and
    -- round accounting all do.
    generation          integer NOT NULL DEFAULT 1 CHECK (generation >= 1),
    execution_budget_ms bigint NOT NULL CHECK (execution_budget_ms > 0),
    execution_used_ms   bigint NOT NULL DEFAULT 0 CHECK (execution_used_ms >= 0),
    wall_deadline       timestamptz NOT NULL,
    outcome             jsonb,
    created_at          timestamptz NOT NULL,
    started_at          timestamptz,
    finished_at         timestamptz
);

CREATE INDEX runs_task_id_idx ON runs (task_id);
-- Supersede-before-work: at most one live run per task.
CREATE UNIQUE INDEX runs_task_live_key ON runs (task_id)
    WHERE status IN ('pending', 'running', 'waiting');
-- Serves ListRunsPage's keyset scan: ORDER BY created_at DESC, id DESC with a
-- row-value cursor on the same pair. Filter predicates (status, task_id, and
-- Task 15's workflow/repo) ride the tasks join and do not belong in it.
CREATE INDEX runs_created_id_idx ON runs (created_at DESC, id DESC);

-- Immutable attempt rows (spec §2): one row per (run, node, attempt); at
-- most one non-terminal row per (run, node); a retry is a NEW row
-- inserted in the same tx that terminalizes its predecessor.
CREATE TABLE phase_runs (
    id           uuid PRIMARY KEY DEFAULT uuidv7(),
    run_id       uuid NOT NULL REFERENCES runs (id) ON DELETE RESTRICT,
    node_id      text NOT NULL,
    attempt      integer NOT NULL CHECK (attempt >= 1),
    max_attempts integer NOT NULL CHECK (max_attempts >= 1),
    -- Failures that spent the budget (spec §4.3): cap waits and lease
    -- contention requeue without charging, so the claim predicate bounds
    -- THIS against max_attempts, never the attempt ordinal.
    chargeable_failures integer NOT NULL DEFAULT 0 CHECK (chargeable_failures >= 0),
    -- Earliest moment a parked retry may be claimed (spec §4.4); NULL = now.
    not_before   timestamptz,
    status       text NOT NULL CHECK (status IN ('pending', 'running', 'waiting_decision', 'reconciling', 'succeeded', 'failed', 'dead', 'superseded')),
    generation   integer NOT NULL DEFAULT 1 CHECK (generation >= 1),
    worker_id    text,
    leased_until timestamptz,
    claimed_at   timestamptz,
    finished_at  timestamptz,
    outcome      jsonb,
    created_at   timestamptz NOT NULL,
    UNIQUE (run_id, node_id, attempt)
);

-- A gate awaiting a decision is as live as a running attempt: it must block
-- a second row for its node.
CREATE UNIQUE INDEX phase_runs_live_key ON phase_runs (run_id, node_id)
    WHERE status IN ('pending', 'running', 'waiting_decision', 'reconciling');
CREATE INDEX phase_runs_claim_idx ON phase_runs (created_at) WHERE status = 'pending';
CREATE INDEX phase_runs_lease_idx ON phase_runs (leased_until) WHERE status = 'running';
CREATE INDEX phase_runs_not_before_idx ON phase_runs (not_before)
    WHERE status = 'pending' AND not_before IS NOT NULL;

-- Attempt-scoped durable work products with recorded lineage (spec §2).
-- parent_ids: jsonb array of artifact uuid strings (plan decision 8).
CREATE TABLE artifacts (
    id         uuid PRIMARY KEY,
    run_id     uuid NOT NULL REFERENCES runs (id) ON DELETE RESTRICT,
    node_id    text NOT NULL,
    attempt    integer NOT NULL,
    generation integer NOT NULL DEFAULT 1 CHECK (generation >= 1),
    -- publication and gate_note join log_ref and failure_snapshot as RUNTIME
    -- outputs: they are never edge bindings (the definitions validator only
    -- knows plan/patch/review_verdict), they are produced by the control
    -- plane rather than by a harness, and they are read by the API and by the
    -- revision seed respectively.
    type       text NOT NULL CHECK (type IN ('plan', 'patch', 'review_verdict', 'log_ref', 'failure_snapshot', 'publication', 'gate_note')),
    payload    jsonb NOT NULL,
    parent_ids jsonb NOT NULL DEFAULT '[]'::jsonb,
    created_at timestamptz NOT NULL
);

CREATE INDEX artifacts_run_id_idx ON artifacts (run_id);
-- buildRequest and the lineage reader both want "the newest artifact of this
-- type for this node", which is this index read backwards. The column order
-- mirrors GetNodeArtifact's ORDER BY exactly: selection is by PROVENANCE
-- first — generation, then attempt — and only then by time, because seeds and
-- produced artifacts are stamped from two independent clocks. An index ordered
-- on created_at would no longer serve that query.
CREATE INDEX artifacts_node_lookup_idx ON artifacts (run_id, node_id, type, generation DESC, attempt DESC, created_at DESC, id DESC);

-- Commit-ordered event ids (spec §2): allocated from this single row,
-- locked FOR UPDATE inside the mutation tx. Sequences allocate in
-- non-commit order and would let a tailer cursor skip a late commit;
-- the serialization cost is accepted at V1 scale.
CREATE TABLE run_event_counter (
    onerow  boolean PRIMARY KEY DEFAULT true CHECK (onerow),
    next_id bigint NOT NULL
);

CREATE TABLE run_events (
    id             bigint PRIMARY KEY,
    run_id         uuid NOT NULL REFERENCES runs (id) ON DELETE RESTRICT,
    project_id     uuid NOT NULL,
    kind           text NOT NULL,
    payload        jsonb NOT NULL DEFAULT '{}'::jsonb,
    occurred_at    timestamptz NOT NULL,
    correlation_id text NOT NULL DEFAULT ''
);

CREATE INDEX run_events_run_id_idx ON run_events (run_id, id);
CREATE INDEX run_events_project_id_idx ON run_events (project_id, id);

-- Same-tx outbox envelope (spec §2): one row per run event, no consumer
-- in V1; the retention sweeper prunes by age — never an immortal table.
CREATE TABLE outbox (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    event_id   bigint NOT NULL REFERENCES run_events (id) ON DELETE CASCADE,
    kind       text NOT NULL,
    payload    jsonb NOT NULL,
    created_at timestamptz NOT NULL
);

CREATE INDEX outbox_created_at_idx ON outbox (created_at);

-- A gate's decision (spec §2 Gate). One row per gate ATTEMPT: a revision
-- rematerializes the gate as a new attempt row, which is decidable again,
-- while the unique key makes deciding the SAME row twice impossible — that
-- uniqueness IS the API's concurrency answer, not a convention.
CREATE TABLE gate_decisions (
    id                   uuid PRIMARY KEY DEFAULT uuidv7(),
    phase_run_id         uuid NOT NULL UNIQUE REFERENCES phase_runs (id) ON DELETE RESTRICT,
    run_id               uuid NOT NULL REFERENCES runs (id) ON DELETE RESTRICT,
    node_id              text NOT NULL,
    generation           integer NOT NULL CHECK (generation >= 1),
    decision             text NOT NULL CHECK (decision IN ('approve', 'request_changes', 'reject')),
    -- A SERVER-SELECTED EVIDENCE BINDING, not a record of human examination.
    -- DecideGate resolves it from the run's pinned definition
    -- (ExecPlan.ExaminedInput) immediately before the stamp, whether or not
    -- anybody looked at it — so it names the artifact the decision was ABOUT.
    -- Approval authorizes exactly this id and nothing newer (spec §3 artifact
    -- flow). The system's real guarantee is that publication waits for a
    -- person to act, which holds wherever that person acts from; see Plan 12a
    -- §10.2 for why no reachable design closes the gap between "evidence
    -- rendered" and "evidence read".
    examined_artifact_id uuid NOT NULL REFERENCES artifacts (id) ON DELETE RESTRICT,
    note                 text NOT NULL DEFAULT '',
    acted_by             uuid NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    -- Marked distinctly because the policy permits it, not because it is
    -- ordinary (spec §6).
    self_approval        boolean NOT NULL DEFAULT false,
    acted_at             timestamptz NOT NULL
);

CREATE INDEX gate_decisions_run_node_idx ON gate_decisions (run_id, node_id);

-- The single-use ApprovedPatch authorization (spec §2): the only token
-- git-publish can consume, scoped to the one publish node its gate
-- dominates. Every field here is re-validated at consumption; a mismatch
-- aborts before any forge call.
CREATE TABLE approved_patches (
    id               uuid PRIMARY KEY DEFAULT uuidv7(),
    run_id           uuid NOT NULL REFERENCES runs (id) ON DELETE RESTRICT,
    gate_node_id     text NOT NULL,
    publish_node_id  text NOT NULL,
    gate_decision_id uuid NOT NULL UNIQUE REFERENCES gate_decisions (id) ON DELETE RESTRICT,
    artifact_id      uuid NOT NULL REFERENCES artifacts (id) ON DELETE RESTRICT,
    base_sha         text NOT NULL,
    tree_hash        text NOT NULL,
    repo_id          uuid NOT NULL REFERENCES repos (id) ON DELETE RESTRICT,
    -- The repo's coordinates AS APPROVED. repo_id alone is not an identity:
    -- UpdateRepo can change a repo's slug, default branch and credential
    -- mid-run (assets/repos.go), so the row the id points at is not
    -- necessarily the repository the human approved publishing to. Pinning
    -- them here means the publish path can prove the destination has not
    -- moved, and a rename becomes a refusal instead of a patch delivered to
    -- the wrong repository.
    forge_kind       text NOT NULL,
    repo_slug        text NOT NULL,
    target_branch    text NOT NULL,
    generation       integer NOT NULL CHECK (generation >= 1),
    status           text NOT NULL CHECK (status IN ('valid', 'consumed', 'invalidated')),
    created_at       timestamptz NOT NULL,
    settled_at       timestamptz
);

-- At most one LIVE authorization per publish node per run. A newer patch
-- generation invalidates the prior one (spec §2), and this index is what
-- makes "invalidate then mint" impossible to get half-done.
CREATE UNIQUE INDEX approved_patches_live_key ON approved_patches (run_id, publish_node_id)
    WHERE status = 'valid';

-- The git-publish effect state machine (spec §3 Publishing). The row commits
-- BEFORE the first forge call; every effect records its receipt as it lands,
-- so a retry always distinguishes "never happened" from "succeeded, receipt
-- lost". One intent per (run, publish node) — resumed across attempts, never
-- re-minted, because a second intent would mean a second branch.
CREATE TABLE publish_intents (
    id               uuid PRIMARY KEY DEFAULT uuidv7(),
    run_id           uuid NOT NULL REFERENCES runs (id) ON DELETE RESTRICT,
    node_id          text NOT NULL,
    -- The attempt that created the intent: provenance, not identity.
    attempt          integer NOT NULL CHECK (attempt >= 1),
    -- UNIQUE is the single-use guarantee in its strongest form: a second
    -- consumption of the same authorization is a constraint violation, not a
    -- lost CAS (invariant 2, Postgres is the authority).
    authorization_id uuid NOT NULL UNIQUE REFERENCES approved_patches (id) ON DELETE RESTRICT,
    repo_id          uuid NOT NULL REFERENCES repos (id) ON DELETE RESTRICT,
    -- Copied from the authorization at creation, so every later attempt and
    -- every recovery probe addresses the repository the approval named — not
    -- whatever the repos table says today. The credential rides along for the
    -- same reason: UpdateRepo can repoint a repo at a different one, and a
    -- resumed publication must present the credential its approval implied.
    forge_kind       text NOT NULL,
    repo_slug        text NOT NULL,
    credential_id    uuid NOT NULL REFERENCES credentials (id) ON DELETE RESTRICT,
    -- The deterministic markers recovery reconstructs from the authorization.
    branch           text NOT NULL,
    target_branch    text NOT NULL,
    base_sha         text NOT NULL,
    tree_hash        text NOT NULL,
    title            text NOT NULL,
    body             text NOT NULL,
    -- The commit's author identity, snapshotted when the intent is created.
    --
    -- It cannot be re-resolved per attempt. A commit sha hashes the author
    -- identity, and recovery proves a receiptless branch is ours by rebuilding
    -- the SAME sha (spec §3). The requester's address lives on a mutable users
    -- row and the orchestrator caches it per process, so a restart — or an
    -- address change between two attempts — would rebuild a DIFFERENT commit,
    -- the adopt comparison would miss, and a crash-after-push would become a
    -- permanent publish_conflict on a branch that is genuinely ours.
    author_email     text NOT NULL,
    -- The commit message, rendered once and stored, for the same reason
    -- author_email is: it is hashed into the commit sha. It interpolates the
    -- APPROVER's address, which is joined from the mutable users row, so
    -- re-rendering it per attempt would rebuild a different commit and break
    -- the adopt comparison recovery depends on. Storing the finished string
    -- also means the settlement rebuild in a later task cannot drift from
    -- what was pushed: there is no second renderer to disagree with.
    commit_message   text NOT NULL,
    status           text NOT NULL CHECK (status IN ('pending', 'pushed', 'published', 'halted')),
    -- Receipts, each written in the transaction after its effect landed.
    head_sha         text,
    pr_number        bigint,
    pr_url           text,
    halt_reason      text,
    -- When settlement first found the marker missing. A single absent probe
    -- cannot settle an intent — a request issued just before the deadline may
    -- still be in flight — so absence must persist across ticks before it
    -- means "nothing landed". Cleared the moment a probe finds the marker.
    absent_since     timestamptz,
    created_at       timestamptz NOT NULL,
    updated_at       timestamptz NOT NULL,
    UNIQUE (run_id, node_id)
);

-- Two publications can never target the same branch on the same repo: the
-- marker is derived from the approved artifact id, so a collision would mean
-- two authorizations over one artifact, which approved_patches_live_key
-- already forbids. Belt and braces, because the consequence is a silent
-- overwrite of somebody else's ref.
CREATE UNIQUE INDEX publish_intents_marker_key ON publish_intents (repo_id, branch);
