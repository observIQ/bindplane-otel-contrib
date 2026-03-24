#!/usr/bin/env python3
"""
Benchmark the Python cti-stix-validator with the same payloads and scenarios
as the Go validator E2E benchmarks. Payloads are read from testdata/bench/
so Go and Python use the exact same JSON files. Reports ns/op and B/op (tracemalloc)
for comparison with Go -benchmem.

Run from repo root: python3 scripts/bench_python_validator.py

Optional: --iterations N, --scenario single|bundle3|scaling, --no-memory to skip B/op.
Use --verify to run one iteration per scenario and print OK (confirms validation runs).
Use -v/--verbose to log testdata paths, schema dir, and each scenario as it runs.
"""

from __future__ import print_function

import argparse
import json
import os
import sys
import time
import tracemalloc

# #region agent log
def _debug_log(hypothesis_id, location, message, data):
    try:
        path = os.path.join(os.path.dirname(__file__), "..", ".cursor", "debug-5a5036.log")
        path = os.path.abspath(path)
        with open(path, "a") as f:
            f.write(json.dumps({"sessionId": "5a5036", "hypothesisId": hypothesis_id, "location": location, "message": message, "data": data, "timestamp": int(time.time() * 1000)}) + "\n")
    except Exception:
        pass
# #endregion

# Add cti-stix-validator to path so we can import without installing into env
_SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
_REPO_ROOT = os.path.dirname(_SCRIPT_DIR)
_BENCH_TESTDATA = os.path.join(_REPO_ROOT, "testdata", "bench")
# MITRE ATT&CK STIX bundle (e2e testdata); same file as Go BenchmarkValidateReader_MITREAttack.
_MITRE_ATTACK_JSON = os.path.join(_REPO_ROOT, "internal", "stixvalidator", "e2e", "testdata", "attack.json")
# Optional: use repo STIX 2.1 schemas so validation works when cti-stix-validator
# has no/empty bundled schemas-2.1. Try pkg/ first, then subtree at repo root.
_SCHEMAS_CANDIDATES = [
    os.path.join(_REPO_ROOT, "pkg", "cti-stix2-json-schemas", "schemas"),
    os.path.join(_REPO_ROOT, "cti-stix2-json-schemas", "schemas"),
]
_VALIDATOR_DIR = os.path.join(_REPO_ROOT, "cti-stix-validator")
if _VALIDATOR_DIR not in sys.path:
    sys.path.insert(0, _VALIDATOR_DIR)
# Also try pkg/ for editable installs: pip install -e pkg/cti-stix-validator
_PKG_VALIDATOR_DIR = os.path.join(_REPO_ROOT, "pkg", "cti-stix-validator")
if _PKG_VALIDATOR_DIR not in sys.path:
    sys.path.insert(0, _PKG_VALIDATOR_DIR)

try:
    from stix2validator import ValidationOptions, validate_string  # type: ignore[import-untyped]
except ImportError as e:
    print("Failed to import stix2validator:", e, file=sys.stderr)
    print(
        "Ensure cti-stix-validator is present and installed (e.g. pip install -e cti-stix-validator).",
        file=sys.stderr,
    )
    print("See docs/BENCHMARKING.md for setup.", file=sys.stderr)
    sys.exit(1)


def _load_payload(path):
    """Load JSON payload from testdata/bench (same files as Go benchmarks)."""
    with open(path, "r") as f:
        return f.read()


def _resolve_schema_dir():
    """Return an absolute path to a schema directory that contains indicator and core schemas, or None."""
    for candidate in _SCHEMAS_CANDIDATES:
        if not os.path.isdir(candidate):
            # #region agent log
            _debug_log("H1", "bench_python_validator.py:_resolve_schema_dir", "candidate not a dir", {"candidate": candidate})
            # #endregion
            continue
        abs_path = os.path.abspath(candidate)
        # cti-stix2-json-schemas layout: sdos/indicator.json, common/core.json
        ind_path = os.path.join(abs_path, "sdos", "indicator.json")
        core_path = os.path.join(abs_path, "common", "core.json")
        has_ind = os.path.isfile(ind_path)
        has_core = os.path.isfile(core_path)
        # #region agent log
        _debug_log("H1", "bench_python_validator.py:_resolve_schema_dir", "candidate check", {"candidate": abs_path, "has_sdos_indicator": has_ind, "has_common_core": has_core})
        # #endregion
        if has_ind and has_core:
            return abs_path
    return None


