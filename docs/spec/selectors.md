# Target selectors

Status: draft for approval (M0). Normative.

Selectors describe source repositories and, optionally, pipelines. They do not describe target names. The selector engine MUST parse and normalize all entries before performing network reads.

## Grammar

The grammar uses ISO-style EBNF. Terminals are case-sensitive in the grammar; matching rules follow section 4.

```ebnf
selection      = selector , { line-end , selector } ;
selector       = [ "!" ] , repository , [ "#" , pipeline ] ;
repository     = ado-repo | github-repo | ghes-repo ;
ado-repo       = "ado://" , segment , "/" , segment , "/" , repo-ref ;
github-repo    = "github://" , segment , "/" , repo-ref ;
ghes-repo      = "ghes://" , host , "/" , segment , "/" , repo-ref ;
repo-ref       = segment | stable-id ;
pipeline       = segment | stable-id ;
stable-id      = "id:" , id-char , { id-char } ;
segment        = segment-part , { segment-part } ;
segment-part   = literal | "*" | "**" ;
literal        = unreserved | pct-encoded ;
host           = host-char , { host-char } ;
id-char        = unreserved | pct-encoded ;
unreserved     = ALPHA | DIGIT | "-" | "." | "_" | "~" ;
pct-encoded    = "%" , HEXDIG , HEXDIG ;
host-char      = ALPHA | DIGIT | "-" | "." | ":" ;
line-end       = "\n" | "\r\n" ;
```

`*` matches zero or more characters within one URI segment. `**` MUST occupy an entire segment and matches zero or more complete segments. Current repository forms have fixed segment counts, so `**` is primarily useful for an organization/project segment and future platform schemes. A bare `**` inside text is invalid. `/`, `#`, `!`, `%`, spaces, and non-ASCII bytes inside names MUST be UTF-8 percent-encoded. Percent hex digits are canonicalized to uppercase. Encoded `/` remains data and MUST NOT become a separator during grammar recognition.

Stable IDs use platform-returned opaque identifiers. A repository ID occupies the repository position (`github://owner/id:123`); a pipeline ID follows `#` (`.../repo#id:123`). IDs MUST NOT contain globs. Selectors MUST NOT include query strings, fragments other than the pipeline separator, credentials, or trailing `/`.

## Selection files

A selection file is UTF-8 without a required BOM, with one selector per physical line. Blank lines and lines whose first non-whitespace character is `#` are ignored. Leading and trailing ASCII whitespace is removed. Inline comments are not supported because `#` separates a pipeline. A line beginning `\#` is invalid, not a comment escape. Maximums are 4 MiB and 100,000 physical lines; exceeding either emits `TRL-SEL-0007`.

CLI selectors and files are concatenated in command-line occurrence order for reporting only. Order MUST NOT affect the selected set.

## Evaluation

1. Parse every entry. Any syntax error rejects the complete selection; partial selection is forbidden.
2. Resolve each positive selector against the authorized source inventory. At least one positive selector is REQUIRED (`TRL-SEL-0004`).
3. Union all positive matches.
4. Resolve every exclusion and subtract its matches. **Exclusion always wins**, regardless of order or how many positive selectors matched.
5. De-duplicate by platform stable repository ID plus stable pipeline ID. Preserve all matching selector IDs as provenance.
6. Sort the result by canonical platform URI, then stable ID, using unsigned UTF-8 byte order. API enumeration order MUST NOT affect output.

A repository selector without `#` selects the repository and all discoverable pipelines in it. A selector with `#` selects the repository context plus only matching pipelines. An exclusion without `#` removes the entire repository. A pipeline exclusion removes only matching pipelines. A repository that remains selected but has zero selected pipelines remains a unit for resource/repository audit; commands requiring pipelines MUST report the empty set.

Name matches MUST NOT fall back to IDs. `id:` is the only ID marker. A pipeline-name selector matching multiple pipelines selects all for bulk commands; commands requiring one pipeline emit `TRL-SEL-0006`.

## Case rules

| Platform component | Matching | Canonicalization |
|---|---|---|
| `ado` organization, project, repository, pipeline display name | case-insensitive Unicode simple case fold | preserve platform-returned display spelling |
| `github` owner and repository | ASCII case-insensitive | preserve API spelling; comparison key lower-case ASCII |
| `ghes` host | ASCII case-insensitive; default port removed | lower-case DNS host |
| `ghes` owner and repository | ASCII case-insensitive | preserve API spelling |
| stable ID | exact opaque byte comparison after percent-decoding | preserve decoded value |

VERIFY: Confirm Azure DevOps pipeline display-name case behavior. Until confirmed, the ADO adapter MUST query exact platform objects and detect case-fold collisions rather than choose one.

Glob matching uses the component's case rule. Percent-decoding occurs before comparison. Unicode MUST be normalized to NFC for comparison without changing the emitted display name.

## Endpoint containment

