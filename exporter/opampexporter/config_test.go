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

package opampexporter

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
)

func TestConfigValidate(t *testing.T) {
	t.Run("Default config is valid", func(t *testing.T) {
		err := createDefaultConfig().(*Config).Validate()
		require.NoError(t, err)
	})

	t.Run("OpAMP ID must be specified", func(t *testing.T) {
		var emptyID component.ID

		cfg := createDefaultConfig().(*Config)
		cfg.OpAMP = emptyID

		require.ErrorContains(t, cfg.Validate(), "`opamp` must be specified")
	})

	t.Run("CustomMessage.Capability must be specified", func(t *testing.T) {
		cfg := createDefaultConfig().(*Config)
		cfg.CustomMessage.Capability = ""

		require.ErrorContains(t, cfg.Validate(), "`custom_message.capability` must be specified")
	})

	t.Run("CustomMessage.Type must be specified", func(t *testing.T) {
		cfg := createDefaultConfig().(*Config)
		cfg.CustomMessage.Type = ""

		require.ErrorContains(t, cfg.Validate(), "`custom_message.type` must be specified")
	})

	t.Run("MaxQueuedMessages must be greater than 0", func(t *testing.T) {
		cfg := createDefaultConfig().(*Config)
		cfg.MaxQueuedMessages = 0

		require.ErrorContains(t, cfg.Validate(), "`max_queued_messages` must be greater than 0")

		cfg.MaxQueuedMessages = -1
		require.ErrorContains(t, cfg.Validate(), "`max_queued_messages` must be greater than 0")
	})
}
