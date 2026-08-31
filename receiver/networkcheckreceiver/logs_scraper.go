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
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/receiver"
	"go.opentelemetry.io/collector/scraper/scrapererror"

	"github.com/observiq/bindplane-otel-contrib/receiver/networkcheckreceiver/internal/metadata"
)

// networkStatLogsScraper renders probe cycles as log records. It shares the
// prober with the metrics scraper, so wiring the same receiver into both a
// metrics and a logs pipeline probes each target once, not twice.
type networkStatLogsScraper struct {
	cfg      *Config
	settings receiver.Settings
	id       component.ID

	lb *metadata.LogsBuilder
	rb *metadata.ResourceBuilder

	prober *sharedProber
}

func newNetworkStatLogsScraper(settings receiver.Settings, cfg *Config) *networkStatLogsScraper {
	return &networkStatLogsScraper{
		cfg:      cfg,
		settings: settings,
		id:       settings.ID,
		prober:   acquireProber(settings.ID, cfg, settings),
	}
}

func (s *networkStatLogsScraper) start(ctx context.Context, host component.Host) error {
	s.lb = metadata.NewLogsBuilder(s.settings)
	s.rb = metadata.NewResourceBuilder(s.cfg.MetricsBuilderConfig.ResourceAttributes)
	return s.prober.start(ctx, host)
}

func (s *networkStatLogsScraper) shutdown(_ context.Context) error {
	releaseProber(s.id)
	return nil
}

// scrape renders the latest probe cycle as logs. Only HTTP probes and
// traceroutes produce records: a DNS or ICMP probe is a scalar sampled on an
// interval, which is a metric, not an event.
func (s *networkStatLogsScraper) scrape(ctx context.Context) (plog.Logs, error) {
	errs := &scrapererror.ScrapeErrors{}
	cycle := s.prober.latestCycle(ctx, s.prober.cycleMaxAge())
	observed := time.Now()

	for _, res := range cycle.results {
		ts := res.target
		if res.pingErr != nil {
			errs.AddPartial(1, fmt.Errorf("ping %s: %w", redactEndpoint(ts.cfg.Endpoint), redactErr(res.pingErr)))
			continue
		}

		// The record builders redact the endpoint they embed; the resource
		// attribute has to match, or the credential simply moves one level up.
		endpoint := ts.cfg.Endpoint
		if s.cfg.Logs.RedactURLUserinfo {
			endpoint = redactEndpoint(endpoint)
		}
		s.rb.SetTargetEndpoint(endpoint)

		if res.ping.Method == MethodHTTP {
			rec := plog.NewLogRecord()
			buildHTTPLogRecord(rec, ts, res.ping, res.startedAt, observed, s.cfg.Logs)
			s.lb.AppendLogRecord(rec)
		}

		if res.traceErr != nil {
			errs.AddPartial(1, fmt.Errorf("traceroute %s: %w", redactEndpoint(ts.cfg.Endpoint), redactErr(res.traceErr)))
		}
		if res.traced && len(res.trace.Hops) > 0 {
			rec := plog.NewLogRecord()
			buildTracerouteLogRecord(rec, ts, res.trace, res.startedAt, observed, s.cfg.Logs)
			s.lb.AppendLogRecord(rec)
		}

		// EmitForResource is a no-op when no records were appended for this
		// target, and always resets the resource builder for the next one.
		s.lb.EmitForResource(metadata.WithLogsResource(s.rb.Emit()))
	}

	return s.lb.Emit(), errs.Combine()
}
