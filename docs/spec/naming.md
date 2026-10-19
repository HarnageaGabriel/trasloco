# Target naming service

Status: draft for approval (M0). Normative.

Every target name MUST pass through one deterministic naming service. Adapters provide a versioned `NamePolicy`; emitters MUST NOT implement ad hoc cleanup.

## Name kinds and scopes

| Kind | Scope key for uniqueness | Target rules |
|---|---|---|
| GitHub repository | target owner | Length and allowed characters MUST come from the GitHub capability probe. The default policy accepts ASCII letters, digits, `.`, `-`, `_`, rejects empty/`.`/`..`, and compares case-insensitively. VERIFY: GitHub's current documented maximum and server-side canonicalization. |
| GitHub Actions secret | organization, repository, or environment separately | ASCII letters, digits, `_` ([Ruling R1] the target alphabet is exactly `A-Z`, `0-9`, `_`; generated truncation and collision markers use `__`, never `--`); MUST NOT start with `GITHUB_` (case-insensitive) or a digit; case-insensitive uniqueness; canonical output upper-case. |
| GitHub Actions variable | organization, repository, or environment separately | Same lexical/reserved rules as Actions secrets; case-insensitive uniqueness; canonical output upper-case. VERIFY: confirm identical restrictions for configuration variables on supported GHES versions. |
| GitHub environment | repository | Trim surrounding whitespace; then require non-empty and at most 255 characters with case-insensitive uniqueness. `/`, internal spaces, and Unicode are preserved if accepted by capability probe. VERIFY: character restrictions and exact Unicode comparison. |
| GitHub custom property | organization schema | Default policy: ASCII letters, digits, `_`, `-`; first character letter; case-insensitive uniqueness. VERIFY: current allowed characters, length, and case behavior. |
| ADO variable | effective dictionary scope | ASCII letters, digits, `.`, `_`; MUST NOT begin with reserved prefixes `endpoint`, `input`, `secret`, `path`, or `securefile`, compared case-insensitively; comparison is case-insensitive. VERIFY: whether hyphen is accepted consistently across UI, YAML, and logging commands. |
| ADO variable group | ADO project | Non-empty display name; trim surrounding whitespace; case-insensitive uniqueness. VERIFY: length, forbidden characters, and actual uniqueness behavior. |
| ADO environment | ADO project | Non-empty display name; trim surrounding whitespace; case-insensitive uniqueness. VERIFY: length and forbidden characters. |
| GitHub workflow file | repository tree under `.github/workflows/` | Lower-case filename, ASCII letters/digits/`-`/`_`, extension `.yml`; `/` is not allowed in the generated basename; path comparison case-insensitive for portability. |
| GitHub job ID | one workflow | Must start with ASCII letter or `_`; remaining characters ASCII letters, digits, `-`, `_`; case-sensitive syntax but case-insensitive collision policy for portability. |

Names already valid still pass collision and reserved-name checks. A platform capability probe MAY tighten a rule but MUST NOT loosen plan-time validation relative to the actual endpoint.

[Ruling R2] IR secrets at pipeline, stage, job, step and group scope all target the same GitHub repository secret namespace; only environment scope selects an environment namespace. Distinct source identities whose names normalize equal collide in that namespace and MUST receive distinct suffixed names.

## Deterministic normalization

The input to normalization is `(kind, sourceStableId, sourceName, targetScopeKey, policyVersion)`.

1. Decode source text as UTF-8. Invalid input is a source error; it MUST NOT be replaced silently.
2. Normalize Unicode to NFC and remove leading/trailing Unicode whitespace.
3. Apply kind-specific case policy. Upper-case GitHub secrets/variables; lower-case workflow basenames; preserve display case otherwise. For workflow files, remove one case-insensitive terminal `.yaml` or `.yml` before normalizing the basename; append canonical `.yml` after step 10. [Ruling R6] For GitHub secrets, apply the upper-case conversion after transliteration and NFD mark stripping, so transliteration cannot reintroduce lower case.
4. Transliterate non-ASCII to an ASCII approximation only for ASCII-only kinds. If no deterministic transliteration exists, encode each UTF-8 byte as `_UHH`; `HH` is uppercase hex and the encoding token is not processed as a separator. Display-name kinds preserve Unicode.
5. Replace each maximal run of forbidden characters with the kind's separator: `_` for variables/secrets/job IDs/custom properties; `-` for repositories/workflow files. Dots are allowed and preserved in repository names. Display-name kinds remove prohibited control characters and use `-` only when removal would concatenate tokens.
6. Collapse repeated separators of the chosen kind. Remove leading/trailing separators when forbidden by that kind. [Ruling R7] Collapse only separators that did not come from an encoding token; `_UHH` is never a separator.
7. If the first character is illegal, prefix `N_` for variables/secrets/job IDs/custom properties or `n-` for repository/workflow kinds. [Ruling R5] Workflow filenames receive no first-character prefix: a leading digit or `_` is accepted. Prefixes remain only for job IDs and step IDs (must start with a letter or `_`) and for secret/variable names (must not start with a digit).
8. If the result is reserved, prefix `T_` or `t-` as appropriate. Case-insensitive reserved comparisons use Unicode simple case fold.
9. If empty, stop with `TRL-NAME-0005`; automatic invention from IDs is forbidden.
10. Reserve room for suffixes. If over the endpoint limit, truncate at a Unicode code-point boundary and append `--h` plus the first 10 lower-case hexadecimal characters of SHA-256 over `kind NUL sourceStableId NUL NFC(sourceName)`. Emit `TRL-NAME-0006`. [Ruling R1] For GitHub secret and variable names the marker is `__H` and the 10 hexadecimal characters are upper-case, because the target alphabet has no lower-case letters.
11. Detect collisions **after normalization**, within the scope key in the table and against observed target names.
12. If no collision, use the normalized candidate. Otherwise apply disambiguation below.

