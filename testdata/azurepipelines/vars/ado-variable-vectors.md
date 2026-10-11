# Azure Pipelines variable evaluator: authoritative test vectors

Status: independent differential authorship (2026-10-09). Derived only from Microsoft Learn pages (rendered
pages plus their Markdown sources in MicrosoftDocs/azure-devops-docs `main` and the YAML-schema pages,
ms.date up to 2026-09). **No trasloco code was read.** `docs/spec/variable-precedence.md` was read only to report
contradictions (section 3); no expected value below was taken from it.

Companion of `ado-expr-vectors.md` (expression-language functions). This table covers *variable binding*:
scope, order, phase, visibility, mutation, secrecy, queue-time, naming and environment projection.

## 0. Conventions

### Source abbreviations (column `source`)

Base = `https://learn.microsoft.com/en-us/azure/devops/pipelines/`. Quoted fragments are < 15 words.

| Prefix | Expands to |
|---|---|
| `vars#a` | Base + `process/variables#a` |
| `setvar#a` | Base + `process/set-variables-scripts#a` |
| `secret#a` | Base + `process/set-secret-variables#a` |
| `vg#a` | Base + `library/variable-groups#a` |
| `rtp#a` | Base + `process/runtime-parameters#a` |
| `texpr#a` | Base + `process/template-expressions#a` |
| `expr#a` | Base + `process/expressions#a` |
| `tmpl#a` | Base + `process/templates#a` |
| `predef#a` | Base + `build/variables#a` (predefined variables) |
| `log#a` | Base + `scripts/logging-commands#a` |
| `secin#a` | Base + `security/inputs#a` |
| `deploy#a` | Base + `process/deployment-jobs#a` |
| `ys-vars#a` | Base + `yaml-schema/variables#a` |
| `ys-name` | Base + `yaml-schema/variables-name` |
| `naming#a` | `https://learn.microsoft.com/en-us/azure/devops/organizations/settings/naming-restrictions#a` |

Anchor `#x` = Learn's auto-anchor of the heading text.

### Columns

