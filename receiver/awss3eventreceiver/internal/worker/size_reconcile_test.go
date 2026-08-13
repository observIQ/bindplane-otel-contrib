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
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.uber.org/zap"

	"github.com/observiq/bindplane-otel-contrib/internal/aws/client/mocks"
)

// TestConsume_SizeFromContentLengthNotStaleEvent asserts the object size used for
// truncation detection comes from the GetObject response (ContentLength), not the size
// carried in the SQS event. The event size is a snapshot from when the event was emitted;
// if the object was overwritten smaller before this download, a stale (larger) event size
// would make a complete download look truncated and force a permanent retry loop.
func TestConsume_SizeFromContentLengthNotStaleEvent(t *testing.T) {
	t.Parallel()

	body := "line1\nline2\nline3\n"
	contentLength := int64(len(body))

	mockS3 := &mocks.MockS3Client{}
	mockClient := &mocks.MockClient{}
	mockClient.EXPECT().S3().Return(mockS3)
	mockS3.EXPECT().GetObject(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(
		func(_ context.Context, _ *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
			return &s3.GetObjectOutput{
				Body:          io.NopCloser(strings.NewReader(body)),
				ContentLength: &contentLength,
			}, nil
		})

	w := &Worker{
		client:         mockClient,
		nextConsumer:   consumertest.NewNop(),
		offsetStorage:  errStorage{},
		obsrecv:        newTestObsReport(t),
		metrics:        testMetrics(t),
		maxLogSize:     4096,
		maxLogsEmitted: 1000,
	}

	// The SQS event reports a stale, larger size (object was overwritten smaller).
	staleEventSize := contentLength + 1000
	_, err := w.consumeLogsFromS3Object(context.Background(), s3RecordFor("mykey", staleEventSize), "mykey", false, zap.NewNop())
	require.NoError(t, err, "size must come from the GetObject ContentLength, not the stale SQS event size")
}
