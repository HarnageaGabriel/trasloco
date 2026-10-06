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

Top-level fields are `schemaVersion` and `mappings`. Unknown fields are invalid except `extensions`. Each mapping requires a unique `id`, `source`, `target`, and non-empty `purposes`. `verified` defaults to false. `note` is optional user-authored text and MUST be redacted like other output.

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

Audit MUST draft entries for every referenced identity, with the stable source locator and `target: null`. Drafting MUST NOT query private target identity data unless authorized. Display names and emails are included only when the user supplied them or the authorized source API returned them and configuration permits personal-data export.

An unmapped identity produces `TRL-IDENT-0001`, fidelity `needs-manual`, and a blocked dependent operation. Other independent resources MAY continue, yielding partial status where applicable. Trasloco MUST NOT:

- select a target based only on equal display name or email;
- replace a missing team with all organization members;
- drop an approver/check while enabling an environment;
- fan permissions out to repositories or groups;
- construct cloud federation issuer/subject identifiers locally.

Apply MUST resolve both endpoints again, verify kind and stable locator, verify target existence, and record only stable IDs in the ledger. `verified: true` means the maintainer approved the semantic mapping; it does not skip live existence/precondition checks.

## Directionality

Mappings are directional. For GitHub to ADO, GitHub is `source`; for ADO to GitHub, ADO is `source`. A reverse migration MUST use an explicit reverse entry because one-to-many and purpose-specific mappings are not generally invertible. Tools MAY propose the reverse as a draft, never as verified.

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
| IDENT-015 | user enables discovered identity metadata | include only permitted fields, still redact tokens/memberships |
| IDENT-016 | cloud identity without provider-returned issuer | `TRL-IDENT-0005`; never synthesize |

## Open questions

1. Should email-only audit drafts be written by default or require privacy opt-in?
2. Should one file support endpoint aliases for multi-organization migrations?
3. Which mappings may be reused across purposes without separate approval?
4. Should mapping signatures be supported for regulated approval workflows?
