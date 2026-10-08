-- Ledger schema v2 (ADR 0006). All timestamps are INTEGER unix nanoseconds.

CREATE TABLE migrations (
    id TEXT PRIMARY KEY,
    created_at INTEGER NOT NULL
);

CREATE TABLE runs (
    id TEXT PRIMARY KEY,
    migration_id TEXT NOT NULL REFERENCES migrations(id),
    command TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('running','succeeded','failed','partial','interrupted')),
    started_at INTEGER NOT NULL,
    finished_at INTEGER
);

CREATE INDEX runs_migration_idx ON runs(migration_id, started_at, id);

CREATE TABLE units (
    migration_id TEXT NOT NULL REFERENCES migrations(id),
    unit_key TEXT NOT NULL,
    kind TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'planned' CHECK (state IN ('planned','running','done','failed','skipped')),
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (migration_id, unit_key)
);

CREATE TABLE resources (
    migration_id TEXT NOT NULL REFERENCES migrations(id),
    resource_key TEXT NOT NULL,
    unit_key TEXT,
    state TEXT NOT NULL CHECK (state IN ('planned','applying','applied','failed','skipped','rolledback')),
    retryable INTEGER NOT NULL DEFAULT 0 CHECK (retryable IN (0,1)),
    failure_reason TEXT,
    previous_value BLOB CHECK (previous_value IS NULL OR json_valid(previous_value)),
    target_id TEXT,
    etag TEXT,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (migration_id, resource_key),
    FOREIGN KEY (migration_id, unit_key) REFERENCES units(migration_id, unit_key)
) WITHOUT ROWID;

CREATE INDEX resources_state_idx ON resources(migration_id, state, resource_key);

CREATE TABLE operations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    migration_id TEXT NOT NULL,
    resource_key TEXT NOT NULL,
    run_id TEXT NOT NULL REFERENCES runs(id),
    previous_state TEXT,
    new_state TEXT NOT NULL,
    retryable INTEGER NOT NULL DEFAULT 0,
    reason TEXT,
    occurred_at INTEGER NOT NULL,
    FOREIGN KEY (migration_id, resource_key) REFERENCES resources(migration_id, resource_key)
);

CREATE INDEX operations_resource_idx ON operations(migration_id, resource_key, id);

-- An intent is written before each mutation; its outcome after the response.
-- An intent without an outcome row must be reconciled against the target.
CREATE TABLE intents (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    migration_id TEXT NOT NULL,
    resource_key TEXT NOT NULL,
    run_id TEXT NOT NULL REFERENCES runs(id),
    operation TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    FOREIGN KEY (migration_id, resource_key) REFERENCES resources(migration_id, resource_key)
);

CREATE INDEX intents_resource_idx ON intents(migration_id, resource_key, id);

CREATE TABLE outcomes (
    intent_id INTEGER PRIMARY KEY REFERENCES intents(id),
    status TEXT NOT NULL CHECK (status IN ('succeeded','failed','absent')),
    target_id TEXT,
    detail TEXT,
    recorded_at INTEGER NOT NULL
);

CREATE TABLE id_map (
    migration_id TEXT NOT NULL REFERENCES migrations(id),
    kind TEXT NOT NULL,
    old_id TEXT NOT NULL,
    new_id TEXT NOT NULL,
    recorded_at INTEGER NOT NULL,
    PRIMARY KEY (migration_id, kind, old_id)
) WITHOUT ROWID;

PRAGMA user_version = 2;
