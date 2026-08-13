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

// TestAvro_ShortDownloadInHeaderRetries asserts an Avro download that ends short of the
// object's known size while the header is still being read is reported as a broken stream
// (retry), not a truncated object that acks zero records. It mirrors the scan-end
// short-read classification on the header path.
func TestAvro_ShortDownloadInHeaderRetries(t *testing.T) {
	t.Parallel()

	body := largeHeaderAvro(t)
	require.Greater(t, len(body), detectionPeekBytes+256, "header must exceed the detection window")

	// Clean EOF partway through the header, but the known size says more should follow.
	stream := LogStream{
		Name:        "logs/object.avro",
		Body:        &cutAfter{data: body, n: detectionPeekBytes + 128},
		MaxLogSize:  testMaxLogSize,
		Logger:      zap.NewNop(),
		TryDecoding: true,
		Size:        int64(len(body)),
	}
	reader, err := stream.BufferedReader(context.Background())
	require.NoError(t, err)
	producer, err := NewRecordProducer(context.Background(), stream, reader, nil)
	require.NoError(t, err)
	_, err = producer.Records(context.Background(), Offset{})

	require.Error(t, err, "a short download mid-header must reach the caller")
	require.True(t, IsStreamRead(err), "a short download mid-header must retry")
	require.False(t, IsTruncatedObject(err), "a short download is not a stored truncation")
}
