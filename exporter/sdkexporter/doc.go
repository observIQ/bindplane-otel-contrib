// Copyright observIQ, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:generate mdatagen metadata.yaml

// Package sdkexporter republishes incoming pipeline metrics as observations on
// the collector's TelemetrySettings.MeterProvider, so anything wired to that
// provider (Prometheus on :8888/metrics, OTLP self-telemetry, etc.) sees them
// alongside otelcol_* metrics. Paired with the signaltometricsconnector, it
// lets users define new self-telemetry metrics at runtime via OTTL rather than
// at compile time.
//
// MVP scope (see README.md for the full type-by-type breakdown and the
// rationale for what is and isn't supported):
//   - Sums with delta temporality: supported (monotonic via Counter,
//     non-monotonic via UpDownCounter).
//   - Gauges: supported (Int64Gauge / Float64Gauge).
//   - Cumulative sums, histograms, exponential histograms, and summaries:
//     dropped with a sampled warning.
//   - Logs and traces signals: not implemented.
//   - Original data point timestamps are not preserved; SDK observation
//     timestamps are "now".
//   - pdata Resource attributes are dropped by default. Set
//     include_resource_attributes: true (optionally with
//     resource_attribute_keys) to fold them into per-instrument attributes.
package sdkexporter // import "github.com/observiq/bindplane-otel-contrib/exporter/sdkexporter"
