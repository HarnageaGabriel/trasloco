# Azure Pipelines expression language

Status: implemented from the public Microsoft documentation available to the
project. Microsoft's evaluator is closed. Statements tagged **VERIFY** require
comparison with the hosted service before claiming byte-for-byte compatibility.

## Forms and phases

| Form | Evaluation | Placement | Available roots | Undefined value |
|---|---|---|---|---|
| `${{ expression }}` | template compilation | values and insertion-directive keys | `parameters`, static `variables`, compile-time `pipeline` data | error |
| `$[ expression ]` | runtime | entire scalar value only | `variables`, `dependencies`, `stageDependencies`, `pipeline`; never `parameters` | error |
| `$(name)` | immediately before a task | embedded in scalar values, never keys | variables | original `$(name)` remains literal |

`counter()` is runtime-only and only valid as the complete expression defining
a variable. Status functions use an injected status provider. `failed` and
`succeeded` accept zero or more job/stage names; other status functions accept
none.

**VERIFY:** `$$(name)` is treated by this package as an escaped macro and emits
literal `$(name)`. The public documentation does not specify a general macro
escape. Confirm against the service.

## Lexical grammar

Offsets are UTF-8 byte offsets. Whitespace is insignificant outside strings.
Keywords and function names are case-insensitive. Property spelling is retained
by the AST and canonical printer; evaluation uses ordinal ignore-case lookup.

```ebnf
expression     = primary, { selector } ;
primary        = literal | identifier | call ;
call           = identifier, "(", [ expression, { ",", expression } ], ")" ;
selector       = ".", (identifier | "*") | "[", expression, "]" ;
literal        = null | boolean | number | version | string ;
null           = "null" ;
boolean        = "true" | "false" ;
number         = ["-"], digit, {digit}, [".", digit, {digit}],
                 [("e" | "E"), ["+" | "-"], digit, {digit}] ;
version        = digits, ".", digits, ".", digits, [".", digits] ;
string         = "'", { character | "''" }, "'" ;
identifier     = (letter | "_"), {letter | digit | "_"} ;
selector-key   = string | number | expression ;
template       = "${{", expression, "}}" ;
runtime        = "$[", expression, "]" ;
macro          = "$(", macro-name, ")" ;
directive      = "${{", ("if", expression
               | "elseif", expression | "else"
               | "each", identifier, "in", expression), "}}" ;
```

The lexer emits identifiers, numbers, versions, strings, parentheses, brackets,
comma, dot, star, and EOF. There are no infix operators. A version literal has
three or four non-negative integer components; a numeric literal has at most one dot.
Single quotes escape as `''`.

## AST and access

Nodes are `Literal`, `Identifier`, `Call`, `Member`, and `Index`. Every node has
a half-open source byte span. `foo.*.id` projects `id` from each array element or
object value. Bracket lookup accepts a string object key or an integral array
index. Relevant paths include:

```text
variables.x
variables['x']
parameters.x
dependencies.job.outputs['step.var']
dependencies.job.result
stageDependencies.stage.job.outputs['step.var']
pipeline.startTime
foo.*.id
```

## Functions

`C` means compile/template; `R` means runtime. String operations and string
comparisons use ordinal ignore-case semantics except transformations and
`replace`, which preserve/use the supplied casing. `and`, `or`, and `iif` are
short-circuiting.

