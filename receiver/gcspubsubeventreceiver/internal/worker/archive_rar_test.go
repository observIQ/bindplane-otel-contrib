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
	"context"
	"os"
	"strings"
	"testing"

	"github.com/gabriel-vasile/mimetype"
	"github.com/nwaples/rardecode/v2"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// The rar fixture (testdata/logs.rar) holds three entries mirroring the 7z
// fixture: a.log ("line1\nline2\n"), b.json (`[{"msg":"j1"},{"msg":"j2"}]`), and
// c.avro (Avro OCF with "av1","av2"). It is a committed RAR5 fixture generated
// once with the rar CLI, because rardecode is read-only (no pure-Go rar writer).
// RAR4 decode is not exercised end-to-end because rar 7.x cannot create RAR4
// archives; RAR4 detection and routing are covered by TestArchiveRar_DetectionRoutes.

func readRarFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/logs.rar")
	require.NoError(t, err)
	return b
}

// TestArchiveRar_HeterogeneousEntries verifies a rar with text, JSON and Avro
// entries is detected and each entry is parsed on its own terms.
func TestArchiveRar_HeterogeneousEntries(t *testing.T) {
	t.Parallel()

	bodies, _, err := driveArchive(t, readRarFixture(t), Offset{})
	require.NoError(t, err)
	require.Len(t, bodies, 6)
	all := joined(bodies)
	for _, want := range []string{"line1", "line2", "j1", "j2", "av1", "av2"} {
		require.Contains(t, all, want)
	}
}

// TestArchiveRar_Resume verifies the shared (entryIndex, offset) resume model works
// for the rar backend: resuming at entry 1 skips entry 0 entirely.
func TestArchiveRar_Resume(t *testing.T) {
	t.Parallel()

	body := readRarFixture(t)

	all, _, err := driveArchive(t, body, Offset{})
	require.NoError(t, err)
	require.Len(t, all, 6)

	fromEntry1, _, err := driveArchive(t, body, Offset{EntryIndex: 1, Offset: 0})
	require.NoError(t, err)
	require.Len(t, fromEntry1, 4)
	require.NotContains(t, joined(fromEntry1), "line1")
	require.Contains(t, joined(fromEntry1), "j1")
	require.Contains(t, joined(fromEntry1), "av2")
}

func rar4Magic() []byte { return append([]byte("Rar!\x1a\x07\x00"), make([]byte, 128)...) }
func rar5Magic() []byte { return append([]byte("Rar!\x1a\x07\x01\x00"), make([]byte, 128)...) }

// TestArchiveRar_DetectionRoutes verifies both RAR4 and RAR5 signatures are
// detected and routed to a rar backend factory.
func TestArchiveRar_DetectionRoutes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"rar4", rar4Magic()},
		{"rar5", rar5Magic()},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			detected := mimetype.Detect(tc.body)
			require.True(t, detected.Is("application/x-rar-compressed"), "detected %s", detected.String())
			open, ok := archiveBackendFor(detected, bytes.NewReader(tc.body))
			require.True(t, ok)
			require.NotNil(t, open)
		})
	}
}

// TestArchiveRar_ProducerSelected verifies rar content routes to an archiveProducer
// through the full producer-selection path.
func TestArchiveRar_ProducerSelected(t *testing.T) {
	t.Parallel()

	stream := LogStream{
		Name:        "o.rar",
		Body:        newNopReadCloser(rar4Magic()),
		MaxLogSize:  testMaxLogSize,
		Logger:      zap.NewNop(),
		TryDecoding: true,
	}
	br, err := stream.BufferedReader(context.Background())
	require.NoError(t, err)
	producer, err := newRecordProducer(context.Background(), stream, br, nil)
	require.NoError(t, err)
	require.IsType(t, &archiveProducer{}, producer)
}

// TestArchiveRar_CorruptContentErrors verifies a valid RAR signature followed by a
// corrupt body surfaces an error (rardecode/v2 validates the first block header at
// open) rather than panicking or emitting spurious records.
func TestArchiveRar_CorruptContentErrors(t *testing.T) {
	t.Parallel()

	bodies, _, err := driveArchive(t, rar4Magic(), Offset{})
	require.Error(t, err)
	require.Empty(t, bodies)

	// A corrupt archive is a DLQ condition (unsupported file), not a generic
	// transient failure that would be redelivered pointlessly.
	var corrupt ErrCorruptArchive
	require.ErrorAs(t, err, &corrupt)
	require.Equal(t, dlqErrorKindUnsupportedFile, dlqConditionKind(err))
}

// TestArchiveRar_EntryAdapter verifies the rarEntry adapter maps header fields to
// the archiveEntry contract.
func TestArchiveRar_EntryAdapter(t *testing.T) {
	t.Parallel()

	r := strings.NewReader("payload")
	file := &rarEntry{hdr: &rardecode.FileHeader{Name: "dir/a.log", IsDir: false}, r: r}
	require.Equal(t, "dir/a.log", file.Name())
	require.False(t, file.IsDir())
	got, err := file.Open()
	require.NoError(t, err)
	require.Equal(t, r, got)

	dir := &rarEntry{hdr: &rardecode.FileHeader{Name: "dir/", IsDir: true}}
	require.True(t, dir.IsDir())
}
