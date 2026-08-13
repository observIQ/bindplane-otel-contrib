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
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"cloud.google.com/go/storage"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.uber.org/zap"
	"google.golang.org/api/option"
)

// fakeGCSCompleteBody serves object metadata and then the full object body, so the worker
// reads the object cleanly. It is the success-path counterpart to fakeGCSBrokenStream.
func fakeGCSCompleteBody(t *testing.T, body string) *storage.Client {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("alt") != "media" && strings.Contains(r.URL.Path, "/storage/v1/") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"name":"myobject","bucket":"mybucket","contentType":"text/plain"}`)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = io.WriteString(w, body)
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

func newGCSFlushFailWorker(t *testing.T, body string, maxLogsEmitted int, consumeErr error) *Worker {
	t.Helper()
	return &Worker{
		storageClient:  fakeGCSCompleteBody(t, body),
		nextConsumer:   consumertest.NewErr(consumeErr),
		offsetStorage:  errStorage{},
		obsrecv:        newTestObsReport(t),
		maxLogSize:     4096,
		maxLogsEmitted: maxLogsEmitted,
	}
}

// TestConsumeGCS_TrailingFlushFailureFailsObject asserts that when the final batch is
// rejected by the next consumer, consumeLogsFromGCSObject returns the error so the object
// is not acked (the message is preserved for retry).
func TestConsumeGCS_TrailingFlushFailureFailsObject(t *testing.T) {
	t.Parallel()

	consumeErr := errors.New("pipeline backpressure")
	// Fewer records than maxLogsEmitted, so only the trailing flush runs.
	w := newGCSFlushFailWorker(t, "line1\nline2\n", 1000, consumeErr)

	_, err := w.consumeLogsFromGCSObject(context.Background(), "mybucket", "myobject", false, zap.NewNop())
	require.ErrorIs(t, err, consumeErr)
	require.ErrorContains(t, err, "consume logs")
}

// TestConsumeGCS_MidLoopFlushFailureFailsObject asserts that when a mid-object batch (the
// one emitted once maxLogsEmitted is reached) is rejected, consumeLogsFromGCSObject
// returns the error immediately rather than continuing to read.
func TestConsumeGCS_MidLoopFlushFailureFailsObject(t *testing.T) {
	t.Parallel()

	consumeErr := errors.New("pipeline backpressure")
	// maxLogsEmitted of 1 forces a flush after the first record, mid-loop.
	w := newGCSFlushFailWorker(t, "line1\nline2\nline3\n", 1, consumeErr)

	_, err := w.consumeLogsFromGCSObject(context.Background(), "mybucket", "myobject", false, zap.NewNop())
	require.ErrorIs(t, err, consumeErr)
	require.ErrorContains(t, err, "consume logs")
}
