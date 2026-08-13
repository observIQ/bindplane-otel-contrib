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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cloud.google.com/go/storage"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/api/option"
)

// TestConsumeGCS_ContentEncodingAttr covers the branch that copies a non-empty object
// Content-Encoding into the stream. The fake serves a Content-Encoding the storage
// client preserves (it only auto-decodes gzip) over a plain body, so the attribute is
// non-empty while the content still parses as text.
func TestConsumeGCS_ContentEncodingAttr(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("alt") != "media" && strings.Contains(r.URL.Path, "/storage/v1/") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"name":"myobject","bucket":"mybucket","contentType":"text/plain","contentEncoding":"br"}`)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Encoding", "br")
		_, _ = io.WriteString(w, "line1\nline2\n")
	}))
	t.Cleanup(srv.Close)

	client, err := storage.NewClient(context.Background(), option.WithEndpoint(srv.URL), option.WithoutAuthentication())
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	w := newGCSConsumeWorker(t, client, nil)
	_, err = w.consumeLogsFromGCSObject(context.Background(), "mybucket", "myobject", false, zap.NewNop())
	require.NoError(t, err)
}
