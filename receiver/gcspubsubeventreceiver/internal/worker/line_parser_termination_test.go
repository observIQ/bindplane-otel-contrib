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

package worker_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/observiq/bindplane-otel-contrib/receiver/gcspubsubeventreceiver/internal/worker"
)

// yieldBudget caps the iterations a test consumer accepts. A parser that does not
// terminate spins until the go test timeout.
const yieldBudget = 1000

// errAfterReader serves prefix, then returns err on every later read.
type errAfterReader struct {
	prefix []byte
	err    error
}

func (r *errAfterReader) Read(p []byte) (int, error) {
	if len(r.prefix) > 0 {
		n := copy(p, r.prefix)
		r.prefix = r.prefix[n:]
		return n, nil
	}
	return 0, r.err
}

// drain consumes the sequence and continues past errors, as processObject does.
// exhausted reports that the sequence ran past yieldBudget.
func drain(t *testing.T, logs func(func(any, error) bool)) (records []any, errs []error, exhausted bool) {
	t.Helper()

	iterations := 0
	for record, err := range logs {
		iterations++
		if iterations > yieldBudget {
			exhausted = true
			break
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		records = append(records, record)
	}
	return records, errs, exhausted
}

func TestLineParser_TerminatesOnPersistentReadError(t *testing.T) {
	t.Parallel()

	readErr := errors.New("connection reset by peer")
	reader := &errAfterReader{prefix: []byte("first\nsecond\n"), err: readErr}
	parser := worker.NewLineParser(worker.NewBufferedReader(reader, 4096))

	logs, err := parser.Parse(context.Background(), 0)
	require.NoError(t, err)

	records, errs, exhausted := drain(t, logs)

	require.False(t, exhausted, "parser did not terminate on a persistent read error")
	require.Equal(t, []any{"first", "second"}, records, "records read before the error should still be yielded")
	require.Len(t, errs, 1, "the terminal read error should be yielded exactly once")
	require.ErrorIs(t, errs[0], readErr)
	require.True(t, worker.IsStreamRead(errs[0]),
		"a broken stream must be marked so the caller fails the object instead of acking a partial read")
}

func TestLineParser_TerminatesOnCancelledContext(t *testing.T) {
	t.Parallel()

	reader := &errAfterReader{prefix: []byte("first\n"), err: context.Canceled}
	parser := worker.NewLineParser(worker.NewBufferedReader(reader, 4096))

	logs, err := parser.Parse(context.Background(), 0)
	require.NoError(t, err)

	records, errs, exhausted := drain(t, logs)

	require.False(t, exhausted, "parser did not terminate on a cancelled context")
	require.Equal(t, []any{"first"}, records)
	require.Len(t, errs, 1)
	require.ErrorIs(t, errs[0], context.Canceled)
}

func TestLineParser_TerminatesOnExceededDeadline(t *testing.T) {
	t.Parallel()

	reader := &errAfterReader{prefix: []byte("first\n"), err: context.DeadlineExceeded}
	parser := worker.NewLineParser(worker.NewBufferedReader(reader, 4096))

	logs, err := parser.Parse(context.Background(), 0)
	require.NoError(t, err)

	_, errs, exhausted := drain(t, logs)

	require.False(t, exhausted, "parser did not terminate on an exceeded deadline")
	require.Len(t, errs, 1)
	require.ErrorIs(t, errs[0], context.DeadlineExceeded)
}

func TestLineParser_TerminatesOnWrappedEOF(t *testing.T) {
	t.Parallel()

	reader := &errAfterReader{prefix: []byte("first\nsecond\n"), err: fmt.Errorf("decompress: %w", io.EOF)}
	parser := worker.NewLineParser(worker.NewBufferedReader(reader, 4096))

	logs, err := parser.Parse(context.Background(), 0)
	require.NoError(t, err)

	records, errs, exhausted := drain(t, logs)

	require.False(t, exhausted, "parser did not terminate on a wrapped io.EOF")
	require.Equal(t, []any{"first", "second"}, records)
	require.Empty(t, errs, "a wrapped io.EOF is the end of the stream rather than a parse failure")
}
