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

package awsneuronreceiver // import "github.com/observiq/bindplane-otel-contrib/receiver/awsneuronreceiver"

import (
	"errors"
	"reflect"
	"strings"

	"go.opentelemetry.io/collector/confmap"
	"go.opentelemetry.io/collector/scraper/scraperhelper"

	"github.com/observiq/bindplane-otel-contrib/receiver/awsneuronreceiver/internal/metadata"
)

// Config defines the configuration for the AWS Neuron receiver.
type Config struct {
	scraperhelper.ControllerConfig `mapstructure:",squash"`
	metadata.MetricsBuilderConfig  `mapstructure:",squash"`

	// Command is the path to (or name of) the neuron-monitor binary.
	Command string `mapstructure:"command"`

	// ConfigFile is an optional path to a neuron-monitor JSON configuration file.
	ConfigFile string `mapstructure:"config_file"`

	// MetricGroups is the category layer of the two-layer config. Each key is a
	// metric group (the third dot-segment of the metric name, e.g. "neuroncore",
	// "execution", "system"); the value bulk-sets every metric in that group.
	// Tri-state: a group left unset falls through to the metadata.yaml defaults;
	// true enables all in the group; false disables all. An explicit per-metric
	// `metrics.<name>.enabled` always wins over its group toggle.
	MetricGroups map[string]*bool `mapstructure:"metric_groups"`
}

// Validate checks that the configuration is well formed.
func (c *Config) Validate() error {
	if strings.TrimSpace(c.Command) == "" {
		return errors.New("command must not be empty")
	}
	if c.CollectionInterval <= 0 {
		return errors.New("collection_interval must be positive")
	}
	return nil
}

// Unmarshal applies the two-layer (category + per-metric) enablement resolution
// on top of the mdatagen-generated per-metric config.
func (c *Config) Unmarshal(conf *confmap.Conf) error {
	if conf == nil {
		return nil
	}
	// type plain strips Config's methods so this does not recurse.
	type plain Config
	if err := conf.Unmarshal((*plain)(c)); err != nil {
		return err
	}
	if len(c.MetricGroups) == 0 {
		return nil
	}
	explicit := explicitlySetMetrics(conf)
	applyMetricGroups(&c.MetricsBuilderConfig.Metrics, c.MetricGroups, explicit)
	return nil
}

// explicitlySetMetrics returns the set of metric names whose `enabled` flag the
// user wrote explicitly (priority 1, beats any category toggle).
func explicitlySetMetrics(conf *confmap.Conf) map[string]bool {
	explicit := map[string]bool{}
	if !conf.IsSet("metrics") {
		return explicit
	}
	sub, err := conf.Sub("metrics")
	if err != nil {
		return explicit
	}
	for name := range sub.ToStringMap() {
		ms, err := sub.Sub(name)
		if err == nil && ms.IsSet("enabled") {
			explicit[name] = true
		}
	}
	return explicit
}

// applyMetricGroups sets each metric's Enabled from its group toggle, unless the
// metric was explicitly set. Group is the third dot-segment of the metric name.
func applyMetricGroups(metrics any, groups map[string]*bool, explicit map[string]bool) {
	v := reflect.ValueOf(metrics).Elem()
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		name := t.Field(i).Tag.Get("mapstructure")
		if name == "" || explicit[name] {
			continue
		}
		toggle, ok := groups[groupFromName(name)]
		if !ok || toggle == nil {
			continue
		}
		if f := v.Field(i).FieldByName("Enabled"); f.IsValid() && f.CanSet() {
			f.SetBool(*toggle)
		}
	}
}

// groupFromName extracts the group from an aws.neuron.<group>.* metric name.
func groupFromName(metricName string) string {
	parts := strings.Split(metricName, ".")
	if len(parts) >= 3 && parts[0] == "aws" && parts[1] == "neuron" {
		return parts[2]
	}
	return ""
}
