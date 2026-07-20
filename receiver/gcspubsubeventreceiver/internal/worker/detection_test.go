// Copyright observIQ, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package worker

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

const testMaxLogSize = 4096

// gzipBytes returns the gzip-compressed form of s.
func gzipBytes(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, err := w.Write([]byte(s))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return buf.Bytes()
}

// readAllFromStream builds a LogStream over body and returns the decoded content
// produced by BufferedReader.
func readAllFromStream(t *testing.T, stream *LogStream) ([]byte, error) {
	t.Helper()
	br, err := stream.BufferedReader(context.Background())
	if err != nil {
		return nil, err
	}
	return io.ReadAll(br)
}

// TestBufferedReader_ContentAuthoritativeGzip verifies gzip is decided from the
// object's bytes, not from its name or Content-Encoding label.
func TestBufferedReader_ContentAuthoritativeGzip(t *testing.T) {
	t.Parallel()

	gzipEnc := "gzip"

	testCases := []struct {
		name            string
		objectName      string
		contentEncoding *string
		body            []byte
		want            string
	}{
		{
			// The customer bug: uncompressed content under a .gz name must not be
			// fed to gzip.NewReader.
			name:       "uncompressed bytes under .gz name read as raw",
			objectName: "logs/app.log.gz",
			body:       []byte("hello\nworld\n"),
			want:       "hello\nworld\n",
		},
		{
			name:       "real gzip without .gz name is decompressed",
			objectName: "logs/app.log",
			body:       gzipBytes(t, "hello\nworld\n"),
			want:       "hello\nworld\n",
		},
		{
			name:            "Content-Encoding gzip on plaintext body reads as raw",
			objectName:      "logs/app.log",
			contentEncoding: &gzipEnc,
			body:            []byte("plain text line\n"),
			want:            "plain text line\n",
		},
		{
			name:            "real gzip with matching Content-Encoding is decompressed",
			objectName:      "logs/app.log",
			contentEncoding: &gzipEnc,
			body:            gzipBytes(t, "compressed payload\n"),
			want:            "compressed payload\n",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			stream := &LogStream{
				Name:            tc.objectName,
				ContentEncoding: tc.contentEncoding,
				Body:            io.NopCloser(bytes.NewReader(tc.body)),
				MaxLogSize:      testMaxLogSize,
				Logger:          zap.NewNop(),
			}

			got, err := readAllFromStream(t, stream)
			require.NoError(t, err)
			require.Equal(t, tc.want, string(got))
		})
	}
}

// TestBufferedReader_TinyAndEmpty verifies short objects do not error.
func TestBufferedReader_TinyAndEmpty(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"empty", []byte("")},
		{"one byte", []byte("x")},
		{"one byte under .gz name", []byte("x")},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			name := "obj"
			if strings.Contains(tc.name, ".gz") {
				name = "obj.gz"
			}
			stream := &LogStream{
				Name:       name,
				Body:       io.NopCloser(bytes.NewReader(tc.body)),
				MaxLogSize: testMaxLogSize,
				Logger:     zap.NewNop(),
			}
			got, err := readAllFromStream(t, stream)
			require.NoError(t, err)
			require.Equal(t, tc.body, got)
		})
	}
}

// TestBufferedReader_LabelMismatchWarns verifies a warning is emitted when the
// gzip label disagrees with the detected content.
func TestBufferedReader_LabelMismatchWarns(t *testing.T) {
	t.Parallel()

	core, logs := observer.New(zap.WarnLevel)
	logger := zap.New(core)

	stream := &LogStream{
		Name:       "logs/app.log.gz", // labeled gzip
		Body:       io.NopCloser(bytes.NewReader([]byte("not compressed\n"))),
		MaxLogSize: testMaxLogSize,
		Logger:     logger,
	}

	_, err := readAllFromStream(t, stream)
	require.NoError(t, err)
	require.Positive(t, logs.FilterMessageSnippet("label").Len(),
		"expected a warning about the gzip label disagreeing with content")
}

// TestBufferedReader_NoWarnWhenLabelMatches verifies no mismatch warning fires
// when label and content agree.
func TestBufferedReader_NoWarnWhenLabelMatches(t *testing.T) {
	t.Parallel()

	core, logs := observer.New(zap.WarnLevel)
	logger := zap.New(core)

	stream := &LogStream{
		Name:       "logs/app.log", // no gzip label
		Body:       io.NopCloser(bytes.NewReader([]byte("plain\n"))),
		MaxLogSize: testMaxLogSize,
		Logger:     logger,
	}

	_, err := readAllFromStream(t, stream)
	require.NoError(t, err)
	require.Zero(t, logs.FilterMessageSnippet("label").Len(),
		"expected no mismatch warning when label matches content")
}

