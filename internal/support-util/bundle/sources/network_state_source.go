package sources

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/observiq/bindplane-otel-contrib/internal/support-util/bundle"
)

// NetworkStateSource collects network state information
type NetworkStateSource struct {
	GetNetworkStateFunc func() (interface{}, error)
}

// NewNetworkStateSource creates a new network state source
func NewNetworkStateSource(getNetworkStateFunc func() (interface{}, error)) *NetworkStateSource {
	return &NetworkStateSource{
		GetNetworkStateFunc: getNetworkStateFunc,
	}
}

// Collect gathers network state information as an artifact
func (s *NetworkStateSource) Collect(opts bundle.BundleOptions) ([]bundle.Artifact, error) {
	if !opts.IncludeNetworkState {
		return nil, nil
	}

	if s.GetNetworkStateFunc == nil {
		return nil, fmt.Errorf("network state function not provided")
	}

	networkState, err := s.GetNetworkStateFunc()
	if err != nil {
		return nil, fmt.Errorf("failed to get network state: %v", err)
	}

	data, err := json.MarshalIndent(networkState, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal network state: %v", err)
	}

	return []bundle.Artifact{
		{
			Name:        "system/network/network_state.json",
			Data:        data,
			Type:        "system",
			CollectedAt: time.Now(),
		},
	}, nil
}
