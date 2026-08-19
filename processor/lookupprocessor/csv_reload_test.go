// Copyright  observIQ, Inc.
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

package lookupprocessor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// writeCSVAt writes a csv keyed by "ip" whose single data row carries env.
func writeCSVAt(t *testing.T, path, ip, env string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(fmt.Sprintf("ip,env\n%s,%s\n", ip, env)), 0o600))
}

// bumpModTime moves a file's modtime forward so a stamp comparison sees a change
// even when the rewrite happens within the filesystem's timestamp resolution.
func bumpModTime(t *testing.T, path string, d time.Duration) {
	t.Helper()
	fi, err := os.Stat(path)
	require.NoError(t, err)
	ts := fi.ModTime().Add(d)
	require.NoError(t, os.Chtimes(path, ts, ts))
}

func TestLoadSkipsUnchangedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lookup.csv")
	writeCSVAt(t, path, "0.0.0.0", "prod")

	c := NewCSVFile(path, "ip", zap.NewNop())
	// Report a clock far enough past the write that the stamp settles on the first
	// load, so this exercises the skip rather than the same-tick re-read.
	c.now = func() time.Time { return time.Now().Add(stampSettleWindow) }

	require.NoError(t, c.Load())
	require.Equal(t, int64(1), c.reads.Load(), "first load must read the file")

	first := c.data.Load()
	require.NotNil(t, first)

	require.NoError(t, c.Load())
	require.Equal(t, int64(1), c.reads.Load(), "unchanged file must not be re-read")
	require.Same(t, first, c.data.Load(), "unchanged file must not republish the index")
}

func TestLoadSkipsOnlyOnceTheStampHasSettled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lookup.csv")
	writeCSVAt(t, path, "0.0.0.0", "prod")

	fi, err := os.Stat(path)
	require.NoError(t, err)
	written := fi.ModTime()

	c := NewCSVFile(path, "ip", zap.NewNop())

	// Within the settle window another write could share this modification time,
	// so an equal stamp proves nothing and the file is read again.
	c.now = func() time.Time { return written.Add(stampSettleWindow - time.Millisecond) }
	require.NoError(t, c.Load())
	require.Equal(t, int64(1), c.reads.Load(), "first load must read the file")
	require.NoError(t, c.Load())
	require.Equal(t, int64(2), c.reads.Load(), "an unsettled stamp must not be skipped on")

	// Once the modification time has settled, the stamp identifies the content and
	// the read is skipped from then on.
	c.now = func() time.Time { return written.Add(stampSettleWindow) }
	require.NoError(t, c.Load())
	require.Equal(t, int64(3), c.reads.Load(), "the load that settles the stamp still reads")
	require.NoError(t, c.Load())
	require.Equal(t, int64(3), c.reads.Load(), "a settled stamp must be skipped on")
}

func TestLoadRereadsWhenRewriteSharesTheStampedModTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lookup.csv")
	writeCSVAt(t, path, "0.0.0.0", "prod")

	c := NewCSVFile(path, "ip", zap.NewNop())
	require.NoError(t, c.Load())

	fi, err := os.Stat(path)
	require.NoError(t, err)
	stamped := fi.ModTime()

	// A rewrite of the same size landing in the same filesystem timestamp tick
	// leaves both mtime and size identical to what the load stamped, so the stamp
	// cannot tell that the content changed. Skipping on it strands the old index
	// until some later write moves mtime or size.
	writeCSVAt(t, path, "0.0.0.0", "test")
	require.NoError(t, os.Chtimes(path, stamped, stamped))

	require.NoError(t, c.Load())

	got, err := c.Lookup(context.Background(), "0.0.0.0")
	require.NoError(t, err)
	require.Equal(t, "test", got["env"], "a rewrite sharing the stamped modtime must not be skipped")
}

func TestLoadPicksUpChangedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lookup.csv")
	writeCSVAt(t, path, "0.0.0.0", "prod")

	c := NewCSVFile(path, "ip", zap.NewNop())
	require.NoError(t, c.Load())

	got, err := c.Lookup(context.Background(), "0.0.0.0")
	require.NoError(t, err)
	require.Equal(t, "prod", got["env"])

	// same size, different content, so the modtime is what has to carry the change
	writeCSVAt(t, path, "0.0.0.0", "test")
	bumpModTime(t, path, time.Second)

	require.NoError(t, c.Load())
	require.Equal(t, int64(2), c.reads.Load(), "changed file must be re-read")

	got, err = c.Lookup(context.Background(), "0.0.0.0")
	require.NoError(t, err)
	require.Equal(t, "test", got["env"], "lookup must see the new content")
}

func TestLoadRetriesWhenFileChangesMidRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lookup.csv")
	writeCSVAt(t, path, "0.0.0.0", "prod")

	c := NewCSVFile(path, "ip", zap.NewNop())

	// Simulate a writer landing a rewrite between the read and the re-stat, which
	// is the window where a truncated-but-parseable file would be published.
	var rewritten bool
	c.readHook = func() {
		if rewritten {
			return
		}
		rewritten = true
		writeCSVAt(t, path, "0.0.0.0", "rewritten")
		bumpModTime(t, path, time.Second)
	}

	require.NoError(t, c.Load())
	require.Equal(t, int64(2), c.reads.Load(), "a mid-read change must trigger exactly one retry")

	got, err := c.Lookup(context.Background(), "0.0.0.0")
	require.NoError(t, err)
	require.Equal(t, "rewritten", got["env"], "the published index must be the post-rewrite content")
}

func TestNewCSVFileToleratesNilLogger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lookup.csv")
	writeCSVAt(t, path, "0.0.0.0", "prod")

	c := NewCSVFile(path, "ip", nil)
	require.NotPanics(t, func() { require.NoError(t, c.Load()) })
}

func TestLoadWithoutInjectedClockDoesNotPanic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lookup.csv")
	writeCSVAt(t, path, "0.0.0.0", "prod")

	// A CSVFile built by hand rather than through NewCSVFile has no clock, so the
	// stamp-settle check must fall back instead of dereferencing a nil func.
	c := &CSVFile{filepath: path, lookupColumn: "ip", logger: zap.NewNop()}
	require.NotPanics(t, func() { require.NoError(t, c.Load()) })

	got, err := c.Lookup(context.Background(), "0.0.0.0")
	require.NoError(t, err)
	require.Equal(t, "prod", got["env"])
}

func TestLoadSurfacesStatErrors(t *testing.T) {
	c := NewCSVFile(filepath.Join(t.TempDir(), "missing.csv"), "ip", zap.NewNop())
	require.Error(t, c.Load())
	require.Nil(t, c.data.Load(), "a failed load must not publish an index")
}

func TestLoadSurfacesReadErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lookup.csv")
	// A bare quote in an unquoted field makes csv parsing fail on the first read.
	require.NoError(t, os.WriteFile(path, []byte("ip,env\nbad\"quote,x\n"), 0o600))

	c := NewCSVFile(path, "ip", zap.NewNop())
	require.Error(t, c.Load())
	require.Nil(t, c.data.Load(), "a failed read must not publish an index")
}

func TestLoadSurfacesReStatErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lookup.csv")
	writeCSVAt(t, path, "0.0.0.0", "prod")

	c := NewCSVFile(path, "ip", zap.NewNop())
	// Remove the file after the read but before the re-stat, so the re-stat fails.
	var fired bool
	c.readHook = func() {
		if fired {
			return
		}
		fired = true
		require.NoError(t, os.Remove(path))
	}
	require.Error(t, c.Load())
}

func TestLoadSurfacesRetryReadErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lookup.csv")
	writeCSVAt(t, path, "0.0.0.0", "prod")

	c := NewCSVFile(path, "ip", zap.NewNop())
	// Rewrite the file mid-read so the re-stat forces a retry, and make that retry
	// read fail on a bare quote.
	var fired bool
	c.readHook = func() {
		if fired {
			return
		}
		fired = true
		require.NoError(t, os.WriteFile(path, []byte("ip,env\nbad\"quote,more\n"), 0o600))
		bumpModTime(t, path, time.Second)
	}
	require.Error(t, c.Load())
}

func TestLoadKeepsPreviousIndexWhenFileGoesAway(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lookup.csv")
	writeCSVAt(t, path, "0.0.0.0", "prod")

	c := NewCSVFile(path, "ip", zap.NewNop())
	require.NoError(t, c.Load())
	before := c.data.Load()

	require.NoError(t, os.Remove(path))
	require.Error(t, c.Load())
	require.Same(t, before, c.data.Load(), "a failed reload must leave the last good index in place")

	got, err := c.Lookup(context.Background(), "0.0.0.0")
	require.NoError(t, err)
	require.Equal(t, "prod", got["env"])
}

func TestResolveReloadInterval(t *testing.T) {
	require.Equal(t, defaultReloadInterval, resolveReloadInterval(&Config{}),
		"unset must fall back to the historical 60s cadence")
	require.Equal(t, 5*time.Second, resolveReloadInterval(&Config{ReloadInterval: 5 * time.Second}))
}
