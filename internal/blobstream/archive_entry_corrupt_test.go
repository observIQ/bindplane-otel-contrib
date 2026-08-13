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
	"archive/zip"
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// TestArchiveZip_CorruptMemberSkipsEntryNotObject asserts a zip with one corrupt member
// (a deterministic per-entry decode failure) skips just that entry and still delivers the
// good one, rather than failing the whole object and looping on every redelivery.
func TestArchiveZip_CorruptMemberSkipsEntryNotObject(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	// A stored (uncompressed) member: its data is literal in the archive, so flipping a
	// byte changes what is read and its CRC no longer matches.
	w1, err := zw.CreateHeader(&zip.FileHeader{Name: "bad.log", Method: zip.Store})
	require.NoError(t, err)
	_, err = w1.Write([]byte("MARKER-bad-line-one\nMARKER-bad-line-two\n"))
	require.NoError(t, err)
	w2, err := zw.Create("good.log")
	require.NoError(t, err)
	_, err = w2.Write([]byte("good1\ngood2\n"))
	require.NoError(t, err)
	require.NoError(t, zw.Close())

	data := buf.Bytes()
	// Corrupt a byte in the first member's stored data so archive/zip's checksum reader
	// fails when the entry is read.
	idx := bytes.Index(data, []byte("MARKER"))
	require.GreaterOrEqual(t, idx, 0)
	data[idx+2] ^= 0xff

	stream := LogStream{
		Name:        "o.zip",
		Body:        io.NopCloser(bytes.NewReader(data)),
		MaxLogSize:  testMaxLogSize,
		Logger:      zap.NewNop(),
		TryDecoding: true,
	}

	ctx := context.Background()
	reader, err := stream.BufferedReader(ctx)
	require.NoError(t, err)
	producer, err := NewRecordProducer(ctx, stream, reader, nil)
	require.NoError(t, err)
	seq, err := producer.Records(ctx, Offset{})
	require.NoError(t, err)

	var bodies []string
	var fatal error
	for rec, rerr := range seq {
		if rerr != nil {
			if IsStreamRead(rerr) || IsUnsupportedContent(rerr) {
				fatal = rerr
			}
			continue
		}
		bodies = append(bodies, rec.(string))
	}

	t.Logf("OBSERVED bodies=%v fatal=%v", bodies, fatal)
	require.NoError(t, fatal, "a corrupt member must not fail the whole object")
	require.Contains(t, bodies, "good1", "the good entry is still delivered")
	require.Contains(t, bodies, "good2")
}
