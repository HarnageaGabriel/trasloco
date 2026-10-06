# Credential policy

Status: draft for approval (M0). Normative.

Credential pooling MUST respect provider terms and MUST NOT multiply rate-limit throughput for one actor. Each credential has a non-secret stable `actor` identity key and `targetScope` key in addition to its credential ID and cache-equivalence scope.

## Registration

The pool key is `(normalized host, targetScope, actor)`. At most one credential MAY be registered for that key. A second credential for the same key MUST be rejected with `TRL-AUTH-0006` before any request is made. Different actors MAY have credentials for the same host and target scope when provider policy permits their independent use.

`actor` identifies the authenticated human, service principal, GitHub App installation identity, or equivalent provider actor; token text, token hash, and arbitrary configuration labels are invalid actor keys. `targetScope` identifies the rate-limit target, such as an organization, tenant, project collection, or installation. Credential ID MAY serve as `actor` only when configuration guarantees a one-to-one mapping to the provider actor. `ScopeID` MAY serve as `targetScope` only when it denotes that target rather than a token-specific cache partition.

Hosts are lower-cased, trailing dots are removed, and URL/port syntax is normalized before comparison. Actor and target-scope keys are trimmed and otherwise compared exactly. Diagnostics and reports MUST NOT include token material or personal identifiers; the violation diagnostic may include only normalized host and non-secret target-scope key.

## Test vectors

| ID | Input | Expected |
|---|---|---|
| CRD-001 | one credential for actor A, `github.com`, org X | accepted |
| CRD-002 | two credentials for actor A, `github.com`, org X | reject second; `TRL-AUTH-0006` |
| CRD-003 | actor A and actor B, same host and org X | accepted when provider policy permits independent actors |
| CRD-004 | actor A, same host, org X and org Y | accepted as distinct target scopes |
| CRD-005 | same actor/scope under `https://API.EXAMPLE.COM` and `api.example.com.` | reject second after host normalization; `TRL-AUTH-0006` |
| CRD-006 | token value used as actor key | invalid configuration; token MUST NOT enter diagnostics |
