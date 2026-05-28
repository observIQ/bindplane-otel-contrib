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
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/otel/attribute"
)

func TestPdataValueToOtel(t *testing.T) {
	tests := []struct {
		name string
		set  func(pcommon.Value)
		want attribute.KeyValue
	}{
		{
			name: "string",
			set:  func(v pcommon.Value) { v.SetStr("hello") },
			want: attribute.String("k", "hello"),
		},
		{
			name: "bool",
			set:  func(v pcommon.Value) { v.SetBool(true) },
			want: attribute.Bool("k", true),
		},
		{
			name: "int",
			set:  func(v pcommon.Value) { v.SetInt(42) },
			want: attribute.Int64("k", 42),
		},
		{
			name: "double",
			set:  func(v pcommon.Value) { v.SetDouble(1.5) },
			want: attribute.Float64("k", 1.5),
		},
		{
			name: "bytes",
			set:  func(v pcommon.Value) { v.SetEmptyBytes().FromRaw([]byte{0xde, 0xad}) },
			want: attribute.String("k", "dead"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := pcommon.NewValueEmpty()
			tt.set(v)
			got := pdataValueToOtel("k", v)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestPdataValueToOtel_SliceAndMap(t *testing.T) {
	// Slice and Map fall through to AsString; we just assert the key is right
	// and the value is a non-empty string.
	v := pcommon.NewValueEmpty()
	v.SetEmptySlice().AppendEmpty().SetStr("a")
	got := pdataValueToOtel("k", v)
	require.Equal(t, attribute.STRING, got.Value.Type())
	require.NotEmpty(t, got.Value.AsString())

	mv := pcommon.NewValueEmpty()
	mv.SetEmptyMap().PutStr("nested", "x")
	got = pdataValueToOtel("k", mv)
	require.Equal(t, attribute.STRING, got.Value.Type())
	require.NotEmpty(t, got.Value.AsString())
}

func TestResourceAttrsForConfig(t *testing.T) {
	res := pcommon.NewResource()
	res.Attributes().PutStr("service.name", "svc")
	res.Attributes().PutStr("env", "prod")

	t.Run("disabled-by-default", func(t *testing.T) {
		got := resourceAttrsForConfig(res, &Config{})
		require.Nil(t, got)
	})

	t.Run("include-all", func(t *testing.T) {
		got := resourceAttrsForConfig(res, &Config{IncludeResourceAttributes: true})
		require.Len(t, got, 2)
	})

	t.Run("allowlist-filters", func(t *testing.T) {
		got := resourceAttrsForConfig(res, &Config{
			IncludeResourceAttributes: true,
			ResourceAttributeKeys:     []string{"service.name"},
		})
		require.Len(t, got, 1)
		require.Equal(t, "service.name", string(got[0].Key))
	})

	t.Run("empty-resource", func(t *testing.T) {
		got := resourceAttrsForConfig(pcommon.NewResource(), &Config{IncludeResourceAttributes: true})
		require.Nil(t, got)
	})
}

func TestDpAttrsToOtel(t *testing.T) {
	dpAttrs := pcommon.NewMap()
	dpAttrs.PutStr("k1", "v1")
	dpAttrs.PutInt("k2", 7)

	resource := []attribute.KeyValue{attribute.String("service.name", "svc")}

	got := dpAttrsToOtel(dpAttrs, resource)
	require.Len(t, got, 3)
	require.Equal(t, "service.name", string(got[0].Key))
	keys := []string{string(got[1].Key), string(got[2].Key)}
	require.ElementsMatch(t, []string{"k1", "k2"}, keys)
}
