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
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/linkedin/goavro/v2"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// largeHeaderAvro builds an Avro OCF whose schema carries a long doc, so the container
// header exceeds detectionPeekBytes. A read failure while decoding the header then lands
// in Parse's NewOCFReader rather than during content detection.
func largeHeaderAvro(t *testing.T) []byte {
	t.Helper()
	schema := `{"type":"record","name":"r","doc":"` + strings.Repeat("x", 5000) +
		`","fields":[{"name":"msg","type":"string"}]}`
	var buf bytes.Buffer
	w, err := goavro.NewOCFWriter(goavro.OCFConfig{W: &buf, Schema: schema})
	require.NoError(t, err)
	require.NoError(t, w.Append([]interface{}{map[string]interface{}{"msg": "hello"}}))
	return buf.Bytes()
}

// TestAvro_BrokenStreamInHeaderRetries asserts a connection failure while the Avro
// container header is still being read is reported as a broken stream (retry), not a
// corrupt container (DLQ). A transient blip must not permanently dead-letter the object.
func TestAvro_BrokenStreamInHeaderRetries(t *testing.T) {
	t.Parallel()

	body := largeHeaderAvro(t)
	require.Greater(t, len(body), detectionPeekBytes+256, "header must exceed the detection window")
	readErr := errors.New("read: connection reset by peer")

	// Cut inside the header but past the detection window, so detection succeeds and the
	// failure lands in NewOCFReader.
	stream := LogStream{
		Name:        "logs/object.avro",
		Body:        &cutAfter{data: body, n: detectionPeekBytes + 128, err: readErr},
		MaxLogSize:  testMaxLogSize,
		Logger:      zap.NewNop(),
		TryDecoding: true,
	}
	reader, err := stream.BufferedReader(context.Background())
	require.NoError(t, err)
	producer, err := NewRecordProducer(context.Background(), stream, reader, nil)
	require.NoError(t, err)
	_, err = producer.Records(context.Background(), Offset{})

	require.Error(t, err, "a broken stream mid-header must reach the caller")
	require.True(t, IsStreamRead(err), "a broken stream mid-header must retry")
	require.False(t, IsUnsupportedContent(err), "a broken stream is not a corrupt container")
	require.ErrorIs(t, err, readErr)
}
