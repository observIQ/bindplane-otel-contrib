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

package networkcheckreceiver

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/receiver/receivertest"

	"github.com/observiq/bindplane-otel-contrib/receiver/networkcheckreceiver/internal/metadata"
)

// recordOne runs a single PingResult through recordMetrics and returns the
// names of the metrics that were emitted.
func recordOne(t *testing.T, r PingResult) map[string]float64 {
	t.Helper()

	cfg := createDefaultConfig().(*Config)
	settings := receivertest.NewNopSettings(metadata.Type)
	s := newNetworkStatScraper(settings, cfg)
	s.mb = metadata.NewMetricsBuilder(cfg.MetricsBuilderConfig, settings)
	s.rb = metadata.NewResourceBuilder(cfg.MetricsBuilderConfig.ResourceAttributes)

	ts := &targetState{cfg: TargetConfig{}, dnsServer: "8.8.8.8"}
	s.recordMetrics(pcommon.NewTimestampFromTime(time.Now()), ts, r)

	got := map[string]float64{}
	md := s.mb.Emit()
	for i := 0; i < md.ResourceMetrics().Len(); i++ {
		sms := md.ResourceMetrics().At(i).ScopeMetrics()
		for j := 0; j < sms.Len(); j++ {
			ms := sms.At(j).Metrics()
			for k := 0; k < ms.Len(); k++ {
				m := ms.At(k)
				dps := m.Gauge().DataPoints()
				require.Equal(t, 1, dps.Len(), "expected a single data point for %s", m.Name())
				dp := dps.At(0)
				if dp.ValueType() == pmetric.NumberDataPointValueTypeInt {
					got[m.Name()] = float64(dp.IntValue())
				} else {
					got[m.Name()] = dp.DoubleValue()
				}
			}
		}
	}
	return got
}

// A failed probe must publish its status and nothing else. Emitting the timing
// metrics would put the configured timeout into the latency series and report
// 0ms for phases that never ran.
func TestRecordMetricsFailedHTTPProbeEmitsOnlyStatus(t *testing.T) {
	got := recordOne(t, PingResult{
		Method:        MethodHTTP,
		StatusCode:    0,                // connection failed
		TotalDuration: 10 * time.Second, // the client timeout, not a measurement
	})

	require.Equal(t, map[string]float64{"network.http.status": 0}, got,
		"a failed HTTP probe must emit only network.http.status")
}

func TestRecordMetricsSuccessfulHTTPProbeEmitsTimings(t *testing.T) {
	got := recordOne(t, PingResult{
		Method:        MethodHTTP,
		StatusCode:    200,
		TotalDuration: 105 * time.Millisecond,
		DNSLookup:     3 * time.Millisecond,
		TCPConnect:    15 * time.Millisecond,
		TLSHandshake:  45 * time.Millisecond,
		RequestWrite:  41 * time.Millisecond,
		ResponseRead:  31 * time.Millisecond,
	})

	require.Equal(t, float64(1), got["network.http.status"])
	require.Equal(t, float64(105), got["network.http.duration"])
	require.Equal(t, float64(3), got["network.http.dns_lookup_duration"])
	require.Len(t, got, 7, "a successful HTTP probe emits status plus six timings")
}

func TestRecordMetricsFailedDNSProbeEmitsOnlyStatus(t *testing.T) {
	got := recordOne(t, PingResult{
		Method:        MethodDNS,
		QuerySuccess:  false,
		QueryName:     "example.com",
		QueryDuration: 5 * time.Second, // the timeout we waited out
	})

	require.Equal(t, map[string]float64{"network.dns.status": 0}, got,
		"a failed DNS probe must emit only network.dns.status")
}

func TestRecordMetricsSuccessfulDNSProbeEmitsDuration(t *testing.T) {
	got := recordOne(t, PingResult{
		Method:        MethodDNS,
		QuerySuccess:  true,
		QueryName:     "example.com",
		QueryDuration: 12 * time.Millisecond,
	})

	require.Equal(t, float64(1), got["network.dns.status"])
	require.Equal(t, float64(12), got["network.dns.lookup_duration"])
	require.Len(t, got, 2)
}

// Total packet loss means nothing came back, so the min/avg/max fields are zero
// values rather than round-trip times.
func TestRecordMetricsTotalPacketLossEmitsOnlyLoss(t *testing.T) {
	got := recordOne(t, PingResult{
		Method:     MethodICMP,
		PacketLoss: 1.0,
	})

	require.Equal(t, map[string]float64{"network.ping.packet_loss": 1}, got,
		"a fully lost ping must emit only network.ping.packet_loss")
}

func TestRecordMetricsPartialPacketLossEmitsLatencies(t *testing.T) {
	got := recordOne(t, PingResult{
		Method:     MethodICMP,
		PacketLoss: 0.5,
		MinRTT:     8 * time.Millisecond,
		AvgRTT:     10 * time.Millisecond,
		MaxRTT:     14 * time.Millisecond,
	})

	require.Equal(t, float64(0.5), got["network.ping.packet_loss"])
	require.Equal(t, float64(8), got["network.ping.latency_min"])
	require.Equal(t, float64(14), got["network.ping.latency_max"])
	require.Len(t, got, 4)
}
