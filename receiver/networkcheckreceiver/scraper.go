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

package networkcheckreceiver // import "github.com/observiq/bindplane-otel-contrib/receiver/networkcheckreceiver"

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/receiver"
	"go.opentelemetry.io/collector/scraper/scrapererror"
	"go.uber.org/zap"

	"github.com/observiq/bindplane-otel-contrib/receiver/networkcheckreceiver/internal/metadata"
)

// targetState holds runtime state for a single probe target.
type targetState struct {
	cfg        TargetConfig
	p          pinger
	tr         *tracerouter
	checkCount int
	dnsServer  string
}

type networkStatScraper struct {
	cfg      *Config
	settings receiver.Settings
	logger   *zap.Logger
	mb       *metadata.MetricsBuilder
	rb       *metadata.ResourceBuilder

	// prober owns probe execution and is shared with the logs signal when the
	// same receiver ID appears in both a metrics and a logs pipeline.
	prober *sharedProber
	id     component.ID
}

func newNetworkStatScraper(settings receiver.Settings, cfg *Config) *networkStatScraper {
	return &networkStatScraper{
		cfg:      cfg,
		settings: settings,
		logger:   settings.Logger,
		id:       settings.ID,
		prober:   acquireProber(settings.ID, cfg, settings),
	}
}

func (s *networkStatScraper) start(ctx context.Context, host component.Host) error {
	s.mb = metadata.NewMetricsBuilder(s.cfg.MetricsBuilderConfig, s.settings)
	s.rb = metadata.NewResourceBuilder(s.cfg.MetricsBuilderConfig.ResourceAttributes)
	return s.prober.start(ctx, host)
}

func (s *networkStatScraper) shutdown(_ context.Context) error {
	releaseProber(s.id)
	return nil
}

func (s *networkStatScraper) scrape(ctx context.Context) (pmetric.Metrics, error) {
	errs := &scrapererror.ScrapeErrors{}
	cycle := s.prober.latestCycle(ctx, s.prober.cycleMaxAge())
	now := pcommon.NewTimestampFromTime(cycle.at)

	for _, res := range cycle.results {
		ts := res.target
		if res.pingErr != nil {
			errs.AddPartial(1, fmt.Errorf("ping %s: %w", redactEndpoint(ts.cfg.Endpoint), res.pingErr))
			continue
		}

		// Redacted unconditionally: the resource attribute is attached to every
		// data point, and a target may be configured as https://user:pass@host.
		// The error strings above redact for the same reason.
		s.rb.SetTargetEndpoint(redactEndpoint(ts.cfg.Endpoint))
		s.recordMetrics(now, ts, res.ping)

		if res.traceErr != nil {
			errs.AddPartial(1, fmt.Errorf("traceroute %s: %w", redactEndpoint(ts.cfg.Endpoint), res.traceErr))
		}
		if res.traced {
			for _, hop := range res.trace.Hops {
				// Status is reported for every probed hop, answered or not, so
				// that a hop going dark is visible as a 0 rather than as an
				// absent series indistinguishable from "traceroute never ran".
				status := int64(1)
				if hop.TimedOut {
					status = 0
				}
				s.mb.RecordNetworkTracerouteHopStatusDataPoint(
					now,
					status,
					int64(hop.Index),
					hop.Address,
					ts.dnsServer,
				)

				// A hop that never answered has no latency to report; its RTT
				// is just the probe timeout. Emitting it would look like a
				// real (and very slow) measurement.
				if hop.TimedOut {
					continue
				}
				s.mb.RecordNetworkTracerouteHopLatencyDataPoint(
					now,
					msFloat(hop.RTT),
					int64(hop.Index),
					hop.Address,
					ts.dnsServer,
				)
			}
		}

		s.mb.EmitForResource(metadata.WithResource(s.rb.Emit()))
	}

	return s.mb.Emit(), errs.Combine()
}

// recordMetrics writes data points for one completed probe cycle.
func (s *networkStatScraper) recordMetrics(now pcommon.Timestamp, ts *targetState, r PingResult) {
	// A probe that failed has no timings to report: the only duration available
	// is how long we waited before giving up, and the per-phase timers never
	// fired at all. Publishing those would put the configured timeout into the
	// latency series as though it were a measurement, and report 0ms for phases
	// that never ran. Only the status metric is emitted on failure, which
	// already expresses the outcome; the timing series shows a gap instead.
	dns := ts.dnsServer
	switch r.Method {
	case MethodDNS:
		status := int64(0)
		if r.QuerySuccess {
			status = 1
		}
		s.mb.RecordNetworkDNSStatusDataPoint(now, status, r.QueryName)
		if !r.QuerySuccess {
			return
		}
		s.mb.RecordNetworkDNSLookupDurationDataPoint(now, msFloat(r.QueryDuration), r.QueryName)
	case MethodICMP:
		m := metadata.AttributePingMethodIcmp
		s.mb.RecordNetworkPingPacketLossDataPoint(now, r.PacketLoss, m, dns)
		if r.PacketLoss >= 1.0 {
			// Nothing came back, so min/avg/max are zero values rather than
			// round-trip times. Packet loss of 1.0 is the failure signal.
			return
		}
		s.mb.RecordNetworkPingLatencyMinDataPoint(now, msFloat(r.MinRTT), m, dns)
		s.mb.RecordNetworkPingLatencyAvgDataPoint(now, msFloat(r.AvgRTT), m, dns)
		s.mb.RecordNetworkPingLatencyMaxDataPoint(now, msFloat(r.MaxRTT), m, dns)
	case MethodHTTP:
		code := int64(r.StatusCode)
		up := int64(0)
		if r.StatusCode > 0 {
			up = 1
		}
		s.mb.RecordNetworkHTTPStatusDataPoint(now, up, code, dns)
		if up == 0 {
			return
		}
		s.mb.RecordNetworkHTTPDurationDataPoint(now, msFloat(r.TotalDuration), code, dns)
		s.mb.RecordNetworkHTTPDNSLookupDurationDataPoint(now, msFloat(r.DNSLookup), dns)
		s.mb.RecordNetworkHTTPClientConnectionDurationDataPoint(now, msFloat(r.TCPConnect), dns)
		s.mb.RecordNetworkHTTPTLSHandshakeDurationDataPoint(now, msFloat(r.TLSHandshake), dns)
		s.mb.RecordNetworkHTTPRequestDurationDataPoint(now, msFloat(r.RequestWrite), dns)
		s.mb.RecordNetworkHTTPResponseDurationDataPoint(now, msFloat(r.ResponseRead), dns)
	}
}

// msFloat converts a duration to fractional milliseconds, preserving sub-ms
// precision that time.Duration.Milliseconds() truncates to zero.
func msFloat(d time.Duration) float64 {
	return float64(d.Nanoseconds()) / 1e6
}

// detectSystemDNS is implemented per platform: see systemdns_other.go and
// systemdns_windows.go.
