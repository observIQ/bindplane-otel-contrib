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

package blobstream

import (
	"bytes"
	"compress/gzip"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// truncatedGzip gzip-compresses body and drops the trailing bytes so decompressing
// it runs out before the gzip trailer, surfacing io.ErrUnexpectedEOF at the end of
// the stream rather than a clean EOF.
func truncatedGzip(t *testing.T, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	_, err := gw.Write([]byte(body))
	require.NoError(t, err)
	require.NoError(t, gw.Close())
	data := buf.Bytes()
	// Drop the 8-byte trailer (and one more) so the reader decodes the data and then
	// hits an unexpected EOF looking for the trailer.
	return data[:len(data)-9]
}

// TestArchive_MaterializedMemberTruncationIsCounted asserts that a member of a
// materialized archive (zip/7z) whose content ends in an unexpected EOF is counted
// and logged as a skipped entry, rather than silently dropped. The archive itself is
// intact, so a materialized backend's Next never reports the member truncation the way
// a streaming backend's does; without an explicit skip here the truncation vanishes
// with no metric and no log.
func TestArchive_MaterializedMemberTruncationIsCounted(t *testing.T) {
	t.Parallel()

	// A member that is itself a gzip missing its trailer: reading it yields the data
	// and then io.ErrUnexpectedEOF. The body is larger than the detection peek so the
	// truncation surfaces during parsing, not during format detection. The zip around
	// it is complete.
	bad := tarFile{name: "member.log.gz", body: truncatedGzip(t, strings.Repeat("line\n", 1000))}
	good := tarFile{name: "good.log", body: []byte("kept\n")}
	raw := zipBytes(t, []tarFile{bad, good})

	r := NewBufferedReader(bytes.NewReader(raw), testMaxLogSize)
	var parseErrors int
	producer := &archiveProducer{
		stream:       LogStream{Name: "o.zip", MaxLogSize: testMaxLogSize, Logger: zap.NewNop(), TryDecoding: true},
		open:         func() (archiveBackend, error) { return newZipBackend(bytes.NewReader(raw), "", 1<<30) },
		limits:       defaultArchiveLimits(),
		onParseError: func(context.Context) { parseErrors++ },
		reader:       r,
		format:       "application/zip",
	}

	seq, err := producer.Records(context.Background(), Offset{})
	require.NoError(t, err)

	var bodies []string
	var fatal error
	for rec, rerr := range seq {
		if rerr != nil {
			fatal = rerr
			continue
		}
		if text, ok := rec.(string); ok {
			bodies = append(bodies, text)
		}
	}

	require.NoError(t, fatal, "the archive is intact, so a truncated member does not fail the object")
	require.Contains(t, bodies, "kept", "the intact member still parses")
	require.Equal(t, 1, parseErrors, "the truncated member must be counted as a skipped entry, not silently dropped")
}
