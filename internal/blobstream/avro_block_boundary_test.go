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
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAvro_BlockBoundaryTruncationReported asserts that an Avro object whose download ends
// cleanly exactly at a block boundary, short of the object's known size, is reported as an
// incomplete download rather than treated as a clean end. goavro's Scan returns a nil
// error when the next block-count read hits a clean EOF, so without consulting the raw
// tier the missing block would be silently acked and lost.
func TestAvro_BlockBoundaryTruncationReported(t *testing.T) {
	t.Parallel()

	full := avroOcfBytes(t, []string{"rec-1", "rec-2"})
	// A single-record OCF has the same header + first-block length, so its size is the
	// exact offset of the first block boundary in the two-record object.
	boundary := len(avroOcfBytes(t, []string{"rec-1"}))

	// Deliver header + first block exactly (clean EOF), with the known size being the
	// full two-block length.
	records, err := driveAvroWithSize(t, full, boundary, nil, int64(len(full)))

	require.Equal(t, 1, records, "the first block's record is delivered before the cut")
	require.Error(t, err, "a block-boundary truncation short of the known size must be reported")
	require.True(t, IsStreamRead(err), "a download short of the known size is retryable, got %v", err)
}
