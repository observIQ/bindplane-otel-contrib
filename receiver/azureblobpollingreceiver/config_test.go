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

package azureblobpollingreceiver //import "github.com/observiq/bindplane-otel-contrib/receiver/azureblobpollingreceiver"

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestConfigValidate(t *testing.T) {
	testCases := []struct {
		desc      string
		cfg       *Config
		expectErr error
	}{
		{
			desc: "Missing connection string",
			cfg: &Config{
				ConnectionString: "",
				Container:        "container",
				RootFolder:       "root",
				PollInterval:     10 * time.Minute,
				DeleteOnRead:     false,
				BatchSize:        30,
				PageSize:         1000,
			},
			expectErr: errors.New("connection_string is required"),
		},
		{
			desc: "Missing container",
			cfg: &Config{
				ConnectionString: "connection_string",
				Container:        "",
				RootFolder:       "root",
				PollInterval:     10 * time.Minute,
				DeleteOnRead:     false,
				BatchSize:        30,
				PageSize:         1000,
			},
			expectErr: errors.New("container is required"),
		},
		{
			desc: "Missing poll_interval",
			cfg: &Config{
				ConnectionString: "connection_string",
				Container:        "container",
				RootFolder:       "root",
				PollInterval:     0,
				DeleteOnRead:     false,
				BatchSize:        30,
				PageSize:         1000,
			},
			expectErr: errors.New("poll_interval must be greater than 0"),
		},
		{
			desc: "poll_interval less than 1 minute",
			cfg: &Config{
				ConnectionString: "connection_string",
				Container:        "container",
				RootFolder:       "root",
				PollInterval:     30 * time.Second,
				DeleteOnRead:     false,
				BatchSize:        30,
				PageSize:         1000,
			},
			expectErr: errors.New("poll_interval must be at least 1 minute"),
		},
		{
			desc: "Negative initial_lookback",
			cfg: &Config{
				ConnectionString: "connection_string",
				Container:        "container",
				RootFolder:       "root",
				PollInterval:     10 * time.Minute,
				InitialLookback:  -5 * time.Minute,
				DeleteOnRead:     false,
				BatchSize:        30,
				PageSize:         1000,
			},
			expectErr: errors.New("initial_lookback must be greater than or equal to 0"),
		},
		{
			desc: "Bad batch_size",
			cfg: &Config{
				ConnectionString: "connection_string",
				Container:        "container",
				RootFolder:       "root",
				PollInterval:     10 * time.Minute,
				DeleteOnRead:     false,
				BatchSize:        0,
				PageSize:         1000,
			},
			expectErr: errors.New("batch_size must be greater than 0"),
		},
		{
			desc: "Bad page_size",
			cfg: &Config{
				ConnectionString: "connection_string",
				Container:        "container",
				RootFolder:       "root",
				PollInterval:     10 * time.Minute,
				DeleteOnRead:     false,
				BatchSize:        30,
				PageSize:         0,
			},
			expectErr: errors.New("page_size must be greater than 0"),
		},
		{
			desc: "Valid config",
			cfg: &Config{
				ConnectionString: "connection_string",
				Container:        "container",
				RootFolder:       "root",
				PollInterval:     10 * time.Minute,
				DeleteOnRead:     false,
				BatchSize:        30,
				PageSize:         1000,
			},
			expectErr: nil,
		},
		{
			desc: "Valid config with initial_lookback",
			cfg: &Config{
				ConnectionString: "connection_string",
				Container:        "container",
				RootFolder:       "root",
				PollInterval:     5 * time.Minute,
				InitialLookback:  1 * time.Hour,
				DeleteOnRead:     false,
				BatchSize:        30,
				PageSize:         1000,
			},
			expectErr: nil,
		},
		{
			desc: "Valid config with glob root_folder",
			cfg: &Config{
				ConnectionString: "connection_string",
				Container:        "container",
				RootFolder:       "linux/*",
				PollInterval:     5 * time.Minute,
				BatchSize:        30,
				PageSize:         1000,
			},
			expectErr: nil,
		},
		{
			desc: "Invalid glob pattern in root_folder",
			cfg: &Config{
				ConnectionString: "connection_string",
				Container:        "container",
				RootFolder:       "linux/[invalid",
				PollInterval:     5 * time.Minute,
				BatchSize:        30,
				PageSize:         1000,
			},
			expectErr: errors.New("root_folder contains an invalid glob pattern"),
		},
		{
			desc: "Valid blob_format otlp",
			cfg: &Config{
				ConnectionString: "connection_string",
				Container:        "container",
				RootFolder:       "root",
				PollInterval:     5 * time.Minute,
				BatchSize:        30,
				PageSize:         1000,
				BlobFormat:       BlobFormatOTLP,
			},
			expectErr: nil,
		},
		{
			desc: "Valid blob_format json",
			cfg: &Config{
				ConnectionString: "connection_string",
				Container:        "container",
				RootFolder:       "root",
				PollInterval:     5 * time.Minute,
				BatchSize:        30,
				PageSize:         1000,
				BlobFormat:       BlobFormatJSON,
			},
			expectErr: nil,
		},
		{
			desc: "Valid blob_format text",
			cfg: &Config{
				ConnectionString: "connection_string",
				Container:        "container",
				RootFolder:       "root",
				PollInterval:     5 * time.Minute,
				BatchSize:        30,
				PageSize:         1000,
				BlobFormat:       BlobFormatText,
			},
			expectErr: nil,
		},
		{
			desc: "Valid blob_format records-json",
			cfg: &Config{
				ConnectionString: "connection_string",
				Container:        "container",
				RootFolder:       "root",
				PollInterval:     5 * time.Minute,
				BatchSize:        30,
				PageSize:         1000,
				BlobFormat:       BlobFormatRecordsJSON,
			},
			expectErr: nil,
		},
		{
			desc: "Invalid blob_format",
			cfg: &Config{
				ConnectionString: "connection_string",
				Container:        "container",
				RootFolder:       "root",
				PollInterval:     5 * time.Minute,
				BatchSize:        30,
				PageSize:         1000,
				BlobFormat:       "invalid",
			},
			expectErr: errors.New("blob_format must be one of: otlp, json, text, records-json"),
		},
		{
			desc: "Negative incremental_revisit_window",
			cfg: &Config{
				ConnectionString:         "connection_string",
				Container:                "container",
				PollInterval:             5 * time.Minute,
				BatchSize:                30,
				PageSize:                 1000,
				IncrementalRevisitWindow: -time.Hour,
			},
			expectErr: errors.New("incremental_revisit_window must be greater than or equal to 0"),
		},
		{
			desc: "enable_per_line_text without enable_incremental_read is rejected",
			cfg: &Config{
				ConnectionString:  "connection_string",
				Container:         "container",
				PollInterval:      5 * time.Minute,
				BatchSize:         30,
				PageSize:          1000,
				BlobFormat:        BlobFormatText,
				EnablePerLineText: true,
			},
			expectErr: errors.New("enable_per_line_text requires enable_incremental_read"),
		},
		{
			desc: "enable_per_line_text with enable_incremental_read is valid",
			cfg: &Config{
				ConnectionString:      "connection_string",
				Container:             "container",
				PollInterval:          5 * time.Minute,
				BatchSize:             30,
				PageSize:              1000,
				BlobFormat:            BlobFormatText,
				EnableIncrementalRead: true,
				EnablePerLineText:     true,
			},
			expectErr: nil,
		},
		{
			desc: "text with enable_incremental_read but no enable_per_line_text is rejected",
			cfg: &Config{
				ConnectionString:      "connection_string",
				Container:             "container",
				PollInterval:          5 * time.Minute,
				BatchSize:             30,
				PageSize:              1000,
				BlobFormat:            BlobFormatText,
				EnableIncrementalRead: true,
			},
			expectErr: errors.New("enable_incremental_read on blob_format text requires enable_per_line_text"),
		},
		{
			desc: "enable_per_line_text on a non-text format is rejected",
			cfg: &Config{
				ConnectionString:      "connection_string",
				Container:             "container",
				PollInterval:          5 * time.Minute,
				BatchSize:             30,
				PageSize:              1000,
				BlobFormat:            BlobFormatRecordsJSON,
				EnableIncrementalRead: true,
				EnablePerLineText:     true,
			},
			expectErr: errors.New("enable_per_line_text has no effect unless blob_format is text"),
		},
		{
			desc: "enable_incremental_read with otlp is rejected",
			cfg: &Config{
				ConnectionString:      "connection_string",
				Container:             "container",
				PollInterval:          5 * time.Minute,
				BatchSize:             30,
				PageSize:              1000,
				BlobFormat:            BlobFormatOTLP,
				EnableIncrementalRead: true,
			},
			expectErr: errors.New("enable_incremental_read has no effect with blob_format otlp"),
		},
		{
			desc: "enable_incremental_read with the default (empty) format is rejected",
			cfg: &Config{
				ConnectionString:      "connection_string",
				Container:             "container",
				PollInterval:          5 * time.Minute,
				BatchSize:             30,
				PageSize:              1000,
				EnableIncrementalRead: true,
			},
			expectErr: errors.New("enable_incremental_read has no effect with blob_format otlp"),
		},
		{
			desc: "incremental_revisit_window without enable_incremental_read is rejected",
			cfg: &Config{
				ConnectionString:         "connection_string",
				Container:                "container",
				PollInterval:             5 * time.Minute,
				BatchSize:                30,
				PageSize:                 1000,
				IncrementalRevisitWindow: time.Hour,
			},
			expectErr: errors.New("incremental_revisit_window has no effect without enable_incremental_read"),
		},
		{
			desc: "fingerprint_size without enable_incremental_read is rejected",
			cfg: &Config{
				ConnectionString: "connection_string",
				Container:        "container",
				PollInterval:     5 * time.Minute,
				BatchSize:        30,
				PageSize:         1000,
				FingerprintSize:  512,
			},
			expectErr: errors.New("fingerprint_size has no effect without enable_incremental_read"),
		},
		{
			desc: "fingerprint_size below the minimum is rejected",
			cfg: &Config{
				ConnectionString:      "connection_string",
				Container:             "container",
				PollInterval:          5 * time.Minute,
				BatchSize:             30,
				PageSize:              1000,
				BlobFormat:            BlobFormatRecordsJSON,
				EnableIncrementalRead: true,
				FingerprintSize:       8,
			},
			expectErr: errors.New("fingerprint_size must be at least 16"),
		},
		{
			desc: "assume_append_only without enable_incremental_read is rejected",
			cfg: &Config{
				ConnectionString: "connection_string",
				Container:        "container",
				PollInterval:     5 * time.Minute,
				BatchSize:        30,
				PageSize:         1000,
				AssumeAppendOnly: true,
			},
			expectErr: errors.New("assume_append_only has no effect without enable_incremental_read"),
		},
		{
			desc: "fingerprint_size and assume_append_only with enable_incremental_read are accepted",
			cfg: &Config{
				ConnectionString:      "connection_string",
				Container:             "container",
				PollInterval:          5 * time.Minute,
				BatchSize:             30,
				PageSize:              1000,
				BlobFormat:            BlobFormatRecordsJSON,
				EnableIncrementalRead: true,
				FingerprintSize:       1000,
				AssumeAppendOnly:      true,
			},
			expectErr: nil,
		},
		{
			desc: "incremental_revisit_window shorter than poll_interval is rejected",
			cfg: &Config{
				ConnectionString:         "connection_string",
				Container:                "container",
				PollInterval:             5 * time.Minute,
				BatchSize:                30,
				PageSize:                 1000,
				BlobFormat:               BlobFormatRecordsJSON,
				EnableIncrementalRead:    true,
				IncrementalRevisitWindow: time.Minute,
			},
			expectErr: errors.New("must be at least poll_interval"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.expectErr == nil {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.expectErr.Error())
			}
		})
	}
}
