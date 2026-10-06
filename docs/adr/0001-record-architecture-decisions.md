# ADR 0001: Record architecture decisions

Status: Accepted — 2026-10-06. Source: architecture review of M0 + M1 wave 1.

## Context
Most code is written by AI agents working in parallel on separate packages. Without written decisions they drift (the first review found four position models and five error representations).

## Decision
- Every cross-cutting decision lives in `docs/adr/NNNN-title.md` (Context, Decision, Consequences).
- ADRs are immutable once accepted; changes create a new ADR that supersedes the old one.
- Agents must read `CONVENTIONS.md` and every ADR relevant to their package before writing code. Reviews check compliance.

## Consequences
Small overhead per decision; consistent code across agents; reviewers can cite ADR numbers.
