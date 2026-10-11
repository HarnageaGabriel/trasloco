# Diagnostics

Status: draft for approval (M0). Normative.

Every finding the tool reports (parse problem, lossy conversion, drift, missing secret, internal error) is a **Diagnostic** with a stable code. Codes appear in reports, events (`DiagnosticEmitted`), CI output, and support requests.

`internal/diag/registry.yaml` is the single source of truth. This specification's registry table and per-code diagnostic pages are generated views of that registry; until generation is automated, maintainers MUST keep the views byte-for-byte semantically equivalent to the registry.

## 1. Code format

```
TRL-<AREA>-<NNNN>
```

- `TRL` is a fixed prefix.
- `<AREA>` is one of the areas in section 2 (upper-case ASCII).
- `<NNNN>` is a zero-padded 4-digit decimal, starting at `0001` within each area.
- Regex: `^TRL-(CFG|SEL|AUTH|HTTP|SRC|PARSE|EXPR|TPL|VAR|NAME|CONV|CAT|EMIT|VAL|PLAN|APPLY|SECRET|IDENT|REPO|CUTOVER|LLM|INT)-[0-9]{4}$`

## 2. Areas

| Area | Scope |
|---|---|
| CFG | Configuration files, flags, schema versions |
| SEL | Selectors and selection files (`selectors.md`) |
| AUTH | Credentials, permissions, auth methods |
| HTTP | Transport: rate limits, retries, pagination, TLS |
| SRC | Reading the source platform / source files |
| PARSE | YAML / file parsing |
| EXPR | Expression language (ADO and GHA expressions) |
| TPL | Template resolution (ADO templates, reusable workflows) |
| VAR | Variables: scoping, precedence, collisions (`variable-precedence.md`) |
| NAME | Target naming (`naming.md`) |
| CONV | Conversion fidelity decisions (IR to target) |
| CAT | Catalog: tasks/actions mapping, allowlist, retirement |
| EMIT | Target emission |
| VAL | Validation of emitted output |
| PLAN | Plan creation, staleness, conflicts |
| APPLY | Apply, resume, rollback, ledger |
| SECRET | Secret values and handling |
| IDENT | Identities (`identities.md`), cloud federation |
| REPO | Repository content migration |
| CUTOVER | Cutover phases |
| LLM | Optional AI assistance |
| INT | Internal errors (bugs) |

## 3. Severity

| Severity | Meaning | Default effect |
|---|---|---|
| `error` | The operation for the affected resource cannot proceed or its result is invalid. | Resource not emitted/applied; contributes to exit code 1 (or 2 if the run was otherwise partially successful). |
| `warning` | The operation proceeds but the result differs from the source or needs attention. | Reported; exit code unaffected unless `exitClass` says otherwise. |
| `info` | Informational: a deterministic decision was made. | Reported only at verbosity >= normal in text output; always present in JSON. |

## 4. Diagnostic fields

A diagnostic instance in `report.json`, events, and logs has exactly these fields (JSON, camelCase):

| Field | Type | Req | Description |
|---|---|---|---|
| `code` | string | yes | `TRL-AREA-NNNN`, must exist in the registry. |
| `severity` | enum | yes | `error` \| `warning` \| `info`. Fixed by the registry code; instances MUST NOT override it. Policy thresholds use `--fail-on` without changing stored severity. |
| `fidelity` | enum or null | yes | `exact` \| `approximated` \| `needs-manual` \| `unsupported` \| `null` (when not a conversion finding). |
| `provenance` | enum | yes | `deterministic` \| `llm`; orthogonal to fidelity. |
| `message` | string | yes | Human-readable, English, single line, already redacted. MUST NOT contain secret values. |
| `source` | object or null | yes | `{ "platform": "...", "file": "path-or-uri", "line": 1, "col": 1, "endLine": 1, "endCol": 1 }`. Lines and columns are 1-based; `col` counts Unicode code points. `file` is repo-relative with `/` separators, or a URI for API objects. Unknown parts are omitted, not zero. |
| `resource` | object or null | yes | `{ "kind": "pipeline\|repo\|variable\|secret\|environment\|group\|connection\|unit\|run\|...", "key": "stable key", "name": "display name" }`. `key` equals the Unit key / stable source key when applicable. |
| `remediation` | string | yes | One or two sentences telling the user what to do. Copied from the registry; MAY be refined per instance. |
| `docsUrl` | string | yes | `https://github.com/HarnageaGabriel/trasloco/blob/main/docs/diagnostics/<CODE>.md` (stable, derived from the code). |
| `params` | object | no | Machine-readable parameters (e.g. `{"from":"db-pass","to":"DB_PASS"}`). No secrets. |

