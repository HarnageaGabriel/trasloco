# Provider-neutral pipeline intermediate representation

Status: draft for approval (M1). Normative.

The intermediate representation (IR) is the sole semantic contract between pipeline dialect parsers, capability checks, and emitters. A parser MUST normalize into this model. An emitter MUST consume this model and MUST NOT depend on source-dialect syntax hidden outside `extensions` or `raw` nodes.

## 1. Representation and invariants

The persisted form is UTF-8 JSON with top-level integer `schemaVersion`. Version 1 is defined here. Unknown fields are errors except keys inside `extensions`; readers MUST NOT infer a missing version.

Every valid pipeline obeys all of these invariants:

1. `jobs` and `needs` form a directed acyclic graph. Every dependency names an existing job. A job MUST NOT depend on a later stage across a `barrier: true` boundary, including the target stage.
2. Stable IDs are non-empty and unique within their namespace. Pipeline-level namespaces are inputs, triggers, stages, and jobs; step IDs are unique within a job; matrix-leg IDs are unique within a matrix.
3. Every `OutputRef` resolves to an existing stage, job, step, and `setOutput` operation. A consuming job MUST directly depend on the producing job unless it is the producer.
4. `stages` are a partition of `jobs`: every job appears in exactly one stage and no unknown job appears. Stage order is declaration order. A stage with `barrier: true` requires every job in all preceding stages to reach a terminal result before any job in that stage starts; `false` preserves only explicit DAG edges.
5. Tagged unions (`Expr`, `Cond`, and `Step`) contain exactly the payload selected by their discriminator.
6. Each source-derived semantic node carries `fidelity`, `provenance`, and `origin`. A synthetic normalization node MAY omit `origin`, but MUST inherit or aggregate fidelity and provenance from its causes.
7. Secrets appear only as `SecretRef`; secret values MUST NOT occur in the IR, extensions, raw nodes, debug output, or fixtures.
8. All implicit source behavior is explicit as described in section 3. Absence then means intentionally disabled or inapplicable, never “use the provider default.”

`Validate(*Pipeline)` emits diagnostics through `diag.Collector`, deduplicated and sorted by source position and code; map keys are visited in sorted order. Each node finding uses that node's origin, never the pipeline origin. `Fidelity` and `Provenance` are shared diag types. Stable diagnostics: `TRL-VAL-0007` for a schema version or required field, `TRL-VAL-0008` for duplicate IDs, `TRL-VAL-0009` for a cycle, `TRL-VAL-0010` for an unresolved reference, `TRL-VAL-0011` for an invalid stage partition, and `TRL-VAL-0012` for an invalid union or enum.

## 2. Common metadata

### Metadata

| Field | Type | Req | Semantics |
|---|---|---|---|
| `origin` | `Origin` | source nodes | Exact source location and spelling identity. Omitted only for synthetic nodes. |
| `fidelity` | `Fidelity` | yes | `exact`, `approximated`, `needs-manual`, or `unsupported`. Aggregation uses the ordering in `README.md`. |
| `provenance` | `Provenance` | yes | `deterministic` or `llm`, orthogonal to fidelity. Any LLM contribution makes the aggregate `llm`. |
| `extensions` | object | no | Provider bags keyed `x-<provider>`, containing arbitrary JSON for lossless same-dialect round-trip. Other keys are invalid. Cross-dialect emitters MUST ignore semantics known only to an extension and report the resulting fidelity. |

### Origin

| Field | Type | Req | Semantics |
|---|---|---|---|
| `source` | `diag.Source` | yes | Shared source position: platform, file, line/col and exclusive endLine/endCol. Byte spans belong to the shared Source model (ADR 0003). |
| `chain` | `diag.Source[]` | no | Template instantiation chain, outermost first. |
| `originalTextHash` | string | yes | `sha256:<hex>` of the exact source bytes for the construct before decoding or normalization. |

`Raw` is the explicit escape hatch: `provider`, provider node `kind`, serialization `format`, and exactly one of textual `text` or JSON `json`, plus optional `origin`. Raw content is never silently executed by another dialect. A cross-dialect emitter emits a failing placeholder or manual task according to fidelity.

## 3. Pipeline normalization

### Pipeline

| Field | Type | Req | Semantics |
|---|---|---|---|
| common metadata | `Metadata` | yes | Aggregate metadata for the pipeline. |
| `schemaVersion` | integer | yes | Exactly `1`. |
| `id` | string | yes | Stable source-independent identity used by joins. |
| `name` | string | yes | Display name; never an identity key. |
| `inputs` | `Input[]` | no | Typed user/caller inputs. |
| `triggers` | `Trigger[]` | no | Creation events, including externally provisioned bindings. Empty means explicitly no automatic trigger. |
| `variables` | `Variable[]` | no | Pipeline-visible bindings in declaration order. |
| `jobs` | `Job[]` | yes | Vertices of the explicit DAG. |
| `stages` | `Stage[]` | yes | Ordered partition of jobs. Empty only when `jobs` is empty. |
| `concurrency` | `Concurrency` | no | Cross-run scheduling intent; omission means explicitly unconstrained. |
| `diagnostics` | `Diagnostic[]` | no | Stable parse/normalization findings already associated with this IR, using `diagnostics.md`. Validation returns additional diagnostics and does not mutate this list. |