def run_scenario(name, payload, iterations, options, measure_memory=True):
    """Time validate_string(payload, options) over iterations. Returns (ns_per_op, b_per_op or None)."""
    def run_one():
        results = validate_string(payload, options)
        if hasattr(results, "is_valid"):
            valid = results.is_valid
            res_list = [results]
        else:
            valid = all(r.is_valid for r in results)
            res_list = results if isinstance(results, list) else [results]
        if not valid:
            err_lines = []
            for r in res_list:
                if hasattr(r, "errors") and r.errors:
                    err_lines.extend(r.errors)
                if hasattr(r, "warnings") and r.warnings:
                    err_lines.extend("(warn) %s" % w for w in r.warnings)
            raise RuntimeError(
                "%s: validation unexpectedly failed: %s"
                % (name, "; ".join(str(e) for e in err_lines) if err_lines else "no errors/warnings")
            )

    b_per_op = None
    if measure_memory:
        tracemalloc.start()
        try:
            _, start_peak = tracemalloc.get_traced_memory()
            start = time.perf_counter()
            for i in range(iterations):
                if i == 0:
                    _debug_log("H2", "bench_python_validator.py:run_scenario", "before validate_string", {"scenario": name, "options_schema_dir": getattr(options, "schema_dir", None)})
                run_one()
            _, end_peak = tracemalloc.get_traced_memory()
            elapsed = time.perf_counter() - start
        finally:
            tracemalloc.stop()
        ns_per_op = (elapsed * 1e9) / iterations
        delta = max(0, end_peak - start_peak)
        b_per_op = int(delta / iterations) if iterations else 0
    else:
        start = time.perf_counter()
        for i in range(iterations):
            if i == 0:
                _debug_log("H2", "bench_python_validator.py:run_scenario", "before validate_string", {"scenario": name, "options_schema_dir": getattr(options, "schema_dir", None)})
            run_one()
        elapsed = time.perf_counter() - start
        ns_per_op = (elapsed * 1e9) / iterations

    return ns_per_op, b_per_op


def _run_verify(bench_dir, schema_dir, verbose=False):
    """Run one validation per scenario; print OK for each. Confirms same testdata and validation as Go."""
    if verbose:
        print("verbose: verify mode: one validation per scenario (same testdata as Go)")
        print("verbose: bench_dir=%s schema_dir=%s" % (bench_dir, schema_dir or "(bundled)"))
        print()
    options = ValidationOptions(version="2.1", silent=True, schema_dir=schema_dir)
    scenarios = [
        ("SingleObject", "single_object.json"),
        ("Bundle_3", "bundle_3.json"),
        ("Scaling_N=1", "bundle_1.json"),
        ("Scaling_N=10", "bundle_10.json"),
        ("Scaling_N=100", "bundle_100.json"),
        ("MITREAttack", _MITRE_ATTACK_JSON),
    ]
    for name, path_or_fname in scenarios:
        path = path_or_fname if os.path.isabs(path_or_fname) else os.path.join(bench_dir, path_or_fname)
        if not os.path.isfile(path):
            if name == "MITREAttack":
                print("SKIP MITREAttack (file not found: %s)" % path)
                continue
            print("FAIL %s (file not found)" % name, file=sys.stderr)
            sys.exit(1)
        if verbose:
            print("verbose: validating %s <- %s" % (name, path))
        payload = _load_payload(path)
        results = validate_string(payload, options)
        if hasattr(results, "is_valid"):
            valid = results.is_valid
        else:
            valid = all(r.is_valid for r in results)
        if not valid:
            print("FAIL %s" % name, file=sys.stderr)
            sys.exit(1)
        print("OK %s (same testdata as Go: %s)" % (name, path if name == "MITREAttack" else "testdata/bench/" + os.path.basename(path)))
    print("Verify done: Python validator ran full validation on same payloads as Go benchmarks.")


