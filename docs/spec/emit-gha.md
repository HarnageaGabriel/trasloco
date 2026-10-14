# GitHub Actions workflow emission (M2-A walking skeleton)

Status: draft for approval (M2). Normative for `internal/dialect/githubactions/emit`. Written independently of the implementation (differential authorship, `CONVENTIONS.md`, "Tests"): every semantic claim cites an official page or carries a `VERIFY:` marker, never the behavior of any code.

Keywords MUST, MUST NOT, SHOULD and MAY are used as in [README.md](README.md). A statement prefixed `VERIFY:` is a platform fact that is not confirmed by the cited page; implementations treat it as the documented behavior until it is confirmed (`CONVENTIONS.md`, "AI agent rules"), and each one becomes a tracked issue.

## 1. Purpose and scope

The emitter turns a validated provider-neutral pipeline ([ir.md](ir.md)) into one GitHub Actions workflow file. The only reader implemented when this specification was written is Azure Pipelines ([normalize-ado.md](normalize-ado.md)), so every vector enters through `parse -> resolve -> normalize -> emit`. The emitter itself MUST NOT depend on Azure syntax: it consumes IR nodes only, and it ignores semantics that exist only inside `extensions` ([ir.md](ir.md) section 2) while carrying the node fidelity that the reader recorded.

M2-A is a deliberately thin end-to-end subset. Anything outside it MUST become an explicit disposition (section 2.3), never a silent drop.

### 1.1 In scope

| Area | M2-A coverage |
|---|---|
| Workflow | `name`, `on`, explicit top-level `permissions`, workflow `env`, `jobs` |
| Triggers | `push` and `pr` triggers: branch include/exclude, tag include/exclude, path include/exclude |
| Jobs | explicit `needs` graph (including the stage-derived edges of section 5.4), `name`, `runs-on` from hosted-image requirements, `timeout-minutes`, `continue-on-error`, job `env` |
| Steps | `run` with shells `bash`, `pwsh`, `powershell`, `cmd`; `checkout` of `self`; step `env`, `working-directory`, `timeout-minutes`, `continue-on-error`, `name` |
| Variables | literal non-secret variables to `env`; secret references as `${{ secrets.NAME }}` in step `env` only |
| Conditions | `success()`, `failure()`, `always()`, `cancelled()`, `and`/`or`/`not`, `eq`/`ne`, `contains`/`startsWith`/`endsWith` over step-visible literal variables |

### 1.2 Out of scope (explicit disposition required)

Scheduled, manual, upstream, webhook and other triggers; `pull_request_target`/`workflow_run` (never emitted, INV-09); task (`action`) steps and the catalog; artifact, cache, raw and protocol-operation steps; `run` steps carrying `operations`; checkout of another repository, checkout `path`, sparse checkout; matrix and parallel strategies; job containers and services; deployment jobs and environments; job outputs and `OutputRef`; reusable-workflow calls (`Job.uses`); `concurrency`; job-level `permissions` (except behind approval, section 5.12); variable groups; macro expansion; pipeline inputs; relational comparisons and `format`/`join`; opaque conditions. Section 5 states the disposition of each.

### 1.3 Relationship to other specifications

- [ir.md](ir.md) defines the input. This file assumes a pipeline that passed `Validate` and MUST NOT re-implement validation; invalid IR is rejected before emission (`TRL-VAL-0007`..`TRL-VAL-0012`).
- [normalize-ado.md](normalize-ado.md) defines what the Azure reader puts into the IR, including the losses it already reported with `TRL-CONV-0008`. The emitter MUST NOT report those again; it only records the carried fidelity (section 2.3).
- [diagnostics.md](diagnostics.md) owns the codes. Section 9 proposes new codes; this file does not edit the registry.
- [naming.md](naming.md) owns job IDs, workflow file names and GitHub secret names; the emitter calls the same normalizer and MUST NOT re-implement it.
- [catalog.md](catalog.md) and `catalog/github-actions/actions.lock.yaml` own action pins (INV-03).
- [script-protocol.md](script-protocol.md) is not applied in M2-A (section 1.2).

### 1.4 Source aliases

Every semantic vector and mapping row cites these pages. "Read" means read on 2026-10-11.

| Alias | URL |
|---|---|
| GH-WS | https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax |
| GH-EV | https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows |
| GH-EX | https://docs.github.com/en/actions/reference/workflows-and-actions/expressions |
| GH-CX | https://docs.github.com/en/actions/reference/workflows-and-actions/contexts |
| GH-SEC | https://docs.github.com/en/actions/reference/security/secure-use |
| GH-INJ | https://docs.github.com/en/actions/concepts/security/script-injections |
| GH-RUN | https://docs.github.com/en/actions/reference/runners/github-hosted-runners |
| GH-CHK | https://raw.githubusercontent.com/actions/checkout/3d3c42e5aac5ba805825da76410c181273ba90b1/action.yml |
| Y12 | https://yaml.org/spec/1.2.2/ext/changes/ |
| AZ-TRG | https://learn.microsoft.com/en-us/azure/devops/pipelines/yaml-schema/trigger |
| AZ-PR | https://learn.microsoft.com/en-us/azure/devops/pipelines/yaml-schema/pr |
| AZ-GH | https://learn.microsoft.com/en-us/azure/devops/pipelines/repos/github |
| AZ-AG | https://learn.microsoft.com/en-us/azure/devops/pipelines/repos/azure-repos-git |
| AZ-COND | https://learn.microsoft.com/en-us/azure/devops/pipelines/process/conditions |
| AZ-EXPR | https://learn.microsoft.com/en-us/azure/devops/pipelines/process/expressions |
| AZ-HOST | https://learn.microsoft.com/en-us/azure/devops/pipelines/agents/hosted |
| AZ-JOBS | https://learn.microsoft.com/en-us/azure/devops/pipelines/process/phases |
| AZ-SCR | https://learn.microsoft.com/en-us/azure/devops/pipelines/yaml-schema/steps-script |
| AZ-BASH | https://learn.microsoft.com/en-us/azure/devops/pipelines/yaml-schema/steps-bash |
| AZ-CHK | https://learn.microsoft.com/en-us/azure/devops/pipelines/yaml-schema/steps-checkout |
| AZ-SEC | https://learn.microsoft.com/en-us/azure/devops/pipelines/process/set-secret-variables |

## 2. Contract

### 2.1 Input

A validated `ir.Pipeline`, the action lock (`catalog/github-actions/actions.lock.yaml`), the naming policy, and `Options{AllowPermissionWidening bool}` (default false, set by `--allow-permission-widening`, [diagnostics.md](diagnostics.md) `TRL-EMIT-0003`). The emitter performs no I/O (`CONVENTIONS.md`, "Layering"): the lock and the policy are values.

### 2.2 Result

The emitter returns, for one pipeline:

1. zero or one file: path `.github/workflows/<workflow-file>` where `<workflow-file>` is the `naming.md` workflow-file normalization of `Pipeline.name`, and its bytes. A pipeline without jobs yields no file and `TRL-EMIT-0001`;
2. diagnostics, attributed to the origin of the IR node that caused them (`CONVENTIONS.md`, "Positions");
3. one disposition per IR node (section 2.3);
4. `requiredSecrets`: the sorted, de-duplicated list of GitHub secret names referenced by the file (never values).

Output bytes, the diagnostic list and the disposition list MUST be byte-identical across runs, processes and operating systems for the same IR, lock, policy and options (INV-01).

### 2.3 Dispositions and conservation

Each IR node of these kinds receives exactly one disposition, recorded under its stable ID: pipeline, input, trigger, variable, stage, job, step, and each job-level feature of the out-of-scope list in section 1.2 that is present on a job.

| Disposition | Meaning | Fidelity of the node in the report |
|---|---|---|
| `emitted` | Rendered in the file with the semantics of the mapping rule | the mapping's grade |
| `default` | Not rendered because the GitHub default is identical (for example `if: success()`) | `exact` |
| `placeholder` | The node's body is replaced by the failing placeholder of section 2.5 | `needs-manual` |
| `dropped` | Not rendered, with a diagnostic stating what behavior is lost | `approximated` or `unsupported` as the rule states |
| `carried` | The reader already recorded the loss in `extensions`; the emitter renders the rest and adds no diagnostic | the node's recorded fidelity |

Conservation (INV-12): the union of dispositions MUST cover every node, and the union of emitted file content MUST be explained by dispositions. A property test walks the IR against the re-parsed file and fails on any node without a disposition or any output element without a source node.

The reported fidelity of the file is the highest grade over all dispositions ([README.md](README.md) aggregation order). Provenance is `deterministic` (this emitter never calls an LLM).

### 2.4 Diagnostic attribution

Codes come from [diagnostics.md](diagnostics.md) unless section 9 proposes them. When several rules apply to one node the diagnostics are all emitted, each once per node. `severity` and exit class are fixed by the registry. Diagnostic messages and `params` MUST NOT contain source text beyond identifiers, never literal values (INV-05).

### 2.5 Failing placeholder

A node that cannot be converted safely becomes a placeholder rather than being omitted, so that a migrated workflow fails loudly instead of silently doing less (`TRL-CONV-0002`/`TRL-CONV-0003` remediation text).

- **Step placeholder** replaces the step. It keeps the step `name` (when emitted by section 5.6), and renders exactly:

```yaml
- name: <name>
  shell: bash
  run: |
    echo "::error::trasloco: manual conversion required (<CODE>)"
    exit 1
```

  `<CODE>` is the primary diagnostic code of the node, a fixed identifier from the registry. The placeholder MUST NOT contain any IR text (script, name, value, condition, path): the message is the only dynamic part and it is a code. A YAML comment `# trasloco: <CODE> (source line <N>)` MAY precede the item, with `<N>` the integer start line of the node origin, and is omitted when the node has no origin.
- **Job placeholder** keeps the job key, `name`, `needs`, `runs-on` (the mapped label, or `ubuntu-24.04` when the runner itself is the problem) and replaces every other key by a single step placeholder named `Manual conversion required`. The job `if`, `env`, `timeout-minutes` and `continue-on-error` are not rendered.
- A placeholder never inherits the source `if` of the step it replaces.

## 3. Output invariants

All invariants are normative (`MUST`). Each has a property test over every vector and over the corpus goldens.

| ID | Invariant |
|---|---|
| INV-01 | Deterministic: byte-identical output across runs, hosts and operating systems. Ordered IR collections (`OrderedMap`, arrays) keep declaration order. Unordered inputs are sorted by total comparators: lock entries by action then owner/repo, `requiredSecrets` by normalized name (byte order). No map iteration, wall-clock time or locale is observable. Line endings are `\n`; the file ends with exactly one `\n`. |
| INV-02 | Explicit least privilege: the document always has top-level `permissions: {}` (deny all). Jobs needing scopes carry job-level permissions; self checkout gets `contents: read` plus approved grants. `read-all`, `write-all` and an absent top-level block are never emitted. |
| INV-03 | Every `uses:` is `<owner>/<repo>@<40 lowercase hex>` followed by ` # <version>` from `actions.lock.yaml`. An absent lock entry is `TRL-CAT-0003` (error), becomes the section 2.5 placeholder, and still writes the workflow; no unpinned `uses:` is emitted. |
| INV-04 | No secret value is ever written: not in the file, the diagnostics, the dispositions or `requiredSecrets`. A secret appears only as `${{ secrets.<NAME> }}` and only as the value of a **step-level** `env` entry (never workflow or job `env`, never inside `run`, `with`, `name` or `if`). |
| INV-05 | No IR-supplied text is ever interpreted as a GitHub expression: a literal containing the two-character sequence `${{` is not emitted (section 5.13). The only `${{ }}` in the file are the ones this emitter generates from `Expr` nodes (section 5.10) and secret references. |
| INV-06 | Untrusted event data is never interpolated into `run`: the emitter generates no `${{ }}` inside `run:` text. Where a later package must expose such data to a script it passes the value through a step `env` entry and the script reads the shell variable (GH-INJ, GH-SEC). M2-A emits no event context at all. |
| INV-07 | YAML quoting: every scalar is written by the single writer under the rules of section 4.2, so that no value or key is reinterpreted by a YAML 1.1 or 1.2 reader (`on`/`off`/`yes`/`no`, numbers, `null`, leading indicators, `: `, ` #`). |
| INV-08 | Every `runs-on` is a pinned hosted label from section 5.5; `*-latest` aliases are never emitted. |
| INV-09 | `pull_request_target` and `workflow_run` are never emitted, and `actions/checkout` is never given a `ref` naming the pull-request head (GH-SEC, GH-EV). `persist-credentials` is always explicit. |
| INV-10 | The workflow is valid for the GitHub workflow schema and for actionlint (oracle tests); a violation is `TRL-EMIT-0002` (internal error), never shipped. |
| INV-11 | A placeholder never contains IR text (section 2.5). |
| INV-12 | Conservation (section 2.3): every IR node has a disposition; nothing is dropped without a diagnostic. |

## 4. Document layout and YAML writer

One YAML writer (`internal/yamlx`, ADR 0009: "new files are emitted from typed structures") produces the whole file; no other code may concatenate YAML text, and a bypass test greps for it.

### 4.1 Layout

