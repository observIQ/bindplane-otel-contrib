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
	"strconv"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/observiq/bindplane-otel-contrib/receiver/fileintegrityreceiver/internal/metadata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
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

func TestReceiver_MaxWatches(t *testing.T) {
	// Create a directory tree with more directories than our low max_watches limit.
	dir := t.TempDir()
	for i := 0; i < 10; i++ {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "d"+strconv.Itoa(i), "nested"), 0o755))
	}

	sink := new(consumertest.LogsSink)
	cfg := &Config{
		Paths:      []string{dir},
		Recursive:  true,
		MaxWatches: 3, // very low limit
	}
	require.NoError(t, cfg.Validate())

	recv, err := newFileIntegrityReceiver(cfg, receivertest.NewNopSettings(metadata.Type), sink)
	require.NoError(t, err)

	// Start should succeed even though the tree has more directories than max_watches.
	require.NoError(t, recv.Start(context.Background(), componenttest.NewNopHost()))
	defer func() { require.NoError(t, recv.Shutdown(context.Background())) }()

	// Only max_watches directories should actually be watched.
	assert.Equal(t, 3, recv.watchCount)
}

func TestBuildResourceLogs(t *testing.T) {
	var r fileIntegrityReceiver
	logs := plog.NewLogs()

	rl := r.buildResourceLogs(logs)

	assert.Equal(t, 1, logs.ResourceLogs().Len())
	assert.Equal(t, rl, logs.ResourceLogs().At(0))
	val, ok := rl.Resource().Attributes().Get("fim.receiver")
	require.True(t, ok)
	assert.Equal(t, "file_integrity", val.Str())
}

func TestBuildScope(t *testing.T) {
	r := fileIntegrityReceiver{
		settings: receivertest.NewNopSettings(metadata.Type),
	}
	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()

	sl := r.buildScope(rl)

	assert.Equal(t, 1, rl.ScopeLogs().Len())
	assert.Equal(t, sl, rl.ScopeLogs().At(0))
	assert.Equal(t, metadata.ScopeName, sl.Scope().Name())
	assert.Equal(t, r.settings.BuildInfo.Version, sl.Scope().Version())
}

func TestBuildLogRecord_NoHash(t *testing.T) {
	var r fileIntegrityReceiver
	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()
	sl := rl.ScopeLogs().AppendEmpty()

	ts := pcommon.NewTimestampFromTime(time.Unix(123, 0))
	rec := fimRecord{
		path:      "/tmp/file.txt",
		op:        fsnotify.Write,
		fimOp:     "write",
		timestamp: ts,
	}

	r.buildLogRecord(sl, rec)

	require.Equal(t, 1, sl.LogRecords().Len())
	lr := sl.LogRecords().At(0)
	assert.Equal(t, ts, lr.Timestamp())
	assert.Equal(t, ts, lr.ObservedTimestamp())
	assert.Equal(t, "FIM write /tmp/file.txt", lr.Body().Str())

	attrs := lr.Attributes()
	assertAttributeString(t, attrs, "file.path", "/tmp/file.txt")
	assertAttributeString(t, attrs, "file.name", "file.txt")
	assertAttributeString(t, attrs, "file.extension", ".txt")
	assertAttributeString(t, attrs, "fim.operation", "write")
	assertAttributeString(t, attrs, "fsnotify.op", fsnotify.Write.String())
}

func TestBuildLogRecord_WithHash(t *testing.T) {
	var r fileIntegrityReceiver
	logs := plog.NewLogs()
	rl := logs.ResourceLogs().AppendEmpty()
	sl := rl.ScopeLogs().AppendEmpty()

	ts := pcommon.NewTimestampFromTime(time.Unix(123, 0))
	rec := fimRecord{
		path:      "/tmp/file.txt",
		op:        0,
		fimOp:     "write",
		timestamp: ts,
		hash: &hashResult{
			sha256: "abc",
		},
	}

	r.buildLogRecord(sl, rec)

	require.Equal(t, 1, sl.LogRecords().Len())
	lr := sl.LogRecords().At(0)
	attrs := lr.Attributes()
	assertAttributeString(t, attrs, "file.hash.sha256", "abc")
}

