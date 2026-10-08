# Ledger v2

The ledger is the durable boundary for apply, resume, reconciliation, and rollback. ADR 0006 defines its identity and crash-safety rules.

## Identity and schema

A migration identifies one plan lineage. A run is one attempt and references its migration. Resources use `(migration_id, resource_key)` as their primary key, so resume and rollback attempts see state written by earlier runs. `id_map` stores source-to-target identifiers by migration, kind, and old identifier.

Schema files are embedded from `internal/ledger/migrations/NNN_name.sql`. Names must begin at `001` and remain contiguous; invalid names, gaps, applied gaps, newer versions, and a non-v2 schema identity fail with `TRL-APPLY-0010`. Schema v2 replaces v1 because no production ledger data exists. Every persisted timestamp is an INTEGER Unix nanosecond value.

The output directory is mode `0700`. `ledger.db`, `ledger.db-wal`, `ledger.db-shm`, and `ledger.lock` are mode `0600`. The store holds an OS-enforced exclusive lock on `ledger.lock` for its full lifetime; the kernel releases it if the process exits. The file is never removed on release, because unlinking a locked file would permit a second inode and a second owner. Its PID text is informational only: a leftover dead or malformed PID does not block a new owner, and PID liveness is never used to establish ownership.

## Lifecycles

Runs start as `running` through `BeginRun` and finish once as `succeeded`, `failed`, `partial`, or `interrupted` through `FinishRun`.

Units follow:

```text
planned -> running -> done
   |          |
   v          v
 skipped    failed -> running
```

Resources follow:

```text
planned -> applying -> applied -> rolledback
   |           |
   v           v
 skipped     failed -> applying  (only when retryable)
```

Failed and skipped transitions require a reason. State changes and their operation-log rows commit atomically. Batch resource insertion and transition methods are also atomic.

## Mutation intent and reconciliation

The caller must commit `RecordIntent` before sending every target mutation. The row contains the migration resource, operation, canonical request fingerprint, and run attempt. After a target response, the caller commits `RecordOutcome` with `succeeded`, `failed`, or `absent`.

An intent without an outcome means the process may have crashed after the target received the request. Before retrying, callers stream `ListPendingIntents`, read the target, and record the observed outcome. `RequireNoPendingIntents` returns `TRL-APPLY-0008` while reconciliation is required. In the same write transaction, a transition to `applying` and `RecordIntent` also reject a resource with a pending intent using `TRL-APPLY-0008`. `RecordOutcome` rejects an intent that already has an outcome. An `applying` resource can then advance to `applied` or `failed`; it must not be blindly retried.

`previous_value` is rollback JSON, not an audit payload. Invalid JSON is rejected. Values explicitly flagged secret, or matching a secret registered with the run redactor, are rejected with `TRL-APPLY-0009` and are never persisted.

## Streaming and ordering

`ListByState`, `ListPendingIntents`, and `ListIDMappings` return `iter.Seq2` streams. Each uses stable keyset pagination, never an offset or a full-result allocation. Resource keys order resource reads; intent IDs order reconciliation; `(kind, old_id)` orders identifier mappings. Consumers may stop a stream early.

## Context contract

Cancellation before a write enters the writer queue returns the original context error and persists nothing. Once enqueued, the writer removes cancellation and deadline signals, completes the transaction, and only then returns its commit result. The caller therefore never receives a cancellation result with an ambiguous commit outcome. Context values remain available to the queued operation. Reads retain ordinary context cancellation semantics.

## Load test

Run the opt-in million-resource benchmark with:

```text
go test ./internal/ledger -run '^$' -bench '^BenchmarkMillionResources$' -benchtime=1x
```

It inserts and transitions 1,000,000 resources in batches, streams the full keyset listing, and reports `wall_s`, `db_bytes`, and `peak_rss_bytes`.

Measured on Windows/amd64 on 2026-10-09: `wall_s=100.2`, `db_bytes=230363136`, `peak_rss_bytes=68718592`.
