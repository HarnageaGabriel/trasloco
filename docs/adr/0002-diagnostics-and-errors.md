# ADR 0002: One diagnostics and error model

Status: Accepted — 2026-10-06. Source: architecture review of M0 + M1 wave 1.

## Context
Findings were represented five ways (RegistrationError, EvalError, SyntaxError with code baked into Error(), sentinel errors, raw []Diagnostic). `diag.New(code, ...any)` dispatched on argument types. Failures to build a diagnostic were handled three different ways.

## Decision
- `diag.Diagnostic` is the only representation of a finding. The registry (`internal/diag/registry`) is the single source of truth; docs pages are generated from it.
- API: `diag.New(code string, opts ...diag.Option) (Diagnostic, error)` and `diag.MustNew` for static codes. Options are typed (`WithSource`, `WithArgs`, `WithResource`, `WithFidelity`, `WithRedactor`, ...). No `any` dispatch.
- Errors that carry a finding use `*diag.Error` (wraps one Diagnostic, supports `errors.As`). No sentinel errors with codes in the message.
- Every producer returns or appends to a `*diag.Collector`, which dedups, caps per code (1000) and sorts deterministically (source position, then code).
- `ir` uses `diag.Fidelity` and `diag.Provenance`; duplicate types are deleted.
- A test asserts every `TRL-` literal in the code is registered and every registered code is referenced (code or spec).

## Consequences
One-time refactor of existing packages; afterwards consistent diagnostics in reports, events and exit codes.
