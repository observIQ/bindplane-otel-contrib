package googlesecopsexporter

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConfigValidate(t *testing.T) {
	testCases := []struct {
		desc        string
		config      *Config
		expectedErr string
	}{
		{
			desc: "Both creds_file_path and creds are set",
			config: &Config{
				CredsFilePath:         "/path/to/creds_file",
				Creds:                 "creds_example",
				DefaultLogType:        "log_type_example",
				Compression:           noCompression,
				BatchRequestSizeLimit: defaultBatchRequestSizeLimit,
			},
			expectedErr: "can only specify creds_file_path or creds",
		},
		{
			desc: "Valid backstory config with creds",
			config: &Config{
				Creds:                 "creds_example",
				DefaultLogType:        "log_type_example",
				Compression:           noCompression,
				API:                   backstoryAPI,
				BatchRequestSizeLimit: defaultBatchRequestSizeLimit,
			},
		},
		{
			desc: "Valid backstory config with creds_file_path",
			config: &Config{
				CredsFilePath:         "/path/to/creds_file",
				DefaultLogType:        "log_type_example",
				Compression:           noCompression,
				API:                   backstoryAPI,
				BatchRequestSizeLimit: defaultBatchRequestSizeLimit,
			},
		},
		{
			desc: "Valid backstory config with raw log field",
			config: &Config{
				CredsFilePath:         "/path/to/creds_file",
				DefaultLogType:        "log_type_example",
				RawLogField:           `body["field"]`,
				Compression:           noCompression,
				API:                   backstoryAPI,
				BatchRequestSizeLimit: defaultBatchRequestSizeLimit,
			},
		},
		{
			desc: "Invalid raw log field",
			config: &Config{
				CredsFilePath:         "/path/to/creds_file",
				DefaultLogType:        "log_type_example",
				RawLogField:           `invalid[`,
				Compression:           noCompression,
				API:                   backstoryAPI,
				BatchRequestSizeLimit: defaultBatchRequestSizeLimit,
			},
			expectedErr: "raw_log_field is invalid",
		},
		{
			desc: "Invalid compression type",
			config: &Config{
				CredsFilePath:  "/path/to/creds_file",
				DefaultLogType: "log_type_example",
				Compression:    "invalid",
			},
			expectedErr: "invalid compression type",
		},
		{
			desc: "Hostname with http protocol prefix",
			config: &Config{
				Hostname:              "http://chronicle.googleapis.com",
				CredsFilePath:         "/path/to/creds_file",
				DefaultLogType:        "log_type_example",
				Compression:           noCompression,
				BatchRequestSizeLimit: defaultBatchRequestSizeLimit,
			},
			expectedErr: "host should not contain a protocol prefix",
		},
		{
			desc: "Hostname with https protocol prefix",
			config: &Config{
				Hostname:              "https://chronicle.googleapis.com",
				CredsFilePath:         "/path/to/creds_file",
				DefaultLogType:        "log_type_example",
				Compression:           noCompression,
				BatchRequestSizeLimit: defaultBatchRequestSizeLimit,
			},
			expectedErr: "host should not contain a protocol prefix",
		},
		{
			desc: "Invalid batch request size limit",
			config: &Config{
				Creds:                 "creds_example",
				DefaultLogType:        "log_type_example",
				Compression:           noCompression,
				API:                   backstoryAPI,
				BatchRequestSizeLimit: 0,
			},
			expectedErr: "positive batch request size limit is required",
		},
		{
			desc: "Negative batch request size limit",
			config: &Config{
				Creds:                 "creds_example",
				DefaultLogType:        "log_type_example",
				Compression:           noCompression,
				API:                   backstoryAPI,
				BatchRequestSizeLimit: -1,
			},
			expectedErr: "positive batch request size limit is required",
		},
		{
			desc: "Chronicle API missing location",
			config: &Config{
				Hostname:              "chronicle.googleapis.com",
				CredsFilePath:         "/path/to/creds_file",
				DefaultLogType:        "log_type_example",
				API:                   chronicleAPI,
				Compression:           noCompression,
				ProjectNumber:         "project_example",
				BatchRequestSizeLimit: defaultBatchRequestSizeLimit,
			},
			expectedErr: "location is required for the Chronicle API",
		},
		{
			desc: "Chronicle API missing hostname",
			config: &Config{
				CredsFilePath:         "/path/to/creds_file",
				DefaultLogType:        "log_type_example",
				API:                   chronicleAPI,
				Compression:           noCompression,
				ProjectNumber:         "project_example",
				BatchRequestSizeLimit: defaultBatchRequestSizeLimit,
				Location:              "us",
			},
			expectedErr: "base URL is required for the Chronicle API",
		},
		{
			desc: "Chronicle API missing project number",
			config: &Config{
				Hostname:              "chronicle.googleapis.com",
				CredsFilePath:         "/path/to/creds_file",
				DefaultLogType:        "log_type_example",
				API:                   chronicleAPI,
				Compression:           noCompression,
				Location:              "us",
				BatchRequestSizeLimit: defaultBatchRequestSizeLimit,
			},
			expectedErr: "project number is required for the Chronicle API",
		},
		{
			desc: "Chronicle API batch request size limit zero",
			config: &Config{
				Hostname:              "chronicle.googleapis.com",
				CredsFilePath:         "/path/to/creds_file",
				DefaultLogType:        "log_type_example",
				API:                   chronicleAPI,
				Compression:           noCompression,
				ProjectNumber:         "project_example",
				Location:              "us",
				BatchRequestSizeLimit: 0,
			},
			expectedErr: "positive batch request size limit is required",
		},
		{
			desc: "Valid chronicle config",
			config: &Config{
				Hostname:              "chronicle.googleapis.com",
				CredsFilePath:         "/path/to/creds_file",
				DefaultLogType:        "log_type_example",
				API:                   chronicleAPI,
				Compression:           noCompression,
				ProjectNumber:         "project_example",
				Location:              "us",
				BatchRequestSizeLimit: defaultBatchRequestSizeLimit,
			},
		},
		{
			desc: "Valid chronicle config with v1beta API version",
			config: &Config{
				Hostname:              "chronicle.googleapis.com",
				CredsFilePath:         "/path/to/creds_file",
				DefaultLogType:        "log_type_example",
				API:                   chronicleAPI,
				Compression:           noCompression,
				ProjectNumber:         "project_example",
				Location:              "us",
				BatchRequestSizeLimit: defaultBatchRequestSizeLimit,
				APIVersion:            apiVersionV1Beta,
			},
		},
		{
			desc: "Valid chronicle config with v1alpha API version",
			config: &Config{
				Hostname:              "chronicle.googleapis.com",
				CredsFilePath:         "/path/to/creds_file",
				DefaultLogType:        "log_type_example",
				API:                   chronicleAPI,
				Compression:           noCompression,
				ProjectNumber:         "project_example",
				Location:              "us",
				BatchRequestSizeLimit: defaultBatchRequestSizeLimit,
				APIVersion:            apiVersionV1Alpha,
			},
		},
		{
			desc: "Invalid API version",
			config: &Config{
				Hostname:              "chronicle.googleapis.com",
				CredsFilePath:         "/path/to/creds_file",
				DefaultLogType:        "log_type_example",
				API:                   chronicleAPI,
				Compression:           noCompression,
				ProjectNumber:         "project_example",
				Location:              "us",
				BatchRequestSizeLimit: defaultBatchRequestSizeLimit,
				APIVersion:            "invalid",
			},
			expectedErr: "invalid API version: invalid",
		},
		{
			desc: "Invalid API",
			config: &Config{
				Hostname:              "chronicle.googleapis.com",
				CredsFilePath:         "/path/to/creds_file",
				DefaultLogType:        "log_type_example",
				Compression:           noCompression,
				BatchRequestSizeLimit: defaultBatchRequestSizeLimit,
				API:                   "invalid_api",
			},
			expectedErr: "invalid API: invalid_api",
		},
		{
			desc: "Valid chronicle config with gzip compression",
			config: &Config{
				Hostname:              "chronicle.googleapis.com",
				CredsFilePath:         "/path/to/creds_file",
				DefaultLogType:        "log_type_example",
				API:                   chronicleAPI,
				Compression:           "gzip",
				ProjectNumber:         "project_example",
				Location:              "us",
				BatchRequestSizeLimit: defaultBatchRequestSizeLimit,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			err := tc.config.Validate()
			if tc.expectedErr == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.expectedErr)
			}
		})
	}
}
