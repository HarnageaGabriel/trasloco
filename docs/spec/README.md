# Trasloco cross-cutting specifications

Status: draft for approval (M0). Normative.

These specifications define contracts shared by every dialect and platform adapter. Engine code and tests MUST implement these contracts literally. A platform adapter MUST NOT weaken a cross-cutting rule; a platform limitation is represented by a diagnostic and fidelity grade.

## Specification index

| File | Contract |
|---|---|
| `selectors.md` | Source target syntax and deterministic selection |
| `naming.md` | Target name validation, normalization, collision handling, and reversibility |
| `variable-precedence.md` | Effective variable evaluation and target-scope projection |
| `diagnostics.md` | Stable diagnostic model and initial registry |
| `schema-versioning.md` | Compatibility, upgrades, and plan preconditions |
| `identities.md` | Explicit human and group identity mappings |
| `credentials.md` | Credential actor/scope policy and anti-abuse invariants |
| `script-protocol.md` | Portable operations embedded in scripts |
| `events.md` | Versioned engine event stream |
| `exit-codes.md` | Process exit status contract |

## Normative language

The keywords **MUST**, **MUST NOT**, **REQUIRED**, **SHOULD**, **SHOULD NOT**, and **MAY** are interpreted as described by RFC 2119 and RFC 8174 when, and only when, they appear in uppercase.

Examples are normative when introduced by “Test vectors”. Explanatory notes are non-normative. A statement prefixed `VERIFY:` records a platform fact that requires confirmation against current public vendor documentation before implementation. Until confirmed, code MUST treat that fact as an adapter capability or configured limit, not a hard-coded invariant.

## Test-vector convention

Test vectors are Markdown tables containing at least `ID`, `Input`, and `Expected`. IDs are globally stable, uppercase, and use the file prefix plus a zero-padded number, for example `SEL-001`. Once published, an ID MUST NOT be reused for a different case. Tests MUST read these tables directly as the single source of truth; duplicated machine-readable fixtures are forbidden. Cells MUST be self-contained, line breaks MUST use `<br>`, and structured data SHOULD use inline JSON or fenced blocks outside the table. `Expected diagnostic` is included when applicable.

Implementations MUST preserve the literal input before parsing, compare the canonical result structurally, and fail if a documented vector cannot be loaded. New behavior requires a new vector. Correcting an erroneous vector requires a changelog entry and MUST NOT silently change an already released expectation.

## Glossary

| Term | Definition |
|---|---|
| unit | Smallest independently planned and ledgered migration subject, identified by a stable `unitKey`; normally one repository and its pipelines/resources. |
| shared unit | Independently planned resource consumed by multiple units, such as an organization variable, ADO variable group, runner group, or reusable template repository. It is never duplicated implicitly. |
| dialect | Pipeline language and evaluation model, independent of network APIs; examples: `azure-pipelines`, `github-actions`. |
| platform | API-backed product that stores repositories and DevOps resources; examples: Azure DevOps Services, GitHub.com, GHES. |
| fidelity | Per-construct conversion outcome. Allowed grades are defined below. |
| provenance | How output was produced, independently of fidelity: `deterministic` or `llm`. |
| `exact` | Target behavior is equivalent within declared platform capabilities. Cosmetic output differences are allowed. |
| `approximated` | Target is runnable but has a documented semantic difference that does not require user input before apply. |
| `needs-manual` | A person must make or approve a decision. The dependent operation remains blocked. |
| `unsupported` | Trasloco has no safe target representation. Source content is retained for reporting; active target behavior MUST NOT be silently omitted. |
| plan | Immutable, secret-free declaration of intended changes, dependencies, diagnostics, rename decisions, and preconditions. |
| ledger | Durable per-operation apply journal used for idempotency, resume, verification, and compensating rollback. |
| run | One invocation with a unique `runId`, fixed configuration digest, event stream, and terminal outcome. |

Fidelity ordering for aggregation is `exact < approximated < needs-manual < unsupported`; the highest encountered grade is reported. Provenance aggregates independently: any LLM-proposed contribution makes the aggregate `llm`. An accepted LLM proposal MAY have `exact` fidelity only after deterministic validation proves the normal exactness criteria; its provenance remains `llm`.

These contracts become compatibility guarantees at 1.0. Before 1.0 they remain normative implementation drafts and MAY change with an explicit specification update.

## Shared invariants

1. All externally stored objects MUST contain a `schemaVersion` governed by `schema-versioning.md`.
2. Stable identifiers MUST be used for joins. Display names MUST NOT be identity keys.
3. Plans, reports, events, diagnostics, and logs MUST NOT contain secret values.
4. Source information MUST NOT disappear silently. Loss or uncertainty MUST produce a diagnostic with fidelity and provenance.
5. Planning MUST be deterministic for the same normalized inputs, catalog version, and configuration.
6. Apply MUST check plan preconditions before its first mutation.
7. Every automatic rename and scope change MUST be recorded in the plan with enough metadata to reverse references.
8. Unknown enum values on a supported schema version MUST be rejected unless that field explicitly permits extensions.

## Test vectors

| ID | Input | Expected |
|---|---|---|
| SPEC-001 | keyword `MUST` | normative requirement |
| SPEC-002 | word `must` in explanatory prose | not an RFC 2119 requirement |
| SPEC-003 | grades `exact`, `approximated` | aggregate `approximated` |
| SPEC-004 | grades `needs-manual`, `unsupported` | aggregate `unsupported` |
| SPEC-005 | plan field containing a supplied secret value | reject; `TRL-PLAN-0006` |
| SPEC-006 | two units refer to one organization variable | one shared unit plus two bindings; no copies created by default |
| SPEC-007 | LLM proposal passes deterministic exactness validation | fidelity `exact`; provenance `llm` |
