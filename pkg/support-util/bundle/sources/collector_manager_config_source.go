package sources

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/observiq/bindplane-otel-contrib/pkg/support-util/bundle"
)

// CollectorManagerConfigSource collects Otel Collector manager configuration files
type CollectorManagerConfigSource struct {
	ManagerConfigPath string
}

// NewCollectorManagerConfigSource creates a new collector manager config source
func NewCollectorManagerConfigSource(managerConfigPath string) *CollectorManagerConfigSource {
	return &CollectorManagerConfigSource{
		ManagerConfigPath: managerConfigPath,
	}
}

// Collect gathers collector manager configuration files as artifacts, stripping sensitive information
func (s *CollectorManagerConfigSource) Collect(opts bundle.BundleOptions) ([]bundle.Artifact, error) {
	// Use collector manager config path from options if provided, otherwise use configured path
	configPath := opts.Collector.ManagerConfigPath
	if configPath == "" {
		configPath = s.ManagerConfigPath
	}
	if configPath == "" {
		// If no manager config path is configured, return empty (not an error)
		return nil, nil
	}

	// Use os.Root to scope file access and prevent directory traversal
	configDir := filepath.Dir(configPath)
	configFile := filepath.Base(configPath)

	root, err := os.OpenRoot(configDir)
	if err != nil {
		return nil, fmt.Errorf("failed to open root directory for collector manager config file: %w", err)
	}
	defer func() { _ = root.Close() }()

	data, err := root.ReadFile(configFile)
	if err != nil {
		// If file doesn't exist, return empty (not an error)
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read collector manager config file: %v", err)
	}

	// Strip sensitive information (passwords, tokens, etc.)
	sanitized := StripSensitiveInfo(string(data))

	baseName := "manager.yaml"
	if strings.HasSuffix(strings.ToLower(configPath), ".yaml") || strings.HasSuffix(strings.ToLower(configPath), ".yml") {
		baseName = filepath.Base(configPath)
	}

	return []bundle.Artifact{
		{
			Name:        "collector/config/" + baseName,
			Data:        []byte(sanitized),
			Type:        "collector",
			CollectedAt: time.Now(),
		},
	}, nil
}
