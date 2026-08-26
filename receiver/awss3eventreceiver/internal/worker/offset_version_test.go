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

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.uber.org/zap"

	"github.com/observiq/bindplane-otel-contrib/internal/aws/client/mocks"
	"github.com/observiq/bindplane-otel-contrib/internal/blobstream"
	"github.com/observiq/bindplane-otel-contrib/internal/storageclient"
)

// seededStore returns seed for every load, so a test can stage a saved offset.
type seededStore struct{ seed *blobstream.Offset }

func (s seededStore) SaveStorageData(context.Context, string, storageclient.StorageData) error {
	return nil
}
func (s seededStore) LoadStorageData(_ context.Context, _ string, data storageclient.StorageData) error {
	b, err := s.seed.Marshal()
	if err != nil {
		return err
	}
	return data.Unmarshal(b)
}
func (s seededStore) DeleteStorageData(context.Context, string) error { return nil }
func (s seededStore) Close(context.Context) error                     { return nil }

// readWithSeededOffset reads a 3-line object whose ETag is objectETag after seeding an
// offset (at the start of the third line) tagged with storedVersion, and returns how many
// records reached the consumer.
func readWithSeededOffset(t *testing.T, objectETag, storedVersion string) int {
	t.Helper()

	const body = "AAAA\nBBBB\nCCCC\n" // three 5-byte lines; byte 10 starts the third
	mockS3 := &mocks.MockS3Client{}
	mockClient := &mocks.MockClient{}
	mockClient.EXPECT().S3().Return(mockS3)
	mockS3.EXPECT().GetObject(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(
		func(_ context.Context, _ *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
			return &s3.GetObjectOutput{Body: io.NopCloser(strings.NewReader(body)), ETag: aws.String(objectETag)}, nil
		})

	sink := new(consumertest.LogsSink)
	w := &Worker{
		client:         mockClient,
		nextConsumer:   sink,
		offsetStorage:  seededStore{seed: &blobstream.Offset{Offset: 10, Version: storedVersion}},
		obsrecv:        newTestObsReport(t),
		metrics:        testMetrics(t),
		maxLogSize:     4096,
		maxLogsEmitted: 1000,
	}

	_, err := w.consumeLogsFromS3Object(context.Background(),
		s3RecordFor("mykey", int64(len(body))), "mykey", false, zap.NewNop())
	require.NoError(t, err)
	return sink.LogRecordCount()
}

// TestConsume_IgnoresOffsetFromDifferentObjectVersion asserts a saved offset is applied
// only when its version matches the object being read. An object can be deleted and a
// different one created under the same name; the stale offset must not make the new object
// resume partway and skip its head.
func TestConsume_IgnoresOffsetFromDifferentObjectVersion(t *testing.T) {
	t.Parallel()

	// Same version: the offset is honored, so the read resumes at the seeded position.
	require.Less(t, readWithSeededOffset(t, "etag-A", "etag-A"), 3,
		"a matching-version offset resumes partway")

	// Different version (a replacement object under the same name): the offset is
	// discarded and the whole object is read.
	require.Equal(t, 3, readWithSeededOffset(t, "etag-B", "etag-A"),
		"a stale offset from a different object version must be ignored and the object read from the start")

	// Empty version (a backend that omits ETag): ownership can't be confirmed, so even a
	// same-empty stored offset is discarded rather than honored.
	require.Equal(t, 3, readWithSeededOffset(t, "", ""),
		"an empty/unknown version must not honor a stored offset")
}
