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
	"archive/tar"
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/linkedin/goavro/v2"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"
)

// tarFile describes one entry to write into an in-process tar.
type tarFile struct {
	name string
	body []byte
	dir  bool
}

// tarBytes builds a tar archive in memory from the given entries.
func tarBytes(t *testing.T, files []tarFile) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range files {
		hdr := &tar.Header{Name: f.name, Mode: 0644, Size: int64(len(f.body)), Typeflag: tar.TypeReg}
		if f.dir {
			hdr = &tar.Header{Name: f.name, Mode: 0755, Typeflag: tar.TypeDir}
		}
		require.NoError(t, tw.WriteHeader(hdr))
		if !f.dir {
			_, err := tw.Write(f.body)
			require.NoError(t, err)
		}
	}
	require.NoError(t, tw.Close())
	return buf.Bytes()
}

// avroOcfBytes builds a minimal single-field Avro OCF carrying the given messages.
func avroOcfBytes(t *testing.T, msgs []string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := goavro.NewOCFWriter(goavro.OCFConfig{
		W:      &buf,
		Schema: `{"type":"record","name":"r","fields":[{"name":"msg","type":"string"}]}`,
	})
	require.NoError(t, err)
	for _, m := range msgs {
		require.NoError(t, w.Append([]interface{}{map[string]interface{}{"msg": m}}))
	}
	return buf.Bytes()
}

// driveArchive runs a body through the full stream -> producer -> record loop and
// returns each record's rendered body, the final resume position, and any fatal
// (object-failing) error. It mirrors how the worker consumes a producer.
func driveArchive(t *testing.T, body []byte, start Offset) (bodies []string, finalPos Offset, fatal error) {
	t.Helper()
	ctx := context.Background()
	stream := LogStream{
		Name:        "logs/object",
		Body:        newNopReadCloser(body),
		MaxLogSize:  testMaxLogSize,
		Logger:      zap.NewNop(),
		TryDecoding: true,
	}
	br, err := stream.BufferedReader(ctx)
	require.NoError(t, err)
	producer, err := newRecordProducer(ctx, stream, br, nil)
	require.NoError(t, err)

	seq, err := producer.records(ctx, start)
	if err != nil {
		return nil, Offset{}, err
	}
	for rec, rerr := range seq {
		if rerr != nil {
			if isDLQConditionError(rerr) {
				return bodies, producer.position(), rerr
			}
			// per-record error: skip, as the worker does
			continue
		}
		lr := plog.NewLogRecord()
		require.NoError(t, producer.appendLogBody(ctx, lr, rec))
		bodies = append(bodies, lr.Body().AsString())
	}
	return bodies, producer.position(), nil
}

func newNopReadCloser(b []byte) *nopReadCloser { return &nopReadCloser{r: bytes.NewReader(b)} }

type nopReadCloser struct{ r *bytes.Reader }

func (n *nopReadCloser) Read(p []byte) (int, error) { return n.r.Read(p) }
func (n *nopReadCloser) Close() error               { return nil }

func joined(bodies []string) string { return strings.Join(bodies, "\n") }

// TestArchive_TarHeterogeneousEntries verifies a tar with text, JSON and Avro
// entries is detected as an archive and every entry is parsed on its own terms.
func TestArchive_TarHeterogeneousEntries(t *testing.T) {
	t.Parallel()

	body := tarBytes(t, []tarFile{
		{name: "a.log", body: []byte("line1\nline2\n")},
		{name: "b.json", body: []byte(`[{"msg":"j1"},{"msg":"j2"}]`)},
		{name: "c.avro", body: avroOcfBytes(t, []string{"av1", "av2"})},
	})

	bodies, _, err := driveArchive(t, body, Offset{})
	require.NoError(t, err)
	require.Len(t, bodies, 6)
	all := joined(bodies)
	for _, want := range []string{"line1", "line2", "j1", "j2", "av1", "av2"} {
		require.Contains(t, all, want)
	}
}

// TestArchive_TarCompressedRecursion proves the post-decompress re-detect routes
// archives: a .tar.<codec> object decompresses, re-detects as tar, and parses.
func TestArchive_TarCompressedRecursion(t *testing.T) {
	t.Parallel()

	tarRaw := tarBytes(t, []tarFile{
		{name: "a.log", body: []byte("line1\nline2\n")},
		{name: "b.json", body: []byte(`[{"msg":"j1"},{"msg":"j2"}]`)},
	})

	bz, err := os.ReadFile("testdata/logs.tar.bz2")
	require.NoError(t, err)

	cases := []struct {
		name string
		body []byte
		// want records
		wantCount int
		wantSubs  []string
	}{
		{"tar.gz", gzipBytes(t, string(tarRaw)), 4, []string{"line1", "line2", "j1", "j2"}},
		{"tar.zst", zstdBytes(t, string(tarRaw)), 4, []string{"line1", "line2", "j1", "j2"}},
		{"tar.xz", xzBytes(t, string(tarRaw)), 4, []string{"line1", "line2", "j1", "j2"}},
		// committed fixture: entries a.log ("line1\nline2\n") + b.json (j1,j2)
		{"tar.bz2", bz, 4, []string{"line1", "line2", "j1", "j2"}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bodies, _, err := driveArchive(t, tc.body, Offset{})
			require.NoError(t, err)
			require.Len(t, bodies, tc.wantCount)
			all := joined(bodies)
			for _, s := range tc.wantSubs {
				require.Contains(t, all, s)
			}
		})
	}
}