Instances are ordered in reports by: `(source.file, source.line, source.col, code, resource.key)` ascending; instances without source sort last.

At most 1,000 instances of one code may be stored per run. Additional distinct instances are suppressed and reports MUST include the summarized suppressed count per code. Exact duplicate instances count once.

## 5. Stability rules

1. A code, once released in any tagged version, is never reused for a different meaning.
2. The title MAY be reworded; the meaning MUST NOT change. A meaning change requires a new code.
3. Deprecation: set `status: deprecated`, add `replacedBy`. A deprecated code MAY still be emitted for 2 minor releases, then becomes `retired` (never emitted, never reused, kept in the registry forever).
4. Default severity changes are allowed only in minor releases and MUST be recorded in the registry `history`.
5. `exitClass` changes are breaking and require a major release.
6. Every code the implementation emits MUST exist in the registry; a unit test asserts the set of emitted codes (collected from source) is a subset of the registry, and every registry entry has a docs page.
7. Codes in other specs (`docs/spec/*.md`) MUST exist in section 7 of this file. A test greps the specs and verifies this.

## 6. Registry file schema (`internal/diag/registry.yaml`)

The registry is YAML, validated by JSON Schema, with `schemaVersion` (see `schema-versioning.md`).

```yaml
schemaVersion: 1
codes:
  - code: TRL-NAME-0002            # required, unique, matches the regex in section 1
    area: NAME                     # required, must equal the code's area
    title: "Name collision resolved by suffix"   # required, <= 80 chars, English
    severity: warning              # required: error | warning | info
    fidelity: approximated         # optional: exact|approximated|needs-manual|unsupported; omit for non-conversion
    provenance: deterministic      # optional: deterministic|llm; default deterministic
    exitClass: none                # required: none | error | blocked | drift | usage  (see exit-codes.md)
    remediation: "Review the rename decision in the plan or set an explicit name in the mapping file."  # required
    since: "0.1.0"                 # required, first version that emits it
    status: active                 # required: active | deprecated | retired
    replacedBy: null               # required when status != active
    params: [from, to, scope]      # optional: documented param names
    history: []                    # optional: [{version, change}]
```

Validation rules: unique `code`; `area` matches code; `status != active` requires `replacedBy` (a code or null for retired-without-replacement); `retired` entries are never removed.

## 7. Initial registry

`Exit` is the `exitClass`: `-` none, `E` error, `B` blocked-by-manual, `D` drift, `U` usage (see `exit-codes.md`). `Fid` abbreviations: ex = exact, ap = approximated, nm = needs-manual, un = unsupported, llm.

### CFG

| Code | Sev | Fid | Exit | Title | Remediation |
|---|---|---|---|---|---|
| TRL-CFG-0001 | error | - | U | Configuration file not found or unreadable | Check the path passed with `-c`/`--config` and file permissions. |
| TRL-CFG-0002 | error | - | U | Configuration does not match the schema | Fix the reported key; run `trasloco config validate`. |
| TRL-CFG-0003 | warning | - | - | Unknown configuration key ignored | Remove or correct the key; it has no effect. |
| TRL-CFG-0004 | error | - | U | Conflicting options | Use only one of the conflicting flags/keys. |
| TRL-CFG-0005 | error | - | U | File schemaVersion is newer than supported | Upgrade trasloco or regenerate the file with this version. |
| TRL-CFG-0006 | info | - | - | File schemaVersion older, upgraded in memory | Run `trasloco upgrade <file>` to persist the new version. |
| TRL-CFG-0007 | error | - | U | Required option missing | Provide the option via flag, environment or config file. |

### SEL

