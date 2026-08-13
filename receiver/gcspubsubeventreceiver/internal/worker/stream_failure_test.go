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

package worker_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cloud.google.com/go/storage"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
)

// fakeGCSBrokenStream serves an object's metadata normally, then on the media read
// declares more bytes than it sends and drops the connection part way through. The
// client's read of the body fails rather than ending cleanly, which surfaces a raw
// read error the worker classifies as a broken source stream (a transient failure).
func fakeGCSBrokenStream(t *testing.T, head string) *storage.Client {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("alt") != "media" && strings.Contains(r.URL.Path, "/storage/v1/") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"name":"myobject","bucket":"mybucket","contentType":"text/plain"}`)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Errorf("test ResponseWriter does not support hijacking")
			return
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		// Promise 64 more bytes than we deliver, then drop the connection, so the
		// client sees a read error mid-body rather than a clean short read.
		fmt.Fprintf(buf, "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: %d\r\n\r\n", len(head)+64)
		_, _ = buf.WriteString(head)
		_ = buf.Flush()
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

// TestProcessMessage_BacksOffWhenObjectStreamBreaks asserts that an interrupted
// download (a broken source stream) preserves the message for retry by letting the
// ack deadline lapse, rather than nacking it for immediate redelivery. Immediate
// redelivery would spin against a persistently broken stream; the awss3 receiver
// backs off via its visibility timeout, and this receiver must match. Cancellation
// and DLQ conditions still nack for immediate redelivery.
func TestProcessMessage_BacksOffWhenObjectStreamBreaks(t *testing.T) {
	head, _ := objectLines(0, 250) // exceeds the content-detection window
	client := fakeGCSBrokenStream(t, head)
	h := newGCSHarness(t, finalizeAttrs(), 1000, newMemStorage(), client, func() {}, 0)

	h.process(context.Background())

	require.False(t, h.pubsub.nacked(),
		"a broken object stream must let the ack deadline lapse for backoff, not nack for immediate redelivery")
	require.Zero(t, h.pubsub.acks(),
		"a broken object stream must not ack the message")
}
