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
	"fmt"
	"io"
	"iter"
	"slices"

	"github.com/linkedin/goavro/v2"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.uber.org/zap"
)

const (
	avroOcfMagicString = "Obj\x01"
)

var (
	avroOcfMagicBytes = []byte(avroOcfMagicString)
)

type avroOcfParser struct {
	reader  BufferedReader
	logger  *zap.Logger
	counter int64
}

var _ LogParser = (*avroOcfParser)(nil)

// NewAvroOcfParser creates a new Avro OCF parser. Before attempting to parse the stream,
// call StartsWithAvroOcfMagic to check if the stream starts with the Avro OCF magic
// string.
func NewAvroOcfParser(reader BufferedReader, logger *zap.Logger) LogParser {
	return &avroOcfParser{
		reader:  reader,
		logger:  logger,
		counter: 0,
	}
}

// StartsWithAvroOcfMagic checks if the reader starts with the Avro OCF magic string.
// A stream shorter than the magic (io.EOF from Peek) simply is not Avro, so that
// case returns (false, nil) rather than an error.
func StartsWithAvroOcfMagic(reader BufferedReader) (bool, error) {
	bytes, err := reader.Peek(len(avroOcfMagicBytes))
	if err != nil {
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		return false, err
	}
	return slices.Equal(bytes, avroOcfMagicBytes), nil
}

// Parse parses the avro ocf stream into a sequence of log records. An avro ocf stream
// starts with the schema and then contains blocks of records. The parser will return a
// sequence of records from the ocf reader or an error if a new ocf reader cannot be
// created. The parser will skip the records before the startOffset and expects an offset
// in the number of records read so far.
func (p *avroOcfParser) Parse(_ context.Context, startOffset int64) (logs iter.Seq2[any, error], err error) {
	ocfReader, err := goavro.NewOCFReader(p.reader)
	if err != nil {
		// The header failed to decode. Classify it the same way the scan end does: a
		// broken stream retries, a header cut short is a truncated object, and anything
		// else is a container this decoder cannot read. A header larger than the detection
		// window can break mid-read here, so the raw failure is not always caught earlier.
		if p.reader.ReadErr() != nil {
			return nil, ErrStreamRead{Err: p.reader.ReadErr()}
		}
		if p.reader.AtEOF() {
			return nil, ErrTruncatedObject{Err: err}
		}
		return nil, ErrCorruptContainer{Format: "avro", Err: err}
	}

	// yield a sequence of records from the ocf reader
	return func(yield func(any, error) bool) {
		for ocfReader.Scan() {
			record, err := ocfReader.Read()

			if err != nil {
				currentOffset := p.Offset()
				p.counter += ocfReader.RemainingBlockItems()

				// A record failed to decode. Avro binary has no per-record framing, so the
				// position of every later record in the block is lost: goavro can only skip to
				// the next block, and those records are unrecoverable (a line stream would
				// resync). Records before the failure were already yielded. Treat it as a
				// per-record error, gated on currentOffset (the failing record's position,
				// mirroring the other parsers) so a block already reported before the resume
				// checkpoint is not re-counted. Correctness of that advance relies on goavro
				// never decrementing RemainingBlockItems on a failed Read (so counter always
				// moves past the bad block); a resume landing exactly on a block boundary can
				// re-count this one error, accepted as metric-only.
				if currentOffset >= startOffset {
					if !yield(nil, fmt.Errorf("decode avro record: %w", err)) {
						return
					}
				}
				p.logger.Error("avro ocf read error, skipping block",
					zap.Error(err),
					zap.Int64("offset", currentOffset),
					zap.Int64("new_offset", p.Offset()),
					zap.Int64("remaining_block_items", ocfReader.RemainingBlockItems()),
				)
				ocfReader.SkipThisBlockAndReset()
				continue
			}

			// skip if we are still before the startOffset
			p.counter++
			if p.counter <= startOffset {
				continue
			}

			// yield the avro record
			if !yield(record, nil) {
				return
			}
		}

		// Scan stops at the end of the object and on failure alike, and keeps the
		// reason to itself. Without this check a broken stream looks like a clean
		// finish, and every record after the break is lost with no report.
		//
		// The reader tells the three cases apart. A recorded failure means the
		// stream broke. Reaching the end of the object means the scan finished,
		// because the decoder reports its own end-of-input the same way it reports
		// a fault. Anything else stopped early on content it could not decode.
		scanErr := ocfReader.Err()
		switch {
		case scanErr == nil:
			// The decoder read the object to its end.
		case p.reader.ReadErr() != nil:
			// The stream broke, so a later attempt can still read the object.
			yield(nil, ErrStreamRead{Err: p.reader.ReadErr()})
		case p.reader.AtEOF():
			// The bytes ran out part way through. They were never written, so a
			// later attempt reads the same object again.
			yield(nil, ErrTruncatedObject{Err: scanErr})
		default:
			// The decoder stopped early on content it cannot read.
			yield(nil, ErrCorruptContainer{Format: "avro", Err: scanErr})
		}
	}, nil
}

// Offset returns the number of records read so far. We use a counter instead of an offset
// in the reader because the avro library will read an entire block of records at a time,
// and we want to track the number of records read so far so that we can skip the records
// before the startOffset.
func (p *avroOcfParser) Offset() int64 {
	return p.counter
}

// AppendLogBody appends the avro record to the log record.
func (p *avroOcfParser) AppendLogBody(_ context.Context, lr plog.LogRecord, record any) error {
	return lr.Body().FromRaw(record)
}
