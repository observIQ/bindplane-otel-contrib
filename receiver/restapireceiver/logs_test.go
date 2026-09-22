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

package restapireceiver

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.uber.org/zap"
)

func TestConvertJSONToLogs_SimpleArray(t *testing.T) {
	data := []map[string]any{
		{"id": "1", "message": "test message 1", "level": "info"},
		{"id": "2", "message": "test message 2", "level": "error"},
	}

	logger := zap.NewNop()
	logs := convertJSONToLogs(data, nil, &Config{}, logger)

	require.Equal(t, 1, logs.ResourceLogs().Len())
	require.Equal(t, 1, logs.ResourceLogs().At(0).ScopeLogs().Len())
	require.Equal(t, 2, logs.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().Len())

	// Check first log record
	record1 := logs.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0)
	body := record1.Body()
	require.True(t, body.Type() == pcommon.ValueTypeMap)
	bodyMap := body.Map()
	require.Equal(t, "1", bodyMap.AsRaw()["id"])
	require.Equal(t, "test message 1", bodyMap.AsRaw()["message"])
	require.Equal(t, "info", bodyMap.AsRaw()["level"])

	// Check second log record
	record2 := logs.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(1)
	body2 := record2.Body()
	require.True(t, body2.Type() == pcommon.ValueTypeMap)
	bodyMap2 := body2.Map()
	require.Equal(t, "2", bodyMap2.AsRaw()["id"])
	require.Equal(t, "test message 2", bodyMap2.AsRaw()["message"])
	require.Equal(t, "error", bodyMap2.AsRaw()["level"])
}

func TestConvertJSONToLogs_EmptyArray(t *testing.T) {
	data := []map[string]any{}
	logger := zap.NewNop()
	logs := convertJSONToLogs(data, nil, &Config{}, logger)

	require.Equal(t, 1, logs.ResourceLogs().Len())
	require.Equal(t, 1, logs.ResourceLogs().At(0).ScopeLogs().Len())
	require.Equal(t, 0, logs.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().Len())
}

func TestConvertJSONToLogs_WithTimestamp(t *testing.T) {
	now := time.Now()
	timestamp := now.Format(time.RFC3339)

	data := []map[string]any{
		{
			"id":        "1",
			"message":   "test",
			"timestamp": timestamp,
		},
	}

	logger := zap.NewNop()
	logs := convertJSONToLogs(data, nil, &Config{}, logger)

	record := logs.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0)

	// Timestamp should be set (either from JSON or observed time)
	require.Greater(t, record.Timestamp(), pcommon.Timestamp(0))
	require.Greater(t, record.ObservedTimestamp(), pcommon.Timestamp(0))
}

func TestConvertJSONToLogs_WithNestedFields(t *testing.T) {
	data := []map[string]any{
		{
			"id":      "1",
			"message": "test",
			"user": map[string]any{
				"name": "John",
				"id":   "123",
			},
			"metadata": map[string]any{
				"source": "api",
				"env":    "prod",
			},
		},
	}

	logger := zap.NewNop()
	logs := convertJSONToLogs(data, nil, &Config{}, logger)

	record := logs.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0)
	body := record.Body()
	require.True(t, body.Type() == pcommon.ValueTypeMap)
	bodyMap := body.Map()

	// Check nested fields are preserved
	rawBody := bodyMap.AsRaw()
	require.NotNil(t, rawBody["user"])
	require.NotNil(t, rawBody["metadata"])

	userMap := rawBody["user"].(map[string]any)
	require.Equal(t, "John", userMap["name"])
	require.Equal(t, "123", userMap["id"])
}

func TestConvertJSONToLogs_ObservedTimestamp(t *testing.T) {
	data := []map[string]any{
		{"id": "1", "message": "test"},
	}

	logger := zap.NewNop()
	logs := convertJSONToLogs(data, nil, &Config{}, logger)

	record := logs.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0)

	// Observed timestamp should always be set
	require.Greater(t, record.ObservedTimestamp(), pcommon.Timestamp(0))
}

func TestConvertJSONToLogs_MultipleRecords(t *testing.T) {
	data := []map[string]any{
		{"id": "1", "message": "first"},
		{"id": "2", "message": "second"},
		{"id": "3", "message": "third"},
		{"id": "4", "message": "fourth"},
		{"id": "5", "message": "fifth"},
	}

	logger := zap.NewNop()
	logs := convertJSONToLogs(data, nil, &Config{}, logger)

	require.Equal(t, 5, logs.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().Len())

	// Verify all records are present
	for i := 0; i < 5; i++ {
		record := logs.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(i)
		body := record.Body()
		bodyMap := body.Map()
		expectedID := string(rune('1' + i))
		require.Equal(t, expectedID, bodyMap.AsRaw()["id"])
	}
}

