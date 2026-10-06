# Engine event stream

Status: draft for approval (M0). Normative.

The headless engine exposes one typed append-only event stream consumed by the CLI and future UIs. JSONL is the only wire encoding through 1.0.

## Envelope

Every line is one UTF-8 JSON object followed by LF. Producers MUST emit compact JSON with no embedded literal newlines. Consumers MUST ignore object-key order.

| Field | Type | Required | Meaning |
|---|---|---|---|
| `version` | integer | yes | Event wire schema version; starts at 1. |
| `sequence` | integer | yes | Run-global, starts at 1, increments by exactly 1. |
| `time` | RFC 3339 string | yes | UTC timestamp with at least millisecond precision; informational, not ordering. |
| `runId` | string | yes | Opaque unique run identifier. |
| `type` | enum | yes | Event type below. |
| `phase` | enum or null | yes | Current version-1 phase ID. |
| `unitKey` | string or null | yes | Stable unit/shared-unit key. |
| `operationId` | string or null | yes | Stable planned operation ID when applicable. |
| `attempt` | integer | yes | At least 1. Retries of one operation increment it; events without an operation use 1. |
| `payload` | object | yes | Type-specific fields. |

Unknown top-level fields are invalid for version 1. Payloads MAY contain type-specific extension fields only under `payload.extensions`. Event schema version is independent from plan/report versions.

Version 1 phases form a closed, versioned enum: `initialize|discover|audit|convert|modernize|plan|apply|verify|rollback|cutover|report|finalize`. Adding a phase requires an event-version change. `PhaseStarted` and `PhaseFinished` MUST use one of these values and their envelope `phase` MUST equal `payload.phaseId`.

## Event types

| Type | Required payload | Contract |
|---|---|---|
| `RunStarted` | `command`, `toolVersion`, `configDigest` | First event. Exactly once. |
| `PhaseStarted` | `phaseId`, `name`, `ordinal` | Opens a phase. |
| `PhaseFinished` | `phaseId`, `outcome`, `counts` | Closes a started phase. |
| `UnitStarted` | `kind`, `resourceRef` | Opens work for one unit. |
| `UnitFinished` | `outcome`, `counts`, `fidelity`, `provenance` | Closes a started unit; provenance is `deterministic|llm`. |
| `RequestThrottled` | `platform`, `endpointClass`, `delayMs`, `reason` | Reports scheduled rate-limit delay; no credentials/URL query. |
| `CacheHit` | `cacheKind`, `keyDigest` | Reports content-addressed reuse; digest only. |
| `DiagnosticEmitted` | `diagnostic` | Contains the diagnostic object from `diagnostics.md`. |
| `ChangePlanned` | `changeId`, `action`, `resourceRef`, `digest` | One canonical planned change. No before/after values. |
| `ChangeApplied` | `changeId`, `action`, `resourceRef`, `resultRef` | Mutation verified or accepted by endpoint. |
| `ChangeFailed` | `changeId`, `resourceRef`, `retryable`, `diagnosticCode` | Terminal attempt failure for that operation. |
| `ChangeSkipped` | `changeId`, `reason`, `prerequisiteId` | Not attempted because no-op or prerequisite failure. |
| `ProgressTick` | `completed`, `total`, `unit` | Coalescible progress snapshot; `total` may be null. Consumers calculate rates from sequence/time samples. |
| `RollbackStarted` | `sourceRunId` | Opens compensating rollback. |
| `RollbackApplied` | `changeId`, `result` | One compensation completed. |
| `RunFinished` | `outcome`, `exitCode`, `counts`, `reportRef` | Last event. Exactly once for orderly shutdown. |

`outcome` is `succeeded|partial|blocked|failed|stale|cancelled`. Counts are objects containing non-negative integers and MUST be cumulative at their level.

## Ordering and concurrency

The engine assigns `sequence` at one serialization boundary after concurrent work. File order and sequence order MUST agree. The following happens-before constraints apply:

