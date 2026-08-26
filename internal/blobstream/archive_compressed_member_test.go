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
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"
)

// TestArchive_CompressedMemberDecompressed verifies a compressed archive member
// (a .log.gz inside a tar) is decompressed and parsed rather than dropped as an
// unsupported binary. Log shippers commonly gzip each file before archiving them.
func TestArchive_CompressedMemberDecompressed(t *testing.T) {
	t.Parallel()

	raw := tarBytes(t, []tarFile{
		{name: "a.log.gz", body: gzipBytes(t, "inner1\ninner2\n")},
		{name: "b.log", body: []byte("plain1\n")},
	})

	bodies := driveArchiveWithParseErrors(t, raw, nil)
	require.Equal(t, []string{"inner1", "inner2", "plain1"}, bodies)
}

// TestArchive_NestingDepthCapSkipsMember verifies a member requiring more
// decompression layers than the nesting cap allows is skipped (counted as a parse
// error) rather than expanded, so a deeply nested compression bomb cannot exhaust
// resources. The other members still parse.
func TestArchive_NestingDepthCapSkipsMember(t *testing.T) {
	t.Parallel()

	raw := tarBytes(t, []tarFile{
		{name: "bomb.log.gz", body: gzipBytes(t, "x\n")},
		{name: "good.log", body: []byte("kept\n")},
	})

	ctx := context.Background()
	ap := &archiveProducer{
		stream: LogStream{Name: "o", MaxLogSize: testMaxLogSize, Logger: zap.NewNop(), TryDecoding: true},
		open:   func() (archiveBackend, error) { return newTarBackend(bytes.NewReader(raw)), nil },
		// maxNestingDepth 0: no decompression layers permitted, so the gz member trips
		// the cap and is skipped.
		limits: archiveLimits{maxEntries: 1000, maxEntryBytes: 1 << 30, maxTotalBytes: 1 << 30, maxNestingDepth: 0},
	}

	var parseErrors atomic.Int64
	ap.onParseError = func(context.Context) { parseErrors.Add(1) }

	seq, err := ap.Records(ctx, Offset{})
	require.NoError(t, err)

	var bodies []string
	for rec, rerr := range seq {
		require.NoError(t, rerr)
		lr := plog.NewLogRecord()
		require.NoError(t, ap.AppendLogBody(ctx, lr, rec))
		bodies = append(bodies, lr.Body().AsString())
	}
	require.Equal(t, []string{"kept"}, bodies)
	require.Equal(t, int64(1), parseErrors.Load())
}

// TestArchive_UnusableContentEntrySkipped verifies a member whose record stream
// yields unusable content mid-parse (a truncated JSON array) is skipped after its
// readable prefix is delivered, and the other members still parse.
func TestArchive_UnusableContentEntrySkipped(t *testing.T) {
	t.Parallel()

	raw := tarBytes(t, []tarFile{
		{name: "bad.json", body: []byte(`[{"a":1},{"b":2`)}, // truncated array
		{name: "good.log", body: []byte("kept\n")},
	})

	var parseErrors atomic.Int64
	bodies := driveArchiveWithParseErrors(t, raw, func(context.Context) { parseErrors.Add(1) })

	require.Contains(t, bodies, "kept", "the good member still parses")
	require.Equal(t, int64(1), parseErrors.Load(), "the truncated json member is skipped once")
}
