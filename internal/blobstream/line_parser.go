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
	if _, err = io.CopyN(io.Discard, p.reader, startOffset); err != nil {
		// The object is shorter than the saved resume offset: it was truncated or
		// rewritten smaller. A bare error here redelivers the object forever, so
		// classify it. A clean EOF while skipping means the stored object no longer
		// reaches the offset, so treat it as truncation; classifyReadFailure routes a
		// broken or short download to retry instead.
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return nil, classifyReadFailure(p.reader, err)
	}
	p.offset = p.reader.Offset()

	// logs is a sequence of log records that can be used with the provided appender
	return func(yield func(any, error) bool) {
		for {
			lineBytes, _, err := readLine(p.reader)
			if err != nil {
				// End of stream. readLine returns io.EOF with any trailing bytes that
				// had no newline. If the raw source broke or ended short of the
				// object's known size, those bytes are a cut record, not a real final
				// line: drop them and report the truncation so the worker retries or
				// dead-letters rather than acking a partial object. A decompressing
				// reader can wrap the sentinel, so errors.Is is used.
				if errors.Is(err, io.EOF) {
					if p.reader.RawReadErr() != nil || p.reader.RawTruncated() {
						yield(nil, classifyReadFailure(p.reader, io.ErrUnexpectedEOF))
						return
					}
					// Clean end: trailing bytes with no newline are a legitimate final
					// record.
					if len(lineBytes) > 0 {
						p.offset = p.reader.Offset()
						yield(string(lineBytes), nil)
					}
					return
				}
				// A read error is terminal. Every later ReadLine returns the same
				// error, so continuing spins forever. classifyReadFailure sorts a
				// broken source stream (retry) from a truncated object and from
				// content the decompressor rejected (both terminal, never retried).
				yield(nil, classifyReadFailure(p.reader, err))
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
// At end of stream it returns io.EOF together with any trailing bytes that had no
// newline, so the caller can tell a clean final line from a record cut short by a broken
// or truncated source. Under any other read error it drops the partial bytes.
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
		// Bytes with no terminator. At end of stream, hand them back with the io.EOF
		// sentinel so the caller can decide whether they are a legitimate final line or
		// a record cut short. Under any other error they are a fragment, so drop them.
		if errors.Is(err, io.EOF) && len(line) > 0 {
			// A final line whose '\n' was never written may still carry a stray trailing
			// '\r' (a CRLF stream cut after the '\r'); drop it so the body matches every
			// other line rather than delivering "last\r". The io.EOF sentinel is returned
			// with the bytes so Parse can tell a clean final line from a cut record.
			if line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			return line, false, err
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
