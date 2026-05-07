package sources

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/observiq/bindplane-otel-contrib/internal/support-util/bundle"
)

// CollectorLogSource collects Otel Collector log files
type CollectorLogSource struct {
	LogDir string
}

// NewCollectorLogSource creates a new collector log source
func NewCollectorLogSource(logDir string) *CollectorLogSource {
	return &CollectorLogSource{
		LogDir: logDir,
	}
}

// Collect gathers collector log files as artifacts
func (s *CollectorLogSource) Collect(opts bundle.BundleOptions) ([]bundle.Artifact, error) {
	var artifacts []bundle.Artifact

	// Use collector log directory from options if provided, otherwise use configured directory
	logDir := opts.CollectorLogDir
	if logDir == "" {
		logDir = s.LogDir
	}
	if logDir == "" {
		// If no collector log directory is configured, return empty (not an error)
		return nil, nil
	}

	// Create scoped root to prevent directory traversal attacks
	root, err := os.OpenRoot(logDir)
	if err != nil {
		// Directory doesn't exist or can't be opened, return empty (not an error)
		return nil, nil
	}
	defer func() { _ = root.Close() }()

	// Files to look for
	targetFiles := []string{"collector.log", "collector.err"}

	now := time.Now()
	// Collect each target file
	for _, targetFile := range targetFiles {
		// Collect current file if it exists using scoped root
		if data, err := root.ReadFile(targetFile); err == nil {
			artifacts = append(artifacts, bundle.Artifact{
				Name:        filepath.Join("collector/runtime", targetFile),
				Data:        data,
				Type:        "collector",
				CollectedAt: now,
			})
		}

		// If CollectAllLogs is enabled, also collect rotated logs
		if opts.CollectAllLogs {
			rotatedFiles, err := findRotatedLogs(root, targetFile)
			if err != nil {
				// Log error but continue
				continue
			}

			for _, rotatedFileName := range rotatedFiles {
				data, err := root.ReadFile(rotatedFileName)
				if err != nil {
					continue
				}

				relPath := rotatedFileName

				artifacts = append(artifacts, bundle.Artifact{
					Name:        filepath.Join("collector/runtime", relPath),
					Data:        data,
					Type:        "collector",
					CollectedAt: now,
				})
			}
		}
	}

	return artifacts, nil
}

// findRotatedLogs finds rotated log files matching common patterns using scoped root
func findRotatedLogs(root *os.Root, baseFile string) ([]string, error) {
	var rotatedFiles []string

	// Common rotated log patterns:
	// - collector.log.1, collector.log.2, etc. (numbered)
	// - collector.log.20240101, collector.log.20240101_150405 (timestamped)
	// - collector.log.old, collector.log.bak (backup extensions)

	baseName := strings.TrimSuffix(baseFile, filepath.Ext(baseFile))
	ext := filepath.Ext(baseFile)

	// Pattern for numbered rotation: base.log.1, base.log.2, etc.
	numberedPattern := regexp.MustCompile(fmt.Sprintf(`^%s\.\d+%s$`, regexp.QuoteMeta(baseName), regexp.QuoteMeta(ext)))

	// Pattern for timestamped rotation: base.log.20240101, base.log.20240101_150405
	timestampPattern := regexp.MustCompile(fmt.Sprintf(`^%s\.\d{8}(_\d{6})?%s$`, regexp.QuoteMeta(baseName), regexp.QuoteMeta(ext)))

	// Pattern for backup extensions: base.log.old, base.log.bak
	backupPattern := regexp.MustCompile(fmt.Sprintf(`^%s\.(old|bak|backup)%s$`, regexp.QuoteMeta(baseName), regexp.QuoteMeta(ext)))

	// Use root's FS to walk the directory safely
	rootFS := root.FS()
	err := fs.WalkDir(rootFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // Skip errors, continue walking
		}

		if d.IsDir() {
			return nil
		}

		fileName := filepath.Base(path)

		// Check if file matches any rotated log pattern
		if numberedPattern.MatchString(fileName) ||
			timestampPattern.MatchString(fileName) ||
			backupPattern.MatchString(fileName) {
			rotatedFiles = append(rotatedFiles, path)
		}

		return nil
	})

	return rotatedFiles, err
}