All selectors in one invocation MUST belong to the configured source endpoint. `github://` denotes GitHub.com only. `ghes://host/...` denotes exactly that host and effective port. An ADO organization in a selector MUST equal the organization in the configured endpoint. Cross-endpoint entries emit `TRL-SEL-0008`.

## Errors

Unknown schemes, empty segments, malformed percent escapes, credentials, query strings, non-terminal `#`, invalid `**`, globbed IDs/hosts, and trailing separators are syntax errors. A positive name/glob matching nothing emits warning `TRL-SEL-0003`, or an error under `--strict-selectors`. An explicit stable ID matching nothing emits `TRL-SEL-0005`. Exclusions matching nothing are silent.

## Test vectors

Assume inventory: ADO `Acme/Payments/API` with pipelines `(12, Build)`, `(13, Deploy)`, repo `Acme/Payments/Web`; GitHub `Acme/API`, `Acme/api-tools`, `Acme/Web`; GHES host `git.acme.test`, owner `Core`, repo `API`. Unless stated, expected values are canonical display URIs.

| ID | Input | Expected |
|---|---|---|
| SEL-001 | `ado://Acme/Payments/API` | repo API; pipelines 12 and 13 |
| SEL-002 | `ado://Acme/Payments/API#Build` | pipeline 12 only |
| SEL-003 | `ado://Acme/Payments/API#id:13` | pipeline 13 only |
| SEL-004 | `ado://Acme/Payments/id:repo-7` | repository with exact stable ID `repo-7` |
| SEL-005 | `github://Acme/API` | `github://Acme/API` |
| SEL-006 | `github://acme/api` | same repository as SEL-005 |
| SEL-007 | `ghes://git.acme.test/Core/API` | GHES Core/API |
| SEL-008 | `ghes://GIT.ACME.TEST/Core/API` | same repository as SEL-007 |
| SEL-009 | `github://Acme/*` | API, api-tools, Web |
| SEL-010 | `github://Acme/api*` | API and api-tools |
| SEL-011 | `ado://Acme/*/API` | ADO API |
| SEL-012 | `ado://*/Payments/*` | ADO API and Web |
| SEL-013 | `ado://**/Payments/API` | valid; ADO API |
| SEL-014 | `github://Acme/a**` | syntax error `TRL-SEL-0001` |
| SEL-015 | `github://Acme/API`<br>`!github://Acme/API` | empty selected set; exclusion wins |
| SEL-016 | `!github://Acme/API`<br>`github://Acme/API` | empty selected set; order irrelevant |
| SEL-017 | `ado://Acme/Payments/API`<br>`!ado://Acme/Payments/API#Deploy` | repo API; pipeline 12 only |
| SEL-018 | `ado://Acme/Payments/API#*`<br>`!ado://Acme/Payments/API#id:12` | pipeline 13 only |
| SEL-019 | `github://Acme/My%20Repo` | decoded repository name `My Repo` |
| SEL-020 | `github://Acme/A%2FB` | one repository name `A/B`, not two segments |
| SEL-021 | `github://Acme/API/` | syntax error: trailing `/` |
| SEL-022 | `github://Acme//API` | syntax error: empty segment |
| SEL-023 | `gitlab://Acme/API` | `TRL-SEL-0002` |
| SEL-024 | `https://token@github.com/HarnageaGabriel/trasloco` | syntax error; credentials/URL forbidden |
| SEL-025 | `github://Acme/API?x=1` | syntax error; query forbidden |
| SEL-026 | `github://Acme/API#` | syntax error; empty pipeline |
| SEL-027 | `github://Acme/id:*` | syntax error; ID cannot glob |
| SEL-028 | `# full line comment`<br>`github://Acme/API` | API only |
| SEL-029 | `github://Acme/API # comment` | syntax error; no inline comments |
| SEL-030 | blank file | `TRL-SEL-0004` |
| SEL-031 | `github://Acme/Missing` | warning `TRL-SEL-0003`; no match |
| SEL-032 | `github://Acme/id:missing` | error `TRL-SEL-0005` |
| SEL-033 | `!github://Acme/Missing` plus a valid include | valid; unmatched exclusion silent |
| SEL-034 | `github://Acme/API` repeated twice | one unit; two selector provenance entries |
| SEL-035 | configured GHES `git.acme.test`, selector `ghes://other.test/Core/API` | `TRL-SEL-0008` |
| SEL-036 | `github://Acme/caf%C3%A9` against decomposed Unicode spelling | match after NFC normalization |
| SEL-037 | `github://Acme/%ZZ` | malformed percent escape; `TRL-SEL-0001` |
| SEL-038 | `ado://Acme/Payments/API#Build#x` | syntax error; extra `#` |

## Open questions

1. Should a repository left with zero pipelines after exclusions remain selected for `convert`, or only for `audit` and `plan`?
2. Should future selectors support resource kinds other than repositories and pipelines without introducing a second grammar?
3. Should ADO organization aliases be permitted when the endpoint URL contains a collection name?
