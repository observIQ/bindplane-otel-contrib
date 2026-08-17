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
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// cutAfter serves the first n bytes of data and then fails every read.
type cutAfter struct {
	data []byte
	n    int
	pos  int
	err  error
}

func (r *cutAfter) Read(p []byte) (int, error) {
	if r.pos >= r.n {
		if r.err != nil {
			return 0, r.err
		}
		return 0, io.EOF
	}
	end := min(r.pos+len(p), r.n)
	c := copy(p, r.data[r.pos:end])
	r.pos += c
	return c, nil
}

func (r *cutAfter) Close() error { return nil }

// multiBlockAvro builds an Avro object large enough to hold several blocks, so a read
// can break part way through rather than during detection.
func multiBlockAvro(t *testing.T) []byte {
	t.Helper()

	msgs := make([]string, 0, 4000)
	for i := 0; i < 4000; i++ {
		msgs = append(msgs, fmt.Sprintf("record-%04d-padding-padding-padding", i))
	}
	return avroOcfBytes(t, msgs)
}

// driveAvro reads an Avro object and returns the record count and the final error.
func driveAvro(t *testing.T, body []byte, cut int, readErr error) (int, error) {
	t.Helper()
	ctx := context.Background()

	stream := LogStream{
		Name:        "logs/object.avro",
		Body:        &cutAfter{data: body, n: cut, err: readErr},
		MaxLogSize:  testMaxLogSize,
		Logger:      zap.NewNop(),
		TryDecoding: true,
	}
	reader, err := stream.BufferedReader(ctx)
	require.NoError(t, err)

	producer, err := NewRecordProducer(ctx, stream, reader, nil)
	require.NoError(t, err)

	seq, err := producer.Records(ctx, Offset{})
	require.NoError(t, err)

	var records int
	var last error
	for _, rerr := range seq {
		if rerr != nil {
			last = rerr
			continue
		}
		records++
	}
	return records, last
}

// TestAvro_FailsTheObjectOnABrokenStream asserts a read failure part way through an
// Avro object is reported. Ending quietly makes the worker ack a partial read and drop
// every record after the break.
func TestAvro_FailsTheObjectOnABrokenStream(t *testing.T) {
	t.Parallel()

	body := multiBlockAvro(t)
	readErr := errors.New("read: connection reset by peer")

	records, err := driveAvro(t, body, len(body)/2, readErr)

	require.Positive(t, records, "records read before the break are still delivered")
	require.Error(t, err, "a broken stream must reach the caller")
	require.True(t, IsStreamRead(err), "a broken stream must be marked for retry")
	require.ErrorIs(t, err, readErr)
	require.False(t, IsUnsupportedContent(err), "a broken stream is retryable")
}

// TestAvro_DeadLettersACorruptContainer asserts an object whose structure will not
// decode routes to the dead-letter queue. A retry reads the same bytes, so requeuing it
// would redeliver it forever.
func TestAvro_DeadLettersACorruptContainer(t *testing.T) {
	t.Parallel()

	body := multiBlockAvro(t)
	// Break a sync marker part way in, which ends the scan without any read failing.
	corrupt := append([]byte{}, body...)
	for i := len(corrupt) / 2; i < len(corrupt)/2+16; i++ {
		corrupt[i] ^= 0xff
	}

	_, err := driveAvro(t, corrupt, len(corrupt), nil)

	require.Error(t, err, "a corrupt container must reach the caller")
	require.True(t, IsUnsupportedContent(err), "a corrupt container belongs in the dead-letter queue")
	require.False(t, IsStreamRead(err), "a corrupt container is not retryable")
}

// TestAvro_CleanObjectReportsNoError asserts the check adds no error to a healthy read.
func TestAvro_CleanObjectReportsNoError(t *testing.T) {
	t.Parallel()

	body := multiBlockAvro(t)

	records, err := driveAvro(t, body, len(body), nil)

	require.NoError(t, err)
	require.Equal(t, 4000, records)
}

// TestAvro_TruncatedObjectRoutesToDLQ asserts an Avro object that simply runs out of
// bytes reports the truncation. Ending quietly makes the worker ack the object and drop
// every record after the cut, which is the same loss the text and JSON paths avoid.
func TestAvro_TruncatedObjectRoutesToDLQ(t *testing.T) {
	t.Parallel()

	body := multiBlockAvro(t)

	// No transport error: the reader simply reaches the end early.
	records, err := driveAvro(t, body, len(body)/2, nil)

	require.Positive(t, records, "records before the cut are still delivered")
	require.Error(t, err, "a truncated object must reach the caller")
	require.True(t, IsTruncatedObject(err))
	require.False(t, IsStreamRead(err), "truncation is not a broken connection")
	require.True(t, IsUnsupportedContent(err), "it must route to the dead-letter queue")
}
