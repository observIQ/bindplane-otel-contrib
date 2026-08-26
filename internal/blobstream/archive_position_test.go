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
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// TestArchive_PositionAfterTrailingSkippedEntryHasZeroOffset asserts that once iteration
// advances past a real entry to a trailing skipped one (here a directory), Position pairs
// the new entry index with offset 0, not the previous entry's parser offset. A mismatched
// {index, offset} checkpoint would mis-seek a future backend on resume.
func TestArchive_PositionAfterTrailingSkippedEntryHasZeroOffset(t *testing.T) {
	t.Parallel()

	backend := &stubBackend{entries: []archiveEntry{
		stubEntry{name: "a.log", body: "x1\nx2\n"},
		stubEntry{name: "d/", isDir: true},
	}}
	producer := newStubArchive(backend, zap.NewNop())

	seq, err := producer.Records(context.Background(), Offset{})
	require.NoError(t, err)
	for _, rerr := range seq {
		require.NoError(t, rerr)
	}

	pos := producer.Position()
	require.Equal(t, 1, pos.EntryIndex, "iteration advanced to the trailing directory")
	require.Equal(t, int64(0), pos.Offset, "a skipped trailing entry must not inherit the previous entry's offset")
}
