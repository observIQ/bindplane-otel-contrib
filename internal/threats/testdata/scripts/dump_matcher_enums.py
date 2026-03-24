#!/usr/bin/env python3
"""
Dump STIX pattern matcher enums from cti-pattern-matcher to JSON for Go codegen.
Run from repo root: python scripts/dump_matcher_enums.py
Loads enums.py directly to avoid matcher dependencies (antlr4, stix2patterns, etc.).
Output: stix/matcher/enums_matcher.json (or -o path).
"""
from __future__ import annotations

import importlib.util
import json
import os
import sys

# Repo root: parent of directory containing this script.
SCRIPT_DIR = os.path.abspath(os.path.dirname(__file__))
REPO_ROOT = os.path.dirname(SCRIPT_DIR)

# Try standard subtree path first, then pkg/ for alternate layout.
MATCHER_ENUMS_CANDIDATES = [
    os.path.join(REPO_ROOT, "cti-pattern-matcher", "stix2matcher", "enums.py"),
    os.path.join(REPO_ROOT, "pkg", "cti-pattern-matcher", "stix2matcher", "enums.py"),
]


def to_serializable(obj):
    """Convert Python enums to JSON-serializable form."""
    if obj is None:
        return None
    if isinstance(obj, (str, int, float, bool)):
        return obj
    if isinstance(obj, tuple):
        return [to_serializable(x) for x in obj]
    if isinstance(obj, list):
        return [to_serializable(x) for x in obj]
    if isinstance(obj, set):
        return sorted(to_serializable(x) for x in obj)
    if isinstance(obj, dict):
        return {str(k): to_serializable(v) for k, v in obj.items()}
    raise TypeError(f"cannot serialize {type(obj).__name__}: {obj!r}")


def find_enums_py():
    for path in MATCHER_ENUMS_CANDIDATES:
        if os.path.isfile(path):
            return path
    return None


def load_enums(enums_py_path):
    """Load enums module from file without importing the rest of stix2matcher."""
    spec = importlib.util.spec_from_file_location("stix2matcher_enums", enums_py_path)
    if spec is None or spec.loader is None:
        raise ImportError(f"could not load {enums_py_path}")
    mod = importlib.util.module_from_spec(spec)
    sys.modules["stix2matcher_enums"] = mod
    spec.loader.exec_module(mod)
    return mod


def main():
    default_out = os.path.join(REPO_ROOT, "stix", "matcher", "enums_matcher.json")
    if "-o" in sys.argv:
        i = sys.argv.index("-o")
        if i + 1 < len(sys.argv):
            default_out = sys.argv[i + 1]
            if not os.path.isabs(default_out):
                default_out = os.path.join(REPO_ROOT, default_out)

    enums_py = find_enums_py()
    if enums_py is None:
        print("Enums file not found. Tried:", file=sys.stderr)
        for p in MATCHER_ENUMS_CANDIDATES:
            print(f"  {p}", file=sys.stderr)
        sys.exit(1)

    try:
        enums = load_enums(enums_py)
    except Exception as e:
        print(f"Load error: {e}", file=sys.stderr)
        sys.exit(1)

    data = {}
    for name in dir(enums):
        if not name.isupper() or name.startswith("_"):
            continue
        try:
            val = getattr(enums, name)
        except AttributeError:
            continue
        if callable(val):
            continue
        try:
            data[name] = to_serializable(val)
        except TypeError as err:
            print(f"Skip {name}: {err}", file=sys.stderr)
            continue

    os.makedirs(os.path.dirname(default_out), exist_ok=True)
    with open(default_out, "w", encoding="utf-8") as f:
        json.dump(data, f, indent=2, sort_keys=True)

    print(f"Wrote {default_out}")


if __name__ == "__main__":
    main()
