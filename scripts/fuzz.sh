#!/bin/sh
# Run every Fuzz* target for the given duration (default 60s). Corpora live in testdata/fuzz.
set -eu
FUZZTIME="${1:-60s}"
status=0
for pkg in $(go list ./...); do
  dir=$(go list -f '{{.Dir}}' "$pkg")
  targets=$(grep -h '^func Fuzz[A-Za-z0-9_]*(' "$dir"/*_test.go 2>/dev/null | sed 's/^func \(Fuzz[A-Za-z0-9_]*\)(.*/\1/' || true)
  for target in $targets; do
    echo "== $pkg $target"
    go test "$pkg" -run '^$' -fuzz "^${target}\$" -fuzztime "$FUZZTIME" || status=1
  done
done
exit $status
