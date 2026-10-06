# trasloco

`trasloco` is an open-source Go CLI for planning and executing auditable CI/CD
pipeline and DevOps migrations between platforms.

> **Status:** pre-alpha, not usable yet. Commands other than `version` are
> placeholders; no migration logic exists.

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
