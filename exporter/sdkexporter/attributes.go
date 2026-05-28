// Copyright observIQ, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sdkexporter

import (
	"encoding/hex"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/otel/attribute"
)

// resourceAttrsForConfig returns the resource attribute KeyValues to fold into
// every data point recorded under the given Resource, honoring the config.
// Returns nil when folding is disabled.
func resourceAttrsForConfig(res pcommon.Resource, cfg *Config) []attribute.KeyValue {
	if !cfg.IncludeResourceAttributes {
		return nil
	}
	src := res.Attributes()
	if src.Len() == 0 {
		return nil
	}
	if len(cfg.ResourceAttributeKeys) == 0 {
		out := make([]attribute.KeyValue, 0, src.Len())
		for k, v := range src.All() {
			out = append(out, pdataValueToOtel(k, v))
		}
		return out
	}
	allow := make(map[string]struct{}, len(cfg.ResourceAttributeKeys))
	for _, k := range cfg.ResourceAttributeKeys {
		allow[k] = struct{}{}
	}
	out := make([]attribute.KeyValue, 0, len(allow))
	for k, v := range src.All() {
		if _, ok := allow[k]; !ok {
			continue
		}
		out = append(out, pdataValueToOtel(k, v))
	}
	return out
}

// dpAttrsToOtel converts a pdata data-point attribute map combined with any
// preconverted resource attributes into a single OTel attribute slice.
// Resource attributes come first; data-point attributes overwrite on key
// collision (data-point intent wins, matching OTLP semantics).
func dpAttrsToOtel(dpAttrs pcommon.Map, resourceAttrs []attribute.KeyValue) []attribute.KeyValue {
	out := make([]attribute.KeyValue, 0, len(resourceAttrs)+dpAttrs.Len())
	out = append(out, resourceAttrs...)
	for k, v := range dpAttrs.All() {
		out = append(out, pdataValueToOtel(k, v))
	}
	return out
}

// pdataValueToOtel converts a single pdata Value to an OTel attribute.KeyValue.
// Slice and Map fall back to AsString (JSON-ish) since OTel attributes only
// support homogeneous slices and no nested maps. Bytes are hex-encoded.
func pdataValueToOtel(key string, v pcommon.Value) attribute.KeyValue {
	switch v.Type() {
	case pcommon.ValueTypeStr:
		return attribute.String(key, v.Str())
	case pcommon.ValueTypeBool:
		return attribute.Bool(key, v.Bool())
	case pcommon.ValueTypeInt:
		return attribute.Int64(key, v.Int())
	case pcommon.ValueTypeDouble:
		return attribute.Float64(key, v.Double())
	case pcommon.ValueTypeBytes:
		return attribute.String(key, hex.EncodeToString(v.Bytes().AsRaw()))
	case pcommon.ValueTypeSlice, pcommon.ValueTypeMap, pcommon.ValueTypeEmpty:
		return attribute.String(key, v.AsString())
	default:
		return attribute.String(key, v.AsString())
	}
}
