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
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
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

	targets     []*targetState
	batchOffset int
	systemDNS   string
}

func newNetworkStatScraper(settings receiver.Settings, cfg *Config) *networkStatScraper {
	return &networkStatScraper{
		cfg:      cfg,
		settings: settings,
		logger:   settings.Logger,
	}
}

func (s *networkStatScraper) start(_ context.Context, _ component.Host) error {
	s.mb = metadata.NewMetricsBuilder(s.cfg.MetricsBuilderConfig, s.settings)
	s.rb = metadata.NewResourceBuilder(s.cfg.MetricsBuilderConfig.ResourceAttributes)
	s.systemDNS = detectSystemDNS()

	icmpAvailable, icmpPrivileged := checkICMPMode()
	if !icmpAvailable {
		s.logger.Warn("ICMP unavailable (raw and datagram modes both failed); ICMP targets will fall back to HTTP probing")
	} else if !icmpPrivileged {
		s.logger.Info("ICMP running in datagram (unprivileged) mode")
	}

	for i, tc := range s.cfg.Targets {
		method := tc.Method
		if method == "" {
			method = MethodICMP
		}

		dnsServer := tc.DNSServer
		if dnsServer == "" {
			dnsServer = s.systemDNS
		}

		var p pinger
		var err error

		if method == MethodICMP && !icmpAvailable {
			s.logger.Warn("falling back to HTTP for ICMP target",
				zap.String("target", tc.Endpoint),
				zap.Int("index", i),
			)
			method = MethodHTTP
		}

		switch method {
		case MethodICMP:
			p = newICMPPinger(tc, icmpPrivileged)
		case MethodDNS:
			p = newDNSPinger(tc)
		default:
			fallbackTC := tc
			if !strings.HasPrefix(fallbackTC.Endpoint, "http://") && !strings.HasPrefix(fallbackTC.Endpoint, "https://") {
				fallbackTC.Endpoint = "http://" + fallbackTC.Endpoint
			}
			p, err = newHTTPPinger(fallbackTC, dnsServer)
			if err != nil {
				return fmt.Errorf("target[%d] HTTP pinger: %w", i, err)
			}
		}

		s.targets = append(s.targets, &targetState{
			cfg:       tc,
			p:         p,
			tr:        newTracerouter(s.cfg.Traceroute, tc.Endpoint),
			dnsServer: dnsServer,
		})
	}

	return nil
}

func (s *networkStatScraper) shutdown(_ context.Context) error {
	return nil
}

func (s *networkStatScraper) scrape(ctx context.Context) (pmetric.Metrics, error) {
	errs := &scrapererror.ScrapeErrors{}
	now := pcommon.NewTimestampFromTime(time.Now())

	batch := s.activeBatch()
	for _, ts := range batch {
		result, err := ts.p.ping(ctx)
		if err != nil {
			errs.AddPartial(1, fmt.Errorf("ping %s: %w", ts.cfg.Endpoint, err))
			continue
		}

		s.rb.SetTargetEndpoint(ts.cfg.Endpoint)
		s.recordMetrics(now, ts, result)
		ts.checkCount++

		if ts.tr.shouldRun(ts.checkCount, result) {
			hops, trErr := ts.tr.trace(ctx)
			if trErr != nil {
				errs.AddPartial(1, fmt.Errorf("traceroute %s: %w", ts.cfg.Endpoint, trErr))
			}
			for _, hop := range hops {
				// A hop that never answered has no latency to report; its RTT
				// is just the probe timeout. Emitting it would look like a
				// real (and very slow) measurement.
				if hop.TimedOut {
					continue
				}
				s.mb.RecordNetworkTracerouteHopLatencyDataPoint(
					now,
					float64(hop.RTT.Milliseconds()),
					int64(hop.Index),
					hop.Address,
					ts.dnsServer,
				)
			}
		}

		s.mb.EmitForResource(metadata.WithResource(s.rb.Emit()))
	}

	if s.cfg.BatchSize > 0 && len(s.targets) > 0 {
		s.batchOffset = (s.batchOffset + len(batch)) % len(s.targets)
	}

	return s.mb.Emit(), errs.Combine()
}

// activeBatch returns the targets to probe this cycle. When BatchSize is 0
// (default) all targets are returned. Otherwise a rotating window of BatchSize
// targets is returned.
func (s *networkStatScraper) activeBatch() []*targetState {
	if len(s.targets) == 0 {
		return nil
	}
	if s.cfg.BatchSize <= 0 {
		return s.targets
	}
	size := s.cfg.BatchSize
	if size > len(s.targets) {
		size = len(s.targets)
	}
	out := make([]*targetState, size)
	for i := range size {
		out[i] = s.targets[(s.batchOffset+i)%len(s.targets)]
	}
	return out
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
		s.mb.RecordNetworkDNSLookupDurationDataPoint(now, float64(r.QueryDuration.Milliseconds()), r.QueryName)
	case MethodICMP:
		m := metadata.AttributePingMethodIcmp
		s.mb.RecordNetworkPingPacketLossDataPoint(now, r.PacketLoss, m, dns)
		if r.PacketLoss >= 1.0 {
			// Nothing came back, so min/avg/max are zero values rather than
			// round-trip times. Packet loss of 1.0 is the failure signal.
			return
		}
		s.mb.RecordNetworkPingLatencyMinDataPoint(now, float64(r.MinRTT.Milliseconds()), m, dns)
		s.mb.RecordNetworkPingLatencyAvgDataPoint(now, float64(r.AvgRTT.Milliseconds()), m, dns)
		s.mb.RecordNetworkPingLatencyMaxDataPoint(now, float64(r.MaxRTT.Milliseconds()), m, dns)
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
		s.mb.RecordNetworkHTTPDurationDataPoint(now, float64(r.TotalDuration.Milliseconds()), code, dns)
		s.mb.RecordNetworkHTTPDNSLookupDurationDataPoint(now, float64(r.DNSLookup.Milliseconds()), dns)
		s.mb.RecordNetworkHTTPClientConnectionDurationDataPoint(now, float64(r.TCPConnect.Milliseconds()), dns)
		s.mb.RecordNetworkHTTPTLSHandshakeDurationDataPoint(now, float64(r.TLSHandshake.Milliseconds()), dns)
		s.mb.RecordNetworkHTTPRequestDurationDataPoint(now, float64(r.RequestWrite.Milliseconds()), dns)
		s.mb.RecordNetworkHTTPResponseDurationDataPoint(now, float64(r.ResponseRead.Milliseconds()), dns)
	}
}

// detectSystemDNS reads the first nameserver entry from /etc/resolv.conf.
func detectSystemDNS() string {
	f, err := os.Open("/etc/resolv.conf")
	if err != nil {
		return ""
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "nameserver") {
			if parts := strings.Fields(line); len(parts) >= 2 {
				return parts[1]
			}
		}
	}
	return ""
}
