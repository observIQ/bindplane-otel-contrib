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
	"errors"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"cloud.google.com/go/storage"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/api/option"

	"github.com/observiq/bindplane-otel-contrib/internal/blobstream"
	"github.com/observiq/bindplane-otel-contrib/internal/storageclient"
	"github.com/observiq/bindplane-otel-contrib/receiver/gcspubsubeventreceiver/internal/metadata"
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

// gcsClient builds a storage client backed by an httptest server. metaJSON is returned
// for object-metadata lookups; body is returned for media reads. When notFound is set,
// every request 404s so the object read fails.
func gcsClient(t *testing.T, metaJSON string, body []byte, notFound bool) *storage.Client {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if notFound {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("alt") != "media" && strings.Contains(r.URL.Path, "/storage/v1/") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, metaJSON)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	client, err := storage.NewClient(context.Background(),
		option.WithEndpoint(srv.URL),
		option.WithoutAuthentication(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

const plainMeta = `{"name":"myobject","bucket":"mybucket","contentType":"text/plain"}`

func gcsTestMetrics(t *testing.T) *metadata.TelemetryBuilder {
	t.Helper()
	tb, err := metadata.NewTelemetryBuilder(componenttest.NewNopTelemetrySettings())
	require.NoError(t, err)
	return tb
}

func newGCSConsumeWorker(t *testing.T, client *storage.Client, store storageclient.StorageClient) *Worker {
	t.Helper()
	if store == nil {
		store = errStorage{}
	}
	return &Worker{
		storageClient:  client,
		nextConsumer:   consumertest.NewNop(),
		offsetStorage:  store,
		obsrecv:        newTestObsReport(t),
		metrics:        gcsTestMetrics(t),
		maxLogSize:     4096,
		maxLogsEmitted: 1000,
	}
}

// TestProcessRecord_RetriesWithLineParsingWhenNotJSON asserts an object that is valid
// JSON but not an array or known-records object is retried as line-delimited text rather
// than failing, so a plain JSON document is still ingested.
func TestProcessRecord_RetriesWithLineParsingWhenNotJSON(t *testing.T) {
	t.Parallel()

	body := []byte(`{"foo":"bar"}`)
	w := newGCSConsumeWorker(t, gcsClient(t, plainMeta, body, false), nil)

	_, err := w.processRecord(context.Background(), "mybucket", "myobject", zap.NewNop())
	require.NoError(t, err, "a non-array JSON object falls back to line parsing")
}

// TestConsumeGCS_NewReaderFailureFailsObject asserts a missing object fails at reader
// creation rather than being treated as empty.
func TestConsumeGCS_NewReaderFailureFailsObject(t *testing.T) {
	t.Parallel()

	w := newGCSConsumeWorker(t, gcsClient(t, "", nil, true), nil)

	_, err := w.consumeLogsFromGCSObject(context.Background(), "mybucket", "myobject", false, zap.NewNop())
	require.Error(t, err)
	require.ErrorContains(t, err, "get object")
}

// TestConsumeGCS_LoadOffsetFailureFailsObject asserts a failure reading the saved offset
// fails the object rather than restarting from the beginning.
func TestConsumeGCS_LoadOffsetFailureFailsObject(t *testing.T) {
	t.Parallel()

	loadErr := errors.New("storage extension unavailable")
	w := newGCSConsumeWorker(t, gcsClient(t, plainMeta, []byte("line1\n"), false), loadErrStorage{err: loadErr})

	_, err := w.consumeLogsFromGCSObject(context.Background(), "mybucket", "myobject", false, zap.NewNop())
	require.ErrorIs(t, err, loadErr)
	require.ErrorContains(t, err, "load offset")
}

// TestConsumeGCS_UnsupportedContentFailsAtParserCreation asserts a recognized-but-
// unparseable binary fails at parser creation (routing it to the DLQ).
func TestConsumeGCS_UnsupportedContentFailsAtParserCreation(t *testing.T) {
	t.Parallel()

	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 4000)...)
	w := newGCSConsumeWorker(t, gcsClient(t, plainMeta, png, false), nil)

	_, err := w.consumeLogsFromGCSObject(context.Background(), "mybucket", "myobject", true, zap.NewNop())
	require.Error(t, err)
	require.ErrorContains(t, err, "create parser")
}

// TestConsumeGCS_MalformedRecordIsSkipped asserts a single malformed record is skipped
// (and counted) rather than failing the whole object.
func TestConsumeGCS_MalformedRecordIsSkipped(t *testing.T) {
	t.Parallel()

	body := []byte(`[{"a":1},{"a":2},bad]`)
	core, logs := observer.New(zap.ErrorLevel)
	w := newGCSConsumeWorker(t, gcsClient(t, plainMeta, body, false), nil)

	_, err := w.consumeLogsFromGCSObject(context.Background(), "mybucket", "myobject", true, zap.New(core))
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

func newFakeProducerWorkerGCS(t *testing.T, fake *fakeProducer) *Worker {
	t.Helper()
	w := newGCSConsumeWorker(t, gcsClient(t, plainMeta, []byte("placeholder\n"), false), nil)
	w.newRecordProducer = func(context.Context, blobstream.LogStream, blobstream.BufferedReader, blobstream.ParseErrorFunc) (blobstream.RecordProducer, error) {
		return fake, nil
	}
	return w
}

// TestConsumeGCS_DLQConditionFailsObject asserts a fatal DLQ-condition error yielded
// during iteration fails the whole object so it routes to the dead-letter queue.
func TestConsumeGCS_DLQConditionFailsObject(t *testing.T) {
	t.Parallel()

	w := newFakeProducerWorkerGCS(t, &fakeProducer{yieldErr: blobstream.ErrUnsupportedContent{MIMEType: "image/png"}})

	_, err := w.consumeLogsFromGCSObject(context.Background(), "mybucket", "myobject", false, zap.NewNop())
	require.Error(t, err)
	require.True(t, blobstream.IsUnsupportedContent(err), "an unsupported-content error is a DLQ condition, got %v", err)
}

// TestConsumeGCS_AppendBodyFailureSkipsRecord asserts a record whose body cannot be
// appended is skipped and counted, rather than failing the whole object.
func TestConsumeGCS_AppendBodyFailureSkipsRecord(t *testing.T) {
	t.Parallel()

	core, logs := observer.New(zap.ErrorLevel)
	w := newFakeProducerWorkerGCS(t, &fakeProducer{records: []any{"rec"}, appendErr: errors.New("append boom")})

	_, err := w.consumeLogsFromGCSObject(context.Background(), "mybucket", "myobject", false, zap.New(core))
	require.NoError(t, err, "an un-appendable record is skipped, not fatal to the object")
	require.Positive(t, logs.FilterMessage("append log body").Len(), "the skipped record must be logged")
}

// TestConsumeGCS_StreamReaderConstructionFailureFailsObject asserts that when the
// object's content sniffs as a compression format whose decompression reader cannot be
// built (here a gzip magic followed by an invalid header), the object fails at stream-
// reader construction rather than being read as raw text.
func TestConsumeGCS_StreamReaderConstructionFailureFailsObject(t *testing.T) {
	t.Parallel()

	// A gzip magic number followed by bytes that are not a valid gzip header, so
	// content detection selects gzip but gzip.NewReader fails to construct.
	body := append([]byte{0x1f, 0x8b}, []byte("not-a-valid-gzip-header")...)
	w := newGCSConsumeWorker(t, gcsClient(t, plainMeta, body, false), nil)

	_, err := w.consumeLogsFromGCSObject(context.Background(), "mybucket", "myobject", true, zap.NewNop())
	require.Error(t, err)
	require.ErrorContains(t, err, "get stream reader")
}
