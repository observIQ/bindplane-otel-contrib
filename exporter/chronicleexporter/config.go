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

package chronicleexporter

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/observiq/bindplane-otel-contrib/pkg/expr"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/config/configoptional"
	"go.opentelemetry.io/collector/config/configretry"
	"go.opentelemetry.io/collector/exporter/exporterhelper"
	"go.uber.org/zap"
	"google.golang.org/grpc/encoding/gzip"
)

const (
	// noCompression is the no compression type.
	noCompression     = "none"
	protocolHTTPS     = "https"
	protocolGRPC      = "gRPC"
	apiVersionV1Alpha = "v1alpha"
	apiVersionV1Beta  = "v1beta"

	// httpVersion11 and httpVersion2 are the valid values for the http_version setting.
	httpVersion11 = "1.1"
	httpVersion2  = "2"
)

// Config defines configuration for the Chronicle exporter.
type Config struct {
	TimeoutConfig    exporterhelper.TimeoutConfig                             `mapstructure:",squash"` // squash ensures fields are correctly decoded in embedded struct.
	QueueBatchConfig configoptional.Optional[exporterhelper.QueueBatchConfig] `mapstructure:"sending_queue"`
	BackOffConfig    configretry.BackOffConfig                                `mapstructure:"retry_on_failure"`

	// Endpoint is the URL where Chronicle data will be sent.
	Endpoint string `mapstructure:"endpoint"`

	// CredsFilePath is the file path to the Google credentials JSON file.
	CredsFilePath string `mapstructure:"creds_file_path"`

	// Creds are the Google credentials JSON file.
	Creds string `mapstructure:"creds"`

	// LogType is the type of log that will be sent to Chronicle if not overridden by `attributes["log_type"]` or `attributes["chronicle_log_type"]`.
	LogType string `mapstructure:"log_type"`

	// ValidateLogTypes is a flag that determines whether or not to validate the log types using an API call to SecOps.
	ValidateLogTypes bool `mapstructure:"validate_log_types"`

	// OverrideLogType is a flag that determines whether or not to override the `log_type` in the config with `attributes["log_type"]`.
	OverrideLogType bool `mapstructure:"override_log_type"`

	// RawLogField is the field name that will be used to send raw logs to Chronicle.
	RawLogField string `mapstructure:"raw_log_field"`

	// CustomerID is the customer ID that will be used to send logs to Chronicle.
	CustomerID string `mapstructure:"customer_id"`

	// Namespace is the namespace that will be used to send logs to Chronicle.
	Namespace string `mapstructure:"namespace"`

	// Compression is the compression type that will be used to send logs to Chronicle.
	Compression string `mapstructure:"compression"`

	// IngestionLabels are the labels that will be attached to logs when sent to Chronicle.
	IngestionLabels map[string]string `mapstructure:"ingestion_labels"`

	// CollectAgentMetrics is a flag that determines whether or not to collect agent metrics.
	CollectAgentMetrics bool `mapstructure:"collect_agent_metrics"`

	// MetricsInterval is the interval at which to collect and send agent metrics.
	MetricsInterval time.Duration `mapstructure:"metrics_interval"`

	// Protocol is the protocol that will be used to send logs to Chronicle.
	// Either https or grpc.
	Protocol string `mapstructure:"protocol"`

	// Location is the location that will be used when the protocol is https.
	Location string `mapstructure:"location"`

	// Project is the project that will be used when the protocol is https.
	Project string `mapstructure:"project"`

	// Forwarder is the forwarder that will be used when the protocol is https.
	// Deprecated as of v1.87.1: The forwarder (Collector ID) is now determined by the license type
	Forwarder string `mapstructure:"forwarder"`

	// BatchRequestSizeLimitGRPC is the maximum batch request size, in bytes, that can be sent to Chronicle via the GRPC protocol
	// This field is defaulted to 4000000 as that is the default Chronicle backend limit
	// Setting this option to a value above the Chronicle backend limit may result in rejected log batch requests
	BatchRequestSizeLimitGRPC int `mapstructure:"batch_request_size_limit_grpc"`

	// BatchRequestSizeLimitHTTP is the maximum batch request size, in bytes, that can be sent to Chronicle via the HTTP protocol
	// This field is defaulted to 4000000 as that is the default Chronicle backend limit
	// Setting this option to a value above the Chronicle backend limit may result in rejected log batch requests
	BatchRequestSizeLimitHTTP int `mapstructure:"batch_request_size_limit_http"`

	// LicenseType is the license type of the bindplane instance managing this agent.
	// This field is used to determine collector ID for Chronicle.
	LicenseType string `mapstructure:"license_type"`

	// LogErroredPayloads is a flag that determines whether or not to log errored payloads.
	LogErroredPayloads bool `mapstructure:"log_errored_payloads"`

	// OverrideEndpoint determines whether or not to ignore the Location field when constructing the endpoint.
	// This is useful for when the endpoint is a custom endpoint and the Location field is not needed.
	// We still need the Location field for the API call to Chronicle, but we don't want to use it in the endpoint.
	// Only applies to HTTPS protocol.
	OverrideEndpoint bool `mapstructure:"override_endpoint"`

	// APIVersion is the version of the API to use. Default is "v1alpha". Only applies to HTTPS protocol.
	APIVersion string `mapstructure:"api_version"`

	// HTTPResponseHeaderTimeout is the timeout for the HTTP response headers when using the HTTPS protocol.
	HTTPResponseHeaderTimeout time.Duration `mapstructure:"http_response_header_timeout"`

	// HTTPVersion selects the HTTP protocol version used to send logs over the https protocol.
	// Valid values are "1.1" and "2"; the default is "2". HTTP/2 multiplexes every concurrent
	// request over a single TCP connection to Chronicle, which becomes a throughput bottleneck under
	// high volume: large request bodies serialize behind the connection's write lock and HTTP/2
	// flow-control window. Setting "1.1" instead opens a pool of connections (bounded by
	// max_idle_conns / max_idle_conns_per_host), giving real upload parallelism across consumers.
	// Only applies to the https protocol.
	HTTPVersion string `mapstructure:"http_version"`

	// MaxIdleConns controls the total number of idle (keep-alive) connections kept across all hosts.
	// Most relevant when http_version is "1.1". 0 means no limit. Only applies to the https protocol.
	MaxIdleConns int `mapstructure:"max_idle_conns"`

	// MaxIdleConnsPerHost controls how many idle (keep-alive) connections are retained per host.
	// When http_version is "1.1", raise this toward sending_queue.num_consumers so connections are
	// reused instead of being repeatedly re-dialed. Only applies to the https protocol.
	MaxIdleConnsPerHost int `mapstructure:"max_idle_conns_per_host"`

	// MaxConnsPerHost limits the total number of connections per host, counting connections in the
	// dialing, active, and idle states. When the limit is reached, further requests block until a
	// connection is released rather than opening new ones. 0 (the default) means no limit, which
	// lets HTTP/1.1 open an unbounded number of connections under high concurrency. Set this to cap
	// connections (and keep max_idle_conns_per_host at or above it to avoid connection churn).
	// Only applies to the https protocol.
	MaxConnsPerHost int `mapstructure:"max_conns_per_host"`
}

