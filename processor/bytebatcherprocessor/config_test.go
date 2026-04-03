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
