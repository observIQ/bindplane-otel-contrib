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
)

// writeStallCSV writes a small host-keyed CSV and returns its path.
func writeStallCSV(t *testing.T, rows int) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "assets.csv")
	f, err := os.Create(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, f.Close()) }()

	_, err = fmt.Fprintln(f, "host,owner,env")
	require.NoError(t, err)
	for i := 0; i < rows; i++ {
		_, err = fmt.Fprintf(f, "host-%04d,owner-%d,production\n", i, i)
		require.NoError(t, err)
	}
	require.NoError(t, f.Sync())

	return path
}

// TestReloadDoesNotBlockLookups holds a reload in flight and asserts that a
// lookup completes against the still-published old index rather than waiting for
// the reload to finish.
//
// The index is published through an atomic pointer, so a reload builds the new
// map off to the side and swaps it in with one Store. readHook pauses the reload
// after it has built the new index but before that Store, which is the exact
// window a concurrent lookup would have blocked in under a lock held across the
// whole reload. A lookup issued during that window must still return, since it
// reads the old index and never takes the reload's path.
func TestReloadDoesNotBlockLookups(t *testing.T) {
	path := writeStallCSV(t, 100)
	csvFile := NewCSVFile(path, "host")
	require.NoError(t, csvFile.Load())

	// Confirm the key resolves off the initial index before the reload starts.
	got, err := csvFile.Lookup(context.Background(), "host-0000")
	require.NoError(t, err)
	require.Equal(t, "owner-0", got["owner"])

	reloading := make(chan struct{}) // closed once the reload is paused mid-flight
	release := make(chan struct{})   // closed to let the paused reload finish
	var hooked bool
	csvFile.readHook = func() {
		if hooked {
			return
		}
		hooked = true
		close(reloading)
		<-release
	}

	reloadDone := make(chan error, 1)
	go func() { reloadDone <- csvFile.Load() }()

	<-reloading // the reload has built the new index and is parked before publishing

	// The reload is provably in flight. A lookup now must not wait on it. Hand the
	// result back to this goroutine rather than asserting in the spawned one, since
	// a testify require outside the test goroutine only stops its own goroutine.
	type lookupResult struct {
		row map[string]string
		err error
	}
	lookupDone := make(chan lookupResult, 1)
	go func() {
		row, err := csvFile.Lookup(context.Background(), "host-0001")
		lookupDone <- lookupResult{row: row, err: err}
	}()

	select {
	case res := <-lookupDone:
		require.NoError(t, res.err)
		require.Equal(t, "owner-1", res.row["owner"], "the lookup must see the old index while the reload is parked")
	case <-time.After(2 * time.Second):
		require.FailNow(t, "a lookup blocked while a reload was in flight")
	}

	close(release)
	require.NoError(t, <-reloadDone)
}
