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
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/observiq/bindplane-otel-contrib/receiver/fileintegrityreceiver/internal/metadata"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/receiver"
	"go.uber.org/zap"
)

type pendingEvent struct {
	op        fsnotify.Op
	timer     *time.Timer
	firstSeen time.Time
}

type fileIntegrityReceiver struct {
	cfg      *Config
	settings receiver.Settings
	consumer consumer.Logs
	logger   *zap.Logger

	watcher    *fsnotify.Watcher
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	watchCount int

	excludes []PathMatcher

	debounceMu sync.Mutex
	pending    map[string]*pendingEvent
}

func newFileIntegrityReceiver(cfg *Config, params receiver.Settings, next consumer.Logs) (*fileIntegrityReceiver, error) {
	excludes, err := CompileExcludes(cfg.Exclude)
	if err != nil {
		return nil, err
	}

	return &fileIntegrityReceiver{
		cfg:      cfg,
		settings: params,
		consumer: next,
		logger:   params.Logger,
		excludes: excludes,
		pending:  make(map[string]*pendingEvent),
	}, nil
}

func (r *fileIntegrityReceiver) Start(_ context.Context, _ component.Host) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("fsnotify watcher: %w", err)
	}
	r.watcher = watcher

	if err := r.registerWatches(); err != nil {
		_ = watcher.Close()
		r.watcher = nil
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.wg.Add(1)
	go r.run(ctx)
	return nil
}

func (r *fileIntegrityReceiver) maxWatches() int {
	if r.cfg.MaxWatches > 0 {
		return r.cfg.MaxWatches
	}
	return 65536
}

func (r *fileIntegrityReceiver) addWatch(path string) error {
	if r.watchCount >= r.maxWatches() {
		return errMaxWatchesReached
	}
	if err := r.watcher.Add(path); err != nil {
		return err
	}
	r.watchCount++
	return nil
}

var errMaxWatchesReached = errors.New("max watches reached")

func (r *fileIntegrityReceiver) registerWatches() error {
	for _, p := range r.cfg.Paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return fmt.Errorf("abs path %q: %w", p, err)
		}
		abs = filepath.Clean(abs)
		fi, err := os.Stat(abs)
		if err != nil {
			return err
		}
		if fi.IsDir() && r.cfg.Recursive {
			if err := r.addRecursiveWatches(abs); err != nil {
				return err
			}
		} else {
			if err := r.addWatch(abs); err != nil {
				return fmt.Errorf("watch %q: %w", abs, err)
			}
		}
	}
	r.logger.Info("registered watches", zap.Int("count", r.watchCount), zap.Int("max", r.maxWatches()))
	return nil
}

func (r *fileIntegrityReceiver) addRecursiveWatches(root string) error {
	root = filepath.Clean(root)
	limitLogged := false
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Permission errors and other transient issues should not abort the
			// entire walk — skip the subtree and keep going.
			r.logger.Warn("skipping path during walk", zap.String("path", path), zap.Error(err))
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		cleanPath := filepath.Clean(path)
		if cleanPath != root && r.excluded(cleanPath) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if err := r.addWatch(cleanPath); err != nil {
			if errors.Is(err, errMaxWatchesReached) {
				if !limitLogged {
					r.logger.Warn("max_watches limit reached, skipping remaining directories",
						zap.Int("max_watches", r.maxWatches()),
						zap.String("first_skipped", cleanPath),
					)
					limitLogged = true
				}
				return filepath.SkipDir
			}
			// Treat individual watch failures as non-fatal. Continue walking
			// into children — we may still be able to watch subdirectories
			// even if the parent watch failed (e.g. macOS kqueue quirks).
			r.logger.Warn("failed to add watch, continuing into children", zap.String("path", cleanPath), zap.Error(err))
			return nil
		}
		return nil
	})
}

// excluded reports whether path matches an exclude rule. path must already be filepath.Clean
// (one clean per event at the call site); matchers do not call Clean again.
func (r *fileIntegrityReceiver) excluded(path string) bool {
	for _, m := range r.excludes {
		if m(path) {
			return true
		}
	}
	return false
}

