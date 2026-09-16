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

package blobconsume //import "github.com/observiq/bindplane-otel-contrib/internal/blobconsume"

import (
	"context"
	"errors"
	"testing"

	"github.com/observiq/bindplane-otel-contrib/internal/testutils"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.uber.org/zap"
)

func Test_metricsConsumer(t *testing.T) {
	testConsumer := &consumertest.MetricsSink{}
	con := NewMetricsConsumer(testConsumer)

	metrics, jsonBytes := testutils.GenerateTestMetrics(t)

	_, _, err := con.ConsumeCounted(context.Background(), jsonBytes)
	require.NoError(t, err)

	require.Equal(t, metrics.DataPointCount(), testConsumer.DataPointCount())

	// Test case of failed unmarshal
	_, _, err = con.ConsumeCounted(context.Background(), []byte("nope"))
	require.Error(t, err)
}

func Test_logsConsumer(t *testing.T) {
	testConsumer := &consumertest.LogsSink{}
	con := NewLogsConsumer(testConsumer)

	logs, jsonBytes := testutils.GenerateTestLogs(t)

	consumed, emitted, err := con.ConsumeCounted(context.Background(), jsonBytes)
	require.NoError(t, err)
	require.Equal(t, logs.LogRecordCount(), consumed)
	require.Equal(t, logs.LogRecordCount(), emitted)

	require.Equal(t, logs.LogRecordCount(), testConsumer.LogRecordCount())

	// Test case of failed unmarshal
	consumed, emitted, err = con.ConsumeCounted(context.Background(), []byte("nope"))
	require.Error(t, err)
	require.Zero(t, consumed)
	require.Zero(t, emitted)
}

func Test_tracesConsumer(t *testing.T) {
	testConsumer := &consumertest.TracesSink{}
	con := NewTracesConsumer(testConsumer)

	traces, jsonBytes := testutils.GenerateTestTraces(t)

	_, _, err := con.ConsumeCounted(context.Background(), jsonBytes)
	require.NoError(t, err)

	require.Equal(t, traces.SpanCount(), testConsumer.SpanCount())

	// Test case of failed unmarshal
	_, _, err = con.ConsumeCounted(context.Background(), []byte("nope"))
	require.Error(t, err)
}

func Test_consumeWrappers(t *testing.T) {
	// The Consume wrappers (the simple error-only form the rehydration receivers call) delegate
	// to ConsumeCounted and drop its counts. Exercise each on a success path.
	_, metricBytes := testutils.GenerateTestMetrics(t)
	_, logBytes := testutils.GenerateTestLogs(t)
	_, traceBytes := testutils.GenerateTestTraces(t)

	cases := map[string]struct {
		con   Consumer
		input []byte
	}{
		"metrics":      {NewMetricsConsumer(&consumertest.MetricsSink{}), metricBytes},
		"logs":         {NewLogsConsumer(&consumertest.LogsSink{}), logBytes},
		"traces":       {NewTracesConsumer(&consumertest.TracesSink{}), traceBytes},
		"ndjson":       {NewNDJSONLogsConsumer(&consumertest.LogsSink{}, zap.NewNop()), []byte(`{"a":1}`)},
		"records-json": {NewRecordsJSONLogsConsumer(&consumertest.LogsSink{}, zap.NewNop()), []byte(`{"records":[{"a":1}]}`)},
		"text":         {NewRawTextLogsConsumer(&consumertest.LogsSink{}), []byte("a line")},
		"line-text":    {NewLineTextLogsConsumer(&consumertest.LogsSink{}), []byte("one\ntwo")},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, tc.con.Consume(context.Background(), tc.input))
		})
	}
}

func Test_otlpConsumers_DownstreamError(t *testing.T) {
	// On a downstream error the payload was parsed but not forwarded: consumed is the record
	// count and emitted is zero, so the metric gap reflects the drop.
	t.Run("metrics", func(t *testing.T) {
		metrics, jsonBytes := testutils.GenerateTestMetrics(t)
		con := NewMetricsConsumer(consumertest.NewErr(errors.New("boom")))
		consumed, emitted, err := con.ConsumeCounted(context.Background(), jsonBytes)
		require.Error(t, err)
		require.Equal(t, metrics.DataPointCount(), consumed)
		require.Zero(t, emitted)
	})
	t.Run("logs", func(t *testing.T) {
		logs, jsonBytes := testutils.GenerateTestLogs(t)
		con := NewLogsConsumer(consumertest.NewErr(errors.New("boom")))
		consumed, emitted, err := con.ConsumeCounted(context.Background(), jsonBytes)
		require.Error(t, err)
		require.Equal(t, logs.LogRecordCount(), consumed)
		require.Zero(t, emitted)
	})
	t.Run("traces", func(t *testing.T) {
		traces, jsonBytes := testutils.GenerateTestTraces(t)
		con := NewTracesConsumer(consumertest.NewErr(errors.New("boom")))
		consumed, emitted, err := con.ConsumeCounted(context.Background(), jsonBytes)
		require.Error(t, err)
		require.Equal(t, traces.SpanCount(), consumed)
		require.Zero(t, emitted)
	})
}
