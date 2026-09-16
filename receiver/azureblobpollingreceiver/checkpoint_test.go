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

package azureblobpollingreceiver //import "github.com/observiq/bindplane-otel-contrib/receiver/azureblobpollingreceiver"

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPollingCheckpoint_Progress(t *testing.T) {
	t.Run("ProgressFor and UpdateProgress", func(t *testing.T) {
		cp := NewPollingCheckpoint()
		_, ok := cp.ProgressFor("blob")
		require.False(t, ok, "unknown blob has no progress")

		ts := time.Date(2026, 8, 31, 14, 0, 0, 0, time.UTC)
		cp.UpdateProgress("blob", BlobProgress{Offset: 128, Fingerprint: []byte("fp"), LastModified: ts})
		got, ok := cp.ProgressFor("blob")
		require.True(t, ok)
		require.Equal(t, int64(128), got.Offset)
		require.Equal(t, []byte("fp"), got.Fingerprint)
		require.Equal(t, ts, got.LastModified)
	})

	t.Run("UpdateProgress lazily initializes a nil map", func(t *testing.T) {
		// A checkpoint unmarshaled from an older payload has no progress map.
		cp := &PollingCheckPoint{}
		require.Nil(t, cp.Progress)
		cp.UpdateProgress("blob", BlobProgress{Offset: 10})
		got, ok := cp.ProgressFor("blob")
		require.True(t, ok)
		require.Equal(t, int64(10), got.Offset)
	})

	t.Run("AgedOutProgress removes and returns only aged-out entries", func(t *testing.T) {
		cp := NewPollingCheckpoint()
		cutoff := time.Date(2026, 8, 31, 15, 0, 0, 0, time.UTC)
		cp.UpdateProgress("old", BlobProgress{Offset: 5, LastModified: cutoff.Add(-time.Hour)}) // before cutoff
		cp.UpdateProgress("new", BlobProgress{Offset: 5, LastModified: cutoff.Add(time.Hour)})  // after cutoff
		cp.UpdateProgress("edge", BlobProgress{Offset: 5, LastModified: cutoff})                // exactly cutoff, kept

		aged, _ := cp.AgedOutProgress(cutoff, cutoff.Add(-quarantineRetention), cutoff.Add(-quarantineRetention))
		require.Contains(t, aged, "old")
		require.NotContains(t, aged, "new")
		require.NotContains(t, aged, "edge")

		_, ok := cp.ProgressFor("old")
		require.False(t, ok, "aged-out entry removed")
		_, ok = cp.ProgressFor("new")
		require.True(t, ok, "in-horizon entry kept")
		_, ok = cp.ProgressFor("edge")
		require.True(t, ok, "entry exactly at cutoff kept")
	})

	t.Run("AgedOutProgress leaves quarantined entries in place", func(t *testing.T) {
		cp := NewPollingCheckpoint()
		cutoff := time.Date(2026, 8, 31, 15, 0, 0, 0, time.UTC)
		old := cutoff.Add(-time.Hour)
		cp.UpdateProgress("normal", BlobProgress{Offset: 5, LastModified: old})
		cp.UpdateProgress("bad", BlobProgress{Offset: 0, LastModified: old, Quarantined: true})

		aged, _ := cp.AgedOutProgress(cutoff, cutoff.Add(-quarantineRetention), cutoff.Add(-quarantineRetention))
		require.Contains(t, aged, "normal")
		require.NotContains(t, aged, "bad", "a quarantined entry is not aged out again")
		_, ok := cp.ProgressFor("bad")
		require.True(t, ok, "the quarantined entry stays in the map so the blob is ignored while unchanged")
	})

	t.Run("AgedOutProgress prunes a quarantined entry past the prune cutoff", func(t *testing.T) {
		cp := NewPollingCheckpoint()
		cutoff := time.Date(2026, 8, 31, 15, 0, 0, 0, time.UTC)
		pruneCutoff := cutoff.Add(-quarantineRetention)
		cp.UpdateProgress("bad", BlobProgress{Offset: 0, LastModified: pruneCutoff.Add(-time.Hour), Quarantined: true})

		aged, _ := cp.AgedOutProgress(cutoff, pruneCutoff, pruneCutoff)
		require.NotContains(t, aged, "bad", "a pruned quarantined entry is dropped, not flushed/deleted")
		_, ok := cp.ProgressFor("bad")
		require.False(t, ok, "the long-quarantined entry is pruned to bound the map")
	})

	t.Run("AgedOutProgress leaves sealed entries in place", func(t *testing.T) {
		cp := NewPollingCheckpoint()
		cutoff := time.Date(2026, 8, 31, 15, 0, 0, 0, time.UTC)
		old := cutoff.Add(-time.Hour)
		cp.UpdateProgress("sealed", BlobProgress{Offset: 42, LastModified: old, Sealed: true})

		aged, _ := cp.AgedOutProgress(cutoff, cutoff.Add(-quarantineRetention), cutoff.Add(-quarantineRetention))
		require.NotContains(t, aged, "sealed", "a sealed entry is not aged out again / re-flushed")
		got, ok := cp.ProgressFor("sealed")
		require.True(t, ok, "the sealed entry stays so a later resume reads from its offset")
		require.Equal(t, int64(42), got.Offset)
	})

	t.Run("AgedOutProgress prunes a sealed entry past the prune cutoff", func(t *testing.T) {
		cp := NewPollingCheckpoint()
		cutoff := time.Date(2026, 8, 31, 15, 0, 0, 0, time.UTC)
		pruneCutoff := cutoff.Add(-quarantineRetention)
		cp.UpdateProgress("sealed", BlobProgress{Offset: 42, LastModified: pruneCutoff.Add(-time.Hour), Sealed: true})

		cp.AgedOutProgress(cutoff, pruneCutoff, pruneCutoff)
		_, ok := cp.ProgressFor("sealed")
		require.False(t, ok, "a long-sealed entry is pruned to bound the map")
	})

	t.Run("AgedOutProgress gives up on a retried-and-failing entry past the retry budget", func(t *testing.T) {
		// A persistently-failing finalize, once older than quarantineRetention before the aging
		// cutoff, is dropped and reported in giveUp for the caller's drop log.
		cp := NewPollingCheckpoint()
		cutoff := time.Date(2026, 8, 31, 15, 0, 0, 0, time.UTC)
		cp.UpdateProgress("stuck", BlobProgress{Offset: 5, LastModified: cutoff.Add(-quarantineRetention - time.Hour), FinalizeFailures: 2})

		aged, giveUp := cp.AgedOutProgress(cutoff, cutoff.Add(-quarantineRetention), cutoff.Add(-quarantineRetention))
		require.NotContains(t, aged, "stuck", "past the retry budget it is dropped, not retried again")
		require.Contains(t, giveUp, "stuck", "and reported so the drop is logged")
		_, ok := cp.ProgressFor("stuck")
		require.False(t, ok, "and removed from the map to bound it")
	})

	t.Run("AgedOutProgress retries a just-failed entry despite a recent window-coupled prune cutoff", func(t *testing.T) {
		// Regression: a revisit window >= quarantineRetention makes quarantinePruneCutoff newer than
		// the aging cutoff. A just-aged-out entry must still be retried, not pruned on its first
		// failure — the retry bound is anchored to the aging cutoff, not the window.
		cp := NewPollingCheckpoint()
		cutoff := time.Date(2026, 8, 31, 15, 0, 0, 0, time.UTC)
		recentPruneCutoff := cutoff.Add(time.Hour) // window-coupled cutoff, newer than the aging cutoff
		cp.UpdateProgress("stuck", BlobProgress{Offset: 5, LastModified: cutoff.Add(-time.Minute), FinalizeFailures: 1})

		aged, giveUp := cp.AgedOutProgress(cutoff, recentPruneCutoff, recentPruneCutoff)
		require.Contains(t, aged, "stuck", "a just-failed entry is retried, not collapsed to one attempt by a large window")
		require.NotContains(t, giveUp, "stuck")
	})

	t.Run("AgedOutProgress finalizes a never-failed aged entry even past the prune cutoff", func(t *testing.T) {
		// A first-seen blob whose mtime is already older than the prune cutoff (e.g. a large
		// initial_lookback backfill) must be finalized (returned as aged), not silently dropped.
		cp := NewPollingCheckpoint()
		cutoff := time.Date(2026, 8, 31, 15, 0, 0, 0, time.UTC)
		pruneCutoff := cutoff.Add(-quarantineRetention)
		cp.UpdateProgress("fresh", BlobProgress{Offset: 5, LastModified: pruneCutoff.Add(-time.Hour)}) // FinalizeFailures 0

		aged, _ := cp.AgedOutProgress(cutoff, pruneCutoff, pruneCutoff)
		require.Contains(t, aged, "fresh", "a never-failed entry is finalized, not pruned")
	})

	t.Run("AgedOutProgress never seals a zero-LastModified entry", func(t *testing.T) {
		cp := NewPollingCheckpoint()
		cutoff := time.Date(2026, 8, 31, 15, 0, 0, 0, time.UTC)
		cp.UpdateProgress("no-mtime", BlobProgress{Offset: 5}) // zero LastModified

		aged, _ := cp.AgedOutProgress(cutoff, cutoff.Add(-quarantineRetention), cutoff.Add(-quarantineRetention))
		require.NotContains(t, aged, "no-mtime", "a blob with unknown mtime is never sealed")
		_, ok := cp.ProgressFor("no-mtime")
		require.True(t, ok, "the entry is retained rather than sealed/deleted")
	})
}

