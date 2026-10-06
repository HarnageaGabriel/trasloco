# ADR 0006: Ledger identity, intents and lifecycle

Status: Accepted — 2026-10-06. Source: architecture review of M0 + M1 wave 1.

## Context
Resources were keyed by `(run_id, resource_key)`, so a resume or rollback run could not see the previous run's resources and there was no place for old to new ID maps. A crash during a non-idempotent POST left no trace.

## Decision
- Resources are keyed by `(migration_id, resource_key)`; a migration is the identity of a plan lineage. Runs are attempts referencing a migration.
- Before every mutation an `intent` row is written (resource, operation, request fingerprint, attempt id); after the response, the outcome. On restart, intents without an outcome are reconciled by reading the target before any retry.
- Old to new ID mapping table (for reference rewriting, M7).
- Timestamps are INTEGER unix nanoseconds. Migrations are versioned by file name and gaps fail.
- A process lock prevents two runs on the same output directory. WAL and SHM files are created 0600.
- Reads are streamed (`iter.Seq2`) with keyset pagination.

## Consequences
Implemented before M3/M4 (apply). Schema v2 replaces v1; no production data exists yet.
