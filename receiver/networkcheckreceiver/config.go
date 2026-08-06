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

// Package networkcheckreceiver actively probes network targets and emits
// ICMP ping, HTTP timing, and traceroute metrics.
package networkcheckreceiver // import "github.com/observiq/bindplane-otel-contrib/receiver/networkcheckreceiver"

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/collector/config/confighttp"
	"go.opentelemetry.io/collector/scraper/scraperhelper"
	"go.uber.org/multierr"

	"github.com/observiq/bindplane-otel-contrib/receiver/networkcheckreceiver/internal/metadata"
)

// Probe method constants used in TargetConfig.Method.
const (
	MethodICMP = "icmp"
	MethodHTTP = "http"
	MethodDNS  = "dns"
)

// Config is the top-level configuration for the networkstat receiver.
type Config struct {
	scraperhelper.ControllerConfig `mapstructure:",squash"`
	metadata.MetricsBuilderConfig  `mapstructure:",squash"`

	// Targets is the list of endpoints to probe.
	Targets []TargetConfig `mapstructure:"targets"`

	// BatchSize controls how many targets are checked per scrape cycle.
	// 0 (default) means all targets every cycle. With N targets and batch_size 1,
	// each target is checked once every N * collection_interval.
	BatchSize int `mapstructure:"batch_size"`

	// Traceroute configures optional traceroute probes.
	Traceroute TracerouteConfig `mapstructure:"traceroute"`
}

// TargetConfig configures a single probe target.
type TargetConfig struct {
	confighttp.ClientConfig `mapstructure:",squash"`

	// Method is "icmp" or "http". Defaults to "icmp".
	// For ICMP targets, only ClientConfig.Endpoint (host/IP) is used.
	// For HTTP targets, ClientConfig.Endpoint must be a full URL.
	Method string `mapstructure:"method"`

	// PingCount is the number of ICMP packets to send per scrape. Default 3.
	PingCount int `mapstructure:"ping_count"`

	// HTTPMethod is the HTTP verb to use in HTTP mode. Default "HEAD".
	HTTPMethod string `mapstructure:"http_method"`

	// DNSServer overrides the DNS resolver for this target (e.g. "8.8.8.8:53").
	// If empty the system resolver is used and its address is detected from /etc/resolv.conf.
	DNSServer string `mapstructure:"dns_server"`

	// DNSQuery is the hostname to resolve when method is "dns". Required for dns targets.
	DNSQuery string `mapstructure:"dns_query"`

	// DNSRecordType is the record type to query in dns mode: "A" (default), "AAAA", "CNAME", "MX", "TXT".
	DNSRecordType string `mapstructure:"dns_record_type"`
}

// TracerouteConfig configures optional traceroute probes.
type TracerouteConfig struct {
	// Enabled enables traceroute. Default false.
	Enabled bool `mapstructure:"enabled"`

	// Method is "udp" (default, no root required) or "icmp" (requires root/CAP_NET_RAW).
	Method string `mapstructure:"method"`

	// MaxHops is the maximum TTL to probe. Default 30.
	MaxHops int `mapstructure:"max_hops"`

	// Interval runs a traceroute every N times a target is checked. 0 disables interval-based runs.
	Interval int `mapstructure:"interval"`

	// OnFailure triggers a traceroute when ICMP packet loss >= FailureThreshold.
	OnFailure bool `mapstructure:"on_failure"`

	// FailureThreshold is the packet-loss ratio (0.0–1.0) that triggers on-failure traceroute. Default 0.5.
	FailureThreshold float64 `mapstructure:"failure_threshold"`

	// Timeout is the per-hop probe timeout. Default 3s.
	Timeout time.Duration `mapstructure:"timeout"`
}

// Validate checks the configuration for required fields and valid values.
func (c *Config) Validate() error {
	var errs error

	if len(c.Targets) == 0 {
		errs = multierr.Append(errs, errors.New("at least one target is required"))
	}

	for i, t := range c.Targets {
		if t.Endpoint == "" {
			errs = multierr.Append(errs, fmt.Errorf("target[%d]: endpoint is required", i))
		}
		switch t.Method {
		case "", MethodICMP, MethodHTTP, MethodDNS:
		default:
			errs = multierr.Append(errs, fmt.Errorf("target[%d]: method %q is invalid; must be %q, %q, or %q", i, t.Method, MethodICMP, MethodHTTP, MethodDNS))
		}
		if t.Method == MethodDNS && t.DNSQuery == "" {
			errs = multierr.Append(errs, fmt.Errorf("target[%d]: dns_query is required when method is %q", i, MethodDNS))
		}
		switch strings.ToUpper(t.DNSRecordType) {
		case "", "A", "AAAA", "CNAME", "MX", "TXT":
		default:
			errs = multierr.Append(errs, fmt.Errorf("target[%d]: dns_record_type %q is invalid; must be A, AAAA, CNAME, MX, or TXT", i, t.DNSRecordType))
		}
		if t.PingCount < 0 {
			errs = multierr.Append(errs, fmt.Errorf("target[%d]: ping_count must be >= 0", i))
		}
	}

	if c.BatchSize < 0 {
		errs = multierr.Append(errs, errors.New("batch_size must be >= 0"))
	}

	if c.Traceroute.Enabled {
		switch strings.ToLower(c.Traceroute.Method) {
		case "", "udp", "icmp":
		default:
			errs = multierr.Append(errs, fmt.Errorf("traceroute.method %q is invalid; must be \"udp\" or \"icmp\"", c.Traceroute.Method))
		}
		if c.Traceroute.FailureThreshold < 0 || c.Traceroute.FailureThreshold > 1 {
			errs = multierr.Append(errs, errors.New("traceroute.failure_threshold must be between 0.0 and 1.0"))
		}
	}

	return errs
}
