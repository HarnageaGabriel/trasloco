# Variable precedence and scope projection

Status: draft for approval (M0). Normative.

The evaluator produces an effective binding graph; it does not flatten names globally. Every binding retains source, declaration index, phase, scope, secrecy, mutability, and consumer set. Secret values MUST never enter the graph—only secret references and content-independent metadata.

## Common model

A binding key is compared using the source platform's case rule. A candidate is:

```text
(name, kind, sourceScope, sourceId, declarationIndex, phase, valueRef, isSecret,
 settableAtQueueTime, consumers, provenance)
```

`kind` is `literal|expression|secret-ref|runtime-input|external-store-ref`. The evaluator MUST resolve only values available in the relevant phase. It MUST retain shadowed candidates in provenance.

Identical duplicates have equal comparison key, kind, non-secret canonical value digest, secrecy flag, and target consumer set. They MAY merge with all provenance retained (`TRL-VAR-0003`). Same-name candidates differing in any of those semantics are collisions and MUST be resolved by precedence or an explicit scope/name decision; they MUST NOT be silently merged.

Reports represent non-secret value equality only with a domain-separated hash salted by fresh cryptographic randomness per run. The salt and raw values MUST NOT appear in reports or events. Hashes therefore support equality comparisons within one run but MUST NOT enable correlation across runs. Secret references are never value-hashed.

## Azure Pipelines evaluator

### Scope precedence

For a job, most local YAML scope wins: job, then stage, then pipeline root. Within one YAML scope, later declarations win. A direct YAML variable declaration and a variable-group import participate at their declaration position; group members are expanded as a block. Later groups therefore override earlier groups at the same scope, and a later direct variable overrides earlier group values.

Template variables are expanded into the declaration position of the template reference. Compile-time template expressions use the bindings visible at expansion time. Macro `$(name)` and runtime expressions use runtime bindings; an output variable is available only along its declared dependency edge and in its documented downstream syntax.

UI pipeline variables form the base below YAML-scoped definitions. Variable-group values form a base only at the position imported; a YAML declaration at the same or more local scope can override them. Queue-time values override UI and YAML only when the variable is explicitly permitted to be set at queue time. A disallowed queue-time assignment MUST be rejected, not ignored. System variables are read-only unless vendor semantics explicitly permit override.

For deterministic evaluation, precedence from lowest to highest is:

1. UI pipeline variables;
2. pipeline-root YAML sequence in declaration order, with groups and templates expanded at their declaration positions;
3. stage YAML sequence in declaration order, with groups and templates expanded likewise;
4. job YAML sequence in declaration order, with groups and templates expanded likewise;
5. allowed queue-time override;
6. runtime logging-command mutation from the current step forward in its platform-defined visibility.

The apparent ordering of groups versus UI values varies by how ADO materializes a definition. The adapter MUST retain each provider-reported source category and apply the sequence above. VERIFY: confirm UI-versus-group precedence and same-scope multiple-group collision behavior; public documentation warns some group collisions may be unpredictable. If the endpoint cannot prove order, emit `TRL-VAR-0004` and require an explicit decision for differing values.

`settableAtQueueTime` is metadata on a UI variable definition, not a new scope. Queue-time data MUST NOT create an undeclared variable when the organization restricts queue-time variables. VERIFY: exact behavior on supported ADO Server versions.

### Last-wins boundary

Last-wins applies only within one ordered scope/phase. It MUST NOT make an inaccessible stage/job value visible elsewhere, cross dependency boundaries, or select between unrelated variable groups whose order is unavailable.

## GitHub Actions evaluator

Workflow `env`, job `env`, and step `env` form a lexical chain: step overrides job, job overrides workflow for that step. A value written to `$GITHUB_ENV` is available to subsequent steps in the same job, not the writing step, other jobs, or expression evaluation that occurred earlier. Default `GITHUB_*` and `RUNNER_*` environment variables MUST NOT be overwritten.

Configuration-variable candidates are selected by consumer scope: environment, then repository, then organization. Organization variables may be visibility-limited; invisible variables are absent, not empty. Environment candidates are phase-tagged because GitHub makes them available only after the job starts and documents that they do not overwrite earlier `env`/`vars` context values. The evaluator MUST NOT collapse an environment candidate with the repository/organization candidate before expression phase is known. VERIFY: confirm exact environment-variable visibility and same-name behavior on GitHub.com and each supported GHES version.

Secrets resolve by consumer: environment secret overrides repository secret, which overrides organization secret. Organization secret visibility restrictions are part of the binding. Secrets are references; equality of values cannot be used to merge them. Environment secrets and variables are attached only to jobs that declare that environment. Reusable-workflow secret passing is an explicit graph edge; environment secrets MUST NOT be assumed to flow through `secrets: inherit`.

Expression namespaces remain distinct: `env.X`, `vars.X`, and `secrets.X` MUST NOT be collapsed merely because `X` matches.

## Projection to target scopes

### ADO to GitHub