| Function | Arity | Input/result semantics | Phase |
|---|---:|---|---|
| `and` | 2+ | truthiness; stop on false | C,R |
| `or` | 2+ | truthiness; stop on true | C,R |
| `not` | 1 | negated truthiness | C,R |
| `xor` | 2 | exactly one truthy | C,R |
| `eq`, `ne` | 2 | convert right to left type; conversion failure is false/true | C,R |
| `gt`, `ge`, `lt`, `le` | 2 | convert right to left type; failure is an error | C,R |
| `in`, `notIn` | 2+ | compare first with remaining values using `eq` conversion | C,R |
| `coalesce` | 2+ | first non-null, non-empty-string value; otherwise null | C,R |
| `iif` | 3 | evaluate only selected branch | C,R |
| `contains` | 2 | case-insensitive substring | C,R |
| `containsValue` | 2 | array/object-value membership | C,R |
| `startsWith`, `endsWith` | 2 | case-insensitive prefix/suffix | C,R |
| `format` | 1+ | `{n}` substitution; `{{` and `}}` escapes | C,R |
| `join` | 2 | stringify array items and join; scalar stringifies | C,R |
| `split` | 2 | split string by exact separator | C,R |
| `replace` | 3 | replace all exact substrings | C,R |
| `length` | 1 | string bytes or array/object entries | C,R |
| `lower`, `upper`, `trim` | 1 | Unicode case mapping / surrounding whitespace | C,R |
| `convertToJson` | 1 | indented JSON | C |
| `counter` | 1..2 | injected persistent counter `(prefix, seed=0)`; variable definitions only | R |
| `always`, `canceled`, `succeededOrFailed` | 0 | injected status predicate | R |
| `failed`, `succeeded` | 0+ | injected status predicate, optional job/stage names | R |

**VERIFY:** Azure documents date/time formatting for `format`; this
implementation currently substitutes the value and ignores a `:format`
suffix. `length` counts UTF-8 bytes because Go strings are byte sequences;
confirm whether hosted Azure counts UTF-16 code units. Object iteration order
is intentionally unspecified. Confirm compile-time availability of status
functions in every YAML location.

## Conversion and comparison

| Left type | Accepted right values | Conversion | Truthiness |
|---|---|---|---|
| null | null | only null converts | false |
| boolean | boolean; `true`/`false` string; number | parse or nonzero | value |
| number | numeric types or numeric string | floating-point parse | nonzero |
| string | any scalar | Azure-style scalar stringification | non-empty |
| version | version or version-shaped string | 2..4 integer parts | true |
| array | same concrete type for equality | no scalar conversion | non-empty |
| object | same concrete type for equality | no scalar conversion | non-empty |

Versions compare component-by-component with missing trailing components equal
to zero. Strings compare ordinal ignore-case. `eq`/`ne` conversion direction is
right-to-left. Relational conversion failure emits `TRL-EXPR-0005`.

**VERIFY:** Exact Azure numeric domain, overflow behavior, `NaN`/infinity
handling, complex-value truthiness, and equality of arrays/objects are not fully
specified publicly. This implementation uses Go `float64` parsing and deep
equality for same-typed complex values; non-finite results are not accepted as
source literals because their spellings are not in the grammar.

## Errors

- `TRL-EXPR-0001`: bad token, malformed wrapper/string/version, unexpected
  token, unknown name/property/function, invalid arity/phase/directive, or a
  missing provider.
- `TRL-EXPR-0005`: operand/conversion/type failure.
- `$[ ]` containing `parameters` is rejected.
- Runtime syntax must occupy an entire scalar; placement enforcement belongs to
  the YAML caller.
- Macro scanning rejects an unterminated or empty macro. Unknown macros remain
  literal.
- Directive parsing requires the directive to occupy the complete key.

## Conformance vectors

The Go test suite reads this table directly. `Context` and `Expected` are JSON,
except `error:<substring>`. `parse` expects the canonical printer result;
`directive` expects the directive kind; `macro` expands the scalar.

