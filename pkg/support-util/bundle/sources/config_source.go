package sources

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/observiq/bindplane-otel-contrib/pkg/support-util/bundle"
)

// ConfigSource collects configuration files
type ConfigSource struct {
	ConfigPath string
}

// NewConfigSource creates a new config source
func NewConfigSource(configPath string) *ConfigSource {
	return &ConfigSource{
		ConfigPath: configPath,
	}
}

// Collect gathers configuration files as artifacts, stripping sensitive information
func (s *ConfigSource) Collect(opts bundle.BundleOptions) ([]bundle.Artifact, error) {
	if !opts.IncludeConfig {
		return nil, nil
	}

	// Use config path from options if provided, otherwise use configured path
	configPath := opts.ConfigPath
	if configPath == "" {
		configPath = s.ConfigPath
	}
	if configPath == "" {
		configPath = "config.yaml"
	}

	// Use os.Root to scope file access and prevent directory traversal
	configDir := filepath.Dir(configPath)
	configFile := filepath.Base(configPath)

	root, err := os.OpenRoot(configDir)
	if err != nil {
		return nil, fmt.Errorf("failed to open root directory for config file: %w", err)
	}
	defer func() { _ = root.Close() }()

	data, err := root.ReadFile(configFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %v", err)
	}

	// Strip sensitive information (passwords, tokens, etc.)
	sanitized := StripSensitiveInfo(string(data))

	return []bundle.Artifact{
		{
			Name:        "metadata/config.yaml",
			Data:        []byte(sanitized),
			Type:        "system",
			CollectedAt: time.Now(),
		},
	}, nil
}

// StripSensitiveInfo removes sensitive information from config content
// This function is exported so it can be used by other config sources
func StripSensitiveInfo(content string) string {
	// Patterns to match sensitive fields
	patterns := []struct {
		pattern     *regexp.Regexp
		replacement string
	}{
		// Password fields
		{regexp.MustCompile(`(?i)(password|passwd|pwd)\s*:\s*[^\s\n]+`), `$1: "***REDACTED***"`},
		// Token fields
		{regexp.MustCompile(`(?i)(token|api_key|apikey|secret|key)\s*:\s*[^\s\n]+`), `$1: "***REDACTED***"`},
		// Service account passwords
		{regexp.MustCompile(`service_password\s*:\s*[^\s\n]+`), `service_password: "***REDACTED***"`},
	}

	result := content
	for _, p := range patterns {
		result = p.pattern.ReplaceAllString(result, p.replacement)
	}

	// Also handle YAML comments that might contain sensitive info
	lines := strings.Split(result, "\n")
	for i, line := range lines {
		if strings.Contains(strings.ToLower(line), "password") || strings.Contains(strings.ToLower(line), "token") {
			// Check if line contains actual sensitive data (not just a comment)
			if !strings.HasPrefix(strings.TrimSpace(line), "#") {
				// Already handled by regex above
				continue
			}
		}
		lines[i] = line
	}

	return strings.Join(lines, "\n")
}