- `phase` — the phase in which the claimed behavior is decided:
  - `compile`: template expansion (`${{ }}`, `template:` insertion, YAML structure/schema validation).
  - `macro`: agent `$(name)` substitution in a task input / script body / `env:` / `displayName`, done
    once at job start and again right before each step (vars#expansion-of-variables). Scope-precedence vectors whose
    consumer is `$(x)` are tagged `macro`.
  - `runtime`: `$[ ]` variable definitions, `condition:`, `dependencies` / `stageDependencies`.
  - `queue`: run creation (queue-time variables, runtime-parameter selection, resource authorization, org/project settings).
  - `logging`: effect of `##vso[...]` logging commands on later consumers.
- `YAML input` — `yaml:` a complete pipeline written as **JSON** (JSON is valid YAML 1.2; key order = YAML order;
  sequences = YAML lists, objects = mappings). Pipelines may use the single-job shortcut (`steps` at root) or `jobs`
  at root, both legal ADO shapes. `yaml-text:` (only where JSON cannot express the input, e.g. duplicate keys) is raw YAML
  with `\n` line breaks. `ctx:` (optional) is JSON with non-YAML context:
  - `ui`: pipeline-settings UI variables `{name:{"value":..,"secret":bool,"settable":bool}}` (`settable` = "Let users override this value when running this pipeline").
  - `groups`: library variable groups `{group:{var:{"value":..,"secret":bool}}}`; `authorized:false` on a group = pipeline not authorized.
  - `files`: other repo files `{path: <pipeline-fragment JSON>}` (templates).
  - `queue`: values supplied when the run is queued (UI "Variables" panel or Runs API) `{name:value}`; `params`: runtime-parameter values chosen at queue.
  - `settings`: org/project pipeline settings, e.g. `{"limitQueueTimeVariables":true}`.
  - `run`: predefined values the platform would set for this run, e.g. `{"Build.SourceBranch":"refs/heads/main"}`.
  - `fork`: `true` = run is a pull-request build from a fork of a GitHub repo.
- Script bodies are **not** executed by a shell. A harness treats every `echo '##vso[...]...'` / `echo "##vso[...]..."`
  line in a step as that step emitting the logging command; `echo X` otherwise means "the consumer is the text X after
  macro substitution".
- `consumer` — what reads the binding: `<stage>/<job>/step<N>` (0-based; stage omitted for jobs-only pipelines,
  both omitted for the single-job shortcut) followed by the reference: `$(x)` (macro), `${{ variables.x }}`, `$[ ... ]`
  (value of the variable defined by it), `env X` (process environment variable `X` of that step), `log` (what the
  pipeline log shows), `condition`, `run` (run creation outcome).
- `expected`:
  - `"v"` — exact string the consumer sees.
  - `empty` — empty string.
  - `literal` — the reference text is left unchanged (e.g. `$(x)` stays `$(x)`).
  - `env-absent` — no such environment variable in the step process (reads as empty in the shell).
  - `masked` — the value is usable but the log shows `***`.
  - `unchanged:"v"` — a mutation attempt has no effect; consumer still sees `v` (warning optional unless stated).
  - `reject:<class>` — compile/queue fails (`schema`, `auth`, `queue-var`, `param-value`, `template-content`).
  - `unavailable` — binding not visible to that consumer (rendering: `$[ ]` → empty, `$( )` → literal, `${{ }}` → empty).
  - `runs` / `skipped` — job/stage condition outcome.
- `confidence`: `docs` = stated by (or a direct reading of an example in) the cited page; `inferred` = follows from
  combining documented rules, docs do not state it; `uncertain` = docs silent or self-contradictory, row carries a
  `VERIFY:` note and is listed in section 4. A conforming implementation MUST pass all `docs` rows, SHOULD pass
  `inferred` rows, and MUST surface an explicit decision/diagnostic (not a silent guess) for `uncertain` rows until the
  live conformance run settles them.

### How a harness consumes rows

1. Parse every table in section 1 whose header is exactly `ID | phase | YAML input | consumer | expected | confidence | source`.
2. Build the run: parse `yaml:` (or `yaml-text:`), install `ctx` (UI vars, groups, files, queue values, settings,
   predefined `run` values; any predefined variable not listed may take any value).
3. Evaluate the consumer reference with the evaluator at the stated `phase`, after simulating every logging command
   emitted by earlier steps (and the same step where relevant).
4. Compare with `expected`. For `reject:` rows assert an error of that class and no partial binding graph. For `masked`
   rows assert the binding is a secret reference (the evaluator never holds the raw value) and the consumer is marked
   log-masked.
5. Rows with `confidence = uncertain` are reported, not failed, until settled by the live runs in section 4.

## 1. Vectors

### 1.1 Scope precedence and same-scope order

| ID | phase | YAML input | consumer | expected | confidence | source |
|---|---|---|---|---|---|---|
| VARV-001 | macro | yaml:`{"variables":{"a":"pipeline yaml"},"stages":[{"stage":"one","variables":[{"name":"a","value":"stage yaml"}],"jobs":[{"job":"A","variables":[{"name":"a","value":"job yaml"}],"steps":[{"bash":"echo $(a)"}]}]}]}` | `one/A/step0 $(a)` | `"job yaml"` | docs | vars#expansion-of-variables "This will be 'job yaml'" |
| VARV-002 | macro | yaml:`{"variables":{"a":"root"},"stages":[{"stage":"S","variables":{"a":"stage"},"jobs":[{"job":"J","steps":[{"bash":"echo $(a)"}]}]}]}` | `S/J/step0 $(a)` | `"stage"` | docs | vars#variable-scopes "Variables at the stage level override variables at the root level" |
| VARV-003 | macro | yaml:`{"variables":{"a":"root"},"stages":[{"stage":"S1","variables":{"a":"stage1"},"jobs":[{"job":"J","steps":[{"bash":"echo $(a)"}]}]},{"stage":"S2","jobs":[{"job":"J","steps":[{"bash":"echo $(a)"}]}]}]}` | `S2/J/step0 $(a)` | `"root"` | docs | vars#variable-scopes "At the stage level, to make it available only to a specific stage" |
| VARV-004 | macro | yaml:`{"jobs":[{"job":"job1","variables":{"job_variable1":"value1"},"steps":[{"bash":"echo hi"}]},{"job":"job2","steps":[{"bash":"echo $(job_variable1)"}]}]}` | `job2/step0 $(job_variable1)` | `literal` (job-scoped variable not visible in job2) | docs | vars#variable-scopes "this is only available in job1"; vars#macro-syntax-variables |
| VARV-005 | macro | yaml:`{"variables":{"a":"yaml"},"steps":[{"bash":"echo $(a)"}]}` ctx:`{"ui":{"a":{"value":"ui"}}}` | `step0 $(a)` | `"yaml"` | docs | vars#define-variables "root level overrides a variable set in the Pipeline settings UI" |
| VARV-006 | macro | yaml:`{"steps":[{"bash":"echo $(a)"}]}` ctx:`{"ui":{"a":{"value":"ui"}}}` | `step0 $(a)` | `"ui"` | docs | vars#set-variables-in-pipeline "define variables in the pipeline settings UI ... reference them in your YAML" |
| VARV-007 | macro | yaml:`{"jobs":[{"job":"J","variables":{"a":"job"},"steps":[{"bash":"echo $(a)"}]}]}` ctx:`{"ui":{"a":{"value":"ui"}}}` | `J/step0 $(a)` | `"job"` | docs | vars#expansion-of-variables (precedence list: job YAML first, Pipeline settings UI last) |
| VARV-008 | macro | yaml:`{"stages":[{"stage":"one","variables":[{"name":"a","value":"alpha"},{"name":"a","value":"beta"}],"jobs":[{"job":"I","steps":[{"script":"echo $(a)"}]}]}]}` | `one/I/step0 $(a)` | `"beta"` | docs | vars#expansion-of-variables "the last set value takes precedence" |
| VARV-009 | macro | yaml:`{"stages":[{"stage":"one","jobs":[{"job":"I","variables":[{"name":"b","value":"uno"},{"name":"b","value":"dos"}],"steps":[{"script":"echo $(b)"}]}]}]}` | `one/I/step0 $(b)` | `"dos"` | docs | vars#expansion-of-variables "#outputs dos" |
| VARV-010 | compile | yaml-text:`variables:\n  a: one\n  a: two\nsteps:\n- bash: echo $(a)` | `run` | `reject:schema` (duplicate mapping key). VERIFY: ADO may instead accept and keep the last key (`"two"`). | uncertain | ys-vars#remarks (mapping form; duplicate keys not addressed) |
| VARV-011 | macro | yaml:`{"variables":{"group":"G"},"steps":[{"bash":"echo $(group)"}]}` ctx:`{"groups":{"G":{"x":{"value":"g"}}}}` | `step0 $(group)` | `"G"` (mapping key `group` is a plain variable; group G is not imported) | docs | ys-vars#remarks "In mapping syntax, all keys are variable names" |
| VARV-012 | macro | yaml:`{"variables":[{"group":"G"},{"name":"x","value":"direct"}],"steps":[{"bash":"echo $(g1) $(x)"}]}` ctx:`{"groups":{"G":{"g1":{"value":"one"}}}}` | `step0 $(g1) $(x)` | `"one direct"` | docs | ys-vars#remarks "you can combine name, group, and template when using list syntax" |
| VARV-013 | macro | yaml:`{"variables":[{"name":"a","value":"1"},{"template":"v.yml"}],"steps":[{"bash":"echo $(a)"}]}` ctx:`{"files":{"v.yml":{"variables":[{"name":"a","value":"2"}]}}}` | `step0 $(a)` | `"2"` (template members take the template's list position; later wins) | inferred | tmpl#variable-reuse + vars#expansion-of-variables |
| VARV-014 | macro | yaml:`{"variables":[{"template":"v.yml"},{"name":"a","value":"1"}],"steps":[{"bash":"echo $(a)"}]}` ctx:`{"files":{"v.yml":{"variables":[{"name":"a","value":"2"}]}}}` | `step0 $(a)` | `"1"` | inferred | tmpl#variable-reuse + vars#expansion-of-variables |
| VARV-015 | macro | yaml:`{"variables":{"A":"1"},"jobs":[{"job":"J","variables":{"a":"2"},"steps":[{"bash":"echo $(A)"}]}]}` | `J/step0 $(A)` | `"2"` (names compare case-insensitively, so job `a` shadows root `A`) | inferred | predef#systemdebug (`system.debug` sets `System.Debug`); vars#configure-settable-variables-for-steps (`Sauce` allowed by `sauce`) |

### 1.2 Variable groups

| ID | phase | YAML input | consumer | expected | confidence | source |
|---|---|---|---|---|---|---|
| VARV-016 | macro | yaml:`{"variables":[{"group":"my-variable-group"}],"steps":[{"script":"echo $(myhello)"}]}` ctx:`{"groups":{"my-variable-group":{"myhello":{"value":"hello"}}}}` | `step0 $(myhello)` | `"hello"` | docs | vg#use-variables-in-a-linked-variable-group |
| VARV-017 | macro | yaml:`{"variables":[{"group":"G1"},{"group":"G2"}],"steps":[{"bash":"echo $(x)"}]}` ctx:`{"groups":{"G1":{"x":{"value":"one"}},"G2":{"x":{"value":"two"}}}}` | `step0 $(x)` | `"two"`. VERIFY: same page also says such collisions "may be unpredictable". | uncertain | vg#link-a-variable-group-to-a-pipeline "the last variable group that uses the variable"; vg (variable-collision note) |
| VARV-018 | macro | yaml:`{"variables":[{"group":"G"},{"name":"x","value":"direct"}],"steps":[{"bash":"echo $(x)"}]}` ctx:`{"groups":{"G":{"x":{"value":"g"}}}}` | `step0 $(x)` | `"direct"` | inferred | vars#expansion-of-variables (same scope, last wins); vg Classic list "Variable set in the pipeline" above group |
| VARV-019 | macro | yaml:`{"variables":[{"name":"x","value":"direct"},{"group":"G"}],"steps":[{"bash":"echo $(x)"}]}` ctx:`{"groups":{"G":{"x":{"value":"g"}}}}` | `step0 $(x)` | `"g"` by same-scope last-wins. VERIFY: Classic rule ranks pipeline variables above group variables; YAML order effect undocumented. | uncertain | vars#expansion-of-variables; vg#use-variable-groups-in-pipelines (Classic precedence list) |
| VARV-020 | macro | yaml:`{"variables":[{"group":"G"}],"jobs":[{"job":"J","variables":{"x":"job"},"steps":[{"bash":"echo $(x)"}]}]}` ctx:`{"groups":{"G":{"x":{"value":"g"}}}}` | `J/step0 $(x)` | `"job"` | docs | vars#define-variables "the most locally scoped variable takes precedence" |
| VARV-021 | macro | yaml:`{"variables":{"x":"root"},"jobs":[{"job":"J","variables":[{"group":"G"}],"steps":[{"bash":"echo $(x)"}]}]}` ctx:`{"groups":{"G":{"x":{"value":"g"}}}}` | `J/step0 $(x)` | `"g"` (job-scope group shadows root direct) | inferred | vars#define-variables (local scope wins) + vg#link-a-variable-group-to-a-pipeline |
| VARV-022 | macro | yaml:`{"variables":[{"group":"G"}],"steps":[{"bash":"echo $(x)"}]}` ctx:`{"ui":{"x":{"value":"ui"}},"groups":{"G":{"x":{"value":"g"}}}}` | `step0 $(x)` | `"g"` (YAML root import above UI). VERIFY: UI-vs-group precedence is not stated for YAML. | uncertain | vars#define-variables (root over UI); vg Classic list (pipeline over group) |
| VARV-023 | macro | yaml:`{"stages":[{"stage":"MyStage","variables":[{"template":"variables.yml"}],"jobs":[{"job":"Test","steps":[{"script":"echo $(myhello)"}]}]}]}` ctx:`{"files":{"variables.yml":{"variables":[{"group":"my-variable-group"}]}},"groups":{"my-variable-group":{"myhello":{"value":"hello"}}}}` | `MyStage/Test/step0 $(myhello)` | `"hello"` | docs | vg#link-a-variable-group-to-a-pipeline "You can also reference a variable group in a template" |
| VARV-024 | queue | yaml:`{"variables":[{"group":"G"}],"steps":[{"bash":"echo $(x)"}]}` ctx:`{"groups":{"G":{"x":{"value":"g"},"authorized":false}}}` | `run` | `reject:auth` (resource authorization error) | docs | vg#use-variable-groups-in-pipelines "You see a resource authorization error" |
| VARV-025 | runtime | yaml:`{"variables":[{"group":"my-variable-group"},{"name":"my-passed-variable","value":"$[variables.myhello]"}],"steps":[{"script":"echo $(my-passed-variable)"}]}` ctx:`{"groups":{"my-variable-group":{"myhello":{"value":"hello"}}}}` | `$[variables.myhello]` | `"hello"` | docs | vg#use-variables-in-a-linked-variable-group "To use a runtime expression" |
| VARV-026 | compile | yaml:`{"variables":[{"group":"G"}],"steps":[{"bash":"echo [${{ variables.x }}]"}]}` ctx:`{"groups":{"G":{"x":{"value":"g"}}}}` | `${{ variables.x }}` | `empty` (group values not in the compile-time context). VERIFY: docs say only "statically defined variables" are visible. | inferred | expr#expressions "access to parameters and statically defined variables"; texpr#context |
| VARV-027 | macro | yaml:`{"variables":[{"group":"G"}],"steps":[{"pwsh":"echo $env:USER"}]}` ctx:`{"groups":{"G":{"user":{"value":"u1"}}}}` | `step0 env USER` | `"u1"` | docs | vars#reference-secret-variables-in-variable-groups "$env:USER is a normal variable in the variable group" |
| VARV-028 | macro | yaml:`{"variables":[{"group":"G"}],"steps":[{"pwsh":"echo $env:TOKEN"}]}` ctx:`{"groups":{"G":{"token":{"value":"t","secret":true}}}}` | `step0 env TOKEN` | `env-absent` | docs | vg#use-variables-in-a-linked-variable-group "You can't access secret variables ... directly in scripts" |
| VARV-029 | macro | yaml:`{"variables":[{"group":"G"}],"steps":[{"pwsh":"echo $env:MY_MAPPED_TOKEN","env":{"MY_MAPPED_TOKEN":"$(token)"}}]}` ctx:`{"groups":{"G":{"token":{"value":"t","secret":true}}}}` | `step0 env MY_MAPPED_TOKEN` | `masked` (value `t`) | docs | vars#reference-secret-variables-in-variable-groups "Maps the secret variable $(token)" |

### 1.3 Templates and compile-time visibility

| ID | phase | YAML input | consumer | expected | confidence | source |
|---|---|---|---|---|---|---|
| VARV-030 | compile | yaml:`{"variables":[{"template":"insert-vars.yml"}],"steps":[{"script":"echo My favorite vegetable is ${{ variables.favoriteVeggie }}."}]}` ctx:`{"files":{"insert-vars.yml":{"variables":{"favoriteVeggie":"brussels sprouts"}}}}` | `${{ variables.favoriteVeggie }}` | `"brussels sprouts"` | docs | tmpl#variable-reuse |
| VARV-031 | macro | yaml:`{"variables":[{"template":"t.yml","parameters":{"DIRECTORY":"azure/checker"}}],"jobs":[{"job":"A","steps":[{"bash":"echo $(RELEASE_COMMAND)"}]}]}` ctx:`{"files":{"t.yml":{"parameters":[{"name":"DIRECTORY","type":"string","default":"."}],"variables":[{"name":"RELEASE_COMMAND","value":"grep version ${{ parameters.DIRECTORY }}/package.json"}]}}}` | `A/step0 $(RELEASE_COMMAND)` | `"grep version azure/checker/package.json"` | docs | tmpl#variable-templates-with-parameters |
| VARV-032 | macro | as VARV-031 plus stage `{"stage":"S","variables":[{"template":"t.yml","parameters":{"DIRECTORY":"azure/todo-list"}}],"jobs":[{"job":"A","steps":[{"bash":"echo $(RELEASE_COMMAND)"}]}]}` | `S/A/step0 $(RELEASE_COMMAND)` | `"grep version azure/todo-list/package.json"` | inferred | tmpl#variable-templates-with-parameters (example, output not stated) + vars#variable-scopes |
| VARV-033 | compile | yaml:`{"variables":[{"template":"v.yml"}],"steps":[{"bash":"echo hi"}]}` ctx:`{"files":{"v.yml":{"variables":{"a":"1"},"steps":[{"bash":"echo injected"}]}}}` | `run` | `reject:template-content` | docs | tmpl#variable-reuse "the included template can only be used to define variables" |
| VARV-034 | compile | yaml:`{"jobs":[{"template":"jobs/build.yml","parameters":{"additionalVariables":{"TEST_SUITE":"L0,L1"}}}]}` ctx:`{"files":{"jobs/build.yml":{"parameters":[{"name":"additionalVariables","type":"object","default":{}}],"jobs":[{"job":"build","variables":{"configuration":"debug","arch":"x86","${{ insert }}":"${{ parameters.additionalVariables }}"},"steps":[{"bash":"echo $(TEST_SUITE)"}]}]}}}` | `build/step0 $(TEST_SUITE)` | `"L0,L1"` | docs | texpr#insertion |
| VARV-035 | compile | yaml:`{"variables":[{"name":"myVar","value":"baz"},{"name":"conditionalVar","${{ if eq(variables['myVar'], 'foo') }}":{"value":"bar"},"${{ elseif eq(variables['myVar'], 'baz') }}":{"value":"qux"},"${{ else }}":{"value":"default"}}],"steps":[{"script":"echo $(conditionalVar)"}]}` | `step0 $(conditionalVar)` | `"qux"` | docs | texpr#conditional-insertion |
| VARV-036 | compile | yaml:`{"variables":{"${{ if eq(variables['Build.SourceBranchName'], 'main') }}":{"stageName":"prod"}},"steps":[{"script":"echo ${{variables.stageName}}"}]}` ctx:`{"run":{"Build.SourceBranchName":"main"}}` | `${{variables.stageName}}` | `"prod"` | docs | expr#conditionally-assign-a-variable |
| VARV-037 | compile | as VARV-036, ctx:`{"run":{"Build.SourceBranchName":"feature"}}` | `${{variables.stageName}}` | `empty` | docs | vars#template-expression-syntax "silently coalesce to empty strings" |
| VARV-038 | compile | yaml:`{"variables":[{"name":"k","value":"MYKEY"},{"name":"v","value":"val"}],"jobs":[{"job":"J","variables":{"${{ variables.k }}":"${{ variables.v }}"},"steps":[{"bash":"echo $(MYKEY)"}]}]}` | `J/step0 $(MYKEY)` | `"val"` (template expressions allowed in key position) | docs | vars#template-expression-syntax "can appear as either keys (left side) or values" |
| VARV-039 | compile | yaml:`{"variables":{"staticVar":"my value","compileVar":"${{ variables.staticVar }}"},"steps":[{"script":"echo $(compileVar)"}]}` | `step0 $(compileVar)` | `"my value"` | docs | expr#expressions "outputs my value" |
| VARV-040 | compile | yaml:`{"variables":{"a":"${{ variables.b }}","b":"x"},"steps":[{"bash":"echo [$(a)]"}]}` | `step0 $(a)` | `empty` (b not yet defined at a's position). VERIFY: forward reference within one block undocumented. | uncertain | expr#variables "place the variables in the order they should be processed" (stated for UI/runtime case) |
| VARV-041 | compile | yaml:`{"variables":{"a":"root"},"jobs":[{"job":"J","variables":{"a":"job"},"steps":[{"bash":"echo ${{ variables.a }}"}]}]}` | `${{ variables.a }}` in J | `"job"`. VERIFY: whether the compile-time `variables` context is scope-sensitive is undocumented. | uncertain | texpr#context "all the variables specified in the YAML file" |
| VARV-042 | compile | yaml:`{"variables":[{"name":"one","value":"initialValue"}],"steps":[{"bash":"echo '##vso[task.setvariable variable=one]secondValue'"},{"script":"echo ${{ variables.one }}"}]}` | `step1 ${{ variables.one }}` | `"initialValue"` | docs | vars#understand-variable-syntax "# outputs initialValue" |
| VARV-043 | compile | yaml:`{"steps":[{"bash":"echo [${{ variables.u }}]"}]}` ctx:`{"ui":{"u":{"value":"ui"}}}` | `${{ variables.u }}` | `empty` | docs | texpr#context "doesn't have runtime variables such as those stored on the pipeline" |
| VARV-044 | compile | yaml:`{"steps":[{"bash":"echo [${{ variables.q }}]"}]}` ctx:`{"ui":{"q":{"value":"d","settable":true}},"queue":{"q":"queued"}}` | `${{ variables.q }}` | `empty` | docs | texpr#context "or given when you start a run" |
| VARV-045 | compile | yaml:`{"steps":[{"bash":"echo [${{ variables.s }}]"}]}` ctx:`{"ui":{"s":{"value":"p","secret":true}}}` | `${{ variables.s }}` | `empty` (UI secret never enters compile-time context) | docs | texpr#context; vars#what-syntax-should-i-use "don't leverage template syntax for sensitive values" |
| VARV-046 | compile | yaml:`{"steps":[{"${{ if eq(variables['Build.SourceBranch'], 'refs/heads/main') }}":[{"script":"echo main"}]}]}` ctx:`{"run":{"Build.SourceBranch":"refs/heads/main"}}` | `${{ variables['Build.SourceBranch'] }}` | `"refs/heads/main"` (step inserted) | docs | tmpl#how-can-i-use-variables-inside-of-templates |
| VARV-047 | compile | yaml:`{"steps":[{"bash":"echo [${{ variables['Build.BuildId'] }}]"}]}` ctx:`{"run":{"Build.BuildId":"42"}}` | `${{ variables['Build.BuildId'] }}` | `empty` (Available in templates: No) | docs | predef#build-variables-devops-services "the variable doesn't render" |
| VARV-048 | compile | yaml:`{"variables":{"a":"x","b":"$[ variables.a ]"},"steps":[{"bash":"echo ${{ variables.b }}"}]}` | `${{ variables.b }}` | `"$[ variables.a ]"` (unevaluated text). VERIFY. | uncertain | expr#expressions (a: compile, b: runtime; interaction unstated) |
| VARV-049 | compile | yaml:`{"jobs":[{"deployment":"D","environment":"smarthotel-dev","strategy":{"runOnce":{"deploy":{"steps":[{"bash":"echo [${{ variables['Environment.Name'] }}]"}]}}}}]}` | `${{ variables['Environment.Name'] }}` | `empty` (deployment variables resolve at job execution) | inferred | predef#deployment-job-variables "resolved only at job execution time" |
| VARV-050 | compile | yaml:`{"parameters":[{"name":"image","type":"string","default":"ubuntu-latest","values":["windows-latest","ubuntu-latest","macOS-latest"]}],"jobs":[{"job":"build","pool":{"vmImage":"${{ parameters.image }}"},"steps":[{"script":"echo ${{ parameters.image }}"}]}]}` | `${{ parameters.image }}` | `"ubuntu-latest"` | docs | rtp#use-parameters-in-pipelines "the default option ubuntu-latest is used" |

### 1.4 Macro `$(x)` expansion

| ID | phase | YAML input | consumer | expected | confidence | source |
|---|---|---|---|---|---|---|
| VARV-051 | macro | yaml:`{"steps":[{"task":"MSBuild@1","inputs":{"configuration":"$(nosuch)"}}]}` | `step0 input configuration` | `literal` (`$(nosuch)`) | docs | vars#macro-syntax-variables "if $(var) can't be replaced, it remains as $(var)" |
| VARV-052 | macro | yaml:`{"variables":{"e":""},"steps":[{"task":"T@1","inputs":{"x":"a$(e)b"}}]}` | `step0 input x` | `"ab"`. VERIFY: docs say macros "remain unchanged when they have no value"; a defined-but-empty value may stay `a$(e)b`. | uncertain | vars#macro-syntax-variables |
| VARV-053 | macro | yaml:`{"variables":{"k":"realKey"},"steps":[{"bash":"env","env":{"$(k)":"v"}}]}` | `step0 env realKey` | `env-absent` (macro not expanded in key position; key stays `$(k)`) | docs | vars#macro-syntax-variables "The following isn't valid: $(key): value" |
| VARV-054 | compile | yaml:`{"variables":{"ref":"main"},"steps":[{"checkout":"git://MyProject/MyRepo@$(ref)"}]}` | `checkout repository ref` | `literal` (not expanded; checkout fails to resolve) | docs | vars#macro-syntax-variables "checkout: git://MyProject/MyRepo@$(var) doesn't work" |
| VARV-055 | compile | yaml:`{"variables":{"br":"main"},"trigger":["$(br)"],"steps":[{"bash":"echo hi"}]}` | `trigger branch filter` | `literal` (filter is the text `$(br)`) | docs | vars#macro-syntax-variables "doesn't expand them in pipeline keywords ... such as resources, trigger" |
| VARV-056 | compile | yaml:`{"jobs":[{"job":"A","variables":{"a":"10"},"strategy":{"matrix":{"x":{"some_variable":"$(a)"}}},"steps":[{"bash":"echo $(some_variable)"}]}]}` | `A x/step0 $(some_variable)` | `literal` (job variables unavailable when the matrix is expanded) | docs | vars#expansion-of-variables "# This does not work" |
| VARV-057 | macro | yaml:`{"variables":{"myInner":"someValue","myOuter":"$(myInner)"},"steps":[{"script":"echo $(myOuter)"}]}` | `step0 $(myOuter)` | `"someValue"` | docs | vars#recursive-expansion "recursively expanded" |
| VARV-058 | macro | yaml:`{"variables":{"myInner":"someValue","myOuter":"$(myInner)"},"steps":[{"script":"echo x","displayName":"Variable is $(myOuter)"}]}` | `step0 displayName` | `"Variable is someValue"` | docs | vars#recursive-expansion "display name is "Variable is someValue"" |
| VARV-059 | macro | yaml:`{"jobs":[{"job":"A","variables":{"a":"10"},"steps":[{"bash":"echo $(a)\necho '##vso[task.setvariable variable=a]20'\necho $(a)"}]}]}` | `A/step0` second `$(a)` | `"10"` (expansion happens before the step) | docs | vars#expansion-of-variables "This will also be 10" |
| VARV-060 | logging | yaml: VARV-059 plus second step `{"bash":"echo $(a)"}` | `A/step1 $(a)` | `"20"` | docs | vars#expansion-of-variables "This will be 20" |
| VARV-061 | macro | yaml:`{"variables":{"sauce":"red"},"steps":[{"bash":"echo $(SAUCE)"}]}` | `step0 $(SAUCE)` | `"red"` (lookup case-insensitive) | inferred | vars#configure-settable-variables-for-steps (sets `Sauce`, reads `$(sauce)`); predef#systemdebug |
| VARV-062 | macro | yaml:`{"variables":{"system.debug":"true"},"steps":[{"bash":"echo $(System.Debug)"}]}` | `step0 $(System.Debug)` | `"true"` (YAML may set System.Debug) | docs | predef#systemdebug "You can also set System.Debug to true as a variable" |
| VARV-063 | macro | yaml:`{"variables":{"VMS_PASS":"$(vmsAdminPass)"},"steps":[{"task":"AzureFileCopy@6","inputs":{"vmsAdminPassword":"$(VMS_PASS)"}}]}` ctx:`{"ui":{"vmsAdminPass":{"value":"pw","secret":true}}}` | `step0 input vmsAdminPassword` | `masked` (value `pw` reaches the input) | docs | vars#set-secret-variables "map secret variables to tasks by using the variables definition" |
| VARV-064 | macro | yaml:`{"variables":{"ml":"line1\nline2"},"steps":[{"bash":"echo \"$(ml)\""}]}` | `step0 $(ml)` | `"line1\nline2"` byte-for-byte (no normalization) | docs | vars#user-defined-multi-line-variables "Azure DevOps never alters variable values" |

### 1.5 Runtime expressions `$[ ]`

| ID | phase | YAML input | consumer | expected | confidence | source |
|---|---|---|---|---|---|---|
| VARV-065 | runtime | yaml:`{"variables":{"simpleVar":"hello","b":"$[ variables.simpleVar ]"},"steps":[{"bash":"echo $(b)"}]}` | `$[ variables.simpleVar ]` | `"hello"` | docs | vars#runtime-expression-syntax |
| VARV-066 | runtime | yaml:`{"variables":{"isMain":"$[eq(variables['Build.SourceBranch'], 'refs/heads/main')]"},"steps":[{"script":"echo $(isMain)"}]}` ctx:`{"run":{"Build.SourceBranch":"refs/heads/main"}}` | `step0 $(isMain)` | `"True"` | docs | expr#expressions "# outputs True" |
| VARV-067 | runtime | yaml:`{"variables":{"b":"$[ variables.Build.SourceBranch ]"},"steps":[{"bash":"echo [$(b)]"}]}` ctx:`{"run":{"Build.SourceBranch":"refs/heads/main"}}` | `$[ variables.Build.SourceBranch ]` | `empty` (dotted name needs index syntax) | inferred | vars#runtime-expression-syntax "not $[ variables.Build.SourceBranch ]" + "silently coalesce to empty strings" |
| VARV-068 | runtime | yaml:`{"variables":{"b":"$[ variables.nosuch ]"},"steps":[{"bash":"echo [$(b)]"}]}` | `$[ variables.nosuch ]` | `empty` | docs | vars#runtime-expression-syntax "silently coalesce to empty strings" |
| VARV-069 | runtime | yaml:`{"variables":{"a":"1","b":"$[variables.a] foo"},"steps":[{"bash":"echo $(b)"}]}` | `step0 $(b)` | `literal` (`$[variables.a] foo`). VERIFY: docs only say "isn't valid" (could be a compile error). | uncertain | vars#runtime-expression-syntax "key: $[variables.value] foo isn't" |
| VARV-070 | runtime | yaml:`{"variables":{"simpleVar":"hello"},"steps":[{"bash":"echo $[ variables.simpleVar ]"}]}` | `step0 script body` | VERIFY: vars example implies `hello`; expr# limits runtime expressions to variables and conditions, so `literal` is likely. | uncertain | vars#runtime-expression-syntax (example); expr#expressions "use runtime expressions in variables and conditions" |
| VARV-071 | runtime | yaml:`{"variables":[{"name":"a","value":"$[ format('{0}-x', 'v') ]"},{"name":"b","value":"$[ variables.a ]"}],"steps":[{"bash":"echo $(b)"}]}` | `step0 $(b)` | `"v-x"` (YAML runtime variables evaluated in declaration order) | docs | expr#variables "place the variables in the order they should be processed" |
| VARV-072 | runtime | yaml:`{"steps":[{"bash":"echo $(b)"}]}` ctx:`{"ui":{"a":{"value":"$[ format('{0}-x','v') ]"},"b":{"value":"$[ variables.a ]"}}}` | `step0 $(b)` | not guaranteed (`"v-x"` or stale); evaluator must flag order dependence | docs | expr#variables "the order of processing variables isn't guaranteed" |
| VARV-073 | runtime | yaml:`{"parameters":[{"name":"x","type":"string","default":"1"}],"variables":{"b":"$[ parameters.x ]"},"steps":[{"bash":"echo [$(b)]"}]}` | `$[ parameters.x ]` | `unavailable` (no parameters context at runtime). VERIFY: whether this is an error or `empty`. | docs | expr#expressions "you have access to more variables but no parameters" |
| VARV-074 | runtime | yaml:`{"jobs":[{"job":"J","variables":{"a":"$[counter(format('{0:yyyyMMdd}', pipeline.startTime), 100)]"},"steps":[{"bash":"echo $(a)"}]}]}` (first run of the day) | `J/step0 $(a)` | `"100"` | docs | vars#set-variables-by-using-expressions "starts at 100" |
| VARV-075 | runtime | yaml:`{"variables":{"k":"key","$[variables.k]":"value"},"steps":[{"bash":"echo $(key)"}]}` | `step0 $(key)` | `literal` (runtime expression not evaluated in key position) | docs | vars#runtime-expression-syntax "The following isn't valid: $[variables.key]: value" |

### 1.6 Output variables and dependency visibility

| ID | phase | YAML input | consumer | expected | confidence | source |
|---|---|---|---|---|---|---|
| VARV-076 | logging | yaml:`{"jobs":[{"job":"A","steps":[{"bash":"echo '##vso[task.setvariable variable=myJobVar]this is the same job'"},{"bash":"echo $(myJobVar)"}]}]}` | `A/step1 $(myJobVar)` | `"this is the same job"` | docs | setvar#set-an-output-variable-for-use-in-the-same-job |
| VARV-077 | logging | yaml:`{"jobs":[{"job":"A","steps":[{"bash":"echo '##vso[task.setvariable variable=myOutputJobVar;isOutput=true]this is the same job too'","name":"setOutput"},{"bash":"echo $(setOutput.myOutputJobVar)"}]}]}` | `A/step1 $(setOutput.myOutputJobVar)` | `"this is the same job too"` | docs | setvar#set-an-output-variable-for-use-in-the-same-job |
| VARV-078 | logging | yaml: VARV-077 with step1 `{"bash":"echo $(myOutputJobVar)"}` | `A/step1 $(myOutputJobVar)` | VERIFY: `literal` (only the step-qualified name exists) vs value. | uncertain | setvar#set-an-output-variable-for-use-in-the-same-job "you need to reference the variable with the task name" |
| VARV-079 | logging | yaml:`{"steps":[{"pwsh":"Write-Host '##vso[task.setvariable variable=outputSauce;isOutput=true]canned goods'","name":"SetVars"},{"pwsh":"Write-Host $env:SETVARS_OUTPUTSAUCE"}]}` | `step1 env SETVARS_OUTPUTSAUCE` | `"canned goods"` | docs | log#setvariable-initialize-or-modify-the-value-of-a-variable "$env:SETVARS_OUTPUTSAUCE" |
| VARV-080 | logging | yaml:`{"jobs":[{"job":"A","steps":[{"bash":"echo '##vso[task.setvariable variable=myJobVar]v'"}]},{"job":"B","dependsOn":"A","steps":[{"bash":"echo $(myJobVar)"}]}]}` | `B/step0 $(myJobVar)` | `literal` (non-output variable stays in job A) | docs | setvar#about-tasksetvariable "only available to tasks in the same job by default" |
| VARV-081 | runtime | yaml:`{"jobs":[{"job":"A","steps":[{"bash":"echo '##vso[task.setvariable variable=myOutputVar;isOutput=true]this is from job A'","name":"passOutput"}]},{"job":"B","dependsOn":"A","variables":{"myVarFromJobA":"$[ dependencies.A.outputs['passOutput.myOutputVar'] ]"},"steps":[{"bash":"echo $(myVarFromJobA)"}]}]}` | `B/step0 $(myVarFromJobA)` | `"this is from job A"` | docs | setvar#set-an-output-variable-for-use-in-future-jobs |
| VARV-082 | runtime | yaml: VARV-081 without `"dependsOn":"A"` on B | `$[ dependencies.A.outputs['passOutput.myOutputVar'] ]` | `unavailable` → `empty` (A is not a dependency; jobs run in parallel) | inferred | setvar#levels-of-output-variables "only access output variables if they depend on"; expr#succeeded "Jobs run in parallel" |
| VARV-083 | runtime | yaml:`{"jobs":[{"job":"A","steps":[{"bash":"echo '##vso[task.setvariable variable=o;isOutput=true]v'","name":"s"}]},{"job":"B","dependsOn":"A","steps":[{"bash":"echo b"}]},{"job":"C","dependsOn":"B","variables":{"x":"$[ dependencies.A.outputs['s.o'] ]"},"steps":[{"bash":"echo [$(x)]"}]}]}` | `C $[ dependencies.A.outputs['s.o'] ]` | `unavailable` → `empty` (not transitive) | docs | setvar#set-an-output-variable-for-use-in-future-jobs "only available in the next downstream job" |
| VARV-084 | runtime | yaml:`{"jobs":[{"job":"A","steps":[{"bash":"echo '##vso[task.setvariable variable=shouldrun;isOutput=true]true'","name":"printvar"}]},{"job":"B","dependsOn":"A","condition":"and(succeeded(), eq(dependencies.A.outputs['printvar.shouldrun'], 'true'))","steps":[{"script":"echo hello from B"}]}]}` | `B condition` | `runs` | docs | expr#job-to-job-dependencies-within-one-stage |
| VARV-085 | runtime | yaml:`{"jobs":[{"job":"A","strategy":{"matrix":{"debugJob":{"configuration":"debug"},"releaseJob":{"configuration":"release"}}},"steps":[{"bash":"echo '##vso[task.setvariable variable=myOutputVar;isOutput=true]this is the $(configuration) value'","name":"setvarStep"}]},{"job":"B","dependsOn":"A","variables":{"v":"$[ dependencies.A.outputs['debugJob.setvarStep.myOutputVar'] ]"},"steps":[{"script":"echo $(v)"}]}]}` | `B/step0 $(v)` | `"this is the debug value"` | docs | vars#set-a-multi-job-output-variable (matrix example) |
| VARV-086 | runtime | yaml:`{"jobs":[{"job":"A","pool":{"vmImage":"ubuntu-latest","parallel":2},"steps":[{"bash":"echo '##vso[task.setvariable variable=myOutputVar;isOutput=true]slice $(system.jobPositionInPhase)'","name":"setvarStep"}]},{"job":"B","dependsOn":"A","variables":{"v":"$[ dependencies.A.outputs['setvarStep.myOutputVar'] ]"},"steps":[{"script":"echo $(v)"}]}]}` | `B/step0 $(v)` | VERIFY: docs comment says "first slice" (`"slice 1"`) but also say slices need a job-name prefix. | uncertain | vars#set-a-multi-job-output-variable (slicing example) |
| VARV-087 | runtime | yaml:`{"jobs":[{"deployment":"A","environment":"staging","strategy":{"runOnce":{"deploy":{"steps":[{"bash":"echo '##vso[task.setvariable variable=myOutputVar;isOutput=true]dv'","name":"setvarStep"}]}}}},{"job":"B","dependsOn":"A","variables":{"v":"$[ dependencies.A.outputs['A.setvarStep.myOutputVar'] ]"},"steps":[{"bash":"echo $(v)"}]}]}` | `B/step0 $(v)` | `"dv"` | docs | vars#set-a-multi-job-output-variable "prefix the job name to the output variables of a deployment job" |
| VARV-088 | runtime | yaml: VARV-087 with B mapping `$[ dependencies.A.outputs['setvarStep.myOutputVar'] ]` | `B/step0 $(v)` | `empty` (key lacks deployment-job prefix) | inferred | deploy#support-for-output-variables (runOnce key `<job-name>.<step-name>.<variable-name>`) |
| VARV-089 | runtime | yaml: deployment A with `strategy.canary.increments:[10,20]`, step `setvarStep` sets `myOutputVar` (isOutput) in `deploy`; job B `dependsOn: A`, `v: $[ dependencies.A.outputs['deploy_10.setvarStep.myOutputVar'] ]` | `B $(v)` | value set in increment 10 | docs | deploy#support-for-output-variables "<lifecycle-hookname>_<increment-value>.<step-name>.<variable-name>" |
| VARV-090 | runtime | yaml:`{"stages":[{"stage":"A","jobs":[{"job":"A1","steps":[{"bash":"echo '##vso[task.setvariable variable=myStageVal;isOutput=true]this is a stage output variable'","name":"MyOutputVar"}]}]},{"stage":"B","dependsOn":"A","jobs":[{"job":"B1","variables":{"myStageAVar":"$[stageDependencies.A.A1.outputs['MyOutputVar.myStageVal']]"},"steps":[{"bash":"echo $(myStageAVar)"}]}]}]}` | `B/B1/step0 $(myStageAVar)` | `"this is a stage output variable"` | docs | setvar#set-an-output-variable-for-use-in-future-stages |
| VARV-091 | runtime | yaml:`{"stages":[{"stage":"A","jobs":[{"job":"A1","steps":[{"bash":"echo '##vso[task.setvariable variable=myStageOutputVar;isOutput=true]this is a stage output var'","name":"printvar"}]}]},{"stage":"B","dependsOn":"A","variables":{"myVarfromStageA":"$[ stageDependencies.A.A1.outputs['printvar.myStageOutputVar'] ]"},"jobs":[{"job":"B1","steps":[{"script":"echo $(myVarfromStageA)"}]}]}]}` | `B/B1/step0 $(myVarfromStageA)` (stage-scope mapping) | `"this is a stage output var"` | docs | vars#set-a-multi-job-output-variable "use stageDependencies" |
| VARV-092 | runtime | yaml:`{"stages":[{"stage":"A","jobs":[{"job":"A1","steps":[{"bash":"echo '##vso[task.setvariable variable=shouldrun;isOutput=true]true'","name":"printvar"}]}]},{"stage":"B","dependsOn":"A","condition":"and(succeeded(), eq(dependencies.A.outputs['A1.printvar.shouldrun'], 'true'))","jobs":[{"job":"B1","steps":[{"script":"echo hi"}]}]}]}` | `B condition` (stage level uses `dependencies.<stage>.outputs['<job>.<step>.<var>']`) | `runs` | docs | expr#stage-to-stage-dependencies |
| VARV-093 | runtime | yaml: VARV-090 with stage B's `dependsOn` removed (B directly follows A) | `B/B1/step0 $(myStageAVar)` | `"this is a stage output variable"` (implicit dependency on previous stage) | docs | vars#set-a-multi-job-output-variable "each stage in a pipeline depends on the one just before it" |
| VARV-094 | runtime | yaml: stages One (job A sets `ProduceVar.MyVar` isOutput), Two (`dependsOn:[One]`), Three (no dependsOn; maps `$[ stageDependencies.One.A.outputs['ProduceVar.MyVar'] ]`) | `Three $[ stageDependencies.One.A.outputs[...] ]` | `unavailable` → `empty` | docs | vars#use-outputs-in-a-different-stage "only available in the next downstream stage" |
| VARV-095 | runtime | yaml: as VARV-094 but Three `dependsOn:[One, Two]` | `Three $[ stageDependencies.One.A.outputs['ProduceVar.MyVar'] ]` | value of MyVar | docs | vars#use-outputs-in-a-different-stage (stage Three example) |
| VARV-096 | runtime | yaml:`{"stages":[{"stage":"A","jobs":[{"job":"A1","steps":[{"bash":"echo '##vso[task.setvariable variable=shouldrun;isOutput=true]true'","name":"printvar"}]}]},{"stage":"B","dependsOn":"A","jobs":[{"job":"B2","condition":"eq(stageDependencies.A.A1.outputs['printvar.shouldrun'], 'true')","steps":[{"script":"echo hi"}]}]}]}` | `B/B2 condition` | `runs` per expr# example. VERIFY: vars# says stageDependencies usable in conditions "At the stage level, but not the job level". | uncertain | expr#job-to-job-dependencies-across-stages; vars#use-outputs-in-a-different-stage |
| VARV-097 | runtime | yaml:`{"stages":[{"stage":"A","jobs":[{"job":"A1","steps":[{"bash":"echo '##vso[task.setvariable variable=o;isOutput=true]v'","name":"s"}]}]},{"stage":"B","dependsOn":"A","jobs":[{"job":"B1","variables":{"x":"$[ dependencies.A1.outputs['s.o'] ]"},"steps":[{"bash":"echo [$(x)]"}]}]}]}` | `B/B1 $[ dependencies.A1.outputs['s.o'] ]` | `unavailable` → `empty` (job-to-job `dependencies` is same-stage only) | docs | vars#set-a-multi-job-output-variable "Multi-job output variables only work for jobs in the same stage" |
| VARV-098 | runtime | yaml: stage `build` with deployment `build_job` (runOnce) step `setRunTests` sets `runTests=true` isOutput; stage `test` `dependsOn: build`, job `run_tests` `condition: eq(stageDependencies.build.build_job.outputs['build_job.setRunTests.runTests'], 'true')` | `test/run_tests condition` | `runs` | docs | expr#job-to-job-dependencies-across-stages (deployment example) |
| VARV-099 | runtime | yaml: as VARV-098 but stage `test` `condition: eq(dependencies.build.outputs['build_job.build_job.setRunTests.runTests'], 'true')` | `test condition` | `runs` | docs | expr#deployment-job-output-variables "build_job appears twice" |
| VARV-100 | logging | yaml:`{"steps":[{"bash":"echo '##vso[task.setvariable variable=myStageVal;isOutput=true]foo%0Abar'","name":"MyOutputVar"},{"bash":"echo \"$(MyOutputVar.myStageVal)\""}]}` | `step1 $(MyOutputVar.myStageVal)` | `"foo\nbar"` (agent unescapes `%0A`) | docs | setvar#set-an-output-variable-for-use-in-future-stages "the agent automatically unescapes it" |

### 1.7 Logging-command mutation, read-only and settable variables

| ID | phase | YAML input | consumer | expected | confidence | source |
|---|---|---|---|---|---|---|
| VARV-101 | logging | yaml:`{"steps":[{"bash":"echo '##vso[task.setvariable variable=sauce]crushed tomatoes'\necho \"$SAUCE\""}]}` | `step0 env SAUCE` (same step) | `env-absent` | docs | vars#set-a-job-scoped-variable-from-a-script "does not update the environment of the current script" |
| VARV-102 | logging | yaml: VARV-101 plus `{"bash":"echo \"my environment variable is $SAUCE\""}` | `step1 env SAUCE` | `"crushed tomatoes"` | docs | vars#set-a-job-scoped-variable-from-a-script "my environment variable is crushed tomatoes" |
| VARV-103 | logging | yaml:`{"steps":[{"bash":"echo '##vso[task.setvariable variable=mySecretVal;issecret=true]secretvalue'"},{"bash":"echo $(mySecretVal)"}]}` | `step1 $(mySecretVal)` / `log` | `masked` | docs | setvar#set-a-variable-as-secret "masked out from logs" |
| VARV-104 | logging | yaml:`{"steps":[{"bash":"echo '##vso[task.setvariable variable=secretSauce;isSecret=true]garlic'"},{"bash":"echo \"$SECRETSAUCE\""}]}` | `step1 env SECRETSAUCE` | `env-absent` | docs | log#setvariable-initialize-or-modify-the-value-of-a-variable "Secrets are not automatically mapped in" |
| VARV-105 | logging | yaml:`{"steps":[{"bash":"echo '##vso[task.setvariable variable=ro;isreadonly=true]first'"},{"bash":"echo '##vso[task.setvariable variable=ro]second'"},{"bash":"echo $(ro)"}]}` | `step2 $(ro)` | `unchanged:"first"`. VERIFY: whether the overwrite attempt warns or fails the step. | docs | setvar#set-variable-properties "downstream tasks can't overwrite it" |
| VARV-106 | logging | yaml:`{"variables":[{"name":"myReadOnlyVar","value":"myValue","readonly":true}],"steps":[{"bash":"echo '##vso[task.setvariable variable=myReadOnlyVar]changed'"},{"bash":"echo $(myReadOnlyVar)"}]}` | `step1 $(myReadOnlyVar)` | `unchanged:"myValue"` | docs | secin#variables "specify a read-only variable by using the specific readonly key" |
| VARV-107 | macro | yaml:`{"variables":[{"name":"x","value":"root","readonly":true}],"jobs":[{"job":"J","variables":[{"name":"x","value":"job"}],"steps":[{"bash":"echo $(x)"}]}]}` | `J/step0 $(x)` | VERIFY: `"job"` (scope shadowing is not a mutation) vs `"root"` or compile error. | uncertain | ys-name ("Whether a YAML variable is read-only"); scope interaction undocumented |
| VARV-108 | logging | yaml:`{"steps":[{"bash":"echo '##vso[task.setvariable variable=Build.SourcesDirectory]/tmp/evil'"},{"bash":"echo $(Build.SourcesDirectory)"}]}` ctx:`{"run":{"Build.SourcesDirectory":"/home/vsts/work/1/s"}}` | `step1 $(Build.SourcesDirectory)` | `unchanged:"/home/vsts/work/1/s"` | docs | secin#variables "System variables like Build.SourcesDirectory and task output variables are always read-only" |
| VARV-109 | logging | yaml:`{"steps":[{"bash":"echo '##vso[task.setvariable variable=o;isOutput=true]v1'","name":"p"},{"bash":"echo '##vso[task.setvariable variable=p.o]v2'"},{"bash":"echo $(p.o)"}]}` | `step2 $(p.o)` | `unchanged:"v1"` | docs | secin#variables "task output variables are always read-only" |
| VARV-110 | logging | yaml:`{"steps":[{"script":"echo '##vso[task.setvariable variable=x]v'","target":{"settableVariables":"none"}},{"bash":"echo $(x)"}]}` | `step1 $(x)` | `literal` (set attempt ignored) | docs | vars#configure-settable-variables-for-steps "the script can't set a variable" |
| VARV-111 | logging | yaml:`{"steps":[{"bash":"echo '##vso[task.setvariable variable=Sauce;]crushed tomatoes'","target":{"settableVariables":["sauce"]},"name":"SetVars"},{"bash":"echo $(sauce)"}]}` | `step1 $(sauce)` | `"crushed tomatoes"` | docs | vars#configure-settable-variables-for-steps "the script can set the variable sauce" |
| VARV-112 | logging | yaml:`{"steps":[{"bash":"echo '##vso[task.setvariable variable=secretSauce;]garlic'","target":{"settableVariables":["sauce"]}},{"bash":"echo $(secretSauce)"}]}` | `step1 $(secretSauce)` + run page | `literal`, plus a warning on the run | docs | vars#configure-settable-variables-for-steps "You see a warning on the pipeline run page" |
| VARV-113 | logging | yaml:`{"steps":[{"bash":"echo \"pre $NEWSECRET\"\necho \"##vso[task.setsecret]$NEWSECRET\"\necho \"post $NEWSECRET\"","env":{"NEWSECRET":"abc"}}]}` | `log` lines `pre`/`post` | `pre abc` unmasked; `post ***` | docs | log#setsecret-register-a-value-as-a-secret "Previous occurrences of the secret value won't be masked" |
| VARV-114 | macro | yaml:`{"steps":[{"bash":"echo abc"}]}` ctx:`{"ui":{"s":{"value":"abc123","secret":true}}}` | `log` | `"abc"` (substring of a secret not masked) | docs | vars#set-secret-variables "We never mask substrings of secrets" |

### 1.8 Secrets

| ID | phase | YAML input | consumer | expected | confidence | source |
|---|---|---|---|---|---|---|
| VARV-115 | macro | yaml:`{"steps":[{"bash":"echo \"Using an input-macro works: $(mySecret)\""}]}` ctx:`{"ui":{"mySecret":{"value":"foo","secret":true}}}` | `step0 $(mySecret)` | `masked` (log `Using an input-macro works: ***`) | docs | vars#set-secret-variables |
| VARV-116 | macro | same ctx, yaml:`{"steps":[{"bash":"echo \"$MYSECRET\""}]}` | `step0 env MYSECRET` | `env-absent` | docs | vars#set-secret-variables "there's no environment variable called MYSECRET" |
| VARV-117 | macro | same ctx, yaml:`{"steps":[{"bash":"echo \"$MY_MAPPED_ENV_VAR\"","env":{"MY_MAPPED_ENV_VAR":"$(mySecret)"}}]}` | `step0 env MY_MAPPED_ENV_VAR` | `masked` (value `foo`) | docs | vars#set-secret-variables "the recommended way to map to an env variable" |
| VARV-118 | macro | same ctx, yaml:`{"variables":{"GLOBAL_MYSECRET":"$(mySecret)"},"steps":[{"bash":"echo \"[$GLOBAL_MYSECRET]\""}]}` | `step0 env GLOBAL_MYSECRET` | `empty` (log `[]`) | docs | vars#set-secret-variables "this will not work because the secret variable needs to be mapped" |
| VARV-119 | macro | yaml:`{"variables":{"GLOBAL_MY_MAPPED_ENV_VAR":"$(nonSecretVariable)"},"steps":[{"bash":"echo $GLOBAL_MY_MAPPED_ENV_VAR"}]}` ctx:`{"ui":{"nonSecretVariable":{"value":"foo"}}}` | `step0 env GLOBAL_MY_MAPPED_ENV_VAR` | `"foo"` | docs | vars#set-secret-variables "this works because it's not a secret" |
| VARV-120 | queue | yaml:`{"steps":[{"bash":"echo $(mySecret)"}]}` ctx:`{"ui":{"mySecret":{"value":"foo","secret":true}},"fork":true}` | `step0 $(mySecret)` | `unavailable` (fork PR builds get no pipeline secrets by default) | docs | vars#set-secret-variables "pull request builds of forks can't access secret variables" |
| VARV-121 | macro | yaml:`{"steps":[{"bash":"echo \"[$SYSTEM_ACCESSTOKEN]\""}]}` | `step0 env SYSTEM_ACCESSTOKEN` | `env-absent` (must be mapped explicitly) | docs | predef#systemaccesstoken "you must explicitly map System.AccessToken into the pipeline" |
| VARV-122 | runtime | yaml:`{"variables":{"copy":"$[ variables.mySecret ]"},"steps":[{"bash":"echo $(copy)"}]}` ctx:`{"ui":{"mySecret":{"value":"foo","secret":true}}}` | `step0 $(copy)` | VERIFY: value `foo` masked vs `empty`; secrecy propagation through `$[ ]` undocumented. | uncertain | vars#runtime-expression-syntax (silent on secrets) |

### 1.9 Queue time and runtime parameters

| ID | phase | YAML input | consumer | expected | confidence | source |
|---|---|---|---|---|---|---|
| VARV-123 | queue | yaml:`{"steps":[{"bash":"echo $(x)"}]}` ctx:`{"ui":{"x":{"value":"d","settable":true}},"queue":{"x":"q"}}` | `step0 $(x)` | `"q"` | docs | vars#expansion-of-variables (queue time ranked above Pipeline settings UI) |
| VARV-124 | queue | yaml:`{"variables":{"x":"yaml"},"steps":[{"bash":"echo $(x)"}]}` ctx:`{"ui":{"x":{"value":"d","settable":true}},"queue":{"x":"q"}}` | `step0 $(x)` | `"yaml"` (queue value silently loses) | docs | vars#allow-at-queue-time "the value in the YAML has priority" |
| VARV-125 | queue | yaml:`{"jobs":[{"job":"J","variables":{"x":"job"},"steps":[{"bash":"echo $(x)"}]}]}` ctx:`{"ui":{"x":{"value":"d","settable":true}},"queue":{"x":"q"}}` | `J/step0 $(x)` | `"job"` | docs | vars#expansion-of-variables (job YAML above queue time) |
| VARV-126 | queue | yaml:`{"steps":[{"bash":"echo $(BuildConfiguration)"}]}` ctx:`{"ui":{"BuildConfiguration":{"value":"Release","settable":false}},"settings":{"limitQueueTimeVariables":true},"queue":{"BuildConfiguration":"Debug"}}` (Runs API) | `run` | `reject:queue-var` ("You can't set the following variables (BuildConfiguration)") | docs | secin#limit-variables-that-can-be-set-at-queue-time |
| VARV-127 | queue | yaml:`{"steps":[{"bash":"echo $(newVar)"}]}` ctx:`{"settings":{"limitQueueTimeVariables":true},"queue":{"newVar":"v"}}` | `run` | `reject:queue-var` (undeclared variable cannot be added) | docs | secin#limit-variables-that-can-be-set-at-queue-time "users can set only variables that are explicitly marked" |
| VARV-128 | queue | yaml:`{"steps":[{"bash":"echo $(newVar)"}]}` ctx:`{"settings":{"limitQueueTimeVariables":false},"queue":{"newVar":"v"}}` | `step0 $(newVar)` | `"v"` | docs | secin#limit-variables-that-can-be-set-at-queue-time "create variables that the pipeline author didn't define" |
| VARV-129 | queue | yaml:`{"steps":[{"bash":"echo $(Build.SourceBranchName)"}]}` ctx:`{"settings":{"limitQueueTimeVariables":false},"queue":{"Build.SourceBranchName":"x"},"run":{"Build.SourceBranchName":"main"}}` | `step0 $(Build.SourceBranchName)` | VERIFY: secin says queue time can "override system variables"; vars says system variables are read-only. Effective value unknown. | uncertain | secin#limit-variables-that-can-be-set-at-queue-time; vars#system-variables |
| VARV-130 | queue | yaml:`{"steps":[{"bash":"echo $(x)"}]}` ctx:`{"ui":{"x":{"value":"d","settable":false}},"settings":{"limitQueueTimeVariables":false},"queue":{"x":"q"}}` | `step0 $(x)` | VERIFY: `"q"` (unrestricted org allows setting existing variables) vs reject. | uncertain | secin#limit-variables-that-can-be-set-at-queue-time "set values for existing variables at queue time" |
| VARV-131 | queue | yaml:`{"parameters":[{"name":"image","type":"string","default":"ubuntu-latest","values":["windows-latest","ubuntu-latest"]}],"steps":[{"bash":"echo ${{ parameters.image }}"}]}` ctx:`{"params":{"image":"centos"}}` | `run` | `reject:param-value` | docs | rtp#runtime-parameters "they can be restricted to a subset of values" |
| VARV-132 | queue | yaml:`{"parameters":[{"name":"image","displayName":"Pool Image","values":["windows-latest","ubuntu-latest","macOS-latest"]}],"steps":[{"bash":"echo ${{ parameters.image }}"}]}` | `${{ parameters.image }}` | `"windows-latest"` (no default: first value) | docs | rtp#runtime-parameters "the first available value is used" |
| VARV-133 | logging | yaml:`{"parameters":[{"name":"image","type":"string","default":"ubuntu-latest"}],"steps":[{"bash":"echo '##vso[task.setvariable variable=image]changed'"},{"bash":"echo ${{ parameters.image }} $(image)"}]}` | `step1` | `"ubuntu-latest changed"` (parameter immutable; variable namespace separate) | docs | rtp#what-is-the-difference-between-parameters-and-variables "Immutable after queue" |
| VARV-134 | queue | yaml:`{"parameters":[{"name":"customVersion","type":"string","default":""}],"steps":[{"script":"echo ${{ parameters.customVersion }}"}]}` (queued from the Run Pipeline pane) | `run` | Run pane requires a non-empty value; editor-run keeps `empty` | docs | texpr#coalesce "Azure DevOps requires you to provide a non-empty value" |
| VARV-135 | compile | yaml:`{"steps":[{"template":"t.yml"}]}` ctx:`{"files":{"t.yml":{"parameters":[{"name":"r","type":"stringList","values":["WUS"],"default":["WUS"]}],"steps":[{"bash":"echo hi"}]}}}` | `run` | `reject:schema` (stringList not allowed in templates) | docs | rtp#parameter-data-types "The stringList data type isn't available in templates" |
| VARV-136 | compile | yaml:`{"parameters":[{"name":"pw","type":"string","default":"hunter2"}],"steps":[{"bash":"echo ${{ parameters.pw }}"}]}` | `log` | `"hunter2"` unmasked (parameters cannot be secret) | docs | rtp#what-is-the-difference-between-parameters-and-variables "No support for secret values" |

### 1.10 Environment projection and naming rules

| ID | phase | YAML input | consumer | expected | confidence | source |
|---|---|---|---|---|---|---|
| VARV-137 | macro | yaml:`{"variables":{"any.variable":"v"},"steps":[{"bash":"echo $ANY_VARIABLE"}]}` | `step0 env ANY_VARIABLE` | `"v"` | docs | vars#environment-variables "any.variable becomes the variable name $ANY_VARIABLE" |
| VARV-138 | macro | yaml:`{"jobs":[{"job":"job1","variables":{"job_variable1":"value1"},"steps":[{"bash":"echo $JOB_VARIABLE1"}]}]}` | `job1/step0 env JOB_VARIABLE1` | `"value1"` | docs | vars#variable-scopes "variables are available in the script environment too" |
| VARV-139 | macro | yaml:`{"steps":[{"bash":"echo $BUILD_ARTIFACTSTAGINGDIRECTORY"}]}` ctx:`{"run":{"Build.ArtifactStagingDirectory":"/a"}}` | `step0 env BUILD_ARTIFACTSTAGINGDIRECTORY` | `"/a"` | docs | predef#use-predefined-variables "becomes the variable BUILD_ARTIFACTSTAGINGDIRECTORY" |
| VARV-140 | macro | yaml:`{"variables":{"endpointUrl":"https://x"},"steps":[{"bash":"echo $(endpointUrl) $ENDPOINTURL"}]}` | `step0 $(endpointUrl)` and `env ENDPOINTURL` | `unavailable` (reserved prefix `endpoint`). VERIFY (suspect wording): prefix match on any name, not only `endpoint.`. | docs | vars#variable-naming-restrictions "won't be available to your tasks and scripts" |
| VARV-141 | macro | yaml:`{"variables":{"PATHROOT":"/r"},"steps":[{"bash":"echo $(PATHROOT)"}]}` | `step0 $(PATHROOT)` | `unavailable` (prefix check ignores case). VERIFY (same as VARV-140). | docs | vars#variable-naming-restrictions "(regardless of capitalization)"; naming#azure-pipelines |
| VARV-142 | logging | yaml:`{"steps":[{"bash":"echo '##vso[task.setvariable variable=secret.Sauce;issecret=true]garlic'"},{"bash":"echo \"$(secret.Sauce)\""}]}` | `step1 $(secret.Sauce)` | VERIFY: vars example shows `masked` value, contradicting the `secret` reserved-prefix rule (`unavailable`). | uncertain | vars#set-a-job-scoped-variable-from-a-script (secret.Sauce example) vs vars#variable-naming-restrictions |
| VARV-143 | compile | yaml-text:`variables:\n  my var: x\nsteps:\n- bash: echo hi` | `run` | naming-rule violation (`reject:schema` expected; VERIFY the platform's actual reaction) | docs | vars#variable-naming-restrictions "Don't use spaces in variables" |
| VARV-144 | macro | yaml:`{"variables":[{"name":"my-standalone-variable","value":"v"}],"steps":[{"bash":"echo $(my-standalone-variable)"}]}` | `step0 $(my-standalone-variable)` | VERIFY: naming rule allows only letters, digits, `.`, `_`, but vg examples use hyphenated names (likely `"v"`). | uncertain | naming#azure-pipelines; vg#use-variables-in-a-linked-variable-group (hyphenated examples) |
| VARV-145 | macro | yaml:`{"variables":{"Build.BuildId":"5"},"steps":[{"bash":"echo $(Build.BuildId)"}]}` ctx:`{"run":{"Build.BuildId":"42"}}` | `step0 $(Build.BuildId)` | VERIFY: `"42"` (read-only) vs `"5"` vs reject. Docs say both "read-only" and "you can change the value of a system variable before the pipeline runs". | uncertain | vars#system-variables; predef#use-predefined-variables "automatically set by the system and read-only" |
| VARV-146 | macro | yaml:`{"steps":[{"bash":"echo $AGENT_JOBSTATUS"}]}` | `step0 env AGENT_JOBSTATUS` | current job status, e.g. `"Succeeded"` | docs | predef#agent-variables-devops-services "should be referenced as AGENT_JOBSTATUS" |

## 2. Coverage index

| Topic | Rows |
|---|---|
| root/stage/job precedence, UI base | 001-007, 020-022 |
| same-scope later-wins, list vs mapping | 008-015 |
| variable groups | 016-029 |
| templates at declaration position | 013-014, 023, 030-034 |
| compile-time visibility (`${{ }}` YAML-only) | 026, 035-050 |
| macro literal / non-expanding contexts / recursion / case | 051-064 |
| runtime `$[ ]` | 065-075 |
| output variables, dependencies, stageDependencies, dependsOn | 076-100 |
| isOutput / isSecret / isReadOnly / setsecret / settableVariables | 101-114 |
| secrets (env mapping, masking, compile-time, forks) | 045, 063, 103-104, 113-122 |
| queue time, settable, org limit | 044, 123-130 |
| parameters vs variables | 050, 073, 131-136 |
| system/predefined read-only, reserved prefixes, naming | 062, 108-109, 129, 140-146 |
| environment mapping | 027-029, 079, 101-102, 137-139, 146 |
| empty vs undefined | 037, 051-052, 068, 118 |

## 3. Contradictions between `docs/spec/variable-precedence.md` and Microsoft docs

1. **Queue time vs YAML.** Spec precedence puts "allowed queue-time override" (5) above job YAML (4), and VAR-007
   expects `queued` over job `X=job`. Microsoft: "If you define a variable in both the variables block of a YAML and in
   the UI, the value in the YAML has priority" (vars#allow-at-queue-time). The YAML precedence list ranks job, stage,
   then pipeline YAML above "Variable set at queue time" (vars#expansion-of-variables). Rows: VARV-124, VARV-125.
2. **Logging mutation visibility.** Spec item 6: "runtime logging-command mutation from the current step forward". Microsoft:
   "You can't use the variable in the step that it's defined" (vars#set-a-job-scoped-variable-from-a-script). Macros expand
   before the step, so the setting step still sees the old value (VARV-059, VARV-101). The mutation applies from the
   *next* step.
3. **"Rejected, not ignored."** Spec: "A disallowed queue-time assignment MUST be rejected, not ignored". Microsoft documents
   rejection only when **Limit variables that can be set at queue time** is on (secin#limit-variables-that-can-be-set-at-queue-time).
   With the setting off, users "create variables that the pipeline author didn't define, to override system variables"
   (VARV-128, VARV-129). A queue value for a YAML-defined variable is not rejected; the YAML value simply wins (VARV-124).
4. **System variables.** Spec: "System variables are read-only unless vendor semantics explicitly permit override". This is
   consistent, but the exceptions must be modeled. Documented exceptions: `Build.Clean`, `System.Debug` (predef, VARV-062).
   Queue-time overrides are also allowed when the org limit is off (secin). The Microsoft variables page also says
   "you can change the value of a system variable before the pipeline runs" (vars#system-variables), next to "System
   variables are read-only" (VARV-145).
5. **"Declared dependency edge".** Spec: an output variable is "available only along its declared dependency edge". Microsoft:
   stages depend on the previous stage **implicitly** (vars#use-output-variables-from-tasks), so an undeclared edge also
   carries outputs (VARV-093). Visibility is also not transitive ("only available in the next downstream stage/job",
   VARV-083, VARV-094). The spec does not state this.
6. **Group order (consistent, already flagged).** The spec's "later groups override earlier groups" matches vg ("the last
   variable group ... sets the variable's value"). The same page's include note says collisions "may be unpredictable".
   The spec's VERIFY on this point is correct (VARV-017).

Internal inconsistencies inside Microsoft docs (no spec impact, but they make rows `uncertain`):
- texpr#context says "Only predefined variables can be used in template expressions", then says the context contains "all the
  variables specified in the YAML file". Examples (tmpl#variable-reuse, expr#expressions) confirm that YAML variables work.
- vars#use-outputs-in-a-different-stage restricts stageDependencies in conditions to "the stage level, but not the job level".
  expr#job-to-job-dependencies-across-stages shows a job-level condition (VARV-096).
- setvar FAQ says isOutput variables "aren't available in the same job". setvar#set-an-output-variable-for-use-in-the-same-job
  shows `$(setOutput.myOutputJobVar)` working in the same job (VARV-077/078).
- The reserved prefix `secret` (vars#variable-naming-restrictions) conflicts with the `secret.Sauce` examples on the same page (VARV-142).
- The naming rule (letters, digits, `.`, `_`) conflicts with the hyphenated names in vg examples (VARV-144).

## 4. VERIFY items (need live Azure DevOps conformance runs)

Capture method A (compile-time, no agent): Pipelines Preview API
`POST https://dev.azure.com/{org}/{project}/_apis/pipelines/{id}/preview?api-version=7.1` with `{"previewRun":true,"yamlOverride":...}`;
inspect `finalYaml` or the HTTP 400 message.
Capture method B (runtime): real run on a hosted agent; print `[$(x)]`, `[$X]` and `printenv` per step. Configure UI
variables, groups, queue values and org settings exactly as in `ctx`.

| Row | Question | Ruling used | Method |
|---|---|---|---|
| VARV-010 | duplicate key in mapping `variables` | reject | A |
| VARV-017 | two groups, same name, same scope | last group wins | B |
| VARV-019 | direct variable then group, same name | group (last) wins | B |
| VARV-022 | UI variable vs root group, same name | group wins | B |
| VARV-026 | group value visible to `${{ variables.x }}` | no (empty) | A |
| VARV-040 | `${{ }}` forward reference within one variables block | empty | A |
| VARV-041 | compile-time `variables` context respects job/stage shadowing | yes | A |
| VARV-048 | `${{ variables.b }}` where b is `$[ ]` | literal `$[ ... ]` | A |
| VARV-052 | macro of a defined but empty variable | replaced by empty | B |
| VARV-069 | `$[ ]` not covering the whole value | left literal | A+B |
| VARV-070 | `$[ ]` inside a script body | literal (docs example claims evaluation) | B |
| VARV-073 | `$[ parameters.x ]` | unavailable; error vs empty unknown | A+B |
| VARV-078 | bare `$(name)` after `isOutput=true` in the same job | literal | B |
| VARV-086 | sliced job output without job prefix | first slice (doc comment) | B (parallel: 2) |
| VARV-096 | job-level condition using stageDependencies | runs | B |
| VARV-105 | overwrite of `isreadonly=true` variable: warning or error | unchanged + warning | B |
| VARV-107 | YAML `readonly: true` at root shadowed at job scope | job value | A+B |
| VARV-122 | secret copied through `$[ variables.s ]` | masked value | B |
| VARV-129 | queue-time override of a system variable with org limit off | unknown | B (Runs API) |
| VARV-130 | queue-time set of a non-settable UI variable with org limit off | accepted | B (Runs API) |
| VARV-140, VARV-141 | reserved-prefix filtering on any name starting with `endpoint`/`path`/... | unavailable | B |
| VARV-142 | `secret.`-prefixed variable set by logging command | conflicting | B |
| VARV-143 | variable name with a space | reject | A |
| VARV-144 | hyphenated variable names | accepted | A+B |
| VARV-145 | YAML redefinition of a read-only predefined variable | unknown | B |