[Ruling R11] The `kind` bytes used in truncation and collision hashes are the stable labels `workflow`, `github-job`, `github-step`, and `github-secret` (and `github-variable` when that kind is added).

The algorithm version MUST be stored as `namePolicyVersion`. A policy change changes plan output and invalidates old plans unless the old implementation remains available.

## Collision handling

Identical duplicates have the same source semantic identity and normalized content digest; they merge with provenance retained. All other same-scope equal comparison keys are semantic collisions.

For colliding source objects not already bound to target objects:

1. Sort by `(platform, sourceStableId)` using unsigned UTF-8 bytes.
2. The first receives the unsuffixed candidate only if no unrelated target already owns it.
3. Each remaining object receives `<base>--<token>`, where `token` is the first 8 lower-case base32 characters (RFC 4648 alphabet without padding, lower-case) of SHA-256 over `kind NUL sourceStableId`. [Ruling R1] For GitHub secret and variable names the suffix is `<base>__<TOKEN>` with an upper-case base32 token.
4. If the name exceeds the limit, truncate the base before appending. If an extraordinarily rare suffix collision remains, extend both colliding tokens by 2 characters until unique, up to the full digest; exhaustion emits `TRL-NAME-0003`. [Ruling R3] A suffixed form MUST NOT equal another entry's literal name or an already-assigned name in the same namespace. Verify every output case-insensitively against all candidates and assigned names across collision groups. Extend ALL entries whose tokens collide, not only the later one.
5. An observed, unowned target name never loses its name. Every source collision with it receives a suffix unless an explicit `adopt` mapping proves identity.

[Ruling R10] If a limit is too small to hold the separator, marker and at least one base character, stop with `TRL-NAME-0008`; do not report collision exhaustion (`TRL-NAME-0003`) and never emit a name starting with `-`.

Suffixes are based on stable IDs, never enumeration order or current display names. Explicit mappings are validated but are not normalized silently; invalid explicit names are errors so user intent is not changed.

## Reserved names

All kinds reserve empty string, `.` and `..`. Windows device basenames (`CON`, `PRN`, `AUX`, `NUL`, `COM1` through `COM9`, `LPT1` through `LPT9`) are reserved case-insensitively for generated files. Workflow basenames also reserve `README`, `.`, and `..`. GitHub secret/variable names reserve prefix `GITHUB_`. Prefix `RUNNER_` is preserved and emits warning `TRL-NAME-0007`; it is not an error or automatic rename. [Ruling R8] The warning applies to GitHub Actions variable/environment-variable names, not to secrets. ADO variables reserve the prefixes listed in the table. The active `NamePolicy` MAY add endpoint-specific names and MUST record them in the plan.

## Rename decision records

Each normalization, explicit rename, or collision result MUST appear in `plan.json`:

```json
{
  "decisionId": "name:github-secret:ado-var-42",
  "kind": "github-secret",
  "scopeKey": "github:repo:1842",
  "source": {"platform": "ado", "stableId": "ado-var-42", "name": "db.password"},
  "targetName": "DB_PASSWORD",
  "reason": "normalized",
  "algorithm": "trasloco-name-v1",
  "comparisonKey": "db_password",
  "collisionSet": [],
  "reverse": {"originalName": "db.password", "referenceForms": ["$(db.password)"]}
}
```

`reason` is `unchanged|normalized|reserved|truncated|collision|explicit|adopted`. `collisionSet` contains stable IDs, sorted, never values. `reverse` MUST preserve the original spelling and all source reference forms needed for report/rollback. Secret values MUST NOT be included. References in emitted files MUST be rewritten from the record, not by a second normalization call.

## Test vectors

Assume no target conflicts, limits large enough, and stable IDs shown unless stated.

