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

UI pipeline variables form the base below YAML-scoped definitions. Variable-group values form a base only at the position imported; a YAML declaration at the same or more local scope can override them. Queue-time values override UI values but YAML definitions at every scope retain priority ([Microsoft: variable expansion precedence](https://learn.microsoft.com/en-us/azure/devops/pipelines/process/variables#expansion-of-variables), [Microsoft: allow queue-time values](https://learn.microsoft.com/en-us/azure/devops/pipelines/process/variables#allow-at-queue-time)). When the organization limits queue-time variables, an undeclared or non-settable assignment MUST be rejected. When it does not, queue-time data MAY create a variable or override a UI/system value, but still loses to YAML ([Microsoft: limit variables set at queue time](https://learn.microsoft.com/en-us/azure/devops/pipelines/security/inputs#limit-variables-that-can-be-set-at-queue-time)). System variables are read-only at runtime unless vendor semantics explicitly permit an earlier override; documented YAML exceptions include `Build.Clean` and `System.Debug`. VERIFY: reconcile the broader queue-time/system-variable wording with the read-only predefined-variable tables.

For deterministic evaluation, precedence from lowest to highest is:

1. UI pipeline variables;
2. allowed queue-time values;
3. pipeline-root YAML sequence in declaration order, with groups and templates expanded at their declaration positions;
4. stage YAML sequence in declaration order, with groups and templates expanded likewise;
5. job YAML sequence in declaration order, with groups and templates expanded likewise;
6. runtime logging-command mutation beginning with the next step in its platform-defined visibility ([Microsoft: set a job-scoped variable](https://learn.microsoft.com/en-us/azure/devops/pipelines/process/set-variables-scripts#set-a-job-scoped-variable-from-a-script)). Macro expansion and the current process environment therefore retain the pre-mutation value throughout the setting step.

An `isOutput=true` logging command creates a read-only variable; subsequent tasks cannot overwrite it ([Microsoft: protect variables](https://learn.microsoft.com/en-us/azure/devops/pipelines/security/inputs#variables)). Logging-command escaping follows the agent reference implementation: `%3B`, `%0D`, `%0A`, and `%5D` are decoded first, then `%AZP25`; other percent sequences remain data ([microsoft/azure-pipelines-agent `CommandStringConvertor.Unescape`](https://github.com/microsoft/azure-pipelines-agent/blob/master/src/Agent.Sdk/CommandStringConvertor.cs)). This source is a reference implementation, not a separate normative contract.

The apparent ordering of groups versus UI values varies by how ADO materializes a definition. The adapter MUST retain each provider-reported source category and apply the sequence above. VERIFY: confirm UI-versus-group precedence and same-scope multiple-group collision behavior; public documentation warns some group collisions may be unpredictable. If the endpoint cannot prove order, emit `TRL-VAR-0004` and require an explicit decision for differing values.

`settableAtQueueTime` is metadata on a UI variable definition, not a new scope. Queue-time data MUST NOT create an undeclared variable when the organization restricts queue-time variables; without that restriction it may do so. VERIFY: exact behavior on supported ADO Server versions.

Secret bindings remain provider references in every phase. Runtime `$[ ]` and compile-time `${{ }}` expression namespaces do not expose secret values; a missing/invisible secret lookup evaluates to null. VERIFY: live-service visibility of secrets in `$[ ]`.

Fork pull-request builds: Azure Pipelines withholds pipeline secrets from builds of forks unless the pipeline's "make secrets available to builds of forks" setting is on ([Microsoft: contributions from forks](https://learn.microsoft.com/en-us/azure/devops/pipelines/repos/github#contributions-from-forks)). The evaluator context therefore carries both facts, `ForkPullRequest` and `SecretsAvailableToForks`, and the zero value withholds secrets. When a fork build has no secret access, one policy applies to every secret source: UI secrets, imported variable-group secrets, queue-time secrets and provider-backed secret outputs are all absent from the graph (a reference to them stays literal), while non-secret variables and secrets that a step itself declares with a logging command are unaffected. VERIFY: behavior of variable-group secrets under each organization-level fork-protection setting.

Microsoft states that variable names contain only letters, numbers, periods and underscores ([Microsoft: variable naming restrictions](https://learn.microsoft.com/en-us/azure/devops/pipelines/process/variables#variable-naming-restrictions)), yet its own variable-group example uses hyphenated names such as `my-passed-variable` ([Microsoft: use variables in a linked variable group](https://learn.microsoft.com/en-us/azure/devops/pipelines/library/variable-groups#use-variables-in-a-linked-variable-group)). Maintainer decision: a name containing `-` is accepted and bound normally, with the warning `TRL-VAR-0018`; other characters outside the grammar are still rejected with `TRL-VAR-0009`. VERIFY: confirm hyphenated names on a live service. Variable-group identifiers are validated separately: they must be a literal (hyphens are allowed, as in the documented `my-variable-group`), and a dynamic or empty group name is rejected with `TRL-VAR-0009`.

Static `##vso[task.setvariable]` extraction applies a mutation only when the command is provably emitted, in an enabled step with no variable-dependent condition. Provability is an allowlist (maintainer decision A): a logging-command emission is proven only if every line of the script up to and including that emission belongs to the grammar of the step's shell:

The grammar is deliberately minimal (maintainer decision, review r6):

- bash: blank lines; full-line `#` comments that do not end in `\`; the exact lowercase command `echo` followed by one double- or single-quoted ASCII argument, with nothing after the closing quote. No `printf`, no `set -x`/`-v`/`+x`/`+v`.
- pwsh (`pwsh:`, `powershell:`, `PowerShell@2`, `AzurePowerShell@5`): blank lines; full-line `#` comments other than `#requires`; `Write-Host` or `Write-Output` (any case) followed by one single- or double-quoted ASCII argument, with nothing after it. No `echo`, no `print`, no `Set-PSDebug`.
- cmd: blank lines; `REM` by itself or `REM ` followed only by `[A-Za-z0-9 .,_-]`; `@echo off`; `echo ` followed by one unquoted ASCII argument that contains none of `%`, `!`, `^`, `&`, `|`, `<`, `>`, `"`. No `::` comments, no quoted arguments.

A quoted argument (bash, pwsh) contains no quote character of either kind, no backtick, no control or non-ASCII character (smart quotes included) and no `$`; bash also excludes `\` and `!`. On every shell, a raw physical line containing a control character other than tab ends provability; the CR in a CRLF line ending is not part of the physical line. Only ASCII space, tab, CR, and LF are whitespace for blank-line recognition, so a line containing another Unicode space is not blank. Emission analysis never expands macros. Any `$(` on a script line (macro-shaped or not, comments included) ends provability for that line and every later line, and a `$(` anywhere inside a marker (header or value) makes that emission Unknown with `TRL-VAR-0016`. A logging-command marker counts only if the full prefix `##vso[task.setvariable ` is literal on the line (case-insensitive); a line that forms the prefix only once a macro is removed is not a marker and warns the step. On cmd a physical line longer than 8191 bytes ends provability (cmd truncates it and aborts). Whitespace splitting (`@echo off`) uses ASCII space and tab only. Macro-based injection of logging commands is a documented limit: when a script contains any `$(name)` macro whose value is unknown, secret, overflowed or known to contain `##vso[`, the step gets one step-level `TRL-VAR-0016` warning ("macro values may inject logging commands"); no variable name is guessed. Vectors VARV-060 and VARV-085 therefore stay pinned disagreements (the documented runtime value cannot be proven statically). A macro in a header property name (`$(p)=true`) makes the command conservatively secret, and a macro in the `variable=` name records nothing and warns the step. For bash, `echo` followed by one bare word of `[A-Za-z0-9._/:=-]` and nothing else is also an allowed line. A marker must be quoted in bash and pwsh (an unquoted `#` starts a comment). Any other line (any other command or spelling, `printf`, `command`, `builtin`, `eval`, `source`, `exec`, function definitions, control flow, assignments, pipes, redirections, subshells, here-docs, quoted command names, trailing commands or comments) ends provability: every later marker is recorded as an unknown mutation (the name resolves to no value and macros stay literal) with `TRL-VAR-0016`, as is a marker that is not provable for any other reason (comment, branch, loop, function, heredoc, after an `exit`). A step with `enabled:` that is not a literal boolean is recorded as unknown, and so is a step whose shell cannot be determined (for example `AzureCLI@2`).

Value expansion depends on the shell (decision B). In bash and pwsh a payload holding `$` or a backtick, or an unquoted glob, `;`, `&`, `|`, `<`, `>`, `\` or `~`, is Unknown with `TRL-VAR-0016`. In cmd only `%NAME%` and `!NAME!` are expansions (Unknown with `TRL-VAR-0016`); `$`, backtick, backslash, `*`, `?` and `~` are literal. A marker value holding any macro is Unknown (macros are not expanded, so a secret macro never contributes characters to a value). A header value with a macro (`issecret=$(f)`, `isOutput=$(o)`) is not expanded: the command is Unknown; `issecret=$(f)` is conservatively secret and `isOutput=$(o)` is conservatively an output. Duplicate logging-command properties emit `TRL-VAR-0016`; `issecret` is true if any occurrence is true, and `isOutput` is true if any occurrence is true. A logging command whose variable name is not a valid name is reported with `TRL-VAR-0016` and skipped; it does not abort the evaluation. Every Unknown value carries a diagnostic; none is silent.

Review scope (decision C): a review blocks only on (a) a declared-secret leak, (b) a crash, panic or resource exhaustion, (c) silent semantic corruption (a wrong value applied without a diagnostic). An over-conservative Unknown with a diagnostic is acceptable.

Every ingestion path (YAML, matrix, UI, group, queue-time, predefined and logging commands) validates the bare variable name with the name grammar: a hyphen is accepted with `TRL-VAR-0018`; any other invalid character is rejected with `TRL-VAR-0009` and the candidate is skipped.

The shell of a `script:` step and of `CmdLine@2` depends on the agent: cmd.exe on Windows, bash on Linux and macOS. The evaluator context carries `AgentOS` (`windows`, `linux`, `macos`, default unknown). Unquoted `echo ##vso[...]` is a comment in bash, so it is unknown there; on cmd it is emitted, and cmd prints quote characters literally, so `echo "##vso[...]v"` yields the value `v"`. With an unknown agent OS the mutation is recorded as unknown with `TRL-VAR-0016`. `bash:` and `pwsh:`/`powershell:` steps are not OS-dependent.

A pipeline that still holds `extends:` is rejected with `TRL-VAR-0014`, like any other unexpanded template construct.

Macro expansion output is capped at 1 MiB (`expr.MaxEvaluationMemory`) for every write, including literal spans and suffixes. Overflow cuts the text at the bound and reports `TRL-VAR-0017` once per expansion. A truncated value is never used as valid: `EnvironmentEntries` marks the entry Unknown (`Environment` returns an error), and a logging-command value that overflows is recorded as an unknown variable with `TRL-VAR-0017`. Exhausting the expansion work budget also reports `TRL-VAR-0017`.

### Last-wins boundary

Last-wins applies only within one ordered scope/phase. It MUST NOT make an inaccessible stage/job value visible elsewhere, cross dependency boundaries, or select between unrelated variable groups whose order is unavailable. Output visibility is one dependency hop, not transitive. A stage without an explicit `dependsOn` depends on the immediately preceding stage, so that implicit edge carries outputs ([Microsoft: output variables across stages](https://learn.microsoft.com/en-us/azure/devops/pipelines/process/variables#use-output-variables-from-tasks)).

Deployment output keys follow [Microsoft: support for output variables](https://learn.microsoft.com/en-us/azure/devops/pipelines/process/deployment-jobs#support-for-output-variables): `runOnce` uses `<job>.<step>.<variable>`, and `Deploy_<resource>.<step>.<variable>` when the environment names a resource; `canary` uses `<hook>_<increment>.<step>.<variable>`; `rolling` uses `<hook>_<resource>.<step>.<variable>`. When the resource is needed for the key but is not a statically known literal, no key is invented: the evaluator reports `TRL-VAR-0015` and records no output. VERIFY: live-service output-key shapes for `runOnce` hooks other than `deploy` (they keep the hook name), and for canary and rolling `preDeploy`, `routeTraffic`, `postRouteTraffic`, and `on.*` hooks.

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

Azure automatic environment projection uppercases variable names and replaces periods with underscores. Explicit `env:` keys remain as written. VERIFY: live-agent behavior for spaces in variable names and explicit environment names before adding any normalization beyond periods.

## Test vectors

| ID | Input | Expected |
|---|---|---|
| VAR-001 | ADO root `X=root`, stage `X=stage`, job `X=job` | job sees `job` |
| VAR-002 | ADO root `X=root`, stage `X=stage` | stage jobs see `stage`; other stages see `root` |
| VAR-003 | ADO same root sequence `X=one`, then `X=two` | `two` |
| VAR-004 | ADO group G1 `X=one`, then group G2 `X=two` in same sequence | `two` when declaration order is proven |
| VAR-005 | ADO group G1 `X=one`, then direct `X=two` | `two` |
| VAR-006 | ADO direct `X=two`, then group G1 `X=one` | `one` |
| VAR-007 | ADO job `X=job`, allowed queue value `X=queued` ([Microsoft: expansion precedence](https://learn.microsoft.com/en-us/azure/devops/pipelines/process/variables#expansion-of-variables)) | `job`; YAML job scope remains above queue-time input |
| VAR-008 | ADO queue value for `X` not settable while the organization limits queue-time variables ([Microsoft: limit queue-time variables](https://learn.microsoft.com/en-us/azure/devops/pipelines/security/inputs#limit-variables-that-can-be-set-at-queue-time)) | reject evaluation with `TRL-VAR-0010`; do not return a partial graph |
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
