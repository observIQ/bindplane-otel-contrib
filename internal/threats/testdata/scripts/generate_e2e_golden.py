#!/usr/bin/env python3
"""
Generate e2e_golden.json from the Python cti-stix-validator for E2E fixture comparison.
Runs the Python validator on each JSON under test_examples and test_schemas (v21)
with default 2.1 options and writes { "relpath": { "valid": bool, "exit_code": int } }.

Run from repo root: python3 scripts/generate_e2e_golden.py

Optional: --validator-dir DIR (default: pkg/cti-stix-validator)
          --out PATH (default: internal/stixvalidator/e2e/testdata/e2e_golden.json)
"""

from __future__ import print_function

import argparse
import json
import os
import sys

_SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
_REPO_ROOT = os.path.dirname(_SCRIPT_DIR)

# Add validator to path before importing (support both root and pkg layout)
for _validator_dir in ("cti-stix-validator", "pkg/cti-stix-validator"):
    path = os.path.join(_REPO_ROOT, _validator_dir)
    if os.path.isdir(path) and path not in sys.path:
        sys.path.insert(0, path)
        break

try:
    from stix2validator import ValidationOptions, validate_file, codes  # type: ignore[import-untyped]
except ImportError as e:
    print("Failed to import stix2validator:", e, file=sys.stderr)
    print("Run from repo root and ensure pkg/cti-stix-validator exists.", file=sys.stderr)
    print("Try: pip install -e pkg/cti-stix-validator", file=sys.stderr)
    sys.exit(1)


FIXTURE_DIRS = ("test_examples", "test_schemas")

# Schema dir candidates (same order as bench script): use repo schemas so Python
# matches Go's built-in codegen from cti-stix2-json-schemas.
SCHEMAS_CANDIDATES = [
    os.path.join(_REPO_ROOT, "pkg", "cti-stix2-json-schemas", "schemas"),
    os.path.join(_REPO_ROOT, "cti-stix2-json-schemas", "schemas"),
]


def _schema_dir():
    """Return repo schema directory so Python uses same schemas as Go codegen."""
    for d in SCHEMAS_CANDIDATES:
        if os.path.isdir(d):
            return d
    return None


def collect_fixtures(v21_base):
    """Return list of (relpath, abspath) for each .json under v21_base/test_examples and test_schemas."""
    out = []
    for subdir in FIXTURE_DIRS:
        d = os.path.join(v21_base, subdir)
        if not os.path.isdir(d):
            continue
        for name in sorted(os.listdir(d)):
            if not name.endswith(".json"):
                continue
            rel = os.path.join(subdir, name).replace("\\", "/")
            out.append((rel, os.path.join(d, name)))
    return out


def main():
    parser = argparse.ArgumentParser(
        description="Generate E2E golden file from Python cti-stix-validator."
    )
    parser.add_argument(
        "--validator-dir",
        default="pkg/cti-stix-validator",
        help="Validator package directory relative to repo root",
    )
    parser.add_argument(
        "--out",
        default=os.path.join(
            _REPO_ROOT, "internal", "stixvalidator", "e2e", "testdata", "e2e_golden.json"
        ),
        help="Output JSON path",
    )
    args = parser.parse_args()

    # Re-add chosen validator dir in case default was different
    path = os.path.join(_REPO_ROOT, args.validator_dir)
    if path not in sys.path:
        sys.path.insert(0, path)

    v21_base = os.path.join(
        _REPO_ROOT, args.validator_dir, "stix2validator", "test", "v21"
    )
    if not os.path.isdir(v21_base):
        print("v21 test dir not found:", v21_base, file=sys.stderr)
        sys.exit(1)

    schema_dir = _schema_dir()
    if not schema_dir:
        print(
            "Error: repo schema dir not found. Go validator uses schemas from cti-stix2-json-schemas.",
            file=sys.stderr,
        )
        print(
            "Looked for: pkg/cti-stix2-json-schemas/schemas, cti-stix2-json-schemas/schemas",
            file=sys.stderr,
        )
        print("Pull the subtree or ensure one of these paths exists, then re-run.", file=sys.stderr)
        sys.exit(1)
    print("Using schema_dir:", schema_dir, file=sys.stderr)
    options = ValidationOptions(version="2.1", silent=True, schema_dir=schema_dir)
    golden = {}

    for rel, abspath in collect_fixtures(v21_base):
        try:
            result = validate_file(abspath, options)
            valid = result.is_valid
            exit_code = codes.get_code([result])
        except Exception as e:
            print("Warning:", rel, "->", e, file=sys.stderr)
            valid = False
            exit_code = codes.EXIT_VALIDATION_ERROR
        golden[rel] = {"valid": valid, "exit_code": exit_code}

    out_dir = os.path.dirname(args.out)
    if out_dir and not os.path.isdir(out_dir):
        os.makedirs(out_dir, exist_ok=True)

    with open(args.out, "w") as f:
        json.dump(golden, f, indent=2)

    print("Wrote", len(golden), "entries to", args.out)


if __name__ == "__main__":
    main()
