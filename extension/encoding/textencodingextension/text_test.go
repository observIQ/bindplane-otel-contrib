// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package textencodingextension

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/extension/extensiontest"

	"github.com/observiq/bindplane-otel-contrib/extension/encoding/textencodingextension/internal/textutils"
	"github.com/open-telemetry/opentelemetry-collector-contrib/extension/encoding"
)

func repeatedLines(n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "line-%d\n", i)
	}
	return b.String()
}

func TestTextRoundtrip(t *testing.T) {
	enc, err := textutils.LookupEncoding("utf8")
	require.NoError(t, err)
	r := regexp.MustCompile(`\r?\n`)
	codec := &textLogCodec{decoder: enc.NewDecoder(), unmarshalingSeparator: r, marshalingSeparator: "\n"}
	require.NoError(t, err)
	ld, err := codec.UnmarshalLogs([]byte("foo\r\nbar\n"))
	require.NoError(t, err)
	assert.Equal(t, 2, ld.LogRecordCount())
	b, err := codec.MarshalLogs(ld)
	require.NoError(t, err)
	require.Equal(t, "foo\nbar", string(b))
}

func TestTextRoundtripMissingNewline(t *testing.T) {
	enc, err := textutils.LookupEncoding("utf8")
	require.NoError(t, err)
	r := regexp.MustCompile(`\r?\n`)
	codec := &textLogCodec{decoder: enc.NewDecoder(), unmarshalingSeparator: r, marshalingSeparator: "\n"}
	require.NoError(t, err)
	ld, err := codec.UnmarshalLogs([]byte("foo\r\nbar"))
	require.NoError(t, err)
	assert.Equal(t, 2, ld.LogRecordCount())
	b, err := codec.MarshalLogs(ld)
	require.NoError(t, err)
	require.Equal(t, "foo\nbar", string(b))
}

func TestNoSeparator(t *testing.T) {
	enc, err := textutils.LookupEncoding("utf8")
	require.NoError(t, err)
	codec := &textLogCodec{decoder: enc.NewDecoder()}
	require.NoError(t, err)
	ld, err := codec.UnmarshalLogs([]byte("foo\r\nbar\n"))
	require.NoError(t, err)
	assert.Equal(t, 1, ld.LogRecordCount())
	b, err := codec.MarshalLogs(ld)
	require.NoError(t, err)
	require.Equal(t, "foo\r\nbar\n", string(b))
}

func TestNoSeparatorLargeMessage(t *testing.T) {
	// Test that large messages (> bufio.Scanner's default 4096 buffer) are not split
	// when no separator is configured. This verifies the fix for the bug where the
	// split function would return immediately without waiting for EOF.
	enc, err := textutils.LookupEncoding("utf8")
	require.NoError(t, err)
	codec := &textLogCodec{decoder: enc.NewDecoder()}

	// Create a message larger than bufio.Scanner's default 4096 byte buffer
	largeMessage := make([]byte, 10*1024*1024)
	for i := range largeMessage {
		largeMessage[i] = byte('a' + (i % 26))
	}

	ld, err := codec.UnmarshalLogs(largeMessage)
	require.NoError(t, err)
	assert.Equal(t, 1, ld.LogRecordCount(), "large message should not be split into multiple records")

	b, err := codec.MarshalLogs(ld)
	require.NoError(t, err)
	require.Equal(t, largeMessage, b)
}