Normalization MUST materialize provider defaults:

- Every job and step has `if`. A missing source condition becomes `success()` at `runtime`; an explicit ADO condition replaces, rather than implicitly combines with, that gate unless source semantics say otherwise.
- Implicit checkout becomes a `checkout` step at the source-defined position. If checkout is disabled or inapplicable, no checkout step is present. Deployment jobs do not gain checkout unless the source does.
- Implicit artifact download becomes an `artifactRestore` step only where the source performs it, such as an Azure deployment `deploy` hook. Absence means no download.
- Every `run` has an explicit `shell`; parsers resolve the source platform, runner OS, and configured defaults. Emitters never choose a shell silently.
- A PR checkout records the resolved provider merge-ref spelling in `checkout.prMergeRef`; `checkout.ref` expresses the portable selected revision. Emitters diagnose targets whose PR merge semantics differ.

### Inputs and variables

`Input` fields are `id`, display `name`, `type`, `required`, optional typed `default`, optional `choices`, and optional `description`, plus common metadata. Type is `string`, `bool`, `number`, or `choice`; `choice` requires a non-empty `choices` list and its default MUST be one of them.

`Variable` fields are `name`, `scope`, optional `scopeId`, `value`, `secret`, and `secretRef`, plus common metadata. Scope is `organization`, `repository`, `pipeline`, `stage`, `job`, `step`, `environment`, or `group`. A non-secret contains exactly one `value`; a secret contains exactly one `secretRef` and no value. Declaration order is retained because it can affect precedence. `SecretRef` contains `name`, `scope`, and optional `scopeId`; it never contains value material. Effective precedence follows `variable-precedence.md`.

## 4. Creation events

### Trigger

| Field | Type | Req | Semantics |
|---|---|---|---|
| common metadata | `Metadata` | yes | Per-trigger loss/provenance data. |
| `id` | string | yes | Stable trigger identity. |
| `kind` | enum | yes | `push`, `pr`, `schedule`, `manual`, `upstream`, `webhook`, or `other`. |
| `filters` | `TriggerFilters` | no | Normalized branch, tag, path, and activity filters. |
| `binding` | `TriggerBinding` | yes | `inYaml: true` if the target definition belongs in emitted YAML; `false` if the provisioning plan owns it. |
| `schedule` | `Schedule` | iff schedule | Schedule payload. |
| `upstream` | string | upstream | Stable upstream pipeline reference. |
| `other` | `Raw` | iff other | Preserved event without a portable kind. |

