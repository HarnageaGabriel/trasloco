# In-script protocol operations

Status: draft for approval (M0). Normative.

Scripts can communicate with a runner through platform-specific command protocols. The scanner recognizes only statically provable operations and converts them to the abstract operations below. It is not a general shell interpreter.

## Abstract operations

| Operation | Required fields | Semantics |
|---|---|---|
| `setOutput` | `name`, `valueExpr`, `stepId` | Publish a named value for downstream dependency edges. |
| `setEnv` | `name`, `valueExpr` | Set an environment value for later steps in the current job. |
| `setPipelineVar` | `name`, `valueExpr`, `isSecret` | Mutate an Azure Pipelines variable for later task evaluation; distinct from a shell-local environment assignment. |
| `addPath` | `pathExpr` | Prepend a path for later steps in the current job. |
| `mask` | `valueExpr` | Register a value for log redaction before it may be printed. |
| `annotate` | `level`, `messageExpr`, optional location | Emit `error` or `warning` annotation. |
| `summary` | `contentExpr`, `mode` | Append or overwrite the current step summary. |

`valueExpr` is either a static literal, a recognized source variable reference, or `dynamic`. Operations containing secrets keep a reference, never the value. Operation order within a step MUST be preserved.

## Protocol mapping

| Abstract | Azure Pipelines | GitHub Actions | Fidelity notes |
|---|---|---|---|
| `setOutput` | `##vso[task.setvariable variable=N;isOutput=true]V` | append `N=V` to `$GITHUB_OUTPUT` | Exact for single-line static names/values when the producer step has an ID. |
| `setPipelineVar` | `##vso[task.setvariable variable=N]V` | append `N=V` to `$GITHUB_ENV` | Later-step same-job use is exact; broader ADO visibility is diagnosed when consumers cross the job boundary. |
| secret `setPipelineVar` | `##vso[task.setvariable variable=N;issecret=true]V` | `::add-mask::V` then append `N=V` to `$GITHUB_ENV` | `needs-manual` unless value comes from an existing secret binding; masking does not create a GitHub secret. |
| output + secret | `isOutput=true;issecret=true` | mask plus append to `$GITHUB_OUTPUT` | `needs-manual`; downstream secret propagation and redaction must be reviewed. |
| `addPath` | `##vso[task.prependpath]P` | append `P` to `$GITHUB_PATH` | Exact for a single static line. |
| `mask` | `##vso[task.setsecret]V` | `::add-mask::V` | Exact after escape validation; registration is not retroactive. |
| annotate error | `##vso[task.logissue type=error;sourcepath=F;linenumber=L;columnnumber=C]M` or `##[error]M` | `::error file=F,line=L,col=C::M` | Exact when fields are static; simplified form has no location. |
| annotate warning | `##vso[task.logissue type=warning;...]M` or `##[warning]M` | `::warning file=F,line=L,col=C::M` | Same. |
| `summary` | `##vso[task.uploadsummary]PATH` | append file content to `$GITHUB_STEP_SUMMARY` | Approximated: ADO uploads a file; GHA writes per-step Markdown. Reverse mapping requires a temporary file. |

VERIFY: Confirm `task.setsecret` availability and `uploadsummary` behavior on all supported Azure DevOps Server versions. VERIFY: Confirm current GitHub command escaping rules and whether legacy stdout annotations remain accepted on supported GHES versions.

## Generated syntax

Generated Bash MUST use quoted paths and `printf`, not `echo`:

```bash
printf '%s\n' 'name=value' >> "$GITHUB_OUTPUT"
printf '%s\n' 'name=value' >> "$GITHUB_ENV"
printf '%s\n' "$path" >> "$GITHUB_PATH"
printf '::add-mask::%s\n' "$value"
printf '%s\n' "$markdown" >> "$GITHUB_STEP_SUMMARY"
```

Generated PowerShell MUST use UTF-8 append semantics:

```powershell
"name=value" | Out-File -FilePath $env:GITHUB_OUTPUT -Encoding utf8 -Append
"name=value" | Out-File -FilePath $env:GITHUB_ENV -Encoding utf8 -Append
$path | Out-File -FilePath $env:GITHUB_PATH -Encoding utf8 -Append
Write-Output "::add-mask::$value"
$markdown | Out-File -FilePath $env:GITHUB_STEP_SUMMARY -Encoding utf8 -Append
```

For ADO targets, generated Bash uses `printf '##vso[...]%s\n' "$value"`; PowerShell uses `Write-Host "##vso[...]$value"`. Property values and payloads MUST be escaped using the destination protocol encoder, never string substitution alone.

Multi-line GitHub values require an inline delimiter block with a freshly generated delimiter proven absent from a static non-secret value. Trasloco ships no runtime helper and generated workflows MUST have no helper dependency. Dynamic or secret multi-line translation therefore becomes `needs-manual`; the plan MUST NOT contain the delimiter/value. ADO percent/newline escape semantics differ and require target-specific inline encoding. VERIFY: exact agent escape sequences for current ADO and GitHub runners.

