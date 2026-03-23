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
			desc: "Valid config with creds",
			config: &Config{
				Creds:                 "creds_example",
				DefaultLogType:        "log_type_example",
				Compression:           noCompression,
				API:                   backstoryAPI,
				BatchRequestSizeLimit: defaultBatchRequestSizeLimit,
			},
			expectedErr: "",
		},
		{
			desc: "Valid config with creds_file_path",
			config: &Config{
				CredsFilePath:         "/path/to/creds_file",
				DefaultLogType:        "log_type_example",
				Compression:           noCompression,
				API:                   backstoryAPI,
				BatchRequestSizeLimit: defaultBatchRequestSizeLimit,
			},
			expectedErr: "",
		},
		{
			desc: "Valid config with raw log field",
			config: &Config{
				CredsFilePath:         "/path/to/creds_file",
				DefaultLogType:        "log_type_example",
				RawLogField:           `body["field"]`,
				Compression:           noCompression,
				API:                   backstoryAPI,
				BatchRequestSizeLimit: defaultBatchRequestSizeLimit,
			},
			expectedErr: "",
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
			expectedErr: "positive batch request size limit is required when protocol is grpc",
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
			desc: "Protocol is https and endpoint is empty",
			config: &Config{
				CredsFilePath:         "/path/to/creds_file",
				DefaultLogType:        "log_type_example",
				API:                   chronicleAPI,
				Compression:           noCompression,
				ProjectNumber:         "project_example",
				BatchRequestSizeLimit: defaultBatchRequestSizeLimit,
				Location:              "location_example",
			},
			expectedErr: "endpoint is required when protocol is https",
		},
		{
			desc: "Protocol is https and forwarder is empty",
			config: &Config{
				Hostname:              "myendpoint.com",
				CredsFilePath:         "/path/to/creds_file",
				DefaultLogType:        "log_type_example",
				API:                   chronicleAPI,
				Compression:           noCompression,
				ProjectNumber:         "project_example",
				Location:              "location_example",
				BatchRequestSizeLimit: defaultBatchRequestSizeLimit,
			},
			expectedErr: "",
		},
		{
			desc: "Protocol is https and project is empty",
			config: &Config{
				Hostname:              "myendpoint.com",
				CredsFilePath:         "/path/to/creds_file",
				DefaultLogType:        "log_type_example",
				API:                   chronicleAPI,
				Compression:           noCompression,
				Location:              "location_example",
				BatchRequestSizeLimit: defaultBatchRequestSizeLimit,
			},
			expectedErr: "project is required when protocol is https",
		},
		{
			desc: "Protocol is https and http batch request size limit is 0",
			config: &Config{
				Hostname:              "myendpoint.com",
				CredsFilePath:         "/path/to/creds_file",
				DefaultLogType:        "log_type_example",
				API:                   chronicleAPI,
				Compression:           noCompression,
				ProjectNumber:         "project_example",
				Location:              "location_example",
				BatchRequestSizeLimit: 0,
			},
			expectedErr: "positive batch request size limit is required when protocol is https",
		},
		{
			desc: "Valid https config",
			config: &Config{
				Hostname:              "myendpoint.com",
				CredsFilePath:         "/path/to/creds_file",
				DefaultLogType:        "log_type_example",
				API:                   chronicleAPI,
				Compression:           noCompression,
				ProjectNumber:         "project_example",
				Location:              "location_example",
				BatchRequestSizeLimit: defaultBatchRequestSizeLimit,
			},
		},
		{
			desc: "Valid https config with custom API version",
			config: &Config{
				Hostname:              "myendpoint.com",
				CredsFilePath:         "/path/to/creds_file",
				DefaultLogType:        "log_type_example",
				API:                   chronicleAPI,
				Compression:           noCompression,
				ProjectNumber:         "project_example",
				Location:              "location_example",
				BatchRequestSizeLimit: defaultBatchRequestSizeLimit,
				APIVersion:            "v1beta",
			},
		},
		{
			desc: "Invalid API version",
			config: &Config{
				Hostname:              "myendpoint.com",
				CredsFilePath:         "/path/to/creds_file",
				DefaultLogType:        "log_type_example",
				API:                   chronicleAPI,
				Compression:           noCompression,
				ProjectNumber:         "project_example",
				Location:              "location_example",
				BatchRequestSizeLimit: defaultBatchRequestSizeLimit,
				APIVersion:            "invalid",
			},
			expectedErr: "invalid API version: invalid",
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
