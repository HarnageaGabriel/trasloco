#!/bin/sh
# Same check as CI: run tests with a coverage profile, then enforce floors.
set -eu
go test -coverprofile=coverage.out ./...
python3 scripts/cover-floors.py
