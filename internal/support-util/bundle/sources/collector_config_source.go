package sources

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/observiq/bindplane-otel-contrib/internal/support-util/bundle"
)

// CollectorConfigSource collects Otel Collector configuration files
type CollectorConfigSource struct {
	ConfigPath string
}

// NewCollectorConfigSource creates a new collector config source
func NewCollectorConfigSource(configPath string) *CollectorConfigSource {
	return &CollectorConfigSource{
		ConfigPath: configPath,
	}
}

// Collect gathers collector configuration files as artifacts, stripping sensitive information
func (s *CollectorConfigSource) Collect(opts bundle.BundleOptions) ([]bundle.Artifact, error) {
	// Use collector config path from options if provided, otherwise use configured path
	configPath := opts.Collector.ConfigPath
	if configPath == "" {
		configPath = s.ConfigPath
	}
	if configPath == "" {
		// If no collector config path is configured, return empty (not an error)
		return nil, nil
	}

	// Use os.Root to scope file access and prevent directory traversal
	configDir := filepath.Dir(configPath)
	configFile := filepath.Base(configPath)

	root, err := os.OpenRoot(configDir)
	if err != nil {
		return nil, fmt.Errorf("failed to open root directory for collector config file: %w", err)
	}
	defer func() { _ = root.Close() }()

	data, err := root.ReadFile(configFile)
	if err != nil {
		// If file doesn't exist, return empty (not an error)
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read collector config file: %v", err)
	}

	// Strip sensitive information (passwords, tokens, etc.)
	sanitized := StripSensitiveInfo(string(data))

	baseName := "collector.yaml"
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
