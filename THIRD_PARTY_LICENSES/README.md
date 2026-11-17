# Third-party licenses

Dependency license metadata is tracked with releases. Copies of MIT, BSD, or
other notices required for data or code derived into this repository belong in
this directory, together with provenance and the affected files.

Do not copy catalog data without recording its public source and license.

## Entries

- actionlint-MIT.txt: rhysd/actionlint, version v1.7.12; the linter is imported
  by internal/actionlint/ and wrapped by that package.
- azure-pipelines-tasks-MIT.txt: microsoft/azure-pipelines-tasks, pinned commit
  f1962f12fac2899b5d689967dda0d25523cc3ec4; factual task metadata generated
  into catalog/azure-pipelines/tasks.generated.yaml.
- azure-pipelines-yaml-MIT.txt: microsoft/azure-pipelines-yaml, commit 5d433fcad43a32789a968179469c1e6271cb2fc7; templates/*.yml vendored under testdata/azurepipelines/corpus/vendored/azure-pipelines-yaml/ (see testdata/NOTICE.md).

### Transitive via actionlint

- github.com/bmatcuk/doublestar/v4 v4.10.0: MIT License.
- github.com/clipperhouse/uax29/v2 v2.7.0: MIT License.
- github.com/fatih/color v1.19.0: MIT License.
- github.com/mattn/go-colorable v0.1.14: MIT License.
- github.com/mattn/go-runewidth v0.0.21: MIT License.
- github.com/mattn/go-shellwords v1.0.12: MIT License.
- github.com/robfig/cron/v3 v3.0.1: MIT License.
- go.yaml.in/yaml/v4 v4.0.0-rc.3: Apache License 2.0; this release
  candidate is required by actionlint v1.7.12.
