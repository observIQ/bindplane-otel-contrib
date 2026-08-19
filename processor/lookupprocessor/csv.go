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
	"encoding/csv"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

var (
	// errCSVNotLoaded is the error for when the csv is not loaded
	errCSVNotLoaded = errors.New("csv not loaded")
	// errKeyNotFound is the error for when the key is not found
	errKeyNotFound = errors.New("key not found")
	// errNoRecords is the error for when there are no records to parse
	errNoRecords = errors.New("no records to parse")
	// errLookupColumnNotFound is the error for when the lookup column is not found
	errLookupColumnNotFound = errors.New("lookup column not found")
)

// index maps a lookup key to the remaining columns of its row.
type index map[string]map[string]string

// CSVFile is a file that contains csv data.
//
// The index is published through an atomic pointer rather than guarded by a
// lock, so a reload never blocks concurrent lookups. Load builds the whole
// index off to the side and swaps it in with a single Store, which leaves
// readers observing either the complete previous index or the complete new
// one, never a partial rebuild.
type CSVFile struct {
	filepath     string
	lookupColumn string
	logger       *zap.Logger
	data         atomic.Pointer[index]

	// stamp identifies the file version behind the published index, so an
	// unchanged file can be skipped without re-reading it. stampSettled records
	// whether that stamp was taken late enough after the file's own modification
	// time to identify the content unambiguously. Only Load writes them, and Load
	// runs on a single goroutine.
	stamp        fileStamp
	stampSettled bool

	// now reports the current time, and is a seam so a test can drive the
	// stamp-settle check off a fixed clock; it defaults to time.Now. reads and
	// readHook exist only for tests: reads counts completed file reads, and
	// readHook runs between a read and the re-stat that follows it. readHook is
	// nil in production.
	reads    atomic.Int64
	readHook func()
	now      func() time.Time
}

// stampSettleWindow is how long a file's modification time must already be in the
// past before an equal stamp is trusted to mean the content is unchanged.
//
// A write takes its modification time from a coarse clock, so two writes landing
// close together carry byte-identical timestamps. When they also produce the same
// size, the first write's stamp is indistinguishable from the second's, and
// skipping on it would serve the earlier content for as long as the file then sat
// still. Re-reading once while the modification time is still this fresh closes
// that window, and costs one extra read per write rather than one per interval.
// The window covers the coarsest timestamp granularity still in common use, FAT's
// two seconds, so it comfortably covers the millisecond-scale granularity that
// Linux filesystems report.
const stampSettleWindow = 2 * time.Second

// fileStamp identifies a file version cheaply. It relies on a write advancing the
// modification time, which holds on any filesystem storing mtime at sub-second
// resolution. Two writes can still share a timestamp when they land in the same
// clock tick, so a stamp only identifies content once its modification time has
// settled; see stampSettleWindow. A rewrite that deliberately restores the
// previous timestamp and keeps the same size would read as unchanged.
type fileStamp struct {
	modTime time.Time
	size    int64
}

func stampOf(fi os.FileInfo) fileStamp {
	return fileStamp{modTime: fi.ModTime(), size: fi.Size()}
}

// equal compares two stamps. It uses time.Equal rather than ==, since == on a
// time.Time also compares the monotonic reading and the location pointer, which
// would report a difference for the same instant and defeat the skip.
func (s fileStamp) equal(o fileStamp) bool {
	return s.size == o.size && s.modTime.Equal(o.modTime)
}

// Load publishes a freshly built index, skipping the read entirely when the file
// has not changed since the last successful load.
func (c *CSVFile) Load() error {
	before, err := os.Stat(c.filepath)
	if err != nil {
		return fmt.Errorf("stat file: %w", err)
	}

	stamp := stampOf(before)
	if c.data.Load() != nil && c.stampSettled && stamp.equal(c.stamp) {
		c.logger.Debug("csv unchanged, skipping reload", zap.String("path", c.filepath))
		return nil
	}

	data, err := c.readIndex()
	if err != nil {
		return err
	}

	// A rewrite landing mid-read can produce a truncated file that still parses
	// cleanly, which would publish a partial index. Re-stat and read once more
	// when the file moved under us. Writers that rename a completed file into
	// place avoid the window entirely, since the swap is then atomic.
	after, err := os.Stat(c.filepath)
	if err != nil {
		return fmt.Errorf("re-stat file: %w", err)
	}
	if retryStamp := stampOf(after); !retryStamp.equal(stamp) {
		c.logger.Debug("csv changed while being read, retrying", zap.String("path", c.filepath))
		stamp = retryStamp
		if data, err = c.readIndex(); err != nil {
			return err
		}
	}

	c.data.Store(&data)
	c.stamp = stamp
	// now is unset only on a CSVFile built without NewCSVFile; fall back so that
	// path publishes rather than panics.
	now := c.now
	if now == nil {
		now = time.Now
	}
	c.stampSettled = now().Sub(stamp.modTime) >= stampSettleWindow
	return nil
}

// readIndex reads the file and builds an index from it, without publishing.
func (c *CSVFile) readIndex() (index, error) {
	file, err := os.Open(c.filepath)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read all: %w", err)
	}

	data, err := indexRecords(records, c.lookupColumn)
	if err != nil {
		return nil, fmt.Errorf("index records: %w", err)
	}

	c.reads.Add(1)
	if c.readHook != nil {
		c.readHook()
	}

	return data, nil
}

// Lookup returns a row of data that matches the key in the provided column.
// The context is accepted to satisfy the LookupSource interface; CSV lookups
// are in-memory and do not block on it.
func (c *CSVFile) Lookup(_ context.Context, key string) (map[string]string, error) {
	data := c.data.Load()
	if data == nil {
		return nil, errCSVNotLoaded
	}

	results, ok := (*data)[key]
	if !ok {
		return nil, errKeyNotFound
	}

	return results, nil
}

// indexRecords indexes the records by the lookup column
func indexRecords(records [][]string, lookupColumn string) (index, error) {
	if len(records) == 0 {
		return nil, errNoRecords
	}

	headers := records[0]
	lookupIndex, err := findLookupIndex(headers, lookupColumn)
	if err != nil {
		return nil, fmt.Errorf("find lookup index: %w", err)
	}

	result := make(index)
	for _, record := range records[1:] {
		lookupKey := record[lookupIndex]
		result[lookupKey] = make(map[string]string)
		for i, value := range record {
			// Skip the lookup column
			if i == lookupIndex {
				continue
			}

			result[lookupKey][headers[i]] = value
		}
	}

	return result, nil
}

// findLookupIndex finds the index of the lookup column
func findLookupIndex(headers []string, lookupColumn string) (int, error) {
	for i, header := range headers {
		if header == lookupColumn {
			return i, nil
		}
	}

	return -1, errLookupColumnNotFound
}

// Close releases resources held by the CSVFile. CSV data is loaded on demand
// and the file is closed after each Load, so this is a no-op kept for
// LookupSource interface compliance.
func (c *CSVFile) Close() error {
	return nil
}

// NewCSVFile creates a new CSVFile
func NewCSVFile(filepath string, lookupColumn string, logger *zap.Logger) *CSVFile {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &CSVFile{
		filepath:     filepath,
		lookupColumn: lookupColumn,
		logger:       logger,
		now:          time.Now,
	}
}
