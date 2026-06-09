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

//go:generate mdatagen metadata.yaml

// Package awsneuronreceiver collects AWS Neuron (Inferentia/Trainium) hardware
// and runtime metrics by running the vendor-provided neuron-monitor binary as a
// subprocess and translating its JSON output into OpenTelemetry metrics.
//
// Design note (deliberate deviation from typical receiver behavior): the
// neuron-monitor binary is NOT bundled. If it is absent or fails to start, the
// receiver logs a single warning and continues producing no metrics rather than
// failing the collector. The per-runtime performance metrics (utilization,
// flops, execution latency/errors, memory) only populate while a Neuron runtime
// process is actively executing a model.
package awsneuronreceiver // import "github.com/observiq/bindplane-otel-contrib/receiver/awsneuronreceiver"
