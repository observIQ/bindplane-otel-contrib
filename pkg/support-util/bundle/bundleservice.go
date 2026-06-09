package bundle

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

// BundleService orchestrates artifact collection and bundle creation
type BundleService struct {
	Sources []ArtifactSource
	Writer  BundleWriter
}

// CreateBundle collects artifacts from all sources and creates a bundle
func (svc *BundleService) CreateBundle(opts BundleOptions) (string, error) {
	var artifacts []Artifact

	// 1. Collect artifacts
	for _, src := range svc.Sources {
		collected, err := src.Collect(opts)
		if err != nil {
			return "", fmt.Errorf("failed to collect artifacts: %w", err)
		}
		artifacts = append(artifacts, collected...)
	}

	// 2. Build manifest and prepend as first file (spec: authoritative index)
	bundleID := uuid.New().String()
	generatedAt := time.Now()
	manifestJSON, err := BuildManifest(artifacts, bundleID, "1.0", generatedAt)
	if err != nil {
		return "", fmt.Errorf("build manifest: %w", err)
	}
	manifestArtifact := Artifact{Name: "manifest.json", Data: manifestJSON}
	artifacts = append([]Artifact{manifestArtifact}, artifacts...)

	// 3. Build filename (plaintext: .tar.gz or .zip; encrypted: .bundle)
	var filename string
	if opts.Encryption.Enabled {
		filename = opts.BuildEncryptedFilename()
	} else {
		filename = opts.BuildFilename()
	}
	if opts.OutputDir != "" {
		filename = filepath.Join(opts.OutputDir, filename)
	}

	// 4. Write payload archive (to temp path when encrypting, so we can read bytes and remove)
	writePath := filename
	if opts.Encryption.Enabled {
		// Write to a temp name so the final output is only the .bundle file
		writePath = filename + ".tmp"
	}
	archivePath, err := svc.Writer.WriteBundle(writePath, artifacts)
	if err != nil {
		return "", err
	}

	// 5. Optional encryption: produce single-file .bundle (header and manifest share bundleID/createdAt)
	if !opts.Encryption.Enabled {
		return archivePath, nil
	}

	return svc.encryptBundleToFile(archivePath, filename, opts, bundleID, generatedAt)
}

// encryptBundleToFile reads the plaintext archive at archivePath, builds the spec header,
// encrypts with AAD, and writes a single .bundle file at bundlePath. Removes the temp archive.
// bundleID and createdAt must match the manifest so header and payload are consistent.
func (svc *BundleService) encryptBundleToFile(
	archivePath string,
	bundlePath string,
	opts BundleOptions,
	bundleID string,
	createdAt time.Time,
) (string, error) {
	outputDir := filepath.Dir(archivePath)
	archiveBase := filepath.Base(archivePath)
	root, err := os.OpenRoot(outputDir)
	if err != nil {
		return "", fmt.Errorf("open bundle output dir: %w", err)
	}
	defer root.Close()
	payloadBytes, err := root.ReadFile(archiveBase)
	if err != nil {
		return "", err
	}

	pub, err := loadPublicKey(opts.Encryption)
	if err != nil {
		return "", err
	}

	fingerprint, err := RSAPublicKeyFingerprint(pub)
	if err != nil {
		return "", fmt.Errorf("public key fingerprint: %w", err)
	}

	hostname := opts.Hostname
	if hostname == "" {
		if h, err := os.Hostname(); err == nil {
			hostname = h
		}
	}
	opts.Hostname = hostname

	h := BuildHeader(opts, createdAt, fingerprint, bundleID)
	headerJSON, err := MarshalHeader(h)
	if err != nil {
		return "", fmt.Errorf("marshal header: %w", err)
	}

	env, err := EncryptEnvelopeWithAAD(pub, payloadBytes, headerJSON)
	if err != nil {
		return "", err
	}

	bundleBase := filepath.Base(bundlePath)
	if err := WriteBundleFile(filepath.Join(outputDir, bundleBase), headerJSON, env); err != nil {
		return "", err
	}

	if err := os.Remove(archivePath); err != nil {
		return "", fmt.Errorf("remove plaintext archive: %w", err)
	}

	return bundlePath, nil
}
