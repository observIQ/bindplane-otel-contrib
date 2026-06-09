package sources

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/observiq/bindplane-otel-contrib/pkg/support-util/bundle"
)

// CollectorPluginsSource collects all files from the collector install root's plugins/ directory.
// YAML/yml files are sanitized for sensitive data. Used when opts.Collector.InstallRoot is set.
type CollectorPluginsSource struct{}

// NewCollectorPluginsSource creates a new collector plugins source.
func NewCollectorPluginsSource() *CollectorPluginsSource {
	return &CollectorPluginsSource{}
}

// Collect gathers all files under plugins/ as artifacts under a plugins/ prefix.
func (s *CollectorPluginsSource) Collect(opts bundle.BundleOptions) ([]bundle.Artifact, error) {
	if opts.Collector.InstallRoot == "" {
		return nil, nil
	}

	pluginsDir := filepath.Join(opts.Collector.InstallRoot, "plugins")
	root, err := os.OpenRoot(pluginsDir)
	if err != nil {
		// Directory doesn't exist or can't be opened, return empty (not an error)
		return nil, nil
	}
	defer root.Close()

	var artifacts []bundle.Artifact
	rootFS := root.FS()
	err = fs.WalkDir(rootFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}

		data, err := root.ReadFile(path)
		if err != nil {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".yaml" || ext == ".yml" {
			data = []byte(StripSensitiveInfo(string(data)))
		}

		artifacts = append(artifacts, bundle.Artifact{
			Name:        filepath.Join("collector/plugins", path),
			Data:        data,
			Type:        "collector",
			CollectedAt: time.Now(),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to walk collector plugins directory: %w", err)
	}

	return artifacts, nil
}
