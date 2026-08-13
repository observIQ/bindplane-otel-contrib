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
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// zipBytes builds a zip archive in memory from the given entries.
func zipBytes(t *testing.T, files []tarFile) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		if f.dir {
			_, err := zw.Create(f.name + "/")
			require.NoError(t, err)
			continue
		}
		w, err := zw.Create(f.name)
		require.NoError(t, err)
		_, err = w.Write(f.body)
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

// errReader always fails, to exercise materialization-failure cleanup.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("injected read error") }

// newArchiveTempDir returns a fresh scratch dir for a test to thread into the archive
// materialization path (via its own stream) and assert is left empty afterward.
func newArchiveTempDir(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

func requireDirEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, entries, "temp dir should have no leftover files")
}

// TestArchiveZip_HeterogeneousEntries verifies a zip with text, JSON and Avro
// entries is detected and each entry is parsed on its own terms.
func TestArchiveZip_HeterogeneousEntries(t *testing.T) {
	dir := newArchiveTempDir(t)

	body := zipBytes(t, []tarFile{
		{name: "a.log", body: []byte("line1\nline2\n")},
		{name: "b.json", body: []byte(`[{"msg":"j1"},{"msg":"j2"}]`)},
		{name: "c.avro", body: avroOcfBytes(t, []string{"av1", "av2"})},
	})

	bodies, _, err := driveArchiveInDir(t, body, Offset{}, dir)
	require.NoError(t, err)
	require.Len(t, bodies, 6)
	all := joined(bodies)
	for _, want := range []string{"line1", "line2", "j1", "j2", "av1", "av2"} {
		require.Contains(t, all, want)
	}
	requireDirEmpty(t, dir)
}

// TestArchiveZip_DirectoryEntrySkipped verifies directory entries are skipped.
func TestArchiveZip_DirectoryEntrySkipped(t *testing.T) {
	dir := newArchiveTempDir(t)

	body := zipBytes(t, []tarFile{
		{name: "d", dir: true},
		{name: "d/a.log", body: []byte("hello\nworld\n")},
	})
	bodies, _, err := driveArchiveInDir(t, body, Offset{}, dir)
	require.NoError(t, err)
	require.Equal(t, []string{"hello", "world"}, bodies)
	requireDirEmpty(t, dir)
}

// TestArchiveZip_EntryCountLimit verifies the shared bomb-limit machinery applies
// to the zip backend and routes to the DLQ.
func TestArchiveZip_EntryCountLimit(t *testing.T) {
	dir := newArchiveTempDir(t)

	raw := zipBytes(t, []tarFile{
		{name: "a.log", body: []byte("x\n")},
		{name: "b.log", body: []byte("x\n")},
		{name: "c.log", body: []byte("x\n")},
	})
	ap := &archiveProducer{
		stream: LogStream{Name: "o", MaxLogSize: testMaxLogSize, Logger: zap.NewNop(), TryDecoding: true},
		open: func() (archiveBackend, error) {
			return newZipBackend(bytes.NewReader(raw), dir, defaultArchiveLimits().maxTotalBytes)
		},
		limits: archiveLimits{maxEntries: 2, maxEntryBytes: 1 << 30, maxTotalBytes: 1 << 30},
	}
	seq, err := ap.Records(context.Background(), Offset{})
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
	require.True(t, IsUnsupportedContent(fatal))
	requireDirEmpty(t, dir)
}

// TestArchiveZip_Resume verifies the (entryIndex, offset) resume model works for
// the zip backend: resuming at entry 1 skips entry 0 entirely.
func TestArchiveZip_Resume(t *testing.T) {
	dir := newArchiveTempDir(t)

	body := zipBytes(t, []tarFile{
		{name: "0.log", body: []byte("a1\na2\n")},
		{name: "1.json", body: []byte(`[{"msg":"j1"},{"msg":"j2"}]`)},
		{name: "2.log", body: []byte("c1\n")},
	})

	all, _, err := driveArchiveInDir(t, body, Offset{}, dir)
	require.NoError(t, err)
	require.Len(t, all, 5)

	fromEntry1, _, err := driveArchiveInDir(t, body, Offset{EntryIndex: 1, Offset: 0}, dir)
	require.NoError(t, err)
	require.Len(t, fromEntry1, 3) // j1,j2 + c1
	require.NotContains(t, joined(fromEntry1), "a1")
	require.Contains(t, joined(fromEntry1), "j1")
	require.Contains(t, joined(fromEntry1), "c1")
	requireDirEmpty(t, dir)
}

// TestArchiveZip_TempCleanupOnMaterializeError verifies a failed materialization
// removes the temp file so nothing leaks.
func TestArchiveZip_TempCleanupOnMaterializeError(t *testing.T) {
	dir := newArchiveTempDir(t)

	_, err := newZipBackend(errReader{}, dir, defaultArchiveLimits().maxTotalBytes)
	require.Error(t, err)
	requireDirEmpty(t, dir)
}

// TestArchiveZip_TempCleanupOnBadZip verifies a materialized but invalid zip
// (bytes that are not a real zip) is cleaned up and classified as a corrupt
// archive (a DLQ condition), not a generic transient failure.
func TestArchiveZip_TempCleanupOnBadZip(t *testing.T) {
	dir := newArchiveTempDir(t)

	// "PK\x03\x04" prefix looks zip-ish but the rest is not a valid archive.
	_, err := newZipBackend(bytes.NewReader([]byte("PK\x03\x04 not a real zip")), dir, defaultArchiveLimits().maxTotalBytes)
	require.Error(t, err)
	var corrupt ErrCorruptArchive
	require.ErrorAs(t, err, &corrupt)
	require.True(t, IsUnsupportedContent(err))
	requireDirEmpty(t, dir)
}

// TestArchiveZip_MaterializeCap verifies materialization aborts with an archive
// limit error once the copied bytes exceed the cap, so a small zip that would
// inflate a huge temp file (a materialization bomb) cannot exhaust disk. The body
// need not be a valid zip: the cap trips during the copy, before the structure is
// read.
func TestArchiveZip_MaterializeCap(t *testing.T) {
	dir := newArchiveTempDir(t)

	body := bytes.Repeat([]byte("A"), 100)
	_, err := newZipBackend(bytes.NewReader(body), dir, 10)
	require.Error(t, err)
	var limit ErrArchiveLimitExceeded
	require.ErrorAs(t, err, &limit)
	require.True(t, IsUnsupportedContent(err))
	requireDirEmpty(t, dir)
}
