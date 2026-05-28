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

package sdkexporter

import "fmt"

// Config is the top-level configuration for the sdk exporter.
type Config struct {
	// IncludeResourceAttributes folds pdata Resource attributes into each
	// data point's attribute set when recording on the SDK MeterProvider.
	// Defaults to false because SDK self-telemetry typically does not carry
	// per-record resource and folding can explode cardinality.
	IncludeResourceAttributes bool `mapstructure:"include_resource_attributes"`

	// ResourceAttributeKeys, when non-empty, restricts which resource
	// attributes are folded. Only meaningful when IncludeResourceAttributes
	// is true.
	ResourceAttributeKeys []string `mapstructure:"resource_attribute_keys"`
}

// Validate checks the config for inconsistent settings.
func (c *Config) Validate() error {
	if len(c.ResourceAttributeKeys) > 0 && !c.IncludeResourceAttributes {
		return fmt.Errorf("resource_attribute_keys is set but include_resource_attributes is false")
	}
	return nil
}
