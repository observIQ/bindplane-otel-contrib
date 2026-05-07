package bundle

import (
	"encoding/json"
	"runtime"
	"time"

	"github.com/google/uuid"
)

// BundleHeader is the plaintext, authenticated header of a .bundle file (see spec).
type BundleHeader struct {
	BundleID      string       `json:"bundle_id"`
	OrgID         string       `json:"org_id"`
	BundleVersion string       `json:"bundle_version"`
	CreatedAt     string       `json:"created_at"` // RFC3339
	Agent         AgentInfo    `json:"agent"`
	Collector     CollectorInfo `json:"collector"`
	Encryption    *EncryptionInfo `json:"encryption,omitempty"` // nil when not encrypted
	Contents      ContentsInfo `json:"contents"`
}

// AgentInfo identifies the agent that produced the bundle.
type AgentInfo struct {
	AgentID      string `json:"agent_id"`
	AgentVersion string `json:"agent_version"`
	Hostname     string `json:"hostname"`
	Platform     string `json:"platform"` // darwin|linux|windows
	Arch         string `json:"arch"`     // amd64|arm64
}

// CollectorInfo identifies the collector context.
type CollectorInfo struct {
	CollectorID      string `json:"collector_id"`
	CollectorVersion string `json:"collector_version"`
	InstallRoot      string `json:"install_root"`
}

// EncryptionInfo describes the encryption used (present only when encrypted).
type EncryptionInfo struct {
	Algorithm            string `json:"algorithm"`              // AES-256-GCM
	KeyWrapping          string `json:"key_wrapping"`           // RSA-OAEP-SHA256
	PublicKeyFingerprint string `json:"public_key_fingerprint"` // sha256:base64
}

// ContentsInfo describes what is included in the payload.
type ContentsInfo struct {
	System   bool     `json:"system"`
	Collector bool    `json:"collector"`
	Sensors  []string `json:"sensors"`
	Logs     bool     `json:"logs"`
}

// BuildHeader produces a BundleHeader from options and runtime. If publicKeyFingerprint
// is non-empty and encryption is enabled, Encryption is set; otherwise Encryption is nil.
// bundleID is used when non-empty (e.g. to match manifest); otherwise a new UUID is generated.
func BuildHeader(opts BundleOptions, createdAt time.Time, publicKeyFingerprint string, bundleID string) BundleHeader {
	if bundleID == "" {
		bundleID = uuid.New().String()
	}
	platform := runtime.GOOS
	if platform == "" {
		platform = "unknown"
	}
	arch := runtime.GOARCH
	if arch == "" {
		arch = "unknown"
	}

	contents := ContentsInfo{
		System:    opts.IncludeSystemInfo,
		Collector: opts.CollectorConfigPath != "" || opts.CollectorInstallRoot != "" || opts.CollectorLogDir != "",
		Sensors:   nil, // optional: could be populated from config later
		Logs:      opts.IncludeLogs,
	}

	h := BundleHeader{
		BundleID:      bundleID,
		OrgID:         opts.OrgID,
		BundleVersion: "1.0",
		CreatedAt:     createdAt.UTC().Format(time.RFC3339),
		Agent: AgentInfo{
			AgentID:      opts.AgentID,
			AgentVersion: opts.AgentVersion,
			Hostname:     opts.Hostname,
			Platform:     platform,
			Arch:         arch,
		},
		Collector: CollectorInfo{
			CollectorID:      opts.CollectorID,
			CollectorVersion: opts.CollectorVersion,
			InstallRoot:      opts.CollectorInstallRoot,
		},
		Contents: contents,
	}
	if opts.Encryption.Enabled && publicKeyFingerprint != "" {
		h.Encryption = &EncryptionInfo{
			Algorithm:            "AES-256-GCM",
			KeyWrapping:          "RSA-OAEP-SHA256",
			PublicKeyFingerprint: publicKeyFingerprint,
		}
	}
	return h
}

// MarshalHeader returns the canonical JSON bytes for the header (used as AAD).
func MarshalHeader(h BundleHeader) ([]byte, error) {
	return json.Marshal(h)
}

// ParseHeader unmarshals header JSON into a BundleHeader. Used to read org_id or other
// header fields without decrypting. Old bundles without org_id will have OrgID == "".
func ParseHeader(headerJSON []byte) (*BundleHeader, error) {
	var h BundleHeader
	if err := json.Unmarshal(headerJSON, &h); err != nil {
		return nil, err
	}
	return &h, nil
}