func TestStreamDecoding_singleFlush(t *testing.T) {
	enc, err := textutils.LookupEncoding("utf8")
	require.NoError(t, err)
	r := regexp.MustCompile(`\r?\n`)
	codec := &textLogCodec{decoder: enc.NewDecoder(), unmarshalingSeparator: r, marshalingSeparator: "\n"}

	reader := bytes.NewReader([]byte("foo\nbar\nbaz\n"))

	// Decode with WithFlushItems=1
	decoder, err := codec.NewLogsDecoder(reader, encoding.WithFlushItems(1))
	require.NoError(t, err)

	// First call should return "foo"
	ld, err := decoder.DecodeLogs()
	require.NoError(t, err)
	assert.Equal(t, 1, ld.LogRecordCount())
	assert.Equal(t, "foo", ld.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0).Body().AsString())
	assert.Equal(t, int64(4), decoder.Offset())

	// Second call should return "bar"
	ld, err = decoder.DecodeLogs()
	require.NoError(t, err)
	assert.Equal(t, 1, ld.LogRecordCount())
	assert.Equal(t, "bar", ld.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0).Body().AsString())
	assert.Equal(t, int64(8), decoder.Offset())

	// Third call should return "baz"
	ld, err = decoder.DecodeLogs()
	require.NoError(t, err)
	assert.Equal(t, 1, ld.LogRecordCount())
	assert.Equal(t, "baz", ld.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0).Body().AsString())
	assert.Equal(t, int64(12), decoder.Offset())

	// Fourth call should return EOF with empty logs
	ld, err = decoder.DecodeLogs()
	assert.ErrorIs(t, err, io.EOF)
	assert.Equal(t, 0, ld.LogRecordCount())
}

func TestStreamDecoding_offsetWithFlush(t *testing.T) {
	enc, err := textutils.LookupEncoding("utf8")
	require.NoError(t, err)
	r := regexp.MustCompile(`\r?\n`)
	codec := &textLogCodec{decoder: enc.NewDecoder(), unmarshalingSeparator: r, marshalingSeparator: "\n"}

	reader := bytes.NewReader([]byte("foo\nbar\nbaz\n"))

	// Decode with offset=1 to skip "foo", flush after each item
	decoder, err := codec.NewLogsDecoder(reader, encoding.WithOffset(4), encoding.WithFlushItems(1))
	require.NoError(t, err)

	// First call should return "bar" (skipped "foo")
	ld, err := decoder.DecodeLogs()
	require.NoError(t, err)
	assert.Equal(t, 1, ld.LogRecordCount())
	assert.Equal(t, "bar", ld.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0).Body().AsString())
	assert.Equal(t, int64(8), decoder.Offset())

	// Second call should return "baz"
	ld, err = decoder.DecodeLogs()
	require.NoError(t, err)
	assert.Equal(t, 1, ld.LogRecordCount())
	assert.Equal(t, "baz", ld.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0).Body().AsString())
	assert.Equal(t, int64(12), decoder.Offset())

	// Third call should return EOF with empty logs
	ld, err = decoder.DecodeLogs()
	assert.ErrorIs(t, err, io.EOF)
	assert.Equal(t, 0, ld.LogRecordCount())
}

func TestStreamDecoding_flushAll(t *testing.T) {
	enc, err := textutils.LookupEncoding("utf8")
	require.NoError(t, err)
	r := regexp.MustCompile(`\r?\n`)
	codec := &textLogCodec{decoder: enc.NewDecoder(), unmarshalingSeparator: r, marshalingSeparator: "\n"}

	reader := bytes.NewReader([]byte("foo\nbar\nbaz\n"))

	// Decode with zero flush options to flush all records at once
	decoder, err := codec.NewLogsDecoder(reader, encoding.WithFlushItems(0), encoding.WithFlushBytes(0))
	require.NoError(t, err)

	// First call should return all 3 records (each record in separate ResourceLogs)
	ld, err := decoder.DecodeLogs()
	require.NoError(t, err)
	assert.Equal(t, 3, ld.LogRecordCount())
	assert.Equal(t, "foo", ld.ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0).Body().AsString())
	assert.Equal(t, "bar", ld.ResourceLogs().At(1).ScopeLogs().At(0).LogRecords().At(0).Body().AsString())
	assert.Equal(t, "baz", ld.ResourceLogs().At(2).ScopeLogs().At(0).LogRecords().At(0).Body().AsString())

	// Second call should return EOF with empty logs
	ld, err = decoder.DecodeLogs()
	assert.ErrorIs(t, err, io.EOF)
	assert.Equal(t, 0, ld.LogRecordCount())
}

