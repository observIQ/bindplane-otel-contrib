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
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// drainFatal runs an archiveProducer to completion and returns the first fatal
// (object-failing) error yielded, or nil if the object was fully consumed.
func drainFatal(t *testing.T, ap *archiveProducer) error {
	t.Helper()
	seq, err := ap.Records(context.Background(), Offset{})
	require.NoError(t, err)
	for _, rerr := range seq {
		if rerr != nil {
			return rerr
		}
	}
	return nil
}

// TestArchive_ClassificationByteLimit_Decompress verifies that a bomb cap tripping
// while the entry is being classified in decompressEntryToLeaf (the leading
// detection peek) fails the object with a DLQ-routed error, rather than being
// swallowed as a benign per-entry skip and the object acked.
func TestArchive_ClassificationByteLimit_Decompress(t *testing.T) {
	t.Parallel()

	// A single text entry larger than the detection peek. maxEntryBytes is far
	// below detectionPeekBytes (3072), so the peek in decompressEntryToLeaf trips
	// the cap before any parser is selected.
	raw := tarBytes(t, []tarFile{{name: "a.log", body: bytes.Repeat([]byte("x\n"), 4096)}})

	ap := &archiveProducer{
		stream: LogStream{Name: "o", MaxLogSize: testMaxLogSize, Logger: zap.NewNop(), TryDecoding: true},
		open:   func() (archiveBackend, error) { return newTarBackend(bytes.NewReader(raw)), nil },
		limits: archiveLimits{maxEntries: 1000, maxEntryBytes: 512, maxTotalBytes: 1 << 30, maxNestingDepth: 8},
	}

	fatal := drainFatal(t, ap)
	require.Error(t, fatal, "cap tripping during classification must fail the object, not be skipped")
	var limitErr ErrArchiveLimitExceeded
	require.ErrorAs(t, fatal, &limitErr)
	require.True(t, IsUnsupportedContent(fatal))
}

// TestArchive_ClassificationByteLimit_Parse verifies that a bomb cap tripping while
// the selected parser classifies the entry (here the JSON parser scanning for a
// "Records" key past the detection peek) fails the object with a DLQ-routed error,
// rather than being swallowed as a benign per-entry skip.
func TestArchive_ClassificationByteLimit_Parse(t *testing.T) {
	t.Parallel()

	// A JSON object whose first (non-Records) value is large enough that the
	// decoder must read past the 3072-byte detection peek to reach it. maxEntryBytes
	// sits between the peek and that depth, so decompressEntryToLeaf succeeds but
	// parser.Parse trips the cap while scanning.
	var body bytes.Buffer
	body.WriteString(`{"pad":"`)
	body.Write(bytes.Repeat([]byte("x"), 5000))
	body.WriteString(`","Records":[{"m":"a"}]}`)
	raw := tarBytes(t, []tarFile{{name: "a.json", body: body.Bytes()}})

	ap := &archiveProducer{
		stream: LogStream{Name: "o", MaxLogSize: 8192, Logger: zap.NewNop(), TryDecoding: true},
		open:   func() (archiveBackend, error) { return newTarBackend(bytes.NewReader(raw)), nil },
		limits: archiveLimits{maxEntries: 1000, maxEntryBytes: 3500, maxTotalBytes: 1 << 30, maxNestingDepth: 8},
	}

	fatal := drainFatal(t, ap)
	require.Error(t, fatal, "cap tripping during parser classification must fail the object, not be skipped")
	var limitErr ErrArchiveLimitExceeded
	require.ErrorAs(t, fatal, &limitErr)
	require.True(t, IsUnsupportedContent(fatal))
}
