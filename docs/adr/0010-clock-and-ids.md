# ADR 0010: Clock and identifiers

Status: Accepted — 2026-10-06. Source: architecture review of M0 + M1 wave 1.

## Context
Several packages had their own clock interface while others called `time.Now()` directly. IDs had no shared type or generation rule.

## Decision
- One `internal/clock` package (`Clock{Now, Sleep(ctx, d), NewTimer}`) injected everywhere; no `time.Now()` outside it (lint check).
- Plan entities (units, resources, changes) have deterministic IDs: a hash of their identity (platform, scope, kind, natural key), so plans are reproducible.
- Runs and attempts have time-sortable random IDs (timestamp prefix + crypto/rand).
- Each ID kind has its own named type.

## Consequences
Deterministic tests and plans; IDs are safe to log.
