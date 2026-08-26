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
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"cloud.google.com/go/storage"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
)

// fakeGCSTranscodedGzip serves an object stored gzip-encoded. The client requests it and,
// because the response is Content-Encoding: gzip, decompressively transcodes it: the
// worker sees the decompressed bytes, a stripped content-encoding, and Attrs.Size == -1
// (the decompressed length is unknown). When truncate is set, only a prefix of the gzip
// stream is served, so the transcoding decoder fails mid-body with an unexpected EOF,
// modelling a download that broke.
func fakeGCSTranscodedGzip(t *testing.T, plain string, truncate bool) *storage.Client {
	t.Helper()

	var gz bytes.Buffer
	gw := gzip.NewWriter(&gz)
	_, err := gw.Write([]byte(plain))
	require.NoError(t, err)
	require.NoError(t, gw.Close())
	body := gz.Bytes()
	if truncate {
		body = body[:len(body)*6/10]
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("alt") != "media" && strings.Contains(r.URL.Path, "/storage/v1/") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"name":"myobject","bucket":"mybucket","contentType":"text/plain","contentEncoding":"gzip","size":"`+strconv.Itoa(len(body))+`"}`)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Encoding", "gzip")
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

// TestProcessMessage_TranscodedGzipDeliversAndAcks asserts a gzip object that GCS
// decompressively transcodes (Attrs.Size == -1) is read from its decompressed bytes and
// acked. The unknown size must not be mistaken for a truncation.
func TestProcessMessage_TranscodedGzipDeliversAndAcks(t *testing.T) {
	plain := strings.Repeat("transcoded gzip log line padding padding padding\n", 100)
	client := fakeGCSTranscodedGzip(t, plain, false)
	h := newGCSHarness(t, finalizeAttrs(), 1000, newMemStorage(), client, func() {}, 0)

	require.True(t, h.process(context.Background()), "a complete transcoded-gzip object is acked")
	require.Equal(t, 100, len(h.bodyStrings()), "every decompressed line is delivered")
}

// TestProcessMessage_TranscodedStoredTruncationIsNotSilentlyAcked documents the
// undetectable residual: an object GCS transcodes that was STORED truncated (a complete
// transfer of an incomplete gzip stream). With Attrs.Size == -1 there is no size to check
// and the transfer itself did not fail, so the truncation cannot be told from a complete
// object. The safe invariant is that such an object is never silently acked as complete;
// it is failed by content and routed for retry/DLQ instead.
func TestProcessMessage_TranscodedStoredTruncationIsNotSilentlyAcked(t *testing.T) {
	plain := strings.Repeat("transcoded gzip log line padding padding padding\n", 100)
	client := fakeGCSTranscodedGzip(t, plain, true)
	h := newGCSHarness(t, finalizeAttrs(), 1000, newMemStorage(), client, func() {}, 0)

	h.process(context.Background())

	require.Zero(t, h.pubsub.acks(), "a stored-truncated transcoded object must not be acked as complete")
}