1. Line 1 is the fixed comment `# Generated by trasloco. Review before enabling.` The vectors in section 7 omit this line.
2. Indentation is two spaces per level. A block sequence is indented two spaces under its key (`key:` then `  - item`), never at the key's column. No blank lines. No flow collections (`[a, b]`, `{a: b}`) except an empty mapping that a rule demands.
3. Fixed key order:
   - document: `name`, `on`, `permissions`, `env`, `jobs`;
   - job: `name`, `needs`, `if`, `runs-on`, `timeout-minutes`, `continue-on-error`, `env`, `steps`;
   - step: `name`, `if`, `uses`, `with`, `shell`, `working-directory`, `env`, `timeout-minutes`, `continue-on-error`, `run`;
   - `on`: IR trigger order (a push trigger precedes a pr trigger because the reader emits them in that order); inside `push`: `branches` or `branches-ignore`, `tags`, `paths` or `paths-ignore`; inside `pull_request`: `branches` or `branches-ignore`, `paths` or `paths-ignore`;
   - `with` of checkout: `ref`, `fetch-depth`, `submodules`, `lfs`, `persist-credentials`.
   Entries of `env` follow the IR ordered object. Jobs follow `Pipeline.jobs` order.
4. Integers (`timeout-minutes`, `fetch-depth`) are YAML integers; booleans are `true`/`false`. Everything else is a string scalar.
5. A step `id` is never emitted (M2-A has no step output references). A step or job `name` is emitted only when the IR display name differs from the IR ID, because the reader defaults the name to the ID.
6. `uses` carries the trailing comment ` # <version>`; this is the only trailing comment.

### 4.2 Scalar and key styles (INV-07)

For a key or a string value `s` the writer chooses the first applicable style:

1. **Plain** if all hold: `s` matches `^[A-Za-z_][A-Za-z0-9_./@ -]*$`, has no trailing space and no run of two spaces, and `s` case-folded is not one of `y n yes no on off true false null`. The workflow key `on` is the single exception: it is written plain.
2. **Double-quoted without escapes** if `s` has no control character and contains `'` and contains neither `"` nor `\`.
3. **Single-quoted** (an embedded `'` is doubled) if `s` has no control character.
4. **Double-quoted with escapes** (`\\`, `\"`, `\n`, `\t`, `\r`, `\xNN`, `\uNNNN`) otherwise.

The empty string is `''`. Consequently every value starting with a digit, `+`, `-`, `.`, `*`, `!`, `&`, `%`, `@`, a backtick or a bracket is quoted, as is every value containing `: `, ` #`, `"`, `$`, `{`, `(` or `=`. Examples: `yes` is written `'yes'`; `1e3` is `'1e3'`; `*-releases` is `'*-releases'`; `${{ always() }}` is `'${{ always() }}'`; an expression containing `'` and no `"` is double-quoted.

A string with a line break is written as a literal block scalar (`|`) only for `run`; elsewhere it falls to rule 4. The block uses the clip indicator `|` when the text ends with exactly one `\n`, `|-` when it ends without `\n`, `|+` when it ends with more than one, and the explicit indentation indicator `2` when the first line is empty or begins with a space. Text containing `\r`, a control character other than tab and line feed, U+0085, U+2028 or U+2029 uses rule 4 instead.

Rationale and sources: GitHub states that patterns starting with `*`, `[` or `!` must be quoted and that an unquoted `**/README.md` stops the workflow (GH-WS, "Filter pattern cheat sheet"); YAML 1.2 narrowed booleans to `true`/`false` while 1.1 also reads `y`, `yes`, `on` (Y12). VERIFY-19: GitHub does not document which YAML version its parser applies, so the writer is conservative for both.

## 5. Mapping

Fidelity grades are those of [README.md](README.md). "Exact" means equivalent within platform capabilities; the Azure semantics preserved are cited from learn.microsoft.com, the GitHub semantics from docs.github.com.

### 5.1 Master table

| IR construct | GitHub Actions output | Grade | Source | Notes |
|---|---|---|---|---|
| `Pipeline.name` | `name:` (omitted when empty) | exact | GH-WS | A `name` containing `${{` is omitted (section 5.13). |
| `Pipeline.id` | none (file name only) | exact | naming.md | File name from the workflow-file rule. |
| `Pipeline.inputs` | none | approximated | AZ-EXPR | Azure parameters are compile-time and already substituted by the resolver; declarations are `dropped` with `TRL-CONV-0001` (`difference`: parameters are not exposed as workflow inputs in M2-A). |
| `Pipeline.concurrency` | none | unsupported | GH-WS | `dropped`, `TRL-CONV-0002`. |
| trigger `push` | `on.push` (5.2) | exact, or approximated when a pattern is widened (5.3) | AZ-TRG, GH-EV | See 5.2 for tags and the tag guard. |
| trigger `pr` | `on.pull_request` (5.2) | exact | AZ-PR, AZ-GH, GH-EV | Activity types are the GitHub default (`opened`, `synchronize`, `reopened`); Azure fires on PR creation and new commits to the source branch. VERIFY-20 (a confirmed difference makes this approximated with `TRL-CONV-0001`). |
| trigger `schedule`, `manual`, `upstream`, `webhook`, `other` | none | needs-manual (`schedule`, `manual`), unsupported (others) | GH-EV | `dropped`; `TRL-CONV-0003` for `schedule`/`manual`, `TRL-CONV-0005` for the rest. |
| `Trigger.filters.branches/tags/paths` | `branches`, `branches-ignore`, `tags`, `paths`, `paths-ignore` | exact or approximated | GH-WS | Section 5.3. |
| `Stage` | none (GitHub has no stages) | exact | AZ-JOBS | Dependencies become job `needs` (5.4); a non-default stage `if` makes its jobs placeholders. |
| `Job` | `jobs.<id>` | exact | GH-WS | ID from naming.md; name per 4.1 rule 5. |
| `Job.needs`, stage `dependsOn`, `barrier` | `needs:` (always a block list) | exact | GH-WS, AZ-JOBS | Section 5.4. |
| `Job.if` | `if:` | exact or approximated | GH-WS, GH-EX, AZ-COND | Section 5.10; default `success()` is omitted. |
| `Job.runner` | `runs-on:` | exact or approximated | AZ-HOST, GH-RUN | Section 5.5. |
| `Job.timeout` | `timeout-minutes:` | exact | GH-WS, AZ-JOBS | Whole minutes; seconds round up (approximated, `TRL-CONV-0001`). Absent stays absent (Azure default 60 min vs GitHub 360 min is not materialized; see VERIFY-12). |
| `Job.allowFailure` | `continue-on-error: true` | approximated | GH-WS, AZ-JOBS | GitHub reports dependent `needs.<id>.result` as `success`, versus Azure `succeededWithIssues`; record one `TRL-CONV-0001` per job. VERIFY-09. |
| `Job.variables`, `Stage.variables`, `Pipeline.variables` | `env` (job, job, workflow) | exact | AZ-JOBS | Section 5.9. |
| `Job.permissions` | none unless approved | approximated | GH-WS | Section 5.12. |
| `Job.container`, `services`, `matrix`, `deployment`, `outputs`, `uses`, `concurrency` | job placeholder | needs-manual | n/a | `TRL-CONV-0003`; omitting them would change where, how often or how protected the job runs. |
| `Step` `run` | `run:` + `shell:` | exact | AZ-SCR, AZ-BASH, GH-WS | Section 5.7. |
| `Step` `checkout` (self) | `uses: actions/checkout@<sha> # <version>` + `with:` | exact | AZ-CHK, GH-CHK | Section 5.8. |
| `Step` `checkout` other repository, `path` | step placeholder | needs-manual | AZ-CHK | `TRL-CONV-0003`. |
| `Step` `action` | step placeholder | needs-manual | n/a | `TRL-CAT-0001`; the catalog is M2-C. |
| `Step` `artifactPublish`, `artifactRestore`, `cache`, `raw`, protocol operations | step placeholder | needs-manual | n/a | `TRL-CONV-0003`. |
| `Step.if` | `if:` | exact or approximated | GH-EX, AZ-COND | Section 5.10. |
| `Step.env` | `env:` | exact | GH-WS, AZ-SCR | Secret and macro rules in 5.9. |
| `Step.timeout` | `timeout-minutes:` | exact | GH-WS, AZ-SCR | Same rounding as the job. |
| `Step.continueOnError` | `continue-on-error: true` | exact | GH-WS, AZ-SCR | `false` is the default and is omitted. |
| `Step.retry` > 0 | none | approximated | GH-WS | GitHub has no native step retry; `TRL-CONV-0001`. |
| `Variable` literal | `env` entry | exact | AZ-JOBS | |
| `Variable` secret (`SecretRef`) | no `env`; entry in `requiredSecrets` | exact | AZ-SEC | Azure does not map secrets into the environment automatically, so none is created. |
| `Variable` of scope `group` | none | needs-manual | AZ-JOBS | `dropped`, `TRL-CONV-0003`. |

### 5.2 Triggers

1. For every IR trigger of kind `push` or `pr` the emitter writes the event regardless of `binding.inYaml`; GitHub has no externally provisioned trigger. When `inYaml` is `false` the emitter adds `TRL-EMIT-0001` (info): the reader uses `false` for the synthesized "CI on every branch" default ([normalize-ado.md](normalize-ado.md)).
2. **No trigger left.** If no `push`/`pr` event remains (for example `trigger: none` and no `pr`), the `on` mapping is exactly `workflow_dispatch:` (empty value) and the emitter adds `TRL-EMIT-0001` (info). A workflow file without an event is invalid.
3. **Branch guard.** A `push` event always has `branches` or `branches-ignore`, even when the IR has no branch filter, because on GitHub an event with neither filter also runs for tag pushes (GH-WS), whereas Azure does not trigger on tags unless tag filters are written (AZ-AG, "Tags"). The unfiltered default is `branches: ['**']`; `'**'` matches every branch name (GH-WS, cheat sheet). Path filters are not evaluated for tag pushes (GH-WS), which is a second reason for the guard.
4. **Sign handling.** Within one filter kind (branches, tags, paths) the IR glob list is ordered, with a leading `!` marking an exclusion. If it contains at least one positive pattern the emitter writes the positive key (`branches`, `tags`, `paths`) with the patterns in IR order, keeping `!` prefixes. If it contains only negative patterns the emitter writes the `-ignore` key with the `!` removed: Azure documents an exclude-only filter as equivalent to including `*` (AZ-TRG, AZ-PR), and GitHub forbids a positive key and its `-ignore` twin together and forbids a `!` pattern without a positive one (GH-WS). Order matters on GitHub: a later `!` pattern removes and a later positive pattern re-includes (GH-WS). VERIFY-04: Azure says the order of path filters does not matter and that an excluded path cannot be included again unless qualified to a deeper folder (AZ-AG); the reader lists includes before excludes, which makes the two models agree for the shapes the reader produces.
5. **Ref prefixes.** In the branch list of a trigger a pattern `refs/tags/<p>` moves to the tag list as `<p>` and a pattern `refs/heads/<p>` is written as `<p>`, keeping its sign (AZ-TRG documents `refs/tags/{tagname}` in the branch list). VERIFY-02.
6. **Tags.** With tag filters present and no branch filter the branch guard of rule 3 still applies. VERIFY-03: whether an Azure trigger with only `tags` also fires for branches is not documented.
7. **Pull requests.** `pull_request` filters match the pull request's base branch (GH-EV); Azure matches the target branch (AZ-GH, "PR triggers"). `pull_request` has no tag filter; tag filters on a `pr` trigger are `dropped` with `TRL-CONV-0005`. The pull-request build is the merge commit on both platforms (AZ-GH; GH-EV).
8. A `pr` trigger absent from the IR produces no `pull_request` event and no diagnostic. Azure validates pull requests to all branches by default for GitHub repositories (AZ-GH); see the observation in section 10.
9. Reader losses (`batch`, `autoCancel`, `drafts`, display names) are `carried`.

### 5.3 Glob translation

