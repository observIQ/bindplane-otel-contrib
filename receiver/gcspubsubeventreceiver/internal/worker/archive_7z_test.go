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
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// The 7z fixture (testdata/logs.7z) holds three entries: a.log ("line1\nline2\n"),
// b.json (`[{"msg":"j1"},{"msg":"j2"}]`), and c.avro (Avro OCF with "av1","av2").
// It is a committed fixture because bodgit/sevenzip is read-only (no writer).

func read7zFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/logs.7z")
	require.NoError(t, err)
	return b
}

// TestArchive7z_HeterogeneousEntries verifies a 7z with text, JSON and Avro
// entries is detected and each entry is parsed on its own terms.
func TestArchive7z_HeterogeneousEntries(t *testing.T) {
	dir := withArchiveTempDir(t)

	bodies, _, err := driveArchive(t, read7zFixture(t), Offset{})
	require.NoError(t, err)
	require.Len(t, bodies, 6)
	all := joined(bodies)
	for _, want := range []string{"line1", "line2", "j1", "j2", "av1", "av2"} {
		require.Contains(t, all, want)
	}
	requireDirEmpty(t, dir)
}

// TestArchive7z_Resume verifies the shared (entryIndex, offset) resume model works
// for the 7z backend: resuming at entry 1 skips entry 0 entirely.
func TestArchive7z_Resume(t *testing.T) {
	dir := withArchiveTempDir(t)

	body := read7zFixture(t)

	all, _, err := driveArchive(t, body, Offset{})
	require.NoError(t, err)
	require.Len(t, all, 6)

	fromEntry1, _, err := driveArchive(t, body, Offset{EntryIndex: 1, Offset: 0})
	require.NoError(t, err)
	// entry 0 (a.log: line1,line2) skipped; entries 1 (j1,j2) and 2 (av1,av2) remain
	require.Len(t, fromEntry1, 4)
	require.NotContains(t, joined(fromEntry1), "line1")
	require.Contains(t, joined(fromEntry1), "j1")
	require.Contains(t, joined(fromEntry1), "av2")
	requireDirEmpty(t, dir)
}

// TestArchive7z_EntryCountLimit verifies the shared bomb-limit machinery applies
// to the 7z backend and routes to the DLQ.
func TestArchive7z_EntryCountLimit(t *testing.T) {
	dir := withArchiveTempDir(t)

	body := read7zFixture(t)
	ap := &archiveProducer{
		stream: LogStream{Name: "o", MaxLogSize: testMaxLogSize, Logger: zap.NewNop(), TryDecoding: true},
		open:   func() (archiveBackend, error) { return newSevenZipBackend(bytes.NewReader(body)) },
		limits: archiveLimits{maxEntries: 1, maxEntryBytes: 1 << 30, maxTotalBytes: 1 << 30},
	}
	seq, err := ap.records(context.Background(), Offset{})
	require.NoError(t, err)

	var fatal error
	for _, rerr := range seq {
		if rerr != nil {
			fatal = rerr
			break
		}
	}
	require.Error(t, fatal)
	var limitErr ErrArchiveLimitExceeded
	require.ErrorAs(t, fatal, &limitErr)
	require.Equal(t, dlqErrorKindUnsupportedFile, dlqConditionKind(fatal))
	requireDirEmpty(t, dir)
}

// TestArchive7z_TempCleanupOnMaterializeError verifies a failed materialization
// removes the temp file.
func TestArchive7z_TempCleanupOnMaterializeError(t *testing.T) {
	dir := withArchiveTempDir(t)

	_, err := newSevenZipBackend(errReader{})
	require.Error(t, err)
	requireDirEmpty(t, dir)
}

// TestArchive7z_TempCleanupOnBadArchive verifies a materialized but invalid 7z is
// cleaned up.
func TestArchive7z_TempCleanupOnBadArchive(t *testing.T) {
	dir := withArchiveTempDir(t)

	_, err := newSevenZipBackend(bytes.NewReader([]byte("7z\xbc\xaf\x27\x1c not a real archive")))
	require.Error(t, err)
	var corrupt ErrCorruptArchive
	require.ErrorAs(t, err, &corrupt)
	require.Equal(t, dlqErrorKindUnsupportedFile, dlqConditionKind(err))
	requireDirEmpty(t, dir)
}
