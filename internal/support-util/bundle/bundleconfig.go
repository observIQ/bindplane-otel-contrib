package bundle

import (
	"runtime"
	"time"
)

// CollectorOptions holds paths and identity for the local Otel Collector installation.
type CollectorOptions struct {
	InstallRoot       string // When set, root configs, log/, plugins/, version.txt are gathered from here.
	LogDir            string
	ConfigPath        string
	ManagerConfigPath string
	ProfileDir        string
	ProfileMaxAge     int    // Maximum age in hours for profiles
	ID                string // optional, for header
	Version           string // optional, for header
}

// PrometheusOptions configures Prometheus metrics scraping from the Otel Collector.
type PrometheusOptions struct {
	Enabled                bool
	MetricsEndpoint        string        // base URL, e.g. http://localhost:8888
	ScrapeTimeout          time.Duration // HTTP timeout per scrape (e.g. 10s)
	ScrapeInterval         time.Duration // if > 0, multiple scrapes at this interval
	ScrapeDuration         time.Duration // if > 0 with interval, run for this long (e.g. 5m); scrapes = duration/interval, capped
	RetryBackoffInitial    time.Duration // initial delay before first retry (e.g. 1s)
	RetryBackoffMultiplier float64       // exponential backoff multiplier (e.g. 2.0)
	MaxRetryDuration       time.Duration // stop retrying after this long from first attempt (e.g. 60s)
	// Optional up-check before scrape; when zero, package defaults are used (3 attempts, 5s timeout, 2s delay).
	UpCheckMaxAttempts int           // if > 0, max attempts for isUp check
	UpCheckTimeout     time.Duration // if > 0, timeout per isUp attempt
	UpCheckDelay       time.Duration // if > 0, delay between isUp attempts
}

// BundleOptions contains configuration for bundle creation
type BundleOptions struct {
	IncludeConfig       bool
	IncludeLogs         bool
	IncludeNetworkState bool
	IncludeSystemInfo   bool
	IncludeProfiles     bool
	AgentID             string
	OrgID               string // optional, for header; used by backend to select decryption key
	AgentVersion        string // optional, for header
	Hostname            string // optional; if empty, os.Hostname() used when building header
	CollectAllLogs      bool
	IncludeRotatedLogs  bool
	LogDir              string
	ConfigPath          string
	OutputDir           string

	Collector  CollectorOptions
	Prometheus PrometheusOptions
	Encryption EncryptionOptions
}

// BuildFilename generates a filename for the bundle based on current timestamp.
// For encrypted bundles use BuildEncryptedFilename() to get a .bundle path.
func (o BundleOptions) BuildFilename() string {
	timestamp := time.Now().Format("20060102_150405")
	extension := ".tar.gz"
	if runtime.GOOS == "windows" {
		extension = ".zip"
	}
	return "support_bundle_" + timestamp + extension
}

// BuildEncryptedFilename returns the filename for an encrypted single-file .bundle (e.g. support_bundle_20060102_150405.bundle).
func (o BundleOptions) BuildEncryptedFilename() string {
	timestamp := time.Now().Format("20060102_150405")
	return "support_bundle_" + timestamp + ".bundle"
}

// DefaultBundleOptions returns default bundle options
func DefaultBundleOptions() BundleOptions {
	return BundleOptions{
		IncludeConfig:       true,
		IncludeLogs:         true,
		IncludeNetworkState: true,
		IncludeSystemInfo:   true,
		CollectAllLogs:      false,
		OutputDir:           "./bundles",
	}
}