| Code | Sev | Fid | Exit | Title | Remediation |
|---|---|---|---|---|---|
| TRL-SEL-0001 | error | - | U | Selector syntax error | Fix the selector per `selectors.md`; percent-encode special characters. |
| TRL-SEL-0002 | error | - | U | Unknown platform scheme in selector | Use a supported scheme: `ado`, `github`, `ghes`. |
| TRL-SEL-0003 | warning | - | - | Include selector matched nothing | Check spelling/case and that the credential can see the resource. Use `--strict-selectors` to make this an error. |
| TRL-SEL-0004 | error | - | U | Selection has no include entries | Add at least one non-excluded selector. |
| TRL-SEL-0005 | error | - | U | Stable ID not found | Verify the ID on the source platform; IDs are not names. |
| TRL-SEL-0006 | error | - | U | Pipeline name matches more than one pipeline where one is required | Use `#id:<n>` to select the pipeline unambiguously. |
| TRL-SEL-0007 | error | - | U | Selection file unreadable or too large | Check the path; limit is 100000 lines / 4 MiB. |
| TRL-SEL-0008 | error | - | U | Selector does not belong to the configured source endpoint | Use selectors with the same platform and host as `--from`. |

### AUTH

| Code | Sev | Fid | Exit | Title | Remediation |
|---|---|---|---|---|---|
| TRL-AUTH-0001 | error | - | E | No credential configured for endpoint | Provide a credential (token env var, GitHub App, service principal). |
| TRL-AUTH-0002 | error | - | E | Credential rejected (HTTP 401) | Check expiry and scope; rotate the credential. |
| TRL-AUTH-0003 | error | - | E | Insufficient permission (HTTP 403) | Grant the permission named in `params.required` and re-run preflight. |
| TRL-AUTH-0004 | warning | - | - | Deprecated authentication method in use | Switch to a GitHub App or an Entra service principal / workload identity. |
| TRL-AUTH-0005 | error | - | E | Organization requires SSO authorization of the credential | Authorize the token for the organization's SSO and retry. |
| TRL-AUTH-0006 | error | - | E | Duplicate credential actor for target scope | Use one credential per actor, host, and target scope; do not pool tokens to multiply rate-limit throughput. |

### HTTP

| Code | Sev | Fid | Exit | Title | Remediation |
|---|---|---|---|---|---|
| TRL-HTTP-0001 | info | - | - | Request throttled by rate limit | None; the engine waits. Do not add tokens for the same actor to multiply throughput. |
| TRL-HTTP-0002 | error | - | E | Retries exhausted | Check connectivity and platform status; use `resume`. |
| TRL-HTTP-0003 | error | - | E | Pagination loop detected | Report a bug with the redacted request log; the response repeated a continuation token. |
| TRL-HTTP-0004 | error | - | E | TLS verification failed | Install the CA via `--ca-file`; do not disable verification (no such option exists). |
| TRL-HTTP-0005 | error | - | E | Unexpected response shape | Check the platform version; report a bug if supported. |

### SRC

| Code | Sev | Fid | Exit | Title | Remediation |
|---|---|---|---|---|---|
| TRL-SRC-0001 | error | - | E | Source file or definition not found | Verify the path/ref; the resource may have been deleted since discovery. |
| TRL-SRC-0002 | error | - | E | Pipeline repository cannot be resolved | Check the repository reference (project-qualified) and permissions. |
| TRL-SRC-0003 | warning | nm | B | Classic pipeline has no YAML export | Convert manually or recreate as YAML; the inventory keeps the definition JSON. |
| TRL-SRC-0004 | info | - | - | Repository is empty or has no default branch | No pipelines can exist; repository is inventoried as `empty`. |
| TRL-SRC-0005 | info | - | - | Object omitted by selection scope | None; listed so omissions are visible. |
| TRL-SRC-0006 | warning | nm | B | Source construct is inventory-only (e.g. classic release) | Handle manually using the inventory. |

### PARSE

