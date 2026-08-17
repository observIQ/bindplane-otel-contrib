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
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"iter"

	"go.opentelemetry.io/collector/pdata/plog"
)

type lineParser struct {
	reader BufferedReader
	opts   BodyOptions

	// offset is the position after the last record sent to the consumer. It differs
	// from the reader position. A truncated record is read but never emitted.
	offset int64
}

// NewLineParser creates a new line parser.
func NewLineParser(reader BufferedReader, opts BodyOptions) LogParser {
	return &lineParser{
		reader: reader,
		opts:   opts,
	}
}

func (p *lineParser) Offset() int64 {
	return p.offset
}

// Parse parses the log records from the reader using ReadLine.
func (p *lineParser) Parse(_ context.Context, startOffset int64) (logs iter.Seq2[any, error], err error) {
	// skip to the start offset
	_, err = io.CopyN(io.Discard, p.reader, startOffset)
	if err != nil {
		return nil, fmt.Errorf("discard to offset: %w", err)
	}
	p.offset = p.reader.Offset()

	// logs is a sequence of log records that can be used with the provided appender
	return func(yield func(any, error) bool) {
		for {
			lineBytes, _, err := readLine(p.reader)
			if err != nil {
				// A decompressing reader can wrap the sentinel.
				if errors.Is(err, io.EOF) {
					return
				}
				// An object that stops mid-record is truncated rather than
				// unreachable. A decompressor reports a live connection failure
				// as that failure, so this only fires on missing bytes.
				if errors.Is(err, io.ErrUnexpectedEOF) {
					yield(nil, ErrTruncatedObject{Err: err})
					return
				}
				// A read error is terminal. Every later ReadLine returns the same
				// error, so continuing spins forever. It is marked so the caller
				// fails the object instead of acking a partial read.
				yield(nil, ErrStreamRead{Err: err})
				return
			}

			// Advance past consumed bytes, even for an empty line.
			p.offset = p.reader.Offset()

			// only yield non-empty lines
			if len(lineBytes) > 0 {
				if !yield(string(lineBytes), nil) {
					return
				}
			}
		}
	}, nil
}

// readLine is bufio.Reader.ReadLine with the read error preserved.
//
// ReadLine returns (line, nil) when it holds bytes, even after a failed read. A
// truncated record then looks like a final record with no newline.
func readLine(r BufferedReader) (line []byte, isPrefix bool, err error) {
	line, err = r.ReadSlice('\n')

	if errors.Is(err, bufio.ErrBufferFull) {
		// A record longer than the buffer, split at max_log_size. Return a trailing
		// '\r' to the buffer, so the next chunk can find a split "\r\n".
		if len(line) > 0 && line[len(line)-1] == '\r' {
			if unreadErr := r.UnreadByte(); unreadErr != nil {
				return nil, false, unreadErr
			}
			line = line[:len(line)-1]
		}
		return line, true, nil
	}

	if err != nil {
		// Bytes with no terminator. At end of stream they are the final record. Under
		// any other error they are a fragment, so drop them.
		if errors.Is(err, io.EOF) && len(line) > 0 {
			return line, false, nil
		}
		return nil, false, err
	}

	return trimLineEnding(line), false, nil
}

// trimLineEnding drops the trailing newline, and a "\r" before it.
func trimLineEnding(line []byte) []byte {
	if len(line) > 0 && line[len(line)-1] == '\n' {
		drop := 1
		if len(line) > 1 && line[len(line)-2] == '\r' {
			drop = 2
		}
		line = line[:len(line)-drop]
	}
	return line
}

// AppendLogBody appends the log record to the log record body using SetStr. A line is
// already its own original text, so the body and log.record.original match.
func (p *lineParser) AppendLogBody(_ context.Context, lr plog.LogRecord, record any) error {
	str, ok := record.(string)
	if !ok {
		return fmt.Errorf("expected string record, got %T", record)
	}
	lr.Body().SetStr(str)
	p.opts.setOriginal(lr, str)
	return nil
}
