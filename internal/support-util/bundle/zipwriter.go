package bundle

import (
	"archive/zip"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// ZipWriter implements BundleWriter using zip format for Windows
type ZipWriter struct{}

// WriteBundle creates a zip file containing all artifacts
func (w *ZipWriter) WriteBundle(name string, artifacts []Artifact) (string, error) {
	// ZipWriter is intended for Windows, but we allow it on other platforms too
	// The caller should select the appropriate writer based on platform

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

	// Create the zip file using scoped root
	file, err := root.Create(outputFile)
	if err != nil {
		return "", fmt.Errorf("failed to create bundle file: %v", err)
	}
	defer func() { _ = file.Close() }()

	// Create zip writer
	zipWriter := zip.NewWriter(file)
	defer func() { _ = zipWriter.Close() }()

	// Write each artifact to the zip archive
	for _, artifact := range artifacts {
		// Create file header
		header := &zip.FileHeader{
			Name:     artifact.Name,
			Method:   zip.Deflate,
			Modified: time.Now(),
		}

		// Set file permissions (Unix-style, but Windows will handle appropriately)
		header.SetMode(0644)

		writer, err := zipWriter.CreateHeader(header)
		if err != nil {
			return "", fmt.Errorf("failed to create zip entry for %s: %v", artifact.Name, err)
		}

		if _, err := writer.Write(artifact.Data); err != nil {
			return "", fmt.Errorf("failed to write artifact data for %s: %v", artifact.Name, err)
		}
	}

	return name, nil
}

// IsSupported returns true if ZipWriter is supported on the current platform
func (w *ZipWriter) IsSupported() bool {
	return runtime.GOOS == "windows"
}

func WriteEncryptedBundle(
	writer *ZipWriter,
	publicKeyPath string,
	bundlePath string,
	artifacts []Artifact,
) (*Envelope, error) {

	// 1. Write zip bundle
	zipPath, err := writer.WriteBundle(bundlePath, artifacts)
	if err != nil {
		return nil, err
	}

	// 2. Read zip bytes (scoped to dir to prevent path traversal)
	zipDir, zipBase := filepath.Dir(zipPath), filepath.Base(zipPath)
	root, err := os.OpenRoot(zipDir)
	if err != nil {
		return nil, fmt.Errorf("open bundle dir: %w", err)
	}
	defer root.Close()
	zipBytes, err := root.ReadFile(zipBase)
	if err != nil {
		return nil, err
	}

	// 3. Load public key
	pub, err := LoadRSAPublicKey(publicKeyPath)
	if err != nil {
		return nil, err
	}

	// 4. Encrypt envelope
	env, err := EncryptEnvelope(pub, zipBytes)
	if err != nil {
		return nil, err
	}

	return env, nil
}