| Code | Sev | Fid | Exit | Title | Remediation |
|---|---|---|---|---|---|
| TRL-PARSE-0001 | error | - | E | YAML syntax error | Fix the syntax at the reported line/column. |
| TRL-PARSE-0002 | warning | - | - | Unknown key preserved as extension | Check for typos; the key is carried in `x-<provider>` extensions. |
| TRL-PARSE-0003 | error | - | E | Duplicate mapping key | Remove the duplicate key. |
| TRL-PARSE-0004 | warning | - | - | Invalid UTF-8 replaced | Re-save the file as UTF-8. Lossy decoding is reported, never silent. |
| TRL-PARSE-0005 | error | - | E | File exceeds size limit | Split the file; limit is 20 MiB per file. |
| TRL-PARSE-0006 | error | - | E | YAML value has unexpected type | Use the value type required by the dialect at this position. |
| TRL-PARSE-0007 | error | - | E | YAML alias expansion limit exceeded | Reduce aliases or raise the trusted-input expansion limit. |
| TRL-PARSE-0008 | error | - | E | YAML anchors and aliases are not allowed | Remove the anchor or alias for this YAML dialect. |
| TRL-PARSE-0009 | error | - | E | YAML merge keys are not allowed | Replace the merge key with explicit mapping entries. |
| TRL-PARSE-0010 | error | - | E | YAML nesting depth limit exceeded | Reduce nesting or raise the trusted-input depth limit. |
| TRL-PARSE-0011 | error | - | E | Required key is missing | Add the required key at the reported position. |
| TRL-PARSE-0012 | error | - | E | Value is not one of the allowed values | Use one of the documented values for this key. |
| TRL-PARSE-0013 | error | - | E | Insertion directive is out of place | Place elseif and else directly after an if or elseif directive, and give each directive a body. |
| TRL-PARSE-0014 | error | - | E | Mapping selects more than one alternative | Keep exactly one selecting key, such as one step keyword or one pipeline shape. |
| TRL-PARSE-0015 | error | - | E | Task reference is malformed | Write the task as Name@MajorVersion, Name@Major.Minor.Patch, or GUID@MajorVersion. |

### EXPR

| Code | Sev | Fid | Exit | Title | Remediation |
|---|---|---|---|---|---|
| TRL-EXPR-0001 | error | - | E | Expression syntax error | Fix the expression at the reported column. |
| TRL-EXPR-0002 | warning | nm | B | Expression function has no target equivalent | Rewrite the logic manually; the step is emitted as a failing placeholder. |
| TRL-EXPR-0003 | warning | ap | - | Expression evaluation phase differs on target | Review: compile-time vs runtime evaluation semantics differ. |
| TRL-EXPR-0004 | warning | un | - | Expression cannot be translated | Rewrite manually; original text kept in a comment. |
| TRL-EXPR-0005 | error | - | E | Expression type error | Fix operand types per the dialect rules. |

### TPL

| Code | Sev | Fid | Exit | Title | Remediation |
|---|---|---|---|---|---|
| TRL-TPL-0001 | error | - | E | Template not found | Check the path, `@alias` and `ref`. |
| TRL-TPL-0002 | error | - | E | Template cycle detected | Remove the circular `template:` reference. |
| TRL-TPL-0003 | error | - | E | Template limit exceeded | Reduce nesting/size; limits: 100 files, 100 levels, 20 MiB. |
| TRL-TPL-0004 | warning | nm | B | Cross-repository template cannot be resolved | Grant read access or provide the repo via `--template-repo`; pipeline is blocked. |
| TRL-TPL-0005 | error | - | E | Template parameter type mismatch | Fix the value passed to the parameter. |
| TRL-TPL-0006 | warning | nm | B | Template path is dynamic | Make the path static or convert manually. |
| TRL-TPL-0007 | error | - | E | Template repository alias is unknown | Declare the alias under resources.repositories and map it to a source root. |
| TRL-TPL-0008 | error | - | E | Template parameter is unknown or missing | Pass only declared parameters and supply every parameter that has no default. |
| TRL-TPL-0009 | error | - | E | Template does not define the requested content | Reference a template whose top-level key matches the place it is used (steps, jobs, stages or variables). |
| TRL-TPL-0010 | warning | - | - | Duplicate mapping key produced by template expansion | Remove the duplicate; the last value wins in the resolved output. |

### VAR

