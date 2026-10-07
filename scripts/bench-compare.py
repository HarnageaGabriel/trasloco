#!/usr/bin/env python3
"""Fail when a listed benchmark regresses more than THRESHOLD vs the baseline.

Usage: bench-compare.py baseline.txt current.txt [benchmark-name-prefix ...]
Compares the median ns/op per benchmark. With no prefixes, all benchmarks are checked.
"""
import re
import statistics
import sys

THRESHOLD = 0.10
pattern = re.compile(r"^(Benchmark\S+?)(?:-\d+)?\s+\d+\s+([\d.]+)\s+ns/op")


def load(path):
    samples = {}
    with open(path) as handle:
        for line in handle:
            match = pattern.match(line)
            if match:
                samples.setdefault(match.group(1), []).append(float(match.group(2)))
    return {name: statistics.median(values) for name, values in samples.items()}


base, cur = load(sys.argv[1]), load(sys.argv[2])
prefixes = sys.argv[3:]
failed = []
for name in sorted(cur):
    if name not in base or (prefixes and not any(name.startswith(p) for p in prefixes)):
        continue
    change = cur[name] / base[name] - 1
    print(f"{name}: {base[name]:.0f} -> {cur[name]:.0f} ns/op ({change:+.1%})")
    if change > THRESHOLD:
        failed.append(name)
if failed:
    sys.exit("Regression >10%: " + ", ".join(failed))