func TestBuildHashResult_Disabled(t *testing.T) {
	r := fileIntegrityReceiver{
		cfg: &Config{
			Hashing: HashingConfig{
				Enabled: false,
			},
		},
	}

	res := r.buildHashResult(0, "/does/not/matter")
	assert.Nil(t, res)
}

func assertAttributeString(t *testing.T, m pcommon.Map, key, want string) {
	t.Helper()
	val, ok := m.Get(key)
	require.True(t, ok, "missing attribute %q", key)
	assert.Equal(t, want, val.Str())
}

// Benchmarks

func benchmarkReceiverHandleEvent(b *testing.B, hashingEnabled bool) {
	dir := b.TempDir()
	sink := new(consumertest.LogsSink)

	cfg := &Config{
		Paths: []string{dir},
	}
	if hashingEnabled {
		cfg.Hashing = HashingConfig{
			Enabled:  true,
			Debounce: 50 * time.Millisecond,
			MaxBytes: 1 << 20,
		}
	}
	require.NoError(b, cfg.Validate())

	recv, err := newFileIntegrityReceiver(cfg, receivertest.NewNopSettings(metadata.Type), sink)
	if err != nil {
		b.Fatalf("newFileIntegrityReceiver: %v", err)
	}

	path := filepath.Join(dir, "bench.txt")
	if err := os.WriteFile(path, []byte("payload"), 0o600); err != nil {
		b.Fatalf("write temp file: %v", err)
	}

	ev := fsnotify.Event{
		Name: path,
		Op:   fsnotify.Write,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		recv.handleEvent(ev)
	}
}

// Benchmark the per-event overhead of handling a WRITE event with hashing disabled.
func BenchmarkReceiver_HandleEvent_NoHashing(b *testing.B) {
	benchmarkReceiverHandleEvent(b, false)
}

// Benchmark the per-event overhead of handling a WRITE event with hashing enabled.
func BenchmarkReceiver_HandleEvent_WithHashing(b *testing.B) {
	benchmarkReceiverHandleEvent(b, true)
}

// Benchmark a burst of events for a single file, exercising the debounce path.
func BenchmarkReceiver_BurstWrites_SingleFile(b *testing.B) {
	dir := b.TempDir()
	sink := new(consumertest.LogsSink)

	cfg := &Config{
		Paths: []string{dir},
		Hashing: HashingConfig{
			Enabled:  true,
			Debounce: 50 * time.Millisecond,
			MaxBytes: 1 << 20,
		},
	}
	require.NoError(b, cfg.Validate())

	recv, err := newFileIntegrityReceiver(cfg, receivertest.NewNopSettings(metadata.Type), sink)
	if err != nil {
		b.Fatalf("newFileIntegrityReceiver: %v", err)
	}

	path := filepath.Join(dir, "burst.txt")
	if err := os.WriteFile(path, []byte("payload"), 0o600); err != nil {
		b.Fatalf("write temp file: %v", err)
	}

	ev := fsnotify.Event{
		Name: path,
		Op:   fsnotify.Write | fsnotify.Chmod,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		recv.handleEvent(ev)
	}
}