| Code | Sev | Fid | Exit | Title | Remediation |
|---|---|---|---|---|---|
| TRL-VAR-0001 | warning | nm | B | Unresolved binding: no consumer can be determined | Bind the resource to a repository/environment in the mapping file; nothing was created. |
| TRL-VAR-0002 | warning | ap | - | Semantic collision resolved by deterministic rename | Review the rename decision; references were rewritten. |
| TRL-VAR-0003 | info | ex | - | Identical duplicate merged | None; provenance of all sources is retained. |
| TRL-VAR-0004 | warning | ap | - | Ambiguous precedence | Confirm the effective value shown in `params`. |
| TRL-VAR-0005 | warning | ap | - | Macro or workspace path inside a value translated | Review the translated value type (path/expression/literal). |
| TRL-VAR-0006 | warning | nm | B | Variable group name not statically resolvable | Provide the possible values via `variables.groupNameHints` or bind manually. |
| TRL-VAR-0007 | error | - | E | Unresolvable semantic collision | A reference cannot be rewritten; rename one variable in the source. |
| TRL-VAR-0008 | warning | ap | - | Queue-time or runtime-only variable cannot be migrated as a static value | Supply the value at run time (workflow input) or define it explicitly. |
| TRL-VAR-0009 | error | - | E | Variable name is invalid | Use only letters, digits, periods, and underscores in variable names, and a literal name for a variable group. |
| TRL-VAR-0010 | error | - | E | Queue-time variable rejected | Declare the variable in the pipeline UI and mark it settable at queue time, or remove the assignment. |
| TRL-VAR-0011 | error | - | E | Variable group is not authorized | Authorize the pipeline to use the variable group. |
| TRL-VAR-0012 | warning | ex | - | Logging-command variable mutation is not permitted | Add the variable to target.settableVariables or remove the logging command. |
| TRL-VAR-0013 | warning | ex | - | Read-only variable mutation ignored | Remove the mutation or use a different variable name. |
| TRL-VAR-0014 | error | - | E | Unexpanded template construct reached the variable evaluator | Expand template references and conditional insertions before evaluating variables. |
| TRL-VAR-0015 | warning | ap | - | Deployment output key depends on a resource that is not statically known | Use a literal environment resource name so the Deploy_<resource> or <hook>_<resource> output key can be derived. |
| TRL-VAR-0016 | warning | ap | - | Logging command is not provably emitted; variable recorded as unknown | Emit the setvariable command from a top-level echo line, or supply the observed value at run time. |
| TRL-VAR-0017 | warning | ap | - | Macro expansion exceeded the output bound | Shorten the variable values or the text; the expansion was cut at the bound. |
| TRL-VAR-0018 | warning | ap | - | Variable name contains a hyphen | Microsoft restricts variable names to letters, numbers, periods and underscores, but its own variable-group example uses hyphenated names; the name is accepted, so verify it on a live run or rename it. |

### NAME

| Code | Sev | Fid | Exit | Title | Remediation |
|---|---|---|---|---|---|
| TRL-NAME-0001 | info | ap | - | Name normalized for target | None; see rename decision in the plan. |
| TRL-NAME-0002 | warning | ap | - | Name collision resolved by suffix | Review; set an explicit name in the mapping file to override. |
| TRL-NAME-0003 | error | - | E | Name collision could not be resolved | Provide explicit target names for the colliding resources. |
| TRL-NAME-0004 | error | - | E | Reserved name or prefix | Choose another name; reserved names cannot be provisioned. |
| TRL-NAME-0005 | error | - | E | Name is empty after normalization | Provide an explicit target name. |
| TRL-NAME-0006 | info | ap | - | Name truncated with hash | None; the hash keeps the name unique and stable. |
| TRL-NAME-0007 | warning | - | - | RUNNER_ prefix may conflict with runner variables | Review the variable name for portability; the name is preserved. |

### CONV

