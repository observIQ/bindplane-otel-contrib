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
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/observiq/bindplane-otel-contrib/internal/blobstream"
)

// TestConsume_ReturnedTruncatedIsAckedNotRetried asserts that an ErrTruncatedObject
// reported by Records() itself, before any record is yielded (for example an Avro OCF
// whose header is cut short), acks the object as truncated rather than failing it into a
// permanent retry loop. A retry reads the same truncated bytes, and the dead-letter queue
// would hold the same unrecoverable object, so neither is useful.
func TestConsume_ReturnedTruncatedIsAckedNotRetried(t *testing.T) {
	t.Parallel()

	fake := &fakeProducer{recordsErr: blobstream.ErrTruncatedObject{Err: io.ErrUnexpectedEOF}}
	w, record := newFakeProducerWorker(t, fake)

	truncated, err := w.consumeLogsFromS3Object(context.Background(), record, "mykey", false, zap.NewNop())
	require.NoError(t, err, "a truncated object reported before any record must ack, not retry")
	require.True(t, truncated, "it must be counted as a truncated object")
}
