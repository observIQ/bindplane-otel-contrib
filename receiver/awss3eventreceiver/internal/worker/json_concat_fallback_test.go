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
	"go.uber.org/zap/zaptest/observer"

	"github.com/observiq/bindplane-otel-contrib/internal/aws/client/mocks"
)

// TestProcessRecord_ConcatTrailingNonWrapperIsNotReReadAsLines asserts that a records
// wrapper followed by a concatenated document that is not itself a records wrapper is
// handled as a JSON object with one skipped trailing document, NOT re-read from the start
// with the line parser. The line-parse fallback fires only for a returned
// ErrNotArrayOrKnownObject; a trailing non-wrapper must therefore surface a different
// error, or the already-delivered record is re-emitted as a raw line and the checkpoint
// is garbled.
func TestProcessRecord_ConcatTrailingNonWrapperIsNotReReadAsLines(t *testing.T) {
	t.Parallel()

	body := `{"Records":[{"n":"one"}]}{"not":"a wrapper"}`

	mockS3 := &mocks.MockS3Client{}
	mockClient := &mocks.MockClient{}
	mockClient.EXPECT().S3().Return(mockS3)
	mockS3.EXPECT().GetObject(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(
		func(_ context.Context, _ *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
			return &s3.GetObjectOutput{Body: io.NopCloser(strings.NewReader(body))}, nil
		})

	core, logs := observer.New(zap.DebugLevel)
	w := &Worker{
		client:         mockClient,
		nextConsumer:   consumertest.NewNop(),
		offsetStorage:  errStorage{},
		obsrecv:        newTestObsReport(t),
		metrics:        testMetrics(t),
		maxLogSize:     4096,
		maxLogsEmitted: 1000,
	}

	_, err := w.processRecord(context.Background(), s3RecordFor("mykey", int64(len(body))), "mykey", zap.New(core))
	require.NoError(t, err, "the trailing non-wrapper document is skipped and the object acks")
	require.Zero(t, logs.FilterMessage("parsing as JSON failed, trying again with line parsing").Len(),
		"a concatenated non-wrapper document must not re-read the whole object as raw lines")
}