| Code | Sev | Fid | Exit | Title | Remediation |
|---|---|---|---|---|---|
| TRL-CONV-0001 | warning | ap | - | Construct converted with approximation | Review the semantic difference described in `params.difference`. |
| TRL-CONV-0002 | warning | un | - | Construct unsupported on target | Emitted as an explicitly failing step; implement manually. |
| TRL-CONV-0003 | warning | nm | B | Construct requires manual conversion | Complete the manual task listed in the report. |
| TRL-CONV-0004 | info | ex | - | Implicit source semantics made explicit | None (e.g. implicit success gate, implicit checkout). |
| TRL-CONV-0005 | warning | un | - | Trigger/event has no target equivalent | Add the trigger manually or accept loss. |
| TRL-CONV-0006 | warning | ap | - | Schedule time zone converted | Review cron in UTC; DST behavior differs. |
| TRL-CONV-0007 | warning | ap | - | Runner/image mapped to a pinned label | Review the dated mapping (`*-latest` aliases diverge across platforms). |
| TRL-CONV-0008 | warning | ap | - | Source property has no IR field and is preserved in a provider extension | Review `params.construct`; the value is kept under the `x-azure-pipelines` extension and ignored by cross-dialect emitters. |
| TRL-CONV-0009 | info | ap | - | Azure Pipelines macro or runtime expression kept as literal text | Evaluate with the variable-precedence pass before emitting; the text is preserved verbatim. |
| TRL-CONV-0010 | error | - | E | Template construct reached normalization unresolved | Run the template resolver first; normalization requires a fully expanded pipeline. |

### CAT

| Code | Sev | Fid | Exit | Title | Remediation |
|---|---|---|---|---|---|
| TRL-CAT-0001 | warning | nm | B | Task or action not in catalog | Add a mapping via `--catalog-dir` or convert manually. |
| TRL-CAT-0002 | warning | ap | - | Task retired or deprecated | The replacement from the catalog was used; review. |
| TRL-CAT-0003 | error | - | E | Action not on allowlist | Add the action to the allowlist policy or choose another. |
| TRL-CAT-0004 | error | - | E | Action runtime not allowed | Only `node24`, composite and docker actions are allowed. |
| TRL-CAT-0005 | error | - | E | Catalog entry invalid | Fix the entry; run `trasloco catalog lint`. |
| TRL-CAT-0006 | info | ap | - | Image alias pinned from catalog date | None; the pinned label and mapping date are in `params`. |

### EMIT

| Code | Sev | Fid | Exit | Title | Remediation |
|---|---|---|---|---|---|
| TRL-EMIT-0001 | info | ap | - | Invalid target combination prevented | None; the emitter adjusted the output (e.g. removed `environment` from a reusable-workflow caller). |
| TRL-EMIT-0002 | error | - | E | Emitter produced schema-invalid output | Internal bug in the emitter; report with the redacted IR. |
| TRL-EMIT-0003 | warning | ap | - | Token permission widening requires approval | Approve with `--allow-permission-widening` or restructure. |
| TRL-EMIT-0004 | error | - | E | Local reference not present in planned tree | Add the referenced file to the migration or fix the path. |
| TRL-EMIT-0005 | error | - | E | Output path escapes output root | Org/project/repo names containing `..` or separators are rejected; rename. |

### VAL

| Code | Sev | Fid | Exit | Title | Remediation |
|---|---|---|---|---|---|
| TRL-VAL-0001 | error | - | E | Linter finding in emitted workflow | See `message`; severity may be lowered per rule in config. |
| TRL-VAL-0002 | error | - | E | Emitted file fails the workflow schema | Internal bug or catalog error; report. |
| TRL-VAL-0003 | warning | nm | B | Required secret has no value source | Supply via `secrets set`; workflows stay gated. |
| TRL-VAL-0004 | error | - | E | Reusable workflow contract mismatch | Fix inputs/secrets/outputs between caller and callee. |
| TRL-VAL-0005 | error | - | E | Emitted ADO YAML fails the service schema | Internal bug or catalog error; report. |
| TRL-VAL-0006 | info | - | - | Remote validation unavailable | Re-run with `--verify-remote` and a reachable organization. |
| TRL-VAL-0007 | error | - | E | IR schema version or required field is invalid | Regenerate the IR with the current compiler and provide every required field. |
| TRL-VAL-0008 | error | - | E | IR identifier is duplicated | Assign a unique stable identifier within the reported IR namespace. |
| TRL-VAL-0009 | error | - | E | IR job dependency graph contains a cycle | Remove the reported dependency edge so the job graph is acyclic. |
| TRL-VAL-0010 | error | - | E | IR reference cannot be resolved | Correct the reference or retain the source construct as an explicit raw node. |
| TRL-VAL-0011 | error | - | E | IR stages do not partition jobs | Assign every job to exactly one stage and remove unknown stage members. |
| TRL-VAL-0012 | error | - | E | IR node shape or enum value is invalid | Use a documented enum and set exactly the payload selected by the node kind. |