func TestUnmarshalLogs(t *testing.T) {
	longLine := strings.Repeat("x", 100*1024)

	tests := []struct {
		name       string
		separator  string // unmarshaling separator regexp; empty means none configured
		input      string
		wantCount  int
		wantBodies []string // bodies in record order; checked when non-nil
	}{
		{name: "empty_payload", separator: `\r?\n`, input: "", wantCount: 0},
		{name: "empty_payload_no_separator", separator: "", input: "", wantCount: 0},
		{name: "1500_lines", separator: `\r?\n`, input: repeatedLines(1500), wantCount: 1500},
		{name: "100KiB_single_line", separator: `\r?\n`, input: longLine, wantCount: 1, wantBodies: []string{longLine}},
		{name: "whitespace_only_newline", separator: `\r?\n`, input: "\n", wantCount: 1, wantBodies: []string{""}},
		{name: "three_records", separator: `\r?\n`, input: "foo\nbar\nbaz\n", wantCount: 3, wantBodies: []string{"foo", "bar", "baz"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enc, err := textutils.LookupEncoding("utf8")
			require.NoError(t, err)
			codec := &textLogCodec{decoder: enc.NewDecoder(), marshalingSeparator: "\n"}
			if tt.separator != "" {
				codec.unmarshalingSeparator = regexp.MustCompile(tt.separator)
			}

			ld, err := codec.UnmarshalLogs([]byte(tt.input))
			require.NoError(t, err)
			assert.Equal(t, tt.wantCount, ld.LogRecordCount())
			if tt.wantBodies != nil {
				require.Equal(t, len(tt.wantBodies), ld.ResourceLogs().Len())
				for i, want := range tt.wantBodies {
					assert.Equal(t, want, ld.ResourceLogs().At(i).ScopeLogs().At(0).LogRecords().At(0).Body().AsString())
				}
			}
		})
	}
}

func TestUnmarshalLogsFactoryDefaultConfig(t *testing.T) {
	factory := NewFactory()
	ext, err := factory.Create(t.Context(), extensiontest.NewNopSettings(factory.Type()), factory.CreateDefaultConfig())
	require.NoError(t, err)
	require.NoError(t, ext.Start(t.Context(), componenttest.NewNopHost()))
	e := ext.(*textExtension)

	ld, err := e.UnmarshalLogs([]byte(repeatedLines(1500)))
	require.NoError(t, err)
	assert.Equal(t, 1500, ld.LogRecordCount())

	ld, err = e.UnmarshalLogs(nil)
	require.NoError(t, err)
	assert.Equal(t, 0, ld.LogRecordCount())
}

func TestStreamDecoding_defaultOptionsBatchOver1000(t *testing.T) {
	// The streaming path must keep its default 1000-item flush batches and
	// terminate with io.EOF, independent of the one-shot UnmarshalLogs behavior.
	enc, err := textutils.LookupEncoding("utf8")
	require.NoError(t, err)
	r := regexp.MustCompile(`\r?\n`)
	codec := &textLogCodec{decoder: enc.NewDecoder(), unmarshalingSeparator: r, marshalingSeparator: "\n"}

	decoder, err := codec.NewLogsDecoder(strings.NewReader(repeatedLines(1500)))
	require.NoError(t, err)

	ld, err := decoder.DecodeLogs()
	require.NoError(t, err)
	assert.Equal(t, 1000, ld.LogRecordCount())

	ld, err = decoder.DecodeLogs()
	require.NoError(t, err)
	assert.Equal(t, 500, ld.LogRecordCount())

	ld, err = decoder.DecodeLogs()
	assert.ErrorIs(t, err, io.EOF)
	assert.Equal(t, 0, ld.LogRecordCount())
}

func TestUnmarshalLogsAcrossMultipleBatches(t *testing.T) {
	// Regression test: inputs with more than the default 1000-item flush
	// threshold must not be silently truncated by UnmarshalLogs.
	enc, err := textutils.LookupEncoding("utf8")
	require.NoError(t, err)
	r := regexp.MustCompile(`\r?\n`)
	codec := &textLogCodec{decoder: enc.NewDecoder(), unmarshalingSeparator: r, marshalingSeparator: "\n"}

	var input strings.Builder
	const recordCount = 1863
	for i := range recordCount {
		fmt.Fprintf(&input, "record-%d\n", i)
	}

	logs, err := codec.UnmarshalLogs([]byte(input.String()))
	require.NoError(t, err)
	require.Equal(t, recordCount, logs.LogRecordCount())
}