func (r *fileIntegrityReceiver) run(ctx context.Context) {
	defer r.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case err, ok := <-r.watcher.Errors:
			if !ok {
				return
			}
			if err != nil {
				r.logger.Error("fsnotify error", zap.Error(err))
			}
		case ev, ok := <-r.watcher.Events:
			if !ok {
				return
			}
			r.handleEvent(ev)
		}
	}
}

func (r *fileIntegrityReceiver) handleEvent(ev fsnotify.Event) {
	path := filepath.Clean(ev.Name)
	if r.excluded(path) {
		return
	}

	if r.cfg.Recursive && ev.Has(fsnotify.Create) {
		if fi, err := os.Stat(path); err == nil && fi.IsDir() {
			if err := r.addRecursiveWatches(path); err != nil {
				r.logger.Warn("recursive watch for new directory", zap.String("path", path), zap.Error(err))
			}
		}
	}

	if r.needsDebounce(ev.Op) {
		r.scheduleDebounced(path, ev.Op)
		return
	}
	r.emitLogRecord(context.Background(), path, ev.Op)
}

func (r *fileIntegrityReceiver) needsDebounce(op fsnotify.Op) bool {
	return op.Has(fsnotify.Create) || op.Has(fsnotify.Write) || op.Has(fsnotify.Chmod)
}

func (r *fileIntegrityReceiver) debounceDuration() time.Duration {
	if r.cfg.Hashing.Enabled && r.cfg.Hashing.Debounce > 0 {
		return r.cfg.Hashing.Debounce
	}
	// When hashing is disabled, still debounce briefly to coalesce chattier
	// fsnotify sequences. This is a pragmatic default; if needed we can make
	// it configurable later.
	return 75 * time.Millisecond
}

func (r *fileIntegrityReceiver) scheduleDebounced(path string, op fsnotify.Op) {
	r.debounceMu.Lock()
	defer r.debounceMu.Unlock()

	pe, ok := r.pending[path]
	if !ok {
		pe = &pendingEvent{firstSeen: time.Now()}
		r.pending[path] = pe
	}
	pe.op |= op
	if pe.timer != nil {
		pe.timer.Stop()
	}
	pathCopy := path
	debounce := r.debounceDuration()

	if r.cfg.Hashing.Enabled && r.cfg.Hashing.MaxDebounce > 0 && !pe.firstSeen.IsZero() && time.Since(pe.firstSeen) > r.cfg.Hashing.MaxDebounce {
		// We've exceeded the maximum debounce window; fire once now rather than
		// postponing indefinitely for extremely noisy paths.
		go r.fireDebounced(pathCopy)
		return
	}

	pe.timer = time.AfterFunc(debounce, func() {
		r.fireDebounced(pathCopy)
	})
}

func (r *fileIntegrityReceiver) fireDebounced(path string) {
	r.debounceMu.Lock()
	pe, ok := r.pending[path]
	if !ok {
		r.debounceMu.Unlock()
		return
	}
	delete(r.pending, path)
	op := pe.op
	r.debounceMu.Unlock()

	r.emitLogRecord(context.Background(), path, op)
}

func (r *fileIntegrityReceiver) flushPending() {
	r.debounceMu.Lock()
	items := make([]struct {
		path string
		op   fsnotify.Op
	}, 0, len(r.pending))
	for p, pe := range r.pending {
		if pe.timer != nil {
			pe.timer.Stop()
		}
		items = append(items, struct {
			path string
			op   fsnotify.Op
		}{p, pe.op})
	}
	r.pending = make(map[string]*pendingEvent)
	r.debounceMu.Unlock()

	ctx := context.Background()
	for _, it := range items {
		r.emitLogRecord(ctx, it.path, it.op)
	}
}

type fimRecord struct {
	path      string
	op        fsnotify.Op
	fimOp     string
	timestamp pcommon.Timestamp
	hash      *hashResult // nil if not attempted
}

type hashResult struct {
	sha256     string
	skipped    bool
	skipReason string
	err        error
}

