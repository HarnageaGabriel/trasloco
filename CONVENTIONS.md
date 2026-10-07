# Conventions

Mandatory for every contributor, human or AI agent. Reviews check them. Decisions behind them are in `docs/adr/`.

## Layering
- `cmd/` wires; `internal/cli` adapts; `internal/engine` orchestrates.
- Pure packages (`internal/ir`, `internal/dialect/...`, `internal/dialect/.../expr`, `internal/diag`, `internal/yamlx`) must not import `net/http`, `os`, `os/exec`, `database/sql` or perform I/O. Enforced by depguard.
- Interfaces are defined by the consumer package, not the implementer.
- No import cycles; no package-level mutable state (except cobra wiring).

## Errors and diagnostics (ADR 0002)
- Findings are `diag.Diagnostic` with a registered `TRL-` code; errors carrying findings are `*diag.Error`.
- Wrap errors with `%w`; never compare error strings; context errors propagate unchanged.
- Producers append to a `*diag.Collector`.

## Positions (ADR 0003)
- Use `diag.Source` only. Diagnostics about an IR node use that node's origin.

## Ordering and determinism (ADR 0005, 0010)
- No map iteration in observable output paths. Preserve source order; sort otherwise.
- Time comes only from `internal/clock`. IDs follow ADR 0010.

## Concurrency and resources
- `context.Context` is the first parameter of anything that blocks or does I/O.
- Goroutines are bounded (`errgroup.SetLimit`) and always cancellable; tests check for leaks.
- Collections from APIs or the ledger are streamed with `iter.Seq2`; never materialize a whole organization.
- Never `io.ReadAll` an unbounded body; cap sizes or stream.

## Security
- Secrets never reach logs, events, diagnostics, plans, reports, cache or panics: everything goes through `internal/redact`.
- Files 0600, directories 0700. Paths built from remote names are sanitized.
- TLS verification is always on.

## Logging and output
- `slog` for logs (stderr), events for user-facing progress, stdout only for requested output.

## Tests
- Table-driven, `go-cmp` diffs, goldens under `testdata/` updated with `-update`.
- Spec test vectors are loaded from the spec tables. Every semantic vector carries a `source` (doc URL, reference path or live capture id).
- Vectors and the implementation they test are written by different agents (differential authorship).
- Fuzz targets for every parser and decoder; property tests for round-trips.
- Coverage floor per package (CI): 80% for `httpx`, `ledger`, `redact`, `credpool`, `ratelimit`; 70% elsewhere.

## Dependencies
- No new third-party module without a written reason in the PR (and an ADR if it is architectural).
- Pin wrappers around libraries without semver guarantees (actionlint, go-github).

## AI agent rules
- Never use `git stash` (the stash is shared by every worktree; a pop can restore another agent's work into yours). Set work aside with a temporary WIP commit instead.
- Stay inside the task scope; report spec inconsistencies instead of editing specs silently.
- Mark anything not verified against an official source with `VERIFY:`; each one becomes a tracked issue.

## Review protocol (every PR)
1. CI green: build, vet, lint, tests, race (Linux/macOS), coverage floors, govulncheck, registry check.
2. Adversarial review by a second agent focused on failure modes (retries, auth, persistence, concurrency, ordering, secrets).
3. The maintainer reads every change touching auth, redirects, persistence, the ledger state machine or secrets.
4. Semantic claims without a cited source are rejected.
