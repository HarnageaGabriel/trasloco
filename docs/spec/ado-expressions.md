# Azure Pipelines expression language

The typed engine implements ADR 0004 using the independently authored reference
below. Source abbreviations and uncertainty rulings are preserved verbatim.

## Implementation boundary

`Parse` accepts bare expressions and complete compile/runtime wrappers; `Eval`
returns a typed value. Scalar placement and template document insertion belong
to the dialect/template resolver. `ExpandMacros` operates on scalar values only.
Status and persistent counters are injected through typed providers. `Scope`
restricts named status predicates to job conditions; variable definitions reject
status predicates. The caller supplies `DateTime` for `pipeline.startTime`.
`DecodeJSON` preserves object property order and numeric precision. Arrays and
objects have reference identity; callers must not mutate them during evaluation.

All 446 sourced vectors below are loaded and validated by tests. Template-document
vectors EXPRV-355..397, mapping-key macro placement EXPRV-415, and template include
limits EXPRV-445..446 are retained as resolver integration contracts, explicitly
skipped by the expression-only test runner. EXPRV-350 tests scalar placement at
that boundary. No new template resolver is introduced by this change.

Errors are `*diag.Error`: `TRL-EXPR-0001` covers syntax, arity, phase, status and
limits; `TRL-EXPR-0005` covers conversion/type failures. Relative byte offsets
remain in `diag.Source.Offset/EndOffset` for mapping by the dialect layer.

## Semantics changelog

- Missing keys and invalid selections return Null; `null` is not a literal.
- Exact System.Decimal values replace float64; rendering uses `0.#######`.
- Boolean/numeric/null conversions and System.Version missing components follow
  the resolved rulings; collections are truthy and compare by reference.
- Objects preserve order, JSON retains non-ASCII and HTML characters, length counts
  UTF-16 units, and leading-dot decimal literals are accepted.
- Macro scanning is lenient, has no dollar escape, and bounds recursive definitions.
- Membership/coalesce short-circuit; corrected arities, status phases, counter seed
  conversion, join, split, replace, and DateTime format validation.
- Parser depth/length and evaluation memory are bounded by the reference constants.
  Evaluation memory charges 24 bytes for a non-string result or 26 bytes plus two
  bytes per UTF-16 code unit for a string, and releases results by nesting level.

## Independent specification and vector table

# Azure Pipelines expression language: authoritative test vectors

Status: independent differential authorship (2026-10-06). Derived from Microsoft Learn docs plus the
open-source lineage of the ADO expression engine. **No trasloco code was read to produce this file.**

## 0. Conventions

### Source abbreviations (column `source`)

| Prefix | Expands to |
|---|---|
| `expr#a` | https://learn.microsoft.com/en-us/azure/devops/pipelines/process/expressions#a |
| `vars#a` | https://learn.microsoft.com/en-us/azure/devops/pipelines/process/variables#a |
| `texpr#a` | https://learn.microsoft.com/en-us/azure/devops/pipelines/process/template-expressions#a |
| `tmpl#a` | https://learn.microsoft.com/en-us/azure/devops/pipelines/process/templates#a |
| `tparam#a` | https://learn.microsoft.com/en-us/azure/devops/pipelines/process/template-parameters#a |
| `cond#a` | https://learn.microsoft.com/en-us/azure/devops/pipelines/process/conditions#a |
| `RI:F` | https://github.com/actions/runner/blob/c8afc848403652f31f84eb362fc0696ee23cffca/src/Sdk/DTExpressions/Expressions/F |
| `RP:F` | https://github.com/actions/runner/blob/c8afc848403652f31f84eb362fc0696ee23cffca/src/Sdk/DTPipelines/Pipelines/Expressions/F |
| `RT:F` | https://github.com/actions/runner/blob/c8afc848403652f31f84eb362fc0696ee23cffca/src/Sdk/DTObjectTemplating/ObjectTemplating/F |
| `AG:F` | https://github.com/microsoft/azure-pipelines-agent/blob/master/src/F (e.g. `AG:Microsoft.VisualStudio.Services.Agent/Util/VarUtil.cs`, `AG:Agent.Worker/Variables.cs`) |

Docs were read from their Markdown sources (MicrosoftDocs/azure-devops-docs, `docs/pipelines/process/*.md`,
ms.date 01/2026) which render 1:1 to the Learn URLs above.

**Which reference implementation.** The task brief pointed at `actions/runner src/Sdk/DTExpressions2`. That
directory is the *GitHub Actions* dialect (`==`/`&&` operators, `fromJson`, no `Version` type, no `eq()`/`in()`),
so it is NOT the ADO dialect. The ADO dialect ("Expressions v1": `eq`, `in`, `notIn`, `containsValue`, `xor`,
Version literals, `counter`, `length`) is `src/Sdk/DTExpressions/` + `src/Sdk/DTPipelines/Pipelines/Expressions/`,
present in actions/runner only at the initial commit `c8afc84` (2019-10-10, deleted in `d0a4a41`, MIT).
All `basis: reference-impl` rows cite that snapshot. The agent's macro expansion (`$(var)`) is from
microsoft/azure-pipelines-agent (MIT). Functions added to ADO after 2019 (`lower`, `upper`, `replace`, `split`,
`trim`, `iif`, `convertToJson`, status functions, `each`/`if` directives) have no public source; their
edge cases are `docs` or `inferred`.

### Column semantics

- `phase`: `compile` = `${{ }}` template expression (context: `parameters`, static `variables`);
  `runtime` = `$[ ]` / `condition:` (context: `variables`, `dependencies`, `stageDependencies`, `pipeline`, counters, job status);
  `macro` = agent `$(name)` substitution in a task input string.
  Pure function vectors are tagged `compile`; the same evaluator is used at runtime, so they must also pass at runtime
  (with `parameters` moved into `variables` where applicable).
- `expression`:
  - for `compile`/`runtime` rows without a wrapper: the bare expression text (as inside `${{ }}` / `$[ ]` / `condition:`);
  - rows starting with `$[` are a runtime variable definition `name: <expression>`;
  - rows whose expression is a JSON document are **template documents** (YAML rendered as JSON; key order = YAML order);
    `${{ ... }}` keys are directives / key expressions; string values may embed `${{ }}`;
  - `macro` rows: the raw task-input string before agent substitution.
- `context`: JSON. Keys: `parameters` (typed JSON: numbers, booleans, arrays, objects), `variables`
  (always strings; lookup is case-insensitive), `dependencies`, `stageDependencies`, `pipeline.startTime`
  (a DateTime; ISO text given, org time zone = UTC), `counters` (prefix -> last value already issued for this pipeline),
  `scope` (`step`/`job`, for status functions), `canceled` (run cancellation requested). `{}` = empty context.
- `expected`:
  - plain JSON = typed result (`null`, `true`, `1.5`, `"abc"`, `[..]`, `{..}`);
  - `version(1.2.3)` = Version-typed result; `datetime(...)` = DateTime-typed result;
  - for `$[ ]`, `macro`, embedded-`${{ }}` and template-document rows: the rendered string/document;
  - `error:<reason>` = evaluation or parse must fail; reason is informative.
- `code`: exact registered diagnostic code for errors; `-` for successful vectors.
  EXPRV-430 pins byte offset 2 (the missing `(` after `eq`); EXPRV-444 pins
  byte span 0:24 (the complete `join` call), preventing an earlier context
  identifier from satisfying the memory-limit vector for the wrong reason.
- `basis`: `docs` (stated in Learn docs), `reference-impl` (DTExpressions v1 / agent source, docs silent or
  contradicted), `inferred` (neither states it; recommended behavior).

### Rendering rules (how typed values become strings) used by expected values

| Kind | String form | basis |
|---|---|---|
| Boolean | `True` / `False` (capitalized) | docs `expr#boolean` (type casting), RI:EvaluationResult.cs |
| Null | `` (empty) | docs `expr#null` |
| Number | .NET `Decimal.ToString("0.#######", Invariant)`: no exponent, no thousands separator, at most 7 fractional digits (rounded), trailing zeros dropped | RI:ExpressionConstants.cs (`NumberFormat`) |
| Version | `Major.Minor[.Build[.Revision]]` exactly as many components as parsed | docs `expr#version` |
| DateTime | `yyyy-MM-dd HH:mm:sszzz` (e.g. `2024-03-05 07:08:09+00:00`) | RI:ExpressionConstants.cs (`DateTimeFormat`) |
| Array / Object | not convertible to string -> error wherever a string is required (format arg, contains, embedded `${{ }}`) | RI:EvaluationResult.cs |

## 1. Resolved semantics (disputed points)

Quotes are fragments (< 15 words) from the cited page.

