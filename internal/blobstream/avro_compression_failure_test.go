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
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAvro_CompressedStreamFailures pins the classification of a compressed Avro object
// whose failure surfaces at the decompression layer rather than the raw source. The
// classifier must read the raw tier for the retry decision, since a decompression error
// masks a clean raw source. Without it every compressed failure looks like a broken
// stream and retries forever, losing truncation and corruption both.
func TestAvro_CompressedStreamFailures(t *testing.T) {
	t.Parallel()

	gz := gzipBytes(t, string(multiBlockAvro(t)))

	t.Run("interrupted download retries", func(t *testing.T) {
		t.Parallel()
		readErr := errors.New("read: connection reset by peer")
		records, err := driveAvro(t, gz, len(gz)/2, readErr)

		require.Positive(t, records, "records read before the break are still delivered")
		require.Error(t, err)
		require.True(t, IsStreamRead(err), "a broken raw stream under compression must retry")
		require.ErrorIs(t, err, readErr)
		require.False(t, IsTruncatedObject(err))
		require.False(t, IsUnsupportedContent(err), "a broken stream is retryable, not a DLQ condition")
	})

	t.Run("stored truncation delivers and acks", func(t *testing.T) {
		t.Parallel()
		// Clean raw EOF part way through: the stored gzip is short, so the decompressor
		// runs out of input while the raw source ended cleanly.
		records, err := driveAvro(t, gz, len(gz)/2, nil)

		require.Positive(t, records, "records before the cut are still delivered")
		require.Error(t, err)
		require.True(t, IsTruncatedObject(err), "a short compressed object is a truncation, not a broken stream")
		require.False(t, IsStreamRead(err), "stored truncation must not retry")
	})

	t.Run("short download of a known size retries", func(t *testing.T) {
		t.Parallel()
		// The object's size is known but the raw source ends short with a clean EOF, so
		// the download stopped early rather than the object being stored truncated.
		records, err := driveAvroWithSize(t, gz, len(gz)/2, nil, int64(len(gz)))

		require.Positive(t, records, "records before the cut are still delivered")
		require.Error(t, err)
		require.True(t, IsStreamRead(err), "a raw source short of the known size is an interrupted download")
		require.False(t, IsTruncatedObject(err))
	})

	t.Run("mid-stream corruption dead-letters", func(t *testing.T) {
		t.Parallel()
		corrupt := append([]byte{}, gz...)
		for i := len(corrupt) / 2; i < len(corrupt)/2+16; i++ {
			corrupt[i] ^= 0xff
		}
		_, err := driveAvro(t, corrupt, len(corrupt), nil)

		require.Error(t, err)
		require.True(t, IsUnsupportedContent(err), "deterministic compression corruption belongs in the DLQ")
		require.False(t, IsStreamRead(err), "deterministic corruption is not retryable")
		require.False(t, IsTruncatedObject(err))
	})
}
