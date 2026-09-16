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

package snapshot

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func TestAdmission(t *testing.T) {
	t.Run("zero interval is never exhausted", func(t *testing.T) {
		var a admission
		a.init(0, 3)
		a.charge(100)
		require.False(t, a.exhausted())
	})

	t.Run("budget per window", func(t *testing.T) {
		var a admission
		a.init(time.Hour, 3)
		require.False(t, a.exhausted())
		a.charge(3)
		require.True(t, a.exhausted())
		require.True(t, a.exhausted())

		// Rewind the window past the interval: budget resets.
		a.windowNs.Store(0)
		require.False(t, a.exhausted())
		a.charge(1)
		require.False(t, a.exhausted())
		a.charge(2)
		require.True(t, a.exhausted())
	})

	t.Run("concurrent rollover is race free and bounded", func(t *testing.T) {
		var a admission
		a.init(time.Hour, 1)
		a.charge(1)
		a.windowNs.Store(0)

		var admitted atomic.Int32
		var wg sync.WaitGroup
		for i := 0; i < 64; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if !a.exhausted() {
					admitted.Add(1)
					a.charge(1)
				}
			}()
		}
		wg.Wait()
		// One caller rolls the window over and spends the budget; the rest are
		// rejected. Callers that load the budget in the instant between the
		// reset and the charge may also be admitted, so the bound is loose.
		require.GreaterOrEqual(t, admitted.Load(), int32(1))
		require.Less(t, admitted.Load(), int32(64))
		require.NotZero(t, a.windowNs.Load())
	})
}

func TestBufferAddBudgetPerInterval(t *testing.T) {
	t.Run("logs", func(t *testing.T) {
		buf := NewLogBuffer(3, WithRefreshInterval(time.Hour))

		for i := 1; i <= 3; i++ {
			buf.Add(logsWithBody(fmt.Sprintf("fill-%d", i)))
		}
		require.Equal(t, 3, buf.Len())

		// Budget spent inside the interval: ignored.
		buf.Add(logsWithBody("late"))
		require.Equal(t, []string{"fill-1", "fill-2", "fill-3"}, logBodies(t, buf))

		// Past the interval: another buffer's worth is admitted, oldest evicted.
		buf.admit.windowNs.Store(0)
		buf.Add(logsWithBody("late"))
		buf.Add(logsWithBody("later"))
		buf.Add(logsWithBody("last"))
		require.Equal(t, []string{"late", "later", "last"}, logBodies(t, buf))

		// And the budget is spent again.
		buf.Add(logsWithBody("nope"))
		require.Equal(t, []string{"late", "later", "last"}, logBodies(t, buf))
	})

	t.Run("large batch charges only what is kept", func(t *testing.T) {
		buf := NewLogBuffer(3, WithRefreshInterval(time.Hour))
		ld := plog.NewLogs()
		lrs := ld.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords()
		for i := 0; i < 10; i++ {
			lrs.AppendEmpty().Body().SetStr(fmt.Sprintf("r-%d", i))
		}
		buf.Add(ld)
		require.Equal(t, 3, buf.Len())
		require.Equal(t, int64(3), buf.admit.used.Load())
		require.True(t, buf.admit.exhausted())
	})

	t.Run("metrics", func(t *testing.T) {
		buf := NewMetricBuffer(2, WithRefreshInterval(time.Hour))
		md := pmetric.NewMetrics()
		md.ResourceMetrics().AppendEmpty().ScopeMetrics().AppendEmpty().Metrics().AppendEmpty().SetEmptyGauge().DataPoints().AppendEmpty()

		buf.Add(md)
		buf.Add(md)
		require.Equal(t, 2, buf.Len())
		require.True(t, buf.admit.exhausted())
		buf.admit.windowNs.Store(0)
		require.False(t, buf.admit.exhausted())
		buf.Add(md)
		require.Equal(t, 2, buf.Len())
	})

	t.Run("traces", func(t *testing.T) {
		buf := NewTraceBuffer(2, WithRefreshInterval(time.Hour))
		td := ptrace.NewTraces()
		td.ResourceSpans().AppendEmpty().ScopeSpans().AppendEmpty().Spans().AppendEmpty()

		buf.Add(td)
		buf.Add(td)
		require.Equal(t, 2, buf.Len())
		require.True(t, buf.admit.exhausted())
	})

	t.Run("defaults are applied", func(t *testing.T) {
		l := NewLogBuffer(7)
		require.Equal(t, DefaultRefreshInterval, l.admit.interval)
		require.Equal(t, int64(7), l.admit.budget)
		require.Equal(t, DefaultRefreshInterval, NewMetricBuffer(1).admit.interval)
		require.Equal(t, DefaultRefreshInterval, NewTraceBuffer(1).admit.interval)
	})
}

// TestBufferAddRejectedDoesNotReadPayload guards the cheap rejected path: a
// rejected payload must not be walked or copied, so it stays O(1).
func TestBufferAddRejectedDoesNotReadPayload(t *testing.T) {
	buf := NewLogBuffer(1, WithRefreshInterval(time.Hour))
	buf.Add(logsWithBody("fill"))

	big := plog.NewLogs()
	sl := big.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty()
	for i := 0; i < 10_000; i++ {
		lr := sl.LogRecords().AppendEmpty()
		lr.Body().SetStr("payload")
		lr.Attributes().PutStr("k", "v")
	}

	allocs := testing.AllocsPerRun(100, func() { buf.Add(big) })
	require.Zero(t, allocs)
	require.Equal(t, []string{"fill"}, logBodies(t, buf))
}

func logsWithBody(body string) plog.Logs {
	ld := plog.NewLogs()
	lr := ld.ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	lr.Body().SetStr(body)
	lr.SetObservedTimestamp(pcommon.NewTimestampFromTime(time.Now()))
	return ld
}

func logBodies(t *testing.T, buf *LogBuffer) []string {
	payload, err := buf.ConstructPayload(&plog.ProtoMarshaler{}, nil, nil, 1024*1024)
	require.NoError(t, err)
	actual, err := (&plog.ProtoUnmarshaler{}).UnmarshalLogs(payload)
	require.NoError(t, err)

	var bodies []string
	for ri := 0; ri < actual.ResourceLogs().Len(); ri++ {
		sls := actual.ResourceLogs().At(ri).ScopeLogs()
		for si := 0; si < sls.Len(); si++ {
			lrs := sls.At(si).LogRecords()
			for li := 0; li < lrs.Len(); li++ {
				bodies = append(bodies, lrs.At(li).Body().Str())
			}
		}
	}
	return bodies
}
