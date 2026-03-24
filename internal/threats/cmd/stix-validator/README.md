# stix-validator

Command-line tool for validating STIX 2.1 JSON. Compatible with the Python [stix2-validator](https://github.com/oasis-open/cti-stix-validator) flags and exit codes.

## Usage

```bash
# Validate files
go run . file.json
go run . dir/ other.json

# Validate from stdin
cat bundle.json | go run .

# Install and run
go build -o stix-validator .
./stix-validator [options] [FILES...]
```

With no FILES, input is read from stdin.

## Options

| Flag | Description |
|------|-------------|
| `-r`, `--recursive` | Recursively descend into directories (default: true) |
| `-s`, `--schemas` | Path to custom JSON Schema directory |
| `--version` | STIX version, e.g. `2.1` (default: `2.1`) |
| `-v`, `--verbose` | Verbose output |
| `-q`, `--silent` | Suppress all stdout output |
| `-d`, `--disable` | Comma-separated check codes or names to disable (e.g. `202,210`) |
| `-e`, `--enable` | Comma-separated check codes or names to enable |
| `--strict` | Treat warnings as errors |
| `--strict-types` | Warn on custom object types |
| `--strict-properties` | Warn on custom properties |
| `--enforce-refs` | Ensure SDOs referenced by SROs are in the same bundle |
| `--interop` | Interop validation settings |
| `-j`, `--json` | Output results as JSON |
| `-h`, `--help` | Show help |

Output can be either silent (`-q`) or verbose (`-v`), not both.

## Exit codes

| Code | Meaning |
|------|---------|
| 0 | Success (all input valid) |
| 1 | Failure (e.g. invalid flags, internal error) |
| 2 | Schema invalid (at least one object failed schema or MUST checks) |
| 16 (0x10) | Validation error (fatal: invalid JSON, missing file, etc.) |

Codes can be combined (e.g. 18 = 0x12 = schema invalid and validation error).

## See also

- [VALIDATOR_PLAN.md](../../docs/VALIDATOR_PLAN.md) – Full validator implementation plan and spec.