func TestConvertJSONToLogs_WithNumericValues(t *testing.T) {
	data := []map[string]any{
		{
			"id":     "1",
			"count":  42,
			"price":  99.99,
			"active": true,
			"tags":   []any{"tag1", "tag2"},
		},
	}

	logger := zap.NewNop()
	logs := convertJSONToLogs(data, nil, &Config{}, logger)

	record := logs.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0)
	body := record.Body()
	bodyMap := body.Map()
	rawBody := bodyMap.AsRaw()

	// Verify numeric and other types are preserved
	// Note: integers in JSON are converted to int64 in pdata
	require.Equal(t, int64(42), rawBody["count"])
	require.Equal(t, 99.99, rawBody["price"])
	require.Equal(t, true, rawBody["active"])
	require.NotNil(t, rawBody["tags"])
}

func TestConvertJSONToLogs_InvalidTimestamp(t *testing.T) {
	data := []map[string]any{
		{
			"id":        "1",
			"message":   "test",
			"timestamp": "invalid-timestamp",
		},
	}

	logger := zap.NewNop()
	logs := convertJSONToLogs(data, nil, &Config{}, logger)

	record := logs.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0)

	// Should still have timestamps (observed time as fallback)
	require.Greater(t, record.Timestamp(), pcommon.Timestamp(0))
	require.Greater(t, record.ObservedTimestamp(), pcommon.Timestamp(0))
}

// TestConvertJSONToLogs_BodyOptions covers every combination of the two options.
// They are orthogonal, so with both set the body and the attribute intentionally
// carry the same text.
func TestConvertJSONToLogs_BodyOptions(t *testing.T) {
	data := []map[string]any{
		{"id": "1", "message": "first"},
		{"id": "2", "message": "second"},
	}
	originals := [][]byte{
		[]byte(`{"id":"1","message":"first"}`),
		[]byte(`{"id":"2","message":"second"}`),
	}

	testCases := []struct {
		name         string
		cfg          *Config
		wantBodyStr  bool
		wantOriginal bool
	}{
		{
			name: "defaults leave the parsed body untouched",
			cfg:  &Config{},
		},
		{
			name:        "raw replaces the body with the original text",
			cfg:         &Config{Raw: true},
			wantBodyStr: true,
		},
		{
			name:         "include_log_record_original adds the attribute and keeps the map body",
			cfg:          &Config{IncludeLogRecordOriginal: true},
			wantOriginal: true,
		},
		{
			name:         "both options together",
			cfg:          &Config{Raw: true, IncludeLogRecordOriginal: true},
			wantBodyStr:  true,
			wantOriginal: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			logs := convertJSONToLogs(data, originals, tc.cfg, zap.NewNop())

			records := logs.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords()
			require.Equal(t, len(data), records.Len())

			for i := 0; i < records.Len(); i++ {
				record := records.At(i)
				want := string(originals[i])

				if tc.wantBodyStr {
					require.Equal(t, pcommon.ValueTypeStr, record.Body().Type())
					require.Equal(t, want, record.Body().Str())
				} else {
					require.Equal(t, pcommon.ValueTypeMap, record.Body().Type())
					require.Equal(t, data[i]["id"], record.Body().Map().AsRaw()["id"])
				}

				original, ok := record.Attributes().Get(logRecordOriginalAttribute)
				if tc.wantOriginal {
					require.True(t, ok)
					require.Equal(t, want, original.Str())
				} else {
					require.False(t, ok)
				}
			}
		})
	}
}

// TestConvertJSONToLogs_BodyOptionsWithoutOriginals covers the degraded path:
// options on, but no original text recovered, so records fall back to the parsed
// body rather than a re-encoded approximation.
func TestConvertJSONToLogs_BodyOptionsWithoutOriginals(t *testing.T) {
	data := []map[string]any{{"id": "1"}}
	cfg := &Config{Raw: true, IncludeLogRecordOriginal: true}

	logs := convertJSONToLogs(data, nil, cfg, zap.NewNop())

	record := logs.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0)
	require.Equal(t, pcommon.ValueTypeMap, record.Body().Type())
	require.Equal(t, "1", record.Body().Map().AsRaw()["id"])
	_, ok := record.Attributes().Get(logRecordOriginalAttribute)
	require.False(t, ok)
}

// TestConvertJSONToLogs_RawPreservesTimestamps pins that raw mode still reads the
// parsed record for the timestamp, so enabling it does not shift event times.
func TestConvertJSONToLogs_RawPreservesTimestamps(t *testing.T) {
	data := []map[string]any{{"id": "1", "timestamp": "2024-03-18T15:25:43Z"}}
	originals := [][]byte{[]byte(`{"id":"1","timestamp":"2024-03-18T15:25:43Z"}`)}

	logs := convertJSONToLogs(data, originals, &Config{Raw: true}, zap.NewNop())

	record := logs.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0)
	want, err := time.Parse(time.RFC3339, "2024-03-18T15:25:43Z")
	require.NoError(t, err)
	require.Equal(t, pcommon.NewTimestampFromTime(want), record.Timestamp())
}
