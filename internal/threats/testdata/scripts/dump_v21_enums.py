#!/usr/bin/env python3
"""
Dump STIX 2.1 validator enums from cti-stix-validator to JSON for Go codegen.
Run from repo root: python scripts/dump_v21_enums.py
Loads enums.py directly to avoid validator dependencies (jsonschema, etc.).
Output: internal/stixvalidator/enums_v21.json (or -o path).
"""
from __future__ import annotations

import importlib.util
import json
import os
import sys

# Repo root: parent of directory containing this script.
SCRIPT_DIR = os.path.abspath(os.path.dirname(__file__))
REPO_ROOT = os.path.dirname(SCRIPT_DIR)
ENUMS_PY = os.path.join(REPO_ROOT,"pkg", "cti-stix-validator", "stix2validator", "v21", "enums.py")


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


def load_enums():
    """Load enums module from file without importing the rest of stix2validator."""
    spec = importlib.util.spec_from_file_location("enums", ENUMS_PY)
    if spec is None or spec.loader is None:
        raise ImportError(f"could not load {ENUMS_PY}")
    mod = importlib.util.module_from_spec(spec)
    # Add v21 assets path for CSV loading if enums ever call media_types() etc.; we skip those.
    sys.modules["enums"] = mod
    spec.loader.exec_module(mod)
    return mod


def main():
    out_path = os.path.join(REPO_ROOT, "internal", "stixvalidator", "enums_v21.json")
    if "-o" in sys.argv:
        i = sys.argv.index("-o")
        if i + 1 < len(sys.argv):
            out_path = sys.argv[i + 1]
            if not os.path.isabs(out_path):
                out_path = os.path.join(REPO_ROOT, out_path)

    if not os.path.isfile(ENUMS_PY):
        print(f"Enums file not found: {ENUMS_PY}", file=sys.stderr)
        sys.exit(1)

    try:
        enums = load_enums()
    except Exception as e:
        print("Load error:", e, file=sys.stderr)
        sys.exit(1)

    # Skip functions (media_types, char_sets, protocols, ipfix) and private names
    SKIP = {"media_types", "char_sets", "protocols", "ipfix"}
    data = {}
    for name in dir(enums):
        if not name.isupper() or name.startswith("_") or name in SKIP:
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

    os.makedirs(os.path.dirname(out_path), exist_ok=True)
    with open(out_path, "w", encoding="utf-8") as f:
        json.dump(data, f, indent=2, sort_keys=True)

    print(f"Wrote {out_path}")


if __name__ == "__main__":
    main()
