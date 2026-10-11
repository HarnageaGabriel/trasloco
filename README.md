<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/trasloco-logo-dark.svg">
    <img src="docs/assets/trasloco-logo.svg" alt="trasloco" width="420">
  </picture>
</p>

# trasloco

`trasloco` is an open-source Go CLI for planning and executing auditable CI/CD
pipeline and DevOps migrations between platforms.

> **Status:** pre-alpha. Foundations (M0) are complete and the Azure Pipelines
> dialect (M1: parser, expressions, template resolver, variables, normalization)
> is in progress. Not ready for production use; interfaces will change.

## Principles

- Plan before mutation and make every change reviewable.
- Keep secrets out of configuration and logs.
- Prefer deterministic conversion with explicit unsupported cases.
- Use least privilege, pinned actions, resumable execution, and verification.
- Build from public documentation under a clean-room process.

Planned commands: `init`, `auth`, `audit`, `convert`, `modernize`, `plan`,
`apply`, `resume`, `verify`, `rollback`, `cutover`, `secrets`, `report`, and
`catalog`.

## Build

Go 1.25 or newer is required.

```sh
make build
./bin/trasloco version
make test
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for development requirements.

## License

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