// TestStartsWithJSONObjectOrArray verifies the strengthened structural rule:
// a leading '{' must be followed by '"' or '}', and a leading '[' by '{' or ']'.
func TestStartsWithJSONObjectOrArray(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name  string
		input string
		want  bool
	}{
		{"records object", `{"Records":[{"a":1}]}`, true},
		{"empty object", `{}`, true},
		{"object with leading whitespace", "   \n\t{\"x\":1}", true},
		{"object inner whitespace before key", `{  "x": 1}`, true},
		{"array of objects", `[{"a":1},{"b":2}]`, true},
		{"empty array", `[]`, true},
		{"array with whitespace then object", `[  {} ]`, true},
		{"timestamp log line", `[2024-01-01T00:00:00Z] INFO started`, false},
		{"array of numbers", `[1,2,3]`, false},
		{"array of strings", `["a","b"]`, false},
		{"plain text", "not json at all", false},
		{"empty input", "", false},
		{"lone open brace", "{", false},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			br := NewBufferedReader(strings.NewReader(tc.input), testMaxLogSize)
			got, err := StartsWithJSONObjectOrArray(br)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestStartsWithAvroOcfMagic verifies the magic check and that short inputs do
// not error.
func TestStartsWithAvroOcfMagic(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name  string
		input []byte
		want  bool
	}{
		{"avro magic", append([]byte("Obj\x01"), []byte("schema...")...), true},
		{"not avro", []byte("PK\x03\x04rest"), false},
		{"short input", []byte("Ob"), false},
		{"empty input", []byte(""), false},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			br := NewBufferedReader(bytes.NewReader(tc.input), testMaxLogSize)
			got, err := StartsWithAvroOcfMagic(br)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestNewParser_ContentAuthoritative verifies parser selection ignores the
// extension/content-type and routes on content alone.
func TestNewParser_ContentAuthoritative(t *testing.T) {
	t.Parallel()

	avro := string(append([]byte("Obj\x01"), []byte("\x04\x14avro.schema")...))

	testCases := []struct {
		name        string
		objectName  string
		contentType *string
		content     string
		tryDecoding bool
		wantType    string
	}{
		{
			name:        "json content with wrong extension is json",
			objectName:  "data.bin",
			content:     `[{"a":1}]`,
			tryDecoding: true,
			wantType:    "*worker.jsonParser",
		},
		{
			name:        "avro content with wrong extension is avro",
			objectName:  "data.bin",
			content:     avro,
			tryDecoding: true,
			wantType:    "*worker.avroOcfParser",
		},
		{
			name:        "timestamp log line is line parser",
			objectName:  "data.json", // even a .json name must not force JSON
			content:     `[2024-01-01T00:00:00Z] INFO started`,
			tryDecoding: true,
			wantType:    "*worker.lineParser",
		},
		{
			name:        "tryDecoding false is always line parser",
			objectName:  "data.json",
			content:     `[{"a":1}]`,
			tryDecoding: false,
			wantType:    "*worker.lineParser",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			stream := LogStream{
				Name:        tc.objectName,
				ContentType: tc.contentType,
				MaxLogSize:  testMaxLogSize,
				Logger:      zap.NewNop(),
				TryDecoding: tc.tryDecoding,
			}
			br := NewBufferedReader(strings.NewReader(tc.content), testMaxLogSize)
			parser, err := newParser(context.Background(), stream, br)
			require.NoError(t, err)
			require.Equal(t, tc.wantType, typeName(parser))
		})
	}
}

// typeName returns the concrete type name of the parser for assertion.
func typeName(p LogParser) string {
	switch p.(type) {
	case *jsonParser:
		return "*worker.jsonParser"
	case *avroOcfParser:
		return "*worker.avroOcfParser"
	case *lineParser:
		return "*worker.lineParser"
	default:
		return "unknown"
	}
}

// TestNewParser_UnsupportedContentDLQ verifies content that is not text, Avro, or
// JSON produces an ErrUnsupportedContent that maps to the unsupported-file DLQ
// condition, rather than being line-parsed as garbled text.
func TestNewParser_UnsupportedContentDLQ(t *testing.T) {
	t.Parallel()

	// PNG signature + IHDR chunk start, enough for mimetype to detect image/png.
	png := []byte{
		0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	}
	stream := LogStream{
		Name:        "logs/object",
		MaxLogSize:  testMaxLogSize,
		Logger:      zap.NewNop(),
		TryDecoding: true,
	}
	br := NewBufferedReader(bytes.NewReader(png), testMaxLogSize)
	_, err := newParser(context.Background(), stream, br)
	require.Error(t, err)

	var unsupported ErrUnsupportedContent
	require.ErrorAs(t, err, &unsupported)
	require.Equal(t, "image/png", unsupported.MIMEType)
	require.Equal(t, dlqErrorKindUnsupportedFile, dlqConditionKind(err))
}
