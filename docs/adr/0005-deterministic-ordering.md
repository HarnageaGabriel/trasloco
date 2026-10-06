# ADR 0005: Deterministic ordering everywhere

Status: Accepted — 2026-10-06. Source: architecture review of M0 + M1 wave 1.

## Context
Map iteration produced nondeterministic diagnostics, JSON and template expansion.

## Decision
- No Go map iteration in any path whose output is observable (files, plans, reports, events, diagnostics, digests). Use ordered collections (slice of key/value) where order is meaningful; sort keys where it is not.
- Source order is preserved for anything that came from user files (YAML mappings, parameters, variables).
- A lint/test helper flags `range` over maps in packages `ir`, `dialect/...`, `event`, `report`, `plan` unless annotated `//trasloco:unordered-ok <reason>`.

## Consequences
Reproducible plans and goldens; plan digests are stable across runs.
