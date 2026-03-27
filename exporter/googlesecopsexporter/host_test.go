package googlesecopsexporter

import (
	"errors"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMACAddress(t *testing.T) {
	mac := macAddress()
	require.NotEqual(t, unknownValue, mac)
}

func TestFindMACAddress(t *testing.T) {
	testCases := []struct {
		name       string
		interfaces func() ([]net.Interface, error)
		expected   string
	}{
		{
			name: "error returns unknown",
			interfaces: func() ([]net.Interface, error) {
				return nil, errors.New("failure")
			},
			expected: unknownValue,
		},
		{
			name: "no interfaces returns unknown",
			interfaces: func() ([]net.Interface, error) {
				return []net.Interface{}, nil
			},
			expected: unknownValue,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mac := findMACAddress(tc.interfaces)
			require.Equal(t, tc.expected, mac)
		})
	}
}
