# ADR 0003: One source position model

Status: Accepted — 2026-10-06. Source: architecture review of M0 + M1 wave 1.

## Context
Positions existed as `diag.Source`, `ir.Origin`, `ir.Location` and `expr.Span` (byte offsets inside an unwrapped expression with no mapping back to the file).

## Decision
- `diag.Source{Platform, File, Line, Col, EndLine, EndCol, Offset, EndOffset}` is the only position type.
- `ir.Origin` = `{Source diag.Source; Chain []diag.Source; OriginalTextHash string}`, where `Chain` is the template instantiation chain (outermost first).
- Expression spans are mapped to file positions by `yamlx`, which knows each scalar's style (plain, quoted, folded, literal) and indentation. `expr` stays I/O-free and returns offsets relative to its input; the dialect layer converts them through a `yamlx` mapping function.
- Every diagnostic emitted for an IR node uses that node's origin, never the pipeline's.

## Consequences
Precise, clickable diagnostics through templates; requires a scalar-offset mapper in yamlx.
