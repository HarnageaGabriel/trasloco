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
| 64 | `usage` | Command syntax, option combination, selector syntax, or configuration/schema input is invalid. |

Code 64 follows the conventional `EX_USAGE` value, keeping operator/configuration mistakes distinguishable from migration failures. Trasloco reserves 64–78 for future sysexits-compatible CLI/infrastructure failures but MUST NOT emit another value without a specification update. Signals/host termination codes are controlled by the operating system and are outside this contract.

## Precedence

Compute a terminal class per requested unit, then choose:

1. If invocation input is invalid before a run can start: 64.
2. If apply preflight detects plan drift/staleness before any mutation: 4.
3. If successes coexist with errors/failure-skips: 2.
4. If any error exists and no requested unit completed: 1.
5. If successes coexist only with manual blocks: 3, because the requested migration is not fully activatable.
6. If only manual blocks exist: 3.
7. Otherwise: 0.

Staleness discovered after a mutation is an internal invariant violation, not code 4: return 1 or 2 based on completed work and emit an internal diagnostic. Apply MUST check all material preconditions before mutation to prevent this state.

Cancellation requested by the user returns 1 when no unit completed and 2 when some completed, unless the host replaces the code because of a signal. A recovered panic returns 1, or 2 if durable successful changes already exist. Warnings with fidelity `approximated` do not change code 0 unless policy promotes their diagnostic exit class.

For `audit`, manual findings are the product output and return 0 unless an explicit `--fail-on` policy promotes them. For `convert`, `plan`, `apply`, and `verify`, unresolved `needs-manual` findings affecting requested outcomes return 3 when no errors dominate. `--fail-on warning` promotes matching warnings to error class and follows the same partial rules; it does not change the diagnostic's stored severity.

## Command-specific interpretation

| Command | 0 | 2 | 3 | 4 |
|---|---|---|---|---|
| `audit` | audit complete, findings recorded | some units not audited | only with policy promotion to blocked | not used |
| `convert` | all requested outputs valid | some outputs valid, some failed | valid outputs exist but requested manual work remains | not used |
| `plan` | complete applicable plan | some units planned, some failed | plan contains blocked/manual operations | not used |
| `apply` | all applicable changes verified | mixed applied and failed | no failures, but manual/secret gates remain | stale preflight |
| `verify` | target matches requested verified state | mixed matches/errors | verification awaits manual prerequisite | plan/state comparison stale |

Shell scripts MUST compare numerically and MUST NOT treat all nonzero values as retryable. Code 4 requires re-plan; code 64 requires invocation correction; code 3 requires human/input resolution; code 2 requires report/ledger inspection; code 1 may be retryable only when diagnostics say so.

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
| EXIT-016 | same audit with matching `--fail-on` policy | 1, or 2 if other units completed |
| EXIT-017 | approximated fidelity only | 0 |
| EXIT-018 | user cancellation after one durable success | 2 |
| EXIT-019 | user cancellation before success | 1 |
| EXIT-020 | drift detected after mutation due invariant bug | 1 or 2, never 4 |

## Open questions

1. Should user cancellation have a dedicated sysexits-range code rather than 1/2?
2. Should `convert` return 3 when manual tasks are emitted as explicitly failing placeholders but all files validate structurally?
3. Should `--fail-on warning` return 1 or a dedicated policy code for CI consumers?
