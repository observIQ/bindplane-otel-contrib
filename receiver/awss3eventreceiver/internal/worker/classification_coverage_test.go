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
	"context"
	"errors"
	"io"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/observiq/bindplane-otel-contrib/internal/aws/client/mocks"
	"github.com/observiq/bindplane-otel-contrib/internal/blobstream"
	"github.com/observiq/bindplane-otel-contrib/internal/storageclient"
	"github.com/observiq/bindplane-otel-contrib/receiver/awss3eventreceiver/internal/metadata"
)

// loadErrStorage fails LoadStorageData so the offset-load error path runs.
type loadErrStorage struct{ err error }

func (s loadErrStorage) SaveStorageData(context.Context, string, storageclient.StorageData) error {
	return nil
}
func (s loadErrStorage) LoadStorageData(context.Context, string, storageclient.StorageData) error {
	return s.err
}
func (s loadErrStorage) DeleteStorageData(context.Context, string) error { return nil }
func (s loadErrStorage) Close(context.Context) error                     { return nil }

// errorMidReader serves head cleanly (enough for content detection to complete) and then
// returns err on the next read, modelling a source stream that breaks mid-object.
type errorMidReader struct {
	head []byte
	pos  int
	err  error
}

func (r *errorMidReader) Read(p []byte) (int, error) {
	if r.pos < len(r.head) {
		n := copy(p, r.head[r.pos:])
		r.pos += n
		return n, nil
	}
	return 0, r.err
}
func (r *errorMidReader) Close() error { return nil }

func testMetrics(t *testing.T) *metadata.TelemetryBuilder {
	t.Helper()
	tb, err := metadata.NewTelemetryBuilder(componenttest.NewNopTelemetrySettings())
	require.NoError(t, err)
	return tb
}

// s3RecordFor builds a minimal ObjectCreated record for the given key.
func s3RecordFor(key string, size int64) events.S3EventRecord {
	return events.S3EventRecord{
		AWSRegion: "us-west-2",
		EventTime: time.Unix(0, 0),
		S3: events.S3Entity{
			Bucket: events.S3Bucket{Name: "mybucket"},
			Object: events.S3Object{Key: key, Size: size},
		},
	}
}