def main():
    parser = argparse.ArgumentParser(
        description="Benchmark Python cti-stix-validator (same scenarios as Go E2E benchmarks)."
    )
    parser.add_argument(
        "--iterations",
        type=int,
        default=None,
        help="Override default iterations for all scenarios",
    )
    parser.add_argument(
        "--scenario",
        choices=("single", "bundle3", "scaling", "mitre"),
        default=None,
        help="Run only this scenario (scaling = N=1, N=10, N=100; mitre = MITRE ATT&CK attack.json)",
    )
    parser.add_argument(
        "--no-memory",
        action="store_true",
        help="Disable tracemalloc B/op measurement (report ns/op only)",
    )
    parser.add_argument(
        "--verify",
        action="store_true",
        help="Run one iteration per scenario, print OK, and exit (confirms validation runs)",
    )
    parser.add_argument(
        "-v", "--verbose",
        action="store_true",
        help="Log testdata paths, schema dir, and each scenario to stdout",
    )
    args = parser.parse_args()

    if not os.path.isdir(_BENCH_TESTDATA):
        print("testdata/bench not found at %s (run from repo root)" % _BENCH_TESTDATA, file=sys.stderr)
        sys.exit(1)

    if args.verify:
        _run_verify(_BENCH_TESTDATA, _resolve_schema_dir(), verbose=getattr(args, "verbose", False))
        return

    # #region agent log
    _debug_log("H1", "bench_python_validator.py:main", "repo and cwd", {"_REPO_ROOT": _REPO_ROOT, "cwd": os.getcwd(), "candidates": _SCHEMAS_CANDIDATES})
    # #endregion

    # Use repo schemas if present (absolute path so validator finds them regardless of cwd).
    # Needed when cti-stix-validator has no/empty bundled schemas-2.1.
    schema_dir = _resolve_schema_dir()
    # #region agent log
    _debug_log("H1", "bench_python_validator.py:main", "resolved schema_dir", {"schema_dir": schema_dir})
    # #endregion
    if schema_dir is None:
        print(
            "Note: no repo schema dir found (looked for pkg/cti-stix2-json-schemas/schemas and "
            "cti-stix2-json-schemas/schemas). Using validator's bundled schemas.",
            file=sys.stderr,
        )
    options = ValidationOptions(version="2.1", silent=True, schema_dir=schema_dir)
    # #region agent log
    _debug_log("H3", "bench_python_validator.py:main", "options.schema_dir", {"options_schema_dir": getattr(options, "schema_dir", None)})
    # #endregion

    if args.verbose:
        print("verbose: repo_root=%s" % _REPO_ROOT)
        print("verbose: testdata_bench=%s" % _BENCH_TESTDATA)
        print("verbose: schema_dir=%s" % (schema_dir or "(validator bundled)"))
        print("verbose: options version=2.1 silent=True")
        print("verbose: scenarios (same files as Go): single_object.json, bundle_3.json, bundle_1/10/100.json")
        print()

    # Default iterations per scenario (tune so run finishes in a few seconds)
    defaults = {
        "SingleObject": 1000,
        "Bundle_3": 500,
        "Scaling_N=1": 500,
        "Scaling_N=10": 100,
        "Scaling_N=100": 10,
        "MITREAttack": 5,
    }
    if args.iterations is not None:
        for k in defaults:
            defaults[k] = args.iterations

    rows = []
    measure_memory = not args.no_memory

    def do(name, payload_path, iterations):
        if args.verbose:
            print("verbose: running scenario %s payload=%s iterations=%d" % (name, payload_path, iterations))
        payload = _load_payload(payload_path)
        ns_per_op, b_per_op = run_scenario(name, payload, iterations, options, measure_memory=measure_memory)
        if args.verbose:
            extra = " B/op=%s" % (b_per_op if b_per_op is not None else "N/A")
            print("verbose:   -> ns/op=%.0f%s" % (ns_per_op, extra))
        rows.append((name, iterations, ns_per_op, b_per_op))

    run_single = args.scenario is None or args.scenario == "single"
    run_bundle3 = args.scenario is None or args.scenario == "bundle3"
    run_scaling = args.scenario is None or args.scenario == "scaling"
    run_mitre = args.scenario is None or args.scenario == "mitre"

    if run_single:
        do("SingleObject", os.path.join(_BENCH_TESTDATA, "single_object.json"), defaults["SingleObject"])
    if run_bundle3:
        do("Bundle_3", os.path.join(_BENCH_TESTDATA, "bundle_3.json"), defaults["Bundle_3"])
    if run_scaling:
        do("Scaling_N=1", os.path.join(_BENCH_TESTDATA, "bundle_1.json"), defaults["Scaling_N=1"])
        do("Scaling_N=10", os.path.join(_BENCH_TESTDATA, "bundle_10.json"), defaults["Scaling_N=10"])
        do("Scaling_N=100", os.path.join(_BENCH_TESTDATA, "bundle_100.json"), defaults["Scaling_N=100"])
    if run_mitre and os.path.isfile(_MITRE_ATTACK_JSON):
        do("MITREAttack", _MITRE_ATTACK_JSON, defaults["MITREAttack"])

    # Print table
    print("Python cti-stix-validator (version=2.1, silent=True)")
    if measure_memory:
        print("-" * 76)
        print("%-20s %12s %14s %12s" % ("Scenario", "Iterations", "ns/op", "B/op"))
        print("-" * 76)
        for name, iters, ns, b in rows:
            print("%-20s %12d %14.0f %12s" % (name, iters, ns, b if b is not None else "N/A"))
        print("-" * 76)
    else:
        print("-" * 60)
        print("%-20s %12s %14s" % ("Scenario", "Iterations", "ns/op"))
        print("-" * 60)
        for name, iters, ns, _ in rows:
            print("%-20s %12d %14.0f" % (name, iters, ns))
        print("-" * 60)


if __name__ == "__main__":
    main()
