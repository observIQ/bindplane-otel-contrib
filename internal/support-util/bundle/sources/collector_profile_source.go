package sources

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/observiq/bindplane-otel-contrib/internal/support-util/bundle"
)

// CollectorProfileSource collects Otel Collector golang profile files (pprof)
type CollectorProfileSource struct {
	ProfileDir string
}

// NewCollectorProfileSource creates a new collector profile source
func NewCollectorProfileSource(profileDir string) *CollectorProfileSource {
	return &CollectorProfileSource{
		ProfileDir: profileDir,
	}
}

// Collect gathers collector profile files as artifacts
func (s *CollectorProfileSource) Collect(opts bundle.BundleOptions) ([]bundle.Artifact, error) {
	// Only collect if profiles are enabled
	if !opts.IncludeProfiles {
		return nil, nil
	}

	var artifacts []bundle.Artifact

	// Use collector profile directory from options if provided, otherwise use configured directory
	profileDir := opts.Collector.ProfileDir
	if profileDir == "" {
		profileDir = s.ProfileDir
	}
	if profileDir == "" {
		// If no profile directory is configured, return empty (not an error)
		return nil, nil
	}

	// Create scoped root to prevent directory traversal attacks
	root, err := os.OpenRoot(profileDir)
	if err != nil {
		// Directory doesn't exist or can't be opened, return empty (not an error)
		return nil, nil
	}
	defer func() { _ = root.Close() }()

	// Calculate cutoff time for recent profiles (default: last 24 hours)
	cutoffTime := time.Now().Add(-24 * time.Hour)
	if opts.Collector.ProfileMaxAge > 0 {
		cutoffTime = time.Now().Add(-time.Duration(opts.Collector.ProfileMaxAge) * time.Hour)
	}

	// Walk directory and collect profile files using scoped root
	rootFS := root.FS()
	err = fs.WalkDir(rootFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // Skip errors, continue walking
		}

		if d.IsDir() {
			return nil
		}

		// Get file info for mod time check
		info, err := d.Info()
		if err != nil {
			return nil // Skip if we can't get info
		}

		// Check if file is a profile file
		ext := strings.ToLower(filepath.Ext(path))
		baseName := strings.ToLower(filepath.Base(path))

		// Look for .prof, .pprof extensions or files with "profile" in name
		isProfile := ext == ".prof" || ext == ".pprof" ||
			strings.Contains(baseName, "profile") ||
			strings.Contains(baseName, "cpu") ||
			strings.Contains(baseName, "mem") ||
			strings.Contains(baseName, "goroutine") ||
			strings.Contains(baseName, "heap") ||
			strings.Contains(baseName, "block") ||
			strings.Contains(baseName, "mutex")

		if !isProfile {
			return nil
		}

		// Check if file is recent enough (if max age is specified)
		if opts.Collector.ProfileMaxAge > 0 && info.ModTime().Before(cutoffTime) {
			return nil
		}

		// Read file contents using scoped root
		data, err := root.ReadFile(path)
		if err != nil {
			// Log error but continue with other files
			return nil
		}

		// Use path directly as it's already relative to root
		relPath := path

		artifacts = append(artifacts, bundle.Artifact{
			Name:        filepath.Join("collector/state/profiles", relPath),
			Data:        data,
			Type:        "collector",
			CollectedAt: time.Now(),
		})

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to walk profile directory: %v", err)
	}

	return artifacts, nil
}
