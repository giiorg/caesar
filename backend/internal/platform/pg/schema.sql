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
    id          uuid PRIMARY KEY,
    name        text NOT NULL UNIQUE,
    kind        text NOT NULL CHECK (kind IN ('forge_token', 'model_key')),
    key_id      text NOT NULL,
    wrapped_dek bytea NOT NULL,
    ciphertext  bytea NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
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
