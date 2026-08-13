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
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// failingPeekReader serves okPeeks peeks normally and then fails every later one. A
// real stream reaches this when the connection breaks between probes. The count lets a
// test place the break at a chosen detection stage.
type failingPeekReader struct {
	BufferedReader
	peekErr error
	okPeeks int
	seen    int
	// rawReadErr, when set, is reported by RawReadErr once a peek has failed, modeling
	// a break in the raw source (a real connection reset surfaces there, not only from
	// Peek). Left nil, a peek failure looks like a decode error rather than a broken
	// stream.
	rawReadErr error
}

func (r *failingPeekReader) Peek(n int) ([]byte, error) {
	r.seen++
	if r.seen > r.okPeeks {
		return nil, r.peekErr
	}
	return r.BufferedReader.Peek(n)
}

func (r *failingPeekReader) RawReadErr() error {
	if r.rawReadErr != nil && r.seen > r.okPeeks {
		return r.rawReadErr
	}
	return r.BufferedReader.RawReadErr()
}

// detectionStream builds a stream whose reader is supplied by the caller, so detection
// can be driven against a reader that fails.
func detectionStream(body string, logger *zap.Logger, tryDecoding bool) LogStream {
	return LogStream{
		Name:        "logs/object",
		Body:        io.NopCloser(strings.NewReader(body)),
		MaxLogSize:  testMaxLogSize,
		Logger:      logger,
		TryDecoding: tryDecoding,
	}
}

// collectStream runs a producer to exhaustion and returns each rendered body.
func collectStream(t *testing.T, stream LogStream, reader BufferedReader) ([]string, error) {
	t.Helper()
	ctx := context.Background()

	producer, err := NewRecordProducer(ctx, stream, reader, nil)
	if err != nil {
		return nil, err
	}

	seq, err := producer.Records(ctx, Offset{})
	if err != nil {
		return nil, err
	}

	var bodies []string
	for rec, rerr := range seq {
		if rerr != nil {
			return bodies, rerr
		}
		lr := plog.NewLogRecord()
		require.NoError(t, producer.AppendLogBody(ctx, lr, rec))
		bodies = append(bodies, lr.Body().AsString())
	}
	return bodies, nil
}

// TestDetection_FailsWhenTheArchiveProbeCannotRead asserts a read error during archive
// detection fails the object. Nothing has been read yet, so the object is redelivered
// rather than partly emitted.
func TestDetection_FailsWhenTheArchiveProbeCannotRead(t *testing.T) {
	t.Parallel()

	readErr := errors.New("connection reset by peer")
	stream := detectionStream("unused\n", zap.NewNop(), true)

	reader, err := stream.BufferedReader(context.Background())
	require.NoError(t, err)

	_, err = collectStream(t, stream, &failingPeekReader{BufferedReader: reader, peekErr: readErr, rawReadErr: readErr})
	require.ErrorIs(t, err, readErr)
	require.True(t, IsStreamRead(err), "a broken source during detection is a stream read failure")
	require.False(t, IsUnsupportedContent(err), "a read error must stay retryable")
}

// TestDetection_SurvivesFailedFormatProbes asserts a break after the archive probe
// falls back to line parsing with a warning instead of failing the object. Format
// detection is best effort, so a failed probe must not decide the object is unreadable.
func TestDetection_SurvivesFailedFormatProbes(t *testing.T) {
	t.Parallel()

	core, logs := observer.New(zap.WarnLevel)
	stream := detectionStream("unused\n", zap.New(core), true)

	reader, err := stream.BufferedReader(context.Background())
	require.NoError(t, err)

	// The archive probe reads first. Everything after it fails.
	bodies, err := collectStream(t, stream, &failingPeekReader{
		BufferedReader: reader,
		peekErr:        errors.New("connection reset by peer"),
		okPeeks:        1,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"unused"}, bodies,
		"a failed format probe must still deliver the object as lines")

	require.Positive(t, logs.FilterMessageSnippet("avro").Len(), "the avro probe must warn")
	require.Positive(t, logs.FilterMessageSnippet("json").Len(), "the json probe must warn")
}

// TestDetection_SkippedWhenDecodingIsOff asserts TryDecoding false goes straight to
// line parsing. A receiver configured for plain text pays for no detection.
func TestDetection_SkippedWhenDecodingIsOff(t *testing.T) {
	t.Parallel()

	stream := detectionStream(`[{"host":"a"},{"host":"b"}]`, zap.NewNop(), false)

	reader, err := stream.BufferedReader(context.Background())
	require.NoError(t, err)

	bodies, err := collectStream(t, stream, reader)
	require.NoError(t, err)
	require.Equal(t, []string{`[{"host":"a"},{"host":"b"}]`}, bodies,
		"with detection off the JSON array stays one text line")
}

// TestDetection_EmptyObjectYieldsNothing asserts an object with no bytes is not an
// error. An empty object is normal, and failing it would redeliver it forever.
func TestDetection_EmptyObjectYieldsNothing(t *testing.T) {
	t.Parallel()

	stream := detectionStream("", zap.NewNop(), true)

	reader, err := stream.BufferedReader(context.Background())
	require.NoError(t, err)

	bodies, err := collectStream(t, stream, reader)
	require.NoError(t, err)
	require.Empty(t, bodies)
}

// TestDetection_RejectsBinaryContent asserts recognized non-text content fails with an
// error the caller routes to the dead-letter queue, rather than emitting garbled lines.
func TestDetection_RejectsBinaryContent(t *testing.T) {
	t.Parallel()

	png := []byte{
		0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	}
	stream := detectionStream(string(png), zap.NewNop(), true)

	reader, err := stream.BufferedReader(context.Background())
	require.NoError(t, err)

	_, err = collectStream(t, stream, reader)
	require.Error(t, err)
	require.True(t, IsUnsupportedContent(err), "binary content must route to the dead-letter queue")

	var unsupported ErrUnsupportedContent
	require.ErrorAs(t, err, &unsupported)
	require.Equal(t, "image/png", unsupported.MIMEType)
}
