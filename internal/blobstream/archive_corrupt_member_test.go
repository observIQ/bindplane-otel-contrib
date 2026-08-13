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
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestArchive_CorruptCompressedMemberSkipped verifies a member whose compressed data
// breaks part way through (after the detection window) is skipped, not treated as an
// object-level failure. The zip's own stream is intact, so the failure is a
// deterministic per-entry decode error: skip the member and keep the others, rather
// than failing the whole object and re-reading it on every redelivery.
func TestArchive_CorruptCompressedMemberSkipped(t *testing.T) {
	t.Parallel()

	var sb strings.Builder
	for i := 0; i < 6000; i++ {
		fmt.Fprintf(&sb, "line-%06d-unique-payload-%06d\n", i, i*7)
	}
	raw := zipBytes(t, []tarFile{
		{name: "good.log", body: []byte("kept\n")},
		{name: "big.log", body: []byte(sb.String())},
	})

	// Corrupt a swath deep in big.log's compressed data. Decompression yields plenty of
	// output (well past the 3072-byte detection window) before the deflate stream
	// breaks, so it surfaces as a per-entry read failure during parsing.
	for i := len(raw) * 65 / 100; i < len(raw)*65/100+24; i++ {
		raw[i] ^= 0xff
	}

	var parseErrors atomic.Int64
	bodies := driveArchiveWithParseErrors(t, raw, func(context.Context) { parseErrors.Add(1) })

	require.Contains(t, bodies, "kept", "the intact member is still delivered")
	require.Equal(t, int64(1), parseErrors.Load(), "the corrupt member is skipped once")
}