1. **Missing dictionary key -> `null`.** "returned from a dictionary miss, for example (`variables['noSuch']`)" --
   https://learn.microsoft.com/en-us/azure/devops/pipelines/process/expressions#null . When rendered, `${{ }}` and `$[ ]`
   "silently coalesce to empty strings when a replacement value isn't found" (vars#runtime-expression-syntax).
   Indexing *into* null (e.g. `variables.Build.SourceBranch`) also yields null, never an error (RI:IndexerNode.cs).
2. **Number -> string.** Docs: "with no thousands separator and no decimal separator" (expr#number). Taken literally
   "no decimal separator" is self-contradictory (`-1.2` is a documented literal). Ruling: reference-impl format
   `0.#######` (invariant): `1.50`->`1.5`, `1000000`->`1000000`, `1.23456789`->`1.2345679`, never exponent. basis reference-impl.
3. **String -> Boolean.** "'' (the empty string) -> False, any other string -> True" (expr#string). So `'false'`,
   `'0'`, `' '` are all **True**. basis docs.
4. **String -> Number.** "'' (the empty string) -> 0, otherwise, runs C#'s Int32.TryParse" with
   `AllowDecimalPoint | AllowLeadingSign | AllowLeadingWhite | AllowThousands | AllowTrailingWhite` (expr#string).
   So: surrounding whitespace OK, leading `+`/`-` OK, thousands commas OK (`'1,000'`->1000), **no exponent**,
   no hex, no currency, no parentheses; whitespace-only string is NOT empty -> not convertible.
   Conflict: docs say Int32 (would reject `'1.5'` and values > 2^31-1, accept `'1.0'`); reference-impl uses `Decimal.TryParse`
   with the same styles (accepts `'1.5'`). Ruling: Decimal semantics (consistent with decimal number literals); flagged uncertain.
5. **Version semantics.** Literal: "contain two or three period (`.`) characters" (expr#version) -> 2-4 components;
   1 period = Number. String->Version: "runs C#'s `Version.TryParse`. Must contain Major and Minor" (expr#string).
   Number->Version: "Must be greater than zero and must contain a nonzero decimal" (expr#number) -- implemented as
   `Version.TryParse(numberToString(n))`, so `1.2`->1.2 but `1`/`1.0`->not convertible. Comparison and equality are
   `System.Version` semantics: undefined components are -1 and sort *before* 0, so `1.2 < 1.2.0` and
   `eq(1.2.0, '1.2')` is **False**. basis docs (conversion) + reference-impl (comparison, RI:EvaluationResult.cs).
6. **`null` literal is not usable.** "can't be called directly within an expression" (expr#null). The lexer has no
   `null` keyword; `null` is an unknown keyword -> parse error. basis docs + RI:LexicalAnalyzer.cs.
7. **Truthiness of arrays/objects.** Undocumented (conversion table has no Array/Object rows). Ruling: any array or
   object, **including empty**, is True; DateTime and Version are True. basis reference-impl (RI:EvaluationResult.cs `ConvertToBoolean`).
8. **`replace` with empty search string.** Undocumented. Recommended: error (mirrors .NET `String.Replace` which throws
   `ArgumentException` on empty oldValue). basis inferred; flagged.
9. **`length` of object.** Docs: "Returns the length of a string or an array" (expr#length). Reference-impl also
   returns the property count for objects; Null/Boolean/Number/Version -> error (`InvalidTypeForLengthFunction`).
   basis reference-impl (RP:LengthNode.cs). `length(null)` -> error is flagged.
10. **`$$(` escape.** Does not exist (no doc mentions it; agent `VarUtil.ExpandValues` has no escape handling).
    `$$(a)` with `a=1` renders `$1`. basis reference-impl (AG:Microsoft.VisualStudio.Services.Agent/Util/VarUtil.cs).
11. **Unresolvable macros are left literal.** "If there's no variable by that name, the macro expression doesn't change"
    (vars#macro-syntax-variables). The agent takes the text between `$(` and the **first** following `)` as the name
    (no nesting, no trimming); if that name is not a variable the text is kept verbatim and scanning resumes one char
    later. Hence `$((1+2))`, `$(git rev-parse HEAD)`, `$( a )`, `$()` stay literal. Substituted text is not re-scanned
    within the same input. basis docs + reference-impl.
12. **`in` / `notIn` arity.** Docs: "Min parameters: 1. Max parameters: N" (expr#in, expr#notin). 2019 reference-impl
    registers min 2. Ruling: docs (min 1): `in(x)` -> False, `notIn(x)` -> True. Flagged.
13. **Status functions with job names.** "With job names as arguments, evaluates to `True` if all of those jobs succeeded"
    (expr#succeeded); same pattern for `failed` ("any of those jobs failed") and `succeededOrFailed`. Names apply to
    job/stage-level conditions only; step-level forms are defined via `Agent.JobStatus` and take no arguments
    (recommended: error if names given at step scope -- inferred). `always`/`canceled` take no args. Status functions
    are invalid in variable definitions ("but not in variable definitions", expr#job-status-functions).
14. **`counter` arity.** Docs: "Takes two parameters: `prefix` and `seed`." (expr#counter). Reference-impl registers
    0..2 (prefix default `''`, seed default 0, seed via `Convert.ToInt32` of a number). Ruling: accept 1..2, recommend
    error on 0 or 3+; `counter(p)` -> seed 0. Flagged. Only valid in runtime variable definitions ("Don't use it as part of a condition").
15. **`join` on non-array.** "If the right parameter isn't an array, the result is the right parameter converted to a string"
    (expr#join). Array elements that are complex objects -> `''` but the separator is still emitted (`a,,b`). A non-array
    *object* as right parameter -> `''` (not convertible). basis docs + reference-impl (RI:JoinNode.cs).
16. **`length` on non-BMP characters.** Undocumented. .NET `String.Length` = UTF-16 code units: `length('😀')` = 2.
    basis reference-impl.
17. **`format` specifiers.** Only for DateTime arguments; docs list `yyyy, yy, MM, M, dd, d, HH, H, m, mm, ss, s, f, ff, ffff, K`
    (expr#format). Reference-impl also accepts `fff`..`fffffff`, `zzz`, a space, and `\x` escapes; **any other
    character (`-`, `:`, `/`, `.`) inside the specifier is an error**; specifiers on non-DateTime -> error; arg index
    beyond supplied args -> error; lone `{`/`}` -> error; `{{`/`}}` escape. Use `{0:yyyy}-{0:MM}` instead of `{0:yyyy-MM}`.
    basis reference-impl (RI:FormatNode.cs); the `-` ruling is flagged.
18. **`convertToJson`.** Docs show 2-space indented JSON preserving YAML key order (expr#converttojson). Escaping is
    undocumented: recommended Newtonsoft defaults (`"`->`\"`, `\`->`\\`, control chars escaped, non-ASCII emitted raw,
    no HTML escaping). Newline flavor (`\n` vs `\r\n`) unverified; vectors assume `\n`. basis docs (layout) + inferred (escaping).
19. **Numbers starting with `.`.** "Starts with '-', '.', or '0' through '9'" (expr#number). A `.` begins a number only
    at expression start or right after `(`, `[`, `,`; elsewhere it is the dereference operator. `5.` is 5; exponent
    forms (`1e3`) and `+1` are parse errors. basis docs + RI:LexicalAnalyzer.cs.
20. **Case-insensitivity.** Booleans: "case insensitive, so True or TRUE also works" (expr#literals). Function names and
    context names (`EQ`, `Variables`) and variable names are case-insensitive (reference-impl: `OrdinalIgnoreCase`
    dictionaries; agent variables `OrdinalIgnoreCase`). String comparison: "Ordinal ignore-case comparison" (expr#eq) --
    .NET OrdinalIgnoreCase folds to UPPER case, so `lt('_', 'a')` is **False** ('_' 0x5F > 'A' 0x41).
21. **Type coercion direction.** "Converts right parameter to match type of left parameter" (expr#eq). Consequences:
    `eq(true, 'false')` = True; `eq(0, variables.noSuch)` = True but `eq(variables.noSuch, 0)` = False;
    `gt(variables.n, 10)` with `n='5'` compares *strings* -> True. Conversion failure: `eq`/`in`/`containsValue` -> False,
    `ne`/`notIn` -> True, `gt`/`ge`/`lt`/`le` -> error. Arrays/objects compare equal only by reference.
22. **Limits.** Documented (tmpl#imposed-limits-on-template-updates): "No more than 100 separate YAML files may be included";
    100 levels of template nesting; "No more than 20 megabytes of memory consumed while parsing". Not documented on Learn,
    from reference-impl: expression max length **21,000 chars** (RI:ExpressionConstants.cs `MaxLength`), max parse depth
    **50** nested containers (functions + indexers; `MaxDepth`), evaluation memory **1 MiB** (RI:EvaluationContext.cs,
    strings cost 26 + 2*len bytes); agent macro recursion depth **50** with cycle detection (AG:Agent.Worker/Variables.cs).
23. **Other contradictions found in docs.** `format` min params: 1 in expr#format vs 2 in texpr#format -> ruling 1
    (reference-impl agrees). `iif`: "Min parameters: 1. Max parameters: 3" (expr#iif) -- min 1 is implausible; flagged.
    No operators or grouping parentheses exist (`&&`, `==`, `!`, `(a)` are parse errors) -- reference-impl.

## 2. Vectors

| ID | phase | expression | context (JSON) | expected | basis | source | code |
|---|---|---|---|---|---|---|---|
| EXPRV-001 | compile | `true` | `{}` | `true` | docs | expr#boolean | - |
| EXPRV-002 | compile | `TRUE` | `{}` | `true` | docs | expr#literals | - |
| EXPRV-003 | compile | `fAlSe` | `{}` | `false` | reference-impl | RI:LexicalAnalyzer.cs | - |
| EXPRV-004 | compile | `-1.2` | `{}` | `-1.2` | docs | expr#literals | - |
| EXPRV-005 | compile | `.5` | `{}` | `0.5` | docs | expr#number | - |
| EXPRV-006 | compile | `eq(.5, 0.5)` | `{}` | `true` | reference-impl | RI:LexicalAnalyzer.cs | - |
| EXPRV-007 | compile | `-.5` | `{}` | `-0.5` | docs | expr#number | - |
| EXPRV-008 | compile | `5.` | `{}` | `5` | reference-impl | RI:LexicalAnalyzer.cs | - |
| EXPRV-009 | compile | `1e3` | `{}` | `error:unrecognized value (no exponent form)` | reference-impl | RI:LexicalAnalyzer.cs | TRL-EXPR-0001 |
| EXPRV-010 | compile | `+1` | `{}` | `error:unrecognized value` | reference-impl | RI:LexicalAnalyzer.cs | TRL-EXPR-0001 |
| EXPRV-011 | compile | `1,000` | `{}` | `error:unexpected separator outside function` | reference-impl | RI:ExpressionParser.cs | TRL-EXPR-0001 |
| EXPRV-012 | compile | `'a b c'` | `{}` | `"a b c"` | docs | expr#string | - |
| EXPRV-013 | compile | `'It''s OK if they''re using contractions.'` | `{}` | `"It's OK if they're using contractions."` | docs | expr#string | - |
| EXPRV-014 | compile | `''` | `{}` | `""` | docs | expr#string | - |
| EXPRV-015 | compile | `'abc` | `{}` | `error:unterminated string` | reference-impl | RI:LexicalAnalyzer.cs | TRL-EXPR-0001 |
| EXPRV-016 | compile | `"abc"` | `{}` | `error:double quotes are not string delimiters` | docs | expr#string | TRL-EXPR-0001 |
| EXPRV-017 | compile | `1.2.3` | `{}` | `version(1.2.3)` | docs | expr#version | - |
| EXPRV-018 | compile | `1.2.3.4` | `{}` | `version(1.2.3.4)` | docs | expr#version | - |
| EXPRV-019 | compile | `1.2.3.4.5` | `{}` | `error:unrecognized value (max 4 version segments)` | docs | expr#version | TRL-EXPR-0001 |
| EXPRV-020 | compile | `1..2` | `{}` | `error:unrecognized value` | reference-impl | RI:LexicalAnalyzer.cs | TRL-EXPR-0001 |
| EXPRV-021 | compile | `format('{0}', 01.2.3)` | `{}` | `"1.2.3"` | reference-impl | RI:LexicalAnalyzer.cs | - |
| EXPRV-022 | compile | `null` | `{}` | `error:unrecognized value (no null literal)` | docs | expr#null | TRL-EXPR-0001 |
| EXPRV-023 | compile | `eq(variables.a, null)` | `{"variables":{"a":""}}` | `error:unrecognized value (no null literal)` | docs | expr#null | TRL-EXPR-0001 |
| EXPRV-024 | compile | `variables.MyVar` | `{"variables":{"MyVar":"x"}}` | `"x"` | docs | expr#variables | - |
| EXPRV-025 | compile | `variables['MyVar']` | `{"variables":{"MyVar":"x"}}` | `"x"` | docs | expr#variables | - |
| EXPRV-026 | compile | `variables.myvar` | `{"variables":{"MyVar":"x"}}` | `"x"` | reference-impl | AG:Agent.Worker/Variables.cs | - |
| EXPRV-027 | compile | `Variables.MyVar` | `{"variables":{"MyVar":"x"}}` | `"x"` | reference-impl | RI:ExpressionParser.cs | - |
| EXPRV-028 | compile | `variables['noSuch']` | `{}` | `null` | docs | expr#null | - |
| EXPRV-029 | compile | `variables.noSuch` | `{}` | `null` | docs | expr#null | - |
| EXPRV-030 | compile | `variables.Build.SourceBranch` | `{"variables":{"Build.SourceBranch":"refs/heads/main"}}` | `null` | docs | vars#runtime-expression-syntax | - |
| EXPRV-031 | compile | `variables['Build.SourceBranch']` | `{"variables":{"Build.SourceBranch":"refs/heads/main"}}` | `"refs/heads/main"` | docs | vars#runtime-expression-syntax | - |
| EXPRV-032 | compile | `variables.1abc` | `{"variables":{"1abc":"x"}}` | `error:property name must start with letter or _` | docs | expr#variables | TRL-EXPR-0001 |
| EXPRV-033 | compile | `variables.café` | `{"variables":{"café":"x"}}` | `error:invalid property name (use index syntax)` | docs | expr#variables | TRL-EXPR-0001 |
| EXPRV-034 | compile | `variables['café']` | `{"variables":{"café":"x"}}` | `"x"` | docs | vars#template-expression-syntax | - |
| EXPRV-035 | compile | `variables._a1` | `{"variables":{"_a1":"x"}}` | `"x"` | docs | expr#variables | - |
| EXPRV-036 | compile | `parameters.obj.a.b` | `{"parameters":{"obj":{"a":{"b":"deep"}}}}` | `"deep"` | docs | tparam#iterating-through-objects | - |
| EXPRV-037 | compile | `parameters.obj['a']['b']` | `{"parameters":{"obj":{"a":{"b":"deep"}}}}` | `"deep"` | docs | texpr#context | - |
| EXPRV-038 | compile | `parameters.obj.A.B` | `{"parameters":{"obj":{"a":{"b":"deep"}}}}` | `"deep"` | inferred | RI:ExpressionParser.cs | - |
| EXPRV-039 | compile | `parameters.list[0]` | `{"parameters":{"list":["x","y","z"]}}` | `"x"` | docs | expr#expressions | - |
| EXPRV-040 | compile | `parameters.list[2]` | `{"parameters":{"list":["x","y","z"]}}` | `"z"` | docs | expr#expressions | - |
| EXPRV-041 | compile | `parameters.list[3]` | `{"parameters":{"list":["x","y","z"]}}` | `null` | reference-impl | RI:IndexerNode.cs | - |
| EXPRV-042 | compile | `parameters.list[-1]` | `{"parameters":{"list":["x","y","z"]}}` | `null` | reference-impl | RI:IndexerNode.cs | - |
| EXPRV-043 | compile | `parameters.list['1']` | `{"parameters":{"list":["x","y","z"]}}` | `"y"` | reference-impl | RI:IndexerNode.cs | - |
| EXPRV-044 | compile | `parameters.list[1.7]` | `{"parameters":{"list":["x","y","z"]}}` | `"y"` | reference-impl | RI:IndexerNode.cs | - |
| EXPRV-045 | compile | `parameters.obj.missing` | `{"parameters":{"obj":{"a":"1"}}}` | `null` | docs | expr#null | - |
| EXPRV-046 | compile | `parameters.obj.missing.deeper` | `{"parameters":{"obj":{"a":"1"}}}` | `null` | reference-impl | RI:IndexerNode.cs | - |
| EXPRV-047 | compile | `parameters.str[0]` | `{"parameters":{"str":"abc"}}` | `null` | reference-impl | RI:IndexerNode.cs | - |
| EXPRV-048 | compile | `'abc'[0]` | `{}` | `error:unexpected symbol '[' (index must follow name, ')' or ']')` | reference-impl | RI:ExpressionParser.cs | TRL-EXPR-0001 |
| EXPRV-049 | compile | `split('a,b', ',')[1]` | `{}` | `"b"` | docs | expr#split | - |
| EXPRV-050 | compile | `parameters.foo.*.id` | `{"parameters":{"foo":[{"id":1,"a":"avalue1"},{"id":2,"a":"avalue2"},{"id":3,"a":"avalue3"}]}}` | `[1,2,3]` | docs | expr#filtered-arrays | - |
| EXPRV-051 | compile | `parameters.foo[*].id` | `{"parameters":{"foo":[{"id":1},{"id":2},{"id":3}]}}` | `[1,2,3]` | reference-impl | RI:ExpressionParser.cs | - |
| EXPRV-052 | compile | `parameters.foo.*.id` | `{"parameters":{"foo":[{"id":1},{"a":"x"},{"id":3}]}}` | `[1,3]` | reference-impl | RI:IndexerNode.cs | - |
| EXPRV-053 | compile | `parameters.str.*` | `{"parameters":{"str":"abc"}}` | `[]` | reference-impl | RI:IndexerNode.cs | - |
| EXPRV-054 | compile | `containsValue(parameters.foo.*.id, 2)` | `{"parameters":{"foo":[{"id":1},{"id":2}]}}` | `true` | docs | expr#filtered-arrays | - |
| EXPRV-055 | runtime | `dependencies.A.result` | `{"dependencies":{"A":{"result":"Skipped","outputs":{}}}}` | `"Skipped"` | docs | expr#job-to-job-dependencies-within-one-stage | - |
| EXPRV-056 | runtime | `dependencies.A.outputs['printvar.shouldrun']` | `{"dependencies":{"A":{"result":"Succeeded","outputs":{"printvar.shouldrun":"true"}}}}` | `"true"` | docs | expr#job-to-job-dependencies-within-one-stage | - |
| EXPRV-057 | runtime | `eq(dependencies.A.outputs['printvar.nope'], '')` | `{"dependencies":{"A":{"result":"Succeeded","outputs":{}}}}` | `true` | docs | expr#null | - |
| EXPRV-058 | runtime | `stageDependencies.A.A1.outputs['printvar.shouldrun']` | `{"stageDependencies":{"A":{"A1":{"result":"Succeeded","outputs":{"printvar.shouldrun":"true"}}}}}` | `"true"` | docs | expr#job-to-job-dependencies-across-stages | - |
| EXPRV-059 | runtime | `in(dependencies.a.result, 'Succeeded', 'SucceededWithIssues', 'Skipped')` | `{"dependencies":{"a":{"result":"Skipped","outputs":{}}}}` | `true` | docs | expr#job-to-job-dependencies-within-one-stage | - |
| EXPRV-060 | runtime | `eq(dependencies.build.outputs['build_job.build_job.setRunTests.runTests'], 'true')` | `{"dependencies":{"build":{"result":"Succeeded","outputs":{"build_job.build_job.setRunTests.runTests":"true"}}}}` | `true` | docs | expr#deployment-job-output-variables | - |
| EXPRV-061 | compile | `and(true, true)` | `{}` | `true` | docs | expr#and | - |
| EXPRV-062 | compile | `and(true, false, true)` | `{}` | `false` | docs | expr#and | - |
| EXPRV-063 | compile | `and(true)` | `{}` | `error:and requires at least 2 parameters` | docs | expr#and | TRL-EXPR-0001 |
| EXPRV-064 | compile | `and(false, length(5))` | `{}` | `false` | docs | expr#and | - |
| EXPRV-065 | compile | `and(1, 'x', 1.2.3)` | `{}` | `true` | docs | expr#type-casting | - |
| EXPRV-066 | compile | `or(false, 0, '')` | `{}` | `false` | docs | expr#or | - |
| EXPRV-067 | compile | `or(true, length(5))` | `{}` | `true` | docs | expr#or | - |
| EXPRV-068 | compile | `or(false, 'false')` | `{}` | `true` | docs | expr#string | - |
| EXPRV-069 | compile | `or()` | `{}` | `error:or requires at least 2 parameters` | docs | expr#or | TRL-EXPR-0001 |
| EXPRV-070 | compile | `not(0)` | `{}` | `true` | docs | expr#not | - |
| EXPRV-071 | compile | `not('')` | `{}` | `true` | docs | expr#string | - |
| EXPRV-072 | compile | `not('0')` | `{}` | `false` | docs | expr#string | - |
| EXPRV-073 | compile | `not(variables.noSuch)` | `{}` | `true` | docs | expr#null | - |
| EXPRV-074 | compile | `not(eq(1, 2))` | `{}` | `true` | docs | expr#not | - |
| EXPRV-075 | compile | `not(1, 2)` | `{}` | `error:not takes exactly 1 parameter` | docs | expr#not | TRL-EXPR-0001 |
| EXPRV-076 | compile | `not()` | `{}` | `error:not takes exactly 1 parameter` | docs | expr#not | TRL-EXPR-0001 |
| EXPRV-077 | compile | `xor(True, False)` | `{}` | `true` | docs | expr#xor | - |
| EXPRV-078 | compile | `xor(true, true)` | `{}` | `false` | docs | expr#xor | - |
| EXPRV-079 | compile | `xor(true, false, true)` | `{}` | `error:xor takes exactly 2 parameters` | docs | expr#xor | TRL-EXPR-0001 |
| EXPRV-080 | compile | `eq(variables.letters, 'abc')` | `{"variables":{"letters":"ABC"}}` | `true` | docs | expr#eq | - |
| EXPRV-081 | compile | `eq('true', true)` | `{}` | `true` | docs | expr#type-casting | - |
| EXPRV-082 | compile | `eq('false', true)` | `{}` | `false` | docs | expr#type-casting | - |
| EXPRV-083 | compile | `eq(true, 'false')` | `{}` | `true` | docs | expr#string | - |
| EXPRV-084 | compile | `eq(1, true)` | `{}` | `true` | docs | expr#boolean | - |
| EXPRV-085 | compile | `eq(2, true)` | `{}` | `false` | docs | expr#boolean | - |
| EXPRV-086 | compile | `eq(true, 2)` | `{}` | `true` | docs | expr#number | - |
| EXPRV-087 | compile | `eq(1, 'abc')` | `{}` | `false` | docs | expr#eq | - |
| EXPRV-088 | compile | `ne(1, 'abc')` | `{}` | `true` | docs | expr#ne | - |
| EXPRV-089 | compile | `eq(1000, '1,000')` | `{}` | `true` | docs | expr#string | - |
| EXPRV-090 | compile | `eq(1000, '1,0,0,0')` | `{}` | `true` | reference-impl | RI:EvaluationResult.cs | - |
| EXPRV-091 | compile | `eq(42, ' 42 ')` | `{}` | `true` | docs | expr#string | - |
| EXPRV-092 | compile | `eq(5, '+5')` | `{}` | `true` | docs | expr#string | - |
| EXPRV-093 | compile | `eq(-5, ' -5')` | `{}` | `true` | docs | expr#string | - |
| EXPRV-094 | compile | `eq(1000, '1e3')` | `{}` | `false` | docs | expr#string | - |
| EXPRV-095 | compile | `eq(16, '0x10')` | `{}` | `false` | docs | expr#string | - |
| EXPRV-096 | compile | `eq(1.5, '1.5')` | `{}` | `true` | reference-impl | RI:EvaluationResult.cs | - |
| EXPRV-097 | compile | `eq(1, '1.0')` | `{}` | `true` | docs | expr#string | - |
| EXPRV-098 | compile | `eq(0.5, '.5')` | `{}` | `true` | reference-impl | RI:EvaluationResult.cs | - |
| EXPRV-099 | compile | `eq(0, '')` | `{}` | `true` | docs | expr#string | - |
| EXPRV-100 | compile | `eq(0, '  ')` | `{}` | `false` | reference-impl | RI:EvaluationResult.cs | - |
| EXPRV-101 | compile | `eq(0, variables.noSuch)` | `{}` | `true` | docs | expr#null | - |
| EXPRV-102 | compile | `eq(variables.noSuch, 0)` | `{}` | `false` | docs | expr#type-casting | - |
| EXPRV-103 | compile | `ne(variables.noSuch, 0)` | `{}` | `true` | docs | expr#ne | - |
| EXPRV-104 | compile | `eq(variables.noSuch, '')` | `{}` | `true` | docs | expr#string | - |
| EXPRV-105 | compile | `eq(variables.noSuch, 'x')` | `{}` | `false` | docs | expr#string | - |
| EXPRV-106 | compile | `eq('', variables.noSuch)` | `{}` | `true` | docs | expr#null | - |
| EXPRV-107 | compile | `eq(variables.noSuch, variables.alsoNoSuch)` | `{}` | `true` | reference-impl | RI:EvaluationResult.cs | - |
| EXPRV-108 | compile | `eq(variables.noSuch, false)` | `{}` | `false` | docs | expr#type-casting | - |
| EXPRV-109 | compile | `eq(variables.testEmpty, '')` | `{"variables":{"testEmpty":""}}` | `true` | docs | cond#variables-in-conditions | - |
| EXPRV-110 | compile | `eq(0.1, '0.10')` | `{}` | `true` | reference-impl | RI:EvaluationResult.cs | - |
| EXPRV-111 | compile | `eq(1.0, 1)` | `{}` | `true` | reference-impl | RI:EvaluationResult.cs | - |
| EXPRV-112 | compile | `eq(1.2.3, '1.2.3')` | `{}` | `true` | docs | expr#string | - |
| EXPRV-113 | compile | `eq('1.2.3', 1.2.3)` | `{}` | `true` | docs | expr#version | - |
| EXPRV-114 | compile | `eq(1.2.3, 'v1.2.3')` | `{}` | `false` | docs | expr#string | - |
| EXPRV-115 | compile | `eq(1.2.3, '1')` | `{}` | `false` | docs | expr#string | - |
| EXPRV-116 | compile | `eq(1.2.0, '1.2')` | `{}` | `false` | reference-impl | RI:EvaluationResult.cs | - |
| EXPRV-117 | compile | `gt(1.2.0, '1.2')` | `{}` | `true` | reference-impl | RI:EvaluationResult.cs | - |
| EXPRV-118 | compile | `eq(1.2.3, 1.2)` | `{}` | `false` | docs | expr#number | - |
| EXPRV-119 | compile | `gt(1.2.3, 1.2)` | `{}` | `true` | docs | expr#number | - |
| EXPRV-120 | compile | `gt(1.2.3, 1)` | `{}` | `error:1 not convertible to Version (needs nonzero decimal)` | docs | expr#number | TRL-EXPR-0005 |
| EXPRV-121 | compile | `gt(1.2.3, 1.0)` | `{}` | `error:1.0 renders as '1', not convertible to Version` | reference-impl | RI:EvaluationResult.cs | TRL-EXPR-0005 |
| EXPRV-122 | compile | `gt(1.2.3, -1.5)` | `{}` | `error:negative number not convertible to Version` | docs | expr#number | TRL-EXPR-0005 |
| EXPRV-123 | compile | `gt(1.2.3, 0.5)` | `{}` | `true` | docs | expr#number | - |
| EXPRV-124 | compile | `eq(1, 1.2.3)` | `{}` | `false` | docs | expr#type-casting | - |
| EXPRV-125 | compile | `gt(1, 1.2.3)` | `{}` | `error:Version not convertible to Number` | docs | expr#gt | TRL-EXPR-0005 |
| EXPRV-126 | compile | `gt(1.2.3, true)` | `{}` | `error:Boolean not convertible to Version` | docs | expr#type-casting | TRL-EXPR-0005 |
| EXPRV-127 | compile | `gt(1.2.3, variables.noSuch)` | `{}` | `error:Null not convertible to Version` | docs | expr#type-casting | TRL-EXPR-0005 |
| EXPRV-128 | compile | `eq(variables.noSuch, 1.2.3)` | `{}` | `false` | docs | expr#type-casting | - |
| EXPRV-129 | compile | `lt(1.2.3, 1.10.0)` | `{}` | `true` | reference-impl | RI:EvaluationResult.cs | - |
| EXPRV-130 | compile | `lt('1.2.3', '1.10.0')` | `{}` | `false` | docs | expr#lt | - |
| EXPRV-131 | compile | `eq(parameters.list, parameters.list)` | `{"parameters":{"list":["x"]}}` | `true` | reference-impl | RI:EvaluationResult.cs | - |
| EXPRV-132 | compile | `eq(parameters.a1, parameters.a2)` | `{"parameters":{"a1":["x"],"a2":["x"]}}` | `false` | reference-impl | RI:EvaluationResult.cs | - |
| EXPRV-133 | compile | `eq(true, parameters.emptyArr)` | `{"parameters":{"emptyArr":[]}}` | `true` | reference-impl | RI:EvaluationResult.cs | - |
| EXPRV-134 | compile | `eq(1, 2, 3)` | `{}` | `error:eq takes exactly 2 parameters` | docs | expr#eq | TRL-EXPR-0001 |
| EXPRV-135 | compile | `eq(1)` | `{}` | `error:eq takes exactly 2 parameters` | docs | expr#eq | TRL-EXPR-0001 |
| EXPRV-136 | compile | `ne('a', 'A')` | `{}` | `false` | docs | expr#ne | - |
| EXPRV-137 | compile | `ne(1, 2)` | `{}` | `true` | docs | expr#ne | - |
| EXPRV-138 | compile | `gt(5, 2)` | `{}` | `true` | docs | expr#gt | - |
| EXPRV-139 | compile | `ge(5, 5)` | `{}` | `true` | docs | expr#ge | - |
| EXPRV-140 | compile | `lt(2, 5)` | `{}` | `true` | docs | expr#lt | - |
| EXPRV-141 | compile | `le(2, 2)` | `{}` | `true` | docs | expr#le | - |
| EXPRV-142 | compile | `lt(False, True)` | `{}` | `true` | docs | expr#type-casting | - |
| EXPRV-143 | compile | `lt(True, False)` | `{}` | `false` | docs | expr#type-casting | - |
| EXPRV-144 | compile | `gt(variables.n, 10)` | `{"variables":{"n":"5"}}` | `true` | docs | expr#variables | - |
| EXPRV-145 | compile | `gt(parameters.n, 10)` | `{"parameters":{"n":5}}` | `false` | docs | expr#variables | - |
| EXPRV-146 | compile | `gt(10, variables.n)` | `{"variables":{"n":"5"}}` | `true` | docs | expr#gt | - |
| EXPRV-147 | compile | `gt('b', 'A')` | `{}` | `true` | docs | expr#gt | - |
| EXPRV-148 | compile | `lt('_', 'a')` | `{}` | `false` | reference-impl | RI:EvaluationResult.cs | - |
| EXPRV-149 | compile | `le('ABC', 'abc')` | `{}` | `true` | docs | expr#le | - |
| EXPRV-150 | compile | `gt(1, 'abc')` | `{}` | `error:'abc' not convertible to Number` | docs | expr#gt | TRL-EXPR-0005 |
| EXPRV-151 | compile | `lt(1, '')` | `{}` | `false` | docs | expr#string | - |
| EXPRV-152 | compile | `ge(variables.noSuch, 0)` | `{}` | `true` | reference-impl | RI:EvaluationResult.cs | - |
| EXPRV-153 | compile | `gt(parameters.list, 1)` | `{"parameters":{"list":["x"]}}` | `error:Array not convertible to Number` | reference-impl | RI:EvaluationResult.cs | TRL-EXPR-0005 |
| EXPRV-154 | compile | `gt('abc', parameters.list)` | `{"parameters":{"list":["x"]}}` | `error:Array not convertible to String` | reference-impl | RI:EvaluationResult.cs | TRL-EXPR-0005 |
| EXPRV-155 | compile | `gt(parameters.obj, 0)` | `{"parameters":{"obj":{"a":"1"}}}` | `error:Object not convertible to Number` | reference-impl | RI:EvaluationResult.cs | TRL-EXPR-0005 |
| EXPRV-156 | compile | `not(parameters.obj)` | `{"parameters":{"obj":{}}}` | `false` | reference-impl | RI:EvaluationResult.cs | - |
| EXPRV-157 | compile | `ge(1, 2, 3)` | `{}` | `error:ge takes exactly 2 parameters` | docs | expr#ge | TRL-EXPR-0001 |
| EXPRV-158 | compile | `in('B', 'A', 'B', 'C')` | `{}` | `true` | docs | expr#in | - |
| EXPRV-159 | compile | `in('b', 'A', 'B')` | `{}` | `true` | docs | expr#in | - |
| EXPRV-160 | compile | `in(1, '1.0', 'x')` | `{}` | `true` | docs | expr#in | - |
| EXPRV-161 | compile | `in(1, 'x', 'y')` | `{}` | `false` | docs | expr#in | - |
| EXPRV-162 | compile | `in('x', 'a')` | `{}` | `false` | docs | expr#in | - |
| EXPRV-163 | compile | `in('x')` | `{}` | `false` | docs | expr#in | - |
| EXPRV-164 | compile | `in('a', 'a', length(5))` | `{}` | `true` | docs | expr#in | - |
| EXPRV-165 | compile | `in(variables.noSuch, '')` | `{}` | `true` | docs | expr#in | - |
| EXPRV-166 | compile | `in(true, 'x')` | `{}` | `true` | docs | expr#string | - |
| EXPRV-167 | compile | `notIn('D', 'A', 'B', 'C')` | `{}` | `true` | docs | expr#notin | - |
| EXPRV-168 | compile | `notIn('B', 'A', 'B')` | `{}` | `false` | docs | expr#notin | - |
| EXPRV-169 | compile | `notIn(1, 'x')` | `{}` | `true` | docs | expr#notin | - |
| EXPRV-170 | compile | `notIn('x')` | `{}` | `true` | docs | expr#notin | - |
| EXPRV-171 | compile | `contains('ABCDE', 'BCD')` | `{}` | `true` | docs | expr#contains | - |
| EXPRV-172 | compile | `contains('abcde', 'BCD')` | `{}` | `true` | docs | expr#contains | - |
| EXPRV-173 | compile | `contains('abc', '')` | `{}` | `true` | reference-impl | RI:ContainsNode.cs | - |
| EXPRV-174 | compile | `contains(123, 2)` | `{}` | `true` | docs | expr#contains | - |
| EXPRV-175 | compile | `contains(1.2.3, '2.3')` | `{}` | `true` | docs | expr#contains | - |
| EXPRV-176 | compile | `contains(parameters.list, 'x')` | `{"parameters":{"list":["x"]}}` | `error:Array not convertible to String (use containsValue)` | reference-impl | RI:ContainsNode.cs | TRL-EXPR-0005 |
| EXPRV-177 | compile | `contains(variables.noSuch, '')` | `{}` | `true` | docs | expr#null | - |
| EXPRV-178 | compile | `contains('abc')` | `{}` | `error:contains takes exactly 2 parameters` | docs | expr#contains | TRL-EXPR-0001 |
| EXPRV-179 | compile | `startsWith('ABCDE', 'ab')` | `{}` | `true` | docs | expr#startswith | - |
| EXPRV-180 | compile | `startsWith(true, 'Tr')` | `{}` | `true` | docs | expr#boolean | - |
| EXPRV-181 | compile | `startsWith('abc', '')` | `{}` | `true` | reference-impl | RI:StartsWithNode.cs | - |
| EXPRV-182 | compile | `startsWith(variables['Build.SourceBranch'], 'refs/heads/users/')` | `{"variables":{"Build.SourceBranch":"refs/heads/main"}}` | `false` | docs | cond#condition-settings-for-various-outcomes | - |
| EXPRV-183 | compile | `endsWith('ABCDE', 'DE')` | `{}` | `true` | docs | expr#endswith | - |
| EXPRV-184 | compile | `endsWith(1.50, '5')` | `{}` | `true` | reference-impl | RI:ExpressionConstants.cs | - |
| EXPRV-185 | compile | `endsWith('abc', 'B')` | `{}` | `false` | docs | expr#endswith | - |
| EXPRV-186 | compile | `containsValue(parameters.list, 'Y')` | `{"parameters":{"list":["x","y","z"]}}` | `true` | docs | expr#containsvalue | - |
| EXPRV-187 | compile | `containsValue(parameters.list, 'q')` | `{"parameters":{"list":["x","y","z"]}}` | `false` | docs | expr#containsvalue | - |
| EXPRV-188 | compile | `containsValue(parameters.obj2, 'v2')` | `{"parameters":{"obj2":{"k1":"v1","k2":"v2"}}}` | `true` | docs | expr#containsvalue | - |
| EXPRV-189 | compile | `containsValue(parameters.obj2, 'k1')` | `{"parameters":{"obj2":{"k1":"v1","k2":"v2"}}}` | `false` | docs | expr#containsvalue | - |
| EXPRV-190 | compile | `containsValue(parameters.nums, '2')` | `{"parameters":{"nums":[1,2,3]}}` | `true` | docs | expr#containsvalue | - |
| EXPRV-191 | compile | `containsValue(parameters.nums, 'x')` | `{"parameters":{"nums":[1,2,3]}}` | `false` | docs | expr#containsvalue | - |
| EXPRV-192 | compile | `containsValue(parameters.flags, true)` | `{"parameters":{"flags":["false"]}}` | `true` | docs | expr#containsvalue | - |
| EXPRV-193 | compile | `containsValue('abc', 'a')` | `{}` | `false` | reference-impl | RI:ContainsValueNode.cs | - |
| EXPRV-194 | compile | `containsValue(parameters.branchOptions, variables['Build.SourceBranch'])` | `{"parameters":{"branchOptions":["refs/heads/main","refs/heads/test"]},"variables":{"Build.SourceBranch":"refs/heads/MAIN"}}` | `true` | docs | expr#containsvalue | - |
| EXPRV-195 | compile | `coalesce(variables.emptyString, '', 'literal value')` | `{"variables":{"emptyString":""}}` | `"literal value"` | docs | expr#type-casting | - |
| EXPRV-196 | compile | `coalesce(variables.noSuch, 'x')` | `{}` | `"x"` | docs | expr#coalesce | - |
| EXPRV-197 | compile | `coalesce(' ', 'x')` | `{}` | `" "` | reference-impl | RI:CoalesceNode.cs | - |
| EXPRV-198 | compile | `coalesce(variables.noSuch, '')` | `{}` | `""` | docs | expr#coalesce | - |
| EXPRV-199 | compile | `coalesce(0, 'x')` | `{}` | `0` | docs | expr#coalesce | - |
| EXPRV-200 | compile | `coalesce(false, 'x')` | `{}` | `false` | docs | expr#coalesce | - |
| EXPRV-201 | compile | `coalesce(parameters.list, 'x')` | `{"parameters":{"list":["x","y"]}}` | `["x","y"]` | reference-impl | RI:CoalesceNode.cs | - |
| EXPRV-202 | compile | `coalesce('a', length(5))` | `{}` | `"a"` | reference-impl | RI:CoalesceNode.cs | - |
| EXPRV-203 | compile | `coalesce('a')` | `{}` | `error:coalesce requires at least 2 parameters` | docs | expr#coalesce | TRL-EXPR-0001 |
| EXPRV-204 | compile | `coalesce(parameters.customVersion, parameters.defaultVersion)` | `{"parameters":{"customVersion":"","defaultVersion":"1.0.0"}}` | `"1.0.0"` | docs | texpr#coalesce | - |
| EXPRV-205 | compile | `format('Hello {0} {1}', 'John', 'Doe')` | `{}` | `"Hello John Doe"` | docs | expr#format | - |
| EXPRV-206 | compile | `format('literal left brace {{ and literal right brace }}')` | `{}` | `"literal left brace { and literal right brace }"` | docs | expr#format | - |
| EXPRV-207 | compile | `format('{0} Build', parameters.os)` | `{"parameters":{"os":"Windows"}}` | `"Windows Build"` | docs | texpr#format | - |
| EXPRV-208 | compile | `format('{0}{0}', 'a')` | `{}` | `"aa"` | reference-impl | RI:FormatNode.cs | - |
| EXPRV-209 | compile | `format('{{0}}', 'a')` | `{}` | `"{0}"` | docs | expr#format | - |
| EXPRV-210 | compile | `format('{1}', 'a')` | `{}` | `error:format argument index out of range` | reference-impl | RI:FormatNode.cs | TRL-EXPR-0005 |
| EXPRV-211 | compile | `format('{0}', 'a', 'b')` | `{}` | `"a"` | reference-impl | RI:FormatNode.cs | - |
| EXPRV-212 | compile | `format('}')` | `{}` | `error:invalid format string (unescaped })` | reference-impl | RI:FormatNode.cs | TRL-EXPR-0005 |
| EXPRV-213 | compile | `format('{')` | `{}` | `error:invalid format string (unescaped {)` | reference-impl | RI:FormatNode.cs | TRL-EXPR-0005 |
| EXPRV-214 | compile | `format('{ 0}', 'a')` | `{}` | `error:invalid format string` | reference-impl | RI:FormatNode.cs | TRL-EXPR-0005 |
| EXPRV-215 | compile | `format('a')` | `{}` | `"a"` | docs | expr#format | - |
| EXPRV-216 | compile | `format()` | `{}` | `error:format requires at least 1 parameter` | docs | expr#format | TRL-EXPR-0001 |
| EXPRV-217 | compile | `format('{0}', 1.50)` | `{}` | `"1.5"` | reference-impl | RI:ExpressionConstants.cs | - |
| EXPRV-218 | compile | `format('{0}', 1.23456789)` | `{}` | `"1.2345679"` | reference-impl | RI:ExpressionConstants.cs | - |
| EXPRV-219 | compile | `format('{0}', 1000000)` | `{}` | `"1000000"` | docs | expr#number | - |
| EXPRV-220 | compile | `format('{0}', 12345678901234567890)` | `{}` | `"12345678901234567890"` | reference-impl | RI:ExpressionConstants.cs | - |
| EXPRV-221 | compile | `format('{0}', true)` | `{}` | `"True"` | docs | expr#boolean | - |
| EXPRV-222 | compile | `format('{0}', variables.noSuch)` | `{}` | `""` | docs | expr#null | - |
| EXPRV-223 | compile | `format('{0}', 1.2.3)` | `{}` | `"1.2.3"` | docs | expr#version | - |
| EXPRV-224 | compile | `format('{0}', 1.2.3.4)` | `{}` | `"1.2.3.4"` | docs | expr#version | - |
| EXPRV-225 | compile | `format('{0}', parameters.list)` | `{"parameters":{"list":["x"]}}` | `error:Array not convertible to String` | reference-impl | RI:FormatNode.cs | TRL-EXPR-0005 |
| EXPRV-226 | compile | `format('{0}', parameters.obj)` | `{"parameters":{"obj":{"a":"1"}}}` | `error:Object not convertible to String` | reference-impl | RI:FormatNode.cs | TRL-EXPR-0005 |
| EXPRV-227 | compile | `format('{0:yyyy}', 'abc')` | `{}` | `error:format specifiers only valid for DateTime` | reference-impl | RI:FormatNode.cs | TRL-EXPR-0005 |
| EXPRV-228 | runtime | `format('{0:yyyyMMdd}', pipeline.startTime)` | `{"pipeline":{"startTime":"2024-03-05T07:08:09.1234567Z"}}` | `"20240305"` | docs | expr#format | - |
| EXPRV-229 | runtime | `format('{0:yyyy MM dd}', pipeline.startTime)` | `{"pipeline":{"startTime":"2024-03-05T07:08:09.1234567Z"}}` | `"2024 03 05"` | reference-impl | RI:FormatNode.cs | - |
| EXPRV-230 | runtime | `format('{0:HHmmss}', pipeline.startTime)` | `{"pipeline":{"startTime":"2024-03-05T07:08:09.1234567Z"}}` | `"070809"` | docs | expr#format | - |
| EXPRV-231 | runtime | `format('{0:yy}{0:M}{0:d}{0:H}{0:m}{0:s}', pipeline.startTime)` | `{"pipeline":{"startTime":"2024-03-05T07:08:09.1234567Z"}}` | `"2435789"` | docs | expr#format | - |
| EXPRV-232 | runtime | `format('{0:M}.{0:d}', pipeline.startTime)` | `{"pipeline":{"startTime":"2024-03-05T07:08:09.1234567Z"}}` | `"3.5"` | reference-impl | RI:FormatNode.cs | - |
| EXPRV-233 | runtime | `format('{0:fff}', pipeline.startTime)` | `{"pipeline":{"startTime":"2024-03-05T07:08:09.1234567Z"}}` | `"123"` | reference-impl | RI:FormatNode.cs | - |
| EXPRV-234 | runtime | `format('{0:yyyy\-MM\-dd}', pipeline.startTime)` | `{"pipeline":{"startTime":"2024-03-05T07:08:09.1234567Z"}}` | `"2024-03-05"` | reference-impl | RI:FormatNode.cs | - |
| EXPRV-235 | runtime | `format('{0:yyyy-MM-dd}', pipeline.startTime)` | `{"pipeline":{"startTime":"2024-03-05T07:08:09.1234567Z"}}` | `error:invalid DateTime format specifier '-'` | reference-impl | RI:FormatNode.cs | TRL-EXPR-0005 |
| EXPRV-236 | runtime | `format('{0:yyyy}-{0:MM}-{0:dd}', pipeline.startTime)` | `{"pipeline":{"startTime":"2024-03-05T07:08:09.1234567Z"}}` | `"2024-03-05"` | docs | expr#format | - |
| EXPRV-237 | runtime | `format('{0:HH:mm}', pipeline.startTime)` | `{"pipeline":{"startTime":"2024-03-05T07:08:09.1234567Z"}}` | `error:invalid DateTime format specifier ':'` | reference-impl | RI:FormatNode.cs | TRL-EXPR-0005 |
| EXPRV-238 | runtime | `format('{0}', pipeline.startTime)` | `{"pipeline":{"startTime":"2024-03-05T07:08:09Z"}}` | `"2024-03-05 07:08:09+00:00"` | reference-impl | RI:ExpressionConstants.cs | - |
| EXPRV-239 | runtime | `eq(pipeline.startTime, '2024-03-05 07:08:09.1234567Z')` | `{"pipeline":{"startTime":"2024-03-05T07:08:09.1234567Z"}}` | `true` | reference-impl | RI:EvaluationResult.cs | - |
| EXPRV-240 | runtime | `gt(pipeline.startTime, 5)` | `{"pipeline":{"startTime":"2024-03-05T07:08:09Z"}}` | `error:Number not convertible to DateTime` | reference-impl | RI:EvaluationResult.cs | TRL-EXPR-0005 |
| EXPRV-241 | runtime | `and(pipeline.startTime, true)` | `{"pipeline":{"startTime":"2024-03-05T07:08:09Z"}}` | `true` | reference-impl | RI:EvaluationResult.cs | - |
| EXPRV-242 | compile | `join(';', parameters.myArray)` | `{"parameters":{"myArray":["FOO","BAR","ZOO"]}}` | `"FOO;BAR;ZOO"` | docs | expr#join | - |
| EXPRV-243 | compile | `join(',', 'abc')` | `{}` | `"abc"` | docs | expr#join | - |
| EXPRV-244 | compile | `join(',', 5)` | `{}` | `"5"` | docs | expr#join | - |
| EXPRV-245 | compile | `join(',', true)` | `{}` | `"True"` | docs | expr#join | - |
| EXPRV-246 | compile | `join(',', parameters.mixed)` | `{"parameters":{"mixed":["a",{"k":"v"},"b"]}}` | `"a,,b"` | docs | expr#join | - |
| EXPRV-247 | compile | `join(', ', parameters.typed)` | `{"parameters":{"typed":[1.50,true,null]}}` | `"1.5, True, "` | reference-impl | RI:JoinNode.cs | - |
| EXPRV-248 | compile | `join(',', parameters.emptyArr)` | `{"parameters":{"emptyArr":[]}}` | `""` | reference-impl | RI:JoinNode.cs | - |
| EXPRV-249 | compile | `join(',', parameters.one)` | `{"parameters":{"one":["solo"]}}` | `"solo"` | reference-impl | RI:JoinNode.cs | - |
| EXPRV-250 | compile | `join(',', parameters.obj2)` | `{"parameters":{"obj2":{"k1":"v1"}}}` | `""` | reference-impl | RI:JoinNode.cs | - |
| EXPRV-251 | compile | `join(variables.noSuch, parameters.list)` | `{"parameters":{"list":["x","y","z"]}}` | `"xyz"` | reference-impl | RI:JoinNode.cs | - |
| EXPRV-252 | compile | `join(',', variables.noSuch)` | `{}` | `""` | docs | expr#join | - |
| EXPRV-253 | compile | `join(',')` | `{}` | `error:join takes exactly 2 parameters` | docs | expr#join | TRL-EXPR-0001 |
| EXPRV-254 | compile | `length('fabrikam')` | `{}` | `8` | docs | expr#length | - |
| EXPRV-255 | compile | `length('')` | `{}` | `0` | docs | expr#length | - |
| EXPRV-256 | compile | `length('😀')` | `{}` | `2` | reference-impl | RP:LengthNode.cs | - |
| EXPRV-257 | compile | `length('é')` | `{}` | `1` | reference-impl | RP:LengthNode.cs | - |
| EXPRV-258 | compile | `length(parameters.list)` | `{"parameters":{"list":["x","y","z"]}}` | `3` | docs | expr#length | - |
| EXPRV-259 | compile | `length(parameters.emptyArr)` | `{"parameters":{"emptyArr":[]}}` | `0` | docs | expr#length | - |
| EXPRV-260 | compile | `length(parameters.obj2)` | `{"parameters":{"obj2":{"k1":"v1","k2":"v2"}}}` | `2` | reference-impl | RP:LengthNode.cs | - |
| EXPRV-261 | compile | `length(variables.n)` | `{"variables":{"n":"12"}}` | `2` | docs | expr#variables | - |
| EXPRV-262 | compile | `length(5)` | `{}` | `error:length not supported for Number` | reference-impl | RP:LengthNode.cs | TRL-EXPR-0005 |
| EXPRV-263 | compile | `length(true)` | `{}` | `error:length not supported for Boolean` | reference-impl | RP:LengthNode.cs | TRL-EXPR-0005 |
| EXPRV-264 | compile | `length(variables.noSuch)` | `{}` | `error:length not supported for Null` | reference-impl | RP:LengthNode.cs | TRL-EXPR-0005 |
| EXPRV-265 | compile | `length('a', 'b')` | `{}` | `error:length takes exactly 1 parameter` | docs | expr#length | TRL-EXPR-0001 |
| EXPRV-266 | compile | `lower('FOO')` | `{}` | `"foo"` | docs | expr#lower | - |
| EXPRV-267 | compile | `upper('bah')` | `{}` | `"BAH"` | docs | expr#upper | - |
| EXPRV-268 | compile | `lower(true)` | `{}` | `"true"` | inferred | expr#lower | - |
| EXPRV-269 | compile | `upper(variables.noSuch)` | `{}` | `""` | inferred | expr#upper | - |
| EXPRV-270 | compile | `upper('é')` | `{}` | `"É"` | inferred | expr#upper | - |
| EXPRV-271 | compile | `trim('  variable  ')` | `{}` | `"variable"` | docs | expr#trim | - |
| EXPRV-272 | compile | `trim(variables.ws)` | `{"variables":{"ws":"\t a b \n"}}` | `"a b"` | inferred | expr#trim | - |
| EXPRV-273 | compile | `trim('a', 'b')` | `{}` | `error:trim takes exactly 1 parameter` | docs | expr#trim | TRL-EXPR-0001 |
| EXPRV-274 | compile | `replace('https://www.tinfoilsecurity.com/saml/consume','https://www.tinfoilsecurity.com','http://server')` | `{}` | `"http://server/saml/consume"` | docs | expr#replace | - |
| EXPRV-275 | compile | `replace('aAa', 'a', 'b')` | `{}` | `"bAb"` | inferred | expr#replace | - |
| EXPRV-276 | compile | `replace('abc', 'b', '')` | `{}` | `"ac"` | docs | expr#replace | - |
| EXPRV-277 | compile | `replace('aaa', 'aa', 'b')` | `{}` | `"ba"` | inferred | expr#replace | - |
| EXPRV-278 | compile | `replace('a-b-c', '-', '_')` | `{}` | `"a_b_c"` | docs | expr#replace | - |
| EXPRV-279 | compile | `replace('abc', '', 'x')` | `{}` | `error:empty search string` | inferred | expr#replace | TRL-EXPR-0005 |
| EXPRV-280 | compile | `replace('a', 'b')` | `{}` | `error:replace takes exactly 3 parameters` | docs | expr#replace | TRL-EXPR-0001 |
| EXPRV-281 | compile | `split('prod1,prod2', ',')` | `{}` | `["prod1","prod2"]` | docs | expr#split | - |
| EXPRV-282 | compile | `split('a,,b,', ',')` | `{}` | `["a","","b",""]` | docs | expr#split | - |
| EXPRV-283 | compile | `split('', ',')` | `{}` | `[""]` | inferred | expr#split | - |
| EXPRV-284 | compile | `split('a;b,c', ';,')` | `{}` | `["a","b","c"]` | inferred | expr#split | - |
| EXPRV-285 | compile | `replace(split(parameters.rid, '/')[8], '-', '_')` | `{"parameters":{"rid":"/subscriptions/s/resourceGroups/g/providers/Microsoft.Network/loadBalancers/kubernetes-internal"}}` | `"kubernetes_internal"` | docs | expr#split | - |
| EXPRV-286 | compile | `split('a')` | `{}` | `error:split takes exactly 2 parameters` | docs | expr#split | TRL-EXPR-0001 |
| EXPRV-287 | runtime | `iif(eq(variables['Build.Reason'], 'PullRequest'), 'ManagedDevOpsPool', 'Azure Pipelines')` | `{"variables":{"Build.Reason":"PullRequest"}}` | `"ManagedDevOpsPool"` | docs | expr#iif | - |
| EXPRV-288 | runtime | `iif(eq(variables['Build.Reason'], 'PullRequest'), 'ManagedDevOpsPool', 'Azure Pipelines')` | `{"variables":{"Build.Reason":"Manual"}}` | `"Azure Pipelines"` | docs | expr#iif | - |
| EXPRV-289 | compile | `iif('', 'a', 'b')` | `{}` | `"b"` | inferred | expr#iif | - |
| EXPRV-290 | compile | `iif('false', 'a', 'b')` | `{}` | `"a"` | inferred | expr#string | - |
| EXPRV-291 | compile | `iif(true, 'a')` | `{}` | `"a"` | inferred | expr#iif | - |
| EXPRV-292 | compile | `iif(true, 'a', 'b', 'c')` | `{}` | `error:iif takes at most 3 parameters` | docs | expr#iif | TRL-EXPR-0001 |
| EXPRV-293 | compile | `convertToJson(parameters.listOfValues)` | `{"parameters":{"listOfValues":{"this_is":{"a_complex":"object","with":["one","two"]}}}}` | `"{\n  \"this_is\": {\n    \"a_complex\": \"object\",\n    \"with\": [\n      \"one\",\n      \"two\"\n    ]\n  }\n}"` | docs | expr#converttojson | - |
| EXPRV-294 | compile | `convertToJson(parameters.o)` | `{"parameters":{"o":{"z":"1","a":"2"}}}` | `"{\n  \"z\": \"1\",\n  \"a\": \"2\"\n}"` | inferred | expr#converttojson | - |
| EXPRV-295 | compile | `convertToJson(parameters.o)` | `{"parameters":{"o":{"q":"say \"hi\" \\ é"}}}` | `"{\n  \"q\": \"say \\\"hi\\\" \\\\ é\"\n}"` | inferred | expr#converttojson | - |
| EXPRV-296 | compile | `convertToJson(parameters.emptyArr)` | `{"parameters":{"emptyArr":[]}}` | `"[]"` | inferred | expr#converttojson | - |
| EXPRV-297 | compile | `convertToJson(parameters.o)` | `{"parameters":{"o":{"n":1,"b":true}}}` | `"{\n  \"n\": 1,\n  \"b\": true\n}"` | inferred | expr#converttojson | - |
| EXPRV-298 | compile | `convertToJson('abc')` | `{}` | `"\"abc\""` | inferred | expr#converttojson | - |
| EXPRV-299 | compile | `convertToJson(parameters.o, 1)` | `{"parameters":{"o":{}}}` | `error:convertToJson takes exactly 1 parameter` | docs | expr#converttojson | TRL-EXPR-0001 |
| EXPRV-300 | runtime | `$[counter(variables['major'], 100)]` | `{"variables":{"major":"1"},"counters":{}}` | `"100"` | docs | expr#counter | - |
| EXPRV-301 | runtime | `$[counter(variables['major'], 100)]` | `{"variables":{"major":"1"},"counters":{"1":100}}` | `"101"` | docs | expr#counter | - |
| EXPRV-302 | runtime | `$[counter(variables['major'], 100)]` | `{"variables":{"major":"2"},"counters":{"1":101}}` | `"100"` | docs | expr#counter | - |
| EXPRV-303 | runtime | `$[counter(variables['major'], 100)]` | `{"variables":{"major":"1"},"counters":{"1":101,"2":100}}` | `"102"` | docs | expr#counter | - |
| EXPRV-304 | runtime | `$[counter(format('{0:yyyyMMdd}', pipeline.startTime), 100)]` | `{"pipeline":{"startTime":"2024-03-05T07:08:09Z"},"counters":{"20240304":105}}` | `"100"` | docs | expr#counter | - |
| EXPRV-305 | runtime | `$[counter(variables['build.reason'], 0)]` | `{"variables":{"Build.Reason":"PullRequest"},"counters":{"IndividualCI":7}}` | `"0"` | docs | expr#counter | - |
| EXPRV-306 | runtime | `counter('a', 1)` (as a step `condition:`) | `{"counters":{}}` | `error:counter not allowed in conditions` | docs | expr#counter | TRL-EXPR-0001 |
| EXPRV-307 | compile | `counter('a', 1)` | `{}` | `error:counter not available at compile time` | inferred | expr#counter | TRL-EXPR-0001 |
| EXPRV-308 | runtime | `$[counter('a')]` | `{"counters":{}}` | `"0"` | reference-impl | RP:CounterNode.cs | - |
| EXPRV-309 | runtime | `$[counter('a', '5')]` | `{"counters":{}}` | `"5"` | reference-impl | RP:CounterNode.cs | - |
| EXPRV-310 | runtime | `$[counter('a', 'abc')]` | `{"counters":{}}` | `error:seed not convertible to Number` | reference-impl | RP:CounterNode.cs | TRL-EXPR-0005 |
| EXPRV-311 | runtime | `$[counter('a', 1, 2)]` | `{"counters":{}}` | `error:counter takes at most 2 parameters` | docs | expr#counter | TRL-EXPR-0001 |
| EXPRV-312 | runtime | `succeeded()` | `{"scope":"step","canceled":false,"variables":{"Agent.JobStatus":"Succeeded"}}` | `true` | docs | expr#succeeded | - |
| EXPRV-313 | runtime | `succeeded()` | `{"scope":"step","canceled":false,"variables":{"Agent.JobStatus":"SucceededWithIssues"}}` | `true` | docs | expr#succeeded | - |
| EXPRV-314 | runtime | `succeeded()` | `{"scope":"step","canceled":false,"variables":{"Agent.JobStatus":"Failed"}}` | `false` | docs | expr#succeeded | - |
| EXPRV-315 | runtime | `failed()` | `{"scope":"step","canceled":false,"variables":{"Agent.JobStatus":"Failed"}}` | `true` | docs | expr#failed | - |
| EXPRV-316 | runtime | `failed()` | `{"scope":"step","canceled":false,"variables":{"Agent.JobStatus":"SucceededWithIssues"}}` | `false` | docs | expr#failed | - |
| EXPRV-317 | runtime | `succeededOrFailed()` | `{"scope":"step","canceled":false,"variables":{"Agent.JobStatus":"Failed"}}` | `true` | docs | expr#succeededorfailed | - |
| EXPRV-318 | runtime | `succeededOrFailed()` | `{"scope":"step","canceled":true,"variables":{"Agent.JobStatus":"Canceled"}}` | `false` | docs | expr#succeededorfailed | - |
| EXPRV-319 | runtime | `always()` | `{"scope":"step","canceled":true,"variables":{"Agent.JobStatus":"Canceled"}}` | `true` | docs | expr#always | - |
| EXPRV-320 | runtime | `canceled()` | `{"scope":"step","canceled":true,"variables":{"Agent.JobStatus":"Canceled"}}` | `true` | docs | expr#canceled | - |
| EXPRV-321 | runtime | `canceled()` | `{"scope":"step","canceled":false,"variables":{"Agent.JobStatus":"Failed"}}` | `false` | docs | expr#canceled | - |
| EXPRV-322 | runtime | `not(canceled())` | `{"scope":"job","canceled":false,"dependencies":{"A":{"result":"Skipped","outputs":{}}}}` | `true` | docs | expr#succeededorfailed | - |
| EXPRV-323 | runtime | `and(succeeded(), eq(variables['Build.SourceBranch'], 'refs/heads/main'))` | `{"scope":"step","canceled":false,"variables":{"Agent.JobStatus":"Succeeded","Build.SourceBranch":"refs/heads/main"}}` | `true` | docs | cond#condition-settings-for-various-outcomes | - |
| EXPRV-324 | runtime | `succeeded()` | `{"scope":"job","canceled":false,"dependencies":{"A":{"result":"Succeeded","outputs":{}},"B":{"result":"SucceededWithIssues","outputs":{}}}}` | `true` | docs | expr#succeeded | - |
| EXPRV-325 | runtime | `succeeded()` | `{"scope":"job","canceled":false,"dependencies":{"A":{"result":"Succeeded","outputs":{}},"B":{"result":"Failed","outputs":{}}}}` | `false` | docs | expr#succeeded | - |
| EXPRV-326 | runtime | `succeeded('A')` | `{"scope":"job","canceled":false,"dependencies":{"A":{"result":"Succeeded","outputs":{}},"B":{"result":"Failed","outputs":{}}}}` | `true` | docs | expr#succeeded | - |
| EXPRV-327 | runtime | `succeeded('A', 'B')` | `{"scope":"job","canceled":false,"dependencies":{"A":{"result":"Succeeded","outputs":{}},"B":{"result":"Failed","outputs":{}}}}` | `false` | docs | expr#succeeded | - |
| EXPRV-328 | runtime | `failed()` | `{"scope":"job","canceled":false,"dependencies":{"A":{"result":"Succeeded","outputs":{}},"B":{"result":"Failed","outputs":{}}}}` | `true` | docs | expr#failed | - |
| EXPRV-329 | runtime | `failed('A')` | `{"scope":"job","canceled":false,"dependencies":{"A":{"result":"Succeeded","outputs":{}},"B":{"result":"Failed","outputs":{}}}}` | `false` | docs | expr#failed | - |
| EXPRV-330 | runtime | `failed('b')` | `{"scope":"job","canceled":false,"dependencies":{"A":{"result":"Succeeded","outputs":{}},"B":{"result":"Failed","outputs":{}}}}` | `true` | inferred | expr#failed | - |
| EXPRV-331 | runtime | `succeededOrFailed('A', 'B')` | `{"scope":"job","canceled":false,"dependencies":{"A":{"result":"Succeeded","outputs":{}},"B":{"result":"Failed","outputs":{}}}}` | `true` | docs | expr#succeededorfailed | - |
| EXPRV-332 | runtime | `succeededOrFailed()` | `{"scope":"job","canceled":true,"dependencies":{"A":{"result":"Canceled","outputs":{}}}}` | `false` | docs | expr#succeededorfailed | - |
| EXPRV-333 | runtime | `succeeded()` | `{"scope":"job","canceled":true,"dependencies":{"A":{"result":"Succeeded","outputs":{}}}}` | `false` | docs | expr#succeeded | - |
| EXPRV-334 | runtime | `succeeded()` | `{"scope":"job","canceled":false,"dependencies":{}}` | `true` | docs | cond#parameters-in-conditions | - |
| EXPRV-335 | runtime | `succeeded()` | `{"scope":"job","canceled":false,"dependencies":{"A":{"result":"Skipped","outputs":{}}}}` | `false` | inferred | cond#conditions-a-stage-job-or-step-runs-under | - |
| EXPRV-336 | runtime | `and(succeeded(), eq(dependencies.A.outputs['printvar.shouldrun'], 'true'))` | `{"scope":"job","canceled":false,"dependencies":{"A":{"result":"Succeeded","outputs":{"printvar.shouldrun":"true"}}}}` | `true` | docs | expr#job-to-job-dependencies-within-one-stage | - |
| EXPRV-337 | runtime | `eq(dependencies.A.result, 'SucceededWithIssues')` | `{"scope":"job","canceled":false,"dependencies":{"A":{"result":"SucceededWithIssues","outputs":{}}}}` | `true` | docs | cond#how-can-i-trigger-a-job-if-a-previous-job-succeeded-with-issues | - |
| EXPRV-338 | runtime | `$[ succeeded() ]` | `{"scope":"job","canceled":false,"dependencies":{}}` | `error:status functions not allowed in variable definitions` | docs | expr#job-status-functions | TRL-EXPR-0001 |
| EXPRV-339 | compile | `succeeded()` | `{}` | `error:status functions not available at compile time` | inferred | expr#job-status-functions | TRL-EXPR-0001 |
| EXPRV-340 | runtime | `always('A')` | `{"scope":"job","canceled":false,"dependencies":{"A":{"result":"Succeeded","outputs":{}}}}` | `error:always takes no parameters` | inferred | expr#always | TRL-EXPR-0001 |
| EXPRV-341 | runtime | `succeeded('C')` | `{"scope":"job","canceled":false,"dependencies":{"A":{"result":"Succeeded","outputs":{}}}}` | `error:'C' is not a dependency` | inferred | expr#succeeded | TRL-EXPR-0001 |
| EXPRV-342 | runtime | `$[eq(variables['Build.SourceBranch'], 'refs/heads/main')]` | `{"variables":{"Build.SourceBranch":"refs/heads/main"}}` | `"True"` | docs | expr#expressions | - |
| EXPRV-343 | runtime | `$[ variables.noSuch ]` | `{}` | `""` | docs | vars#runtime-expression-syntax | - |
| EXPRV-344 | runtime | `$[lt(False, True)]` | `{}` | `"True"` | docs | expr#type-casting | - |
| EXPRV-345 | runtime | `$[eq('false', true)]` | `{}` | `"False"` | docs | expr#type-casting | - |
| EXPRV-346 | runtime | `$[ 1.50 ]` | `{}` | `"1.5"` | reference-impl | RI:ExpressionConstants.cs | - |
| EXPRV-347 | runtime | `$[ coalesce(variables.a, 'd') ]` | `{"variables":{"a":""}}` | `"d"` | docs | expr#coalesce | - |
| EXPRV-348 | runtime | `$[ variables['simpleVar'] ]` | `{"variables":{"simpleVar":"hello"}}` | `"hello"` | docs | vars#runtime-expression-syntax | - |
| EXPRV-349 | runtime | `$[ parameters.x ]` | `{"parameters":{"x":"1"}}` | `error:parameters not available at runtime` | docs | expr#expressions | TRL-EXPR-0001 |
| EXPRV-350 | runtime | `foo $[variables.a]` | `{"variables":{"a":"1"}}` | `"foo $[variables.a]"` | inferred | vars#runtime-expression-syntax | - |
| EXPRV-351 | compile | `dependencies.A.result` | `{}` | `error:dependencies not available at compile time` | docs | expr#expressions | TRL-EXPR-0001 |
| EXPRV-352 | compile | `variables.staticVar` | `{"variables":{"staticVar":"my value"}}` | `"my value"` | docs | expr#expressions | - |
| EXPRV-353 | compile | `eq(variables.n, 5)` | `{"variables":{"n":"5"}}` | `true` | docs | expr#variables | - |
| EXPRV-354 | compile | `eq(parameters.doThing, true)` | `{"parameters":{"doThing":true}}` | `true` | docs | cond#parameters-in-conditions | - |
| EXPRV-355 | compile | `[{"${{ if eq(parameters.toolset, 'dotnet') }}":["UseDotNet@2"]},"done"]` | `{"parameters":{"toolset":"dotnet"}}` | `["UseDotNet@2","done"]` | docs | texpr#conditional-insertion | - |
| EXPRV-356 | compile | `[{"${{ if eq(parameters.toolset, 'dotnet') }}":["UseDotNet@2"]},"done"]` | `{"parameters":{"toolset":"vsbuild"}}` | `["done"]` | docs | texpr#conditional-insertion | - |
| EXPRV-357 | compile | `[{"${{ if eq(parameters.toolset, 'dotnet') }}":["A","B"]},"done"]` | `{"parameters":{"toolset":"DOTNET"}}` | `["A","B","done"]` | docs | expr#eq | - |
| EXPRV-358 | compile | `[{"${{ if eq(variables.foo, 'adaptum') }}":["A"]},{"${{ elseif eq(variables.foo, 'contoso') }}":["C"]},{"${{ else }}":["D"]}]` | `{"variables":{"foo":"contoso"}}` | `["C"]` | docs | expr#conditional-insertion | - |
| EXPRV-359 | compile | `[{"${{ if eq(variables.foo, 'adaptum') }}":["A"]},{"${{ elseif eq(variables.foo, 'contoso') }}":["C"]},{"${{ else }}":["D"]}]` | `{"variables":{"foo":"adaptum"}}` | `["A"]` | docs | expr#conditional-insertion | - |
| EXPRV-360 | compile | `[{"${{ if eq(variables.foo, 'adaptum') }}":["A"]},{"${{ elseif eq(variables.foo, 'contoso') }}":["C"]},{"${{ else }}":["D"]}]` | `{}` | `["D"]` | docs | expr#conditional-insertion | - |
| EXPRV-361 | compile | `{"targetPath":"x","${{ if eq(variables['Build.SourceBranchName'], 'main') }}":{"artifact":"prod"},"${{ else }}":{"artifact":"dev"},"publishLocation":"pipeline"}` | `{"variables":{"Build.SourceBranchName":"main"}}` | `{"targetPath":"x","artifact":"prod","publishLocation":"pipeline"}` | docs | expr#conditionally-set-a-task-input | - |
| EXPRV-362 | compile | `{"targetPath":"x","${{ if eq(variables['Build.SourceBranchName'], 'main') }}":{"artifact":"prod"},"${{ else }}":{"artifact":"dev"},"publishLocation":"pipeline"}` | `{"variables":{"Build.SourceBranchName":"feature"}}` | `{"targetPath":"x","artifact":"dev","publishLocation":"pipeline"}` | docs | expr#conditionally-set-a-task-input | - |
| EXPRV-363 | compile | `[{"name":"myVar","value":"baz"},{"name":"conditionalVar","${{ if eq(variables['myVar'], 'foo') }}":{"value":"bar"},"${{ elseif eq(variables['myVar'], 'baz') }}":{"value":"qux"},"${{ else }}":{"value":"default"}}]` | `{"variables":{"myVar":"baz"}}` | `[{"name":"myVar","value":"baz"},{"name":"conditionalVar","value":"qux"}]` | docs | texpr#conditional-insertion | - |
| EXPRV-364 | compile | `{"script":"tool","env":{"${{ if eq(parameters.debug, true) }}":{"TOOL_DEBUG":"true","TOOL_DEBUG_DIR":"_dbg"}}}` | `{"parameters":{"debug":true}}` | `{"script":"tool","env":{"TOOL_DEBUG":"true","TOOL_DEBUG_DIR":"_dbg"}}` | docs | texpr#conditional-insertion | - |
| EXPRV-365 | compile | `{"script":"tool","env":{"${{ if eq(parameters.debug, true) }}":{"TOOL_DEBUG":"true"}}}` | `{"parameters":{"debug":false}}` | `{"script":"tool","env":{}}` | docs | texpr#conditional-insertion | - |
| EXPRV-366 | compile | `[{"${{ elseif true }}":["x"]}]` | `{}` | `error:elseif without preceding if` | inferred | expr#conditional-insertion | TRL-EXPR-0001 |
| EXPRV-367 | compile | `[{"${{ if false }}":["a"]},"mid",{"${{ else }}":["b"]}]` | `{}` | `error:else not immediately after if/elseif` | inferred | expr#conditional-insertion | TRL-EXPR-0001 |
| EXPRV-368 | compile | `[{"${{ if 'false' }}":["a"]},"b"]` | `{}` | `["a","b"]` | docs | expr#string | - |
| EXPRV-369 | compile | `[{"${{ if variables.noSuch }}":["a"]},"b"]` | `{}` | `["b"]` | docs | expr#null | - |
| EXPRV-370 | compile | `[{"${{ if parameters.emptyArr }}":["a"]},"b"]` | `{"parameters":{"emptyArr":[]}}` | `["a","b"]` | reference-impl | RI:EvaluationResult.cs | - |
| EXPRV-371 | compile | `[{"${{ if job.dependsOn }}":["${{ job.dependsOn }}"]}]` (inside `each job`, job lacks dependsOn) | `{"job":{"job":"A"}}` | `[]` | docs | texpr#iterative-insertion | - |
| EXPRV-372 | compile | `[{"${{ each value in parameters.listOfStrings }}":[{"script":"echo ${{ value }}"}]}]` | `{"parameters":{"listOfStrings":["one","two"]}}` | `[{"script":"echo one"},{"script":"echo two"}]` | docs | expr#each-keyword | - |
| EXPRV-373 | compile | `[{"${{ each fruit in parameters.listOfFruits }}":[{"${{ each fruitColor in fruit.colors }}":[{"script":"echo ${{ fruit.fruitName }} ${{ fruitColor }}"}]}]}]` | `{"parameters":{"listOfFruits":[{"fruitName":"apple","colors":["red","green"]},{"fruitName":"lemon","colors":["yellow"]}]}}` | `[{"script":"echo apple red"},{"script":"echo apple green"},{"script":"echo lemon yellow"}]` | docs | expr#each-keyword | - |
| EXPRV-374 | compile | `{"${{ each pair in parameters.obj }}":{"${{ pair.key }}":"${{ pair.value }}"}}` | `{"parameters":{"obj":{"zeta":"1","alpha":"2"}}}` | `{"zeta":"1","alpha":"2"}` | inferred | texpr#iterative-insertion | - |
| EXPRV-375 | compile | `[{"${{ each parameter in parameters }}":["${{ parameter.Key }}=${{ parameter.Value }}"]}]` | `{"parameters":{"myStringName":"a string value","myNumber":2,"myBoolean":true}}` | `["myStringName=a string value","myNumber=2","myBoolean=True"]` | docs | tparam#iterating-through-simple-parameters | - |
| EXPRV-376 | compile | `[{"${{ each job in parameters.jobs }}":[{"${{ each pair in job }}":{"${{ if ne(pair.key, 'steps') }}":{"${{ pair.key }}":"${{ pair.value }}"}},"steps":["Setup","${{ job.steps }}","Post"]}]}]` | `{"parameters":{"jobs":[{"job":"A","steps":["s1"]},{"job":"B","steps":["s2","s3"]}]}}` | `[{"job":"A","steps":["Setup","s1","Post"]},{"job":"B","steps":["Setup","s2","s3","Post"]}]` | docs | texpr#iterative-insertion | - |
| EXPRV-377 | compile | `["a",{"${{ each v in parameters.emptyArr }}":["${{ v }}"]},"b"]` | `{"parameters":{"emptyArr":[]}}` | `["a","b"]` | inferred | texpr#iterative-insertion | - |
| EXPRV-378 | compile | `[{"${{ each env in split(variables.environments, ',') }}":["deploy ${{ env }}"]}]` | `{"variables":{"environments":"prod1,prod2"}}` | `["deploy prod1","deploy prod2"]` | docs | expr#split | - |
| EXPRV-379 | compile | `[{"${{ each stage in parameters.regions }}":[{"stage":"${{ stage }}","displayName":"Deploy to ${{ stage }}"}]}]` | `{"parameters":{"regions":["WUS","EUS"]}}` | `[{"stage":"WUS","displayName":"Deploy to WUS"},{"stage":"EUS","displayName":"Deploy to EUS"}]` | docs | texpr#iterative-insertion | - |
| EXPRV-380 | compile | `{"configuration":"debug","arch":"x86","${{ insert }}":"${{ parameters.additionalVariables }}"}` | `{"parameters":{"additionalVariables":{"TEST_SUITE":"L0,L1"}}}` | `{"configuration":"debug","arch":"x86","TEST_SUITE":"L0,L1"}` | docs | texpr#insertion | - |
| EXPRV-381 | compile | `{"configuration":"debug","${{ insert }}":"${{ parameters.additionalVariables }}"}` | `{"parameters":{"additionalVariables":{}}}` | `{"configuration":"debug"}` | docs | texpr#insertion | - |
| EXPRV-382 | compile | `["cred-scan","${{ parameters.preBuild }}","VSBuild@1","${{ parameters.preTest }}","sign"]` | `{"parameters":{"preBuild":["hello","hello2"],"preTest":[]}}` | `["cred-scan","hello","hello2","VSBuild@1","sign"]` | docs | texpr#insertion | - |
| EXPRV-383 | compile | `{"${{ variables.key }}":"${{ variables.value }}"}` | `{"variables":{"key":"k","value":"v"}}` | `{"k":"v"}` | docs | vars#template-expression-syntax | - |
| EXPRV-384 | compile | `{"x":"${{ parameters.n }}"}` | `{"parameters":{"n":2}}` | `{"x":2}` | reference-impl | RT:TemplateReader.cs | - |
| EXPRV-385 | compile | `{"x":"${{ parameters.list }}"}` | `{"parameters":{"list":["a","b"]}}` | `{"x":["a","b"]}` | reference-impl | RT:TemplateReader.cs | - |
| EXPRV-386 | compile | `"pre-${{ parameters.a }}-${{ parameters.n }}"` | `{"parameters":{"a":"x","n":1.50}}` | `"pre-x-1.5"` | reference-impl | RT:TemplateReader.cs | - |
| EXPRV-387 | compile | `"flag=${{ parameters.b }}"` | `{"parameters":{"b":true}}` | `"flag=True"` | reference-impl | RT:TemplateReader.cs | - |
| EXPRV-388 | compile | `"items=${{ parameters.list }}"` | `{"parameters":{"list":["a"]}}` | `error:Array not convertible to String in embedded expression` | reference-impl | RT:TemplateReader.cs | TRL-EXPR-0005 |
| EXPRV-389 | compile | `"echo ${{ variables.nope }}!"` | `{}` | `"echo !"` | docs | vars#template-expression-syntax | - |
| EXPRV-390 | compile | `"${{ 'my${{value' }}"` | `{}` | `"my${{value"` | docs | texpr#escape-a-value | - |
| EXPRV-391 | compile | `"${{ 'my${{value with a '' single quote too' }}"` | `{}` | `"my${{value with a ' single quote too"` | docs | texpr#escape-a-value | - |
| EXPRV-392 | compile | `"${{ '}}' }}"` | `{}` | `"}}"` | reference-impl | RT:TemplateReader.cs | - |
| EXPRV-393 | compile | `"a }} b {{ c"` | `{}` | `"a }} b {{ c"` | reference-impl | RT:TemplateReader.cs | - |
| EXPRV-394 | compile | `"${{ format('{0}{{x}}', 'a') }}-{y}"` | `{}` | `"a{x}-{y}"` | reference-impl | RT:TemplateReader.cs | - |
| EXPRV-395 | compile | `"echo ${{ if true }}"` | `{}` | `error:directive not allowed inline` | reference-impl | RT:TemplateReader.cs | TRL-EXPR-0001 |
| EXPRV-396 | compile | `"${{ parameters.a"` | `{"parameters":{"a":"x"}}` | `error:expression not closed` | reference-impl | RT:TemplateReader.cs | TRL-EXPR-0001 |
| EXPRV-397 | compile | `"${{ '$(a)' }}"` then macro with `a=1` | `{"variables":{"a":"1"}}` | `"1"` | docs | vars#understand-variable-syntax | - |
| EXPRV-398 | macro | `echo $(projectName)` | `{"variables":{"projectName":"contoso"}}` | `"echo contoso"` | docs | vars#macro-syntax-variables | - |
| EXPRV-399 | macro | `$(var)` | `{}` | `"$(var)"` | docs | vars#macro-syntax-variables | - |
| EXPRV-400 | macro | `x$()y` | `{}` | `"x$()y"` | docs | vars#macro-syntax-variables | - |
| EXPRV-401 | macro | `$(A)` | `{"variables":{"a":"1"}}` | `"1"` | reference-impl | AG:Agent.Worker/Variables.cs | - |
| EXPRV-402 | macro | `$(a)$(b)` | `{"variables":{"a":"1","b":"2"}}` | `"12"` | reference-impl | AG:Microsoft.VisualStudio.Services.Agent/Util/VarUtil.cs | - |
| EXPRV-403 | macro | `$(a)-$(missing)-$(b)` | `{"variables":{"a":"1","b":"2"}}` | `"1-$(missing)-2"` | docs | vars#macro-syntax-variables | - |
| EXPRV-404 | macro | `$(Build.SourceBranch)` | `{"variables":{"Build.SourceBranch":"refs/heads/main"}}` | `"refs/heads/main"` | docs | vars#macro-syntax-variables | - |
| EXPRV-405 | macro | `$(a)` | `{"variables":{"a":""}}` | `""` | reference-impl | AG:Microsoft.VisualStudio.Services.Agent/Util/VarUtil.cs | - |
| EXPRV-406 | macro | `echo $((1+2))` | `{"variables":{"a":"1"}}` | `"echo $((1+2))"` | reference-impl | AG:Microsoft.VisualStudio.Services.Agent/Util/VarUtil.cs | - |
| EXPRV-407 | macro | `echo $(git rev-parse HEAD)` | `{}` | `"echo $(git rev-parse HEAD)"` | docs | vars#macro-syntax-variables | - |
| EXPRV-408 | macro | `$$(a)` | `{"variables":{"a":"1"}}` | `"$1"` | reference-impl | AG:Microsoft.VisualStudio.Services.Agent/Util/VarUtil.cs | - |
| EXPRV-409 | macro | `$(a` | `{"variables":{"a":"1"}}` | `"$(a"` | reference-impl | AG:Microsoft.VisualStudio.Services.Agent/Util/VarUtil.cs | - |
| EXPRV-410 | macro | `$( a )` | `{"variables":{"a":"1"}}` | `"$( a )"` | reference-impl | AG:Microsoft.VisualStudio.Services.Agent/Util/VarUtil.cs | - |
| EXPRV-411 | macro | `$(myOuter)` | `{"variables":{"myInner":"someValue","myOuter":"$(myInner)"}}` | `"someValue"` | docs | vars#recursive-expansion | - |
| EXPRV-412 | macro | `$(a)` | `{"variables":{"a":"x$(missing)y"}}` | `"x$(missing)y"` | reference-impl | AG:Agent.Worker/Variables.cs | - |
| EXPRV-413 | macro | `$(a)` | `{"variables":{"a":"$(b)","b":"$(a)"}}` | `"$(b)"` | reference-impl | AG:Agent.Worker/Variables.cs | - |
| EXPRV-414 | macro | `$($(n))` | `{"variables":{"n":"x","x":"SHOULD-NOT-APPEAR"}}` | `"$(x)"` | reference-impl | AG:Microsoft.VisualStudio.Services.Agent/Util/VarUtil.cs | - |
| EXPRV-415 | macro | `{"$(key)":"v"}` (macro in a mapping key) | `{"variables":{"key":"k"}}` | `{"$(key)":"v"}` | docs | vars#macro-syntax-variables | - |
| EXPRV-416 | macro | `$(a) and $(a)` | `{"variables":{"a":"1"}}` | `"1 and 1"` | reference-impl | AG:Microsoft.VisualStudio.Services.Agent/Util/VarUtil.cs | - |
| EXPRV-417 | macro | `$[variables.a]` (in a task input, not a variable definition) | `{"variables":{"a":"1"}}` | `"$[variables.a]"` | inferred | vars#runtime-expression-syntax | - |
| EXPRV-418 | macro | `$(v1)` with `v1..v60` chained (`vN="$(vN+1)"`, `v60="end"`) | `{"variables":{"v1":"$(v2)","...":"...","v60":"end"}}` | `"$(v2)"` | reference-impl | AG:Agent.Worker/Variables.cs | - |
| EXPRV-419 | compile | `eq(1,)` | `{}` | `error:unexpected symbol ')' after separator` | reference-impl | RI:ExpressionParser.cs | TRL-EXPR-0001 |
| EXPRV-420 | compile | `eq(1 1)` | `{}` | `error:unexpected symbol` | reference-impl | RI:ExpressionParser.cs | TRL-EXPR-0001 |
| EXPRV-421 | compile | `eq(1,1))` | `{}` | `error:unexpected symbol ')'` | reference-impl | RI:ExpressionParser.cs | TRL-EXPR-0001 |
| EXPRV-422 | compile | `eq(1,1` | `{}` | `error:unclosed function` | reference-impl | RI:ExpressionParser.cs | TRL-EXPR-0001 |
| EXPRV-423 | compile | `parameters.list[0` | `{"parameters":{"list":["x"]}}` | `error:unclosed indexer` | reference-impl | RI:ExpressionParser.cs | TRL-EXPR-0001 |
| EXPRV-424 | compile | `parameters.list[0, 1]` | `{"parameters":{"list":["x"]}}` | `error:unexpected separator in indexer` | reference-impl | RI:ExpressionParser.cs | TRL-EXPR-0001 |
| EXPRV-425 | compile | `foo(1)` | `{}` | `error:unrecognized function` | reference-impl | RI:ExpressionParser.cs | TRL-EXPR-0001 |
| EXPRV-426 | compile | `noSuchContext.x` | `{}` | `error:unrecognized named value` | reference-impl | RI:ExpressionParser.cs | TRL-EXPR-0001 |
| EXPRV-427 | compile | `eq(1,2) extra` | `{}` | `error:unexpected symbol` | reference-impl | RI:ExpressionParser.cs | TRL-EXPR-0001 |
| EXPRV-428 | compile | `variables.` | `{}` | `error:expected property name` | reference-impl | RI:ExpressionParser.cs | TRL-EXPR-0001 |
| EXPRV-429 | compile | `variables..a` | `{}` | `error:unexpected symbol` | reference-impl | RI:ExpressionParser.cs | TRL-EXPR-0001 |
| EXPRV-430 | compile | `eq` | `{}` | `error:expected '(' after function name` | reference-impl | RI:ExpressionParser.cs | TRL-EXPR-0001 |
| EXPRV-431 | compile | `(true)` | `{}` | `error:grouping parentheses not supported` | reference-impl | RI:ExpressionParser.cs | TRL-EXPR-0001 |
| EXPRV-432 | compile | `true && false` | `{}` | `error:no infix operators` | reference-impl | RI:LexicalAnalyzer.cs | TRL-EXPR-0001 |
| EXPRV-433 | compile | `!true` | `{}` | `error:no prefix operators` | reference-impl | RI:LexicalAnalyzer.cs | TRL-EXPR-0001 |
| EXPRV-434 | compile | `variables.a == 'x'` | `{"variables":{"a":"x"}}` | `error:no infix operators` | reference-impl | RI:LexicalAnalyzer.cs | TRL-EXPR-0001 |
| EXPRV-435 | compile | `EQ(1, 1)` | `{}` | `true` | reference-impl | RI:ExpressionConstants.cs | - |
| EXPRV-436 | compile | `StartsWith('abc', 'A')` | `{}` | `true` | reference-impl | RI:ExpressionConstants.cs | - |
| EXPRV-437 | compile | `NOTIN('a', 'b')` | `{}` | `true` | reference-impl | RI:ExpressionConstants.cs | - |
| EXPRV-438 | compile | `  eq( 1 , 1 )  ` | `{}` | `true` | reference-impl | RI:LexicalAnalyzer.cs | - |
| EXPRV-439 | compile | `` (empty expression, i.e. `${{ }}`) | `{}` | `error:empty expression` | inferred | RI:ExpressionParser.cs | TRL-EXPR-0001 |
| EXPRV-440 | compile | `not(` x49 + `true` + `)` x49 | `{}` | `false` | reference-impl | RI:ExpressionConstants.cs | - |
| EXPRV-441 | compile | `not(` x50 + `true` + `)` x50 | `{}` | `error:exceeded max expression depth (50)` | reference-impl | RI:ExpressionParser.cs | TRL-EXPR-0001 |
| EXPRV-442 | compile | `'` + `a` x20998 + `'` (21000 chars) | `{}` | `"<string of 20998 'a' characters>"` | reference-impl | RI:ExpressionConstants.cs | - |
| EXPRV-443 | compile | `'` + `a` x20999 + `'` (21001 chars) | `{}` | `error:exceeded max expression length (21000)` | reference-impl | RI:ExpressionParser.cs | TRL-EXPR-0001 |
| EXPRV-444 | compile | `join('', parameters.big)` | `{"parameters":{"big":["<300000 x 'a'>","<300000 x 'b'>"]}}` | `error:exceeded max evaluation memory (1 MiB)` | reference-impl | RI:EvaluationContext.cs | TRL-EXPR-0001 |
| EXPRV-445 | compile | template include chain 101 levels deep | `{}` | `error:max 100 levels of template nesting` | docs | tmpl#imposed-limits-on-template-updates | TRL-EXPR-0001 |
| EXPRV-446 | compile | pipeline including 101 distinct template files | `{}` | `error:max 100 included YAML files` | docs | tmpl#imposed-limits-on-template-updates | TRL-EXPR-0001 |

### Conversion-matrix coverage index

Every `from -> to` pair of the docs table (expr#type-casting) plus the undocumented Array/Object/DateTime kinds:

| from \ to | Boolean | Null | Number | String | Version |
|---|---|---|---|---|---|
| Boolean | (identity) | EXPRV-108 | EXPRV-084 | EXPRV-081 | EXPRV-126 |
| Null | EXPRV-073 | (identity) | EXPRV-101 | EXPRV-106 | EXPRV-127 |
| Number | EXPRV-070 | EXPRV-102 | (identity) | EXPRV-174 | EXPRV-119 |
| String | EXPRV-068 | EXPRV-104 | EXPRV-089 | (identity) | EXPRV-112 |
| Version | EXPRV-065 | EXPRV-128 | EXPRV-125 | EXPRV-113 | (identity) |
| Array | EXPRV-133 | n/a | EXPRV-153 | EXPRV-225 | n/a |
| Object | EXPRV-156 | n/a | EXPRV-155 | EXPRV-226 | n/a |
| DateTime | EXPRV-241 | n/a | EXPRV-240 | EXPRV-238 | n/a |

## 3. Vectors whose behavior remains uncertain, and how to settle them

**Capture method A (compile-time, cheap, no agent):** Pipelines Preview API --
`POST https://dev.azure.com/{org}/{project}/_apis/pipelines/{pipelineId}/preview?api-version=7.1`
with body `{"previewRun": true, "yamlOverride": "<yaml>", "templateParameters": {...}}`; read `finalYaml`.
Encode each vector as `variables: - name: vNNN  value: ${{ convertToJson(<expr>) }}` (or `${{ <expr> }}` when the
result is a scalar) so the type survives; for error vectors the API returns HTTP 400 with the parser/evaluator message.
For directive vectors put the JSON document under a `steps:`/`variables:` shape and diff `finalYaml`.

**Capture method B (runtime / macro):** a real run on a hosted agent: one job with
`variables: vNNN: $[ <expr> ]` and a step `echo "vNNN=[$(vNNN)]"` per vector; conditions via
`- script: echo hit  condition: <expr>` (step skipped = False); status functions need a deliberately failing /
cancelled upstream job; counters need two consecutive runs. Macro vectors: `- script: echo '<input>'` with the
variables defined in YAML (log shows the expanded text; disable script interpretation by using `printf '%s\n'`).

| ID(s) | Question | Ruling used here | Settle with |
|---|---|---|---|
| EXPRV-096 | String->Number: Int32 (docs) or Decimal (ref-impl)? `'1.5'`, `'3000000000'` | Decimal | A: `${{ eq(1.5, '1.5') }}`, `${{ eq(3000000000, '3000000000') }}` |
| EXPRV-218 | Number->String max fractional digits / rounding | `0.#######` | A: `${{ format('{0}', 1.23456789) }}` |
| EXPRV-116 | System.Version equality `1.2.0` vs `1.2` | not equal | A |
| EXPRV-022 | `null` literal | parse error | A |
| EXPRV-163, EXPRV-170 | 1-arg `in`/`notIn` | allowed (docs) | A |
| EXPRV-176 | `contains(array, x)` | error | A |
| EXPRV-038 | object-parameter key lookup case-insensitive? | case-insensitive | A: `${{ parameters.obj.A }}` |
| EXPRV-132 | array equality by reference | false for distinct arrays | A |
| EXPRV-370 | empty array truthy in `${{ if }}` | truthy | A |
| EXPRV-235 | DateTime specifier with `-`/`:` | error | B: `$[format('{0:yyyy-MM-dd}', pipeline.startTime)]` |
| EXPRV-238 | DateTime default string form | `yyyy-MM-dd HH:mm:sszzz` | B |
| EXPRV-215 | `format` with a single argument | allowed | A |
| EXPRV-256 | `length` counts UTF-16 units | 2 for U+1F600 | A |
| EXPRV-260, EXPRV-264 | `length(object)`, `length(null)` | key count; error | A |
| EXPRV-275, EXPRV-279 | `replace` case sensitivity; empty search | ordinal case-sensitive; error | A |
| EXPRV-284 | `split` delimiter = char set or whole string? | char set | A |
| EXPRV-291 | `iif` with 2 args (docs min 1) | returns arg 2 when true | A (also `iif(false,'a')`) |
| EXPRV-295, EXPRV-297, EXPRV-298 | `convertToJson` escaping, newline (`\n` vs `\r\n`), scalar typing of YAML object params | Newtonsoft defaults, `\n`, typed | A (inspect `finalYaml` bytes) |
| EXPRV-308 | `counter` with 1 arg | seed 0 | B (two runs) |
| EXPRV-330, EXPRV-341, EXPRV-335 | job-name case, unknown job name, Skipped dependency | case-insensitive; error; Skipped is not success | B (multi-job pipeline) |
| EXPRV-350 | `foo $[expr]` (runtime expression not whole value) | left literal | B |
| EXPRV-367 | `else` separated from `if` | error | A |
| EXPRV-374 | `each` over mapping preserves YAML order | preserved | A |
| EXPRV-408 | no `$$` escape | `$$(a)` -> `$1` | B |
| EXPRV-413, EXPRV-418 | cycle / depth > 50 handling | leave unexpanded, warn | B (check warnings in log) |
| EXPRV-440, EXPRV-441, EXPRV-442, EXPRV-443, EXPRV-444 | server limits (ref-impl constants from 2019) | 50 / 21000 / 1 MiB | A (generate the expressions programmatically) |
| EXPRV-083, EXPRV-144, EXPRV-148 | coercion gotchas (high confidence, but worth pinning) | as listed | A |
