package bundle

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// BundleWriter defines the interface for writing bundles
type BundleWriter interface {
	WriteBundle(name string, artifacts []Artifact) (string, error)
}

// TarGzWriter implements BundleWriter using tar and gzip
type TarGzWriter struct{}

// WriteBundle creates a tar.gz file containing all artifacts
func (w *TarGzWriter) WriteBundle(name string, artifacts []Artifact) (string, error) {
	// Create output directory if it doesn't exist
	outputDir := filepath.Dir(name)
	outputFile := filepath.Base(name)

	if outputDir == "" || outputDir == "." {
		return "", fmt.Errorf("output directory must be specified and cannot be current directory")
	}

	if err := os.MkdirAll(outputDir, 0750); err != nil {
		return "", fmt.Errorf("failed to create output directory: %v", err)
	}

	// Create scoped root to prevent directory traversal
	root, err := os.OpenRoot(outputDir)
	if err != nil {
		return "", fmt.Errorf("failed to open root directory for bundle output: %w", err)
	}
	defer func() { _ = root.Close() }()

	// Create the tar.gz file using scoped root
	file, err := root.Create(outputFile)
	if err != nil {
		return "", fmt.Errorf("failed to create bundle file: %v", err)
	}
	defer func() { _ = file.Close() }()

	// Create gzip writer
	gzWriter := gzip.NewWriter(file)
	defer func() { _ = gzWriter.Close() }()

	// Create tar writer
	tarWriter := tar.NewWriter(gzWriter)
	defer func() { _ = tarWriter.Close() }()

	// Write each artifact to the tar archive
	for _, artifact := range artifacts {
		header := &tar.Header{
			Name:    artifact.Name,
			Size:    int64(len(artifact.Data)),
			Mode:    0644,
			ModTime: time.Now(),
		}

		if err := tarWriter.WriteHeader(header); err != nil {
			return "", fmt.Errorf("failed to write tar header for %s: %v", artifact.Name, err)
		}

		if _, err := tarWriter.Write(artifact.Data); err != nil {
			return "", fmt.Errorf("failed to write artifact data for %s: %v", artifact.Name, err)
		}
	}

	return name, nil
}
