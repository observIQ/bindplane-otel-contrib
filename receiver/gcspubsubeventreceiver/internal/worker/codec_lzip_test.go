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
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// TestBufferedReader_Lzip verifies lzip is content-detected (mimetype reports
// application/lzip) and decompressed via the sorairolake/lzip-go decoder. The
// sample is a real lzip-CLI-generated file of the bytes "hello\nworld\n".
func TestBufferedReader_Lzip(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("testdata/hello.txt.lz")
	require.NoError(t, err)

	stream := &LogStream{
		Name:       "logs/object",
		Body:       io.NopCloser(bytes.NewReader(body)),
		MaxLogSize: testMaxLogSize,
		Logger:     zap.NewNop(),
	}
	got, err := readAllFromStream(t, stream)
	require.NoError(t, err)
	require.Equal(t, "hello\nworld\n", string(got))
}