| ID | Kind / sourceStableId / input | Expected |
|---|---|---|
| NAME-001 | GitHub secret / 1 / `db.password` | `DB_PASSWORD` |
| NAME-002 | GitHub secret / 1 / `Db Password` | `DB_PASSWORD` |
| NAME-003 | GitHub secret / 1 / `9lives` | `N_9LIVES` |
| NAME-004 | GitHub secret / 1 / `github_token2` | `T_GITHUB_TOKEN2`; reserved prefix |
| NAME-005 | GitHub variable / 1 / `runner_arch` | `RUNNER_ARCH`; warning `TRL-NAME-0007` |
| NAME-006 | GitHub secret / 1 / `__a__b__` | `_A_B_` |
| NAME-007 | GitHub secret / 1 / ` café ` | `CAFE` |
| NAME-008 | GitHub secret / 1 / `東京` | `_UE6_U9D_UB1_UE4_UBA_UAC` |
| NAME-009 | GitHub environment / 1 / `QA West` | `QA West` |
| NAME-010 | GitHub environment / 1 / ` QA West ` | `QA West` |
| NAME-011 | workflow / 1 / `Build and Test` | `build-and-test.yml` |
| NAME-012 | workflow / 1 / `release.YAML` | `release.yml` (known workflow extension replaced) |
| NAME-013 | workflow / 1 / `CON` | `t-con.yml` |
| NAME-014 | workflow / 1 / `a/b` | `a-b.yml` |
| NAME-015 | job ID / 1 / `build.test` | `build_test` |
| NAME-016 | job ID / 1 / `2-build` | `N_2-build` |
| NAME-017 | job ID / 1 / `_deploy` | `_deploy` |
| NAME-018 | repository / 1 / `API Service` | `API-Service` |
| NAME-019 | repository / 1 / three spaces | `TRL-NAME-0005` after trimming |
| NAME-020 | ADO variable / 1 / `db-password` | `db_password` |
| NAME-021 | ADO variable / 1 / `secretToken` | `T_secretToken` |
| NAME-022 | ADO variable group / 1 / ` Shared Build ` | `Shared Build` |
| NAME-023 | custom property / 1 / `source.platform` | `source_platform` |
| NAME-024 | same GitHub secret scope: `db.password` ID `a`, `db-password` ID `b` | collision detected after both normalize to `DB_PASSWORD` |
| NAME-025 | case-insensitive existing secret `Db_Password` plus source `DB_PASSWORD` | source gets deterministic suffix; existing name preserved |
| NAME-026 | same normalized name in two repositories | no collision; scopes differ |
| NAME-027 | same environment name in repositories A and B | no collision; scopes differ |
| NAME-028 | workflow `Build` and `build` in one repo | collision under portable comparison |
| NAME-029 | two identical records with same stable ID/content | merged; `TRL-VAR-0003`, no suffix |
| NAME-030 | two distinct IDs `a`,`b`, same input | ID byte-order chooses unsuffixed; other gets hash token |
| NAME-031 | same collision inventory in reverse API order | identical assignments to NAME-030 |
| NAME-032 | explicit mapping `db.password -> bad name` for GitHub secret | error; explicit value not silently normalized |
| NAME-033 | input longer than limit | truncated at code-point boundary plus `--h` and 10 lower-case hex chars; for the secret kind `__H` and 10 upper-case hex characters (Ruling R1) |
| NAME-034 | same long name and ID in two runs | identical truncation hash |
| NAME-035 | composed `é` and decomposed `e` + combining acute | identical normalized candidate; collision if distinct IDs |
| NAME-036 | source renamed after plan, same stable ID | precondition drift; re-plan, not reuse stale decision |
| NAME-037 | target match with explicit `adopt` and proven provenance | target name retained; reason `adopted` |
| NAME-038 | collision suffix itself collides at 8 base32 chars | extend colliding tokens to 10 chars |
| NAME-039 | repository / 1 / `api.v2` | `api.v2`; dots preserved |
| NAME-040 | workflow / 1 / `2-build` | `2-build.yml`; no first-character prefix (Ruling R5) |
| NAME-041 | GitHub secret / 1 / `ǰ` | `J`; upper-case after transliteration (Ruling R6) |
| NAME-042 | same GitHub secret scope: `db.password` ID `a`, `db-password` ID `b` | `DB_PASSWORD`; collision suffix uses `__` and an upper-case token (Ruling R1) |
| NAME-043 | three GitHub secret entries in one scope with IDs `a`, `b`, `c` and input `db.password` | first keeps `DB_PASSWORD`; both others get `DB_PASSWORD__<TOKEN>`; colliding tokens extend together (Ruling R3) |
| NAME-044 | GitHub secret / 1 / `db.password` at limit 3 | `TRL-NAME-0008`; limit too small (Ruling R10) |
| NAME-045 | GitHub secret / 1 / `a_東` | `A_UE6_U9D_UB1`; encoding and separator handling (Ruling R7) |
| NAME-046 | same GitHub secret scope: `db.password` ID `stage`, `db-password` ID `job` | `DB_PASSWORD`; collision in one repository namespace even though source scopes differ (Ruling R2) |

The marked `VERIFY:` platform facts require live conformance tests in M1/M3 before their adapters freeze for 1.0.
