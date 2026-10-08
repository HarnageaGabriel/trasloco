# Task catalog

The catalog is versioned factual input to pipeline conversion. Version 1 has
four document kinds: generated Azure Pipelines task facts, the official task
retirement list, hand-written mappings, and a GitHub Actions lock. Every file
is validated against `schemas/catalog/v1.schema.json` before it is indexed.

Generated task facts come from `microsoft/azure-pipelines-tasks` commit
`f1962f12fac2899b5d689967dda0d25523cc3ec4` under its MIT license. The generator
copies identifiers, versions, input names/aliases/types/defaults/option keys,
visibility and group facts, lifecycle flags, and execution handler names. It
never copies labels, descriptions, or help prose. Overlong defaults are marked
as omitted rather than copied into the repository.

## Reproducible generation

Run these commands from the repository root in a networked environment. Set
`GITHUB_TOKEN` to avoid anonymous GitHub API limits. Outputs are sorted and do
not contain timestamps.

```text
go run ./tools/catalog-gen tasks
go run ./tools/catalog-gen retirement
go run ./tools/catalog-gen actions
```

`tasks` reads the pinned Git tree and task blobs. For an offline checkout of
that exact commit, `tasks --src PATH` reads `PATH/Tasks/*/task.json`.
`retirement` reads the Microsoft task-retirement page and records its source
URL plus the announced 2026-10-15 retirement and 2027-03-15 removal dates; use
`--src PAGE.html` to reproduce from a saved page. `actions` enumerates stable
semantic-version tags through the Git API, dereferences annotated tags, pins
the commit, and reads `action.yml` (or `action.yaml`). Actions whose
`runs.using` is not `node24`, `composite`, or `docker` are recorded in the
rejected section, never in the usable lock.

The generated files checked in under `catalog/` were refreshed on 2026-10-09;
the task source remains pinned to the commit above. Regeneration requires a
networked environment and the commands above. The maintainer must review the
complete generated diff before accepting it, including confirming that task
facts remain prose-free and that no generated YAML was edited by hand.

## Lookup behavior

Task names, GUIDs, input names, and aliases are case-insensitive. Official task
aliases may share a GUID; a bare GUID deterministically resolves to the
lexically first task name. A reference may include a major (`TaskName@2`).
`LatestMajor` selects the greatest major
that is neither preview, deprecated, nor present in the retirement list.
Mapping files are loaded in lexical path order and must reference existing task
majors and inputs whenever generated task facts are present.

Hand-written mappings cite a `source`, declare fidelity and conditions, map
inputs explicitly, and contain at least one inline test case. Unknown fields,
duplicate names/majors, invalid runtimes, and dangling mapping references
produce `TRL-CAT-0004` or `TRL-CAT-0005` diagnostics.
