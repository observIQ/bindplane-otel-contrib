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
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// TestJSONSequence_TruncatedCompressedJSONIsClassified asserts a compressed JSON object
// whose decompressed content ends short (io.ErrUnexpectedEOF) at classification is
// classified rather than returned as a bare error. A bare classifyJSON error is treated as
// transient, so the same deterministic object redelivers forever; classifying it as a
// truncated object delivers what was read and acks, breaking the loop.
func TestJSONSequence_TruncatedCompressedJSONIsClassified(t *testing.T) {
	t.Parallel()

	// A JSON object that fits the classification window but exceeds the detection window,
	// so StartsWithJSONObjectOrArray (512 bytes) succeeds while classifyJSON (4096 bytes)
	// reads to the truncation.
	content := `{"msg":"` + strings.Repeat("x", 3000) + `"}`
	require.Less(t, len(content), maxRecordsSearchBytes, "content must fit the classification window")
	require.Greater(t, len(content), jsonPeekBytes, "content must exceed the detection window")

	// Drop the 8-byte gzip footer: the DEFLATE stream stays intact, so decompression
	// yields the full content and then io.ErrUnexpectedEOF when the missing footer is read.
	g := gzipBytes(t, content)
	truncated := g[:len(g)-8]

	_, last := driveStream(LogStream{
		Name:        "logs/object.json.gz",
		Body:        io.NopCloser(bytes.NewReader(truncated)),
		MaxLogSize:  testMaxLogSize,
		Logger:      zap.NewNop(),
		TryDecoding: true,
	})

	require.Error(t, last, "a truncated compressed object must be classified, not returned bare")
	require.True(t, IsTruncatedObject(last),
		"a complete-at-rest but content-truncated object delivers what was read and acks")
	require.False(t, IsStreamRead(last), "the raw source completed, so it is not a retryable broken stream")
}
