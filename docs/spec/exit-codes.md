# Exit codes

Status: draft for approval (M0). Normative.

The `trasloco` process returns one code representing the run's most actionable terminal class. Reports and events contain per-unit detail.

## Codes

| Code | Name | Meaning |
|---:|---|---|
| 0 | `ok` | Requested operation completed; warnings and informational diagnostics may exist, but no failed, blocked, partial, or stale outcome exists. |
| 1 | `error` | Operation failed before useful completion, or a validation/internal/apply error makes the requested result invalid. |
| 2 | `partial` | At least one independent unit/change completed successfully and at least one failed or was skipped because of failure. |
| 3 | `blocked` | No errors occurred, but one or more requested outcomes remain blocked by `needs-manual` tasks or missing secrets/identity decisions. |
| 4 | `stale` | Apply was refused because source/target/catalog/tool preconditions no longer match the plan. No target mutation from this invocation occurred. |
| 5 | `policy` | A `--fail-on manual|warning` threshold was exceeded without changing diagnostic severity. |
| 64 | `usage` | Command syntax, option combination, selector syntax, or configuration/schema input is invalid. |
| 130 | `cancelled` | The user cancelled the run. Durable work and reports remain recorded. |

Code 64 follows the conventional `EX_USAGE` value, keeping operator/configuration mistakes distinguishable from migration failures. Code 130 follows the conventional SIGINT shell status and is emitted for user cancellation on every supported host. Trasloco reserves 64–78 for future sysexits-compatible CLI/infrastructure failures but MUST NOT emit another value without a specification update.

## Precedence

Compute a terminal class per requested unit, then choose:

1. If invocation input is invalid before a run can start: 64.
2. If the user cancels: 130.
3. If apply preflight detects plan drift/staleness before any mutation: 4.
4. If successes coexist with errors/failure-skips: 2.
5. If any operational error exists and no requested unit completed: 1.
6. If a `--fail-on` threshold is exceeded and no operational error dominates: 5.
7. For `plan` or `apply`, if manual blocks remain and no error dominates: 3.
8. Otherwise: 0.

Staleness discovered after a mutation is an internal invariant violation, not code 4: return 1 or 2 based on completed work and emit an internal diagnostic. Apply MUST check all material preconditions before mutation to prevent this state.

Cancellation requested by the user returns 130 whether or not a unit completed. A recovered panic returns 1, or 2 if durable successful changes already exist. Warnings with fidelity `approximated` do not change code 0 unless a `--fail-on` threshold is exceeded, which returns 5.

For `audit`, `convert`, `modernize`, and `verify`, manual findings return 0 with warnings unless `--fail-on manual|warning` is exceeded. For `plan` and `apply`, unresolved `needs-manual` findings affecting requested outcomes return 3 when no errors dominate. `--fail-on` returns 5 and does not change the diagnostic's stored severity or turn successful units into partial failures.

## Command-specific interpretation

| Command | 0 | 2 | 3 | 4 |
|---|---|---|---|---|
| `audit` | audit complete, findings recorded | some units not audited | not used | not used |
| `convert` | valid outputs emitted; manual placeholders may remain | some outputs valid, some failed | not used | not used |
| `modernize` | valid outputs emitted; manual placeholders may remain | some outputs valid, some failed | not used | not used |
| `plan` | complete applicable plan | some units planned, some failed | plan contains blocked/manual operations | not used |
| `apply` | all applicable changes verified | mixed applied and failed | no failures, but manual/secret gates remain | stale preflight |
| `verify` | verification complete; manual findings recorded | mixed matches/errors | not used | plan/state comparison stale |

Shell scripts MUST compare numerically and MUST NOT treat all nonzero values as retryable. Code 130 is cancellation; code 5 is a CI policy result; code 4 requires re-plan; code 64 requires invocation correction; code 3 requires human/input resolution; code 2 requires report/ledger inspection; code 1 may be retryable only when diagnostics say so.

## Test vectors

| ID | Input outcomes | Expected |
|---|---|---|
| EXIT-001 | all units succeeded | 0 |
| EXIT-002 | succeeded with warnings only | 0 |
| EXIT-003 | one error, no success | 1 |
| EXIT-004 | recovered panic before changes | 1 |
| EXIT-005 | one success, one failed | 2 |
| EXIT-006 | one applied, dependent skipped after failure | 2 |
| EXIT-007 | one success, one manual block, no errors | 3 |
| EXIT-008 | only missing secret blocks | 3 |
| EXIT-009 | stale source plan before mutation | 4 |
| EXIT-010 | target branch SHA drift before mutation | 4 |
| EXIT-011 | selector syntax error | 64 |
| EXIT-012 | unsupported config schemaVersion | 64 |
| EXIT-013 | authentication rejected, no success | 1 |
| EXIT-014 | authentication rejected for one endpoint after another independent unit succeeded | 2 |
| EXIT-015 | audit completes with `needs-manual` findings under default policy | 0 |
| EXIT-016 | same audit with matching `--fail-on` policy | 5 |
| EXIT-017 | approximated fidelity only | 0 |
| EXIT-018 | user cancellation after one durable success | 130 |
| EXIT-019 | user cancellation before success | 130 |
| EXIT-020 | drift detected after mutation due invariant bug | 1 or 2, never 4 |
| EXIT-021 | `convert` emits only valid files plus manual placeholders | 0 with warnings |
| EXIT-022 | `modernize` emits `needs-manual`; `--fail-on manual` set | 5 |
