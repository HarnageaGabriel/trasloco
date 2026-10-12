# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.1] - 2026-10-13

### Changed

- `PackerBuild@0` and `ServiceFabricUpdateAppVersions@1` are now reported as
  replacements with input changes instead of manual migration and drop-in
  replacement, following the approved rewrite rules.

### Fixed

- The GitHub Action resolves `@vMAJOR` across every page of releases.
- `modernize --check` reads UTF-16 YAML files without a byte order mark
  (Latin-range text) instead of skipping them.

## [0.1.0] - 2026-10-12

### Added

- First public release of the trasloco CLI.
- `modernize --check` detection for actionable retired Azure Pipelines tasks.
- Cross-platform archives, checksums, SPDX JSON SBOMs, keyless signatures, and
  SLSA build provenance.
- The `trasloco modernize` composite GitHub Action for pull-request checks and
  optional SARIF upload.
