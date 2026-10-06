CREATE TABLE runs (
    id TEXT PRIMARY KEY,
    command TEXT NOT NULL,
    status TEXT NOT NULL,
    started_at TEXT NOT NULL,
    finished_at TEXT
);

CREATE TABLE units (
    run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    unit_key TEXT NOT NULL,
    kind TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'planned',
    PRIMARY KEY (run_id, unit_key)
);

CREATE TABLE resources (
    run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    unit_key TEXT,
    resource_key TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('planned','applying','applied','failed','skipped','rolledback')),
    retryable INTEGER NOT NULL DEFAULT 0 CHECK (retryable IN (0,1)),
    failure_reason TEXT,
    previous_value BLOB CHECK (previous_value IS NULL OR json_valid(previous_value)),
    target_id TEXT,
    etag TEXT,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (run_id, resource_key),
    FOREIGN KEY (run_id, unit_key) REFERENCES units(run_id, unit_key)
);

CREATE TABLE operations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id TEXT NOT NULL,
    resource_key TEXT NOT NULL,
    previous_state TEXT,
    new_state TEXT NOT NULL,
    retryable INTEGER NOT NULL DEFAULT 0,
    reason TEXT,
    occurred_at TEXT NOT NULL,
    FOREIGN KEY (run_id, resource_key) REFERENCES resources(run_id, resource_key)
);

CREATE INDEX resources_state_idx ON resources(run_id, state, resource_key);
CREATE INDEX operations_resource_idx ON operations(run_id, resource_key, id);
