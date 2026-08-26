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
	"strconv"
	"testing"

	"cloud.google.com/go/storage"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.uber.org/zap"
	"google.golang.org/api/option"

	"github.com/observiq/bindplane-otel-contrib/internal/blobstream"
	"github.com/observiq/bindplane-otel-contrib/internal/storageclient"
)

// seededStore returns seed for every load, so a test can stage a saved offset.
type seededStore struct{ seed *blobstream.Offset }

func (s seededStore) SaveStorageData(context.Context, string, storageclient.StorageData) error {
	return nil
}
func (s seededStore) LoadStorageData(_ context.Context, _ string, data storageclient.StorageData) error {
	b, err := s.seed.Marshal()
	if err != nil {
		return err
	}
	return data.Unmarshal(b)
}
func (s seededStore) DeleteStorageData(context.Context, string) error { return nil }
func (s seededStore) Close(context.Context) error                     { return nil }

// gcsClientGen serves body as a plain-text object whose generation is reported via the
// X-Goog-Generation header, so reader.Attrs.Generation is populated.
func gcsClientGen(t *testing.T, body string, generation int64) *storage.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("X-Goog-Generation", strconv.FormatInt(generation, 10))
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	client, err := storage.NewClient(context.Background(),
		option.WithEndpoint(srv.URL), option.WithoutAuthentication())
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// readWithSeededGen reads a 3-line object of the given generation after seeding an offset
// (at the start of the third line) tagged with storedVersion, and returns how many records
// reached the consumer.
func readWithSeededGen(t *testing.T, generation int64, storedVersion string) int {
	t.Helper()

	const body = "AAAA\nBBBB\nCCCC\n" // three 5-byte lines; byte 10 starts the third
	sink := new(consumertest.LogsSink)
	w := &Worker{
		storageClient:  gcsClientGen(t, body, generation),
		nextConsumer:   sink,
		offsetStorage:  seededStore{seed: &blobstream.Offset{Offset: 10, Version: storedVersion}},
		obsrecv:        newTestObsReport(t),
		metrics:        gcsTestMetrics(t),
		maxLogSize:     4096,
		maxLogsEmitted: 1000,
	}

	require.NoError(t, w.consumeLogsFromGCSObject(context.Background(), "mybucket", "myobject", false, zap.NewNop()))
	return sink.LogRecordCount()
}

// TestConsumeGCS_IgnoresOffsetFromDifferentObjectVersion asserts a saved offset is applied
// only when its version matches the object generation being read. An object can be deleted
// and a different one created under the same name; the stale offset must not make the new
// object resume partway and skip its head.
func TestConsumeGCS_IgnoresOffsetFromDifferentObjectVersion(t *testing.T) {
	t.Parallel()

	// Same generation: the offset is honored, so the read resumes at the seeded position.
	require.Less(t, readWithSeededGen(t, 1001, "1001"), 3, "a matching-version offset resumes partway")

	// Different generation (a replacement object under the same name): the offset is
	// discarded and the whole object is read.
	require.Equal(t, 3, readWithSeededGen(t, 1002, "1001"),
		"a stale offset from a different object generation must be ignored and the object read from the start")
}
