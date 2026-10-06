# ADR 0004: Azure Pipelines expression value model

Status: Accepted — 2026-10-06. Source: architecture review of M0 + M1 wave 1.

## Context
Values were Go `any` plus reflection over maps, so object order was lost and conversions diverged from the documented .NET-based behaviour (number formatting, string to bool/number, version comparison, missing keys).

## Decision
- Typed value model: `Null`, `Bool`, `Number` (decimal, exact for integers, formatted without exponent), `String`, `Version` (System.Version semantics: undefined component = -1), `Array`, `Object` (ordered).
- Conversions follow the "Type casting" table of the Microsoft docs exactly; anything undocumented follows the open reference lineage (actions/runner DTExpressions2) and is marked as such.
- Macro `$(name)` only matches valid variable names; anything else is literal text, never an error.
- Resource limits (nesting depth, expression length) mirror Microsoft's documented limits.
- Test vectors are authored from the documentation by a different agent than the implementer, and every vector carries a `source` (URL or reference path). Vectors without a source are rejected in review.

## Consequences
Deterministic, faithful evaluation; the template resolver (M1 wave 3) can rely on ordered objects.
