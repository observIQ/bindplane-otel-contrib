package sources

import (
	"encoding/json"
	"fmt"
	"runtime"
	"time"

	"github.com/observiq/bindplane-otel-contrib/pkg/support-util/bundle"
)

// SystemInfoSource collects system information
type SystemInfoSource struct{}

// NewSystemInfoSource creates a new system info source
func NewSystemInfoSource() *SystemInfoSource {
	return &SystemInfoSource{}
}

// SystemInfo represents system information
type SystemInfo struct {
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	CPUCount  int    `json:"cpu_count"`
	GoVersion string `json:"go_version"`
}

// Collect gathers system information as an artifact
func (s *SystemInfoSource) Collect(opts bundle.BundleOptions) ([]bundle.Artifact, error) {
	if !opts.IncludeSystemInfo {
		return nil, nil
	}

	info := SystemInfo{
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		CPUCount:  runtime.NumCPU(),
		GoVersion: runtime.Version(),
	}

	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal system info: %v", err)
	}

	return []bundle.Artifact{
		{
			Name:        "system/os/system_info.json",
			Data:        data,
			Type:        "system",
			CollectedAt: time.Now(),
		},
	}, nil
}
