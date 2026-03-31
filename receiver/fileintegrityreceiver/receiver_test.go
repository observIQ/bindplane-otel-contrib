// Copyright observIQ, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package fileintegrityreceiver

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/observiq/bindplane-otel-contrib/receiver/fileintegrityreceiver/internal/metadata"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/receiver/receivertest"
)

func TestReceiver_EmitsOnWrite(t *testing.T) {
	dir := t.TempDir()
	sink := new(consumertest.LogsSink)
	cfg := &Config{Paths: []string{dir}}
	require.NoError(t, cfg.Validate())

	recv, err := newFileIntegrityReceiver(cfg, receivertest.NewNopSettings(metadata.Type), sink)
	require.NoError(t, err)
	require.NoError(t, recv.Start(context.Background(), componenttest.NewNopHost()))
	defer func() { require.NoError(t, recv.Shutdown(context.Background())) }()

	path := filepath.Join(dir, "a.txt")
	require.NoError(t, os.WriteFile(path, []byte("hi"), 0o600))

	require.Eventually(t, func() bool {
		return sink.LogRecordCount() > 0
	}, 8*time.Second, 40*time.Millisecond, "expected a log record")

	found := false
	for _, batch := range sink.AllLogs() {
		for ri := 0; ri < batch.ResourceLogs().Len(); ri++ {
			sl := batch.ResourceLogs().At(ri).ScopeLogs().At(0)
			for li := 0; li < sl.LogRecords().Len(); li++ {
				p, ok := sl.LogRecords().At(li).Attributes().Get("file.path")
				if ok && p.Str() == path {
					found = true
					break
				}
			}
		}
	}
	require.True(t, found, "log for %q not found", path)
}

func TestReceiver_Hashing(t *testing.T) {
	dir := t.TempDir()
	sink := new(consumertest.LogsSink)
	cfg := &Config{
		Paths: []string{dir},
		Hashing: HashingConfig{
			Enabled:  true,
			Debounce: 150 * time.Millisecond,
			MaxBytes: 1024,
		},
	}
	require.NoError(t, cfg.Validate())

	recv, err := newFileIntegrityReceiver(cfg, receivertest.NewNopSettings(metadata.Type), sink)
	require.NoError(t, err)
	require.NoError(t, recv.Start(context.Background(), componenttest.NewNopHost()))
	defer func() { require.NoError(t, recv.Shutdown(context.Background())) }()

	path := filepath.Join(dir, "hashme.txt")
	require.NoError(t, os.WriteFile(path, []byte("hello-fim"), 0o600))

	require.Eventually(t, func() bool {
		for _, batch := range sink.AllLogs() {
			for ri := 0; ri < batch.ResourceLogs().Len(); ri++ {
				sl := batch.ResourceLogs().At(ri).ScopeLogs().At(0)
				for li := 0; li < sl.LogRecords().Len(); li++ {
					rec := sl.LogRecords().At(li)
					p, ok := rec.Attributes().Get("file.path")
					if !ok || p.Str() != path {
						continue
					}
					h, ok := rec.Attributes().Get("file.hash.sha256")
					return ok && len(h.Str()) == 64
				}
			}
		}
		return false
	}, 10*time.Second, 50*time.Millisecond)
}

func TestReceiver_ExcludePrefix(t *testing.T) {
	dir := t.TempDir()
	skipDir := filepath.Join(dir, "skip")
	require.NoError(t, os.MkdirAll(skipDir, 0o755))

	sink := new(consumertest.LogsSink)
	cfg := &Config{
		Paths:   []string{dir},
		Exclude: []string{skipDir},
	}
	require.NoError(t, cfg.Validate())

	recv, err := newFileIntegrityReceiver(cfg, receivertest.NewNopSettings(metadata.Type), sink)
	require.NoError(t, err)
	require.NoError(t, recv.Start(context.Background(), componenttest.NewNopHost()))
	defer func() { require.NoError(t, recv.Shutdown(context.Background())) }()

	allowed := filepath.Join(dir, "y.txt")
	require.NoError(t, os.WriteFile(allowed, []byte("y"), 0o600))
	require.Eventually(t, func() bool {
		return logsContainPath(sink, allowed)
	}, 6*time.Second, 40*time.Millisecond)

	excluded := filepath.Join(skipDir, "x.txt")
	require.NoError(t, os.WriteFile(excluded, []byte("x"), 0o600))
	time.Sleep(400 * time.Millisecond)
	require.False(t, logsContainPath(sink, excluded), "excluded path should not produce logs")
}

func logsContainPath(sink *consumertest.LogsSink, want string) bool {
	for _, batch := range sink.AllLogs() {
		for ri := 0; ri < batch.ResourceLogs().Len(); ri++ {
			sl := batch.ResourceLogs().At(ri).ScopeLogs().At(0)
			for li := 0; li < sl.LogRecords().Len(); li++ {
				p, ok := sl.LogRecords().At(li).Attributes().Get("file.path")
				if ok && p.Str() == want {
					return true
				}
			}
		}
	}
	return false
}