// TestArchive_DirectoryEntrySkipped verifies directory entries are skipped
// without error and do not shift the parsed records.
func TestArchive_DirectoryEntrySkipped(t *testing.T) {
	t.Parallel()

	body := tarBytes(t, []tarFile{
		{name: "dir/", dir: true},
		{name: "dir/a.log", body: []byte("hello\nworld\n")},
	})
	bodies, _, err := driveArchive(t, body, Offset{})
	require.NoError(t, err)
	require.Equal(t, []string{"hello", "world"}, bodies)
}

// TestArchive_UnsupportedEntrySkipped verifies a recognized-but-unsupported entry
// (a PNG) is skipped while the remaining entries are still parsed, and the object
// as a whole does not fail.
func TestArchive_UnsupportedEntrySkipped(t *testing.T) {
	t.Parallel()

	png := []byte{
		0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	}
	body := tarBytes(t, []tarFile{
		{name: "img.png", body: png},
		{name: "a.log", body: []byte("kept1\nkept2\n")},
	})
	bodies, _, err := driveArchive(t, body, Offset{})
	require.NoError(t, err)
	require.Equal(t, []string{"kept1", "kept2"}, bodies)
}

// TestArchive_EntryCountLimit verifies exceeding the entry-count cap aborts the
// object with a DLQ-routed error rather than processing unboundedly.
func TestArchive_EntryCountLimit(t *testing.T) {
	t.Parallel()

	files := make([]tarFile, 0, 5)
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		files = append(files, tarFile{name: n + ".log", body: []byte("x\n")})
	}
	raw := tarBytes(t, files)

	ap := &archiveProducer{
		stream: LogStream{Name: "o", MaxLogSize: testMaxLogSize, Logger: zap.NewNop(), TryDecoding: true},
		open:   func() (archiveBackend, error) { return newTarBackend(bytes.NewReader(raw)), nil },
		limits: archiveLimits{maxEntries: 2, maxEntryBytes: 1 << 30, maxTotalBytes: 1 << 30},
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
}

// TestArchive_TotalByteLimit verifies exceeding the cumulative uncompressed byte
// cap aborts the object with a DLQ-routed error.
func TestArchive_TotalByteLimit(t *testing.T) {
	t.Parallel()

	// One large text entry, well over the byte cap.
	big := bytes.Repeat([]byte("xxxxxxxx\n"), 2000) // ~18 KB
	raw := tarBytes(t, []tarFile{{name: "big.log", body: big}})

	ap := &archiveProducer{
		stream: LogStream{Name: "o", MaxLogSize: testMaxLogSize, Logger: zap.NewNop(), TryDecoding: true},
		open:   func() (archiveBackend, error) { return newTarBackend(bytes.NewReader(raw)), nil },
		limits: archiveLimits{maxEntries: 1000, maxEntryBytes: 1 << 30, maxTotalBytes: 4096},
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
}

// TestArchive_ResumeEntryIndex verifies resuming at (EntryIndex, Offset) skips
// fully-consumed entries and resumes mid-entry using the parser's inner offset.
func TestArchive_ResumeEntryIndex(t *testing.T) {
	t.Parallel()

	body := tarBytes(t, []tarFile{
		{name: "0.log", body: []byte("a1\na2\na3\n")}, // each line "aN\n" = 3 bytes
		{name: "1.json", body: []byte(`[{"msg":"j1"},{"msg":"j2"}]`)},
		{name: "2.log", body: []byte("c1\nc2\n")},
	})

	// Full read establishes the baseline.
	all, _, err := driveArchive(t, body, Offset{})
	require.NoError(t, err)
	require.Len(t, all, 7) // 3 + 2 + 2

	// Resume at entry 1 from its start: entry 0 is skipped entirely.
	fromEntry1, _, err := driveArchive(t, body, Offset{EntryIndex: 1, Offset: 0})
	require.NoError(t, err)
	require.Len(t, fromEntry1, 4) // j1,j2 + c1,c2
	require.NotContains(t, joined(fromEntry1), "a1")
	require.Contains(t, joined(fromEntry1), "j1")
	require.Contains(t, joined(fromEntry1), "c2")

	// Resume mid-entry 0: skip the first line (3 bytes) via the inner offset.
	midEntry0, _, err := driveArchive(t, body, Offset{EntryIndex: 0, Offset: 3})
	require.NoError(t, err)
	require.NotContains(t, joined(midEntry0), "a1")
	require.Contains(t, joined(midEntry0), "a2")
	require.Contains(t, joined(midEntry0), "a3")
	// entries 1 and 2 still follow
	require.Contains(t, joined(midEntry0), "j1")
	require.Contains(t, joined(midEntry0), "c1")
}

// TestArchive_NonArchiveRegression verifies a non-archive object still uses the
// single-parser path and reports EntryIndex 0.
func TestArchive_NonArchiveRegression(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stream := LogStream{
		Name:        "plain.log",
		Body:        newNopReadCloser([]byte("one\ntwo\nthree\n")),
		MaxLogSize:  testMaxLogSize,
		Logger:      zap.NewNop(),
		TryDecoding: true,
	}
	br, err := stream.BufferedReader(ctx)
	require.NoError(t, err)
	producer, err := newRecordProducer(ctx, stream, br, nil)
	require.NoError(t, err)
	require.IsType(t, &singleParserProducer{}, producer)

	bodies, pos, err := driveArchive(t, []byte("one\ntwo\nthree\n"), Offset{})
	require.NoError(t, err)
	require.Equal(t, []string{"one", "two", "three"}, bodies)
	require.Equal(t, 0, pos.EntryIndex)
}
