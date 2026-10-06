# Contributing

Thank you for improving trasloco. By contributing, you agree to follow the
[Code of Conduct](CODE_OF_CONDUCT.md).

## Developer Certificate of Origin

All commits must carry a Developer Certificate of Origin sign-off. Sign commits
with:

```sh
git commit -s
```

The `Signed-off-by` line certifies the contribution under the
[Developer Certificate of Origin 1.1](https://developercertificate.org/).

## Development

Install Go 1.25+, `golangci-lint`, and `govulncheck`, then run:

```sh
make tidy
make fmt
make test
make lint
make vuln
```

`make build` writes the CLI to `bin/trasloco`. `make cover` writes
`coverage.out`.

Keep packages small, pass `context.Context` explicitly, wrap errors with useful
context, add table-driven tests, and preserve stdout for command data. Logs and
diagnostics belong on stderr. New exported identifiers need doc comments.

## Clean-room rule

Do not copy private, employer-owned, or customer-owned code, schemas, fixtures,
or documentation. Build from public documentation and original tests. Every
catalog entry must cite the public documentation it derives from in a `source:`
field. Contributions without verifiable provenance will not be accepted.
