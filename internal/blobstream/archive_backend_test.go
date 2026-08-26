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

package blobstream

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// stubEntry is an archive entry whose reader and open error the test chooses.
type stubEntry struct {
	name    string
	isDir   bool
	body    string
	openErr error
}

func (e stubEntry) Name() string { return e.name }
func (e stubEntry) IsDir() bool  { return e.isDir }
func (e stubEntry) Open() (io.Reader, error) {
	if e.openErr != nil {
		return nil, e.openErr
	}
	return strings.NewReader(e.body), nil
}

// stubBackend hands out a fixed list of entries and then reports the close error the
// test chose. It stands in for a backend holding a resource that fails to release.
type stubBackend struct {
	entries  []archiveEntry
	next     int
	closeErr error
	// nextErr, when set, is returned once the entries are exhausted instead of io.EOF,
	// modeling a streaming backend that fails to advance to the next entry.
	nextErr error
}

func (b *stubBackend) Next() (archiveEntry, error) {
	if b.next >= len(b.entries) {
		if b.nextErr != nil {
			return nil, b.nextErr
		}
		return nil, io.EOF
	}
	entry := b.entries[b.next]
	b.next++
	return entry, nil
}

func (b *stubBackend) Close() error { return b.closeErr }

// Materialized reports false: the stub models a streaming backend.
func (b *stubBackend) Materialized() bool { return false }

// newStubArchive builds a producer over the given entries.
func newStubArchive(backend *stubBackend, logger *zap.Logger) *archiveProducer {
	return &archiveProducer{
		stream: LogStream{Name: "logs/object", MaxLogSize: testMaxLogSize, Logger: logger, TryDecoding: true},
		open:   func() (archiveBackend, error) { return backend, nil },
		limits: defaultArchiveLimits(),
	}
}

// drainStubArchive reads every record a producer yields.
func drainStubArchive(t *testing.T, producer *archiveProducer) []string {
	t.Helper()

	seq, err := producer.Records(context.Background(), Offset{})
	require.NoError(t, err)

	var bodies []string
	for rec, rerr := range seq {
		if rerr != nil {
			continue
		}
		text, ok := rec.(string)
		require.True(t, ok, "expected a line record, got %T", rec)
		bodies = append(bodies, text)
	}
	return bodies
}

// TestArchive_WarnsWhenTheBackendCannotClose asserts a backend that fails to release its
// resources is reported and does not lose the records already read. Close runs after the
// last record, so failing the object there would discard good data.
func TestArchive_WarnsWhenTheBackendCannotClose(t *testing.T) {
	t.Parallel()

	closeErr := errors.New("remove temp file: permission denied")
	core, logs := observer.New(zap.WarnLevel)

	backend := &stubBackend{
		entries:  []archiveEntry{stubEntry{name: "a.log", body: "kept1\nkept2\n"}},
		closeErr: closeErr,
	}

	bodies := drainStubArchive(t, newStubArchive(backend, zap.New(core)))
	require.Equal(t, []string{"kept1", "kept2"}, bodies)

	entries := logs.FilterMessage("close archive backend").All()
	require.Len(t, entries, 1)
	require.Contains(t, entries[0].ContextMap()["error"], "permission denied")
}

// TestArchive_SkipsEntriesThatCannotBeOpened asserts an entry the backend refuses to
// open is skipped and counted, while the rest of the archive is still read.
func TestArchive_SkipsEntriesThatCannotBeOpened(t *testing.T) {
	t.Parallel()

	core, logs := observer.New(zap.WarnLevel)

	backend := &stubBackend{entries: []archiveEntry{
		stubEntry{name: "locked.log", openErr: errors.New("entry is encrypted")},
		stubEntry{name: "a.log", body: "kept1\nkept2\n"},
	}}

	var parseErrors int
	producer := newStubArchive(backend, zap.New(core))
	producer.onParseError = func(context.Context) { parseErrors++ }

	bodies := drainStubArchive(t, producer)
	require.Equal(t, []string{"kept1", "kept2"}, bodies)
	require.Equal(t, 1, parseErrors, "the skipped entry must be counted")

	entries := logs.FilterMessage("skipping unparseable archive entry").All()
	require.Len(t, entries, 1)
	require.Equal(t, "locked.log", entries[0].ContextMap()["entry"])
	require.Contains(t, entries[0].ContextMap()["error"], "open entry")
}
