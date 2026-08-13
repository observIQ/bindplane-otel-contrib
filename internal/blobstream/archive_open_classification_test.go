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
	"io"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// incompressibleBytes returns n deterministic, poorly-compressible bytes, so a zip built
// from them stays larger than the detection peek window.
func incompressibleBytes(n int) []byte {
	b := make([]byte, n)
	x := uint64(0x9e3779b97f4a7c15)
	for i := range b {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		b[i] = byte(x)
	}
	return b
}

// driveArchiveErr runs content through the record producer and returns whichever error
// surfaces: a construction/peek error, an open error returned from Records, or the last
// error yielded during iteration.
func driveArchiveErr(t *testing.T, stream LogStream) error {
	t.Helper()
	ctx := context.Background()

	reader, err := stream.BufferedReader(ctx)
	require.NoError(t, err)

	producer, err := NewRecordProducer(ctx, stream, reader, nil)
	if err != nil {
		return err
	}
	seq, err := producer.Records(ctx, Offset{})
	if err != nil {
		return err
	}
	var last error
	for _, rerr := range seq {
		if rerr != nil {
			last = rerr
		}
	}
	return last
}

// TestArchive_TruncatedGzippedArchiveIsDeadLetteredNotRetriedForever asserts that a
// gzip-wrapped archive whose compressed bytes are delivered whole and cleanly, but whose
// gzip stream is corrupt/truncated (so decompression fails), routes to the dead-letter
// queue instead of returning a bare, unclassified error that redelivers the object
// forever.
func TestArchive_TruncatedGzippedArchiveIsDeadLetteredNotRetriedForever(t *testing.T) {
	t.Parallel()

	zipRaw := zipBytes(t, []tarFile{{name: "a.bin", body: incompressibleBytes(16384)}})
	require.Greater(t, len(zipRaw), detectionPeekBytes, "zip must exceed the peek window so detection sees an archive")

	var gz bytes.Buffer
	gw := gzip.NewWriter(&gz)
	_, err := gw.Write(zipRaw)
	require.NoError(t, err)
	require.NoError(t, gw.Close())
	// Drop the gzip trailer so decompression fails with unexpected EOF at the end, while
	// the compressed object itself is delivered whole and cleanly.
	truncated := gz.Bytes()[:gz.Len()-10]

	stream := LogStream{
		Name:        "logs/a.zip.gz",
		Body:        io.NopCloser(bytes.NewReader(truncated)),
		MaxLogSize:  testMaxLogSize,
		Logger:      zap.NewNop(),
		TryDecoding: true,
		Size:        int64(len(truncated)),
	}

	err = driveArchiveErr(t, stream)
	require.Error(t, err, "an unreadable gzipped archive must reach the caller")
	require.True(t, IsUnsupportedContent(err), "a complete-but-unreadable stored object must route to the DLQ, got %v", err)
	require.False(t, IsStreamRead(err), "the download was complete; it is not a broken stream")
}

// TestArchive_ShortArchiveDownloadRetriesNotDeadLettered asserts that a valid archive
// whose download ended cleanly short of its known size is retried (the missing bytes may
// arrive next time) rather than dead-lettered as a corrupt archive.
func TestArchive_ShortArchiveDownloadRetriesNotDeadLettered(t *testing.T) {
	t.Parallel()

	zipRaw := zipBytes(t, []tarFile{{name: "a.bin", body: incompressibleBytes(16384)}})
	require.Greater(t, len(zipRaw), detectionPeekBytes)

	cut := len(zipRaw) * 6 / 10 // deliver 60%, cleanly, then EOF
	stream := LogStream{
		Name:        "logs/a.zip",
		Body:        &cutAfter{data: zipRaw, n: cut},
		MaxLogSize:  testMaxLogSize,
		Logger:      zap.NewNop(),
		TryDecoding: true,
		Size:        int64(len(zipRaw)),
	}

	err := driveArchiveErr(t, stream)
	require.Error(t, err, "a short archive download must reach the caller")
	require.True(t, IsStreamRead(err), "a short download of a valid archive must retry, got %v", err)
	require.False(t, IsUnsupportedContent(err), "a recoverable short download must not be dead-lettered")
}

// TestArchive_TempDirCreateFailureIsRetryableNotDeadLettered asserts that an
// infrastructure failure while materializing an archive (here, an unwritable temp dir)
// stays a generic, retryable error rather than being dead-lettered as corrupt content:
// the object is fine, the environment is not.
func TestArchive_TempDirCreateFailureIsRetryableNotDeadLettered(t *testing.T) {
	t.Parallel()

	zipRaw := zipBytes(t, []tarFile{{name: "a.bin", body: incompressibleBytes(16384)}})
	badDir := filepath.Join(t.TempDir(), "does-not-exist")

	_, _, err := driveArchiveInDir(t, zipRaw, Offset{}, badDir)

	require.Error(t, err, "a temp-dir create failure must reach the caller")
	require.ErrorContains(t, err, "open archive")
	require.False(t, IsUnsupportedContent(err), "an infrastructure failure must not be dead-lettered")
	require.False(t, IsStreamRead(err), "a temp-dir failure is not a stream read")
}
