# ADR 0008: Response cache backend

Status: Accepted — 2026-10-06. Source: architecture review of M0 + M1 wave 1.

## Context
The cache index was one JSON file rewritten on every write, with O(N squared) eviction and disk I/O under a global lock.

## Decision
- Index in SQLite (pure Go `modernc.org/sqlite`, same as the ledger), content in a content-addressed store `cas/<2 hex>/<sha256>`.
- CAS writes happen outside locks (temp file + rename); the index records access time in batches; eviction by indexed access time with reference counts.
- Only an allowlist of response headers is stored (no Set-Cookie, no X-VSS-UserData). Directories 0700, files 0600.
- `output.encryptCache` is rejected by config validation until implemented.

## Consequences
Implemented before M3. Scales to millions of entries; safe across processes.