| ID | Mode | Phase | Input | Context | Expected |
|---|---|---|---|---|---|
| EXPR-001 | eval | compile | `true` | `{}` | `true` |
| EXPR-002 | eval | compile | `FALSE` | `{}` | `false` |
| EXPR-003 | eval | compile | `null` | `{}` | `null` |
| EXPR-004 | eval | compile | `42` | `{}` | `42` |
| EXPR-005 | eval | compile | `-2.5` | `{}` | `-2.5` |
| EXPR-006 | eval | compile | `1e2` | `{}` | `100` |
| EXPR-007 | eval | compile | `'hello'` | `{}` | `"hello"` |
| EXPR-008 | eval | compile | `'it''s'` | `{}` | `"it's"` |
| EXPR-009 | eval | compile | `1.2.3` | `{}` | `"1.2.3"` |
| EXPR-010 | eval | compile | `variables.name` | `{"variables":{"name":"value"}}` | `"value"` |
| EXPR-011 | eval | compile | `variables['name']` | `{"variables":{"name":"value"}}` | `"value"` |
| EXPR-012 | eval | compile | `parameters.flag` | `{"parameters":{"flag":true}}` | `true` |
| EXPR-013 | eval | runtime | `parameters.flag` | `{"parameters":{"flag":true}}` | `error:parameters are unavailable` |
| EXPR-014 | eval | runtime | `dependencies.build.result` | `{"dependencies":{"build":{"result":"Succeeded"}}}` | `"Succeeded"` |
| EXPR-015 | eval | runtime | `dependencies.build.outputs['step.var']` | `{"dependencies":{"build":{"outputs":{"step.var":"x"}}}}` | `"x"` |
| EXPR-016 | eval | runtime | `stageDependencies.s.j.outputs['a.b']` | `{"stageDependencies":{"s":{"j":{"outputs":{"a.b":"yes"}}}}}` | `"yes"` |
| EXPR-017 | eval | compile | `pipeline.startTime` | `{"pipeline":{"startTime":"now"}}` | `"now"` |
| EXPR-018 | eval | compile | `foo.*.id` | `{"foo":[{"id":1},{"id":2}]}` | `[1,2]` |
| EXPR-019 | eval | compile | `eq('A', 'a')` | `{}` | `true` |
| EXPR-020 | eval | compile | `ne('A', 'b')` | `{}` | `true` |
| EXPR-021 | eval | compile | `eq(2, '2')` | `{}` | `true` |
| EXPR-022 | eval | compile | `eq(2, 'x')` | `{}` | `false` |
| EXPR-023 | eval | compile | `ne(2, 'x')` | `{}` | `true` |
| EXPR-024 | eval | compile | `gt(3, '2')` | `{}` | `true` |
| EXPR-025 | eval | compile | `ge(3, 3)` | `{}` | `true` |
| EXPR-026 | eval | compile | `lt('a', 'B')` | `{}` | `true` |
| EXPR-027 | eval | compile | `le(2, 2)` | `{}` | `true` |
| EXPR-028 | eval | compile | `gt(2, 'x')` | `{}` | `error:TRL-EXPR-0005` |
| EXPR-029 | eval | compile | `eq(1.2.3, '1.2.3')` | `{}` | `true` |
| EXPR-030 | eval | compile | `lt(1.2.3, 1.3.0)` | `{}` | `true` |
| EXPR-031 | eval | compile | `eq(1.2.0, 1.2.0.0)` | `{}` | `true` |
| EXPR-032 | eval | compile | `and(true, true, 1)` | `{}` | `true` |
| EXPR-033 | eval | compile | `and(true, false, variables.missing)` | `{}` | `false` |
| EXPR-034 | eval | compile | `or(false, '', 1)` | `{}` | `true` |
| EXPR-035 | eval | compile | `or(true, variables.missing)` | `{}` | `true` |
| EXPR-036 | eval | compile | `not(false)` | `{}` | `true` |
| EXPR-037 | eval | compile | `xor(true, false)` | `{}` | `true` |
| EXPR-038 | eval | compile | `xor(true, true)` | `{}` | `false` |
| EXPR-039 | eval | compile | `in('b', 'a', 'B', 'c')` | `{}` | `true` |
| EXPR-040 | eval | compile | `notIn(4, 1, 2, 3)` | `{}` | `true` |
| EXPR-041 | eval | compile | `coalesce(null, '', 'x')` | `{}` | `"x"` |
| EXPR-042 | eval | compile | `coalesce(null, '')` | `{}` | `null` |
| EXPR-043 | eval | compile | `iif(true, 'yes', variables.missing)` | `{}` | `"yes"` |
| EXPR-044 | eval | compile | `iif(false, variables.missing, 'no')` | `{}` | `"no"` |
| EXPR-045 | eval | compile | `contains('Hello', 'ELL')` | `{}` | `true` |
| EXPR-046 | eval | compile | `contains('Hello', 'xyz')` | `{}` | `false` |
| EXPR-047 | eval | compile | `containsValue(parameters.items, 'B')` | `{"parameters":{"items":["a","b"]}}` | `true` |
| EXPR-048 | eval | compile | `containsValue(parameters.obj, 2)` | `{"parameters":{"obj":{"a":1,"b":2}}}` | `true` |
| EXPR-049 | eval | compile | `startsWith('Hello', 'he')` | `{}` | `true` |
| EXPR-050 | eval | compile | `endsWith('Hello', 'LO')` | `{}` | `true` |
| EXPR-051 | eval | compile | `format('Hi {0}', 'Ada')` | `{}` | `"Hi Ada"` |
| EXPR-052 | eval | compile | `format('{{{0}}}', 'x')` | `{}` | `"{x}"` |
| EXPR-053 | eval | compile | `join('-', parameters.items)` | `{"parameters":{"items":["a",2,true]}}` | `"a-2-True"` |
| EXPR-054 | eval | compile | `join('-', 'a')` | `{}` | `"a"` |
| EXPR-055 | eval | compile | `split('a,b,c', ',')` | `{}` | `["a","b","c"]` |
| EXPR-056 | eval | compile | `replace('a-b-a', 'a', 'x')` | `{}` | `"x-b-x"` |
| EXPR-057 | eval | compile | `length('abc')` | `{}` | `3` |
| EXPR-058 | eval | compile | `length(parameters.items)` | `{"parameters":{"items":[1,2]}}` | `2` |
| EXPR-059 | eval | compile | `length(parameters.obj)` | `{"parameters":{"obj":{"a":1,"b":2}}}` | `2` |
| EXPR-060 | eval | compile | `lower('AbC')` | `{}` | `"abc"` |
| EXPR-061 | eval | compile | `upper('AbC')` | `{}` | `"ABC"` |
| EXPR-062 | eval | compile | `trim('  x  ')` | `{}` | `"x"` |
| EXPR-063 | eval | compile | `convertToJson(parameters.obj)` | `{"parameters":{"obj":{"x":1}}}` | `"{\n  \"x\": 1\n}"` |
| EXPR-064 | eval | runtime | `convertToJson(variables)` | `{"variables":{}}` | `error:unavailable in this phase` |
| EXPR-065 | eval | runtime | `counter('build', 10)` | `{}` | `error:only valid in a variable definition` |
| EXPR-066 | eval | compile | `counter('build')` | `{}` | `error:unavailable in this phase` |
| EXPR-067 | eval | compile | `eq(true, 'true')` | `{}` | `true` |
| EXPR-068 | eval | compile | `eq(false, 0)` | `{}` | `true` |
| EXPR-069 | eval | compile | `not('')` | `{}` | `true` |
| EXPR-070 | eval | compile | `not(parameters.empty)` | `{"parameters":{"empty":[]}}` | `true` |
| EXPR-071 | parse | compile | `${{ EQ( variables.x , 'Y' ) }}` | `{}` | `"eq(variables.x, 'Y')"` |
| EXPR-072 | parse | runtime | `$[eq(variables.x, 'y')]` | `{}` | `"eq(variables.x, 'y')"` |
| EXPR-073 | parse | compile | `foo.*.id` | `{}` | `"foo.*.id"` |
| EXPR-074 | parse | compile | `'it''s'` | `{}` | `"'it''s'"` |
| EXPR-075 | parse | compile | `1.2.3.4` | `{}` | `"1.2.3.4"` |
| EXPR-076 | parse | compile | `eq(` | `{}` | `error:TRL-EXPR-0001` |
| EXPR-077 | parse | compile | `'open` | `{}` | `error:unterminated string` |
| EXPR-078 | parse | compile | `1.2.3.4.5` | `{}` | `error:more than 4 components` |
| EXPR-079 | parse | compile | `foo.` | `{}` | `error:expected property` |
| EXPR-080 | parse | compile | `foo[1` | `{}` | `error:expected ]` |
| EXPR-081 | parse | compile | `@bad` | `{}` | `error:unexpected character` |
| EXPR-082 | parse | compile | `eq(1)` | `{}` | `"eq(1)"` |
| EXPR-083 | eval | compile | `eq(1)` | `{}` | `error:expects 2 arguments` |
| EXPR-084 | eval | compile | `unknown(1)` | `{}` | `error:unknown function` |
| EXPR-085 | eval | compile | `missing` | `{}` | `error:unknown name` |
| EXPR-086 | eval | compile | `variables.missing` | `{"variables":{}}` | `error:property not found` |
| EXPR-087 | macro | macro | `hello $(name)` | `{"variables":{"name":"world"}}` | `"hello world"` |
| EXPR-088 | macro | macro | `$(missing)` | `{"variables":{}}` | `"$(missing)"` |
| EXPR-089 | macro | macro | `a $(x) b $(y)` | `{"variables":{"x":1,"y":true}}` | `"a 1 b True"` |
| EXPR-090 | macro | macro | `$$(name)` | `{"variables":{"name":"world"}}` | `"$(name)"` |
| EXPR-091 | macro | macro | `$(open` | `{}` | `error:unterminated macro` |
| EXPR-092 | directive | compile | `${{ if eq(parameters.x, 1) }}` | `{}` | `"if"` |
| EXPR-093 | directive | compile | `${{ elseif false }}` | `{}` | `"elseif"` |
| EXPR-094 | directive | compile | `${{ else }}` | `{}` | `"else"` |
| EXPR-095 | directive | compile | `${{ each item in parameters.items }}` | `{}` | `"each"` |
| EXPR-096 | directive | compile | `${{ each in parameters.items }}` | `{}` | `error:each syntax` |
| EXPR-097 | directive | compile | `${{ while true }}` | `{}` | `error:unknown directive` |
| EXPR-098 | directive | compile | `prefix ${{ if true }}` | `{}` | `error:complete` |
| EXPR-099 | eval | compile | `variables.CASE` | `{"variables":{"case":"ok"}}` | `"ok"` |
| EXPR-100 | eval | compile | `parameters.items[1]` | `{"parameters":{"items":["a","b"]}}` | `"b"` |
| EXPR-101 | eval | compile | `containsValue(parameters.obj, 'two')` | `{"parameters":{"obj":{"a":"ONE","b":"TWO"}}}` | `true` |
| EXPR-102 | eval | compile | `notIn('x', 'X')` | `{}` | `false` |
| EXPR-103 | eval | compile | `ge(1.2.0, '1.2')` | `{}` | `true` |
| EXPR-104 | eval | compile | `format('{1}-{0}', 'a', 'b')` | `{}` | `"b-a"` |
| EXPR-105 | eval | compile | `split('', ',')` | `{}` | `[""]` |
| EXPR-106 | eval | compile | `replace('abc', '', '-')` | `{}` | `"-a-b-c-"` |
| EXPR-107 | eval | compile | `coalesce(false, 'x')` | `{}` | `false` |
| EXPR-108 | eval | compile | `not(0)` | `{}` | `true` |
| EXPR-109 | eval | compile | `not(parameters.obj)` | `{"parameters":{"obj":{}}}` | `true` |
| EXPR-110 | eval | compile | `eq('2', 2)` | `{}` | `true` |
| EXPR-111 | eval | compile | `dependencies.build.result` | `{"dependencies":{"build":{"result":"Succeeded"}}}` | `error:unavailable at compile time` |
| EXPR-112 | eval | compile | `succeeded()` | `{}` | `error:unavailable in this phase` |
| EXPR-113 | eval | compile | `format('{2}', 'x')` | `{}` | `error:TRL-EXPR-0005` |
| EXPR-114 | eval | compile | `parameters.matrix.*.os` | `{"parameters":{"matrix":[{"os":"linux"},{"os":"windows"}]}}` | `["linux","windows"]` |
