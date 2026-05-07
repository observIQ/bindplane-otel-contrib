package bundle

import "time"

// Artifact represents a file to be included in the bundle.
type Artifact struct {
	Name        string    // path inside the payload (e.g. system/os/system_info.json)
	Data        []byte    // file content
	Type        string    // system, collector, sensor, or logs (for manifest)
	CollectedAt time.Time // when collected (RFC3339 in manifest)
	Hash        string    // optional sha256 for manifest
}

// ArtifactSource is an abstraction for something that can return artifacts
type ArtifactSource interface {
	Collect(opts BundleOptions) ([]Artifact, error)
}


