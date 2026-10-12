# Azure Pipelines `modernize`: retired task versions

> **Status:** detection is approved (sections 1-4, 7 and the task, replacement and class columns of section 8): `trasloco modernize --check` is built from it.
> The rewrite rules (section 5, section 6, the "Input mapping" column and the rewrite vectors) are **draft**: open review findings are tracked in
> [issue #25](https://github.com/HarnageaGabriel/trasloco/issues/25) and must be resolved before `--write` is implemented.


Normative for `internal/dialect/azurepipelines/modernize`.

This specification defines how `trasloco modernize` rewrites references to retired Azure Pipelines task versions in YAML pipelines and templates. It was authored independently of the implementation: the tables in section 8 and the vectors in section 10 are the oracle. Keywords MUST, MUST NOT, SHOULD and MAY are used as in `README.md`; `VERIFY:` marks a fact that no cited official source confirms (section 11).

Microsoft announced that the tasks listed in section 8 retire on **2026-10-15** (still runnable, with a non-failing retirement error) and are **removed on 2027-03-15** (references then fail with a blocking error) [RET]. `catalog/azure-pipelines/tasks.retirement.yaml` (73 entries) is generated from that page and has exactly the same entries as section 8.

Notation for sources: `[RET]` is the retirement page, `[o:Name@N]` the `task.json` of the retired version, `[n:Name@N]` the `task.json` of the replacement; all link targets are listed in section 14. Old `task.json` files are either in the pinned catalog commit `f1962f12fac2899b5d689967dda0d25523cc3ec4` or, for the "legacy built-in" tasks that the repository no longer carries, in the public git history of `microsoft/azure-pipelines-tasks` under `Tasks/<Name>/task.json` (older, unversioned directory layout), pinned by commit SHA. Where neither exists the row says "old task.json not public".

## 1. Purpose and scope

1. `modernize` reads one YAML file at a time (a pipeline or a template of any kind), finds `task:` steps that reference a retired task version, and either rewrites the reference to the recommended replacement or reports why it cannot. It parses with the structure reader of `ado-yaml-structure.md`; it does **not** expand templates, so a template is rewritten in its own file, regardless of which pipelines include it.
2. Only the 73 retired task versions of section 8 are in scope. Other deprecated or old task versions (for example `Maven@3`) are left alone. Classic build and release definitions, task groups, pipeline decorators and Marketplace extension tasks are out of scope.
3. The tool changes text only where a row of section 8 allows it. It MUST NOT infer a mapping, MUST NOT contact the network and MUST NOT write the catalog.
4. The schedule is that of Azure DevOps Services [RET]; Azure DevOps Server availability of the replacement majors is not established (VERIFY: V19).

## 2. Safety principles

| ID | Principle |
|---|---|
| S1 | **No silent behaviour change.** Every rewritten step produces exactly one `TRL-MOD-0001` (class A) or `TRL-MOD-0002` (class B) diagnostic; every step that is not rewritten but references a retired task produces one of `TRL-MOD-0003`..`0006`, `0009`. |
| S2 | **Minimal diff.** The output is the input text with byte-range splices (section 6). The AST printer MUST NOT produce the output: it drops comments and normalizes layout (`ado-yaml-structure.md` section 15), which contradicts the plan sentence "AST rewrite + existing printer". Comments, blank lines, quoting, key order and line endings outside the edited ranges are preserved. |
| S3 | **Idempotent.** Running `modernize` on its own output changes nothing and reports no rewrite diagnostics. This holds because no replacement of section 8 is itself a retired entry (checked for all 73 rows, no chains). Class C/D steps and guard failures are reported again on every run, unchanged. |
| S4 | **Default deny.** An input of the retired task that the row neither passes through nor handles explicitly makes the step needs-manual (section 5.4). |
| S5 | **Literal values only.** A value is translated only when it is a literal scalar. A macro `$(x)`, a template expression `${{ }}` or a runtime expression `$[ ]` in a value that needs translation is needs-manual. Values that pass through unchanged may be anything. |
| S6 | **Class C and D steps are never edited.** The diagnostic carries the official recommendation. |
| S7 | **Failure isolation.** A guard failure affects only its step; all edits of that step are discarded, other steps in the file are still rewritten. |
| S8 | **Self-check.** After applying the edits the tool re-parses the result. Parse failure, a different number of steps, or any node outside the edited ranges that differs from the input is `TRL-INT-0002` and no file is written. |
| S9 | **Determinism.** Same input and same catalog give byte-identical output and diagnostics (ordering per `diagnostics.md` section 4). |
| S10 | **What the oracle can and cannot show.** The classes were derived from `task.json` facts (input names, aliases, types, defaults, options, visibility rules). Runtime behaviour of a new major that is not visible in `task.json` is not verified (VERIFY: V1); this is why every rewrite is reported, never silent. |

## 3. Recognising task references

### 3.1 Where

A step is a mapping that has a `task` key whose value is a scalar. Keys are matched case-insensitively (`ado-yaml-structure.md` 2.6). Steps are found in: root `steps`, `steps` of jobs, deployment lifecycle hooks, stage-less shapes, bodies of `${{ if }}`, `${{ elseif }}`, `${{ else }}` and `${{ each }}` directives, and in free-form values of `parameters` (template call sites, `extends`, and the `default` of parameters of type `step` or `stepList`) when the mapping has a `task` scalar and every other key is one of `inputs`, `displayName`, `name`, `condition`, `continueOnError`, `enabled`, `env`, `timeoutInMinutes`, `retryCountOnTaskFailure`, `target`. A step with `enabled: false` is treated like any other step.

### 3.2 Reference forms

The grammar is that of `ado-yaml-structure.md` section 12: `Name@Version` or `GUID@Version`, with `Version` = `N`, `N.M` or `N.M.P`. The retirement lookup key is the pair (task identity, major `N`). Source: [TASKS] ("specify the major version by using `@`", "full task version number after the `@`").

1. **Name form.** The name is compared case-insensitively with the `task` column of section 8. `copyfiles@1`, `COPYFILES@1` and `CopyFiles@1` are the same reference. The replacement is written in the canonical spelling of section 8.
2. **Full version.** `CopyFiles@1.1.4` is treated as major 1. The replacement is written as a major only (`CopyFiles@2`) and `TRL-MOD-0011` is added. VERIFY: V15 (whether a full version pins exactly that version).
3. **GUID form.** A GUID is compared case-insensitively. The identity is resolved with the pair (GUID, major): the candidates are the entries of section 8 whose GUID column equals the GUID and whose major equals `N`. Exactly one candidate means a retired reference. More than one, or none, means no rewrite and `TRL-MOD-0005` only when at least one entry of section 8 has that GUID. Several tasks share a GUID: `333b11bd-d341-40d9-afcf-b32d5ce6f23b` is `NuGetInstaller@0`, `NuGetRestore@1` and the live `NuGetCommand@2`; a bare-GUID lookup by "lexically first name" (`catalog.md`) is therefore insufficient and MUST NOT be used for retirement. The rewritten reference keeps the GUID and changes only `@N` when the replacement has the same GUID (true for the replacement in 60 of the 73 rows whose replacement is a single task and whose GUID is known; the implementation compares the catalog `id` of the replacement with the row's GUID); otherwise it is written in name form and `TRL-MOD-0008` is added.
4. **Namespaced names.** A name that contains a `.` (for example `publisher.extension.CopyFiles@1`) is an extension-contributed task, not the built-in. It is never rewritten. If its last segment equals a retired name (case-insensitive), `TRL-MOD-0010` (info) is reported. VERIFY: V14 (the naming scheme of extension task references is not described on [TASKS]; the page only says a custom task whose name equals a built-in one resolves to the built-in, and that a custom task can be referenced by its GUID).
5. **Dynamic references.** A value that contains `${{`, `$(` or `$[` is not static: no edit, `TRL-MOD-0005`. A reference without `@`, with an empty name or a malformed version is the parser's `TRL-PARSE-0015`; the file is not modified.
6. **Strings that look like references.** A scalar in `parameters` (pipeline-level, template declarations including `default` and `values`, template call sites) or in `variables` whose whole trimmed text is a retired reference (name or GUID form) is not edited and is reported with `TRL-MOD-0005`, because the tool cannot know where the string is used (for example `task: ${{ parameters.taskName }}`). Mappings that are steps by 3.1 are not strings and are rewritten.

### 3.3 Rewriting is decided per step

For each recognised retired step the tool looks up its row in section 8 and applies the class rules of section 4. Two retired steps in one file are independent (S7). Order of evaluation is source order.

## 4. Classes

Classes are data (column "Class" of section 8). The implementation MUST NOT recompute them. The criteria below explain how they were assigned from the cited `task.json` files.

| Class | Meaning | Action | Diagnostic | Fidelity |
|---|---|---|---|---|
| A | Same task family (the replacement is the same task at a newer major), every input of the retired version exists in the replacement with the same name or alias, the same type, the same value domain (the replacement may add options) and the same default; every input that is new in the replacement is optional and its default is a no-op or has no effect on a pipeline that does not use it. | Change only the task reference (E1). | `TRL-MOD-0001` (info) | exact |
| B | The replacement is a single task (same family, or a documented single-task successor), but at least one input is renamed, translated, removed, or has a different default, or a new input has a default that changes what the task does. Every such difference is listed in the mapping column. | Apply the mapping (section 5), then E1. | `TRL-MOD-0002` (warning) with the changes and the `review` items in `params` | approximated |
| C | The documented replacement is a different task, more than one task (the tool never splits a step into several), a task with a different input surface, or the old `task.json` is not public so that no mapping can be documented. | No edit. | `TRL-MOD-0003` (warning) with the official replacement and the reason | needs-manual |
| D | There is no replacement task: the platform or product is retired (Xamarin, App Center, Azure Cloud Services), the project is archived, or Microsoft points to an external tool or a script. | No edit. | `TRL-MOD-0004` (warning) with the summarized official guidance | needs-manual |

Class A and B rows never need a human decision when no guard fires; a guard failure (section 5.4) turns the step into needs-manual with `TRL-MOD-0006` and no edit.

## 5. Mapping language (class A and B)

The mapping column of a B row is a `;`-separated list of operations. A row has no operation for the inputs that pass through.

### 5.1 Matching and values

1. An input key is matched against the names and aliases of the **old** task, case-insensitively. The old input sets are the catalog facts when the old major is in `tasks.generated.yaml` (rows 19, 30..33, 35, 36, 44, 50) and Appendix A otherwise. Names and aliases of the new task come from the catalog.
2. *Present* means the key exists in the step's `inputs` mapping, even with a null value (a null value is the empty string). *Literal* means a plain, quoted or block scalar that contains none of `${{`, `$(`, `$[`.
3. Condition operands: `K = V` is true when K is present, literal and equal to V case-insensitively; `K != V` is true when K is present and (non-literal or not equal); `unless K = V` negates the first form. Several conditions after `if` are joined with `and`.

### 5.2 Operations

| Operation | Meaning |
|---|---|
| `rename A -> B` | Replace the key text of input A (any old name or alias) by the canonical new name B; the value is untouched. If the new task also accepts the spelling the user wrote, the key is **not** renamed unless the row says `rename`. |
| `map K: a -> b, c -> d` | If K is present and literal and equals a (case-insensitive), replace the value by b, and so on. A literal value not listed passes through unchanged. A non-literal value is a guard failure. |
| `invert A -> B` | Boolean input A becomes input B with the opposite value (`true` to `false` and back), in place. A non-literal or non-boolean value is a guard failure. |
| `drop K` / `drop K if V` | Remove input K (E6). With `if V` it is removed only when its literal value equals V; otherwise it falls to default deny. |
| `pin K=V [if C] [unless C]` | If neither K nor any alias of K is present (and the condition holds) append `K: V` (E4). V is the exact YAML text to write. A pin keeps the old behaviour when the new default differs. |
| `add K=V` | Append `K: V` unconditionally (K is a new input; the row explains why). |
| `guard C` | If the condition holds the step is needs-manual (`TRL-MOD-0006`), anchored at the key of the first input named in C. |
| `require K [if C]` | If the condition holds (or always) and K is absent, the step is needs-manual (`TRL-MOD-0006`), anchored at the task value when there is no condition, otherwise at the first input of the condition. |
| `literal K` | If K is present and non-literal the step is needs-manual (`TRL-MOD-0006`). |
| `review K=V` | No edit. The new default V is listed in `params.review` of `TRL-MOD-0002` so that a reader sees what changed implicitly. |

Constant `K1` (a three-line string, no trailing newline): `{`, `"Content-Type":"application/json"`, `}` joined with line feeds. It is written as a block scalar with the strip indicator (vector MODV-042).

### 5.3 Order

1. Evaluate all `guard`, `require` and `literal` operations and the default-deny rule (5.4) on the original step. If any fires, report each firing and stop (S7).
2. Apply `drop`, then `rename`, `map`, `invert` (in place), then `pin` and `add` in the order written in the row. Then edit the task reference (E1).

### 5.4 Default deny and unknown inputs

1. An input that is in the old set, is not a name or alias of the new task, and is not handled by an operation, is a guard failure when present (reason `removed-input`, anchored at its key). This is how removed features (Gradle coverage and SonarQube inputs, `outputPattern`, ...) are kept from being dropped silently.
2. An input key that is in neither the old set nor the new set is preserved as written and reported with `TRL-MOD-0007`; the rewrite still proceeds, because the same key was already unknown before.
3. For B rows, a step whose `inputs` mapping contains a template directive (`${{ if }}`, `${{ each }}`), a dynamic key, or is written in flow style is needs-manual with `TRL-MOD-0009` (edits would have to be evaluated against conditions). Class A needs no input edit and is therefore rewritten in any layout.

## 6. Edit rules

| ID | Rule |
|---|---|
| E1 | Task reference: replace only the reference text inside the scalar. Quote style, a trailing comment and surrounding spacing are preserved. Name form becomes the replacement `Name@Major` (canonical case); GUID form keeps the GUID and changes the version when the replacement has the same GUID (section 3.2.3). |
| E2 | Key rename: replace only the key text; quotes around a key are preserved. |
| E3 | Value change: replace only the scalar text; keep the quote style when the new text is representable in it, otherwise use single quotes. |
| E4 | Append: new lines are inserted after the last non-comment content line of the `inputs` mapping (including the nested lines of its last entry), at the indentation of the first entry of `inputs`, in pin order. |
| E5 | Create `inputs`: when the step has no `inputs`, insert `inputs:` after the last non-comment content line of the step mapping at the column of the step's keys, followed by the pinned entries one indentation unit deeper (the unit is the file's first observed nested indentation, default two spaces). |
| E6 | Delete: remove an entry from its first character to the end of its last line (including block scalar continuation lines and a trailing comment on those lines). Comment-only lines before it stay. If no entry remains, `inputs:` is removed too. |
| E7 | Line endings: inserted lines use the dominant line ending of the file (LF if tied). The state of the final newline is preserved. |
| E8 | All edits are computed against the original positions and applied from the end of the file backwards; edits never overlap; inserts at one offset keep pin order. |
| E9 | Unknown nodes, anchors, merge keys and multiple documents are the parser's errors (`TRL-PARSE-*`); the file is then left unmodified. |

## 7. Diagnostics

Diagnostics use the model of `diagnostics.md`. Positions refer to the input file. The codes are **proposed** in section 13 and do not exist in the registry yet; `TRL-CAT-0002` (retired task, replacement used) remains the code of `convert`. Ordering is `(line, column, code)`. `modernize` exits 0 with warnings unless `--fail-on` says otherwise (`diagnostics.md` section 8, last paragraph).

| Code | Emitted when | Anchor | `params` |
|---|---|---|---|
| `TRL-MOD-0001` | class A rewrite | task value | `from`, `to` |
| `TRL-MOD-0002` | class B rewrite | task value | `from`, `to`, `changes[]`, `review[]` |
| `TRL-MOD-0003` | class C step | task value | `from`, `replacements[]`, `reason` |
| `TRL-MOD-0004` | class D step | task value | `from`, `guidance` |
| `TRL-MOD-0005` | dynamic reference, ambiguous GUID, or string that names a retired task | the value | `value` |
| `TRL-MOD-0006` | guard failure in a class A/B step (no edit) | the offending input key, or the task value for `require` without condition | `from`, `input`, `reason` |
| `TRL-MOD-0007` | input unknown to old and new task, preserved | the key | `from`, `input` |
| `TRL-MOD-0008` | GUID reference replaced by a name | task value | `from`, `to` |
| `TRL-MOD-0009` | step layout cannot be edited safely (flow style, directive or dynamic key in `inputs`) | task value | `from`, `reason` |
| `TRL-MOD-0010` | namespaced task not rewritten although its last segment is a retired name | task value | `value` |
| `TRL-MOD-0011` | full version reference replaced by a major | task value | `from`, `to` |

## 8. The 73 retired task versions

Columns: `#`; the retired task and its GUID (the GUID is the `id` of the task; it is the same for every version of a task); the replacement named by [RET]; the class (section 4); the input mapping (section 5) or, for C and D, the reason or the official guidance; sources. Old input sets that are not in the catalog are in Appendix A. "VERIFY: Vn" refers to section 11.

Class counts: A = 3, B = 22, C = 28, D = 20.

| # | Retired task / GUID | Replacement | Class | Input mapping | Sources |
|---|---|---|---|---|---|
| 1 | `AndroidBuild@1`<br>`df857559-8715-46eb-a74e-ac98b9178aa0` | `Gradle@4` | C | The old task also manages an Android emulator (inputs `avdName`, `createAvd`, `startEmulator`, `emulatorTarget`, ...) that `Gradle@4` does not offer; the Gradle inputs differ (`gradleWrapper`, `gradleProj`, `gradleArguments` against `wrapperScript`, `cwd`, `tasks`). | [RET] [o:AndroidBuild@1] [n:Gradle@4] |
| 2 | `AndroidSigning@1`<br>`80f3f6a0-82a6-4a22-ba7a-e5b8c541b9b9` | `AndroidSigning@3` | C | V1 signs with jarsigner (`jarsign`, `jarsignerArguments`, `keystoreFile` as filePath); V3 signs with apksigner (`apksign`, `apksignerArguments`, `apksignerVersion`) and types `keystoreFile` as secureFile. The signing tool changes. | [RET] [o:AndroidSigning@1] [n:AndroidSigning@3] |
| 3 | `AndroidSigning@2`<br>`80f3f6a0-82a6-4a22-ba7a-e5b8c541b9b9` | `AndroidSigning@3` | C | `jarsign` and `jarsignerArguments` (default `-verbose -sigalg MD5withRSA -digestalg SHA1`) are removed; `apksign`, `apksignerArguments`, `apksignerVersion`, `apksignerLocation` are added; the aliases of the keystore inputs change from `jarsigner*` to `apksigner*`. The tool that signs changes. | [RET] [o:AndroidSigning@2] [n:AndroidSigning@3] |
| 4 | `AppCenterDistribute@0`<br>`b832bec5-8c27-4fef-9fb8-6bec8524ad8a` | none | D | Official: use the App Store/TestFlight or Google Play Azure Pipelines extensions; App Center distribution is retired. The `deprecationMessage` of the V1/V2 `task.json` names `AppCenterDistribute@3`, which [RET] does not recommend, so it is not used. | [RET] [o:AppCenterDistribute@0] |
| 5 | `AppCenterDistribute@1`<br>`b832bec5-8c27-4fef-9fb8-6bec8524ad8a` | none | D | As row 4. | [RET] [o:AppCenterDistribute@1] |
| 6 | `AppCenterDistribute@2`<br>`b832bec5-8c27-4fef-9fb8-6bec8524ad8a` | none | D | As row 4. | [RET] [o:AppCenterDistribute@2] |
| 7 | `ArchiveFiles@1`<br>`d8b84976-e99a-4b86-b885-4849694435b0` | `ArchiveFiles@2` | B | `rename rootFolder -> rootFolderOrFile; map archiveType: default -> zip`. In V1 the option key `default` has the label `zip`; the V2 option is `zip`; the defaults (`default`, `zip`) are the same archive type. `review sevenZipCompression=normal, verbose=false, quiet=false` (new, only with `7z` or off). | [RET] [o:ArchiveFiles@1] [n:ArchiveFiles@2] |
| 8 | `AutomatedAnalysis@0`<br>`6b519857-1a20-4248-bae8-aad039015afb` | none | D | Official: replace the task with a supported analysis or monitoring workflow appropriate for the application. | [RET] [o:AutomatedAnalysis@0] |
| 9 | `AzureCLI@0`<br>`46e4be58-730b-4389-8a2f-ea10b3e5e815` | `AzureCLI@3` | C | `scriptType` (`bash`, `batch`, `ps`, `pscore`) is required in V3 with no default and V0 named no shell; `failOnStandardError` default changes from `true` to `false`; the classic connection input `connectedServiceName` is gone; `args` becomes `scriptArguments`. | [RET] [o:AzureCLI@0] [n:AzureCLI@3] |
| 10 | `AzureCloudPowerShellDeployment@1`<br>`2ca8fe15-42ea-4b26-80f1-e0738ec17e89` | `AzurePowerShell@5` (named by [RET]) | D | Azure Cloud Services (classic) is being retired (`task.json` message, `aka.ms/cloudservicesretirement`). `AzurePowerShell@5` only runs a script; none of the deployment inputs (`CsPkg`, `CsCfg`, `ServiceName`, ...) exist. | [RET] [o:AzureCloudPowerShellDeployment@1] [n:AzurePowerShell@5] |
| 11 | `AzureCloudPowerShellDeployment@2`<br>`2ca8fe15-42ea-4b26-80f1-e0738ec17e89` | `AzurePowerShell@5` (named by [RET]) | D | As row 10. | [RET] [o:AzureCloudPowerShellDeployment@2] [n:AzurePowerShell@5] |
| 12 | `AzureFunction@0`<br>`537fdb7a-a601-4537-aa70-92645a2b5ce4` | `AzureFunction@2` | B | `pin headers=K1`. The V2 default of `headers` is a larger JSON object that adds `PlanUrl`, `AuthToken: $(system.AccessToken)` and other callback headers; the V0 default is `K1`. `review azureServiceConnection (new, optional); key is optional in V2`. Both are server (agentless) tasks. | [RET] [o:AzureFunction@0] [n:AzureFunction@2] |
| 13 | `AzureMonitor@0`<br>`99a72e7f-25e4-4576-bf38-22a42b995ed8` | `AzureMonitor@1` | C | The input sets are disjoint: `ResourceType`, `resourceName`, `alertRules` against `filterType`, `resource`, `alertRule`, `severity`, `timeRange`, `alertState`, `monitorCondition`. The `task.json` message points at the migration of classic alerts. | [RET] [o:AzureMonitor@0] [n:AzureMonitor@1] |
| 14 | `AzureNLBManagement@1`<br>`e94f1750-a6a8-11e6-be69-bdf37a7b15d8` | `AzurePowerShell@5` or `AzureCLI@3` (either) | C | No equivalent task: both replacements run a script, which a person must write; the old inputs have no counterpart. | [RET] [o:AzureNLBManagement@1] [n:AzurePowerShell@5] [n:AzureCLI@3] |
| 15 | `AzurePowerShell@1`<br>`72a1931b-effb-4d2e-8fd8-f8472a07cb62` | `AzurePowerShell@5` | C | `ConnectedServiceNameSelector` defaults to the classic Azure connection in V1; V5 only has the ARM connection (`azureSubscription`) and new defaults `errorActionPreference: stop`, `TargetAzurePs: OtherVersion` that change failure and version behaviour. | [RET] [o:AzurePowerShell@1] [n:AzurePowerShell@5] |
| 16 | `AzureResourceGroupDeployment@1`<br>`94a74903-f93f-4075-884f-dc11f34058b4` | `AzureResourceGroupDeployment@2` | C | Old task.json not public: the history of `Tasks/AzureResourceGroupDeployment` starts at major 2, so the V1 inputs are unknown. | [RET] [n:AzureResourceGroupDeployment@2] |
| 17 | `AzureRmWebAppDeployment@2`<br>`497d490f-eea7-4f2b-ab94-48d9c1acdcb1` | `AzureRmWebAppDeployment@5` | C | Several defaults flip from `false` to `true` (`ExcludeFilesFromAppDataFlag`, `RenameFilesFlag`, `TakeAppOfflineFlag`), `AdditionalArguments` gets a default, `DeployToSlotFlag` becomes `DeployToSlotOrASEFlag`, `ConnectionType` and `WebAppKind` are new and required. | [RET] [o:AzureRmWebAppDeployment@2] [n:AzureRmWebAppDeployment@5] |
| 18 | `AzureWebPowerShellDeployment@1`<br>`dcbef2c9-e4f4-4929-82b2-ea7fc9166109` | `AzureRmWebAppDeployment@5` | C | A different task (classic Azure web app deployment with PowerShell) replaced by an ARM web app deployment with `ConnectionType` and `WebAppKind`; no documented input mapping. | [RET] [o:AzureWebPowerShellDeployment@1] [n:AzureRmWebAppDeployment@5] |
| 19 | `CacheBeta@0`<br>`d53ccab4-555e-4494-9d06-11db043fb4a9` | `Cache@2` | B | `guard multiline(key)`: V0 takes a newline-delimited list of key parts, V2 a single string segmented with `|`, so a multi-line key is needs-manual. A single-line key passes through. `review restoreKeys (new, empty), saveOnPartialSuccess=false`. Cache keys are identifiers: a first run after the rewrite may miss the cache (VERIFY: V5). | [RET] [o:CacheBeta@0] [n:Cache@2] |
| 20 | `Chef@1`<br>`b719db6c-40a2-4f43-9aff-827825baecae` | none | D | Official: a supported Chef extension or a script that uses the current Chef CLI. | [RET] [o:Chef@1] |
| 21 | `ChefKnife@1`<br>`c7b7ccf9-d6e0-472b-97bb-06b6e43504f3` | none | D | Official: a supported Chef extension or a script that uses the current Chef CLI (Knife client directly from a script step). | [RET] [o:ChefKnife@1] |
| 22 | `CmdLine@1`<br>`d9bafed4-0b18-4f58-968d-86655b4d2ce9` | `CmdLine@2` | C | V1 runs a program (`filename`, `arguments`, `workingFolder`, `failOnStandardError`); V2 runs a script (`script`, `workingDirectory`, `failOnStderr`). Joining a program and its arguments into a script changes quoting. | [RET] [o:CmdLine@1] [n:CmdLine@2] |
| 23 | `CondaEnvironment@1`<br>`03dd16c3-43e0-4667-ba84-40515d27a410` | none | D | Official: use a script step to create and manage the Conda environment. | [RET] [o:CondaEnvironment@1] |
| 24 | `CopyFiles@1`<br>`5bfb729a-a7c8-4a78-a7c3-8d717bb7c13c` | `CopyFiles@2` | A | Inputs `SourceFolder`, `Contents`, `TargetFolder`, `CleanTargetFolder`, `OverWrite`, `flattenFolders` are identical (name, type, default). New and optional with no-op defaults: `preserveTimestamp=false`, `retryCount=0`, `delayBetweenRetries=1000`, `ignoreMakeDirErrors=false`. | [RET] [o:CopyFiles@1] [n:CopyFiles@2] |
| 25 | `CopyPublishBuildArtifacts@1`<br>`1d341bb0-2106-458c-8422-d00bcea6512a` | `CopyFiles@2` and `PublishBuildArtifacts@1` | C | One step becomes two; `CopyRoot`, `Contents`, `ArtifactName`, `ArtifactType`, `TargetPath` are split between the two replacements (`SourceFolder`, `Contents`, `TargetFolder` and `PathtoPublish`, `ArtifactName`, `publishLocation`, `TargetPath`). `modernize` never splits a step. | [RET] [o:CopyPublishBuildArtifacts@1] [n:CopyFiles@2] [n:PublishBuildArtifacts@1] |
| 26 | `cURLUploader@1`<br>`ad8974d8-de11-11e4-b2fe-7fb898a745f3` | `cURLUploader@2` | B | `add authType=UserAndPass; add remotePath=''`. V1 always used inline `url`, `username`, `password`; in V2 these are only visible for `authType: UserAndPass` (default `ServiceEndpoint`). The V2 default `remotePath: upload/$(Build.BuildId)/` is a sub-folder appended to the URL, which V1 did not have, so it is pinned empty. | [RET] [o:cURLUploader@1] [n:cURLUploader@2] |
| 27 | `DeployVisualStudioTestAgent@1`<br>`52a38a6a-1517-41d7-96cc-73ee0c60d2b6` | `VSTest@3` | C | The old task deploys the Visual Studio test agent; `VSTest@3` runs tests. The purposes differ and no input mapping is documented. | [RET] [o:DeployVisualStudioTestAgent@1] [n:VSTest@3] |
| 28 | `DotNetCoreCLI@0`<br>`5541a522-603c-47ad-91fc-a4b1d163081b` | `DotNetCoreCLI@2` | B | `literal command; pin publishTestResults=false if command = test; pin modifyOutputPath=false if command = publish; pin selectOrConfig=config if command = restore`. V0 inputs `command`, `publishWebProjects`, `projects`, `arguments`, `zipAfterPublish` all exist in V2 with the same names and defaults. The three pins keep V0 behaviour against new defaults: V2 adds `--logger trx` and publishes results for `test` (default `true`), prefixes the project folder to the publish path (default `true`) and selects feeds in the task (default `select`). VERIFY: V3, V4. | [RET] [o:DotNetCoreCLI@0] [n:DotNetCoreCLI@2] |
| 29 | `DotNetCoreCLI@1`<br>`5541a522-603c-47ad-91fc-a4b1d163081b` | `DotNetCoreCLI@2` | B | As row 28 (the V1 input set is the same five inputs; `projects` is `multiLine` in V1). | [RET] [o:DotNetCoreCLI@1] [n:DotNetCoreCLI@2] |
| 30 | `DotNetCoreInstaller@0`<br>`b0ce7256-7898-45d3-9cb5-176b752bfea6` | `UseDotNet@2` | B | `pin version='1.0.4'`. `packageType` and `version` exist in `UseDotNet@2`; the V0 default of `version` was `1.0.4` and V2 has no default. The task name changes, the GUID stays. | [RET] [o:DotNetCoreInstaller@0] [n:UseDotNet@2] |
| 31 | `DotNetCoreInstaller@1`<br>`b0ce7256-7898-45d3-9cb5-176b752bfea6` | `UseDotNet@2` | B | `pin version='2.2.x'`. `packageType`, `version`, `includePreviewVersions`, `installationPath`, `performMultiLevelLookup` exist in V2 with equal defaults except `version` (V1 default `2.2.x`). | [RET] [o:DotNetCoreInstaller@1] [n:UseDotNet@2] |
| 32 | `DownloadPackage@0`<br>`8d6e8f7e-267d-442d-8c92-1f586864c62f` | `DownloadPackage@1` | B | No input change (`feed`, `definition`, `version`, `downloadPath`). `review packageType=nuget (the V0 help text says only NuGet packages are supported), extract=true (new)`. VERIFY: V6. | [RET] [o:DownloadPackage@0] [n:DownloadPackage@1] |
| 33 | `DownloadPipelineArtifact@0`<br>`61f2a582-95ae-4948-b34d-a1b3c4f6a737` | `DownloadPipelineArtifact@2` | B | `guard pipelineId != ''; drop pipelineId if ''; pin artifact=drop`. `artifactName` and `targetPath` are aliases of `artifact` and `path` in V2 and pass through. V0 defaulted `artifactName` to `drop`; V2's `artifact` has no default and then downloads every artifact. A specific run needs `project` and `pipeline` in V2, which V0 did not have. `review patterns=**`. | [RET] [o:DownloadPipelineArtifact@0] [n:DownloadPipelineArtifact@2] |
| 34 | `DuffleInstaller@0`<br>`11ea7cf0-8a3e-4b5a-98a2-8756cc3094b0` | none | D | Official: a script step that installs the required supported CNAB tooling; the Duffle project is archived (`task.json` message). | [RET] [o:DuffleInstaller@0] |
| 35 | `FtpUpload@1`<br>`6f8c69a5-b023-428e-a125-fccf4efcb929` | `FtpUpload@2` | B | `guard overwrite != true; drop overwrite if true`. `overwrite` (default `true`) does not exist in V2; all other inputs pass through. `review implicitFTPS=false, enableUtf8=false, customCmds (empty)`. VERIFY: V2 (V2 overwriting by construction). | [RET] [o:FtpUpload@1] [n:FtpUpload@2] |
| 36 | `GitHubRelease@0`<br>`7b5a6198-adf8-4b16-9939-7addf85708b2` | `GitHubRelease@1` | B | `map tagSource: auto -> gitTag, manual -> userSpecifiedTag; map releaseNotesSource: file -> filePath, input -> inline; rename releaseNotesFile -> releaseNotesFilePath; rename releaseNotes -> releaseNotesInline; review makeLatest=true (new)`. The option keys changed; the labels (`Git tag`, `User specified tag`, `Release notes file`, `Inline release notes`) are the same, and the defaults (`auto`/`gitTag`, `file`/`filePath`) correspond. VERIFY: V7. | [RET] [o:GitHubRelease@0] [n:GitHubRelease@1] |
| 37 | `Gradle@1`<br>`8d8eebd8-2b94-4c97-85af-839254cc6da4` | `Gradle@4` | B | `drop codeCoverageTool if None; pin testResultsFiles='**/build/test-results/TEST-*.xml' unless publishJUnitResults = false`. The code coverage inputs (`classFilesDirectories`, `classFilter`, `failIfCoverageEmpty`) and the SonarQube connection inputs (`sqConnectedServiceName`, `sqProjectName`, `sqProjectKey`, `sqProjectVersion`, `sqDb*`, `sqAnalysis*`) are not in V4 and fall under default deny when present. The V4 default of `testResultsFiles` is `**/TEST-*.xml`. | [RET] [o:Gradle@1] [n:Gradle@4] |
| 38 | `InstallAppleCertificate@0`<br>`d2eff759-736d-4b7b-8554-7ba0960d49d6` | `InstallAppleCertificate@2` | B | `require keychainPassword if keychain != temp`. In V2 `keychainPassword` is required for the `default` and `custom` keychains; in V0 it was optional. All other inputs pass through. `review setUpPartitionIdACLForPrivateKey (new, unset), opensslPkcsArgs (new, empty)`. VERIFY: V12. | [RET] [o:InstallAppleCertificate@0] [n:InstallAppleCertificate@2] |
| 39 | `InstallAppleCertificate@1`<br>`d2eff759-736d-4b7b-8554-7ba0960d49d6` | `InstallAppleCertificate@2` | B | As row 38. | [RET] [o:InstallAppleCertificate@1] [n:InstallAppleCertificate@2] |
| 40 | `InstallAppleProvisioningProfile@0`<br>`0f9f66ca-250e-40fd-9678-309bcd439d5e` | `InstallAppleProvisioningProfile@1` | A | `provProfileSecureFile` and `removeProfile` are identical. The only new required input `provisioningProfileLocation` defaults to `secureFiles`, the only source V0 had. | [RET] [o:InstallAppleProvisioningProfile@0] [n:InstallAppleProvisioningProfile@1] |
| 41 | `InvokeRESTAPI@0`<br>`9c3e8943-130d-4c78-ac63-8af81df62dfb` | `InvokeRESTAPI@1` | B | `pin headers=K1`. The V1 default of `headers` adds `PlanUrl`, `AuthToken: $(system.AccessToken)` and further callback headers to every request; the V0 default is `K1`. `connectedServiceName` (alias `serviceConnection`) and the connection selector default (`connectedServiceName`) are unchanged. Server (agentless) task. | [RET] [o:InvokeRESTAPI@0] [n:InvokeRESTAPI@1] |
| 42 | `JenkinsQueueJob@1`<br>`c24b86d4-4256-4925-9a29-246f81aa64a7` | `JenkinsQueueJob@2` | B | All eight inputs pass through (`parameterizedJob` gains the alias `isParameterizedJob`). `review retryCount=3, delayBetweenRetries=60, failOnUnstableResult=false` (new inputs; the first two retry failed connections). VERIFY: V8. | [RET] [o:JenkinsQueueJob@1] [n:JenkinsQueueJob@2] |
| 43 | `Maven@1`<br>`ac4ee482-65da-4485-a532-7b085873e532` | `Maven@4` | B | `pin testResultsFiles='**/TEST-*.xml' unless publishJUnitResults = false; pin mavenFeedAuthenticate=true`. The V4 defaults are `**/surefire-reports/TEST-*.xml` and `false`. The SonarQube connection inputs (`sqConnectedServiceName`, `sqProjectName`, `sqProjectKey`, `sqProjectVersion`, `sqDb*`, `sqAnalysisIncludeFullReport`, `sqAnalysisBreakBuildIfQualityGateFailed`) are not in V4 and fall under default deny when present. `review allowBrokenSymbolicLinks=true, restoreOriginalPomXml=false, skipEffectivePom=false, isJacocoCoverageReportXML=false` (new). | [RET] [o:Maven@1] [n:Maven@4] |
| 44 | `Maven@2`<br>`ac4ee482-65da-4485-a532-7b085873e532` | `Maven@4` | B | `pin testResultsFiles='**/TEST-*.xml' unless publishJUnitResults = false; pin mavenFeedAuthenticate=true`. All other V2 inputs exist in V4 with equal defaults. `review skipEffectivePom=false, spotBugsAnalysisEnabled=false, ConnectedServiceName (new, optional)`. | [RET] [o:Maven@2] [n:Maven@4] |
| 45 | `MysqlDeploymentOnMachineGroup@1`<br>`6fec3938-df52-4c01-9f5a-8ed5f66c377e` | none | D | Official: a script-based MySQL deployment or migration using supported MySQL tooling. | [RET] [o:MysqlDeploymentOnMachineGroup@1] |
| 46 | `NuGet@0`<br>`2661b7e5-00f9-4de1-ba41-04e68d70b528` | `NuGetCommand@2` | C | V0 takes `command` and `arguments`; `NuGetCommand@2` would need `command: custom` and the whole command line in `arguments`, and the NuGet.exe version is no longer the V0 one. Not documented as equivalent. | [RET] [o:NuGet@0] [n:NuGetCommand@2] |
| 47 | `NuGetInstaller@0`<br>`333b11bd-d341-40d9-afcf-b32d5ce6f23b` | `NuGetToolInstaller@1` and `NuGetCommand@2` (restore) | C | Two tasks; `restoreMode`, `nuGetVersion` (default `3.3.0`), `nuGetPath`, `nuGetRestoreArgs`, `verbosity` have no input in `NuGetCommand@2`. The GUID is shared with `NuGetRestore@1` and `NuGetCommand@2`. | [RET] [o:NuGetInstaller@0] [n:NuGetToolInstaller@1] [n:NuGetCommand@2] |
| 48 | `NuGetPackager@0`<br>`333b11bd-d341-40d9-afcf-b32d5ce6f24b` | `NuGetCommand@2` (pack) | C | `command: pack` needs the renames `searchPattern` to `searchPatternPack`, `outputdir` to `outputDir` and a `versionByBuild` to `versioningScheme` translation, but `nuGetAdditionalArgs`, `nuGetPath` have no input and the default of `outputDir` becomes `$(Build.ArtifactStagingDirectory)`. Not documented as equivalent. | [RET] [o:NuGetPackager@0] [n:NuGetCommand@2] |
| 49 | `NuGetPublisher@0`<br>`333b11bd-d341-40d9-afcf-b32d5ce6f25b` | `NuGetCommand@2` (push) | C | `command: push` changes the default `nuGetFeedType` from `external` to `internal`, and `nuGetAdditionalArgs`, `nuGetVersion`, `nuGetPath`, `continueOnEmptyNupkgMatch` have no input. Not documented as equivalent. | [RET] [o:NuGetPublisher@0] [n:NuGetCommand@2] |
| 50 | `NuGetRestore@1`<br>`333b11bd-d341-40d9-afcf-b32d5ce6f23b` | `NuGetCommand@2` (restore) | B | `rename feed -> feedRestore; rename verbosity -> verbosityRestore; guard verbosity = -`. `solution`, `selectOrConfig`, `includeNuGetOrg`, `nugetConfigPath`, `noCache`, `packagesDirectory` pass through (aliases of V2: `restoreSolution`, `feedsToUse`, `restoreDirectory`). `command` defaults to `restore` and is not added. The V1 verbosity option `-` has no V2 equivalent. `review disableParallelProcessing=false`. The GUID is unchanged by the rewrite. | [RET] [o:NuGetRestore@1] [n:NuGetCommand@2] |
| 51 | `PackerBuild@0`<br>`845fd4f4-642d-4694-8514-047948a5a556` | `PackerBuild@1` | C | V1 adds `isManagedImage` (default `true`), which requires `managedImageName`, `canDeleteTempFolder` and changes the `additionalBuilderParameters` default from `{}` to `{"vm_size":"Standard_D3_v2"}`; the `task.json` message cites workload identity federation. | [RET] [o:PackerBuild@0] [n:PackerBuild@1] |
| 52 | `PowerShell@1`<br>`e213ff0f-5d5c-4791-802d-52ea3e7be1f1` | `PowerShell@2` | C | Old task.json not public (no history under `Tasks/PowerShell`, the V1 input set is unknown). | [RET] [n:PowerShell@2] |
| 53 | `PowerShellOnTargetMachines@1`<br>`3b5693d4-5777-4fee-862a-bd2b7a374c68` | `PowerShellOnTargetMachines@3` | C | The input sets differ wholesale (`EnvironmentName`, `AdminUserName`, `AdminPassword`, `Protocol`, `ResourceFilteringMethod`, `MachineNames` against `Machines`, `UserName`, `UserPassword`, `CommunicationProtocol`, `ScriptType`, `InlineScript`, ...). | [RET] [o:PowerShellOnTargetMachines@1] [n:PowerShellOnTargetMachines@3] |
| 54 | `PublishSymbols@1`<br>`0675668a-7bba-4ccb-901d-5ad6554ca653` | `PublishSymbols@2` | B | `require SymbolsPath; require SymbolsFolder; invert SkipIndexing -> IndexSources; add SymbolServerType=FileShare`. V1 only published to a file share (`SymbolsPath`); V2 requires `SymbolServerType` (`TeamServices` or `FileShare`, no default). The V1 default of `SymbolsFolder` is empty and the V2 default is `$(Build.SourcesDirectory)`, so an unset `SymbolsFolder` is needs-manual. `SearchPattern`, `TreatNotIndexedAsWarning`, `SymbolsMaximumWaitTime`, `SymbolsProduct`, `SymbolsVersion`, `SymbolsArtifactName` pass through. VERIFY: V9. | [RET] [o:PublishSymbols@1] [n:PublishSymbols@2] |
| 55 | `PublishToAzureServiceBus@0`<br>`ba761f24-cbd6-48cb-92f3-fc13396405b1` | `PublishToAzureServiceBus@2` | C | V2 requires `serviceBusQueueName` and `serviceBusNamespace` and an Azure Resource Manager connection (V0 used an `AzureServiceBus` connection); `signPayload`/`certificateString` and `useDataContractSerializer` are new required inputs. | [RET] [o:PublishToAzureServiceBus@0] [n:PublishToAzureServiceBus@2] |
| 56 | `PyPIPublisher@0`<br>`2d8a1d60-8ccd-11e7-a792-11ac56e9f553` | `TwineAuthenticate@1` followed by `twine upload` in a script step | C | Two steps and a script; the `task.json` message says PyPI packages are now published with Twine authentication and custom scripts. | [RET] [o:PyPIPublisher@0] [n:TwineAuthenticate@1] |
| 57 | `QuickPerfTest@1`<br>`9648625c-1523-4eb5-b015-dfe7c685840c` | `AzureLoadTest@1` | C | A different service: `AzureLoadTest@1` is the task of the Azure Load Testing Marketplace extension (inputs `azureSubscription`, `loadTestConfigFile`, `loadTestResource`, `resourceGroup`); it is not in the pinned task catalog. | [RET] [o:QuickPerfTest@1] [n:AzureLoadTest@1] |
| 58 | `RunVisualStudioTestsusingTestAgent@1`<br>GUID unknown (VERIFY: V18) | `VSTest@3` | C | Old task.json not public (no history under `Tasks/RunVisualStudioTestsusingTestAgent`). A test-agent runner replaced by `VSTest@3`; no mapping documented. | [RET] [n:VSTest@3] |
| 59 | `ServiceFabricComposeDeploy@0`<br>`19c02b15-d377-40e0-9efa-3168506e0933` | `ServiceFabricDeploy@1` or a currently supported container deployment workflow | C | Compose deployment against application-package deployment; [RET] also allows another workflow, so there is no single mechanical target. | [RET] [o:ServiceFabricComposeDeploy@0] [n:ServiceFabricDeploy@1] |
| 60 | `ServiceFabricUpdateAppVersions@1`<br>`97ef6e59-b8cc-48aa-9937-1a01e35e7584` | `ServiceFabricUpdateManifests@2` | A | The eight V1 inputs exist in V2 with the same names, types and defaults (only visibility rules gain `updateType = Manifest versions`). The new required `updateType` defaults to `Manifest versions`, the only thing V1 did. The GUID is unchanged. | [RET] [o:ServiceFabricUpdateAppVersions@1] [n:ServiceFabricUpdateManifests@2] |
| 61 | `SonarQubePostTest@1`<br>`730d8de1-7a4f-424c-9542-fe7cc02604eb` | none | D | Official: the supported SonarQube extension from Visual Studio Marketplace. | [RET] [o:SonarQubePostTest@1] |
| 62 | `SonarQubePreBuild@1`<br>`eae5b2cc-ac5e-4cba-b022-a06621f9c01f` | none | D | As row 61. | [RET] [o:SonarQubePreBuild@1] |
| 63 | `VSMobileCenterTest@0`<br>`ad5cd22a-be4e-48bb-adce-181a32432da5` | none | D | Official: BrowserStack App Automate or another supported device-testing service. | [RET] [o:VSMobileCenterTest@0] |
| 64 | `XamarinAndroid@1`<br>`27edd013-36fd-43aa-96a3-7d73e1e35285` | `DotNetCoreCLI@2` (after project migration) | D | Xamarin support ended on 2024-05-01. Official: migrate to an SDK-style .NET for Android or .NET MAUI project and build with `DotNetCoreCLI@2`; the project must be migrated first, so the task cannot be swapped. | [RET] [o:XamarinAndroid@1] [n:DotNetCoreCLI@2] |
| 65 | `XamarinComponentRestore@0`<br>`ff50fc97-da8c-4683-b014-34c15315ee5f` | `DotNetCoreCLI@2` (after project migration) | D | Official: migrate to an SDK-style .NET or .NET MAUI project and restore with `DotNetCoreCLI@2`. | [RET] [o:XamarinComponentRestore@0] [n:DotNetCoreCLI@2] |
| 66 | `XamariniOS@1`<br>`0f077e3a-af59-496d-81bc-ad971b7464e0` | `DotNetCoreCLI@2` (after project migration) | D | Official: migrate to an SDK-style .NET for iOS or .NET MAUI project and build with `DotNetCoreCLI@2`. | [RET] [o:XamariniOS@1] [n:DotNetCoreCLI@2] |
| 67 | `XamariniOS@2`<br>`0f077e3a-af59-496d-81bc-ad971b7464e0` | `DotNetCoreCLI@2` (after project migration) | D | As row 66. | [RET] [o:XamariniOS@2] [n:DotNetCoreCLI@2] |
| 68 | `XamarinLicense@1`<br>`6237827d-6244-4d52-b93e-47d8610fbd8a` | none | D | Official: no replacement task is required; migrate the application from Xamarin to .NET or .NET MAUI. | [RET] [o:XamarinLicense@1] |
| 69 | `XamarinTestCloud@1`<br>`049918cb-1488-48eb-85e8-c318eccaaa74` | none | D | Official: BrowserStack App Automate or another supported device-testing service (the `task.json` message names `AppCenterDistribute@3`, which [RET] does not recommend). | [RET] [o:XamarinTestCloud@1] |
| 70 | `Xcode@2`<br>`1e78dc1b-9132-4b18-9c75-0e7ecc634b74` | `Xcode@5` | C | The signing inputs (`xcode8AutomaticSigning`, `signMethod`, `iosSigningIdentity`, `unlockDefaultKeychain`, `defaultKeychainPassword`, `provProfileUuid`, `p12`, `p12pwd`, `provProfile`, `removeProfile`), `packageTool` and `outputPattern` are gone; the default of `packageApp` changes from `true` to `false`, of `xcWorkspacePath` and `useXcpretty` as well, and the default signing behaviour of V2 (signing from `signMethod: file`) is not that of V5 (`signingOption: nosign`). | [RET] [o:Xcode@2] [n:Xcode@5] |
| 71 | `Xcode@3`<br>`1e78dc1b-9132-4b18-9c75-0e7ecc634b74` | `Xcode@5` | C | As row 70 without `packageTool`. | [RET] [o:Xcode@3] [n:Xcode@5] |
| 72 | `Xcode@4`<br>`1e78dc1b-9132-4b18-9c75-0e7ecc634b74` | `Xcode@5` | B | `pin useXcpretty=false; pin destinationSimulators='iPhone 7' if destinationPlatformOption != default unless destinationTypeOption = devices`. V4 defaulted `useXcpretty` to `false` (V5: `true`) and `destinationSimulators` to `iPhone 7` (V5: no default). `outputPattern` is not in V5 and falls under default deny. `xcodeVersion` keeps its option keys (`default`, `8`, `9`, `specifyPath`; V5 adds more). VERIFY: V10. | [RET] [o:Xcode@4] [n:Xcode@5] |
| 73 | `XcodePackageiOS@0`<br>`f54d001c-999f-408a-9867-0400c1838c5e` | `Xcode@5` | C | A packaging-only task (`appName`, `ipaName`, `provisioningProfile`, `appPath`, `ipaPath`) against a build task whose packaging is `packageApp`/`exportOptions`; no input maps one to one. | [RET] [o:XcodePackageiOS@0] [n:Xcode@5] |

## 9. Appendix A: old input sets not in the catalog

Names are followed by their aliases in parentheses; these lists are the "old set" of section 5.1 for the rows that need them. Each list was read from the cited old `task.json`.

| Row | Old inputs |
|---|---|
| 7 `ArchiveFiles@1` | `rootFolder`, `includeRootFolder`, `archiveType`, `tarCompression`, `archiveFile`, `replaceExistingArchive` |
| 12 `AzureFunction@0` | `function`, `key`, `method`, `headers`, `queryParameters`, `body`, `waitForCompletion`, `successCriteria` |
| 24 `CopyFiles@1` | `SourceFolder`, `Contents`, `TargetFolder`, `CleanTargetFolder`, `OverWrite`, `flattenFolders` |
| 26 `cURLUploader@1` | `files`, `username`, `password`, `url`, `options`, `redirectStderr` |
| 28, 29 `DotNetCoreCLI@0/@1` | `command`, `publishWebProjects`, `projects`, `arguments`, `zipAfterPublish` |
| 37 `Gradle@1` | `wrapperScript` (`gradleWrapperFile`), `options`, `tasks`, `cwd` (`workingDirectory`), `publishJUnitResults`, `testResultsFiles`, `testRunTitle`, `codeCoverageTool` (`codeCoverageToolOption`), `classFilesDirectories` (`codeCoverageClassFilesDirectories`), `classFilter` (`codeCoverageClassFilter`), `failIfCoverageEmpty` (`codeCoverageFailIfEmpty`), `javaHomeSelection` (`javaHomeOption`), `jdkVersion` (`jdkVersionOption`), `jdkUserInputPath` (`jdkDirectory`), `jdkArchitecture` (`jdkArchitectureOption`), `gradleOpts` (`gradleOptions`), `sqAnalysisEnabled` (`sonarQubeRunAnalysis`), `sqConnectedServiceName` (`sonarQubeServiceEndpoint`), `sqProjectName` (`sonarQubeProjectName`), `sqProjectKey` (`sonarQubeProjectKey`), `sqProjectVersion` (`sonarQubeProjectVersion`), `sqGradlePluginVersion` (`sonarQubeGradlePluginVersion`), `sqDbDetailsRequired` (`sonarQubeSpecifyDB`), `sqDbUrl` (`sonarQubeDBUrl`), `sqDbUsername` (`sonarQubeDBUsername`), `sqDbPassword` (`sonarQubeDBPassword`), `sqAnalysisIncludeFullReport` (`sonarQubeIncludeFullReport`), `sqAnalysisBreakBuildIfQualityGateFailed` (`sonarQubeFailWhenQualityGateFails`), `checkstyleAnalysisEnabled` (`checkStyleRunAnalysis`), `findbugsAnalysisEnabled` (`findBugsRunAnalysis`), `pmdAnalysisEnabled` (`pmdRunAnalysis`) |
| 38, 39 `InstallAppleCertificate@0/@1` | `certSecureFile`, `certPwd`, `keychain`, `keychainPassword`, `customKeychainPath`, `deleteCert`, `deleteCustomKeychain`, `signingIdentity` |
| 40 `InstallAppleProvisioningProfile@0` | `provProfileSecureFile`, `removeProfile` |
| 41 `InvokeRESTAPI@0` | `connectedServiceName` (`serviceConnection`), `method`, `headers`, `body`, `urlSuffix`, `waitForCompletion`, `successCriteria` |
| 42 `JenkinsQueueJob@1` | `serverEndpoint`, `jobName`, `isMultibranchJob`, `multibranchPipelineBranch`, `captureConsole`, `capturePipeline`, `parameterizedJob`, `jobParameters` |
| 43 `Maven@1` | `mavenPOMFile` (`mavenPomFile`), `goals`, `options`, `publishJUnitResults`, `testResultsFiles`, `testRunTitle`, `codeCoverageTool` (`codeCoverageToolOption`), `classFilter` (`codeCoverageClassFilter`), `classFilesDirectories` (`codeCoverageClassFilesDirectories`), `srcDirectories` (`codeCoverageSourceDirectories`), `failIfCoverageEmpty` (`codeCoverageFailIfEmpty`), `javaHomeSelection` (`javaHomeOption`), `jdkVersion` (`jdkVersionOption`), `jdkUserInputPath` (`jdkDirectory`), `jdkArchitecture` (`jdkArchitectureOption`), `mavenVersionSelection` (`mavenVersionOption`), `mavenPath` (`mavenDirectory`), `mavenSetM2Home`, `mavenOpts` (`mavenOptions`), `mavenFeedAuthenticate` (`mavenAuthenticateFeed`), `sqAnalysisEnabled` (`sonarQubeRunAnalysis`), `sqConnectedServiceName` (`sonarQubeServiceEndpoint`), `sqProjectName` (`sonarQubeProjectName`), `sqProjectKey` (`sonarQubeProjectKey`), `sqProjectVersion` (`sonarQubeProjectVersion`), `sqDbDetailsRequired` (`sonarQubeSpecifyDB`), `sqDbUrl` (`sonarQubeDBUrl`), `sqDbUsername` (`sonarQubeDBUsername`), `sqDbPassword` (`sonarQubeDBPassword`), `sqAnalysisIncludeFullReport` (`sonarQubeIncludeFullReport`), `sqAnalysisBreakBuildIfQualityGateFailed` (`sonarQubeFailWhenQualityGateFails`), `checkstyleAnalysisEnabled` (`checkStyleRunAnalysis`), `pmdAnalysisEnabled` (`pmdRunAnalysis`), `findbugsAnalysisEnabled` (`findBugsRunAnalysis`) |
| 54 `PublishSymbols@1` | `SymbolsPath`, `SearchPattern`, `SymbolsFolder`, `SkipIndexing`, `TreatNotIndexedAsWarning`, `SymbolsMaximumWaitTime`, `SymbolsProduct`, `SymbolsVersion`, `SymbolsArtifactName` |
| 60 `ServiceFabricUpdateAppVersions@1` | `applicationPackagePath`, `versionSuffix`, `versionBehavior`, `updateOnlyChanged`, `pkgArtifactName`, `logAllChanges`, `compareType`, `buildNumber` |
| 72 `Xcode@4` | `actions`, `configuration`, `sdk`, `xcWorkspacePath`, `scheme`, `xcodeVersion`, `xcodeDeveloperDir`, `packageApp`, `archivePath`, `exportPath`, `exportOptions`, `exportMethod`, `exportTeamId`, `exportOptionsPlist`, `exportArgs`, `signingOption`, `signingIdentity`, `provisioningProfileUuid`, `teamId`, `destinationPlatformOption`, `destinationPlatform`, `destinationTypeOption`, `destinationSimulators`, `destinationDevices`, `args`, `cwd` (`workingDirectory`), `outputPattern`, `useXcpretty`, `publishJUnitResults` |

## 10. Test vectors

Cells contain file text: `\n` is a line feed, `\r` a carriage return, `\|` a literal pipe. `Expected` is either `unchanged` or the complete output text, followed by `diags:` with the ordered list `CODE@line` (the line of the anchor in the **input**, section 7); `diags: []` means none. Parser diagnostics are written with their own prefix. Every vector runs on a file that is a valid pipeline or template (a root `steps:` shape).

| ID | Input | Expected | Source |
|---|---|---|---|
| MODV-001 | `steps:\n- task: CopyFiles@1\n  inputs:\n    SourceFolder: src\n    Contents: '**'\n    TargetFolder: $(Build.ArtifactStagingDirectory)` | `steps:\n- task: CopyFiles@2\n  inputs:\n    SourceFolder: src\n    Contents: '**'\n    TargetFolder: $(Build.ArtifactStagingDirectory)`<br>diags: [TRL-MOD-0001@2] | row 24 [o:CopyFiles@1] [n:CopyFiles@2] |
| MODV-002 | `steps:\n- task: copyfiles@1\n  inputs:\n    Contents: '**'\n    TargetFolder: out` | `steps:\n- task: CopyFiles@2\n  inputs:\n    Contents: '**'\n    TargetFolder: out`<br>diags: [TRL-MOD-0001@2] | section 3.2.1, row 24 |
| MODV-003 | `# pipeline header\nsteps:\n- task: CopyFiles@1 # legacy copy\n  # keep this comment\n  inputs:\n    Contents: '**' # everything\n\n# trailing note\n- script: echo done` | `# pipeline header\nsteps:\n- task: CopyFiles@2 # legacy copy\n  # keep this comment\n  inputs:\n    Contents: '**' # everything\n\n# trailing note\n- script: echo done`<br>diags: [TRL-MOD-0001@3] | S2, E1 |
| MODV-004 | `steps:\n- task: 'CopyFiles@1'\n- task: "CopyFiles@1"` | `steps:\n- task: 'CopyFiles@2'\n- task: "CopyFiles@2"`<br>diags: [TRL-MOD-0001@2, TRL-MOD-0001@3] | E1 |
| MODV-005 | `steps:\n- task: 5BFB729A-A7C8-4A78-A7C3-8D717BB7C13C@1\n  inputs:\n    Contents: '**'` | `steps:\n- task: 5BFB729A-A7C8-4A78-A7C3-8D717BB7C13C@2\n  inputs:\n    Contents: '**'`<br>diags: [TRL-MOD-0001@2] | section 3.2.3, row 24 (same GUID in [n:CopyFiles@2]) |
| MODV-006 | `steps:\n- task: InstallAppleProvisioningProfile@0\n  inputs:\n    provProfileSecureFile: app.mobileprovision\n    removeProfile: true` | `steps:\n- task: InstallAppleProvisioningProfile@1\n  inputs:\n    provProfileSecureFile: app.mobileprovision\n    removeProfile: true`<br>diags: [TRL-MOD-0001@2] | row 40 [o:InstallAppleProvisioningProfile@0] [n:InstallAppleProvisioningProfile@1] |
| MODV-007 | `steps:\n- task: ServiceFabricUpdateAppVersions@1\n  inputs:\n    applicationPackagePath: pkg\n    versionSuffix: .$(Build.BuildNumber)` | `steps:\n- task: ServiceFabricUpdateManifests@2\n  inputs:\n    applicationPackagePath: pkg\n    versionSuffix: .$(Build.BuildNumber)`<br>diags: [TRL-MOD-0001@2] | row 60 [o:ServiceFabricUpdateAppVersions@1] [n:ServiceFabricUpdateManifests@2] |
| MODV-008 | `steps:\n- task: CopyFiles@1.1.4\n  inputs:\n    Contents: '**'` | `steps:\n- task: CopyFiles@2\n  inputs:\n    Contents: '**'`<br>diags: [TRL-MOD-0001@2, TRL-MOD-0011@2] | section 3.2.2 (VERIFY: V15) |
| MODV-009 | `jobs:\n- deployment: d\n  environment: e\n  strategy:\n    runOnce:\n      deploy:\n        steps:\n        - task: CopyFiles@1\n          displayName: Copy\n          enabled: false\n          condition: succeededOrFailed()\n          inputs:\n            Contents: '**'` | `jobs:\n- deployment: d\n  environment: e\n  strategy:\n    runOnce:\n      deploy:\n        steps:\n        - task: CopyFiles@2\n          displayName: Copy\n          enabled: false\n          condition: succeededOrFailed()\n          inputs:\n            Contents: '**'`<br>diags: [TRL-MOD-0001@8] | section 3.1 (hooks, disabled steps) |
| MODV-010 | `parameters:\n- name: copy\n  type: boolean\n  default: true\nsteps:\n- ${{ if eq(parameters.copy, true) }}:\n  - task: CopyFiles@1\n    inputs:\n      Contents: '**'` | `parameters:\n- name: copy\n  type: boolean\n  default: true\nsteps:\n- ${{ if eq(parameters.copy, true) }}:\n  - task: CopyFiles@2\n    inputs:\n      Contents: '**'`<br>diags: [TRL-MOD-0001@7] | section 3.1 (directive bodies) |
| MODV-011 | `parameters:\n- name: contents\n  type: string\n  default: '**'\nsteps:\n- task: CopyFiles@1\n  inputs:\n    Contents: ${{ parameters.contents }}` | `parameters:\n- name: contents\n  type: string\n  default: '**'\nsteps:\n- task: CopyFiles@2\n  inputs:\n    Contents: ${{ parameters.contents }}`<br>diags: [TRL-MOD-0001@6] | section 1.1 (template rewritten in its own file), S5 (pass-through value) |
| MODV-012 | `steps:\n- {task: CopyFiles@1, inputs: {Contents: '**'}}` | `steps:\n- {task: CopyFiles@2, inputs: {Contents: '**'}}`<br>diags: [TRL-MOD-0001@2] | section 5.4.3 (class A is rewritten in any layout) |
| MODV-013 | `steps:\r\n- task: CopyFiles@1\r\n  inputs:\r\n    Contents: '**'\r\n` | `steps:\r\n- task: CopyFiles@2\r\n  inputs:\r\n    Contents: '**'\r\n`<br>diags: [TRL-MOD-0001@2] | S2, E7 |
| MODV-014 | `steps:\n- task: CopyFiles@1\n- script: echo between\n- task: CopyFiles@1` | `steps:\n- task: CopyFiles@2\n- script: echo between\n- task: CopyFiles@2`<br>diags: [TRL-MOD-0001@2, TRL-MOD-0001@4] | section 3.3, S7 |
| MODV-015 | `steps:\n- task: CopyFiles@2\n  inputs:\n    Contents: '**'` | unchanged<br>diags: [] | S3 (already modern) |
| MODV-016 | `steps:\n- task: Maven@3\n  inputs:\n    goals: package` | unchanged<br>diags: [] | section 1.2 (`Maven@3` is not in the retirement list) |
| MODV-017 | `steps:\n- task: Maven@4\n  inputs:\n    goals: package\n    testResultsFiles: '**/TEST-*.xml'\n    mavenFeedAuthenticate: true` | unchanged<br>diags: [] | S3: second run over the output of MODV-036 |
| MODV-018 | `parameters:\n- name: flat\n  type: boolean\n  default: false\nsteps:\n- task: CopyFiles@1\n  inputs:\n    Contents: '**'\n    ${{ if parameters.flat }}:\n      flattenFolders: true` | `parameters:\n- name: flat\n  type: boolean\n  default: false\nsteps:\n- task: CopyFiles@2\n  inputs:\n    Contents: '**'\n    ${{ if parameters.flat }}:\n      flattenFolders: true`<br>diags: [TRL-MOD-0001@6] | section 5.4.3 (class A with a directive in `inputs`) |
| MODV-019 | `parameters:\n- name: taskName\n  type: string\n  default: CopyFiles@2\nsteps:\n- task: ${{ parameters.taskName }}\n  inputs:\n    Contents: '**'` | unchanged<br>diags: [TRL-MOD-0005@6] | section 3.2.5 |
| MODV-020 | `parameters:\n- name: taskName\n  type: string\n  default: CopyFiles@1\nsteps:\n- task: ${{ parameters.taskName }}` | unchanged<br>diags: [TRL-MOD-0005@4, TRL-MOD-0005@6] | section 3.2.6 (a string that names a retired task is not edited) |
| MODV-021 | `parameters:\n- name: s\n  type: step\n  default:\n    task: CopyFiles@1\n    inputs:\n      Contents: '**'\nsteps:\n- ${{ parameters.s }}` | `parameters:\n- name: s\n  type: step\n  default:\n    task: CopyFiles@2\n    inputs:\n      Contents: '**'\nsteps:\n- ${{ parameters.s }}`<br>diags: [TRL-MOD-0001@5] | section 3.1 (step-typed default is a step) |
| MODV-022 | `steps:\n- template: wrap.yml\n  parameters:\n    extra:\n    - task: CopyFiles@1\n      inputs:\n        Contents: '**'` | `steps:\n- template: wrap.yml\n  parameters:\n    extra:\n    - task: CopyFiles@2\n      inputs:\n        Contents: '**'`<br>diags: [TRL-MOD-0001@5] | section 3.1 (step list passed to a template) |
| MODV-023 | `steps:\n- task: acme.tools.CopyFiles@1` | unchanged<br>diags: [TRL-MOD-0010@2] | section 3.2.4 (VERIFY: V14) |
| MODV-024 | `parameters:\n- name: root\n  type: string\n  default: out\nsteps:\n- task: ArchiveFiles@1\n  inputs:\n    rootFolder: ${{ parameters.root }}\n    archiveType: default\n    archiveFile: $(Build.ArtifactStagingDirectory)/app.zip` | `parameters:\n- name: root\n  type: string\n  default: out\nsteps:\n- task: ArchiveFiles@2\n  inputs:\n    rootFolderOrFile: ${{ parameters.root }}\n    archiveType: zip\n    archiveFile: $(Build.ArtifactStagingDirectory)/app.zip`<br>diags: [TRL-MOD-0002@6] | row 7 [o:ArchiveFiles@1] [n:ArchiveFiles@2] |
| MODV-025 | `steps:\n- task: ArchiveFiles@1\n  inputs:\n    rootFolder: out\n    archiveType: $(kind)` | unchanged<br>diags: [TRL-MOD-0006@5] | S5 (`map` needs a literal) |
| MODV-026 | `steps:\n- task: DotNetCoreInstaller@1\n  inputs:\n    packageType: sdk` | `steps:\n- task: UseDotNet@2\n  inputs:\n    packageType: sdk\n    version: '2.2.x'`<br>diags: [TRL-MOD-0002@2] | row 31 [o:DotNetCoreInstaller@1] [n:UseDotNet@2] |
| MODV-027 | `steps:\n- task: DotNetCoreInstaller@0\n  inputs:\n    version: 2.1.401` | `steps:\n- task: UseDotNet@2\n  inputs:\n    version: 2.1.401`<br>diags: [TRL-MOD-0002@2] | row 30 |
| MODV-028 | `steps:\n- task: DotNetCoreInstaller@1\n  displayName: SDK` | `steps:\n- task: UseDotNet@2\n  displayName: SDK\n  inputs:\n    version: '2.2.x'`<br>diags: [TRL-MOD-0002@2] | E5 (create `inputs`), row 31 |
| MODV-029 | `steps:\n- task: cURLUploader@1\n  inputs:\n    files: out/*.zip\n    username: deploy\n    password: $(ftpPassword)\n    url: ftp://host.example/dir/` | `steps:\n- task: cURLUploader@2\n  inputs:\n    files: out/*.zip\n    username: deploy\n    password: $(ftpPassword)\n    url: ftp://host.example/dir/\n    authType: UserAndPass\n    remotePath: ''`<br>diags: [TRL-MOD-0002@2] | row 26 [o:cURLUploader@1] [n:cURLUploader@2] |
| MODV-030 | `steps:\n- task: GitHubRelease@0\n  inputs:\n    gitHubConnection: gh\n    repositoryName: o/r\n    tagSource: manual\n    tag: v1.0.0\n    releaseNotesSource: input\n    releaseNotes: First release` | `steps:\n- task: GitHubRelease@1\n  inputs:\n    gitHubConnection: gh\n    repositoryName: o/r\n    tagSource: userSpecifiedTag\n    tag: v1.0.0\n    releaseNotesSource: inline\n    releaseNotesInline: First release`<br>diags: [TRL-MOD-0002@2] | row 36 [o:GitHubRelease@0] [n:GitHubRelease@1] |
| MODV-031 | `steps:\n- task: GitHubRelease@0\n  inputs:\n    gitHubConnection: gh\n    tagSource: $(src)` | unchanged<br>diags: [TRL-MOD-0006@5] | S5, row 36 |
| MODV-032 | `steps:\n- task: FtpUpload@1\n  inputs:\n    credentialsOption: serviceEndpoint\n    serverEndpoint: ftp\n    rootDirectory: out\n    overwrite: true\n    remoteDirectory: /site/` | `steps:\n- task: FtpUpload@2\n  inputs:\n    credentialsOption: serviceEndpoint\n    serverEndpoint: ftp\n    rootDirectory: out\n    remoteDirectory: /site/`<br>diags: [TRL-MOD-0002@2] | row 35, E6 (aliases `credentialsOption`, `rootDirectory`, `remoteDirectory` pass through) |
| MODV-033 | `steps:\n- task: FtpUpload@1\n  inputs:\n    serverEndpoint: ftp\n    rootFolder: out\n    overwrite: false` | unchanged<br>diags: [TRL-MOD-0006@6] | row 35 (`guard overwrite != true`) |
| MODV-034 | `steps:\n- task: Gradle@1\n  inputs:\n    tasks: build\n    codeCoverageTool: None` | `steps:\n- task: Gradle@4\n  inputs:\n    tasks: build\n    testResultsFiles: '**/build/test-results/TEST-*.xml'`<br>diags: [TRL-MOD-0002@2] | row 37 [o:Gradle@1] [n:Gradle@4] |
| MODV-035 | `steps:\n- task: Gradle@1\n  inputs:\n    tasks: build\n    codeCoverageTool: JaCoCo` | unchanged<br>diags: [TRL-MOD-0006@5] | S4, row 37 |
| MODV-036 | `steps:\n- task: Maven@2\n  inputs:\n    goals: package` | `steps:\n- task: Maven@4\n  inputs:\n    goals: package\n    testResultsFiles: '**/TEST-*.xml'\n    mavenFeedAuthenticate: true`<br>diags: [TRL-MOD-0002@2] | row 44 [o:Maven@2] [n:Maven@4] |
| MODV-037 | `steps:\n- task: Maven@2\n  inputs:\n    mavenPOMFile: pom.xml\n    mavenAuthenticateFeed: false` | `steps:\n- task: Maven@4\n  inputs:\n    mavenPOMFile: pom.xml\n    mavenAuthenticateFeed: false\n    testResultsFiles: '**/TEST-*.xml'`<br>diags: [TRL-MOD-0002@2] | section 5.1.1 (`mavenAuthenticateFeed` is an alias of `mavenFeedAuthenticate`, so no pin) |
| MODV-038 | `steps:\n- task: Maven@2\n  inputs:\n    goals: package\n    publishJUnitResults: false` | `steps:\n- task: Maven@4\n  inputs:\n    goals: package\n    publishJUnitResults: false\n    mavenFeedAuthenticate: true`<br>diags: [TRL-MOD-0002@2] | row 44 (`unless publishJUnitResults = false`) |
| MODV-039 | `steps:\n- task: Maven@1\n  inputs:\n    sqAnalysisEnabled: true\n    sqConnectedServiceName: sonar\n    sqProjectKey: key` | unchanged<br>diags: [TRL-MOD-0006@5, TRL-MOD-0006@6] | S4, row 43 (inputs absent from [n:Maven@4]) |
| MODV-040 | `steps:\n- task: Xcode@4\n  inputs:\n    actions: build\n    scheme: App` | `steps:\n- task: Xcode@5\n  inputs:\n    actions: build\n    scheme: App\n    useXcpretty: false`<br>diags: [TRL-MOD-0002@2] | row 72 [o:Xcode@4] [n:Xcode@5] |
| MODV-041 | `steps:\n- task: Xcode@4\n  inputs:\n    actions: build\n    outputPattern: out` | unchanged<br>diags: [TRL-MOD-0006@5] | S4, row 72 |
| MODV-042 | `steps:\n- task: InvokeRESTAPI@0\n  inputs:\n    serviceConnection: svc\n    method: POST` | `steps:\n- task: InvokeRESTAPI@1\n  inputs:\n    serviceConnection: svc\n    method: POST\n    headers: \|-\n      {\n      "Content-Type":"application/json"\n      }`<br>diags: [TRL-MOD-0002@2] | row 41, constant K1 [o:InvokeRESTAPI@0] [n:InvokeRESTAPI@1] |
| MODV-043 | `steps:\n- task: CacheBeta@0\n  inputs:\n    key: npm \| package-lock.json\n    path: node_modules` | `steps:\n- task: Cache@2\n  inputs:\n    key: npm \| package-lock.json\n    path: node_modules`<br>diags: [TRL-MOD-0002@2] | row 19 [o:CacheBeta@0] [n:Cache@2] |
| MODV-044 | `steps:\n- task: CacheBeta@0\n  inputs:\n    key: \|\n      one\n      two\n    path: node_modules` | unchanged<br>diags: [TRL-MOD-0006@4] | row 19 (`guard multiline(key)`) |
| MODV-045 | `steps:\n- task: DownloadPipelineArtifact@0\n  inputs:\n    targetPath: $(Pipeline.Workspace)` | `steps:\n- task: DownloadPipelineArtifact@2\n  inputs:\n    targetPath: $(Pipeline.Workspace)\n    artifact: drop`<br>diags: [TRL-MOD-0002@2] | row 33 [o:DownloadPipelineArtifact@0] [n:DownloadPipelineArtifact@2] |
| MODV-046 | `steps:\n- task: DownloadPipelineArtifact@0\n  inputs:\n    pipelineId: 123\n    artifactName: drop\n    targetPath: out` | unchanged<br>diags: [TRL-MOD-0006@4] | row 33 (`guard pipelineId != ''`) |
| MODV-047 | `steps:\n- task: InstallAppleCertificate@1\n  inputs:\n    certSecureFile: c.p12\n    certPwd: $(certPassword)` | `steps:\n- task: InstallAppleCertificate@2\n  inputs:\n    certSecureFile: c.p12\n    certPwd: $(certPassword)`<br>diags: [TRL-MOD-0002@2] | row 39 [o:InstallAppleCertificate@1] [n:InstallAppleCertificate@2] |
| MODV-048 | `steps:\n- task: InstallAppleCertificate@1\n  inputs:\n    certSecureFile: c.p12\n    keychain: default` | unchanged<br>diags: [TRL-MOD-0006@5] | row 39 (`require keychainPassword if keychain != temp`) |
| MODV-049 | `steps:\n- task: NuGetRestore@1\n  inputs:\n    solution: '**/*.sln'\n    feed: myfeed\n    verbosity: Quiet` | `steps:\n- task: NuGetCommand@2\n  inputs:\n    solution: '**/*.sln'\n    feedRestore: myfeed\n    verbosityRestore: Quiet`<br>diags: [TRL-MOD-0002@2] | row 50 [o:NuGetRestore@1] [n:NuGetCommand@2] |
| MODV-050 | `steps:\n- task: NuGetRestore@1\n  inputs:\n    solution: '**/*.sln'\n    verbosity: '-'` | unchanged<br>diags: [TRL-MOD-0006@5] | row 50 (`guard verbosity = -`) |
| MODV-051 | `steps:\n- task: 333b11bd-d341-40d9-afcf-b32d5ce6f23b@1\n  inputs:\n    solution: '**/*.sln'` | `steps:\n- task: 333b11bd-d341-40d9-afcf-b32d5ce6f23b@2\n  inputs:\n    solution: '**/*.sln'`<br>diags: [TRL-MOD-0002@2] | section 3.2.3: (GUID, major 1) is `NuGetRestore@1`, and `NuGetCommand@2` has the same GUID |
| MODV-052 | `steps:\n- task: PublishSymbols@1\n  inputs:\n    SymbolsPath: /mnt/symbols\n    SearchPattern: '**/bin/**/*.pdb'\n    SymbolsFolder: $(Build.SourcesDirectory)\n    SkipIndexing: true` | `steps:\n- task: PublishSymbols@2\n  inputs:\n    SymbolsPath: /mnt/symbols\n    SearchPattern: '**/bin/**/*.pdb'\n    SymbolsFolder: $(Build.SourcesDirectory)\n    IndexSources: false\n    SymbolServerType: FileShare`<br>diags: [TRL-MOD-0002@2] | row 54 [o:PublishSymbols@1] [n:PublishSymbols@2] |
| MODV-053 | `steps:\n- task: PublishSymbols@1\n  inputs:\n    SearchPattern: '**/*.pdb'\n    SymbolsFolder: src` | unchanged<br>diags: [TRL-MOD-0006@2] | row 54 (`require SymbolsPath`, anchored at the task value) |
| MODV-054 | `steps:\n- task: JenkinsQueueJob@1\n  inputs:\n    serverEndpoint: jenkins\n    jobName: build\n    bogusInput: 1` | `steps:\n- task: JenkinsQueueJob@2\n  inputs:\n    serverEndpoint: jenkins\n    jobName: build\n    bogusInput: 1`<br>diags: [TRL-MOD-0002@2, TRL-MOD-0007@6] | section 5.4.2, row 42 |
| MODV-055 | `steps:\n- task: DotNetCoreCLI@1\n  inputs:\n    command: test\n    projects: '**/*Tests.csproj'` | `steps:\n- task: DotNetCoreCLI@2\n  inputs:\n    command: test\n    projects: '**/*Tests.csproj'\n    publishTestResults: false`<br>diags: [TRL-MOD-0002@2] | row 29 [o:DotNetCoreCLI@1] [n:DotNetCoreCLI@2] (VERIFY: V3, V4) |
| MODV-056 | `steps:\n- task: DotNetCoreCLI@1\n  inputs:\n    command: $(dotnetCommand)` | unchanged<br>diags: [TRL-MOD-0006@4] | row 29 (`literal command`) |
| MODV-057 | `steps:\n- task: Maven@2\n  inputs:\n    TESTRESULTSFILES: out/TEST-*.xml\n    mavenFeedAuthenticate: true` | `steps:\n- task: Maven@4\n  inputs:\n    TESTRESULTSFILES: out/TEST-*.xml\n    mavenFeedAuthenticate: true`<br>diags: [TRL-MOD-0002@2] | section 5.1.1 (input keys are matched case-insensitively) |
| MODV-058 | `steps:\n- task: Xcode@4\n  inputs:\n    actions: build\n    ${{ if eq(parameters.release, true) }}:\n      configuration: Release` | unchanged<br>diags: [TRL-MOD-0009@2] | section 5.4.3 |
| MODV-059 | `steps:\n- {task: Xcode@4, inputs: {actions: build}}` | unchanged<br>diags: [TRL-MOD-0009@2] | section 5.4.3 |
| MODV-060 | `steps:\n- task: CmdLine@1\n  inputs:\n    filename: make\n    arguments: all` | unchanged<br>diags: [TRL-MOD-0003@2] | row 22 [o:CmdLine@1] [n:CmdLine@2] |
| MODV-061 | `steps:\n- task: NuGetInstaller@0\n  inputs:\n    solution: '**/*.sln'` | unchanged<br>diags: [TRL-MOD-0003@2] | row 47 (two replacement tasks) |
| MODV-062 | `steps:\n- task: 333b11bd-d341-40d9-afcf-b32d5ce6f23b@0` | unchanged<br>diags: [TRL-MOD-0003@2] | section 3.2.3: (GUID, major 0) is `NuGetInstaller@0` |
| MODV-063 | `steps:\n- task: CopyPublishBuildArtifacts@1\n  inputs:\n    CopyRoot: out\n    Contents: '**'\n    ArtifactName: drop\n    ArtifactType: Container` | unchanged<br>diags: [TRL-MOD-0003@2] | row 25 (one step would become two) |
| MODV-064 | `steps:\n- task: AppCenterDistribute@2\n  inputs:\n    serverEndpoint: ac\n    appSlug: o/app` | unchanged<br>diags: [TRL-MOD-0004@2] | row 6 [RET] |
| MODV-065 | `steps:\n- task: XamarinAndroid@1\n  inputs:\n    projectFile: '**/*.csproj'` | unchanged<br>diags: [TRL-MOD-0004@2] | row 64 [RET] |
| MODV-066 | `steps:\n- task: Xcode@3\n  inputs:\n    actions: build` | unchanged<br>diags: [TRL-MOD-0003@2] | row 71 [o:Xcode@3] [n:Xcode@5] |
| MODV-067 | `steps:\n- task: CmdLine@1\n  inputs:\n    filename: make\n- task: CopyFiles@1\n  inputs:\n    Contents: '**'` | `steps:\n- task: CmdLine@1\n  inputs:\n    filename: make\n- task: CopyFiles@2\n  inputs:\n    Contents: '**'`<br>diags: [TRL-MOD-0003@2, TRL-MOD-0001@5] | S7 (steps are independent) |

There are 67 vectors. They cover every class (A: 001..023 and 018, B: 024..059, C: 060..063, 066, D: 064, 065), case-insensitive names (002), input aliases (032, 037), key case (057), comments (003), quoting (004), line endings (013), templates and parameters (010, 011, 018..022), idempotence (015, 017), dynamic references (019, 020), unknown inputs (054) and guard failures (025, 031, 033, 035, 039, 041, 044, 046, 048, 050, 053, 056).

## 11. VERIFY items

| ID | Statement not confirmed by an official source |
|---|---|
| V1 | Runtime behaviour of the new majors that is not visible in `task.json` (task implementation changes, agent requirements, new Node handlers) does not change the meaning of an A or B rewrite. [RET] only says that major versions "can include breaking changes". |
| V2 | `FtpUpload@2` uploads with overwrite semantics although the `overwrite` input is gone (row 35). |
| V3 | `DotNetCoreCLI@2` with `selectOrConfig: config` and no `nugetConfigPath` restores like V0/V1 (from the `NuGet.config` found by `dotnet restore`) (rows 28, 29). |
| V4 | `modifyOutputPath: false` reproduces the V0/V1 publish path (rows 28, 29). |
| V5 | A single-line `CacheBeta@0` key means the same segmentation in `Cache@2` (row 19). |
| V6 | `DownloadPackage@0` handled NuGet packages only and did not behave differently from `extract: true` (row 32); the first statement is from the V0 help text. |
| V7 | `GitHubRelease@1` with the new default `makeLatest: true` matches what `GitHubRelease@0` caused (GitHub's default for the REST field) (row 36). |
| V8 | The retry defaults of `JenkinsQueueJob@2` (3 retries, 60 s) do not change a pipeline whose connection never fails (row 42). |
| V9 | `PublishSymbols@2` with `SymbolServerType: FileShare` and an explicit `SymbolsFolder` equals the V1 behaviour; `IndexSources` is the negation of `SkipIndexing` (row 54, inferred from the labels "Skip indexing" and "Index sources"). |
| V10 | `Xcode@5` with `useXcpretty: false` and `destinationSimulators: 'iPhone 7'` reproduces `Xcode@4` (row 72); the simulator may not exist on current images. |
| V11 | Option values and input keys are matched case-insensitively by the service (`ado-yaml-structure.md` section 2.6 has the same open point). |
| V12 | The only V0/V1 to V2 behavioural change of `InstallAppleCertificate` is the mandatory `keychainPassword` for the `default` and `custom` keychains (row 38). |
| V13 | A block scalar value is literal for the purpose of section 5.1.2. |
| V14 | Extension-contributed tasks are referenced as `publisher.extension.name@N`, so a dot in the name identifies them (section 3.2.4). |
| V15 | `Name@1.1.4` pins exactly that version; replacing it by the replacement's major is acceptable because that major is retired anyway (section 3.2.2). |
| V16 | A step with `enabled: false` should be rewritten (the reference breaks when the step is re-enabled after 2027-03-15). |
| V17 | A null input value (`key:`) is the empty string for `guard` and `drop ... if ''`. |
| V18 | The GUID of `RunVisualStudioTestsusingTestAgent` and the V1 inputs of `AzureResourceGroupDeployment`, `PowerShell`, `RunVisualStudioTestsusingTestAgent` are not public. |
| V19 | The replacement majors exist on Azure DevOps Server (the schedule of [RET] is for Services only). |
| V20 | The Learn task reference pages are not cited per row; only the retirement page and the `task.json` files are. |

## 12. Observations about the catalog (non-normative)

1. `tasks.retirement.yaml` and the Microsoft page list the same 73 entries (34 in the public repository table, 39 legacy built-in tasks).
2. `tasks.generated.yaml` contains the old `task.json` for only 34 of the 73 retired versions. For the 39 "legacy built-in" versions it has none, and for 12 of those it has no entry for the task at all (`AndroidBuild`, `AzureWebPowerShellDeployment`, `CopyPublishBuildArtifacts`, `DeployVisualStudioTestAgent`, `RunVisualStudioTestsusingTestAgent`, `ServiceFabricUpdateAppVersions`, `SonarQubePostTest`, `SonarQubePreBuild`, `VSMobileCenterTest`, `XamarinComponentRestore`, `XamarinLicense`, `XcodePackageiOS`), so no GUID. Section 8 supplies the GUIDs from the git history, except for `RunVisualStudioTestsusingTestAgent`.
3. The GUID `333b11bd-d341-40d9-afcf-b32d5ce6f23b` is shared by `NuGetCommand@2`, `NuGetInstaller@0` and `NuGetRestore@1`; the lookup rule "bare GUID resolves to the lexically first task name" (`catalog.md`) does not identify a retired reference; (GUID, major) does.
4. `QuickPerfTest@1` lists `AzureLoadTest@1`, which is not in `tasks.generated.yaml` (it is a Marketplace extension task).
5. `replacementKind: task` is used for rows whose official text is not a drop-in task: the Xamarin rows (project migration), `PyPIPublisher@0` (Twine plus a script), `ServiceFabricComposeDeploy@0` ("or a currently supported container deployment workflow"), `NuGetPackager@0`/`NuGetPublisher@0`/`NuGetRestore@1` (the page says "with the pack/push/restore command", which the catalog does not record), `AzureCloudPowerShellDeployment@1/@2` (a platform that is retiring).
6. For `AppCenterDistribute@1/@2` and `XamarinTestCloud@1` the `deprecationMessage` in `task.json` names `AppCenterDistribute@3`; [RET] says App Center distribution is retired. This specification follows [RET].
7. The plan sentence for M2-B ("AST rewrite + existing printer") conflicts with `ado-yaml-structure.md` section 15, which says the printer does not preserve comments. Section 6 uses byte-range splices.
8. `tasks.retirement.yaml` stores replacements alphabetically (`AzureCLI@3` before `AzurePowerShell@5` for `AzureNLBManagement@1`); the page lists `AzurePowerShell@5` first. Immaterial because the row is class C.

## 13. Proposed diagnostic codes

These codes are **proposals**; they are not in `internal/diag/registry.yaml` or `diagnostics.md` section 7. A new area `MOD` would also have to be added to the regular expression of section 1 of `diagnostics.md`. Alternatively the codes can be placed in the existing `CAT` area as `TRL-CAT-0007`..`0017`; the meanings are what matters.

| Code | Sev | Fid | Exit | Title | Remediation |
|---|---|---|---|---|---|
| `TRL-MOD-0001` | info | ex | - | Retired task reference rewritten to the same task family | None; the inputs are identical. |
| `TRL-MOD-0002` | warning | ap | - | Retired task reference rewritten with an input mapping | Review `params.changes` and `params.review`; test the pipeline. |
| `TRL-MOD-0003` | warning | nm | - | Retired task needs a manual migration | Follow `params.replacements`; no automatic mapping is documented. |
| `TRL-MOD-0004` | warning | nm | - | Retired task has no replacement task | Follow the official guidance in `params.guidance`. |
| `TRL-MOD-0005` | warning | nm | - | Task reference is not static or names a retired task as a string | Replace the retired task by hand where the value is used. |
| `TRL-MOD-0006` | warning | nm | - | Input cannot be migrated automatically | Change the input named in `params.input` by hand, then re-run. |
| `TRL-MOD-0007` | warning | ap | - | Input unknown to the retired task preserved | Check the input name for typos. |
| `TRL-MOD-0008` | info | ap | - | GUID task reference replaced by a task name | None; the replacement has another GUID. |
| `TRL-MOD-0009` | warning | nm | - | Step layout cannot be edited safely | Write `inputs` as a block mapping without directives, or migrate by hand. |
| `TRL-MOD-0010` | info | - | - | Extension task with a retired built-in name not rewritten | None; check that the extension task is not retired by its publisher. |
| `TRL-MOD-0011` | info | ap | - | Full task version replaced by a major version | None. |

## 14. Link references

[RET]: https://learn.microsoft.com/en-us/azure/devops/pipelines/tasks/task-retirement
[TASKS]: https://learn.microsoft.com/en-us/azure/devops/pipelines/process/tasks
[n:AndroidSigning@3]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/AndroidSigningV3/task.json
[n:ArchiveFiles@2]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/ArchiveFilesV2/task.json
[n:AzureCLI@3]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/AzureCLIV3/task.json
[n:AzureFunction@2]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/AzureFunctionV2/task.json
[n:AzureLoadTest@1]: https://learn.microsoft.com/en-us/azure/load-testing/how-to-configure-load-test-cicd
[n:AzureMonitor@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/AzureMonitorV1/task.json
[n:AzurePowerShell@5]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/AzurePowerShellV5/task.json
[n:AzureResourceGroupDeployment@2]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/AzureResourceGroupDeploymentV2/task.json
[n:AzureRmWebAppDeployment@5]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/AzureRmWebAppDeploymentV5/task.json
[n:Cache@2]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/CacheV2/task.json
[n:CmdLine@2]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/CmdLineV2/task.json
[n:CopyFiles@2]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/CopyFilesV2/task.json
[n:cURLUploader@2]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/CUrlUploaderV2/task.json
[n:DotNetCoreCLI@2]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/DotNetCoreCLIV2/task.json
[n:DownloadPackage@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/DownloadPackageV1/task.json
[n:DownloadPipelineArtifact@2]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/DownloadPipelineArtifactV2/task.json
[n:FtpUpload@2]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/FtpUploadV2/task.json
[n:GitHubRelease@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/GitHubReleaseV1/task.json
[n:Gradle@4]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/GradleV4/task.json
[n:InstallAppleCertificate@2]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/InstallAppleCertificateV2/task.json
[n:InstallAppleProvisioningProfile@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/InstallAppleProvisioningProfileV1/task.json
[n:InvokeRESTAPI@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/InvokeRestApiV1/task.json
[n:JenkinsQueueJob@2]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/JenkinsQueueJobV2/task.json
[n:Maven@4]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/MavenV4/task.json
[n:NuGetCommand@2]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/NuGetCommandV2/task.json
[n:NuGetToolInstaller@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/NuGetToolInstallerV1/task.json
[n:PackerBuild@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/PackerBuildV1/task.json
[n:PowerShell@2]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/PowerShellV2/task.json
[n:PowerShellOnTargetMachines@3]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/PowerShellOnTargetMachinesV3/task.json
[n:PublishBuildArtifacts@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/PublishBuildArtifactsV1/task.json
[n:PublishSymbols@2]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/PublishSymbolsV2/task.json
[n:PublishToAzureServiceBus@2]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/PublishToAzureServiceBusV2/task.json
[n:ServiceFabricDeploy@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/ServiceFabricDeployV1/task.json
[n:ServiceFabricUpdateManifests@2]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/ServiceFabricUpdateManifestsV2/task.json
[n:TwineAuthenticate@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/TwineAuthenticateV1/task.json
[n:UseDotNet@2]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/UseDotNetV2/task.json
[n:VSTest@3]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/VsTestV3/task.json
[n:Xcode@5]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/XcodeV5/task.json
[o:AndroidBuild@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/77d4e3df659c945bdcd465278bca26d87b8f3542/Tasks/AndroidBuild/task.json
[o:AndroidSigning@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/384b8fc58fdae09963fc9039e2d129247fa4db08/Tasks/AndroidSigning/task.json
[o:AndroidSigning@2]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/AndroidSigningV2/task.json
[o:AppCenterDistribute@0]: https://github.com/microsoft/azure-pipelines-tasks/blob/c89344e85082bc12e874ff336d6c1fbf7f2a5232/Tasks/AppCenterDistribute/task.json
[o:AppCenterDistribute@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/AppCenterDistributeV1/task.json
[o:AppCenterDistribute@2]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/AppCenterDistributeV2/task.json
[o:ArchiveFiles@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/ffc71acbb83173150f0d052249546eb6f38708cb/Tasks/ArchiveFiles/task.json
[o:AutomatedAnalysis@0]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/AutomatedAnalysisV0/task.json
[o:AzureCLI@0]: https://github.com/microsoft/azure-pipelines-tasks/blob/c932e92b4db984e06dcec9d6537bd9ca2a0de72c/Tasks/AzureCLI/task.json
[o:AzureCloudPowerShellDeployment@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/AzureCloudPowerShellDeploymentV1/task.json
[o:AzureCloudPowerShellDeployment@2]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/AzureCloudPowerShellDeploymentV2/task.json
[o:AzureFunction@0]: https://github.com/microsoft/azure-pipelines-tasks/blob/0a7b28405d9b7e1a4e52df380a3b2a6e089c8580/Tasks/AzureFunction/task.json
[o:AzureMonitor@0]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/AzureMonitorV0/task.json
[o:AzureNLBManagement@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/AzureNLBManagementV1/task.json
[o:AzurePowerShell@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/62edfc369b9be9b9d80964d829eb863ca293afb1/Tasks/AzurePowerShell/task.json
[o:AzureRmWebAppDeployment@2]: https://github.com/microsoft/azure-pipelines-tasks/blob/abcf4c10b043adb9d6d151e9f5cef48cd385242c/Tasks/AzureRmWebAppDeployment/task.json
[o:AzureWebPowerShellDeployment@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/77d4e3df659c945bdcd465278bca26d87b8f3542/Tasks/AzureWebPowerShellDeployment/task.json
[o:CacheBeta@0]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/CacheBetaV0/task.json
[o:Chef@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/ChefV1/task.json
[o:ChefKnife@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/ChefKnifeV1/task.json
[o:CmdLine@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/c932e92b4db984e06dcec9d6537bd9ca2a0de72c/Tasks/CmdLine/task.json
[o:CondaEnvironment@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/CondaEnvironmentV1/task.json
[o:CopyFiles@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/ca87a35422ab7edea6439f691c89247ee8339798/Tasks/CopyFiles/task.json
[o:CopyPublishBuildArtifacts@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/3b101aae3fb5f1cb8fede7d65987580cc4eae705/Tasks/CopyPublishBuildArtifacts/task.json
[o:cURLUploader@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/e4dbe93d609c753fbafc2fcd33f75d6b4994bf2e/Tasks/cURLUploader/task.json
[o:DeployVisualStudioTestAgent@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/d0c982e8432c574e4bf6bde7f3c0b9329f58005d/Tasks/DeployVisualStudioTestAgent/task.json
[o:DotNetCoreCLI@0]: https://github.com/microsoft/azure-pipelines-tasks/blob/bb868e8322c1b33dae72cb6850af023bb1e0f405/Tasks/DotNetCoreCLI/task.json
[o:DotNetCoreCLI@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/8f51d167932725e1b5ee4052e9a6247aaf0d3624/Tasks/DotNetCoreCLI/task.json
[o:DotNetCoreInstaller@0]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/DotNetCoreInstallerV0/task.json
[o:DotNetCoreInstaller@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/DotNetCoreInstallerV1/task.json
[o:DownloadPackage@0]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/DownloadPackageV0/task.json
[o:DownloadPipelineArtifact@0]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/DownloadPipelineArtifactV0/task.json
[o:DuffleInstaller@0]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/DuffleInstallerV0/task.json
[o:FtpUpload@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/FtpUploadV1/task.json
[o:GitHubRelease@0]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/GitHubReleaseV0/task.json
[o:Gradle@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/ff18c120fda4929ded0c5c7d8517d432ac7876dc/Tasks/Gradle/task.json
[o:InstallAppleCertificate@0]: https://github.com/microsoft/azure-pipelines-tasks/blob/001f56345dc97d851409f445005ba6152f2f10b7/Tasks/InstallAppleCertificate/task.json
[o:InstallAppleCertificate@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/cae4c15489d3601794b0ddde6837f5ef77479477/Tasks/InstallAppleCertificate/task.json
[o:InstallAppleProvisioningProfile@0]: https://github.com/microsoft/azure-pipelines-tasks/blob/2be504d418facc9dc9a5189484842d1beb5c952c/Tasks/InstallAppleProvisioningProfile/task.json
[o:InvokeRESTAPI@0]: https://github.com/microsoft/azure-pipelines-tasks/blob/0a7b28405d9b7e1a4e52df380a3b2a6e089c8580/Tasks/InvokeRestApi/task.json
[o:JenkinsQueueJob@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/c43897e88f612638df587d20e00fd01fa04bb04f/Tasks/JenkinsQueueJob/task.json
[o:Maven@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/ff18c120fda4929ded0c5c7d8517d432ac7876dc/Tasks/Maven/task.json
[o:Maven@2]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/MavenV2/task.json
[o:MysqlDeploymentOnMachineGroup@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/MysqlDeploymentOnMachineGroupV1/task.json
[o:NuGet@0]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/NuGetV0/task.json
[o:NuGetInstaller@0]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/NuGetInstallerV0/task.json
[o:NuGetPackager@0]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/NuGetPackagerV0/task.json
[o:NuGetPublisher@0]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/NuGetPublisherV0/task.json
[o:NuGetRestore@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/NuGetRestoreV1/task.json
[o:PackerBuild@0]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/PackerBuildV0/task.json
[o:PowerShellOnTargetMachines@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/PowerShellOnTargetMachinesV1/task.json
[o:PublishSymbols@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/381a7076fe7cc76cad1a2daaf4709d09455e30ef/Tasks/PublishSymbols/task.json
[o:PublishToAzureServiceBus@0]: https://github.com/microsoft/azure-pipelines-tasks/blob/5e9c0132fcc9f080d71b71e6836f779d07a3e774/Tasks/PublishToAzureServiceBus/task.json
[o:PyPIPublisher@0]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/PyPIPublisherV0/task.json
[o:QuickPerfTest@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/QuickPerfTestV1/task.json
[o:ServiceFabricComposeDeploy@0]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/ServiceFabricComposeDeployV0/task.json
[o:ServiceFabricUpdateAppVersions@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/b14807826a74714e040d5d95a4de4e770ddb95ee/Tasks/ServiceFabricUpdateAppVersions/task.json
[o:SonarQubePostTest@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/80e0bb7a5d0be07dbb32fc4f24e6955b1bb8e82d/Tasks/SonarQubePostTest/task.json
[o:SonarQubePreBuild@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/80e0bb7a5d0be07dbb32fc4f24e6955b1bb8e82d/Tasks/SonarQubePreBuild/task.json
[o:VSMobileCenterTest@0]: https://github.com/microsoft/azure-pipelines-tasks/blob/293e0ac84f088bf9eb91f92ff6b0d3f71c5c137b/Tasks/VSMobileCenterTest/task.json
[o:XamarinAndroid@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/XamarinAndroidV1/task.json
[o:XamarinComponentRestore@0]: https://github.com/microsoft/azure-pipelines-tasks/blob/cc4b3496fd41883b1d17bd1368b942c79e3e6a4d/Tasks/XamarinComponentRestore/task.json
[o:XamariniOS@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/e8ea8601024cf802603715a7b11730cdb1062118/Tasks/XamariniOS/task.json
[o:XamariniOS@2]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/XamariniOSV2/task.json
[o:XamarinLicense@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/77d4e3df659c945bdcd465278bca26d87b8f3542/Tasks/XamarinLicense/task.json
[o:XamarinTestCloud@1]: https://github.com/microsoft/azure-pipelines-tasks/blob/f1962f12fac2899b5d689967dda0d25523cc3ec4/Tasks/XamarinTestCloudV1/task.json
[o:Xcode@2]: https://github.com/microsoft/azure-pipelines-tasks/blob/8cfb5a37ecfac0160988e0f145a8994b7736c0f6/Tasks/Xcode/task.json
[o:Xcode@3]: https://github.com/microsoft/azure-pipelines-tasks/blob/667ce6d3e29bc60afc3d8361417da7be055eb96d/Tasks/Xcode/task.json
[o:Xcode@4]: https://github.com/microsoft/azure-pipelines-tasks/blob/dc4388de8062b62a9cb5f034fe0cb471871882a4/Tasks/Xcode/task.json
[o:XcodePackageiOS@0]: https://github.com/microsoft/azure-pipelines-tasks/blob/cc4b3496fd41883b1d17bd1368b942c79e3e6a4d/Tasks/XcodePackageiOS/task.json
