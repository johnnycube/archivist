-- +goose NO TRANSACTION
-- +goose Up
-- (Migration-runner annotations. This file carries its own BEGIN/COMMIT, so the
-- runner must not wrap it in a second transaction. No Down section: migrations
-- are forward-only.)
--
-- Archivist initial schema (PostgreSQL 16).
--
-- The in-memory store (internal/store) mirrors this for offline dev/tests; this
-- file is the production source of truth. Multi-tenant: every user-owned row
-- carries user_id and is guarded by Row-Level Security (defense in depth on top
-- of app-level scoping).

BEGIN;

-- gen_random_uuid() is built into PostgreSQL 13+; this file used to create
-- the pgcrypto extension for it, which a role without CREATE on its database
-- may not do. Removed before the first public release — the one edit of an applied migration, and
-- harmless to databases that ran the old version (ADR 0010).

-- ---------------------------------------------------------------------------
-- Tenancy & connections
-- ---------------------------------------------------------------------------
CREATE TABLE users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email         text NOT NULL UNIQUE,
    password_hash text NOT NULL,             -- Argon2id
    mfa_secret    bytea,                     -- TOTP, encrypted at rest
    created_at    timestamptz NOT NULL DEFAULT now()
);

-- Each row is one connected account ("connection"). Multiple accounts per
-- source is just multiple rows with the same source_id.
CREATE TABLE accounts (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id        uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    source_id      text NOT NULL,            -- 'reddit', 'twitter', ...
    label          text NOT NULL DEFAULT '', -- user-chosen, e.g. 'work ChatGPT'
    status         text NOT NULL DEFAULT 'active',
    last_synced_at timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ON accounts (user_id, source_id);

-- Encrypted credentials (envelope encryption; KEK never in the DB).
CREATE TABLE encryption_keys (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    wrapped_dek  bytea NOT NULL,             -- per-user DEK, wrapped by the KEK
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE secrets (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    dek_id     uuid NOT NULL REFERENCES encryption_keys(id),
    ciphertext bytea NOT NULL,               -- AES-256-GCM
    nonce      bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------------------
-- Ingestion provenance
-- ---------------------------------------------------------------------------
CREATE TABLE import_bundles (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    blob_key   text NOT NULL,                -- raw ZIP in object storage
    coverage   jsonb NOT NULL DEFAULT '{}',  -- the coverage report
    status     text NOT NULL DEFAULT 'pending',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sync_runs (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id     uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    mode           text NOT NULL,            -- 'full' | 'incremental' | 'bundle'
    cursor         bytea,
    state          text NOT NULL DEFAULT 'queued',
    items_ingested int  NOT NULL DEFAULT 0,
    started_at     timestamptz,
    finished_at    timestamptz
);

-- Raw, immutable payloads — normalization runs from here (reprocessable).
CREATE TABLE raw_records (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id   uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    bundle_id    uuid REFERENCES import_bundles(id) ON DELETE SET NULL,
    external_id  text,
    kind         text,
    member       text,                       -- archive member path, if any
    occurred_at  timestamptz,
    payload      jsonb NOT NULL,
    content_hash text NOT NULL,
    UNIQUE (account_id, content_hash)        -- dedupe
);

-- Background import queue (drained with FOR UPDATE SKIP LOCKED).
CREATE TABLE import_jobs (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    source     text NOT NULL,
    label      text NOT NULL DEFAULT 'default',
    zip_path   text NOT NULL DEFAULT '',
    status     text NOT NULL DEFAULT 'queued',   -- queued|running|done|error
    saved      int  NOT NULL DEFAULT 0,
    coverage   jsonb,
    error      text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX import_jobs_user_idx  ON import_jobs (user_id, created_at DESC);
CREATE INDEX import_jobs_queue_idx ON import_jobs (status, created_at) WHERE status = 'queued';

-- ---------------------------------------------------------------------------
-- Canonical events (the timeline)
-- ---------------------------------------------------------------------------
CREATE TABLE threads (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id  uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    external_id text NOT NULL,
    title       text NOT NULL DEFAULT '',
    UNIQUE (account_id, external_id)
);

CREATE TABLE events (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id           uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    account_id        uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    raw_record_id     uuid REFERENCES raw_records(id) ON DELETE SET NULL,
    source_id         text NOT NULL,
    external_id       text NOT NULL,
    type              text NOT NULL,          -- 'reddit.comment', 'twitter.like'
    category          text NOT NULL,          -- content|reaction|save|...
    occurred_at       timestamptz,
    actor             text,
    title             text NOT NULL DEFAULT '',
    body              text NOT NULL DEFAULT '',
    thread_id         uuid REFERENCES threads(id) ON DELETE SET NULL,
    thread_external_id text,                   -- raw thread key before thread normalization
    parent_external_id text,
    metadata          jsonb NOT NULL DEFAULT '{}',
    search_vector     tsvector,
    ingested_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, source_id, external_id)  -- upsert key
);

-- Timeline is keyset-paginated on (occurred_at desc, id desc).
CREATE INDEX events_timeline_idx ON events (user_id, occurred_at DESC, id DESC);
CREATE INDEX events_source_idx   ON events (user_id, source_id);
CREATE INDEX events_category_idx ON events (user_id, category);
CREATE INDEX events_type_idx     ON events (user_id, type);
CREATE INDEX events_fts_idx      ON events USING gin (search_vector);

CREATE TABLE event_targets (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    event_id           uuid NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    relation           text NOT NULL,         -- liked|voted_up|saved|followed|...
    target_source      text,
    target_external_id text,
    target_url         text
);
CREATE INDEX event_targets_event_idx    ON event_targets (event_id);
CREATE INDEX event_targets_relation_idx ON event_targets (relation);

CREATE TABLE media (
    id       uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    event_id uuid NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    blob_key text NOT NULL,
    mime     text,
    url      text
);

CREATE TABLE tags (
    id      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name    text NOT NULL,
    UNIQUE (user_id, name)
);
CREATE TABLE event_tags (
    event_id uuid NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    tag_id   uuid NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    PRIMARY KEY (event_id, tag_id)
);

-- Maintain the FTS vector from title + body.
-- +goose StatementBegin
CREATE FUNCTION events_tsv() RETURNS trigger AS $$
BEGIN
    NEW.search_vector :=
        setweight(to_tsvector('simple', coalesce(NEW.title, '')), 'A') ||
        setweight(to_tsvector('simple', coalesce(NEW.body, '')),  'B');
    RETURN NEW;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER events_tsv_trg
    BEFORE INSERT OR UPDATE OF title, body ON events
    FOR EACH ROW EXECUTE FUNCTION events_tsv();

-- ---------------------------------------------------------------------------
-- Row-Level Security (defense in depth; app sets app.user_id per connection)
-- ---------------------------------------------------------------------------
ALTER TABLE events        ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounts      ENABLE ROW LEVEL SECURITY;
ALTER TABLE event_targets ENABLE ROW LEVEL SECURITY;

CREATE POLICY events_tenant   ON events
    USING (user_id = current_setting('app.user_id', true)::uuid);
CREATE POLICY accounts_tenant ON accounts
    USING (user_id = current_setting('app.user_id', true)::uuid);
CREATE POLICY targets_tenant  ON event_targets
    USING (event_id IN (SELECT id FROM events));

COMMIT;