`TriggerFilters.branches`, `tags`, and `paths` contain `Glob` objects; `actions` contains normalized provider event activities. A `Glob` has normalized `pattern` and exact `original`. The normalized dialect is gitignore-style: `/` separator, `*` excludes `/`, `**` crosses separators, `?` matches one non-`/` character, `[abc]`/`[!abc]` are character classes, leading `!` negates, and `\` escapes the next metacharacter. Lists are evaluated in order, so later patterns may re-include. Parsers MUST diagnose source behavior not expressible by this dialect.

`Schedule` fields are `cron`, `timezone`, `original`, and `onlyIfChanged`. `cron` is POSIX five-field minute/hour/day-of-month/month/day-of-week syntax; seconds and provider macros are not portable. `timezone` is an IANA name, including `Etc/UTC`; fixed local aliases are invalid. `original` retains the source expression. `onlyIfChanged` states whether unchanged source suppresses the scheduled run.

## 5. Execution graph

### Stage and Job

`Stage` has common metadata, stable `id`, display `name`, ordered job-ID list `jobs`, optional condition `if`, explicit stage-ID dependencies `dependsOn`, ordered `variables`, and `barrier` semantics defined in section 1. Dependencies must resolve and form a DAG.

| Job field | Type | Req | Semantics |
|---|---|---|---|
| common metadata | `Metadata` | yes | Job loss/provenance data. |
| `id`, `name` | string | yes | Stable identity and display name. |
| `uses` | `ReusableCall` | no | Reusable workflow call with required `ref`, expression map `with`, and `secrets` mapping input names to `SecretRef`. Mutually exclusive with non-empty `steps`. |
| `permissions` | object | no | Explicit permission names mapped to `read`, `write`, or `none`. |
| `concurrency` | `Concurrency` | no | Job scheduling intent, using the pipeline concurrency model. |
| `needs` | string[] | no | Direct incoming DAG edges. |
| `if` | `Cond` | yes | Explicit creation/runtime gate, including the normalized success gate. |
| `runner` | `RunnerReq` | yes | Portable runner constraints. Empty fields mean no constraint, not a provider default. |
| `container` | `Container` | no | Job execution container. |
| `services` | `Service[]` | no | Named sidecars. |
| `variables`, `secrets` | arrays | no | Job bindings; secrets remain references. |
| `outputs` | `OutputDef[]` | no | Public job outputs and their fully qualified producer. |
| `steps` | `Step[]` | yes | Ordered operations. |
| `matrix` | `Matrix` | no | Fully expanded execution legs. |
| `timeout` | `Duration` | no | Portable Go-duration string, for example `30m0s`. |
| `allowFailure` | bool | yes | Failure does not fail its consumer scope where supported. |
| `deployment` | `DeploymentIntent` | no | Environment deployment intent. |
| `result` | `ResultModel` | yes | `succeeded`, `succeededWithIssues`, `failed`, `canceled`, or `skipped`. This closed model is used by result/status expressions and reports. |

`OutputDef` has public `name` and `from: OutputRef`. `OutputRef` has mandatory `stage`, `job`, `step`, and output `name`; it is syntax-free and emitters select target syntax.

`Matrix` has exactly one of non-empty explicit `legs` or `opaque: Raw`, optional positive `maxParallel` (`0` means unbounded), and explicit `failFast`. Each `MatrixLeg` has unique `id` and scalar-literal `values`. Parsers expand include/exclude rules before producing IR; dynamic unbounded matrices use `opaque`, retaining source text and origin without inventing legs.

`Concurrency` has expression `group` and mode `cancel-in-progress`, `queue-latest`, or `serialize`. These are intents: capability checking grades any destination approximation. `DeploymentIntent` has target `environment`, optional portable `strategy`, and optional source lifecycle `hook`; unmodeled strategy detail remains in extensions.

### Runner and containers

`RunnerReq` fields are optional `os`, `arch`, ordered `labels`, `image`, immutable `imagePin`, `pool`, and ordered `demands`. `image` is the human/provider image identity; `imagePin` is an immutable digest or dated catalog identity. A moving `*-latest` alias MUST NOT be represented as a pin.

`Container` has required `image`, optional `imagePin`, expression map `env`, ordered `ports` and `options`, and optional `credentials`. `ContainerAuthRef.username` is an expression; `password` is a `SecretRef`. `Service` has stable `id`, `container`, and optional `health`. `ServiceHealth` has command argv `command`, optional `interval` and `timeout`, and non-negative `retries`.

## 6. Steps

`Step` has common metadata, stable `id`, display `name`, discriminator `kind`, required explicit `if`, expression map `env`, optional `timeout`, explicit `continueOnError`, non-negative `retry`, and exactly one matching payload:

| Kind / payload | Fields and semantics |
|---|---|
| `run` / `run` | `script`, explicit `shell`, optional `workingDirectory`, and ordered recognized protocol `operations`. Script text remains intact until a target-specific safe rewrite. |
| `checkout` / `checkout` | `repository`, required expression `ref`, optional `path`, non-negative `fetchDepth`, booleans `submodules`, `lfs`, `persistCredentials`, and optional source `prMergeRef`. |
| `artifactPublish` / `artifactPublish` | Artifact `name` and path expression `path`. |
| `artifactRestore` / `artifactRestore` | Artifact `name` and destination expression `path`. |
| `cache` / `cache` | Key expression `key`, ordered `restoreKeys`, path expressions `paths`, and `save` intent. |
| `action` / `action` | Source `provider`, action/task `name`, `version`, optional immutable `digest`, and expression map `with`. |
| `setOutput`, `setEnv`, `setPipelineVar`, `addPath`, `mask`, `annotate` / `operation` | One abstract runner-protocol operation as defined below and in `script-protocol.md`. |
| `raw` / `raw` | Explicit unmodeled provider fragment. |

`ProtocolOperation` fields are operation `kind`, optional target `name`, optional expression `value`, `secret`, annotation `level`, optional expression `message`, and optional `location`. Required subsets follow `script-protocol.md`: named setters require `name` and `value`; `addPath`/`mask` require `value`; `annotate` requires `level` and `message`. `Location` has file plus optional one-based line and column. Operation order in `RunStep.operations` is semantic.

## 7. Conditions and expressions

`Cond` has evaluation `phase` (`compile`, `creation`, or `runtime`) and exactly one of portable `expr` or opaque `opaque: Raw`. `Opaque` is mandatory fallback when parsing succeeds but semantics fall outside the portable AST; adapters MUST NOT reinterpret its text as another dialect.

`Expr` is a tagged tree with `kind` and exactly one matching payload: `literal`, `ref`, or `call`.

- `Literal` has type `null`, `string`, `bool`, or `number`, and the corresponding scalar field. The `number` field is a JSON string containing a decimal number (optional fraction and exponent, JSON number grammar). This preserves integers beyond 2^53 through serialization, equality, and JCS digests. It is never converted to binary64 by the IR.
- `RefExpr` has context and either non-empty `path` or typed `output`. Context is `variables`, `parameters`, `env`, `secrets`, `dependencyOutputs`, `matrix`, `runner`, or `run`. Only `dependencyOutputs` uses `output`; other contexts use path segments, never a dialect expression string.
- `CallExpr` has a closed `function` and ordered `args`.

Portable functions and mapping obligations:

| Function | Azure Pipelines | GitHub Actions | Notes |
|---|---|---|---|
| `and`, `or`, `not` | same function form | `&&`, `||`, `!` | Emitters preserve short-circuit order. |
| `eq`, `ne`, `lt`, `le`, `gt`, `ge` | same function form | comparison operators | Adapters diagnose coercion differences; they MUST NOT invent casts. |
| `contains`, `startsWith`, `endsWith` | same names | same names | Case/coercion differences require approximation diagnostics. |
| `format` | `format` | `format` | Portable only for indexed placeholders and scalar arguments. |
| `join` | `join` | `join` | Portable only after operand type is proven. |
| `success` | `succeeded()` in job/step context | `success()` | Normalized implicit success gate. |
| `failure` | `failed()` | `failure()` | Current dependency scope. |
| `always` | `always()` | `always()` | Still subject to platform cancellation limitations. |
| `cancelled` | `canceled()` | `cancelled()` | IR spelling uses double `l`; emitters map it. |
| `succeededWithIssues` | result comparison / `succeededOrFailed()` refinement | result comparison | Capability check normally grades this approximated or manual on GitHub. |

Any other function, dynamic property operation, object/array construction, or dialect-specific coercion uses `Cond.opaque` or a raw step. Status function zero-argument requirements and ordinary function arity/type rules are checked by expression adapters; structural IR validation checks membership in this closed set.

## 8. JSON, equality, debug form, and digests

`encoding/json` field names in the Go types are the normative serialized names. Marshal and unmarshal require schema version 1; unmarshal rejects unknown fields. Arrays retain semantic order. Extension values retain JSON meaning; byte-for-byte extension spelling is not promised after a JSON round trip.

Digest input uses RFC 8785 JSON Canonicalization Scheme (JCS): no insignificant whitespace, object keys sorted by UTF-16 code units, ECMAScript string escaping, and ECMAScript finite binary64 number serialization. Negative zero canonicalizes to `0`. The implementation rejects duplicate object names, invalid UTF-8, non-finite numbers, cycles, trailing JSON values, and values outside `encoding/json`'s model. It is intended for JSON emitted by trasloco. Semantic sets, if any are introduced by an enclosing artifact, MUST be sorted by their specified stable key before JCS.

`Equal` is semantic deep equality of canonical JSON, including origins and extension JSON values. `DebugString` is deterministic indented JSON and is safe only because valid IR contains secret references, never values.

## Test vectors

| ID | Input | Expected | Expected diagnostic |
|---|---|---|---|
| IR-001 | valid one-stage pipeline with explicit success gates and shell | valid | none |
| IR-002 | two jobs with ID `build` | invalid duplicate job ID | `TRL-VAL-0008` |
| IR-003 | job A needs B and job B needs A | invalid cyclic DAG | `TRL-VAL-0009` |
| IR-004 | job A needs missing job Z | invalid dependency reference | `TRL-VAL-0010` |
| IR-005 | job output references `main/build/missing/value` | invalid unresolved output | `TRL-VAL-0010` |
| IR-006 | job `build` occurs in stages `one` and `two` | invalid non-partition | `TRL-VAL-0011` |
| IR-007 | job `build` occurs in no stage | invalid non-partition | `TRL-VAL-0011` |
| IR-008 | `schemaVersion: 2` | invalid schema version | `TRL-VAL-0007` |
| IR-009 | step kind `run` with only an `action` payload | invalid tagged union | `TRL-VAL-0012` |
| IR-010 | condition contains both `expr` and `opaque` | invalid tagged union | `TRL-VAL-0012` |
| IR-011 | extension key `github` instead of `x-github` | invalid extension key | `TRL-VAL-0012` |
| IR-012 | run step omits shell | implicit default not explicit | `TRL-VAL-0007` |
| IR-013 | dependency output resolves to a declared `setOutput` and consumer needs producer | valid | none |
| IR-014 | JSON object keys reordered before unmarshal | round trip remains deeply equal | none |
| IR-015 | JCS input `{"b":1e-7,"a":-0.0}` | `{"a":0,"b":1e-7}` | none |
