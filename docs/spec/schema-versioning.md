# Schema versioning and plan preconditions

Status: draft for approval (M0). Normative.

## Version field

Every persisted config, plan, report, serialized IR, catalog entry, and identity mapping MUST contain top-level `schemaVersion`. JSON uses an integer. YAML accepts only an unquoted integer. Version `0`, negative values, strings, floats, and missing values are invalid.

| Artifact | Canonical filename/example | Independently versioned |
|---|---|---|
| Configuration | `trasloco.yaml` | yes |
| Plan | `plan.json` | yes |
| Report | `report.json` | yes |
| Intermediate representation | cache/fixture JSON | yes |
| Catalog entry | task/action mapping YAML | yes |
| Identity mapping | `identities.yaml` | yes |

Versions are monotonically increasing integers per artifact family. They are not tool versions and do not share a counter. Any incompatible structural or semantic change increments the artifact version. Adding an optional field with a fully specified default MAY retain the version. Removing/renaming fields, changing defaults, types, interpretation, canonicalization, hashes, or required invariants MUST increment it.

## Read/write compatibility

An implementation whose current version is `N`:

- MUST read `N`.
- MUST read `N-1` and upgrade it in memory deterministically before validation/use.
- MUST write only `N`.
- MUST explicitly refuse `< N-1` with remediation to run an intermediate upgrader.
- MUST explicitly refuse `> N` with remediation to upgrade trasloco.
- MUST NOT guess a version when the field is absent.

The tool MAY retain more than N-1 upgrader steps, but N-1 is the guaranteed window. Automatic upgrade is read-only/in-memory during normal commands; it MUST NOT overwrite the input. `trasloco upgrade FILE` is the explicit persisted upgrade, writes atomically, preserves a backup unless disabled, and reports source/target versions. Plans are never upgraded for apply: an old plan MAY be read for inspection, but apply MUST require a newly generated current-version plan.

Unknown fields on version N are errors except under fields explicitly named `extensions`. Upgraders MUST be pure, deterministic, idempotent, and covered by golden before/after vectors. They MUST NOT perform network calls, resolve identities, read secrets, or invent missing required decisions.

## Tool compatibility

Artifacts also carry `toolVersion` as semantic-version text. `minimumReaderVersion` MAY set a stricter lower tool bound. Schema compatibility is checked first, then minimum reader. A newer tool patch/minor version MAY read a current-schema artifact. Apply additionally enforces plan producer compatibility below.

## Plan staleness and preconditions

`plan.json` MUST contain:

```json
{
  "schemaVersion": 1,
  "toolVersion": "0.1.0",
  "catalogVersion": "2026.10.0",
  "namePolicyVersion": "trasloco-name-v1",
  "configurationDigest": "sha256:...",
  "sourceSnapshot": {
    "algorithm": "sha256",
    "digest": "sha256:...",
    "objects": [{"resourceRef":"...","stableId":"...","revision":"...","contentDigest":"sha256:..."}]
  },
  "targetPreconditions": [
    {"resourceRef":"github://owner/repo","existence":"present","etag":"...","branch":"main","branchSha":"..."}
  ]
}
```

The source snapshot digest is SHA-256 over canonical JSON of sorted source object tuples and resolved template/catalog inputs. It MUST include content digests, stable IDs, source revisions/refs, and consumer-binding graph; it MUST exclude credentials, secret values, retrieval timestamps, and unordered API response order.

Before the first mutation, apply MUST re-read all material source and target preconditions. A changed source object/digest emits `TRL-PLAN-0001`; changed target ETag, existence, default branch, branch SHA, or relevant policy emits `TRL-PLAN-0002`; catalog, naming-policy, or incompatible tool producer emits `TRL-PLAN-0003`. Any such condition aborts with exit 4 and creates no ledger mutation entry beyond a read-only preflight record.

ETags are opaque and compared byte-for-byte after removing transport quoting only as defined by the adapter. If an endpoint lacks ETags, the adapter MUST use a canonical content digest and record `preconditionKind: digest`. Absence is a precondition (`existence: absent`), not an omitted field. A branch SHA precondition applies to the exact target ref planned for atomic content delivery.

`--allow-tool-version-difference` MAY permit patch-version differences only when schema, catalog, name policy, and canonical plan digest algorithms match. There is no force flag for source or target drift; the user MUST re-plan.

## Test vectors

| ID | Input | Expected |
|---|---|---|
| SCHEMA-001 | reader N=3, file version 3 | read directly |
| SCHEMA-002 | reader N=3, file version 2 | deterministic in-memory upgrade to 3 |
| SCHEMA-003 | reader N=3, file version 1 | refuse; intermediate upgrader required |
| SCHEMA-004 | reader N=3, file version 4 | refuse; upgrade trasloco |
| SCHEMA-005 | missing `schemaVersion` | invalid; no inference |
| SCHEMA-006 | `schemaVersion: "3"` | invalid type |
| SCHEMA-007 | normal command reads N-1 YAML | original bytes unchanged |
| SCHEMA-008 | apply receives N-1 plan | inspectable but not applicable; re-plan required |
| SCHEMA-009 | N file with unknown top-level field | validation error |
| SCHEMA-010 | unknown key under declared `extensions` | preserved |
| SCHEMA-011 | source API order changes; object tuples unchanged | snapshot digest unchanged |
| SCHEMA-012 | source content changes at same display name | `TRL-PLAN-0001`, exit 4 |
| SCHEMA-013 | target branch SHA changes | `TRL-PLAN-0002`, exit 4 |
| SCHEMA-014 | planned target absent, now present | `TRL-PLAN-0002`, exit 4 |
| SCHEMA-015 | catalog version changes | `TRL-PLAN-0003`, exit 4 |
| SCHEMA-016 | only retrieval timestamp changes | plan remains fresh |
| SCHEMA-017 | secret value rotates; plan stores only unresolved secret ref | no value comparison; target secret write follows explicit operation policy |
| SCHEMA-018 | upgrader run twice on already-N artifact | byte-equivalent canonical structure |

## Open questions

1. Should the compatibility guarantee extend to N-2 before 1.0 to reduce upgrader friction?
2. Which canonical JSON profile will be normative for snapshot and plan digests?
3. Should reports embed the exact input plan digest or a run manifest digest containing it?
4. Which patch-level tool differences are proven safe for apply?
