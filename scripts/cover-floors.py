#!/usr/bin/env python3
"""Per-package coverage floors (CONVENTIONS.md). Used by CI and `make cover-floors`."""
import collections
import pathlib
import sys

totals = collections.defaultdict(lambda: [0, 0])
for line in pathlib.Path("coverage.out").read_text().splitlines()[1:]:
    location, statements, count = line.rsplit(" ", 2)
    package = location.rsplit("/", 1)[0]
    totals[package][0] += int(statements)
    totals[package][1] += int(statements) if int(count) else 0
failures = []
critical = {"httpx", "ledger", "redact", "credpool", "ratelimit"}
# Temporary exemptions with an expiry: remove when the linked work lands.
exempt = {"cache"}  # ADR 0008 cache backend redesign, before M3
for package, (total, covered) in sorted(totals.items()):
    floor = 80 if package.rsplit("/", 1)[-1] in critical else 70
    percent = 100 * covered / total if total else 100
    print(f"{package}: {percent:.1f}% (floor {floor}%)")
    if package.rsplit("/", 1)[-1] in exempt:
        continue
    if total and 100 * covered < floor * total:
        failures.append(package)
if not totals:
    sys.exit("Empty coverage profile")
if failures:
    sys.exit("Coverage below floor: " + ", ".join(failures))
