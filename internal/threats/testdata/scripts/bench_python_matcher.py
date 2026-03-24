#!/usr/bin/env python3
"""
Benchmark the Python cti-pattern-matcher with the same payloads and scenarios
as the Go matcher benchmarks. Payloads are read from testdata/bench/matcher/
so Go and Python use the exact same JSON files. Reports ns/op and B/op (tracemalloc)
for comparison with Go -benchmem.

Run from repo root: python3 scripts/bench_python_matcher.py

Optional: --iterations N, --scenario SinglePattern_SmallObs|Compiled_SmallObs|MultiplePatterns, --no-memory to skip B/op.
"""

from __future__ import print_function

import argparse
import json
import os
import sys
import time
import tracemalloc

_SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
_REPO_ROOT = os.path.dirname(_SCRIPT_DIR)
_BENCH_MATCHER_DIR = os.path.join(_REPO_ROOT, "testdata", "bench", "matcher")

# Add cti-pattern-matcher to path so we can import without installing into env
_PKG_MATCHER_DIR = os.path.join(_REPO_ROOT, "pkg", "cti-pattern-matcher")
if _PKG_MATCHER_DIR not in sys.path:
    sys.path.insert(0, _PKG_MATCHER_DIR)
_MATCHER_DIR = os.path.join(_REPO_ROOT, "cti-pattern-matcher")
if _MATCHER_DIR not in sys.path:
    sys.path.insert(0, _MATCHER_DIR)

try:
    from stix2matcher.matcher import match, Pattern  # type: ignore[import-untyped]
except ImportError as e:
    print("Failed to import stix2matcher:", e, file=sys.stderr)
    print(
        "Ensure cti-pattern-matcher is present and installed (e.g. pip install -e pkg/cti-pattern-matcher).",
        file=sys.stderr,
    )
    print("See docs/BENCHMARKING.md for setup.", file=sys.stderr)
    sys.exit(1)

STIX_VERSION = "2.1"


def _load_json(path):
    """Load JSON from testdata/bench/matcher (same files as Go benchmarks)."""
    with open(path, "r") as f:
        return json.load(f)


def run_single_pattern_small_obs(observations, pattern, iterations, measure_memory=True):
    """Time match(pattern, observations) over iterations. Returns (ns_per_op, b_per_op or None)."""
    def run_one():
        result = match(pattern, observations, verbose=False, stix_version=STIX_VERSION)
        if not result:
            raise RuntimeError("SinglePattern_SmallObs: pattern unexpectedly did not match")

    if measure_memory:
        tracemalloc.start()
        try:
            _, start_peak = tracemalloc.get_traced_memory()
            start = time.perf_counter()
            for _ in range(iterations):
                run_one()
            _, end_peak = tracemalloc.get_traced_memory()
            elapsed = time.perf_counter() - start
        finally:
            tracemalloc.stop()
        ns_per_op = (elapsed * 1e9) / iterations
        delta = max(0, end_peak - start_peak)
        b_per_op = int(delta / iterations) if iterations else 0
        return ns_per_op, b_per_op
    start = time.perf_counter()
    for _ in range(iterations):
        run_one()
    elapsed = time.perf_counter() - start
    return (elapsed * 1e9) / iterations, None


def run_compiled_small_obs(observations, pattern, iterations, measure_memory=True):
    """Time compiled.match(observations) over iterations. Returns (ns_per_op, b_per_op or None)."""
    compiled = Pattern(pattern, STIX_VERSION)

    def run_one():
        result = compiled.match(observations, verbose=False)
        if not result:
            raise RuntimeError("Compiled_SmallObs: pattern unexpectedly did not match")

    if measure_memory:
        tracemalloc.start()
        try:
            _, start_peak = tracemalloc.get_traced_memory()
            start = time.perf_counter()
            for _ in range(iterations):
                run_one()
            _, end_peak = tracemalloc.get_traced_memory()
            elapsed = time.perf_counter() - start
        finally:
            tracemalloc.stop()
        ns_per_op = (elapsed * 1e9) / iterations
        delta = max(0, end_peak - start_peak)
        b_per_op = int(delta / iterations) if iterations else 0
        return ns_per_op, b_per_op
    start = time.perf_counter()
    for _ in range(iterations):
        run_one()
    elapsed = time.perf_counter() - start
    return (elapsed * 1e9) / iterations, None