The IR glob dialect is gitignore-style ([ir.md](ir.md) section 4): `/` separator, `*` excludes `/`, `**` crosses it, `?` is one non-`/` character, `[abc]` and `[!abc]` are classes, leading `!` negates, `\` escapes. GitHub's dialect differs (GH-WS, "Filter pattern cheat sheet"): `*` and `**` as in the IR; `?` means zero or one of the preceding character and `+` one or more; `[]` ranges cover only `a-z`, `A-Z`, `0-9`; a leading `!` negates; `\` escapes. Translation of one positive or negative pattern, applied before the sign handling of 5.2:

| IR token | Output | Reason |
|---|---|---|
| `*`, `**`, `/`, letters, digits | unchanged | Same meaning. |
| `\x` (escaped metacharacter) | `\x` | Escape is shared. |
| literal `+` | `\+` | `+` is a quantifier on GitHub. |
| literal `.` and other characters | unchanged | No special meaning. |
| `[abc]`, `[a-z]` (ranges within `a-z`, `A-Z`, `0-9` only) | unchanged | Same meaning. |
| `?`, `[!...]`, or a class using other ranges | not expressible | See below. |

A pattern with a non-expressible token is handled so that a trigger can fire **more often, never less**: a positive pattern has the token replaced by `*`; a negative pattern is dropped. Both record `TRL-EMIT-0007` (warning, approximated) with `params.pattern` (the IR pattern, which is a path or branch glob and not secret material) and `params.action` (`widened` or `dropped`). An empty pattern is dropped without widening. Each emitted pattern is then written under section 4.2 (so `releases/*` becomes `'releases/*'`).

VERIFY-01: Azure documents `*` as "zero or more characters" without excluding `/` (AZ-GH, "Wildcards"), while the IR dialect makes `*` stop at `/`. The emitter follows the IR; see section 10.

### 5.4 Jobs and the dependency graph

1. **Job key.** The IR job ID goes through the `naming.md` "GitHub job ID" normalization (`Build.compile` becomes `Build_compile`, `2build` becomes `N_2build`). A change records `TRL-NAME-0001`; a collision after normalization is resolved by `naming.md` (hash suffix) and records `TRL-NAME-0002`. Every `needs` entry is rewritten with the same map.
2. **Edges.** The `needs` list of a job is, in this order, de-duplicated keeping the first occurrence: (a) `Job.needs` in IR order; (b) for each stage ID in `Stage.dependsOn` of the job's stage, in order, every job of that stage in stage order; (c) when the job's stage has `barrier: true`, every job of all preceding stages in stage order. Azure runs a stage only after the stages it depends on complete, and jobs of a stage run in parallel unless `dependsOn` is set (AZ-JOBS, "Dependencies"; the reader already made the stage default and `dependsOn: []` explicit). Because GitHub has no stage, rule (b) is the faithful encoding; it also keeps GitHub's implicit `success()` over all needed jobs, which equals Azure's default for stage and job conditions (AZ-COND).
3. **Stage conditions.** A stage `if` other than the default `success()` makes **every job of that stage** a placeholder (`TRL-CONV-0003`, one diagnostic on the stage). Stage `variables` are merged into each job's `env` (5.9). Stage display names are not emitted.
4. `needs` is always a block sequence, even for one entry. A job without edges has no `needs` key.
5. A job whose only problem is `if` or `runner` becomes a job placeholder as defined in 2.5.

### 5.5 Runners

`RunnerReq` is mapped only when `pool` is empty or equals `Azure Pipelines` (case-insensitively), `labels` and `demands` are empty, `arch` is empty or `x64`, and `image` is a key of the table below (case-insensitive). The table is the dated catalog snapshot of 2026-10-11 (AZ-HOST, GH-RUN); both platforms publish the same runner-images repository, which is why explicit versions map one to one.

| Azure `vmImage` | GitHub label | Grade | Diagnostic |
|---|---|---|---|
| `ubuntu-latest` | `ubuntu-24.04` | approximated | `TRL-CAT-0006` (info): `-latest` aliases diverge, so the alias is pinned |
| `ubuntu-26.04`, `ubuntu-24.04`, `ubuntu-22.04` | same label | exact | none |
| `windows-latest` | `windows-2025` | approximated | `TRL-CAT-0006` |
| `windows-2025`, `windows-2022` | same label | exact | none |
| `windows-2025-vs2026` | `windows-2025-vs2026` | approximated | `TRL-CONV-0007`; GitHub lists this label for public repositories only (VERIFY-11) |
| `macOS-latest` | `macos-15-intel` | approximated | `TRL-CAT-0006` |
| `macOS-26`, `macOS-15` | `macos-26-intel`, `macos-15-intel` | exact | none |
| `macOS-14` | `macos-14` | approximated | `TRL-CONV-0007`: Azure's image is Intel, GitHub's `macos-14` is Apple silicon (VERIFY-10) |

Rules:

1. A mapped label is emitted with `INV-08`. The pin is a policy choice made so the output does not change when either vendor moves its alias.
2. An empty `image` with `pool` empty or `Azure Pipelines` (no `pool` key at all) maps as `ubuntu-latest`, because Azure uses that image when none is specified (AZ-HOST): label `ubuntu-24.04`, diagnostics `TRL-CONV-0004` (info) and `TRL-CAT-0006`.
3. Anything else (unknown or retired image such as `vs2017-win2016`, a private pool name, demands, labels, a non-`x64` arch, an `os` that disagrees with the label's OS) makes the job a placeholder with `runs-on: ubuntu-24.04` and `TRL-CONV-0003`. Self-hosted pools are never guessed.
4. The label's OS (`ubuntu-*` Linux, `macos-*` macOS, `windows-*` Windows) drives the shell resolution of 5.7.

### 5.6 Steps in general

1. Step order is preserved. The reader's implicit checkout (`implicit-checkout`, name `Checkout self`) is an ordinary checkout step.
2. `name:` per 4.1 rule 5. `if:` per 5.10. `timeout-minutes` and `continue-on-error` per the master table.
3. A step of an out-of-scope kind, or a `run` step with non-empty `operations`, is a step placeholder (2.5) with `TRL-CONV-0003`, or `TRL-CAT-0001` for an `action` step.
4. `enabled: false` reaches the IR as the literal condition `false` and is emitted as `if: ${{ false }}` (5.10, rule 7).

### 5.7 `run` steps and shells

The IR shell is explicit. Mapping to the GitHub `shell:` value:

| IR shell | Runner OS | `shell:` | Source |
|---|---|---|---|
| `bash` | any | `bash` | AZ-BASH, GH-WS |
| `pwsh` | any | `pwsh` | GH-WS |
| `powershell` | any | `powershell` | GH-WS |
| `cmd` | Windows | `cmd` | GH-WS |
| `azure-default` (the reader's name for the generic `script:` shortcut) | Linux, macOS | `bash` | AZ-SCR: "cmd.exe on Windows and Bash on other platforms" |
| `azure-default` | Windows | `cmd` | AZ-SCR |
| any other value | any | step placeholder, `TRL-CONV-0003` | n/a |

`shell:` is always written (INV-01 determinism does not depend on a runner default). Other rules:

- `run` carries the IR script unchanged, written under 4.2. A script is eligible only if it has no `operations` and contains no `${{` (5.13) and no Azure macro (5.9, rule 6). Otherwise the step is a placeholder.
- `working-directory` carries `workingDirectory` when it is a relative or absolute path without `$(`; with `$(` the step is a placeholder. The Azure default (`$(Build.SourcesDirectory)`, the repository root, AZ-SCR) equals the GitHub workspace after `actions/checkout` of `self`.
- GitHub runs `bash` as `bash --noprofile --norc -eo pipefail {0}` and `pwsh`/`powershell` through `-command ". '{0}'"` (GH-WS). VERIFY-07: whether Azure's Bash runs with `errexit`/`pipefail` is not stated by AZ-BASH; if it does not, a failing intermediate command now stops the step. VERIFY-08: PowerShell `ErrorActionPreference` defaults. Until confirmed the grade is `exact` and no per-step diagnostic is raised; a confirmed difference becomes one `TRL-CONV-0001` per workflow.
- `failOnStderr` and other script options are reader losses and are `carried`.

### 5.8 Checkout

Only `repository: self` is emitted. The step is `uses: actions/checkout@<sha> # <version>` from the lock (INV-03) with:

| IR field | `with` key | Rule |
|---|---|---|
| `ref` | `ref` | Omitted when the expression is the literal `$(Build.SourceVersion)` (the reader's spelling of "the triggering commit"): `actions/checkout` defaults to the triggering ref/SHA (GH-CHK; for pull requests the merge commit, GH-EV). Any other string literal is written; one containing `$(` makes the step a placeholder. |
| `fetchDepth` | `fetch-depth` | Written when greater than 0. Zero and absent are omitted (action default 1, GH-CHK). See observation O-5: the IR cannot tell `fetchDepth: 0` (full history, AZ-CHK) from unset. VERIFY-13: Azure's default depth depends on a pipeline setting (AZ-CHK, "Shallow fetch"). |
| `submodules` | `submodules` | `true` written; `false` omitted. VERIFY-14: the reader folds `recursive` into `true` (normalize-ado.md), so recursion is lost on this path. |
| `lfs` | `lfs` | `true` written; `false` omitted. |
| `persistCredentials` | `persist-credentials` | Always written. Azure leaves the token out of git config by default (AZ-CHK) while the action keeps it by default (GH-CHK), so `false` is explicit. |
| `path` non-empty | none | Step placeholder: Azure's root is `$(Pipeline.Workspace)` (AZ-CHK), GitHub's is `GITHUB_WORKSPACE` (GH-CHK). |

A job without a checkout step (for example after `checkout: none`) emits none. `checkout.prMergeRef`, when present, is informational: both platforms build the merge commit of a pull request.

### 5.9 Variables, `env` and secrets

1. **Projection of names.** Azure exposes a variable to scripts as an environment variable whose name is the upper-cased variable name with `.` and spaces replaced by `_` (AZ-JOBS, "Job variables": `mySimpleVar` is `MYSIMPLEVAR`, `my.dotted.var` is `MY_DOTTED_VAR`, `my var with spaces` is `MY_VAR_WITH_SPACES`). The emitter writes that name. Any remaining character outside `[A-Za-z0-9_]` is replaced by `_`, a leading digit gets the prefix `N_`, and the change records `TRL-NAME-0001`. VERIFY-17: Azure's projection of other characters (for example `-`) is unconfirmed. Explicit `env:` keys of a step are written as given ([variable-precedence.md](variable-precedence.md): "Explicit `env:` keys remain as written"). VERIFY-16: GitHub reserves names beginning `GITHUB_` for its own variables; a projected or explicit key with that prefix is written unchanged until the reservation is confirmed for `env`.
2. **Placement.** Pipeline-scope literals go to workflow `env`; stage-scope literals are copied, in declaration order and before job entries, into the `env` of every job of that stage; job-scope literals go to job `env`. A later entry with the same projected name replaces the earlier value in place (Azure's more specific scope wins); two entries of the **same** scope projecting to one name keep the later value and record `TRL-VAR-0004`. VERIFY-18: Azure variable names are case-insensitive.
3. **Values.** A workflow, stage or job variable whose value is a literal string without `$(` and `$[` is written under 4.2. A value containing an Azure macro `$(` or runtime `$[` text cannot be expanded by GitHub; the variable is `dropped` and `TRL-CONV-0003` records it, because GitHub would pass the text through unexpanded. A step `env` value follows the same test, except that the whole-value secret patterns of rule 5 are translated; a step `env` value of any other expression kind (a reference to `variables`, `parameters`, `env`, `matrix`, `runner`, `run` or `dependencyOutputs`, or a call) is not translated and the step is a placeholder (`TRL-EXPR-0002`).
4. **Group variables** (`scope: group`) are `dropped` with `TRL-CONV-0003`; the IR spells a group import as a pseudo-variable named after the group, so the emitter MUST discriminate by `scope`, never by name.
5. **Secrets.** A `Variable` with `SecretRef` creates no `env` entry (Azure does not map secrets into the environment automatically and requires an explicit `env:` mapping, AZ-SEC) and contributes its normalized name to `requiredSecrets`. A step `env` value that is a `secrets` reference (the reader's form for a secret-looking literal) is written as `${{ secrets.<NAME> }}`. A step `env` value that is exactly one macro `$(NAME)`, where `NAME` contains no `.`, is handled as AZ-SEC's canonical pattern (`FOO_ONE: $(SecretOne)`): it becomes `${{ secrets.<NAME> }}`; when `NAME` is not a declared secret variable it is assumed to be a secret defined outside the YAML (UI or variable group) and the emitter records `TRL-CONV-0001` (approximated). `<NAME>` is the `naming.md` "GitHub secret" normalization (`db.password` becomes `DB_PASSWORD`); a change records `TRL-NAME-0001`. Every secret reference records `TRL-EMIT-0008` (info). A macro naming a declared non-secret variable, or a name containing `.` (predefined variables such as `Build.BuildId`), is not translated: the step is a placeholder (`TRL-CONV-0003`).
6. **Macros in scripts.** In `run` text and `working-directory`, the token `$(NAME)` with `NAME` matching `[A-Za-z_][A-Za-z0-9_.-]*` is an Azure macro when `NAME` contains a `.` or is a variable declared anywhere in the IR; such a step is a placeholder (`TRL-CONV-0003`). Other `$(...)` text (for example `$(date)`, or anything with whitespace) is shell command substitution and is left unchanged. Secrets are never inlined into scripts (INV-04).
7. A declared variable that is never referenced is still emitted (rule 2), keeping behavior for scripts that read it from the environment.

### 5.10 Conditions

Input is `Job.if` or `Step.if` (a `Cond`). Output is `if: ${{ <E> }}` written under 4.2, or no `if` at all.

1. **Default.** A condition that is exactly `success()` is omitted (`default`): GitHub applies `success()` unless a status function appears (GH-EX), and Azure's default for jobs and steps is "all previous dependencies/steps succeeded" (AZ-COND).
2. **Opaque.** `Cond.opaque` is a placeholder with `TRL-EXPR-0004`. Source text is never copied into the file.
3. **Expression translation.** `E` is built recursively; `P(x)` means `x` wrapped in parentheses when `x` is a binary operator expression (`&&`, `||`, `==`, `!=`) and unchanged otherwise (function calls, references, literals and a unary `!` expression, whose own operand is already wrapped by `P`). GitHub's documentation does not state operator precedence (GH-EX), so every operand of a binary operator that is itself a binary operator is parenthesized and no precedence between binary operators is assumed.

| IR | GitHub | Notes |
|---|---|---|
| `success`, `failure`, `always`, `cancelled` (no arguments) | `success()`, `failure()`, `always()`, `cancelled()` | AZ-EXPR: `succeeded()` is true for succeeded and partially succeeded; `failed()` for a job is true when any previous job in the graph failed, which matches GitHub's "any ancestor job" (GH-EX). The same function in both. |
| any of the above with arguments (`failed('a')`) | not translated | `TRL-EXPR-0002`, placeholder |
| `succeededWithIssues` | not translated | `TRL-EXPR-0002`, placeholder |
| `and(a, b, ...)` | `P(a) && P(b) && ...` | Short-circuit order preserved. |
| `or(a, b, ...)` | `P(a) \|\| P(b) \|\| ...` | |
| `not(a)` | `!P(a)` | |
| `eq(a, b)`, `ne(a, b)` | `P(a) == P(b)`, `P(a) != P(b)` | Operand rules in rule 5. |
| `contains(a, b)`, `startsWith(a, b)`, `endsWith(a, b)` | same function names | Both platforms compare as case-insensitive strings (AZ-EXPR, GH-EX). |
| `lt`, `le`, `gt`, `ge`, `format`, `join` | not translated | `TRL-EXPR-0002`, placeholder. Azure converts the right operand to the left type, GitHub coerces both to numbers (AZ-EXPR, GH-EX); no safe common form exists in M2-A. |
| `variables` reference `[N]` | `env.<P(N)>` | Rule 4. |
| other contexts (`parameters`, `secrets`, `matrix`, `runner`, `run`, `dependencyOutputs`, `env` reference) | not translated | `TRL-EXPR-0002`, placeholder. `secrets` is not available in `steps.if` (GH-CX). |
| string literal | `'...'` with `'` doubled | GH-EX. A literal containing a control character, `$(` or `$[` is not translated (`TRL-EXPR-0002`): GitHub would not expand the Azure macro. |
| bool literal | `true`, `false` | |
| number literal | its decimal text | Only if it survives binary64 round trip; else `TRL-EXPR-0002`. |
| null literal | `null` | |

4. **Variable references.** `variables.N` is translatable only in a **step** condition, only for a single-segment name `N` that is a literal non-secret `Variable` visible from the step (pipeline, stage or job scope) and actually written to `env` (not dropped by 5.9 rule 3), and becomes `env.<P(N)>` with `P` the projection of 5.9. In a **job** condition the `env` context is not available (GH-CX: `jobs.<job_id>.if` allows `github`, `needs`, `vars`, `inputs`), so any variable reference there is `TRL-EXPR-0002` and the job is a placeholder; translating to `vars.*` belongs to the variables package (M2-D). A reference to an undeclared, secret, predefined (name containing `.`) or step-scope variable is `TRL-EXPR-0002` and the step is a placeholder. VERIFY-05: whether a step's own `env` entries are visible in its `if` (GH-CX lists `env` for `steps.if` without saying).
5. **Comparison operands.** Azure variables are always strings (AZ-EXPR). For `eq`/`ne` where the left operand is a variable reference and the right operand is a bool or number literal, the literal is written as the string of its spelling (`true` becomes `'true'`, `1` becomes `'1'`): Azure converts the right operand to the left operand's type and compares strings ignoring case, whereas GitHub would coerce a string like `'true'` to `NaN` and compare numerically (GH-EX). VERIFY-06: AZ-EXPR also says `eq` converts to numbers; the two statements disagree. With a literal on the left and a reference on the right, only a string literal is translated; otherwise `TRL-EXPR-0002`.
6. **Explicit conditions replace the default.** Azure: when a `condition` is written it overwrites the default and the node "might run even if the build is canceled" (AZ-COND). GitHub adds an implicit `success()` unless an expression contains a status function (GH-EX). Therefore if the translated tree contains no status function, `E` is `always() && P(tree)`; a literal `true` is `always()` and a literal `false` is `false`. A tree containing any status function anywhere is emitted as is. Grade: exact for steps; approximated for jobs (`TRL-CONV-0001`, `difference`: GitHub's `always()` ignores the result of needed jobs, Azure skips a job whose parent stage was skipped), noting GitHub's own warning about `always()` and cancellation (GH-EX).
7. Literal `false` (the reader's encoding of `enabled: false`) is `if: ${{ false }}`.

### 5.11 Timeouts and `continue-on-error`

`Duration` is a Go duration. `timeout-minutes` is its whole minutes; a remainder rounds up and records `TRL-CONV-0001`. A zero or absent duration is omitted. GitHub accepts positive integers only (GH-WS). `continue-on-error: true` is written only for `true`; `false` is the default. Step values equal the job rules.

### 5.12 Permissions

The document always has `permissions:` with exactly `contents: read` (INV-02): the checkout of `self` needs read access to contents and nothing else is required in M2-A. A non-empty `Job.permissions` that grants anything above `contents: read` is not emitted unless `Options.AllowPermissionWidening` is true; without approval the emitter records `TRL-EMIT-0003` (warning, approximated) and the grant is `dropped`. With approval the job gets a job-level `permissions` mapping that lists exactly the IR entries in order, plus `contents: read` first when the job has a checkout step and the IR does not mention `contents` (GitHub sets every unspecified scope to `none` once any is specified, GH-WS), and `TRL-EMIT-0003` is still recorded as the audit trail.

### 5.13 Expression delimiter in literals

GitHub evaluates `${{ ... }}` inside `run`, `env`, `with`, `name`, `if`, `working-directory` and other string fields before the shell or action sees them, and has no escape (GH-INJ; VERIFY-15: absence of an escape). An IR literal containing `${{` therefore cannot be written faithfully and could execute attacker-chosen expressions. The emitter MUST NOT write it: a step containing it becomes a placeholder (its `name` is omitted when it is the field containing it); a workflow or job `name` containing it is omitted; a variable value containing it is `dropped`. Each case records `TRL-EMIT-0006` (warning, needs-manual). The Azure reader cannot produce such literals (compile-time `${{ }}` is rejected earlier), so these vectors edit the IR directly.

## 6. Diagnostics summary

| Situation | Code | Severity (registry) |
|---|---|---|
| Synthesized default trigger written; no trigger left (`workflow_dispatch`); pipeline without jobs | `TRL-EMIT-0001` | info |
| Emitted file fails schema/actionlint | `TRL-EMIT-0002` | error |
| Job-level grant above `contents: read` | `TRL-EMIT-0003` | warning |
| Action missing from lock / not allowed | `TRL-CAT-0003`, `TRL-CAT-0004` | error |
| Task step | `TRL-CAT-0001` | warning, needs-manual |
| `-latest` image pinned | `TRL-CAT-0006` | info |
| Image or architecture differs | `TRL-CONV-0007` | warning |
| Implicit default made explicit (default image) | `TRL-CONV-0004` | info |
| Approximation (retry dropped, rounding, secret assumed, explicit-condition job semantics) | `TRL-CONV-0001` | warning |
| Needs-manual construct (placeholder or drop) | `TRL-CONV-0003` | warning |
| Trigger with no target equivalent | `TRL-CONV-0005` | warning |
| Unsupported construct dropped | `TRL-CONV-0002` | warning |
| Expression not translatable | `TRL-EXPR-0002`, `TRL-EXPR-0004` | warning |
| Name changed or collided | `TRL-NAME-0001`, `TRL-NAME-0002` | info, warning |
| Projection collision | `TRL-VAR-0004` | warning |
| Pattern widened or dropped | `TRL-EMIT-0007` (proposed) | warning |
| `${{` in literal | `TRL-EMIT-0006` (proposed) | warning |
| Secret reference written | `TRL-EMIT-0008` (proposed) | info |

## 7. Test vectors

### 7.1 Conventions

The README convention applies (IDs `EMITV-NNN`, never reused, tests read this file). Because Azure Pipelines YAML and GitHub YAML cannot be written legibly in table cells, each vector has a **row** in a category table (ID, summaries, diagnostics, sources) and a **body** made of fenced blocks under a heading with the same ID. A vector whose row or body is missing is a loader failure. The row cells are non-normative summaries; the bodies are normative.

Body blocks, by info string:

- ` ```yaml ado ` - the complete Azure Pipelines file (the input). Vectors run `parse -> resolve -> normalize -> emit` with pipeline `id` and `name` both `ci`, source file `azure-pipelines.yml`, the lock of the repository, and default options. The reader's implicit checkout (`implicit-checkout`, "Checkout self") is therefore the first step of every job unless the vector says otherwise; the first user step of a `steps:` or `jobs:` pipeline is `step-2`, and the root `steps:` shape yields the single job `__default` named `Default` (normalize-ado.md).
- ` ```text ir-edit ` - used by vectors whose input cannot be produced by the reader (section 5.13, section 5.12): the named edits are applied to the IR produced by the stated base vector before emission.
- ` ```yaml gha full ` - the exact output, without the first header line, compared byte for byte.
- ` ```yaml gha fragment ` - a subset of the output, compared after parsing both documents: every mapping key of the fragment must exist at the same path with a matching value; the value `!absent` asserts that the key is missing; `!any` matches any value; `{}` matches any mapping; sequences must have equal length and match element by element; scalars compare by parsed YAML type and value (`5` is an integer, `'5'` a string). A step mapping with the pseudo-key `placeholder: <CODE>` expands to the placeholder of section 2.5 (`shell: bash` and the two-line `run` naming `<CODE>`); other keys in the same mapping (such as `name`) must also match.
- ` ```text prose ` - an expectation that needs a function rather than a literal (hash suffixes).

`Diagnostics` lists exactly the codes raised by the emit stage with a count when above one; diagnostics from earlier stages are ignored. "none" means no emit-stage diagnostic. `Source` uses the aliases of section 1.4; `n/a` marks a scope decision of this specification (the plan `m2.md`, section 3, "M2-A") with no vendor behavior to cite, and every such vector only asserts that a construct is not converted silently.

### 7.2 Triggers and filters

| ID | Input | Expected | Diagnostics | Source |
|---|---|---|---|---|
| EMITV-001 | `trigger: [main]`, `vmImage: ubuntu-latest`, one `script` | full golden: name, push, `contents: read`, pinned checkout, `bash` step | `TRL-CAT-0006` | AZ-TRG, GH-WS, GH-RUN, GH-CHK |
| EMITV-002 | no `trigger` key | `push.branches: ['**']`, no `pull_request` | `TRL-EMIT-0001` | AZ-TRG, GH-WS |
| EMITV-003 | `trigger: none`, `pr: none` | `on` is `workflow_dispatch:` only | `TRL-EMIT-0001` | AZ-TRG, AZ-PR, GH-WS |
| EMITV-004 | `trigger` list with a wildcard | `push.branches` in order | none | AZ-TRG, GH-WS |
| EMITV-005 | branches include and exclude | one `branches` list, `!` pattern last | none | AZ-TRG, GH-WS |
| EMITV-006 | branches exclude only | `branches-ignore`, no `branches` | none | AZ-TRG, GH-WS |
| EMITV-007 | paths include only | `branches: ['**']` plus `paths` | none | AZ-AG, GH-WS |
| EMITV-008 | paths exclude only | `branches: ['**']` plus `paths-ignore` | none | AZ-TRG, GH-WS |
| EMITV-009 | branches include, paths include and exclude | `branches`, `paths` with trailing `!` | none | AZ-TRG, GH-WS |
| EMITV-010 | `trigger: none`, `pr` branch list | `pull_request.branches` only | none | AZ-PR, GH-EV |
| EMITV-011 | `trigger` without `pr` | no `pull_request` event | none | AZ-GH, GH-EV |
| EMITV-012 | tag include and exclude | `push.tags` with `!` | none | AZ-AG, GH-WS |
| EMITV-013 | `refs/heads/` and `refs/tags/` entries | branch `main`, tag `v1` | none | AZ-TRG, GH-WS |
| EMITV-014 | pattern with `?` (include) | widened to `*` | `TRL-EMIT-0007` | GH-WS |
| EMITV-015 | pattern with `?` (exclude) | exclusion dropped | `TRL-EMIT-0007` | GH-WS |
| EMITV-016 | pattern with literal `+` | escaped `\+`, quoted | none | GH-WS |
| EMITV-017 | push and pr triggers | full golden, `push` before `pull_request` | none | GH-WS, GH-EV |
| EMITV-018 | `schedules` plus a push trigger | schedule not emitted | `TRL-CONV-0003` | GH-EV |
| EMITV-019 | `batch`, `autoCancel`, `drafts` | emitted filters only, no `concurrency` | none (losses carried) | AZ-TRG, AZ-PR |

#### EMITV-001

```yaml ado
trigger:
  - main
pool:
  vmImage: ubuntu-latest
steps:
  - script: echo hello
```

```yaml gha full
name: ci
on:
  push:
    branches:
      - main
permissions:
  contents: read
jobs:
  __default:
    name: Default
    runs-on: ubuntu-24.04
    steps:
      - name: Checkout self
        uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - shell: bash
        run: echo hello
```

#### EMITV-002

```yaml ado
pool:
  vmImage: ubuntu-22.04
steps:
  - script: echo hi
```

```yaml gha fragment
on:
  push:
    branches:
      - '**'
  pull_request: !absent
  workflow_dispatch: !absent
```

#### EMITV-003

```yaml ado
trigger: none
pr: none
pool:
  vmImage: ubuntu-22.04
steps:
  - script: echo hi
```

```yaml gha fragment
on:
  workflow_dispatch:
  push: !absent
  pull_request: !absent
```

#### EMITV-004

```yaml ado
trigger:
  - main
  - releases/*
pool:
  vmImage: ubuntu-22.04
steps:
  - script: echo hi
```

```yaml gha fragment
on:
  push:
    branches:
      - main
      - 'releases/*'
    paths: !absent
```

#### EMITV-005

```yaml ado
trigger:
  branches:
    include:
      - main
      - releases/*
    exclude:
      - releases/old*
pool:
  vmImage: ubuntu-22.04
steps:
  - script: echo hi
```

```yaml gha fragment
on:
  push:
    branches:
      - main
      - 'releases/*'
      - '!releases/old*'
    branches-ignore: !absent
```

#### EMITV-006

```yaml ado
trigger:
  branches:
    exclude:
      - wip/*
pool:
  vmImage: ubuntu-22.04
steps:
  - script: echo hi
```

```yaml gha fragment
on:
  push:
    branches-ignore:
      - 'wip/*'
    branches: !absent
```

#### EMITV-007

```yaml ado
trigger:
  paths:
    include:
      - docs/*.md
pool:
  vmImage: ubuntu-22.04
steps:
  - script: echo hi
```

```yaml gha fragment
on:
  push:
    branches:
      - '**'
    paths:
      - 'docs/*.md'
```

#### EMITV-008

```yaml ado
trigger:
  paths:
    exclude:
      - README.md
pool:
  vmImage: ubuntu-22.04
steps:
  - script: echo hi
```

```yaml gha fragment
on:
  push:
    branches:
      - '**'
    paths-ignore:
      - README.md
    paths: !absent
```

#### EMITV-009

```yaml ado
trigger:
  branches:
    include:
      - main
  paths:
    include:
      - src/**
    exclude:
      - src/docs/**
pool:
  vmImage: ubuntu-22.04
steps:
  - script: echo hi
```

```yaml gha fragment
on:
  push:
    branches:
      - main
    paths:
      - 'src/**'
      - '!src/docs/**'
```

#### EMITV-010

```yaml ado
trigger: none
pr:
  - main
  - releases/*
pool:
  vmImage: ubuntu-22.04
steps:
  - script: echo hi
```

```yaml gha fragment
on:
  pull_request:
    branches:
      - main
      - 'releases/*'
  push: !absent
  workflow_dispatch: !absent
```

#### EMITV-011

```yaml ado
trigger:
  - main
pool:
  vmImage: ubuntu-22.04
steps:
  - script: echo hi
```

```yaml gha fragment
on:
  push:
    branches:
      - main
  pull_request: !absent
```

#### EMITV-012

```yaml ado
trigger:
  branches:
    include:
      - main
  tags:
    include:
      - v*
    exclude:
      - v*-rc
pool:
  vmImage: ubuntu-22.04
steps:
  - script: echo hi
```

```yaml gha fragment
on:
  push:
    branches:
      - main
    tags:
      - 'v*'
      - '!v*-rc'
```

#### EMITV-013

```yaml ado
trigger:
  - refs/heads/main
  - refs/tags/v1
pool:
  vmImage: ubuntu-22.04
steps:
  - script: echo hi
```

```yaml gha fragment
on:
  push:
    branches:
      - main
    tags:
      - v1
```

#### EMITV-014

```yaml ado
trigger:
  - release/v?
pool:
  vmImage: ubuntu-22.04
steps:
  - script: echo hi
```

```yaml gha fragment
on:
  push:
    branches:
      - 'release/v*'
```

#### EMITV-015

```yaml ado
trigger:
  branches:
    include:
      - main
    exclude:
      - feature/?
pool:
  vmImage: ubuntu-22.04
steps:
  - script: echo hi
```

```yaml gha fragment
on:
  push:
    branches:
      - main
```

#### EMITV-016

```yaml ado
trigger:
  - c++
pool:
  vmImage: ubuntu-22.04
steps:
  - script: echo hi
```

```yaml gha fragment
on:
  push:
    branches:
      - 'c\+\+'
```

#### EMITV-017

```yaml ado
trigger:
  - main
pr:
  - main
pool:
  vmImage: ubuntu-22.04
steps:
  - script: echo hi
```

```yaml gha full
name: ci
on:
  push:
    branches:
      - main
  pull_request:
    branches:
      - main
permissions:
  contents: read
jobs:
  __default:
    name: Default
    runs-on: ubuntu-22.04
    steps:
      - name: Checkout self
        uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - shell: bash
        run: echo hi
```

#### EMITV-018

```yaml ado
trigger:
  - main
schedules:
  - cron: "0 3 * * *"
    branches:
      include:
        - main
pool:
  vmImage: ubuntu-22.04
steps:
  - script: echo hi
```

```yaml gha fragment
on:
  push:
    branches:
      - main
  schedule: !absent
```

#### EMITV-019

```yaml ado
trigger:
  batch: true
  branches:
    include:
      - main
pr:
  autoCancel: false
  drafts: false
  branches:
    include:
      - main
pool:
  vmImage: ubuntu-22.04
steps:
  - script: echo hi
```

```yaml gha fragment
on:
  push:
    branches:
      - main
  pull_request:
    branches:
      - main
concurrency: !absent
```

### 7.3 Jobs, dependency graph and runners

| ID | Input | Expected | Diagnostics | Source |
|---|---|---|---|---|
| EMITV-020 | three jobs, only the third has `dependsOn` (two entries) | no `needs` on the parallel jobs, `needs` keeps source order | none | AZ-JOBS, GH-WS |
| EMITV-021 | four stages: default chain, one with `dependsOn: []` | stage edges flattened to job `needs`; empty `dependsOn` stays parallel | `TRL-NAME-0001` x5 | AZ-JOBS, GH-WS |
| EMITV-022 | job `dependsOn` inside a stage that also depends on a stage | job edges first, then stage edges | `TRL-NAME-0001` x3 | AZ-JOBS, GH-WS |
| EMITV-023 | stage with a custom condition | every job of the stage is a placeholder | `TRL-CONV-0003`, `TRL-NAME-0001` x2 | AZ-COND, GH-WS |
| EMITV-024 | job ID starting with a digit | `N_` prefix, `needs` rewritten | `TRL-NAME-0001` | GH-WS |
| EMITV-025 | two jobs that normalize to one ID | second ID gets the hash suffix | `TRL-NAME-0001` x2, `TRL-NAME-0002` | GH-WS |
| EMITV-026 | `windows-latest` and `macOS-latest` | `windows-2025`, `macos-15-intel` | `TRL-CAT-0006` x2 | AZ-HOST, GH-RUN |
| EMITV-027 | `ubuntu-22.04`, `windows-2022` with `Azure Pipelines` pool name, `macOS-15` | same-version labels | none | AZ-HOST, GH-RUN |
| EMITV-028 | `macOS-14`, `windows-2025-vs2026` | same-version labels, approximated | `TRL-CONV-0007` x2 | AZ-HOST, GH-RUN |
| EMITV-029 | retired `vs2017-win2016` image | job placeholder on `ubuntu-24.04` | `TRL-CONV-0003` | AZ-HOST |
| EMITV-030 | private pool with demands | job placeholder | `TRL-CONV-0003` | AZ-JOBS |
| EMITV-031 | no `pool` | `ubuntu-24.04` | `TRL-CONV-0004`, `TRL-CAT-0006` | AZ-HOST, GH-RUN |
| EMITV-032 | job `displayName`, `timeoutInMinutes`, `continueOnError` | `name`, `timeout-minutes`, `continue-on-error` | none | AZ-JOBS, GH-WS |
| EMITV-033 | matrix strategy | job placeholder | `TRL-CONV-0003` | AZ-JOBS |
| EMITV-034 | container job | job placeholder | `TRL-CONV-0003` | AZ-JOBS |
| EMITV-035 | deployment job with environment | job placeholder, no `environment` | `TRL-CONV-0003` | AZ-JOBS |

#### EMITV-020

```yaml ado
trigger:
  - main
pool:
  vmImage: ubuntu-22.04
jobs:
  - job: build
    steps:
      - script: make
  - job: lint
    steps:
      - script: make lint
  - job: test
    dependsOn:
      - lint
      - build
    steps:
      - script: make test
```

```yaml gha fragment
jobs:
  build:
    needs: !absent
  lint:
    needs: !absent
  test:
    needs:
      - lint
      - build
```

#### EMITV-021

```yaml ado
trigger:
  - main
pool:
  vmImage: ubuntu-22.04
stages:
  - stage: Build
    jobs:
      - job: compile
        steps:
          - script: make
  - stage: Test
    jobs:
      - job: unit
        steps:
          - script: make unit
      - job: lint
        steps:
          - script: make lint
  - stage: Deploy
    jobs:
      - job: ship
        steps:
          - script: make ship
  - stage: Docs
    dependsOn: []
    jobs:
      - job: site
        steps:
          - script: make site
```

```yaml gha fragment
jobs:
  Build_compile:
    needs: !absent
  Test_unit:
    needs:
      - Build_compile
  Test_lint:
    needs:
      - Build_compile
  Deploy_ship:
    needs:
      - Test_unit
      - Test_lint
  Docs_site:
    needs: !absent
```

#### EMITV-022

```yaml ado
trigger:
  - main
pool:
  vmImage: ubuntu-22.04
stages:
  - stage: Build
    jobs:
      - job: compile
        steps:
          - script: make
  - stage: Test
    jobs:
      - job: lint
        steps:
          - script: make lint
      - job: unit
        dependsOn: lint
        steps:
          - script: make unit
```

```yaml gha fragment
jobs:
  Build_compile: {}
  Test_lint:
    needs:
      - Build_compile
  Test_unit:
    needs:
      - Test_lint
      - Build_compile
```

#### EMITV-023

```yaml ado
trigger:
  - main
pool:
  vmImage: ubuntu-22.04
stages:
  - stage: Build
    jobs:
      - job: compile
        steps:
          - script: make
  - stage: Publish
    condition: eq(variables['Build.SourceBranch'], 'refs/heads/main')
    jobs:
      - job: push
        steps:
          - script: make push
```

```yaml gha fragment
jobs:
  Build_compile: {}
  Publish_push:
    needs:
      - Build_compile
    if: !absent
    runs-on: ubuntu-22.04
    steps:
      - name: Manual conversion required
        placeholder: TRL-CONV-0003
```

#### EMITV-024

```yaml ado
trigger:
  - main
pool:
  vmImage: ubuntu-22.04
jobs:
  - job: 2build
    steps:
      - script: make
  - job: deploy
    dependsOn: 2build
    steps:
      - script: make deploy
```

```yaml gha fragment
jobs:
  N_2build: {}
  deploy:
    needs:
      - N_2build
```

#### EMITV-025

```yaml ado
trigger:
  - main
pool:
  vmImage: ubuntu-22.04
stages:
  - stage: a
    jobs:
      - job: b_c
        steps:
          - script: echo one
  - stage: a_b
    dependsOn: []
    jobs:
      - job: c
        steps:
          - script: echo two
```

```text prose
IR job IDs are "a.b_c" and "a_b.c"; both normalize to "a_b_c". After sorting by stable ID in unsigned UTF-8 bytes, "a.b_c" keeps "a_b_c" and "a_b.c" becomes "a_b_c--" followed by the 8-character lower-case base32 token that naming.md defines (SHA-256 over kind NUL stableId). The output has exactly two jobs with those two keys; no other key collides.
```

#### EMITV-026

```yaml ado
trigger:
  - main
jobs:
  - job: win
    pool:
      vmImage: windows-latest
    steps:
      - script: echo w
  - job: mac
    pool:
      vmImage: macOS-latest
    steps:
      - script: echo m
```

```yaml gha fragment
jobs:
  win:
    runs-on: windows-2025
  mac:
    runs-on: macos-15-intel
```

#### EMITV-027

```yaml ado
trigger:
  - main
jobs:
  - job: lin
    pool:
      vmImage: ubuntu-22.04
    steps:
      - script: echo l
  - job: win
    pool:
      name: Azure Pipelines
      vmImage: windows-2022
    steps:
      - script: echo w
  - job: mac
    pool:
      vmImage: macOS-15
    steps:
      - script: echo m
```

```yaml gha fragment
jobs:
  lin:
    runs-on: ubuntu-22.04
  win:
    runs-on: windows-2022
  mac:
    runs-on: macos-15-intel
```

#### EMITV-028

```yaml ado
trigger:
  - main
jobs:
  - job: old
    pool:
      vmImage: macOS-14
    steps:
      - script: echo m
  - job: vs
    pool:
      vmImage: windows-2025-vs2026
    steps:
      - script: echo w
```

```yaml gha fragment
jobs:
  old:
    runs-on: macos-14
  vs:
    runs-on: windows-2025-vs2026
```

#### EMITV-029

```yaml ado
trigger:
  - main
jobs:
  - job: legacy
    pool:
      vmImage: vs2017-win2016
    steps:
      - script: echo hi
```

```yaml gha fragment
jobs:
  legacy:
    runs-on: ubuntu-24.04
    steps:
      - name: Manual conversion required
        placeholder: TRL-CONV-0003
```

#### EMITV-030

```yaml ado
trigger:
  - main
jobs:
  - job: onprem
    pool:
      name: BuildFarm
      demands:
        - Agent.OS -equals Linux
    steps:
      - script: echo hi
```

```yaml gha fragment
jobs:
  onprem:
    runs-on: ubuntu-24.04
    steps:
      - name: Manual conversion required
        placeholder: TRL-CONV-0003
```

#### EMITV-031

```yaml ado
trigger:
  - main
steps:
  - script: echo hi
```

```yaml gha fragment
jobs:
  __default:
    runs-on: ubuntu-24.04
```

#### EMITV-032

```yaml ado
trigger:
  - main
pool:
  vmImage: ubuntu-22.04
jobs:
  - job: build
    displayName: Build it
    timeoutInMinutes: 30
    continueOnError: true
    steps:
      - script: make
```

```yaml gha fragment
jobs:
  build:
    name: Build it
    timeout-minutes: 30
    continue-on-error: true
```

#### EMITV-033

```yaml ado
trigger:
  - main
pool:
  vmImage: ubuntu-22.04
jobs:
  - job: t
    strategy:
      matrix:
        one:
          mode: a
        two:
          mode: b
    steps:
      - script: echo $(mode)
```

```yaml gha fragment
jobs:
  t:
    strategy: !absent
    steps:
      - name: Manual conversion required
        placeholder: TRL-CONV-0003
```

#### EMITV-034

```yaml ado
trigger:
  - main
pool:
  vmImage: ubuntu-22.04
jobs:
  - job: boxed
    container: ubuntu:22.04
    steps:
      - script: echo hi
```

```yaml gha fragment
jobs:
  boxed:
    container: !absent
    steps:
      - name: Manual conversion required
        placeholder: TRL-CONV-0003
```

#### EMITV-035

```yaml ado
trigger:
  - main
pool:
  vmImage: ubuntu-22.04
jobs:
  - deployment: ship
    environment: prod
    strategy:
      runOnce:
        deploy:
          steps:
            - script: echo deploy
```

```yaml gha fragment
jobs:
  ship:
    environment: !absent
    steps:
      - name: Manual conversion required
        placeholder: TRL-CONV-0003
```

### 7.4 Steps, shells and checkout

| ID | Input | Expected | Diagnostics | Source |
|---|---|---|---|---|
| EMITV-036 | `script` on Linux, Windows and macOS images | shell `bash`, `cmd`, `bash` | none | AZ-SCR, GH-WS |
| EMITV-037 | `bash`, `pwsh`, `powershell` steps | the same shell names | none | AZ-BASH, GH-WS |
| EMITV-038 | multi-line script (`\|`) | full golden with a clip block scalar | none | GH-WS |
| EMITV-039 | multi-line script (`\|-`) | full golden with a strip block scalar | none | GH-WS |
| EMITV-040 | `workingDirectory` | `working-directory` | none | AZ-SCR, GH-WS |
| EMITV-041 | `retryCountOnTaskFailure: 2` | step emitted, no retry | `TRL-CONV-0001` | GH-WS |
| EMITV-042 | step `timeoutInMinutes`, `continueOnError`, `env` | `timeout-minutes`, `continue-on-error`, `env` | none | AZ-SCR, GH-WS |
| EMITV-043 | `task: Npm@1` with `displayName` | named step placeholder | `TRL-CAT-0001` | n/a (catalog is M2-C) |
| EMITV-044 | `publish` and `download` steps | two step placeholders | `TRL-CONV-0003` x2 | n/a |
| EMITV-045 | `checkout: none` | no `uses`; `permissions` still present | none | AZ-CHK, GH-WS |
| EMITV-046 | `checkout: self` with depth, submodules, lfs, persist | `with` keys written | none | AZ-CHK, GH-CHK |
| EMITV-047 | `checkout` with `path`, and of another repository | two step placeholders | `TRL-CONV-0003` x2 | AZ-CHK |
| EMITV-048 | script emitting a `##vso` logging command | step placeholder | `TRL-CONV-0003` | n/a |
| EMITV-049 | Azure macros in a script and a working directory; shell `$(date)` | macro steps are placeholders; `$(date)` unchanged | `TRL-CONV-0003` x2 | AZ-EXPR |
| EMITV-050 | `enabled: false` | `if: ${{ false }}` | none | GH-EX |

#### EMITV-036

```yaml ado
trigger:
  - main
jobs:
  - job: lin
    pool:
      vmImage: ubuntu-22.04
    steps:
      - script: echo l
  - job: win
    pool:
      vmImage: windows-2022
    steps:
      - script: echo w
  - job: mac
    pool:
      vmImage: macOS-15
    steps:
      - script: echo m
```

```yaml gha fragment
jobs:
  lin:
    steps:
      - {}
      - shell: bash
        run: echo l
  win:
    steps:
      - {}
      - shell: cmd
        run: echo w
  mac:
    steps:
      - {}
      - shell: bash
        run: echo m
```

#### EMITV-037

```yaml ado
trigger:
  - main
jobs:
  - job: win
    pool:
      vmImage: windows-2022
    steps:
      - bash: echo b
      - pwsh: Write-Host p
      - powershell: Write-Host w
```

```yaml gha fragment
jobs:
  win:
    steps:
      - {}
      - shell: bash
        run: echo b
      - shell: pwsh
        run: Write-Host p
      - shell: powershell
        run: Write-Host w
```

#### EMITV-038

```yaml ado
trigger:
  - main
pool:
  vmImage: ubuntu-22.04
steps:
  - script: |
      set -e
      echo one
      echo two
```

```yaml gha full
name: ci
on:
  push:
    branches:
      - main
permissions:
  contents: read
jobs:
  __default:
    name: Default
    runs-on: ubuntu-22.04
    steps:
      - name: Checkout self
        uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - shell: bash
        run: |
          set -e
          echo one
          echo two
```

#### EMITV-039

```yaml ado
trigger:
  - main
pool:
  vmImage: ubuntu-22.04
steps:
  - script: |-
      echo one
      echo two
```

```yaml gha full
name: ci
on:
  push:
    branches:
      - main
permissions:
  contents: read
jobs:
  __default:
    name: Default
    runs-on: ubuntu-22.04
    steps:
      - name: Checkout self
        uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - shell: bash
        run: |-
          echo one
          echo two
```

#### EMITV-040

```yaml ado
trigger:
  - main
pool:
  vmImage: ubuntu-22.04
steps:
  - script: make
    workingDirectory: src/app
```

```yaml gha fragment
jobs:
  __default:
    steps:
      - {}
      - shell: bash
        working-directory: src/app
        run: make
```

#### EMITV-041

```yaml ado
trigger:
  - main
pool:
  vmImage: ubuntu-22.04
steps:
  - script: ./flaky.sh
    retryCountOnTaskFailure: 2
```

```yaml gha fragment
jobs:
  __default:
    steps:
      - {}
      - shell: bash
        run: ./flaky.sh
```

#### EMITV-042

```yaml ado
trigger:
  - main
pool:
  vmImage: ubuntu-22.04
steps:
  - script: make
    timeoutInMinutes: 5
    continueOnError: true
    env:
      MODE: fast
```

```yaml gha fragment
jobs:
  __default:
    steps:
      - {}
      - shell: bash
        env:
          MODE: fast
        timeout-minutes: 5
        continue-on-error: true
        run: make
```

#### EMITV-043

```yaml ado
trigger:
  - main
pool:
  vmImage: ubuntu-22.04
steps:
  - task: Npm@1
    displayName: npm install
    inputs:
      command: install
```

```yaml gha fragment
jobs:
  __default:
    steps:
      - {}
      - name: npm install
        placeholder: TRL-CAT-0001
```

#### EMITV-044

```yaml ado
trigger:
  - main
pool:
  vmImage: ubuntu-22.04
steps:
  - publish: out
    artifact: drop
  - download: current
    artifact: drop
```

```yaml gha fragment
jobs:
  __default:
    steps:
      - {}
      - placeholder: TRL-CONV-0003
      - placeholder: TRL-CONV-0003
```

#### EMITV-045

```yaml ado
trigger:
  - main
pool:
  vmImage: ubuntu-22.04
steps:
  - checkout: none
  - script: echo hi
```

```yaml gha fragment
permissions:
  contents: read
jobs:
  __default:
    steps:
      - shell: bash
        run: echo hi
        uses: !absent
```

#### EMITV-046

```yaml ado
trigger:
  - main
pool:
  vmImage: ubuntu-22.04
steps:
  - checkout: self
    fetchDepth: 5
    submodules: true
    lfs: true
    persistCredentials: true
```

```yaml gha fragment
jobs:
  __default:
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1
        with:
          ref: !absent
          fetch-depth: 5
          submodules: true
          lfs: true
          persist-credentials: true
```

#### EMITV-047

```yaml ado
trigger:
  - main
pool:
  vmImage: ubuntu-22.04
resources:
  repositories:
    - repository: tools
      type: git
      name: proj/tools
steps:
  - checkout: self
    path: src
  - checkout: tools
  - script: echo hi
```

```yaml gha fragment
jobs:
  __default:
    steps:
      - placeholder: TRL-CONV-0003
      - placeholder: TRL-CONV-0003
      - shell: bash
        run: echo hi
```

#### EMITV-048

```yaml ado
trigger:
  - main
pool:
  vmImage: ubuntu-22.04
steps:
  - bash: |
      echo "##vso[task.setvariable variable=foo]bar"
```

```yaml gha fragment
jobs:
  __default:
    steps:
      - {}
      - placeholder: TRL-CONV-0003
```

#### EMITV-049

```yaml ado
trigger:
  - main
pool:
  vmImage: ubuntu-22.04
steps:
  - script: echo $(Build.BuildId)
  - script: echo "now $(date)"
  - script: make
    workingDirectory: $(Build.SourcesDirectory)/src
```

```yaml gha fragment
jobs:
  __default:
    steps:
      - {}
      - placeholder: TRL-CONV-0003
      - shell: bash
        run: echo "now $(date)"
      - placeholder: TRL-CONV-0003
```

#### EMITV-050

```yaml ado
trigger:
  - main
pool:
  vmImage: ubuntu-22.04
steps:
  - script: echo off
    enabled: false
```

```yaml gha fragment
jobs:
  __default:
    steps:
      - {}
      - if: '${{ false }}'
        shell: bash
        run: echo off
```

### 7.5 Variables, environment and secrets

| ID | Input | Expected | Diagnostics | Source |
|---|---|---|---|---|
| EMITV-051 | pipeline variables with dots and spaces | workflow `env` with Azure's projected names | none | AZ-JOBS, GH-WS |
| EMITV-052 | stage variables and job variables overriding one | job `env` with stage entries first, job value wins | none | AZ-JOBS |
| EMITV-053 | step `env` with a secret-looking literal (canary value) | `${{ secrets.DB_PASSWORD }}`, canary absent | `TRL-EMIT-0008` | AZ-SEC, GH-SEC |
| EMITV-054 | step `env: FOO_TWO: $(SecretTwo)` | `${{ secrets.SECRETTWO }}` | `TRL-CONV-0001`, `TRL-NAME-0001`, `TRL-EMIT-0008` | AZ-SEC, GH-SEC |
| EMITV-055 | step `env` with `$(Build.BuildId)` | step placeholder | `TRL-CONV-0003` | AZ-EXPR |
| EMITV-056 | variable group import and a variable whose value is a macro | both dropped, other variable emitted | `TRL-CONV-0003` x2 | AZ-JOBS |
| EMITV-057 | step `env` values that YAML could reinterpret | full golden with every value quoted | none | Y12, GH-WS |
| EMITV-058 | step `env` values with YAML indicators and quotes | full golden, style rules 2 and 3 | none | GH-WS |
| EMITV-059 | variable named `no` | key `'NO'` quoted | none | Y12 |
| EMITV-060 | `a.b` and `A_B` in one scope | one key, later value | `TRL-VAR-0004` | AZ-JOBS |

#### EMITV-051

```yaml ado
trigger:
  - main
variables:
  mySimpleVar: simple var value
  my.dotted.var: dotted var value
  my var with spaces: spaced value
pool:
  vmImage: ubuntu-22.04
steps:
  - script: echo $MYSIMPLEVAR
```

```yaml gha fragment
env:
  MYSIMPLEVAR: simple var value
  MY_DOTTED_VAR: dotted var value
  MY_VAR_WITH_SPACES: spaced value
```

#### EMITV-052

```yaml ado
trigger:
  - main
pool:
  vmImage: ubuntu-22.04
stages:
  - stage: S
    variables:
      stageVar: s
      shared: from-stage
    jobs:
      - job: j
        variables:
          jobVar: j
          shared: from-job
        steps:
          - script: echo hi
```

```yaml gha fragment
env: !absent
jobs:
  S_j:
    env:
      STAGEVAR: s
      SHARED: from-job
      JOBVAR: j
```

```text prose
Key order inside the job env is STAGEVAR, SHARED, JOBVAR: stage entries in declaration order, then job entries; the job value replaces SHARED in place without moving it.
```

#### EMITV-053

```yaml ado
trigger:
  - main
pool:
  vmImage: ubuntu-22.04
steps:
  - script: ./deploy.sh
    env:
      DB_PASSWORD: s3cr3t-canary-0001
```

```yaml gha fragment
jobs:
  __default:
    steps:
      - {}
      - shell: bash
        env:
          DB_PASSWORD: '${{ secrets.DB_PASSWORD }}'
        run: ./deploy.sh
```

```text prose
The output bytes, every diagnostic message and parameter, every disposition and requiredSecrets contain no occurrence of "s3cr3t-canary-0001" (nor its lower-case, upper-case or JSON-escaped form). requiredSecrets is exactly ["DB_PASSWORD"].
```

#### EMITV-054

```yaml ado
trigger:
  - main
pool:
  vmImage: ubuntu-22.04
steps:
  - bash: echo "length ${#FOO_TWO}"
    env:
      FOO_TWO: $(SecretTwo)
```

```yaml gha fragment
jobs:
  __default:
    steps:
      - {}
      - shell: bash
        env:
          FOO_TWO: '${{ secrets.SECRETTWO }}'
```

```text prose
requiredSecrets is exactly ["SECRETTWO"]. The env key FOO_TWO keeps its spelling; the secret name is the naming.md GitHub-secret normalization of "SecretTwo".
```

#### EMITV-055

```yaml ado
trigger:
  - main
pool:
  vmImage: ubuntu-22.04
steps:
  - script: ./build.sh
    env:
      BUILD: $(Build.BuildId)
```

```yaml gha fragment
jobs:
  __default:
    steps:
      - {}
      - placeholder: TRL-CONV-0003
```

#### EMITV-056

```yaml ado
trigger:
  - main
variables:
  - group: shared-settings
  - name: derived
    value: $(other)
  - name: plain
    value: ok
pool:
  vmImage: ubuntu-22.04
steps:
  - script: echo hi
```

```yaml gha fragment
env:
  PLAIN: ok
  DERIVED: !absent
```

#### EMITV-057

```yaml ado
trigger:
  - main
pool:
  vmImage: ubuntu-22.04
steps:
  - script: echo hi
    env:
      A: "yes"
      B: "on"
      C: "1e3"
      D: "0x10"
      E: "007"
      F: "null"
      G: "~"
      H: ""
      I: "true"
      J: "1_000"
      K: "2024-01-01"
```

```yaml gha full
name: ci
on:
  push:
    branches:
      - main
permissions:
  contents: read
jobs:
  __default:
    name: Default
    runs-on: ubuntu-22.04
    steps:
      - name: Checkout self
        uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - shell: bash
        env:
          A: 'yes'
          B: 'on'
          C: '1e3'
          D: '0x10'
          E: '007'
          F: 'null'
          G: '~'
          H: ''
          I: 'true'
          J: '1_000'
          K: '2024-01-01'
        run: echo hi
```

#### EMITV-058

```yaml ado
trigger:
  - main
pool:
  vmImage: ubuntu-22.04
steps:
  - script: echo hi
    env:
      K1: "*star"
      K2: "a: b"
      K3: "a #b"
      K4: "@at"
      K5: "%pct"
      K6: "!bang"
      K7: "- dash"
      K8: "it's"
      K9: 'say "hi"'
      K10: "it's \"x\""
```

```yaml gha full
name: ci
on:
  push:
    branches:
      - main
permissions:
  contents: read
jobs:
  __default:
    name: Default
    runs-on: ubuntu-22.04
    steps:
      - name: Checkout self
        uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - shell: bash
        env:
          K1: '*star'
          K2: 'a: b'
          K3: 'a #b'
          K4: '@at'
          K5: '%pct'
          K6: '!bang'
          K7: '- dash'
          K8: "it's"
          K9: 'say "hi"'
          K10: 'it''s "x"'
        run: echo hi
```

#### EMITV-059

```yaml ado
trigger:
  - main
variables:
  no: x
pool:
  vmImage: ubuntu-22.04
steps:
  - script: echo hi
```

```yaml gha full
name: ci
on:
  push:
    branches:
      - main
permissions:
  contents: read
env:
  'NO': x
jobs:
  __default:
    name: Default
    runs-on: ubuntu-22.04
    steps:
      - name: Checkout self
        uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - shell: bash
        run: echo hi
```

#### EMITV-060

```yaml ado
trigger:
  - main
variables:
  a.b: one
  A_B: two
pool:
  vmImage: ubuntu-22.04
steps:
  - script: echo hi
```

```yaml gha fragment
env:
  A_B: two
```

### 7.6 Conditions

All vectors of this group use the header `trigger: [main]`, `pool: vmImage: ubuntu-22.04`, the declared variables shown, and one or more `script` steps carrying the stated `condition`. The checkout step is `{}` in the fragments.

| ID | Input | Expected | Diagnostics | Source |
|---|---|---|---|---|
| EMITV-061 | `succeeded()`, `always()`, `failed()`, `canceled()` | no `if`, `always()`, `failure()`, `cancelled()` | none | AZ-EXPR, GH-EX |
| EMITV-062 | `eq(variables.mode, 'fast')` (no status function) | `always() && (env.MODE == 'fast')` | none | AZ-COND, GH-EX, GH-CX |
| EMITV-063 | `and(succeeded(), eq(...))` | `success() && (env.MODE == 'fast')` | none | AZ-COND, GH-EX |
| EMITV-064 | `ne` with index syntax and `not(eq(...))` | `!=` and `!( ... )` | none | AZ-EXPR, GH-EX |
| EMITV-065 | `or(failed(), and(...))` | parenthesized nested `&&` | none | AZ-EXPR, GH-EX |
| EMITV-066 | `eq(variables.flag, true)` and `eq(variables.count, 1)` | literals written as strings | none | AZ-EXPR, GH-EX |
| EMITV-067 | `contains` and `startsWith` | same functions over `env` | none | AZ-EXPR, GH-EX |
| EMITV-068 | `lt(variables.count, 3)` | step placeholder | `TRL-EXPR-0002` | AZ-EXPR, GH-EX |
| EMITV-069 | predefined `Build.SourceBranch`; undeclared variable | two step placeholders | `TRL-EXPR-0002` x2 | AZ-COND, GH-CX |
| EMITV-070 | `in(...)`; `succeededOrFailed()` | two step placeholders | `TRL-EXPR-0004` x2 | AZ-EXPR |
| EMITV-071 | string literal containing a quote | doubled quote inside a double-quoted scalar | none | GH-EX |
| EMITV-072 | `condition: true`; `condition: false` | `always()`; `false` | none | AZ-COND, GH-EX |
| EMITV-073 | job conditions `failed()`, `always()`, `succeeded()` | `failure()`, `always()`, no `if` | none | AZ-COND, GH-EX |
| EMITV-074 | job condition on a variable | job placeholder (`env` unavailable) | `TRL-EXPR-0002` | GH-CX |
| EMITV-075 | job condition `failed('a')` | job placeholder | `TRL-EXPR-0002` | AZ-EXPR, GH-EX |

#### EMITV-061

```yaml ado
trigger:
  - main
pool:
  vmImage: ubuntu-22.04
steps:
  - script: echo a
    condition: succeeded()
  - script: echo b
    condition: always()
  - script: echo c
    condition: failed()
  - script: echo d
    condition: canceled()
```

```yaml gha fragment
jobs:
  __default:
    steps:
      - {}
      - shell: bash
        if: !absent
      - if: '${{ always() }}'
      - if: '${{ failure() }}'
      - if: '${{ cancelled() }}'
```

#### EMITV-062

```yaml ado
trigger:
  - main
variables:
  mode: fast
pool:
  vmImage: ubuntu-22.04
steps:
  - script: echo a
    condition: eq(variables.mode, 'fast')
```

```yaml gha fragment
jobs:
  __default:
    steps:
      - {}
      - if: "${{ always() && (env.MODE == 'fast') }}"
```

#### EMITV-063

```yaml ado
trigger:
  - main
variables:
  mode: fast
pool:
  vmImage: ubuntu-22.04
steps:
  - script: echo a
    condition: and(succeeded(), eq(variables.mode, 'fast'))
```

```yaml gha fragment
jobs:
  __default:
    steps:
      - {}
      - if: "${{ success() && (env.MODE == 'fast') }}"
```

#### EMITV-064

```yaml ado
trigger:
  - main
variables:
  mode: fast
pool:
  vmImage: ubuntu-22.04
steps:
  - script: echo a
    condition: and(succeeded(), ne(variables['mode'], 'slow'), not(eq(variables.mode, 'x')))
```

```yaml gha fragment
jobs:
  __default:
    steps:
      - {}
      - if: "${{ success() && (env.MODE != 'slow') && !(env.MODE == 'x') }}"
```

#### EMITV-065

```yaml ado
trigger:
  - main
variables:
  mode: fast
pool:
  vmImage: ubuntu-22.04
steps:
  - script: echo a
    condition: or(failed(), and(succeeded(), eq(variables.mode, 'fast')))
```

```yaml gha fragment
jobs:
  __default:
    steps:
      - {}
      - if: "${{ failure() || (success() && (env.MODE == 'fast')) }}"
```

#### EMITV-066

```yaml ado
trigger:
  - main
variables:
  flag: "true"
  count: "1"
pool:
  vmImage: ubuntu-22.04
steps:
  - script: echo a
    condition: and(succeeded(), eq(variables.flag, true))
  - script: echo b
    condition: and(succeeded(), eq(variables.count, 1))
```

```yaml gha fragment
env:
  FLAG: 'true'
  COUNT: '1'
jobs:
  __default:
    steps:
      - {}
      - if: "${{ success() && (env.FLAG == 'true') }}"
      - if: "${{ success() && (env.COUNT == '1') }}"
```

#### EMITV-067

```yaml ado
trigger:
  - main
variables:
  mode: fast
pool:
  vmImage: ubuntu-22.04
steps:
  - script: echo a
    condition: and(succeeded(), contains(variables.mode, 'fa'))
  - script: echo b
    condition: and(succeeded(), startsWith(variables.mode, 'f'))
```

```yaml gha fragment
jobs:
  __default:
    steps:
      - {}
      - if: "${{ success() && contains(env.MODE, 'fa') }}"
      - if: "${{ success() && startsWith(env.MODE, 'f') }}"
```

#### EMITV-068

```yaml ado
trigger:
  - main
variables:
  count: "1"
pool:
  vmImage: ubuntu-22.04
steps:
  - script: echo a
    condition: and(succeeded(), lt(variables.count, 3))
```

```yaml gha fragment
jobs:
  __default:
    steps:
      - {}
      - placeholder: TRL-EXPR-0002
```

#### EMITV-069

```yaml ado
trigger:
  - main
pool:
  vmImage: ubuntu-22.04
steps:
  - script: echo a
    condition: eq(variables['Build.SourceBranch'], 'refs/heads/main')
  - script: echo b
    condition: and(succeeded(), eq(variables.nosuch, 'a'))
```

```yaml gha fragment
jobs:
  __default:
    steps:
      - {}
      - placeholder: TRL-EXPR-0002
      - placeholder: TRL-EXPR-0002
```

#### EMITV-070

```yaml ado
trigger:
  - main
variables:
  mode: fast
pool:
  vmImage: ubuntu-22.04
steps:
  - script: echo a
    condition: in(variables.mode, 'fast', 'slow')
  - script: echo b
    condition: succeededOrFailed()
```

```yaml gha fragment
jobs:
  __default:
    steps:
      - {}
      - placeholder: TRL-EXPR-0004
      - placeholder: TRL-EXPR-0004
```

#### EMITV-071

```yaml ado
trigger:
  - main
variables:
  msg: "it's"
pool:
  vmImage: ubuntu-22.04
steps:
  - script: echo a
    condition: and(succeeded(), eq(variables.msg, 'it''s'))
```

```yaml gha fragment
env:
  MSG: "it's"
jobs:
  __default:
    steps:
      - {}
      - if: "${{ success() && (env.MSG == 'it''s') }}"
```

#### EMITV-072

```yaml ado
trigger:
  - main
pool:
  vmImage: ubuntu-22.04
steps:
  - script: echo a
    condition: true
  - script: echo b
    condition: false
```

```yaml gha fragment
jobs:
  __default:
    steps:
      - {}
      - if: '${{ always() }}'
      - if: '${{ false }}'
```

#### EMITV-073

```yaml ado
trigger:
  - main
pool:
  vmImage: ubuntu-22.04
jobs:
  - job: a
    steps:
      - script: echo a
  - job: b
    dependsOn: a
    condition: failed()
    steps:
      - script: echo b
  - job: c
    dependsOn: a
    condition: always()
    steps:
      - script: echo c
  - job: d
    dependsOn: a
    condition: succeeded()
    steps:
      - script: echo d
```

```yaml gha fragment
jobs:
  b:
    needs:
      - a
    if: '${{ failure() }}'
  c:
    needs:
      - a
    if: '${{ always() }}'
  d:
    needs:
      - a
    if: !absent
```

#### EMITV-074

```yaml ado
trigger:
  - main
variables:
  mode: fast
pool:
  vmImage: ubuntu-22.04
jobs:
  - job: a
    condition: eq(variables.mode, 'fast')
    steps:
      - script: echo a
```

```yaml gha fragment
jobs:
  a:
    if: !absent
    steps:
      - name: Manual conversion required
        placeholder: TRL-EXPR-0002
```

#### EMITV-075

```yaml ado
trigger:
  - main
pool:
  vmImage: ubuntu-22.04
jobs:
  - job: a
    steps:
      - script: echo a
  - job: b
    dependsOn: a
    condition: failed('a')
    steps:
      - script: echo b
```

```yaml gha fragment
jobs:
  b:
    needs:
      - a
    if: !absent
    steps:
      - name: Manual conversion required
        placeholder: TRL-EXPR-0002
```

### 7.7 Security and global properties

For `ir-edit` vectors the diagnostics column lists only the codes added by the edit; the base vector's own codes still apply. Property vectors (`EMITV-080`..`EMITV-083`) are executed over the other vectors and over the corpus goldens.

| ID | Input | Expected | Diagnostics | Source |
|---|---|---|---|---|
| EMITV-076 | IR edit: job grants `pull-requests: write`, no approval | grant not emitted | `TRL-EMIT-0003` | GH-WS, GH-SEC |
| EMITV-077 | same edit with approval | job `permissions` lists `contents: read` then the grant | `TRL-EMIT-0003` | GH-WS |
| EMITV-078 | IR edit: script holds an expression with event data | step placeholder, no `${{` anywhere | `TRL-EMIT-0006` | GH-INJ, GH-SEC |
| EMITV-079 | IR edits: `${{` in a step name, a step env value and a variable value | two placeholders and one dropped variable | `TRL-EMIT-0006` x3 | GH-INJ |
| EMITV-080 | property: secret canaries | no canary in any output | n/a | GH-SEC, AZ-SEC |
| EMITV-081 | property: pinned actions | every `uses` is SHA-pinned with a version comment | n/a | GH-SEC |
| EMITV-082 | property: determinism | byte-identical output | n/a | n/a |
| EMITV-083 | property: oracles and conservation | schema, actionlint, round trip, coverage | n/a | n/a |

#### EMITV-076

```text ir-edit
base: EMITV-001
edit: set jobs[0].permissions to the ordered object {"pull-requests": "write"}
options: AllowPermissionWidening = false
```

```yaml gha fragment
permissions:
  contents: read
jobs:
  __default:
    permissions: !absent
```

#### EMITV-077

```text ir-edit
base: EMITV-001
edit: set jobs[0].permissions to the ordered object {"pull-requests": "write"}
options: AllowPermissionWidening = true
```

```yaml gha fragment
permissions:
  contents: read
jobs:
  __default:
    permissions:
      contents: read
      pull-requests: write
```

```text prose
The job-level mapping lists contents first, then pull-requests, in that order. The top-level block is unchanged.
```

#### EMITV-078

```text ir-edit
base: EMITV-001
edit: set the run script of step "step-2" to the text: echo "${{ github.event.pull_request.title }}"
```

```yaml gha fragment
jobs:
  __default:
    steps:
      - {}
      - placeholder: TRL-EMIT-0006
```

```text prose
The output contains neither "github.event" nor the two-character sequence "${{" anywhere; the placeholder message is the fixed text of section 2.5.
```

#### EMITV-079

```yaml ado
trigger:
  - main
variables:
  keep: ok
  V: placeholder-value
pool:
  vmImage: ubuntu-22.04
steps:
  - script: echo a
  - script: echo b
    env:
      X: plain
```

```text ir-edit
base: this vector
edit 1: set the name of step "step-2" to: deploy ${{ vars.TARGET }}
edit 2: set the env value of X in step "step-3" to the string literal: ${{ secrets.Y }}
edit 3: set the value of pipeline variable V to the string literal: ${{ github.actor }}
```

```yaml gha fragment
env:
  KEEP: ok
  V: !absent
jobs:
  __default:
    steps:
      - {}
      - placeholder: TRL-EMIT-0006
        name: !absent
      - placeholder: TRL-EMIT-0006
```

#### EMITV-080

```text prose
Property over every vector and over corpus pipelines that contain planted canary secrets (ir.md invariant 7): for each canary value the output bytes, every diagnostic (message and params), every disposition and requiredSecrets contain neither the value nor its lower-case, upper-case or JSON-escaped form. Secret references occur only as the value of a step-level env entry, written ${{ secrets.NAME }}; no workflow-level or job-level env entry, run text, with, name or if contains one.
```

#### EMITV-081

```text prose
Property over every vector that emits a uses line: the value matches ^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+@[0-9a-f]{40}$, is followed by " # " and a version, and both the SHA and the version equal the entry of catalog/github-actions/actions.lock.yaml for that action name. A lock entry whose runsUsing is not node24, composite or docker is never used. Removing actions/checkout from the lock yields TRL-CAT-0003 and no uses line.
```

#### EMITV-082

```text prose
Property: emitting the same IR twice in one process, in a fresh process, with different GOMAXPROCS values, with ordered maps built through different insertion histories that compare equal, and on Linux, macOS and Windows runners produces identical bytes, identical diagnostics in identical order, and identical dispositions. The file uses LF line endings and ends with exactly one LF.
```

#### EMITV-083

```text prose
Property over every vector: (1) the output validates against the SchemaStore GitHub workflow schema and actionlint reports nothing (INV-10); (2) re-parsing the output yields the job set, the needs graph, the step count and order, the env maps and the presence of every if that the dispositions announce, with nothing extra; (3) every pipeline, trigger, variable, stage, job and step of the IR has exactly one disposition and every output job and step maps back to one IR node (INV-12).
```

## 8. Unconfirmed platform facts

Each item is tracked as an issue. Until closed, the implementation treats the stated choice as the rule (`CONVENTIONS.md`, "AI agent rules"); vectors that depend on an item name it.

| ID | Fact to confirm | Choice made here | Vectors |
|---|---|---|---|
| VERIFY-01 | Whether Azure `*` in branch/path filters matches `/` ("zero or more characters", AZ-GH) | Follow the IR dialect (`*` stops at `/`); risk of under-triggering for `releases/*` | EMITV-004, 005 |
| VERIFY-02 | Whether Azure accepts `refs/heads/<name>` as a branch filter spelling equal to `<name>` | Strip the prefix | EMITV-013 |
| VERIFY-03 | Whether an Azure trigger with `tags` and no `branches` also fires for branch pushes | Add the `'**'` branch guard (fires for both) | EMITV-012 |
| VERIFY-04 | Azure path-filter model (order-free, "exclude cannot be re-included unless deeper") against GitHub's ordered model | Includes before excludes, as the reader produces | EMITV-009 |
| VERIFY-05 | Whether a step's own `env` values are visible in its `if` (GH-CX lists `env` without saying) | Only workflow and job `env` are referenced | EMITV-062 |
| VERIFY-06 | Azure `eq` with a boolean/number right operand: AZ-EXPR states both "converts the right operand to the left type" and "implicitly converted to numbers" | Compare strings, literal written as `'true'`/`'1'` | EMITV-066 |
| VERIFY-07 | Whether Azure's Bash steps run with `errexit`/`pipefail` (GitHub's `bash` does) | Grade `exact`, no diagnostic | EMITV-036 |
| VERIFY-08 | PowerShell `ErrorActionPreference` and exit-code handling on both platforms | Grade `exact`, no diagnostic | EMITV-037 |
| VERIFY-09 | Result seen by dependents of a job with `continue-on-error` (GitHub `needs.<id>.result`) against Azure's "succeeded with issues" | Grade `approximated` without a diagnostic | EMITV-032 |
| VERIFY-10 | Architecture of Azure macOS images (Intel per AZ-HOST) and the lifetime of GitHub's `-intel` labels | `macos-<n>-intel` for 15 and 26 | EMITV-026, 027, 028 |
| VERIFY-11 | `windows-2025-vs2026` availability only for public repositories (GH-RUN) | Emit it with `TRL-CONV-0007` | EMITV-028 |
| VERIFY-12 | Azure default job timeout (60 minutes on hosted agents for private projects, AZ-JOBS) against GitHub's 360 | Absent stays absent | EMITV-032 |
| VERIFY-13 | Azure's default checkout depth (a pipeline setting, AZ-CHK) | Omit `fetch-depth` (action default 1) | EMITV-046 |
| VERIFY-14 | `submodules: recursive` is folded to `true` by the reader (normalize-ado.md) | Emit `true` | EMITV-046 |
| VERIFY-15 | That GitHub offers no escape for `${{` in `run`, `env`, `name` | Never emit such literals | EMITV-078, 079 |
| VERIFY-16 | Reservation of `GITHUB_*` names for workflow `env` | Written unchanged | none |
| VERIFY-17 | Azure's environment projection of variable names containing characters other than letters, digits, `.`, space, `_` (for example `-`) | Replace with `_`, `TRL-NAME-0001` | none |
| VERIFY-18 | Case-insensitivity of Azure variable names and of the GitHub `env` context property names | Compare after upper-casing | EMITV-062 |
| VERIFY-19 | Which YAML version GitHub's workflow parser applies | Quote for both 1.1 and 1.2 | EMITV-057, 059 |
| VERIFY-20 | Azure's PR activity set against GitHub's default (`opened`, `synchronize`, `reopened`) | Default types | EMITV-010 |
| VERIFY-21 | That `contents: read` is sufficient for `actions/checkout` of the same repository in every trigger of M2-A | Assumed sufficient | EMITV-001 |
| VERIFY-22 | That Azure leaves an undefined `$(name)` macro unchanged in a script | `$(date)`-style text with no dot and no declared variable is kept | EMITV-049 |

## 9. Proposed diagnostic codes

The registry is not edited here. These entries follow the registry schema of [diagnostics.md](diagnostics.md) section 6 and would extend its `EMIT` table; the count line there changes from 166 to 169.

| Code | Sev | Fid | Exit | Title | Remediation |
|---|---|---|---|---|---|
| TRL-EMIT-0006 | warning | nm | B | Literal text contains the GitHub expression delimiter | Rewrite the value without `${{`; GitHub evaluates it before the shell or the action sees it, and offers no escape. |
| TRL-EMIT-0007 | warning | ap | - | Trigger pattern widened or dropped because GitHub has no equivalent syntax | Review `params.pattern` and `params.action`; the trigger now fires on a superset of refs or paths. |
| TRL-EMIT-0008 | info | - | - | Secret reference emitted; the secret must exist on the target | Create the secret named in `params.secret` before enabling the workflow; no value was read or written. |

Parameters: `TRL-EMIT-0006` has `field` (one of `script`, `env`, `name`, `variable`, `workingDirectory`, `ref`); `TRL-EMIT-0007` has `pattern`, `kind` (`branch`, `tag`, `path`), `action`; `TRL-EMIT-0008` has `secret` (the normalized GitHub name). Per [diagnostics.md](diagnostics.md) rule 5.7 the emitted set must be a subset of the registry, so these entries are added together with the implementation.

Observation on existing codes: the remediation of `TRL-EXPR-0004` says the original text is kept in a comment, and this emitter never copies source text into the file (INV-11, section 2.5), so that sentence should read "original text is kept in the report".

## 10. Observations on `ir.md` and `normalize-ado.md` (non-normative)

Found while writing this specification; neither file is edited here. Items marked **blocks** change what a faithful emitter can do without a decision.

| ID | Where | Observation | Emitter behavior chosen | Suggested resolution |
|---|---|---|---|---|
| O-1 | normalize-ado.md `pr` rows; ir.md rule 8 | Azure validates pull requests to all branches when `pr` is omitted for GitHub repositories (AZ-GH); the reader synthesizes the default CI trigger but not the default PR trigger and records no loss. Azure Repos Git does not support YAML `pr` at all (AZ-PR). | No `pull_request` event (section 5.2 rule 8). | Materialize the default per repository type, or record a loss. |
| O-2 | ir.md section 4 "Glob"; normalize-ado.md `trigger` | **Blocks exactness.** The IR dialect is normalized gitignore style (`*` stops at `/`), but the reader copies Azure text verbatim, and Azure documents `*` as "zero or more characters" (AZ-GH). An exclude-only Azure filter equals include `*` (AZ-TRG), but the IR list then has only negated entries and ir.md does not say what that matches. | Follow the IR dialect; a negation-only list means "everything except" (section 5.2 rule 4). | Convert Azure patterns when reading, and state the negation-only meaning in ir.md. |
| O-3 | ir.md `TriggerBinding` | `inYaml: false` means "the provisioning plan owns it", but the reader sets it on the synthesized default CI trigger and GitHub has no external trigger provisioning. | Emit the event and record `TRL-EMIT-0001`. | Define `inYaml` for the synthesized default, or add a separate flag. |
| O-4 | ir.md section 5 `Job.needs`, section 1 rule 4 | **Clarify.** Stage `dependsOn` edges are not copied into `Job.needs`, yet `needs` is described as the direct incoming DAG edge and `barrier: false` as "only explicit DAG edges". | Flatten stage edges into job `needs` (section 5.4). | State in ir.md that stage edges apply to every job of the stage. |
| O-5 | ir.md `CheckoutStep`; normalize-ado.md checkout | **Blocks exactness.** `fetchDepth` is a plain integer, so unset and explicit `0` (full history, AZ-CHK) are indistinguishable. `ref` holds the Azure macro `$(Build.SourceVersion)` rather than a portable revision. | `fetch-depth` omitted for 0; macro `ref` recognized as "the triggering commit". | Use an optional depth; give `ref` a portable form. |
| O-6 | ir.md rule 8; normalize-ado.md `pool` | An omitted `pool` yields an empty `RunnerReq` although Azure defaults to `ubuntu-latest` (AZ-HOST); `os` and `arch` are never filled from the image. | Default image with `TRL-CONV-0004`. | Materialize the default and the OS in the reader. |
| O-7 | ir.md section 3 "Every `run` has an explicit `shell`"; normalize-ado.md `script` | The reader stores `azure-default` for the generic `script:` step, which is not a shell; ir.md says parsers resolve the shell. | Resolve from the mapped runner OS (section 5.7). | Resolve in the reader once the runner OS is known (already a VERIFY there). |
| O-8 | normalize-ado.md steps and jobs | Step `name` defaults to the step ID (`step-N`), and a stage job has ID `<stage>.<job>` but name `<job>`, so "name differs from ID" is an imperfect test for "the author wrote a display name". | Emit `name` iff it differs from the ID (section 4.1 rule 5). | Add an explicit "has display name" marker, or omit the default name. |
| O-9 | normalize-ado.md variable groups | A group import is an ordinary `Variable` named after the group with scope `group` and the group name as its value, which looks like a variable. | Discriminate by scope (section 5.9 rule 4). | A dedicated IR node (already a VERIFY there). |
| O-10 | normalize-ado.md `condition`; ir.md `RefExpr` | `variables['Build.SourceBranch']` yields the one-segment path `["Build.SourceBranch"]` while `variables.Build.SourceBranch` yields `["Build","SourceBranch"]`: one variable, two spellings. | Only one-segment paths of declared literals are translated; both spellings of a predefined variable are placeholders. | Normalize paths in the reader (join on `.`). |