1. `RunStarted` precedes all events.
2. `PhaseStarted(P)` precedes any event whose `phase` is P; `PhaseFinished(P)` follows them.
3. `UnitStarted(U)` precedes unit events and `UnitFinished(U)` follows them.
4. `ChangePlanned(C)` precedes `ChangeApplied(C)`, `ChangeFailed(C)`, or `ChangeSkipped(C)` in a plan+apply stream.
5. At most one terminal change event exists per `(operationId, attempt)`. Resume increments the required first-class `attempt` field.
6. `RunFinished` follows all engine events and is last.

No ordering is guaranteed between different concurrently processed units beyond sequence serialization. Consumers MUST NOT infer dependency from adjacency or timestamps; use plan operation IDs. Within one unit, semantic operation dependency order MUST be reflected in sequence.

Backpressure MUST slow producers rather than drop semantic events. Only `ProgressTick`, `CacheHit`, and repeated `RequestThrottled` events MAY be coalesced. Diagnostics and change events MUST never be dropped. On abrupt process termination, the JSONL file may lack `RunFinished`; the last complete LF-terminated line remains valid and resume consults the ledger, not event inference.

## Redaction

Redaction occurs before sequence assignment and serialization. No event may contain credentials, authorization headers, cookies, secret values, raw secret digests, signed URLs, URL query strings, identity emails/display names unless explicitly user-supplied for reporting, or arbitrary HTTP bodies. `keyDigest` uses a domain-separated digest of a non-secret cache key. Messages pass the same canary redactor as diagnostics.

Resource references MUST omit embedded credentials and sensitive query parameters. `resultRef` and `reportRef` are safe relative paths or sanitized web URLs. If safe representation is impossible, emit a stable opaque ID. Redaction failure is fatal (`TRL-SECRET-0004`); the unsafe event MUST NOT be written.

## Version negotiation

Version 1 consumers MUST reject other versions unless invoked with a documented pass-through mode. New optional event types require a wire-version increment if old consumers would fail; therefore consumers SHOULD offer `--ignore-unknown-event-types` only for display, never for audit reconstruction.

## Example

```json
{"version":1,"sequence":1,"time":"2026-10-06T12:00:00.000Z","runId":"run-01","type":"RunStarted","phase":null,"unitKey":null,"operationId":null,"attempt":1,"payload":{"command":"plan","toolVersion":"0.1.0","configDigest":"sha256:abc"}}
{"version":1,"sequence":2,"time":"2026-10-06T12:00:00.010Z","runId":"run-01","type":"PhaseStarted","phase":"discover","unitKey":null,"operationId":null,"attempt":1,"payload":{"phaseId":"discover","name":"Discover","ordinal":1}}
```

## Test vectors

| ID | Input | Expected |
|---|---|---|
| EVENT-001 | first line `RunStarted`, sequence 1 | valid |
| EVENT-002 | sequences 1, 2, 4 | invalid gap |
| EVENT-003 | sequence duplicated | invalid |
| EVENT-004 | timestamp decreases but sequence increases | valid; sequence controls order |
| EVENT-005 | `UnitFinished` before its `UnitStarted` | invalid |
| EVENT-006 | two `RunFinished` events | invalid |
| EVENT-007 | event after `RunFinished` | invalid |
| EVENT-008 | concurrent units interleave while each lifecycle is ordered | valid |
| EVENT-009 | `ChangeApplied` with no prior plan in plan+apply stream | invalid |
| EVENT-010 | abrupt final partial JSON line | ignore partial line; stream incomplete |
| EVENT-011 | missing `RunFinished` after abrupt termination | valid prefix; run not terminal |
| EVENT-012 | payload contains authorization header | reject/redact before write; fatal if canary remains |
| EVENT-013 | `ProgressTick` coalesced under backpressure | valid |
| EVENT-014 | `DiagnosticEmitted` dropped under backpressure | invalid implementation |
| EVENT-015 | version 2 read by strict v1 consumer | explicit refusal |
| EVENT-016 | web resource URL with query token | query removed or opaque reference used |
| EVENT-017 | missing or zero `attempt` | invalid |
| EVENT-018 | phase ID outside the version-1 enum | invalid |
