# Install and verify trasloco

Release archives contain one static binary plus `LICENSE`, `NOTICE`, and
`README.md`. Choose the archive for your operating system and architecture on
the [releases page](https://github.com/HarnageaGabriel/trasloco/releases).

## Download and verify

The examples use `v0.1.0` on Linux amd64. Change both variables for another
release or platform.

```sh
version=v0.1.0
archive=trasloco_0.1.0_linux_amd64.tar.gz
base=https://github.com/HarnageaGabriel/trasloco/releases/download/$version
curl -fLO "$base/$archive"
curl -fLO "$base/checksums.txt"
curl -fLO "$base/checksums.txt.sig"
curl -fLO "$base/checksums.txt.pem"
sha256sum --check --ignore-missing checksums.txt
cosign verify-blob \
  --certificate checksums.txt.pem \
  --signature checksums.txt.sig \
  --certificate-identity "https://github.com/HarnageaGabriel/trasloco/.github/workflows/release.yml@refs/tags/$version" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
gh attestation verify "$archive" --repo HarnageaGabriel/trasloco
tar -xzf "$archive"
./trasloco version
```

On macOS, use `shasum -a 256 -c checksums.txt`. Windows archives are ZIP files;
PowerShell's `Get-FileHash -Algorithm SHA256` can be used to compare the archive
digest with `checksums.txt`. Every archive also has an SPDX JSON SBOM beside it.

## Check Azure Pipelines

The command accepts files or directories. It exits 5 when `--check` finds an
actionable retired task, 0 when none remain, 64 for invalid usage, and 1 for a
named-path I/O error (or a parse failure with `--strict`).

```sh
trasloco modernize . --check --format text
```

Azure Pipelines can download or cache the verified binary, then run it in an
ordinary script step:

```yaml
steps:
  - script: trasloco modernize "$(Build.SourcesDirectory)" --check --format text
    displayName: Check retired Azure Pipelines tasks
```

## GitHub Action

The composite action downloads the runner's release archive, verifies it
against `checksums.txt`, and fails closed before running the binary. It needs no
secret; the check itself only contacts GitHub Releases for those downloads.
Optional SARIF upload sends the result to GitHub code scanning.

```yaml
permissions:
  contents: read

steps:
  - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
  - uses: HarnageaGabriel/trasloco@v0
    with:
      path: .
      fail-on-findings: true
```

For code-scanning annotations, grant `security-events: write` and request SARIF
upload (fork pull requests may not receive that permission):

```yaml
permissions:
  contents: read
  security-events: write

steps:
  - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
  - uses: HarnageaGabriel/trasloco@v0
    with:
      path: .
      format: sarif
      upload-sarif: true
```

Set `fail-on-findings: false` to report findings without `--check`. The optional
`version` input selects an explicit release tag; it defaults to the release
containing the action.
