package sources

import (
	"os"
	"time"

	"github.com/observiq/bindplane-otel-contrib/internal/support-util/bundle"
)

// CollectorRootExtrasSource collects logging.yaml and version.txt from the collector install root.
// It is used when opts.CollectorInstallRoot is set (e.g. from config or Windows registry discovery).
type CollectorRootExtrasSource struct{}

// NewCollectorRootExtrasSource creates a new collector root extras source.
func NewCollectorRootExtrasSource() *CollectorRootExtrasSource {
	return &CollectorRootExtrasSource{}
}

// Collect gathers logging.yaml and version.txt (or VERSION.txt) from the install root as artifacts.
// Missing files are skipped without error.
func (s *CollectorRootExtrasSource) Collect(opts bundle.BundleOptions) ([]bundle.Artifact, error) {
	if opts.CollectorInstallRoot == "" {
		return nil, nil
	}

	root, err := os.OpenRoot(opts.CollectorInstallRoot)
	if err != nil {
		return nil, nil
	}
	defer root.Close()

	var artifacts []bundle.Artifact

	now := time.Now()
	// logging.yaml
	if data, err := root.ReadFile("logging.yaml"); err == nil {
		sanitized := StripSensitiveInfo(string(data))
		artifacts = append(artifacts, bundle.Artifact{
			Name:        "collector/config/logging.yaml",
			Data:        []byte(sanitized),
			Type:        "collector",
			CollectedAt: now,
		})
	}

	// version.txt or VERSION.txt
	for _, name := range []string{"version.txt", "VERSION.txt"} {
		if data, err := root.ReadFile(name); err == nil {
			artifacts = append(artifacts, bundle.Artifact{
				Name:        "collector/state/version.txt",
				Data:        data,
				Type:        "collector",
				CollectedAt: now,
			})
			break
		}
	}

	return artifacts, nil
}