## Scanner rules

The scanner MUST parse shell tokens/string literals sufficiently to distinguish literal output from arbitrary commands. It recognizes:

- Bash `echo`/`printf` and PowerShell `Write-Host`/`Write-Output` of a statically assembled protocol prefix;
- Bash `>> "$GITHUB_OUTPUT|ENV|PATH|STEP_SUMMARY"` and PowerShell `Out-File`/`Add-Content` to the corresponding `$env:` path;
- ADO command strings beginning exactly with `##vso[` or `##[error]`/`##[warning]` after static quote removal;
- GitHub annotation commands beginning `::add-mask::`, `::error`, or `::warning`.

The scanner MUST ignore comments, quoted examples that are not emitted, escaped redirection operators, and writes to similarly named ordinary files. It MUST retain source byte ranges for every match.

Bash and PowerShell (`pwsh`) receive full scanner and emitter support. A detected `cmd.exe`/batch step is retained unchanged and graded `needs-manual`; no partial protocol rewrite is allowed.

Dynamic variable names, computed protocol names, `eval`/`Invoke-Expression`, indirect environment/file-handle references, unbounded loops generating commands, multi-line values without a provably safe inline encoding, and data-dependent command construction MUST be graded `needs-manual`. The original step remains present and a diagnostic explains the unconverted operation; it MUST NOT be partially rewritten in a way that duplicates side effects. Annotation file and line are preserved on a best-effort basis whenever path mapping is known; otherwise the message and level remain and location is omitted with an approximated-fidelity diagnostic.

## Test vectors

| ID | Shell / input | Expected |
|---|---|---|
| SCRIPT-001 | bash `echo "##vso[task.setvariable variable=foo]bar"` | `setPipelineVar(foo, bar)`; map to `$GITHUB_ENV` |
| SCRIPT-002 | bash `echo "##vso[task.setvariable variable=foo;isOutput=true]bar"` | `setOutput(foo, bar)` |
| SCRIPT-003 | pwsh `Write-Host "##vso[task.setvariable variable=foo;issecret=true]$env:TOKEN"` | secret `setPipelineVar`; `needs-manual` unless TOKEN binding known |
| SCRIPT-004 | bash `echo "##vso[task.prependpath]/opt/tool"` | `addPath(/opt/tool)` |
| SCRIPT-005 | pwsh `Write-Host "##vso[task.setsecret]$value"` | `mask(value ref)` |
| SCRIPT-006 | bash `echo "##[error]failed"` | `annotate(error, failed)` |
| SCRIPT-007 | pwsh `Write-Host "##vso[task.logissue type=warning;sourcepath=a.cs;linenumber=3]warn"` | warning annotation with file/line |
| SCRIPT-008 | bash `echo "##vso[task.uploadsummary]summary.md"` | `summary(file summary.md)`; approximated to GHA |
| SCRIPT-009 | bash `echo "result=ok" >> "$GITHUB_OUTPUT"` | `setOutput(result, ok)` |
| SCRIPT-010 | pwsh `"X=1" >> $env:GITHUB_ENV` | `setEnv(X, 1)` |
| SCRIPT-011 | bash `printf '%s\n' "$p" >> "$GITHUB_PATH"` | `addPath(p ref)` |
| SCRIPT-012 | bash `echo "::add-mask::$TOKEN"` | `mask(TOKEN ref)` |
| SCRIPT-013 | bash `echo "::error file=a.go,line=4::bad"` | error annotation at a.go:4 |
| SCRIPT-014 | pwsh `"text" \| Out-File $env:GITHUB_STEP_SUMMARY -Append` | append summary |
| SCRIPT-015 | bash name stored in `$name`, written as `echo "$name=$v" >> "$GITHUB_OUTPUT"` | dynamic name; `needs-manual` |
| SCRIPT-016 | bash multi-line `$value` appended as one line | `needs-manual`; no unsafe rewrite |
| SCRIPT-017 | bash `eval "echo ...$x"` | `needs-manual` |
| SCRIPT-018 | pwsh `Invoke-Expression $command` | `needs-manual` |
| SCRIPT-019 | bash comment `# echo "##vso[...]"` | no operation |
| SCRIPT-020 | bash `echo '$GITHUB_OUTPUT example'` without redirection | no operation |
| SCRIPT-021 | bash `echo x >> ./GITHUB_OUTPUT` | no operation |
| SCRIPT-022 | ADO `isOutput=true;issecret=true` | output+secret; `needs-manual` |
| SCRIPT-023 | static GitHub output then downstream dependency | output edge rewritten to target syntax |
| SCRIPT-024 | unbounded loop emits names `X_$i` | `needs-manual`; retain whole step |
| SCRIPT-025 | `cmd.exe` step emits `##vso[task.setvariable ...]` | retain step; `needs-manual`; no rewrite |
| SCRIPT-026 | static annotation path with known source-to-target mapping | preserve mapped file and line |
