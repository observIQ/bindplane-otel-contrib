// Copyright observIQ, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package blitzpdata // import "github.com/observiq/bindplane-otel-contrib/receiver/telemetrygeneratorreceiver/internal/blitzpdata"

import (
	"fmt"
	"sort"
	"strings"
)

// Z3Config holds a Z3-shaped attribute map after parsing:
//
//   - Base is every key's effective value — the scalar from the simple
//     form `key: value`, or the `value` field of the structured form
//     `key: { value: ..., lock: true|false }`. The lock state is not
//     reflected in Base; both locked and unlocked keys contribute
//     their value as the adapter-side base.
//   - Locked is the set of keys with `lock: true`. Per-record blitz
//     metadata values for these keys are dropped during merge — the
//     receiver-config Base value stays put.
//
// A zero-value Z3Config (nil maps) is treated as "no base, no locks":
// blitz's per-record metadata flows through unchanged to the outgoing
// pdata.
type Z3Config struct {
	Base   map[string]any
	Locked map[string]struct{}
}

// IsEmpty reports whether the config has neither base entries nor
// locked keys. Used by adapter merge paths to short-circuit work for
// the common "no receiver-config base, no locking" case.
func (z Z3Config) IsEmpty() bool {
	return len(z.Base) == 0 && len(z.Locked) == 0
}

// ParseZ3 parses a YAML/config map into a Z3Config. Each entry's value
// may be either:
//
//   - A scalar (string, number, bool, slice, or any value that is NOT
//     a Go map[string]any with a `value` sub-key) — simple form, the
//     scalar is the attribute value and the key is unlocked.
//   - A structured form: a map containing exactly `value` (required)
//     and optionally `lock` (bool, default false). `lock: true` adds
//     the key to the locked set.
//
// Validation errors are returned with the offending key's full config
// path (e.g. "resource_attributes.deployment.environment") so the user
// can locate the issue in their YAML without scanning the whole config.
//
// A nil or empty input map returns a zero-value Z3Config without
// allocation.
func ParseZ3(raw map[string]any, fieldPath string) (Z3Config, error) {
	if len(raw) == 0 {
		return Z3Config{}, nil
	}
	cfg := Z3Config{
		Base:   make(map[string]any, len(raw)),
		Locked: make(map[string]struct{}),
	}
	// Iterate in sorted key order so validation errors are
	// deterministic across runs — the user gets the same first-failing
	// key on every parse attempt.
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		keyPath := fieldPath + "." + key
		val := raw[key]
		sub, isMap := val.(map[string]any)
		if !isMap {
			cfg.Base[key] = val
			continue
		}
		// Map at value position: must be Z3 structured form with a
		// `value` sub-key. Without `value` the input is ambiguous
		// (typo in a structured form, or a multi-value attribute the
		// user meant as the value itself); reject explicitly.
		valueField, hasValue := sub["value"]
		if !hasValue {
			return Z3Config{}, fmt.Errorf(
				"%s: map at value position has no `value` sub-key; "+
					"for a multi-value attribute use the simple form `%s: <map>`, "+
					"for Z3 per-key locking use the structured form `%s: { value: <map>, lock: ... }`",
				keyPath, key, key)
		}
		for sk := range sub {
			if sk != "value" && sk != "lock" {
				return Z3Config{}, fmt.Errorf(
					"%s: unknown sub-key %q in structured form; only `value` and `lock` are allowed",
					keyPath, sk)
			}
		}
		var locked bool
		if lockRaw, hasLock := sub["lock"]; hasLock {
			lockBool, ok := lockRaw.(bool)
			if !ok {
				return Z3Config{}, fmt.Errorf(
					"%s.lock: must be a boolean (true or false), got %T",
					keyPath, lockRaw)
			}
			locked = lockBool
		}
		cfg.Base[key] = valueField
		if locked {
			cfg.Locked[key] = struct{}{}
		}
	}
	return cfg, nil
}

// MergeWithStringOverlay returns a fresh map[string]any combining the
// Z3 base map with a string-typed per-record overlay (used for
// resource attributes, which are typed `map[string]string` across all
// three signal types in blitz's record contract). Locked keys in the
// base are NOT overridden by the overlay; unlocked keys are.
//
// For locks-only / overlay-only / both-empty cases, the result is
// allocated to the combined max size to avoid a re-grow during merge.
// Returning a fresh map (vs in-place mutation of base.Base) is
// deliberate — the LogAdapter is constructed once per receiver entry
// and may be invoked concurrently from blitz module goroutines.
func (z Z3Config) MergeWithStringOverlay(overlay map[string]string) map[string]any {
	merged := make(map[string]any, len(z.Base)+len(overlay))
	for k, v := range z.Base {
		merged[k] = v
	}
	for k, v := range overlay {
		if _, locked := z.Locked[k]; locked {
			continue
		}
		merged[k] = v
	}
	return merged
}

// MergeWithAnyOverlay is the `map[string]any`-overlay sibling of
// MergeWithStringOverlay, used for per-record attribute overlays on
// logs (`LogRecord.Metadata.Attributes`) and spans
// (`Span.Metadata.Attributes`) where blitz emits values as `any`.
// Semantics are otherwise identical: locked keys stay receiver-config;
// unlocked keys take the overlay value.
func (z Z3Config) MergeWithAnyOverlay(overlay map[string]any) map[string]any {
	merged := make(map[string]any, len(z.Base)+len(overlay))
	for k, v := range z.Base {
		merged[k] = v
	}
	for k, v := range overlay {
		if _, locked := z.Locked[k]; locked {
			continue
		}
		merged[k] = v
	}
	return merged
}

// FingerprintMap returns a stable string representation of the
// argument map suitable for grouping records by their effective
// resource. Keys are sorted; values are stringified with `%v`.
//
// Used by adapters to bucket records in a single batch into one
// `ResourceLogs` / `ResourceMetrics` / `ResourceSpans` per unique
// merged-resource map (Q1 resource grouping). Two records whose
// merged resources serialize to the same fingerprint share a
// `ResourceX`; records whose fingerprints differ land in separate
// resource sets.
//
// The unit-separator byte (0x1f) between entries prevents `key=val`
// vs `keyval=` ambiguity (e.g. `{a: "b=c"}` vs `{a: "b", c: ""}`).
func FingerprintMap(m map[string]any) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString(k)
		sb.WriteByte('=')
		fmt.Fprintf(&sb, "%v", m[k])
		sb.WriteByte('\x1f')
	}
	return sb.String()
}
