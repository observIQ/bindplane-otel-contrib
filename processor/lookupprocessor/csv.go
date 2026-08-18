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
	data         atomic.Pointer[index]

	// readHook runs after a reload has built the new index but before it is
	// published, so a test can hold a reload in flight and observe that lookups
	// proceed against the still-published old index. It is nil in production.
	readHook func()
}

// Load reads the csv and publishes a freshly built index.
func (c *CSVFile) Load() error {
	file, err := os.Open(c.filepath)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	records, err := reader.ReadAll()
	if err != nil {
		return fmt.Errorf("read all: %w", err)
	}

	data, err := indexRecords(records, c.lookupColumn)
	if err != nil {
		return fmt.Errorf("index records: %w", err)
	}

	if c.readHook != nil {
		c.readHook()
	}

	c.data.Store(&data)
	return nil
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
func NewCSVFile(filepath string, lookupColumn string) *CSVFile {
	return &CSVFile{
		filepath:     filepath,
		lookupColumn: lookupColumn,
	}
}
