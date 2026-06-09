package bundle

import (
	"encoding/json"
	"time"
)

// Manifest is the authoritative index of bundle contents (see spec manifest.json).
type Manifest struct {
	BundleID      string         `json:"bundle_id"`
	GeneratedAt   string         `json:"generated_at"` // RFC3339
	SchemaVersion string         `json:"schema_version"`
	Artifacts     []ManifestEntry `json:"artifacts"`
}

// ManifestEntry describes one artifact in the bundle.
type ManifestEntry struct {
	Path        string `json:"path"`
	Type        string `json:"type"` // system, collector, sensor, logs
	Source      string `json:"source"`
	CollectedAt string `json:"collected_at,omitempty"` // RFC3339
	Hash        string `json:"hash,omitempty"`        // sha256:...
	SensorType  string `json:"sensor_type,omitempty"`
	EventCount  int    `json:"event_count,omitempty"`
}

// BuildManifest produces manifest.json bytes from collected artifacts.
func BuildManifest(artifacts []Artifact, bundleID, schemaVersion string, generatedAt time.Time) ([]byte, error) {
	entries := make([]ManifestEntry, 0, len(artifacts))
	for _, a := range artifacts {
		typ := a.Type
		if typ == "" {
			typ = "system"
		}
		source := "host"
		switch typ {
		case "collector":
			source = "collector"
		case "sensor":
			source = "sensor"
		case "logs":
			source = "agent"
		}
		collectedAt := ""
		if !a.CollectedAt.IsZero() {
			collectedAt = a.CollectedAt.UTC().Format(time.RFC3339)
		}
		entries = append(entries, ManifestEntry{
			Path:        a.Name,
			Type:        typ,
			Source:      source,
			CollectedAt: collectedAt,
			Hash:        a.Hash,
		})
	}
	m := Manifest{
		BundleID:      bundleID,
		GeneratedAt:   generatedAt.UTC().Format(time.RFC3339),
		SchemaVersion: schemaVersion,
		Artifacts:     entries,
	}
	return json.MarshalIndent(m, "", "  ")
}