// TestConsume_AppliesRegionOption asserts the request option that pins the S3 region to
// the record's region is wired onto the GetObject call.
func TestConsume_AppliesRegionOption(t *testing.T) {
	t.Parallel()

	body := "line1\nline2\n"
	var gotRegion string

	mockS3 := &mocks.MockS3Client{}
	mockClient := &mocks.MockClient{}
	mockClient.EXPECT().S3().Return(mockS3)
	mockS3.EXPECT().GetObject(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(
		func(_ context.Context, _ *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
			var o s3.Options
			for _, fn := range optFns {
				fn(&o)
			}
			gotRegion = o.Region
			return &s3.GetObjectOutput{Body: io.NopCloser(strings.NewReader(body))}, nil
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

	_, err := w.consumeLogsFromS3Object(context.Background(), s3RecordFor("mykey", int64(len(body))), "mykey", false, zap.NewNop())
	require.NoError(t, err)
	require.Equal(t, "us-west-2", gotRegion, "the record's region must be applied as a request option")
}

// TestConsume_LoadOffsetFailureFailsObject asserts a failure reading the saved offset
// fails the object rather than restarting it from the beginning.
func TestConsume_LoadOffsetFailureFailsObject(t *testing.T) {
	t.Parallel()

	mockS3 := &mocks.MockS3Client{}
	mockClient := &mocks.MockClient{}
	mockClient.EXPECT().S3().Return(mockS3)
	mockS3.EXPECT().GetObject(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(
		func(_ context.Context, _ *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
			return &s3.GetObjectOutput{Body: io.NopCloser(strings.NewReader("line1\n"))}, nil
		})

	loadErr := errors.New("storage extension unavailable")
	w := &Worker{
		client:         mockClient,
		nextConsumer:   consumertest.NewNop(),
		offsetStorage:  loadErrStorage{err: loadErr},
		obsrecv:        newTestObsReport(t),
		metrics:        testMetrics(t),
		maxLogSize:     4096,
		maxLogsEmitted: 1000,
	}

	_, err := w.consumeLogsFromS3Object(context.Background(), s3RecordFor("mykey", 6), "mykey", false, zap.NewNop())
	require.ErrorIs(t, err, loadErr)
	require.ErrorContains(t, err, "load offset")
}

// TestConsume_UnsupportedContentFailsAtParserCreation asserts an object whose content is
// a recognized-but-unparseable binary fails at parser creation (routing it to the DLQ),
// rather than being read as text.
func TestConsume_UnsupportedContentFailsAtParserCreation(t *testing.T) {
	t.Parallel()

	// A PNG signature: mimetype detects image/png, which has no log parser.
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 4000)...)

	mockS3 := &mocks.MockS3Client{}
	mockClient := &mocks.MockClient{}
	mockClient.EXPECT().S3().Return(mockS3)
	mockS3.EXPECT().GetObject(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(
		func(_ context.Context, _ *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
			return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(png))}, nil
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

	_, err := w.consumeLogsFromS3Object(context.Background(), s3RecordFor("image.png", int64(len(png))), "image.png", true, zap.NewNop())
	require.Error(t, err)
	require.ErrorContains(t, err, "create parser")
}

// TestConsume_BrokenStreamMidObjectFailsObject asserts a source stream that breaks after
// the content-detection window fails the object as a broken stream (so it redelivers and
// resumes from the saved offset) rather than acking a partial read.
func TestConsume_BrokenStreamMidObjectFailsObject(t *testing.T) {
	t.Parallel()

	// A clean head larger than the detection window, then a read failure.
	head := []byte(strings.Repeat("log-line-padding-padding-padding\n", 200))
	connReset := errors.New("connection reset by peer")

	mockS3 := &mocks.MockS3Client{}
	mockClient := &mocks.MockClient{}
	mockClient.EXPECT().S3().Return(mockS3)
	mockS3.EXPECT().GetObject(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(
		func(_ context.Context, _ *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
			return &s3.GetObjectOutput{Body: &errorMidReader{head: head, err: connReset}}, nil
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

	_, err := w.consumeLogsFromS3Object(context.Background(), s3RecordFor("mykey", 0), "mykey", false, zap.NewNop())
	require.Error(t, err)
	require.True(t, blobstream.IsStreamRead(err), "a mid-object read failure is a broken stream, got %v", err)
}

// TestConsume_MalformedRecordIsSkipped asserts a single malformed record is skipped (and
// counted) rather than failing the whole object, so the object is still acked.
func TestConsume_MalformedRecordIsSkipped(t *testing.T) {
	t.Parallel()

	// A JSON array whose only element is not valid JSON: the decoder reports a syntax
	// error, which is a per-record parse error rather than a stream or DLQ condition.
	// A valid array whose trailing element is not valid JSON: the decoder yields the
	// good elements and then reports a syntax error, a per-record parse error.
	body := `[{"a":1},{"a":2},bad]`
	core, logs := observer.New(zap.ErrorLevel)

	mockS3 := &mocks.MockS3Client{}
	mockClient := &mocks.MockClient{}
	mockClient.EXPECT().S3().Return(mockS3)
	mockS3.EXPECT().GetObject(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(
		func(_ context.Context, _ *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
			return &s3.GetObjectOutput{Body: io.NopCloser(strings.NewReader(body))}, nil
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

	_, err := w.consumeLogsFromS3Object(context.Background(), s3RecordFor("mykey", int64(len(body))), "mykey", true, zap.New(core))
	require.NoError(t, err, "a malformed record is skipped, not fatal to the object")
	require.Positive(t, logs.FilterMessage("parse log").Len(), "the skipped record must be logged")
}

// fakeProducer is an injected blobstream.RecordProducer that yields a scripted sequence,
// so the worker's per-record error-handling branches can be exercised deterministically
// without crafting object content that provokes each classification.
type fakeProducer struct {
	records    []any
	yieldErr   error
	appendErr  error
	recordsErr error
}

func (f *fakeProducer) Records(context.Context, blobstream.Offset) (iter.Seq2[any, error], error) {
	if f.recordsErr != nil {
		return nil, f.recordsErr
	}
	return func(yield func(any, error) bool) {
		for _, r := range f.records {
			if !yield(r, nil) {
				return
			}
		}
		if f.yieldErr != nil {
			yield(nil, f.yieldErr)
		}
	}, nil
}

func (f *fakeProducer) AppendLogBody(context.Context, plog.LogRecord, any) error {
	return f.appendErr
}

func (f *fakeProducer) Position() blobstream.Offset { return blobstream.Offset{} }

func newFakeProducerWorker(t *testing.T, fake *fakeProducer) (*Worker, events.S3EventRecord) {
	t.Helper()

	mockS3 := &mocks.MockS3Client{}
	mockClient := &mocks.MockClient{}
	mockClient.EXPECT().S3().Return(mockS3)
	mockS3.EXPECT().GetObject(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(
		func(_ context.Context, _ *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
			return &s3.GetObjectOutput{Body: io.NopCloser(strings.NewReader("placeholder\n"))}, nil
		})

	w := &Worker{
		client:         mockClient,
		nextConsumer:   consumertest.NewNop(),
		offsetStorage:  errStorage{},
		obsrecv:        newTestObsReport(t),
		metrics:        testMetrics(t),
		maxLogSize:     4096,
		maxLogsEmitted: 1000,
		newRecordProducer: func(context.Context, blobstream.LogStream, blobstream.BufferedReader, blobstream.ParseErrorFunc) (blobstream.RecordProducer, error) {
			return fake, nil
		},
	}
	return w, s3RecordFor("mykey", 12)
}

// TestConsume_DLQConditionFailsObject asserts a fatal DLQ-condition error yielded during
// iteration (an unsupported/corrupt content error) fails the whole object so it routes to
// the dead-letter queue rather than being partially acked.
func TestConsume_DLQConditionFailsObject(t *testing.T) {
	t.Parallel()

	w, record := newFakeProducerWorker(t, &fakeProducer{yieldErr: blobstream.ErrUnsupportedContent{MIMEType: "image/png"}})

	_, err := w.consumeLogsFromS3Object(context.Background(), record, "mykey", false, zap.NewNop())
	require.Error(t, err)
	require.True(t, blobstream.IsUnsupportedContent(err), "an unsupported-content error is a DLQ condition, got %v", err)
}

// TestConsume_AppendBodyFailureSkipsRecord asserts a record whose body cannot be appended
// is skipped and counted, rather than failing the whole object.
func TestConsume_AppendBodyFailureSkipsRecord(t *testing.T) {
	t.Parallel()

	core, logs := observer.New(zap.ErrorLevel)
	w, record := newFakeProducerWorker(t, &fakeProducer{records: []any{"rec"}, appendErr: errors.New("append boom")})

	_, err := w.consumeLogsFromS3Object(context.Background(), record, "mykey", false, zap.New(core))
	require.NoError(t, err, "an un-appendable record is skipped, not fatal to the object")
	require.Positive(t, logs.FilterMessage("append log body").Len(), "the skipped record must be logged")
}