1. Compute consumers after template expansion.
2. A binding used by exactly one environment/deployment intent SHOULD map to that GitHub environment.
3. A binding used across one repository with no narrower environment maps to repository scope.
4. A binding may map to organization scope only when source ownership is shared, all consumers are known, visibility can be expressed without widening, and one run-level policy opt-in permits organization projection. A per-binding override MAY deny or narrow that opt-in and MUST be recorded in the plan.
5. Job/stage-only non-secret bindings SHOULD remain generated workflow/job/step `env` when safe; they MUST NOT be provisioned as shared `vars` merely for convenience.
6. Queue-time values SHOULD become typed `workflow_dispatch` inputs when manually invoked and semantically represent user input; otherwise they remain unresolved runtime inputs.
7. Variable-group bindings with unknown consumers become a shared unit with unresolved edges. They MUST NOT be copied to every repository.

### GitHub to ADO

1. Workflow/job/step `env` remains root/job/step-local YAML at the narrowest equivalent scope.
2. Repository configuration variables map to a generated per-repository ADO variable group. The group is reusable by that repository's pipelines and MAY be linked to Key Vault under explicit secret policy; it MUST NOT be widened to other repositories implicitly.
3. Environment variables/secrets map to an environment-bound variable group or approved external store binding; the plan MUST include the ADO authorization edge.
4. Organization variables/secrets map to shared variable groups only when target project ownership and pipeline authorization are explicit.
5. Runtime inputs map to typed ADO parameters when compile-time use is required, otherwise to queue-time variables explicitly marked settable.
6. Unknown target project or consumer produces `TRL-VAR-0001`; no fan-out occurs.

Target naming follows `naming.md`. When two source bindings project to one target name/scope, identical duplicates merge; semantic collisions receive deterministic rename decisions and all statically resolvable references are rewritten. If a dynamic reference prevents safe rewriting, emit `TRL-VAR-0007` and block.

## Test vectors

| ID | Input | Expected |
|---|---|---|
| VAR-001 | ADO root `X=root`, stage `X=stage`, job `X=job` | job sees `job` |
| VAR-002 | ADO root `X=root`, stage `X=stage` | stage jobs see `stage`; other stages see `root` |
| VAR-003 | ADO same root sequence `X=one`, then `X=two` | `two` |
| VAR-004 | ADO group G1 `X=one`, then group G2 `X=two` in same sequence | `two` when declaration order is proven |
| VAR-005 | ADO group G1 `X=one`, then direct `X=two` | `two` |
| VAR-006 | ADO direct `X=two`, then group G1 `X=one` | `one` |
| VAR-007 | ADO job `X=job`, allowed queue value `X=queued` | `queued` |
| VAR-008 | ADO queue value for `X` not settable at queue time | reject assignment; source definition remains effective |
| VAR-009 | ADO template declares `X=t`, caller later declares `X=c` | `c` at runtime; template compile-time uses value visible at its expansion point |
| VAR-010 | ADO output set in job A, job B has declared dependency and valid output syntax | binding visible in B |
| VAR-011 | same output, job C lacks dependency | unresolved reference; not globally visible |
| VAR-012 | GHA workflow `env X=w`, job `X=j`, step `X=s` | step sees `s` |
| VAR-013 | GHA workflow `env X=w`, job `X=j`, step no X | step sees `j` |
| VAR-014 | GHA step writes `X=v` to `$GITHUB_ENV` then reads X in same step | new value unavailable in same step |
| VAR-015 | following step in same job after VAR-014 | sees `v` |
| VAR-016 | another job after VAR-014 | does not see `v` |
| VAR-017 | org/repo/environment `vars.X` all exist for an environment job | retain phase-tagged environment and pre-job candidates; resolve only at the consuming expression phase |
| VAR-018 | org/repo `vars.X`, no environment binding | repository value selected |
| VAR-019 | org secret invisible to repository | absent binding; never treated as empty string |
| VAR-020 | environment and repository secret `TOKEN` | environment secret reference for environment job |
| VAR-021 | `env.X`, `vars.X`, `secrets.X` | three distinct bindings |
| VAR-022 | ADO stage-only literal used by one generated job | inline target job `env`, not repository variable |
| VAR-023 | ADO variable group used by three known repos, run-level org sharing disabled | three planned repo bindings; decision recorded |
| VAR-024 | ADO variable group with unknown consumers | shared unresolved unit; `TRL-VAR-0001`; zero target copies |
| VAR-025 | ADO queue-time manual string input | GHA `workflow_dispatch.inputs` when manual-invocation policy applies |
| VAR-026 | GHA environment secret to ADO, target environment known | environment-bound secret reference plus authorization edge |
| VAR-027 | GHA organization variable, target project unknown | `TRL-VAR-0001`; no fan-out |
| VAR-028 | two equal non-secret bindings, same consumers and scope | merge; retain two provenances; `TRL-VAR-0003` |
| VAR-029 | same name/scope, differing literal digests | semantic collision; deterministic rename and reference rewrite |
| VAR-030 | collision with dynamic name lookup | `TRL-VAR-0007`; block |
| VAR-031 | two secret refs with same name but unknown values | not identical-duplicate merge; resolve by scope/identity |
| VAR-032 | GHA reusable workflow called with explicit secret mapping | create explicit binding edge only for mapped secret |
| VAR-033 | GitHub repository variable `X` projected to ADO | generated per-repository variable group binding |
| VAR-034 | equal non-secret values in one report | equal per-run salted hashes; no raw values or salt |
| VAR-035 | same non-secret value in two runs | unrelated salted hashes |

The marked ADO precedence details remain subject to live M1 conformance fixtures; adapters MUST use `TRL-VAR-0004` rather than guess until those fixtures pass for the target endpoint.