func TestPollingCheckpoint(t *testing.T) {
	t.Run("NewPollingCheckpoint", func(t *testing.T) {
		cp := NewPollingCheckpoint()
		require.NotNil(t, cp)
		require.True(t, cp.LastPollTime.IsZero())
		require.True(t, cp.LastTs.IsZero())
		require.Empty(t, cp.ParsedEntities)
	})

	t.Run("UpdatePollTime", func(t *testing.T) {
		cp := NewPollingCheckpoint()
		now := time.Now().UTC()
		cp.UpdatePollTime(now)
		require.Equal(t, now, cp.LastPollTime)
	})

	t.Run("ShouldParse", func(t *testing.T) {
		cp := NewPollingCheckpoint()
		now := time.Now().UTC()

		// Should parse new entity
		require.True(t, cp.ShouldParse(now, "blob1"))

		// Update checkpoint with entity
		cp.UpdateCheckpoint(now, "blob1")

		// Should not parse same entity again
		require.False(t, cp.ShouldParse(now, "blob1"))

		// Should parse different entity at same time
		require.True(t, cp.ShouldParse(now, "blob2"))

		// Should not parse entity before LastTs
		past := now.Add(-1 * time.Hour)
		require.False(t, cp.ShouldParse(past, "blob3"))

		// Should parse entity after LastTs
		future := now.Add(1 * time.Hour)
		require.True(t, cp.ShouldParse(future, "blob4"))
	})

	t.Run("UpdateCheckpoint clears old entities", func(t *testing.T) {
		cp := NewPollingCheckpoint()
		now := time.Now().UTC()

		// Add some entities at current time
		cp.UpdateCheckpoint(now, "blob1")
		cp.UpdateCheckpoint(now, "blob2")
		require.Len(t, cp.ParsedEntities, 2)

		// Update with newer time should clear old entities
		future := now.Add(1 * time.Hour)
		cp.UpdateCheckpoint(future, "blob3")
		require.Len(t, cp.ParsedEntities, 1)
		require.Contains(t, cp.ParsedEntities, "blob3")
		require.NotContains(t, cp.ParsedEntities, "blob1")
		require.NotContains(t, cp.ParsedEntities, "blob2")
		require.Equal(t, future, cp.LastTs)
	})

	t.Run("Marshal and Unmarshal", func(t *testing.T) {
		cp := NewPollingCheckpoint()
		now := time.Now().UTC().Truncate(time.Second) // Truncate for JSON precision

		cp.UpdatePollTime(now)
		cp.UpdateCheckpoint(now, "blob1")
		cp.UpdateCheckpoint(now, "blob2")

		// Marshal
		data, err := cp.Marshal()
		require.NoError(t, err)
		require.NotEmpty(t, data)

		// Unmarshal into new checkpoint
		cp2 := NewPollingCheckpoint()
		err = cp2.Unmarshal(data)
		require.NoError(t, err)

		// Verify data matches
		require.Equal(t, cp.LastPollTime.Unix(), cp2.LastPollTime.Unix())
		require.Equal(t, cp.LastTs.Unix(), cp2.LastTs.Unix())
		require.Equal(t, cp.ParsedEntities, cp2.ParsedEntities)
	})

	t.Run("Unmarshal empty data", func(t *testing.T) {
		cp := NewPollingCheckpoint()
		err := cp.Unmarshal([]byte{})
		require.NoError(t, err)
	})

	t.Run("ShouldParse with entity at same time as LastTs", func(t *testing.T) {
		cp := NewPollingCheckpoint()
		now := time.Now().UTC()

		// Update checkpoint with first entity
		cp.UpdateCheckpoint(now, "blob1")

		// Should not parse same entity at same time
		require.False(t, cp.ShouldParse(now, "blob1"))

		// Should parse different entity at same time
		require.True(t, cp.ShouldParse(now, "blob2"))
	})

	t.Run("ShouldParse with exactly LastTs time", func(t *testing.T) {
		cp := NewPollingCheckpoint()
		now := time.Now().UTC()

		// Set LastTs
		cp.UpdateCheckpoint(now, "blob1")

		// Entity at exactly LastTs time should be processed if not already parsed
		require.True(t, cp.ShouldParse(now, "blob2"))
		require.False(t, cp.ShouldParse(now, "blob1"))
	})

	t.Run("UpdateCheckpoint preserves entities at same time", func(t *testing.T) {
		cp := NewPollingCheckpoint()
		now := time.Now().UTC()

		// Add multiple entities at same time
		cp.UpdateCheckpoint(now, "blob1")
		cp.UpdateCheckpoint(now, "blob2")
		cp.UpdateCheckpoint(now, "blob3")

		// All entities should be tracked
		require.Len(t, cp.ParsedEntities, 3)
		require.Contains(t, cp.ParsedEntities, "blob1")
		require.Contains(t, cp.ParsedEntities, "blob2")
		require.Contains(t, cp.ParsedEntities, "blob3")
		require.Equal(t, now, cp.LastTs)
	})

	t.Run("Marshal and Unmarshal with empty ParsedEntities", func(t *testing.T) {
		cp := NewPollingCheckpoint()
		now := time.Now().UTC().Truncate(time.Second)

		cp.UpdatePollTime(now)
		// Don't add any entities

		// Marshal
		data, err := cp.Marshal()
		require.NoError(t, err)
		require.NotEmpty(t, data)

		// Unmarshal
		cp2 := NewPollingCheckpoint()
		err = cp2.Unmarshal(data)
		require.NoError(t, err)

		// Verify
		require.Equal(t, cp.LastPollTime.Unix(), cp2.LastPollTime.Unix())
		require.Empty(t, cp2.ParsedEntities)
	})

	t.Run("Marshal and Unmarshal with many entities", func(t *testing.T) {
		cp := NewPollingCheckpoint()
		now := time.Now().UTC().Truncate(time.Second)

		cp.UpdatePollTime(now)
		// Add many entities
		for i := 0; i < 100; i++ {
			blob := fmt.Sprintf("blob%d", i)
			cp.UpdateCheckpoint(now, blob)
		}

		// Marshal
		data, err := cp.Marshal()
		require.NoError(t, err)
		require.NotEmpty(t, data)

		// Unmarshal
		cp2 := NewPollingCheckpoint()
		err = cp2.Unmarshal(data)
		require.NoError(t, err)

		// Verify all entities are preserved
		require.Equal(t, cp.LastPollTime.Unix(), cp2.LastPollTime.Unix())
		require.Equal(t, cp.LastTs.Unix(), cp2.LastTs.Unix())
		require.Len(t, cp2.ParsedEntities, 100)
		require.Equal(t, cp.ParsedEntities, cp2.ParsedEntities)
	})

	t.Run("Unmarshal invalid JSON", func(t *testing.T) {
		cp := NewPollingCheckpoint()
		err := cp.Unmarshal([]byte("invalid json"))
		require.Error(t, err)
	})

	t.Run("ShouldParse with zero LastTs", func(t *testing.T) {
		cp := NewPollingCheckpoint()
		now := time.Now().UTC()

		// With zero LastTs, any entity with non-zero time should be processed
		require.True(t, cp.ShouldParse(now, "blob1"))
	})
}