func applyHashAttrs(attrs pcommon.Map, h *hashResult) {
	switch {
	case h.err != nil:
		attrs.PutStr("file.hash.error", h.err.Error())
	case h.skipped:
		attrs.PutBool("file.hash.skipped", true)
		if h.skipReason != "" {
			attrs.PutStr("file.hash.skip_reason", h.skipReason)
		}
	default:
		attrs.PutStr("file.hash.sha256", h.sha256)
	}
}

func (r *fileIntegrityReceiver) buildHashResult(op fsnotify.Op, path string) *hashResult {
	if !r.shouldTryHash(op, path) {
		return nil
	}

	hex, skipped, reason, err := hashFileSHA256(path, r.cfg.Hashing.MaxBytes)
	return &hashResult{
		sha256:     hex,
		skipped:    skipped,
		skipReason: reason,
		err:        err,
	}
}

func (r *fileIntegrityReceiver) buildLogs(rec fimRecord) plog.Logs {
	logs := plog.NewLogs()
	rl := r.buildResourceLogs(logs)
	sl := r.buildScope(rl)
	r.buildLogRecord(sl, rec)
	return logs
}

func (r *fileIntegrityReceiver) buildResourceLogs(logs plog.Logs) plog.ResourceLogs {
	rl := logs.ResourceLogs().AppendEmpty()
	rl.Resource().Attributes().PutStr("fim.receiver", "file_integrity")
	return rl
}

func (r *fileIntegrityReceiver) buildScope(rl plog.ResourceLogs) plog.ScopeLogs {
	sl := rl.ScopeLogs().AppendEmpty()
	sl.Scope().SetName(metadata.ScopeName)
	sl.Scope().SetVersion(r.settings.BuildInfo.Version)
	return sl
}

func (r *fileIntegrityReceiver) buildLogRecord(sl plog.ScopeLogs, rec fimRecord) {
	lr := sl.LogRecords().AppendEmpty()
	lr.SetTimestamp(rec.timestamp)
	lr.SetObservedTimestamp(rec.timestamp)
	lr.Body().SetStr(fmt.Sprintf("FIM %s %s", rec.fimOp, rec.path))

	attrs := lr.Attributes()
	attrs.PutStr("file.path", rec.path)
	attrs.PutStr("file.name", filepath.Base(rec.path))
	if ext := filepath.Ext(rec.path); ext != "" {
		attrs.PutStr("file.extension", ext)
	}
	attrs.PutStr("fim.operation", rec.fimOp)
	attrs.PutStr("fsnotify.op", rec.op.String())

	if rec.hash != nil {
		applyHashAttrs(attrs, rec.hash)
	}
}

func (r *fileIntegrityReceiver) emitLogRecord(ctx context.Context, path string, op fsnotify.Op) {
	if r.excluded(path) {
		return
	}
	now := time.Now()
	ts := pcommon.NewTimestampFromTime(now)
	fimOp := normalizeFIMOp(op)

	rec := fimRecord{
		path:      path,
		op:        op,
		fimOp:     fimOp,
		timestamp: ts,
		hash:      r.buildHashResult(op, path),
	}

	logs := r.buildLogs(rec)

	if err := r.consumer.ConsumeLogs(ctx, logs); err != nil {
		r.logger.Error("consume logs", zap.Error(err))
	}
}

func normalizeFIMOp(op fsnotify.Op) string {
	switch {
	case op.Has(fsnotify.Remove):
		return "remove"
	case op.Has(fsnotify.Rename):
		return "rename"
	case op.Has(fsnotify.Create):
		return "create"
	case op.Has(fsnotify.Write):
		return "write"
	case op.Has(fsnotify.Chmod):
		return "chmod"
	default:
		return "unknown"
	}
}

func (r *fileIntegrityReceiver) shouldTryHash(op fsnotify.Op, path string) bool {
	if !r.cfg.Hashing.Enabled {
		return false
	}
	if !op.Has(fsnotify.Create) && !op.Has(fsnotify.Write) {
		return false
	}
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	return fi.Mode().IsRegular()
}

func (r *fileIntegrityReceiver) Shutdown(ctx context.Context) error {
	if r.cancel != nil {
		r.cancel()
	}
	r.flushPending()
	if r.watcher != nil {
		if err := r.watcher.Close(); err != nil {
			r.logger.Debug("close watcher", zap.Error(err))
		}
	}
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}
