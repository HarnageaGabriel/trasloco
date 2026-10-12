# Install and verify trasloco

Release archives contain one static binary plus `LICENSE`, `NOTICE`, and
`README.md`. Choose the archive for your operating system and architecture on
the [releases page](https://github.com/HarnageaGabriel/trasloco/releases).

## Download and verify

The examples use `v0.1.0` on Linux amd64. Change both variables for another
release or platform.

```bash
set -euo pipefail

version=v0.1.0
archive=trasloco_0.1.0_linux_amd64.tar.gz
base=https://github.com/HarnageaGabriel/trasloco/releases/download/$version
curl -fLO "$base/$archive"
curl -fLO "$base/checksums.txt"
curl -fLO "$base/checksums.txt.sig"
curl -fLO "$base/checksums.txt.pem"
cosign verify-blob \
  --certificate checksums.txt.pem \
  --signature checksums.txt.sig \
  --certificate-identity "https://github.com/HarnageaGabriel/trasloco/.github/workflows/release.yml@refs/tags/$version" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
checksum_line=$(awk -v file="$archive" '
  ($2 == file || $2 == "*" file) { count++; line = $1 "  " file }
  END { if (count == 1) print line; else exit 1 }
' checksums.txt)
printf '%s\n' "$checksum_line" | sha256sum -c -
gh attestation verify "$archive" --repo HarnageaGabriel/trasloco
tar -xzf "$archive"
./trasloco version
```

On macOS, select the same single entry and pass it to `shasum`:

```bash
checksum_line=$(awk -v file="$archive" '
  ($2 == file || $2 == "*" file) { count++; line = $1 "  " file }
  END { if (count == 1) print line; else exit 1 }
' checksums.txt)
printf '%s\n' "$checksum_line" | shasum -a 256 -c -
```

Windows archives are ZIP files. In PowerShell, compare `Get-FileHash` only with
the exact archive entry:

```powershell
$ErrorActionPreference = 'Stop'
$archive = 'trasloco_0.1.0_windows_amd64.zip'
$entryHashes = @(
  foreach ($line in Get-Content checksums.txt) {
    if ($line -match '^([0-9a-fA-F]{64}) [ *](.+)$' -and $Matches[2] -ceq $archive) {
      $Matches[1]
    }
  }
)
if ($entryHashes.Count -ne 1) {
  throw "checksums.txt must contain exactly one SHA-256 entry for $archive"
}
$actual = (Get-FileHash -Algorithm SHA256 $archive).Hash
if ($actual -ine $entryHashes[0]) {
  throw "SHA-256 verification failed for $archive"
}
```

Every archive also has an SPDX JSON SBOM beside it.

## Check Azure Pipelines

The command accepts files or directories. It exits 5 when `--check` finds an
actionable retired task, 0 when none remain, 64 for invalid usage, and 1 for a
named-path I/O error (or a parse failure with `--strict`).

```sh
trasloco modernize --check --format text -- .
```

This complete Azure Pipelines example pins a release, verifies the exact Linux
amd64 archive entry before extraction, and runs the check from a Bash step:

```yaml
pool:
  vmImage: ubuntu-latest

steps:
  - bash: |
      set -euo pipefail
      version=v0.1.0
      archive=trasloco_0.1.0_linux_amd64.tar.gz
      base="https://github.com/HarnageaGabriel/trasloco/releases/download/$version"
      work_dir="$(Agent.TempDirectory)/trasloco-$version"
      mkdir -p "$work_dir"
      cd "$work_dir"
      curl -fLO "$base/$archive"
      curl -fLO "$base/checksums.txt"
      checksum_line=$(awk -v file="$archive" '
        ($2 == file || $2 == "*" file) { count++; line = $1 "  " file }
        END { if (count == 1) print line; else exit 1 }
      ' checksums.txt)
      printf '%s\n' "$checksum_line" | sha256sum -c -
      tar -xzf "$archive"
      "$work_dir/trasloco" modernize --check -- "$(Build.SourcesDirectory)"
    displayName: Check retired Azure Pipelines tasks
```

Exit 5 means actionable retired tasks remain and fails the script step.

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
`version` input selects an explicit release tag. With its default empty value,
an exact action ref such as `@v0.1.0` downloads that release. A major alias such
as `@v0`, a branch, or a commit SHA resolves the latest release through GitHub's
public `/releases/latest` redirect; no token is needed. The `@v0` major alias
moves to each non-prerelease v0 release, while `@v0.1.0` is immutable.
