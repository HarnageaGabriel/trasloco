# Identity mappings

Status: draft for approval (M0). Normative.

`identities.yaml` is an explicit, user-controlled mapping between principals. Trasloco MUST NOT guess a target from a display name or silently grant permissions.

## File format

```yaml
schemaVersion: 1
mappings:
  - id: approver-platform-team
    source:
      platform: azuredevops
      kind: group
      descriptor: vssgp.Uy0xLTkt...
      guid: 11111111-2222-3333-4444-555555555555
      email: null
    target:
      platform: github
      kind: team
      organization: HarnageaGabriel
      slug: platform-team
    purposes: [environment-reviewer]
    verified: false
    note: Confirm team membership before apply.
```

Top-level fields are `schemaVersion`, optional `endpointAliases`, and `mappings`. Unknown fields are invalid except `extensions`. `endpointAliases` maps a user-chosen alias to one canonical configured endpoint; source and target principals MAY reference the alias so one file can cover multiple organizations. Aliases MUST be unique, MUST resolve during preflight, and MUST NOT weaken endpoint containment. Each mapping requires a unique `id`, `source`, `target`, and non-empty `purposes`. `verified` defaults to false. `note` is optional user-authored text and MUST be redacted like other output.

### Principal forms

| Platform | Kind | Required stable locator | Optional corroborating fields |
|---|---|---|---|
| GitHub | `user` | `login` | numeric `databaseId`, email |
| GitHub | `team` | `organization` + `slug` | numeric `databaseId` |
| Azure DevOps | `user` | `descriptor` or `guid` | email |
| Azure DevOps | `group` | `descriptor` or `guid` | display name, email |

VERIFY: Confirm which Azure DevOps APIs accept descriptors versus identity GUIDs for each permission/reviewer operation. The adapter MUST resolve the accepted platform identifier during read-only preflight and store the returned stable identifier; it MUST NOT synthesize descriptors or GUIDs.

Email is a discovery hint, not identity. If `descriptor` and `guid` are both supplied, they MUST resolve to the same principal. GitHub login/team slug comparisons follow GitHub case behavior but the API-returned spelling is preserved. One source MAY map to multiple targets only when purposes are disjoint. Conflicting applicable mappings emit `TRL-IDENT-0002`.

Allowed purposes are `environment-reviewer`, `repository-permission`, `team-membership`, `pipeline-permission`, `variable-group-permission`, `service-connection-permission`, and `ownership`. New purposes require a schema-compatible declared extension or a schema increment.

## Audit and apply behavior

Audit MUST draft entries for every referenced identity, with the stable source locator and `target: null`. Drafting MUST NOT query private target identity data unless authorized. Display names may be included when supplied or discovered under configured privacy policy. Emails MUST be omitted unless the user explicitly passes `--include-emails`; the flag applies only to that run and does not permit membership-list export.

An unmapped identity produces `TRL-IDENT-0001`, fidelity `needs-manual`, and a blocked dependent operation. Other independent resources MAY continue, yielding partial status where applicable. Trasloco MUST NOT:

- select a target based only on equal display name or email;
- replace a missing team with all organization members;
- drop an approver/check while enabling an environment;
- fan permissions out to repositories or groups;
- construct cloud federation issuer/subject identifiers locally.

Apply MUST resolve both endpoints again, verify kind and stable locator, verify target existence, and record only stable IDs in the ledger. `verified: true` means the maintainer approved the semantic mapping; it does not skip live existence/precondition checks.

## Directionality

Mappings are directional. For GitHub to ADO, GitHub is `source`; for ADO to GitHub, ADO is `source`. A reverse migration MUST use an explicit reverse entry because one-to-many and purpose-specific mappings are not generally invertible. Tools MAY propose the reverse as a draft, never as verified.

One identity file is valid for every declared purpose and endpoint alias. A mapping MAY be reused across purposes, but each run MUST record explicit approval of the mapping IDs and purposes it consumes; approval from an earlier run is not inherited. Signed identity mappings and signed reports are deferred to the 1.x format and MUST be introduced together.

## Privacy and redaction

Reports MUST contain no personal data beyond fields the user supplies in `identities.yaml`, unless the user explicitly enables `report.includeDiscoveredIdentityMetadata`. Default reports use mapping ID, platform, kind, stable opaque locator digest, and status. Raw emails, display names, membership lists, access tokens, and identity API payloads MUST NOT appear in events or diagnostics.

Local identity files SHOULD have owner-only permissions. Plans SHOULD reference the identity file digest and mapping IDs, not embed personal fields. Audit caches containing discovered identity metadata MUST follow configured encryption and retention policy.

## Test vectors

| ID | Input | Expected |
|---|---|---|
| IDENT-001 | ADO group descriptor maps to GitHub org/team slug for `environment-reviewer` | valid mapping |
| IDENT-002 | GitHub user login maps to ADO user GUID for `repository-permission` | valid directional mapping |
| IDENT-003 | same mapping `id` twice | schema error `TRL-IDENT-0003` |
| IDENT-004 | source has only display name | invalid; stable locator required |
| IDENT-005 | source has only email | invalid as final mapping; may remain audit draft |
| IDENT-006 | descriptor and GUID resolve to different ADO principals | `TRL-IDENT-0002` |
| IDENT-007 | two mappings for same source/purpose with different targets | `TRL-IDENT-0002` |
| IDENT-008 | same source maps to team for reviewer and user for ownership | allowed if purposes disjoint |
| IDENT-009 | referenced approver has `target: null` | `TRL-IDENT-0001`; dependent protection blocked |
| IDENT-010 | missing reviewer mapping, unrelated repository content valid | reviewer operation blocked; content may proceed; terminal status partial/blocked per outcomes |
| IDENT-011 | equal source/target display names without mapping | no automatic match |
| IDENT-012 | reverse migration using only forward entry | unmapped; reverse entry required |
| IDENT-013 | `verified: true`, target deleted after plan | stale/failed precondition; no grant |
| IDENT-014 | default report from mapping containing user email | omit email; retain mapping ID/status |
| IDENT-015 | user passes `--include-emails` and enables discovered identity metadata | include permitted emails only; still redact tokens/memberships |
| IDENT-016 | cloud identity without provider-returned issuer | `TRL-IDENT-0005`; never synthesize |
| IDENT-017 | email-only audit draft without `--include-emails` | omit email; retain stable opaque locator when available |
| IDENT-018 | mapping uses a declared endpoint alias | resolve alias during preflight; valid |
| IDENT-019 | mapping reused for two purposes in a new run | require and record approval for both purposes in that run |
