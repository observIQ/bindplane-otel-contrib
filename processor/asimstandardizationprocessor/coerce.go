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

package asimstandardizationprocessor

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// datetimeLayouts is the set of timestamp formats accepted when coercing a
// string value to a `datetime` column. Listed in order of frequency.
var datetimeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05.000Z",
	"2006-01-02T15:04:05Z",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
}

// coerceValue converts a mapped value to the form Azure Log Analytics expects
// for the column's declared type. Returns the coerced value plus a boolean
// indicating whether coercion succeeded. Callers drop the field (and, when
// runtime_validation is enabled, the record) on a failed coercion.
func coerceValue(value any, want ColType) (any, bool) {
	if value == nil {
		return nil, false
	}
	switch want {
	case ColString:
		return coerceString(value)
	case ColDateTime:
		return coerceDateTime(value)
	case ColInt:
		return coerceInt(value)
	case ColLong:
		return coerceLong(value)
	case ColReal:
		return coerceReal(value)
	case ColBoolean:
		return coerceBool(value)
	case ColDynamic:
		// Dynamic accepts any JSON-serialisable value as-is.
		return value, true
	default:
		return value, true
	}
}

func coerceString(v any) (any, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case fmt.Stringer:
		return x.String(), true
	case bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return fmt.Sprint(x), true
	case []any, map[string]any:
		// Composite values are JSON-marshalled rather than fmt.Sprint'd so the
		// emitted string is something Sentinel queries can usefully extract
		// fields from (instead of Go's `[1 2 3]` / `map[a:1]` debug syntax).
		b, err := json.Marshal(x)
		if err != nil {
			return nil, false
		}
		return string(b), true
	default:
		// Anything we don't explicitly recognise is rejected so the warn log
		// + field-drop catch the misconfiguration. Avoid the Go-default
		// formatter for arbitrary types that won't round-trip through KQL.
		return nil, false
	}
}

func coerceDateTime(v any) (any, bool) {
	switch x := v.(type) {
	case time.Time:
		return x.UTC().Format(time.RFC3339Nano), true
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return nil, false
		}
		for _, layout := range datetimeLayouts {
			if t, err := time.Parse(layout, s); err == nil {
				return t.UTC().Format(time.RFC3339Nano), true
			}
		}
		return nil, false
	default:
		// Numeric epoch values (seconds, ms, µs, ns) are deliberately not
		// supported here because the unit can't be inferred from magnitude
		// alone and silently picking the wrong scale produces timestamps that
		// are off by 1000x or 1Mx. Mappings should convert to a string
		// timestamp with an explicit format via expr-lang before reaching
		// this path.
		return nil, false
	}
}

func coerceInt(v any) (any, bool) {
	n, ok := toInt64(v)
	if !ok {
		return nil, false
	}
	// Microsoft KQL `int` is 32-bit signed
	// (https://learn.microsoft.com/en-us/kusto/query/scalar-data-types/int).
	// Values that don't fit are rejected so the warn log + field-drop catch
	// the overflow rather than letting Azure silently truncate at ingest.
	if n < math.MinInt32 || n > math.MaxInt32 {
		return nil, false
	}
	return n, true
}

func coerceLong(v any) (any, bool) {
	n, ok := toInt64(v)
	if !ok {
		return nil, false
	}
	return n, true
}

func coerceReal(v any) (any, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int32:
		return float64(x), true
	case int64:
		return float64(x), true
	case uint, uint32, uint64:
		n, ok := toInt64(x)
		if !ok {
			return nil, false
		}
		return float64(n), true
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return nil, false
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, false
		}
		return f, true
	default:
		return nil, false
	}
}

func coerceBool(v any) (any, bool) {
	switch x := v.(type) {
	case bool:
		return x, true
	case string:
		s := strings.ToLower(strings.TrimSpace(x))
		switch s {
		case "true", "1", "yes":
			return true, true
		case "false", "0", "no":
			return false, true
		}
		return nil, false
	case int:
		return x != 0, true
	case int32:
		return x != 0, true
	case int64:
		return x != 0, true
	default:
		return nil, false
	}
}

func toInt64(v any) (int64, bool) {
	switch x := v.(type) {
	case int:
		return int64(x), true
	case int32:
		return int64(x), true
	case int64:
		return x, true
	case uint:
		return int64(x), true
	case uint32:
		return int64(x), true
	case uint64:
		return int64(x), true
	case float32:
		return int64(x), true
	case float64:
		return int64(x), true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return 0, false
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err == nil {
			return n, true
		}
		// Allow hex with 0x prefix.
		if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
			n, err := strconv.ParseInt(s[2:], 16, 64)
			if err == nil {
				return n, true
			}
		}
		// Tolerate trailing decimals on otherwise-integer strings.
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return int64(f), true
		}
		return 0, false
	default:
		return 0, false
	}
}
