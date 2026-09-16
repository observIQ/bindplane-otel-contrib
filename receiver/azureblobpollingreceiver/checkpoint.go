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
	"encoding/json"
	"time"

	"github.com/observiq/bindplane-otel-contrib/internal/blobconsume"
	"github.com/observiq/bindplane-otel-contrib/internal/storageclient"
)

// BlobProgress records incremental read progress for a single append-growable blob.
type BlobProgress struct {
	// Offset is the number of bytes already consumed from the blob.
	Offset int64 `json:"offset"`

	// Fingerprint is the leading bytes of the blob's committed prefix, grown toward
	// fingerprint_size as the blob grows, used to detect replacement/truncation: if a later
	// read's fingerprint no longer matches, the blob is a different one and the offset is reset.
	Fingerprint []byte `json:"fingerprint,omitempty"`

	// LastModified is the blob's modified time as of the last read. A poll skips a
	// blob whose LastModified is unchanged (nothing new written), and prunes the
	// entry once LastModified falls out of the revisit horizon.
	LastModified time.Time `json:"last_modified"`

	// Quarantined marks a sealed blob whose content could not be parsed. Its entry is
	// kept (not deleted, not re-flushed) so the blob is ignored while unchanged; a
	// later read that finds a new LastModified overwrites the entry and clears this.
	Quarantined bool `json:"quarantined,omitempty"`

	// FinalizeFailures counts consecutive seal-time flush/delete failures; past a
	// threshold the blob is quarantined instead of retried forever.
	FinalizeFailures int `json:"finalize_failures,omitempty"`

	// Sealed marks a blob cleanly finalized but not deleted (delete_on_read off). Its entry is
	// kept with the final offset so a later append (new LastModified) resumes from there rather
	// than re-reading from the start; like Quarantined it is not re-flushed and prunes at the horizon.
	Sealed bool `json:"sealed,omitempty"`

	// LastReadError (with Offset 0) marks a blob every read/consume attempt has failed, keeping the
	// otherwise-untracked blob visible so its drop is logged with this reason if it ages out still
	// never read. A successful read overwrites the entry, clearing it.
	LastReadError string `json:"last_read_error,omitempty"`
}

// PollingCheckPoint extends the basic checkpoint to include lastPollTime for continuous polling
type PollingCheckPoint struct {
	blobconsume.CheckPoint // Embed the base checkpoint

	// LastPollTime is the timestamp of the last successful poll
	// This is used to determine the starting time for the next poll window
	LastPollTime time.Time `json:"last_poll_time"`

	// Progress tracks per-blob incremental read progress for append-growable
	// formats (records-json, json), keyed by blob name. For those formats
	// it replaces the name/wall-clock dedup: a blob is re-read whenever its
	// LastModified changes, only its new bytes are consumed, and its fingerprint
	// guards against a replaced blob resuming at a stale offset. Entries are
	// pruned once a blob ages out of the revisit horizon.
	Progress map[string]BlobProgress `json:"progress"`
}

// PollingCheckPoint implements the StorageData interface
var _ storageclient.StorageData = &PollingCheckPoint{}

// NewPollingCheckpoint creates a new PollingCheckPoint
func NewPollingCheckpoint() *PollingCheckPoint {
	return &PollingCheckPoint{
		CheckPoint:   *blobconsume.NewCheckpoint(),
		LastPollTime: time.Time{},
		Progress:     make(map[string]BlobProgress),
	}
}

// UpdatePollTime records pollTime as the end of the last successful poll; the next poll uses
// it as the start of its time window.
func (c *PollingCheckPoint) UpdatePollTime(pollTime time.Time) {
	c.LastPollTime = pollTime
}

// ProgressFor returns the stored read progress for the named blob and whether an
// entry exists.
func (c *PollingCheckPoint) ProgressFor(blobName string) (BlobProgress, bool) {
	p, ok := c.Progress[blobName]
	return p, ok
}

// UpdateProgress records the read progress for the named blob.
func (c *PollingCheckPoint) UpdateProgress(blobName string, p BlobProgress) {
	if c.Progress == nil {
		c.Progress = make(map[string]BlobProgress)
	}
	c.Progress[blobName] = p
}

// AgedOutProgress removes entries whose LastModified is before cutoff (aged out of the revisit
// horizon, never listed again) and returns two maps: aged, entries with final work to do (tail
// flush, delete-on-read), and giveUp, entries whose finalize retries are exhausted and are dropped
// so the caller can log it. Sealing on mtime is safe even for a first-seen blob: mtime advances on
// every append, so an old mtime means growth stopped.
//
// quarantinePruneCutoff/sealedPruneCutoff bound the map, dropping a quarantined/sealed entry once
// its LastModified passes the matching cutoff. They track the listing window so an entry usually
// survives while its blob is still listable (path-time modes with large path-time/mtime skew are
// the exception, per settled tradeoff), and are separate to prune sealed on a different schedule.
func (c *PollingCheckPoint) AgedOutProgress(cutoff, quarantinePruneCutoff, sealedPruneCutoff time.Time) (aged, giveUp map[string]BlobProgress) {
	for name, p := range c.Progress {
		// Quarantined (unparseable) and Sealed (cleanly finalized, not deleted) entries stay
		// put — ignored, not re-flushed — until a new LastModified brings the blob back
		// through the normal read path, or until they pass their prune cutoff, at which point
		// they are dropped to bound the map.
		if p.Quarantined {
			if p.LastModified.Before(quarantinePruneCutoff) {
				delete(c.Progress, name)
			}
			continue
		}
		if p.Sealed {
			if p.LastModified.Before(sealedPruneCutoff) {
				delete(c.Progress, name)
			}
			continue
		}
		// Zero LastModified means the listing reported no mtime; don't seal a blob of unknown age.
		// Safe: shouldParseBlob skips zero-mtime blobs, so no Progress entry ever carries a zero one.
		if p.LastModified.IsZero() {
			continue
		}
		if p.LastModified.Before(cutoff) {
			// Give up on a finalize that kept failing past quarantineRetention before the aging
			// cutoff (bounds retries + the map). Anchored to the aging cutoff, not the listing
			// window, so a window >= the retention can't collapse it to one attempt.
			if p.FinalizeFailures > 0 && p.LastModified.Before(cutoff.Add(-quarantineRetention)) {
				if giveUp == nil {
					giveUp = make(map[string]BlobProgress)
				}
				giveUp[name] = p
				delete(c.Progress, name)
				continue
			}
			if aged == nil {
				aged = make(map[string]BlobProgress)
			}
			aged[name] = p
			delete(c.Progress, name)
		}
	}
	return aged, giveUp
}

// Marshal implements the StorageData interface
func (c *PollingCheckPoint) Marshal() ([]byte, error) {
	return json.Marshal(c)
}

// Unmarshal implements the StorageData interface
// If the data is empty, it returns nil
func (c *PollingCheckPoint) Unmarshal(data []byte) error {
	if len(data) == 0 {
		return nil
	}

	return json.Unmarshal(data, c)
}
