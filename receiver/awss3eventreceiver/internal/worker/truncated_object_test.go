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

package worker_test

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/observiq/bindplane-otel-contrib/receiver/awss3eventreceiver/internal/worker"
)

// TestLineParser_TruncatedObjectIsNotRetryable asserts a stream cut short mid-record is
// reported as content the receiver cannot use, rather than as a broken connection.
// Retrying reads the same truncated bytes, so a retryable answer redelivers forever.
func TestLineParser_TruncatedObjectIsNotRetryable(t *testing.T) {
	t.Parallel()

	reader := &errAfterReader{prefix: []byte("first\nsecond\n"), err: io.ErrUnexpectedEOF}
	parser := worker.NewLineParser(worker.NewBufferedReader(reader, 4096))

	logs, err := parser.Parse(context.Background(), 0)
	require.NoError(t, err)

	records, errs, exhausted := drain(t, logs)

	require.False(t, exhausted, "the parser must stop rather than spin")
	require.Equal(t, []any{"first", "second"}, records, "records read before the cut are still delivered")
	require.Len(t, errs, 1)

	require.False(t, worker.IsStreamRead(errs[0]),
		"a truncated object is not a broken connection, so it must not be retried")
	require.True(t, worker.IsTruncatedObject(errs[0]),
		"a truncated object must be marked so the caller can route it")
}