### PLAN

| Code | Sev | Fid | Exit | Title | Remediation |
|---|---|---|---|---|---|
| TRL-PLAN-0001 | error | - | D | Plan stale: source changed | Re-run `plan`. |
| TRL-PLAN-0002 | error | - | D | Plan stale: target changed (ETag/branch SHA) | Re-run `plan`. |
| TRL-PLAN-0003 | error | - | D | Tool or catalog version differs from plan | Re-plan with the current version or use the version recorded in the plan. |
| TRL-PLAN-0004 | error | - | E | Plan schemaVersion unsupported | Re-generate the plan with this version. |
| TRL-PLAN-0005 | error | - | E | Conflict with an existing target resource not owned by the tool | Choose `adopt`, rename, or remove the resource manually. |
| TRL-PLAN-0006 | error | - | E | Plan contains forbidden content | Internal invariant: a secret-like value reached the plan; report immediately. |

### APPLY

| Code | Sev | Fid | Exit | Title | Remediation |
|---|---|---|---|---|---|
| TRL-APPLY-0001 | error | - | E | Resource apply failed (retryable) | Run `resume`. |
| TRL-APPLY-0002 | warning | - | - | Dependent subtree skipped after prerequisite failure | Fix the prerequisite error and `resume`. |
| TRL-APPLY-0003 | info | - | - | Direct write rejected; delivered via branch and PR | None; review and merge the PR. |
| TRL-APPLY-0004 | error | - | E | Ledger error | Check disk space/permissions; do not delete the ledger. |
| TRL-APPLY-0005 | error | - | E | Another run holds the lock | Wait or pass `--break-lock` only if the other process is dead. |
| TRL-APPLY-0006 | warning | - | - | Run interrupted | Run `resume --run <id>`. |
| TRL-APPLY-0007 | warning | - | - | Run finished partially | See the report for failed units. |
| TRL-APPLY-0008 | error | - | E | Unreconciled mutation intent | Read the target state and record an outcome before retrying the mutation. |
| TRL-APPLY-0009 | error | - | E | Secret detected in ledger previous value | Remove secret-bearing fields; secrets must never be persisted for rollback. |
| TRL-APPLY-0010 | error | - | E | Ledger schema mismatch | Use a compatible trasloco version; do not edit or delete the ledger. |

### SECRET

| Code | Sev | Fid | Exit | Title | Remediation |
|---|---|---|---|---|---|
| TRL-SECRET-0001 | warning | nm | B | Secret value not supplied | Provide a value source (`env:`, `file:`, `vault://`) and run `secrets set`. |
| TRL-SECRET-0002 | error | - | E | Secret value source unreadable | Check the source path/credentials. |
| TRL-SECRET-0003 | warning | - | - | Existing secret overwritten; previous value cannot be restored | Rollback will report, not restore, this secret. |
| TRL-SECRET-0004 | error | - | E | Secret canary detected in output | Internal security bug; stop and report. |
| TRL-SECRET-0005 | warning | nm | B | Workflows gated until required secrets are set | Run `secrets set`, then enable the workflows. |

### IDENT

| Code | Sev | Fid | Exit | Title | Remediation |
|---|---|---|---|---|---|
| TRL-IDENT-0001 | warning | nm | B | Identity not mapped | Add the mapping to `identities.yaml`; approvers/permissions were not created. |
| TRL-IDENT-0002 | error | - | E | Ambiguous identity mapping | Make the mapping unique (one target per source). |
| TRL-IDENT-0003 | error | - | E | identities.yaml invalid | Fix per `identities.md`. |
| TRL-IDENT-0004 | warning | nm | B | Cloud federation must be created or verified manually | Create the federated credential; verify with `verify`. |
| TRL-IDENT-0005 | error | - | E | Issuer/subject could not be read from the platform | Check permissions; values are never constructed locally. |
| TRL-IDENT-0006 | error | - | E | Federated credential limit reached for the application | Use another app registration (limit is 20 per app; VERIFY). |

### REPO

