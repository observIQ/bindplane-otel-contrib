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

//go:build embed_library

package telemetrygeneratorreceiver

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/receiver/receivertest"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// runBlitzWithProviders boots a one-recipe blitz receiver whose
// MeterProvider and TracerProvider are the supplied SDK providers,
// waits until it has produced at least one record (so it has also
// emitted self-telemetry), then shuts it down so the coarse spans
// flush. It is the shared setup for the routing and isolation tests.
func runBlitzWithProviders(t *testing.T, mp *sdkmetric.MeterProvider, tp *sdktrace.TracerProvider) {
	t.Helper()
	sink := &consumertest.LogsSink{}
	factory := NewFactory()
	settings := receivertest.NewNopSettings(factory.Type())
	settings.TelemetrySettings.MeterProvider = mp
	if tp != nil {
		settings.TelemetrySettings.TracerProvider = tp
	}

	r, err := factory.CreateLogs(context.Background(), settings, blitzRecipeTestCfg("apache", nil, nil), sink)
	require.NoError(t, err)
	require.NoError(t, r.Start(context.Background(), componenttest.NewNopHost()))

	require.Eventually(t, func() bool {
		return sink.LogRecordCount() > 0
	}, 5*time.Second, 20*time.Millisecond, "blitz produced no records")

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, r.Shutdown(stopCtx))
}

// hasBlitzMetric reports whether any collected instrument is one of
// blitz's own (its self-metrics are all named with a "blitz." prefix).
func hasBlitzMetric(rm metricdata.ResourceMetrics) bool {
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if strings.HasPrefix(m.Name, "blitz.") {
				return true
			}
		}
	}
	return false
}

// TestBlitz_SelfTelemetry_RoutesIntoReceiverProviders proves PIPE-1066's
// core claim under a real caller: blitz's self-metrics and self-spans
// land in the providers the receiver hands it (not the process globals).
// The existing integration tests run on nop providers and so can't show
// routing; this one wires real SDK providers and asserts blitz's own
// telemetry arrives in them.
func TestBlitz_SelfTelemetry_RoutesIntoReceiverProviders(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))

	runBlitzWithProviders(t, mp, tp)

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	assert.True(t, hasBlitzMetric(rm), "blitz self-metrics did not reach the receiver's MeterProvider")

	var sawBlitzSpan bool
	for _, s := range sr.Ended() {
		if strings.HasPrefix(s.Name(), "blitz.") {
			sawBlitzSpan = true
			break
		}
	}
	assert.True(t, sawBlitzSpan, "blitz self-spans did not reach the receiver's TracerProvider")
}

// TestBlitz_SelfTelemetry_PerInstanceIsolation runs two independent
// receivers, each with its own MeterProvider, and asserts both collect
// blitz metrics. Because the wiring is per-receiver (built from each
// instance's own TelemetrySettings) rather than a shared global, two
// embedded instances keep their self-telemetry to their own provider —
// the payoff of dropping the package globals.
func TestBlitz_SelfTelemetry_PerInstanceIsolation(t *testing.T) {
	collect := func() metricdata.ResourceMetrics {
		reader := sdkmetric.NewManualReader()
		mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
		runBlitzWithProviders(t, mp, nil)
		var rm metricdata.ResourceMetrics
		require.NoError(t, reader.Collect(context.Background(), &rm))
		return rm
	}

	rm1, rm2 := collect(), collect()
	assert.True(t, hasBlitzMetric(rm1), "first instance's MeterProvider recorded no blitz metrics")
	assert.True(t, hasBlitzMetric(rm2), "second instance's MeterProvider recorded no blitz metrics")
}
