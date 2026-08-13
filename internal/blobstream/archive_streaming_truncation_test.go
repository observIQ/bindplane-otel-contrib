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
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// TestArchive_StreamingMemberTruncationIsCounted asserts that a member of a STREAMING
// archive (tar/rar) whose own payload is internally truncated is counted and logged as a
// skipped entry, when the container is intact and more entries follow. The streaming
// backend's Next() only reports a truncation when the OBJECT stream itself runs out; with
// an intact container it advances cleanly to the next entry, so without an explicit skip
// the truncated member is silently dropped and the object acked (F5 fixed only the
// materialized zip/7z half of this).
func TestArchive_StreamingMemberTruncationIsCounted(t *testing.T) {
	t.Parallel()

	// A tar member that is itself a gzip missing its trailer (reading it yields the data
	// then io.ErrUnexpectedEOF), followed by an intact member. The tar container is
	// complete, so the raw source is NOT at EOF when the member truncation surfaces.
	bad := tarFile{name: "member.log.gz", body: truncatedGzip(t, strings.Repeat("line\n", 1000))}
	good := tarFile{name: "good.log", body: []byte("kept\n")}
	raw := tarBytes(t, []tarFile{bad, good})

	r := NewBufferedReader(bytes.NewReader(raw), testMaxLogSize)
	var parseErrors int
	producer := &archiveProducer{
		stream:       LogStream{Name: "o.tar", MaxLogSize: testMaxLogSize, Logger: zap.NewNop(), TryDecoding: true},
		open:         func() (archiveBackend, error) { return newTarBackend(bytes.NewReader(raw)), nil },
		limits:       defaultArchiveLimits(),
		onParseError: func(context.Context) { parseErrors++ },
		reader:       r,
		format:       "application/x-tar",
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

	require.NoError(t, fatal, "an intact tar with one internally-truncated member does not fail the object")
	require.Contains(t, bodies, "kept", "the intact member still parses")
	require.Equal(t, 1, parseErrors, "the truncated streaming member must be counted as a skipped entry, not silently dropped")
}