| Code | Sev | Fid | Exit | Title | Remediation |
|---|---|---|---|---|---|
| TRL-REPO-0001 | error | - | E | File exceeds target size limit | Move to LFS or remove from history (100 MiB per file on GitHub). |
| TRL-REPO-0002 | error | - | E | Push exceeds target size limit | Use another RepoMigrator provider or split pushes. |
| TRL-REPO-0003 | error | - | E | Target repository not empty | Choose `repoContent: none` or an empty target. |
| TRL-REPO-0004 | warning | - | - | Submodules detected | URLs are not rewritten in history; review. |
| TRL-REPO-0005 | warning | - | - | Git LFS objects present | LFS is migrated separately; verify with `verify`. |
| TRL-REPO-0006 | error | - | E | Target rejected refs | See the rejected ref list; adjust refspecs or rules. |

### CUTOVER

| Code | Sev | Fid | Exit | Title | Remediation |
|---|---|---|---|---|---|
| TRL-CUTOVER-0001 | error | - | E | Cutover phase out of order | Run phases in order: seed, dual-run, freeze, sync, switch. |
| TRL-CUTOVER-0002 | error | - | E | Ref verification mismatch after sync | Re-run `sync`; do not `switch`. |
| TRL-CUTOVER-0003 | error | - | E | Source freeze failed | Check permissions; nothing was switched. |
| TRL-CUTOVER-0004 | warning | - | - | Only the first connected ADO organization receives triggers | Declare this in the dual-run plan. |
| TRL-CUTOVER-0005 | warning | - | - | Dual-run may cause duplicate deployments | Keep deploy stages gated until `switch`. |

### LLM

| Code | Sev | Fid | Exit | Title | Remediation |
|---|---|---|---|---|---|
| TRL-LLM-0001 | error | - | E | Remote data transfer not allowed | Set `llm.allowRemoteData: true` or use a local provider. |
| TRL-LLM-0002 | warning | nm | B | LLM output not accepted | Pass `--accept-llm` or approve the entry. Provenance is `llm`. |
| TRL-LLM-0003 | error | - | E | LLM output failed validation | Output discarded; convert manually. |
| TRL-LLM-0004 | error | - | E | LLM provider error | Check provider configuration and quota. |

### INT

| Code | Sev | Fid | Exit | Title | Remediation |
|---|---|---|---|---|---|
| TRL-INT-0001 | error | - | E | Internal error (recovered panic) | Report a bug with the run report and `--debug` log. |
| TRL-INT-0002 | error | - | E | Internal invariant violated | Report a bug. |
| TRL-INT-0003 | error | - | E | Feature not implemented for this platform pair | See the capability matrix. |
| TRL-INT-0004 | warning | - | - | Event sink failed and was detached | Check the sink destination (disk space, closed pipe); the run continues without this sink. |

Total: 166 codes.

## 8. Test vectors

| ID | Input | Expected |
|---|---|---|
| DIAG-001 | `TRL-NAME-0002` | valid code |
| DIAG-002 | `TRL-RLY-0001` | invalid (prefix) |
| DIAG-003 | `TRL-NAME-2` | invalid (digits) |
| DIAG-004 | `TRL-XXX-0001` | invalid (unknown area) |
| DIAG-005 | registry entry with `status: retired` and no `replacedBy` key | schema error |
| DIAG-006 | instance with `severity: info` for TRL-PLAN-0001 | rejected by constructor; severity fixed by code |
| DIAG-007 | two diagnostics, same file, lines 3 and 10 | ordered 3 then 10 |
| DIAG-008 | diagnostic without `source` | sorts after all with `source` |
| DIAG-009 | `docsUrl` for `TRL-VAR-0003` | `https://github.com/HarnageaGabriel/trasloco/blob/main/docs/diagnostics/TRL-VAR-0003.md` |
| DIAG-010 | message containing a canary secret | redacted before storage; test fails if canary survives |
| DIAG-011 | 1,001 distinct instances of one code | store 1,000; summarize one suppressed instance |

`needs-manual` contributes exit 3 only for `plan` and `apply`. `audit`, `convert`, and `modernize` return 0 with warnings unless `--fail-on manual|warning` exceeds a policy threshold, which returns exit 5.
