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

package lookupprocessor

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidate(t *testing.T) {
	testCases := []struct {
		name string
		cfg  Config
		err  error
	}{
		{
			name: "missing context",
			cfg:  Config{},
			err:  errMissingContext,
		},
		{
			name: "missing field",
			cfg:  Config{Context: "body"},
			err:  errMissingField,
		},
		{
			name: "invalid context",
			cfg:  Config{Context: "invalid", Field: "field", CSV: "csv"},
			err:  errInvalidContext,
		},
		{
			name: "missing source",
			cfg:  Config{Context: "body", Field: "ip"},
			err:  errMissingSource,
		},
		{
			name: "multiple sources",
			cfg: Config{
				Context: "body",
				Field:   "ip",
				CSV:     "csv",
				Redis:   &RedisConfig{Address: "localhost:6379"},
			},
			err: errMultipleSources,
		},
		{
			name: "invalid source_type",
			cfg: Config{
				Context:    "body",
				Field:      "ip",
				CSV:        "csv",
				SourceType: "bogus",
			},
			err: errInvalidSourceType,
		},
		{
			name: "source_type mismatch",
			cfg: Config{
				Context:    "body",
				Field:      "ip",
				CSV:        "csv",
				SourceType: "redis",
			},
			err: errSourceTypeMismatch,
		},
		{
			name: "redis missing address",
			cfg: Config{
				Context: "body",
				Field:   "ip",
				Redis:   &RedisConfig{},
			},
			err: errMissingRedisAddr,
		},
		{
			name: "api missing url",
			cfg: Config{
				Context: "body",
				Field:   "ip",
				API:     &APIConfig{},
			},
			err: errMissingAPIURL,
		},
		{
			name: "valid body context with csv",
			cfg:  Config{CSV: "csv", Context: "body", Field: "field"},
		},
		{
			name: "valid attributes context with csv",
			cfg:  Config{CSV: "csv", Context: "attributes", Field: "field"},
		},
		{
			name: "valid resource context with csv",
			cfg:  Config{CSV: "csv", Context: "resource.attributes", Field: "field"},
		},
		{
			name: "valid redis",
			cfg: Config{
				Context: "attributes",
				Field:   "user_id",
				Redis:   &RedisConfig{Address: "redis:6379", KeyPrefix: "u"},
			},
		},
		{
			name: "valid api",
			cfg: Config{
				Context: "attributes",
				Field:   "host",
				API:     &APIConfig{URL: "https://example.com/${fieldValue}"},
			},
		},
		{
			name: "valid api with explicit source_type",
			cfg: Config{
				Context:    "attributes",
				Field:      "host",
				SourceType: "api",
				API:        &APIConfig{URL: "https://example.com/${fieldValue}"},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.err == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tc.err)
		})
	}
}
