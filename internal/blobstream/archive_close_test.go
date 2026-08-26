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
	"io"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// closeCountingBackend counts Close calls so a test can verify a materialized backend is
// released. Its optional closeErr drives the close-error branch.
type closeCountingBackend struct {
	closed   int
	closeErr error
}

func (b *closeCountingBackend) Next() (archiveEntry, error) { return nil, io.EOF }
func (b *closeCountingBackend) Close() error                { b.closed++; return b.closeErr }

// Materialized satisfies archiveBackend once later commits add the method; it is unused by
// these tests (the iterator is never driven), so its value does not matter.
func (b *closeCountingBackend) Materialized() bool { return true }

func newBackendProducer(open func() (archiveBackend, error)) *archiveProducer {
	return &archiveProducer{
		stream: LogStream{Logger: zap.NewNop()},
		open:   open,
		limits: defaultArchiveLimits(),
	}
}

// TestArchiveProducer_CloseReleasesUndrivenBackend asserts Close (via CloseProducer)
// releases a backend whose Records iterator was never driven — the temp-file leak Caleb
// flagged — and that a second call is a no-op.
func TestArchiveProducer_CloseReleasesUndrivenBackend(t *testing.T) {
	b := &closeCountingBackend{}
	p := newBackendProducer(func() (archiveBackend, error) { return b, nil })

	_, err := p.Records(context.Background(), Offset{})
	require.NoError(t, err)
	require.Zero(t, b.closed, "an undriven iterator must not close the backend on its own")

	CloseProducer(p)
	require.Equal(t, 1, b.closed, "CloseProducer must release the undriven backend")

	CloseProducer(p)
	require.Equal(t, 1, b.closed, "Close is idempotent")
}

// TestArchiveProducer_RecordsTwiceClosesFirstBackend asserts a second Records call releases
// the first backend rather than leaking it.
func TestArchiveProducer_RecordsTwiceClosesFirstBackend(t *testing.T) {
	first := &closeCountingBackend{closeErr: errors.New("close boom")} // also covers the close-error log branch
	second := &closeCountingBackend{}
	backends := []*closeCountingBackend{first, second}
	i := 0
	p := newBackendProducer(func() (archiveBackend, error) {
		b := backends[i]
		i++
		return b, nil
	})

	_, err := p.Records(context.Background(), Offset{})
	require.NoError(t, err)
	_, err = p.Records(context.Background(), Offset{})
	require.NoError(t, err)
	require.Equal(t, 1, first.closed, "a second Records call must release the first backend")
}

// TestCloseProducer_NonCloserIsNoOp asserts CloseProducer safely ignores a producer that
// holds no closable resources (the non-archive path).
func TestCloseProducer_NonCloserIsNoOp(t *testing.T) {
	require.NotPanics(t, func() { CloseProducer(&singleParserProducer{}) })
}
