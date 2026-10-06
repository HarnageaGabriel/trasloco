# ADR 0009: YAML handling per dialect and emitter strategy

Status: Accepted — 2026-10-06. Source: architecture review of M0 + M1 wave 1.

## Context
Strict scalar typing rejected valid Azure Pipelines YAML (all scalars are strings there). Anchors and merge keys were accepted although Azure Pipelines forbids them. Emit re-encoded whole files and swallowed errors.

## Decision
- `yamlx.Node.Scalar()` returns the raw scalar text regardless of tag; the Azure Pipelines dialect uses it exclusively. Strict typed accessors remain for GitHub Actions and config.
- Per-dialect `AnchorPolicy`: Azure Pipelines = error diagnostic on anchors/aliases/merge keys; GitHub Actions = anchors allowed, merge keys VERIFY; config = allowed.
- Recursive walkers have a depth cap.
- Emitter strategy: new files are emitted from typed structures (normalized formatting); in-place edits (`modernize`, small fixes) use byte-range patches on the original text so diffs stay minimal. `Emit` always returns an error value.

## Consequences
Real-world ADO files parse; modernize PRs show only the meaningful lines.