// Benchmark many files in a single directory receiving WRITE events, to simulate
// a hot directory with lots of churn.
func BenchmarkReceiver_ManyFilesInDirectory(b *testing.B) {
	dir := b.TempDir()
	sink := new(consumertest.LogsSink)

	const numFiles = 256
	paths := make([]string, numFiles)
	for i := 0; i < numFiles; i++ {
		p := filepath.Join(dir, "file-"+strconv.Itoa(i)+".log")
		if err := os.WriteFile(p, []byte("payload"), 0o600); err != nil {
			b.Fatalf("write temp file %d: %v", i, err)
		}
		paths[i] = p
	}

	cfg := &Config{
		Paths: []string{dir},
		Hashing: HashingConfig{
			Enabled:  true,
			Debounce: 50 * time.Millisecond,
			MaxBytes: 1 << 20,
		},
	}
	require.NoError(b, cfg.Validate())

	recv, err := newFileIntegrityReceiver(cfg, receivertest.NewNopSettings(metadata.Type), sink)
	if err != nil {
		b.Fatalf("newFileIntegrityReceiver: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		path := paths[i%len(paths)]
		ev := fsnotify.Event{
			Name: path,
			Op:   fsnotify.Write,
		}
		recv.handleEvent(ev)
	}
}

// BenchmarkReceiver_Watcher_SingleFileHot benchmarks the receiver end-to-end with
// a single hot file in one directory that is written repeatedly. This exercises
// the fsnotify watcher, debounce, hashing (when enabled), and log emission
// under a realistic workload.
//
// To run watcher benchmarks with profiles:
//
//	go test -bench=Watcher -run=^$ -benchtime=10s -cpuprofile=cpu.out -memprofile=mem.out
func BenchmarkReceiver_Watcher_SingleFileHot(b *testing.B) {
	dir := b.TempDir()
	sink := new(consumertest.LogsSink)

	cfg := &Config{
		Paths: []string{dir},
		Hashing: HashingConfig{
			Enabled:  true,
			Debounce: 50 * time.Millisecond,
			MaxBytes: 1 << 20,
		},
	}
	require.NoError(b, cfg.Validate())

	recv, err := newFileIntegrityReceiver(cfg, receivertest.NewNopSettings(metadata.Type), sink)
	if err != nil {
		b.Fatalf("newFileIntegrityReceiver: %v", err)
	}

	ctx := context.Background()
	require.NoError(b, recv.Start(ctx, componenttest.NewNopHost()))
	defer func() {
		require.NoError(b, recv.Shutdown(ctx))
	}()

	path := filepath.Join(dir, "hot.log")
	payloadA := []byte("payload-a")
	payloadB := []byte("payload-b")
	if err := os.WriteFile(path, payloadA, 0o600); err != nil {
		b.Fatalf("write temp file: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Alternate between two payloads to generate writes that fsnotify will
		// observe as content changes.
		var payload []byte
		if i%2 == 0 {
			payload = payloadA
		} else {
			payload = payloadB
		}
		if err := os.WriteFile(path, payload, 0o600); err != nil {
			b.Fatalf("write hot file: %v", err)
		}
	}
}

// BenchmarkReceiver_Watcher_ManyFilesSingleDir benchmarks the receiver with many
// files in a single directory that are written repeatedly. This simulates a hot
// directory where many files are changing.
func BenchmarkReceiver_Watcher_ManyFilesSingleDir(b *testing.B) {
	dir := b.TempDir()
	sink := new(consumertest.LogsSink)

	const numFiles = 512
	paths := make([]string, numFiles)
	for i := 0; i < numFiles; i++ {
		p := filepath.Join(dir, "file-"+strconv.Itoa(i)+".log")
		if err := os.WriteFile(p, []byte("payload"), 0o600); err != nil {
			b.Fatalf("write temp file %d: %v", i, err)
		}
		paths[i] = p
	}

	cfg := &Config{
		Paths: []string{dir},
		Hashing: HashingConfig{
			Enabled:  true,
			Debounce: 50 * time.Millisecond,
			MaxBytes: 1 << 20,
		},
	}
	require.NoError(b, cfg.Validate())

	recv, err := newFileIntegrityReceiver(cfg, receivertest.NewNopSettings(metadata.Type), sink)
	if err != nil {
		b.Fatalf("newFileIntegrityReceiver: %v", err)
	}

	ctx := context.Background()
	require.NoError(b, recv.Start(ctx, componenttest.NewNopHost()))
	defer func() {
		require.NoError(b, recv.Shutdown(ctx))
	}()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		path := paths[i%len(paths)]
		if err := os.WriteFile(path, []byte("payload"), 0o600); err != nil {
			b.Fatalf("write temp file: %v", err)
		}
	}
}

// BenchmarkReceiver_Watcher_NestedDirsHighChurn benchmarks the receiver with a
// recursive watch over a nested directory tree where many files are being
// created, written, and deleted rapidly.
func BenchmarkReceiver_Watcher_NestedDirsHighChurn(b *testing.B) {
	dir := b.TempDir()
	sink := new(consumertest.LogsSink)

	// Build a small deterministic nested tree to keep benchmark time reasonable.
	const (
		depth    = 3
		fanout   = 4
		filesPer = 4
	)

	var dirs []string
	dirs = append(dirs, dir)
	for d := 0; d < depth; d++ {
		curLevel := dirs[len(dirs)-1:]
		for _, parent := range curLevel {
			for i := 0; i < fanout; i++ {
				child := filepath.Join(parent, "d"+strconv.Itoa(d)+"_"+strconv.Itoa(i))
				require.NoError(b, os.MkdirAll(child, 0o755))
				dirs = append(dirs, child)
				for f := 0; f < filesPer; f++ {
					p := filepath.Join(child, "f"+strconv.Itoa(f)+".log")
					if err := os.WriteFile(p, []byte("payload"), 0o600); err != nil {
						b.Fatalf("write temp file %s: %v", p, err)
					}
				}
			}
		}
	}

	cfg := &Config{
		Paths:     []string{dir},
		Recursive: true,
		Hashing: HashingConfig{
			Enabled:  true,
			Debounce: 50 * time.Millisecond,
			MaxBytes: 1 << 20,
		},
	}
	require.NoError(b, cfg.Validate())

	recv, err := newFileIntegrityReceiver(cfg, receivertest.NewNopSettings(metadata.Type), sink)
	if err != nil {
		b.Fatalf("newFileIntegrityReceiver: %v", err)
	}

	ctx := context.Background()
	require.NoError(b, recv.Start(ctx, componenttest.NewNopHost()))
	defer func() {
		require.NoError(b, recv.Shutdown(ctx))
	}()

	// Collect all files currently in the tree.
	var allFiles []string
	for _, dpath := range dirs {
		entries, err := os.ReadDir(dpath)
		if err != nil {
			b.Fatalf("readdir %s: %v", dpath, err)
		}
		for _, ent := range entries {
			if ent.IsDir() {
				continue
			}
			allFiles = append(allFiles, filepath.Join(dpath, ent.Name()))
		}
	}

	// Use a deterministic pseudo-random pattern over the known files and
	// directories to simulate churn without relying on math/rand's global state.
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		switch i % 3 {
		case 0:
			// Overwrite an existing file.
			p := allFiles[i%len(allFiles)]
			if err := os.WriteFile(p, []byte("payload-write"), 0o600); err != nil {
				b.Fatalf("write file %s: %v", p, err)
			}
		case 1:
			// Create a new file in a directory.
			dpath := dirs[i%len(dirs)]
			p := filepath.Join(dpath, "new-"+strconv.Itoa(i)+".log")
			if err := os.WriteFile(p, []byte("payload-new"), 0o600); err != nil {
				b.Fatalf("create file %s: %v", p, err)
			}
			allFiles = append(allFiles, p)
		case 2:
			// Delete a file, if we have any.
			if len(allFiles) == 0 {
				continue
			}
			idx := i % len(allFiles)
			p := allFiles[idx]
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				b.Fatalf("remove file %s: %v", p, err)
			}
			// Swap-delete to keep slice compact.
			allFiles[idx] = allFiles[len(allFiles)-1]
			allFiles = allFiles[:len(allFiles)-1]
		}
	}
}
