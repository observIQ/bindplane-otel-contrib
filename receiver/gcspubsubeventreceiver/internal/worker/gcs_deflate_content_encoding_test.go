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
	"compress/flate"
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

// fakeGCSDeflate serves an object stored raw-DEFLATE-encoded. GCS only decompressively
// transcodes gzip, so a deflate object is passed through untouched: the response carries
// Content-Encoding: deflate, the client does not decompress it, and Attrs.ContentEncoding
// is reported as "deflate" (a positive Size, no transcoding). This is the path that hits
// the worker's non-empty Content-Encoding branch and threads the label into blobstream,
// which decodes the raw DEFLATE body from the label alone.
func fakeGCSDeflate(t *testing.T, plain string) *storage.Client {
	t.Helper()

	var buf bytes.Buffer
	fw, err := flate.NewWriter(&buf, flate.DefaultCompression)
	require.NoError(t, err)
	_, err = fw.Write([]byte(plain))
	require.NoError(t, err)
	require.NoError(t, fw.Close())
	body := buf.Bytes()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("alt") != "media" && strings.Contains(r.URL.Path, "/storage/v1/") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"name":"myobject","bucket":"mybucket","contentType":"text/plain","contentEncoding":"deflate","size":"`+strconv.Itoa(len(body))+`"}`)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Encoding", "deflate")
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

// TestProcessMessage_DeflateContentEncodingDeliversAndAcks asserts that an object served
// with a non-empty Content-Encoding (deflate, which GCS does not transcode) is read via
// its Content-Encoding label, its decoded lines are delivered, and the object is acked.
// This exercises the worker's `reader.Attrs.ContentEncoding != ""` branch, which is not
// reached by gzip objects (GCS transcodes and strips their label) or unlabeled objects.
func TestProcessMessage_DeflateContentEncodingDeliversAndAcks(t *testing.T) {
	plain := strings.Repeat("deflate log line padding padding padding padding\n", 100)
	client := fakeGCSDeflate(t, plain)
	h := newGCSHarness(t, finalizeAttrs(), 1000, newMemStorage(), client, func() {}, 0)

	require.True(t, h.process(context.Background()), "a complete deflate object is acked")

	delivered := h.bodyStrings()
	require.Equal(t, 100, len(delivered), "every decoded line is delivered")
	for _, line := range delivered {
		require.Equal(t, "deflate log line padding padding padding padding", line)
	}
}
