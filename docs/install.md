# Install and verify trasloco

Release archives contain one static binary plus `LICENSE`, `NOTICE`, and
`README.md`. Choose the archive for your operating system and architecture on
the [releases page](https://github.com/HarnageaGabriel/trasloco/releases).

## Download and verify

The examples use `v0.2.1` on Linux amd64. Change both variables for another
release or platform.

```bash
set -euo pipefail

version=v0.2.1
archive=trasloco_0.2.1_linux_amd64.tar.gz
base=https://github.com/HarnageaGabriel/trasloco/releases/download/$version
curl -fLO "$base/$archive"
curl -fLO "$base/checksums.txt"
curl -fLO "$base/checksums.txt.sigstore.json"
cosign verify-blob \
  --bundle checksums.txt.sigstore.json \
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

`gh attestation verify` needs `gh auth login` or a `GH_TOKEN` environment
variable.

On macOS, use `shasum` and the same order (all verification before extracting):

```bash
set -euo pipefail

version=v0.2.1
archive=trasloco_0.2.1_darwin_arm64.tar.gz
base=https://github.com/HarnageaGabriel/trasloco/releases/download/$version
curl -fLO "$base/$archive"
curl -fLO "$base/checksums.txt"
curl -fLO "$base/checksums.txt.sigstore.json"
cosign verify-blob \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity "https://github.com/HarnageaGabriel/trasloco/.github/workflows/release.yml@refs/tags/$version" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
checksum_line=$(awk -v file="$archive" '
  ($2 == file || $2 == "*" file) { count++; line = $1 "  " file }
  END { if (count == 1) print line; else exit 1 }
' checksums.txt)
printf '%s\n' "$checksum_line" | shasum -a 256 -c -
gh attestation verify "$archive" --repo HarnageaGabriel/trasloco
tar -xzf "$archive"
./trasloco version
```

Windows archives are ZIP files. In PowerShell, verify `checksums.txt` with
`cosign verify-blob --bundle checksums.txt.sigstore.json` (same identity and
issuer flags as above), compare `Get-FileHash` only with the exact archive entry,
run `gh attestation verify`, and only then expand the archive:

```powershell
$ErrorActionPreference = 'Stop'
$version = 'v0.2.1'
$archive = 'trasloco_0.2.1_windows_amd64.zip'
$base = "https://github.com/HarnageaGabriel/trasloco/releases/download/$version"
Invoke-WebRequest "$base/$archive" -OutFile $archive
Invoke-WebRequest "$base/checksums.txt" -OutFile checksums.txt
Invoke-WebRequest "$base/checksums.txt.sigstore.json" -OutFile checksums.txt.sigstore.json
cosign verify-blob --bundle checksums.txt.sigstore.json `
  --certificate-identity "https://github.com/HarnageaGabriel/trasloco/.github/workflows/release.yml@refs/tags/$version" `
  --certificate-oidc-issuer https://token.actions.githubusercontent.com `
  checksums.txt
if ($LASTEXITCODE -ne 0) { throw 'cosign verification failed' }
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
gh attestation verify $archive --repo HarnageaGabriel/trasloco
if ($LASTEXITCODE -ne 0) { throw 'attestation verification failed' }
Expand-Archive $archive -DestinationPath .
.\trasloco.exe version
```

Every archive also has an SPDX JSON SBOM beside it.

## Check Azure Pipelines

The command accepts files or directories. It exits 5 when `--check` finds an
actionable retired task, 0 when none remain, 64 for invalid usage, and 1 for an
I/O error. With `--check` or `--write`, any discovered file that cannot be read,
decoded, or is too large to scan, or any directory that cannot be traversed,
makes the scan incomplete and exits 1; findings from files that were scanned are
still reported. A parse failure also exits 1 with `--strict`. Exit 1 takes
precedence over exit 5.

```sh
trasloco modernize --check --format text -- .
```

This complete Azure Pipelines example pins a release, verifies the exact Linux
amd64 archive entry before extraction, and runs the check from a Bash step.
It verifies the archive against `checksums.txt` from the same release only (no
cosign or attestation); see [Download and verify](#download-and-verify) for the
full verification.

```yaml
pool:
  vmImage: ubuntu-latest

steps:
  - bash: |
      set -euo pipefail
      version=v0.2.1
      archive=trasloco_0.2.1_linux_amd64.tar.gz
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

The composite action downloads the runner's release archive and checksum files,
verifies the Sigstore bundle for `checksums.txt` with cosign, then verifies the
archive's SHA-256 checksum before running it. It does not perform the additional
GitHub artifact attestation check shown in [Download and verify](#download-and-verify).
The automatically provided GitHub token is used only for release API calls and
downloads and is removed from the environment before trasloco runs.
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
`strict` input defaults to `true`; set it to `false` to report pipeline parse
errors without failing. With `strict: true`, a pipeline that cannot be parsed
makes the run exit 1 and, with `format: sarif`, the SARIF file is then not
uploaded, because only complete reports (exit 0 or 5) are uploaded to code
scanning. The optional
`version` input selects an exact `vMAJOR.MINOR.PATCH` tag or a `vMAJOR` alias.
With its default empty value, the action uses its own ref: `@v0.2.1` downloads
that exact release, while `@v0` resolves the highest non-prerelease, non-draft
`v0.x.y` release through the GitHub Releases API. Branches, commit SHAs,
prerelease tags, and other refs are rejected unless `version` is set, so
when you pin the action by commit SHA also set `version: v0.2.1`. The moving `@v0` alias points to
the highest released v0 version, while `@v0.2.1` is immutable.