def main():
    parser = argparse.ArgumentParser(
        description="Benchmark Python cti-pattern-matcher (same scenarios as Go matcher benchmarks)."
    )
    parser.add_argument(
        "--iterations",
        type=int,
        default=None,
        help="Override default iterations for all scenarios",
    )
    parser.add_argument(
        "--scenario",
        choices=("SinglePattern_SmallObs", "Compiled_SmallObs", "MultiplePatterns"),
        default=None,
        help="Run only this scenario",
    )
    parser.add_argument(
        "--no-memory",
        action="store_true",
        help="Disable tracemalloc B/op measurement (report ns/op only)",
    )
    args = parser.parse_args()

    if not os.path.isdir(_BENCH_MATCHER_DIR):
        print(
            "testdata/bench/matcher not found at %s (run from repo root)" % _BENCH_MATCHER_DIR,
            file=sys.stderr,
        )
        sys.exit(1)

    observed_path = os.path.join(_BENCH_MATCHER_DIR, "observed_small.json")
    patterns_path = os.path.join(_BENCH_MATCHER_DIR, "patterns.json")
    if not os.path.isfile(observed_path) or not os.path.isfile(patterns_path):
        print(
            "Required files not found: observed_small.json and patterns.json in %s" % _BENCH_MATCHER_DIR,
            file=sys.stderr,
        )
        sys.exit(1)

    observations = _load_json(observed_path)
    patterns = _load_json(patterns_path)
    if not patterns:
        print("patterns.json is empty", file=sys.stderr)
        sys.exit(1)

    defaults = {
        "SinglePattern_SmallObs": 1000,
        "Compiled_SmallObs": 1000,
        "MultiplePatterns": 500,
    }
    if args.iterations is not None:
        for k in defaults:
            defaults[k] = args.iterations

    rows = []
    measure_memory = not args.no_memory

    run_single = args.scenario is None or args.scenario == "SinglePattern_SmallObs"
    run_compiled = args.scenario is None or args.scenario == "Compiled_SmallObs"
    run_multi = args.scenario is None or args.scenario == "MultiplePatterns"

    if run_single:
        ns, b = run_single_pattern_small_obs(
            observations, patterns[0], defaults["SinglePattern_SmallObs"], measure_memory
        )
        rows.append(("SinglePattern_SmallObs", defaults["SinglePattern_SmallObs"], ns, b))

    if run_compiled:
        ns, b = run_compiled_small_obs(
            observations, patterns[0], defaults["Compiled_SmallObs"], measure_memory
        )
        rows.append(("Compiled_SmallObs", defaults["Compiled_SmallObs"], ns, b))

    if run_multi:
        for i, pattern in enumerate(patterns):
            name = "MultiplePatterns/pattern_%d" % i
            iters = defaults["MultiplePatterns"]
            ns, b = run_single_pattern_small_obs(observations, pattern, iters, measure_memory)
            rows.append((name, iters, ns, b))

    # Print table (order matches Go benchmark output for side-by-side comparison)
    print("Python cti-pattern-matcher (stix_version=2.1)")
    if measure_memory:
        print("-" * 76)
        print("%-35s %12s %14s %12s" % ("Scenario", "Iterations", "ns/op", "B/op"))
        print("-" * 76)
        for name, iters, ns, b in rows:
            print("%-35s %12d %14.0f %12s" % (name, iters, ns, b if b is not None else "N/A"))
        print("-" * 76)
    else:
        print("-" * 60)
        print("%-35s %12s %14s" % ("Scenario", "Iterations", "ns/op"))
        print("-" * 60)
        for name, iters, ns, _ in rows:
            print("%-35s %12d %14.0f" % (name, iters, ns))
        print("-" * 60)


if __name__ == "__main__":
    main()
