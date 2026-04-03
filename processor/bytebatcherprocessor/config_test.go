// Copyright observIQ, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package bytebatcherprocessor

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name          string
		config        *Config
		expectedError error
	}{
		{
			name: "valid config",
			config: &Config{
				FlushInterval: 1 * time.Second,
				Bytes:         1024 * 1024,
			},
			expectedError: nil,
		},
		{
			name: "invalid Flush Interval",
			config: &Config{
				FlushInterval: 0,
				Bytes:         1024 * 1024,
			},
			expectedError: errors.New("flush_interval must be greater than 0"),
		},
		{
			name: "invalid Bytes",
			config: &Config{
				FlushInterval: 1 * time.Second,
				Bytes:         0,
			},
			expectedError: errors.New("bytes must be greater than 0"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.config.Validate()
			if test.expectedError != nil {
				require.Error(t, err)
				require.ErrorContains(t, err, test.expectedError.Error())
			} else {
				require.NoError(t, err)
			}
		})
	}
}