// useHTTP2 reports whether the exporter should negotiate HTTP/2 for the https protocol.
// HTTP/2 is the default; only an explicit "1.1" selects HTTP/1.1 (an unset value stays on HTTP/2).
func (cfg *Config) useHTTP2() bool {
	return cfg.HTTPVersion != httpVersion11
}

// Validate checks if the configuration is valid.
func (cfg *Config) Validate() error {
	if cfg.CredsFilePath != "" && cfg.Creds != "" {
		return errors.New("can only specify creds_file_path or creds")
	}

	if cfg.RawLogField != "" {
		_, err := expr.NewOTTLLogRecordExpression(cfg.RawLogField, component.TelemetrySettings{
			Logger: zap.NewNop(),
		})
		if err != nil {
			return fmt.Errorf("raw_log_field is invalid: %s", err)
		}
	}

	if cfg.Compression != gzip.Name && cfg.Compression != noCompression {
		return fmt.Errorf("invalid compression type: %s", cfg.Compression)
	}

	if strings.HasPrefix(cfg.Endpoint, "http://") || strings.HasPrefix(cfg.Endpoint, "https://") {
		return fmt.Errorf("endpoint should not contain a protocol: %s", cfg.Endpoint)
	}

	if cfg.CollectAgentMetrics && cfg.MetricsInterval <= 0 {
		return errors.New("metrics_interval must be a positive duration")
	}

	if cfg.Protocol == protocolHTTPS {
		if cfg.Location == "" {
			return errors.New("location is required when protocol is https")
		}
		if cfg.Endpoint == "" {
			return errors.New("endpoint is required when protocol is https")
		}
		if cfg.Project == "" {
			return errors.New("project is required when protocol is https")
		}
		if cfg.BatchRequestSizeLimitHTTP <= 0 {
			return errors.New("positive batch request size limit is required when protocol is https")
		}
		if cfg.APIVersion != "" {
			if cfg.APIVersion != apiVersionV1Alpha && cfg.APIVersion != apiVersionV1Beta {
				return fmt.Errorf("invalid API version: %s", cfg.APIVersion)
			}
		}
		if cfg.HTTPResponseHeaderTimeout <= 0 {
			return errors.New("positive HTTP response header timeout is required when protocol is https")
		}
		switch cfg.HTTPVersion {
		case "", httpVersion11, httpVersion2:
		default:
			return fmt.Errorf("invalid http_version: %q (valid values are %q and %q)", cfg.HTTPVersion, httpVersion11, httpVersion2)
		}
		if cfg.MaxIdleConns < 0 {
			return errors.New("max_idle_conns must not be negative")
		}
		if cfg.MaxIdleConnsPerHost < 0 {
			return errors.New("max_idle_conns_per_host must not be negative")
		}
		if cfg.MaxConnsPerHost < 0 {
			return errors.New("max_conns_per_host must not be negative")
		}

		return nil
	}

	if cfg.Protocol == protocolGRPC {
		if cfg.BatchRequestSizeLimitGRPC <= 0 {
			return errors.New("positive batch request size limit is required when protocol is grpc")
		}

		return nil
	}

	return fmt.Errorf("invalid protocol: %s", cfg.Protocol)
}
