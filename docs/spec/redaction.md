# Output redaction

Construct the run collector with `diag.NewCollector(diag.WithRedactor(r))`.
Every added finding is redacted before deduplication or storage, including its
message, nested parameters, source and resource. Producers need not repeat the
option. `diag.WithPerCodeCap(n)` configures the collector cap (default 1000;
non-positive disables it). The zero-value collector uses built-in patterns.

URL credentials are removed from arbitrary output strings, including error
messages, event payload strings and slog values, without requiring a URL-named
field. Query keys are case-insensitive and percent-decoded for matching; query
ordering and non-sensitive fields are preserved. All occurrences are masked.

- Azure SAS: `sig`; `se`, `sp`, `sv` when the same query contains `sig`.
  [Microsoft service SAS fields](https://learn.microsoft.com/en-us/rest/api/storageservices/create-service-sas).
- AWS: `X-Amz-Signature`, `X-Amz-Credential`, `X-Amz-Security-Token`.
  [AWS query authentication](https://docs.aws.amazon.com/AmazonS3/latest/API/sigv4-query-string-auth.html).
- Google Cloud: `X-Goog-Signature`, `X-Goog-Credential`; legacy `signature`.
  [Google signed URLs](https://cloud.google.com/storage/docs/access-control/signed-urls).
- OAuth-style `token` and `access_token` values.
  [Bearer token query parameter](https://www.rfc-editor.org/rfc/rfc6750#section-2.3).

Built-in token detection includes default GitLab `glpat-`, `glptt-`, `gldt-`
prefixes followed by at least 20 token characters, and Slack `xoxb-` bot tokens.
These are output detection heuristics, not token validators. Custom GitLab
prefixes and other opaque secrets must be registered on the run redactor.
Sources: [GitLab token prefixes](https://docs.gitlab.com/security/tokens/),
[Slack token types](https://docs.slack.dev/authentication/tokens/).

Basic and Bearer credential detection requires an Authorization header context;
ordinary prose such as “basic setup” is preserved. `event.ErrRedaction()` returns
a fresh `*diag.Error` with `TRL-SECRET-0004`; callers identify it with `errors.As`
and the finding code.
