// Copyright observIQ, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package azureblobpollingreceiver //import "github.com/observiq/bindplane-otel-contrib/receiver/azureblobpollingreceiver"

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	"go.opentelemetry.io/collector/component"
)

// BlobFormat represents the format of blob contents
type BlobFormat string

const (
	// BlobFormatOTLP indicates blobs contain OTLP-formatted JSON
	BlobFormatOTLP BlobFormat = "otlp"

	// BlobFormatJSON indicates blobs contain newline-delimited JSON (NDJSON)
	BlobFormatJSON BlobFormat = "json"

	// BlobFormatText indicates blobs contain raw text
	BlobFormatText BlobFormat = "text"

	// BlobFormatRecordsJSON indicates blobs contain a single JSON document with
	// a top-level "records" array (e.g. Azure NSG flow logs and Azure diagnostic
	// settings exports). Each element of the array becomes one log record.
	BlobFormatRecordsJSON BlobFormat = "records-json"
)

// Config is the configuration for the azure blob polling receiver
type Config struct {
	// BatchSize is the number of blobs to process entering the pipeline in a single batch. (default 30)
	// This number directly affects the number of goroutines that will be created to process the blobs.
	BatchSize int `mapstructure:"batch_size"`

	// ConnectionString is the Azure Blob Storage connection key,
	// which can be found in the Azure Blob Storage resource on the Azure Portal. (no default)
	ConnectionString string `mapstructure:"connection_string"`

	// Container is the name of the storage container to pull from. (no default)
	Container string `mapstructure:"container"`

	// RootFolder is the name of the root folder in path.
	RootFolder string `mapstructure:"root_folder"`

	// FallbackOnGlobFailure controls behavior when root_folder is a glob and
	// the Azure ListPrefixes call used to expand it fails. When false (default),
	// the poll lists nothing for that cycle and an error is logged. When true,
	// the receiver falls back to listing under the static portion of the glob,
	// which on large containers (e.g. NSG flow logs) can produce a full-container
	// scan and is only safe when the static prefix is known to be narrow.
	FallbackOnGlobFailure bool `mapstructure:"fallback_on_glob_failure"`

	// PollInterval is the interval at which to poll for new blobs. (no default, required)
	// The receiver will continuously poll at this interval and dynamically adjust the time window
	// to collect only new data from each interval.
	PollInterval time.Duration `mapstructure:"poll_interval"`

	// InitialLookback is the duration to look back on the first poll when no checkpoint exists. (default: same as poll_interval)
	// For example, if set to 1h, on first startup the receiver will look for blobs from the last hour.
	InitialLookback time.Duration `mapstructure:"initial_lookback"`

	// DeleteOnRead indicates if a file should be deleted once it has been processed
	// Default value of false
	DeleteOnRead bool `mapstructure:"delete_on_read"`

	// PageSize is the number of blobs to request from the Azure API at a time. (default 1000)
	PageSize int `mapstructure:"page_size"`

	// ID of the storage extension to use for storing progress
	StorageID *component.ID `mapstructure:"storage"`

	// UseLastModified when true, uses the blob's LastModified timestamp instead of parsing the folder structure
	// This allows collecting blobs that don't follow the year=/month=/day=/hour= naming convention
	// Default is false to maintain backward compatibility
	UseLastModified bool `mapstructure:"use_last_modified"`

	// TimePattern specifies a custom pattern for extracting timestamps from blob paths
	// Supports both named placeholders ({year}/{month}/{day}/{hour}/{minute}) and Go time format (2006/01/02/15/04)
	// Examples:
	//   "{year}/{month}/{day}/{hour}" matches "2025/12/05/14/app.log"
	//   "logs/{year}-{month}-{day}" matches "logs/2025-12-05/data.json"
	//   "2006/01/02/15/04" matches "2025/12/05/14/28/logs.json"
	// If not specified, uses the default year=YYYY/month=MM/... format
	TimePattern string `mapstructure:"time_pattern"`

	// UseTimePatternAsPrefix tells the receiver to use the time_pattern to generate
	// prefixes for the Azure API calls. This is an optimization to reduce the number of
	// blobs scanned. It limits the prefix generation to the hour.
	UseTimePatternAsPrefix bool `mapstructure:"use_time_pattern_as_prefix"`

	// TelemetryType explicitly sets the telemetry type ("logs", "metrics", or "traces")
	// Required when using time_pattern, as the receiver can't infer type from the path
	// If not set, falls back to the pipeline type the receiver is configured in
	TelemetryType string `mapstructure:"telemetry_type"`

	// FilenamePattern is a regex pattern to filter blobs by filename
	// Only blobs whose names match this pattern will be processed
	// Examples:
	//   "firewall\\d+_\\w+\\.json" matches "firewall43_dfreds.json"
	//   ".*\\.json" matches any file ending with .json
	//   "app-.*\\.log" matches "app-server.log", "app-client.log"
	// If not specified, all blobs matching the time pattern will be processed
	FilenamePattern string `mapstructure:"filename_pattern"`

	// BlobFormat specifies the format of blob contents.
	// Supported values: "otlp" (default), "json" (NDJSON), "text" (raw text),
	// "records-json" (single JSON document with a top-level "records" array,
	// e.g. Azure NSG flow logs).
	// Non-"otlp" formats are only supported for logs pipelines.
	BlobFormat BlobFormat `mapstructure:"blob_format"`

	// EnableIncrementalRead opts a blob into incremental byte-offset reads for the
	// "records-json" and "json" formats: the receiver tracks a per-blob offset and consumes
	// only newly appended complete records/lines, revisiting the blob until it seals. When
	// false (default) those formats parse the whole blob once per poll and dedupe by name,
	// losing mid-hour appends to a growing blob. See the README for details.
	//
	// TEMPORARY: this will become the default and be removed once the incremental
	// path is proven, at which point the legacy whole-blob behavior goes away.
	EnableIncrementalRead bool `mapstructure:"enable_incremental_read"`

	// IncrementalRevisitWindow bounds two things for the incremental path: how far back each
	// poll re-lists blobs to pick up in-place appends, and how long after a blob's last
	// observed change it is held before being treated as sealed. Default 2h; 0 selects it.
	// Only consulted when EnableIncrementalRead is set.
	IncrementalRevisitWindow time.Duration `mapstructure:"incremental_revisit_window"`

	// FingerprintSize is the number of leading blob bytes used to identify an append-growable
	// blob across reads, mirroring the filelog receiver's fingerprint_size. Only consulted
	// when EnableIncrementalRead is set; 0 selects the default (512), any explicit value >= 16.
	FingerprintSize int `mapstructure:"fingerprint_size"`

	// AssumeAppendOnly skips the per-poll blob-identity read on the incremental path (halving
	// the per-poll read operations for a growing blob). Only set this when blobs are guaranteed
	// append-only: if a blob is ever replaced or truncated under the same name, the receiver
	// emits stale bytes as records (replaced larger) or silently misses new content (replaced
	// smaller). Only consulted when EnableIncrementalRead is set.
	AssumeAppendOnly bool `mapstructure:"assume_append_only"`
}

// Validate validates the config
func (c *Config) Validate() error {
	if c.BatchSize < 1 {
		return errors.New("batch_size must be greater than 0")
	}

	if c.ConnectionString == "" {
		return errors.New("connection_string is required")
	}

	if c.Container == "" {
		return errors.New("container is required")
	}

	if c.PollInterval <= 0 {
		return errors.New("poll_interval must be greater than 0")
	}

	if c.PollInterval < time.Minute {
		return errors.New("poll_interval must be at least 1 minute")
	}

	if c.InitialLookback < 0 {
		return errors.New("initial_lookback must be greater than or equal to 0")
	}

	if c.IncrementalRevisitWindow < 0 {
		return errors.New("incremental_revisit_window must be greater than or equal to 0")
	}

	// incremental_revisit_window only bounds the incremental path; without the opt-in it
	// is a silent no-op, so reject it rather than let it look active.
	if c.IncrementalRevisitWindow != 0 && !c.EnableIncrementalRead {
		return errors.New("incremental_revisit_window has no effect without enable_incremental_read")
	}

	// The revisit window must cover at least one poll, or a still-growing blob can age out
	// between polls and be re-read from byte 0 (duplication) or deleted mid-write (loss).
	if c.EnableIncrementalRead {
		window := c.IncrementalRevisitWindow
		if window == 0 {
			window = defaultIncrementalRevisitWindow
		}
		if window < c.PollInterval {
			return fmt.Errorf("incremental_revisit_window (%s) must be at least poll_interval (%s)", window, c.PollInterval)
		}
	}

	if c.PageSize < 1 {
		return errors.New("page_size must be greater than 0")
	}

	// Validate use_last_modified and time_pattern are not both set
	if c.UseLastModified && c.TimePattern != "" && !c.UseTimePatternAsPrefix {
		return errors.New("use_last_modified and time_pattern cannot both be set")
	}

	if c.UseTimePatternAsPrefix && c.TimePattern == "" {
		return errors.New("time_pattern must be set when use_time_pattern_as_prefix is true")
	}

	// Validate telemetry_type if set
	if c.TelemetryType != "" {
		switch c.TelemetryType {
		case "logs", "metrics", "traces":
			// valid
		default:
			return errors.New("telemetry_type must be one of: logs, metrics, traces")
		}
	}

	// Validate root_folder glob pattern if it contains glob characters
	if c.RootFolder != "" && strings.ContainsAny(c.RootFolder, "*?[") {
		if _, err := path.Match(c.RootFolder, ""); err != nil {
			return errors.New("root_folder contains an invalid glob pattern: " + err.Error())
		}
	}

	// Validate filename_pattern is a valid regex if set
	if c.FilenamePattern != "" {
		_, err := regexp.Compile(c.FilenamePattern)
		if err != nil {
			return errors.New("filename_pattern must be a valid regex: " + err.Error())
		}
	}

	// Validate blob_format if set
	if c.BlobFormat != "" {
		switch c.BlobFormat {
		case BlobFormatOTLP, BlobFormatJSON, BlobFormatText, BlobFormatRecordsJSON:
			// valid
		default:
			return fmt.Errorf("blob_format must be one of: %s, %s, %s, %s", BlobFormatOTLP, BlobFormatJSON, BlobFormatText, BlobFormatRecordsJSON)
		}

		// non-otlp formats are only supported for logs pipelines
		if c.BlobFormat != BlobFormatOTLP &&
			c.TelemetryType != "" && c.TelemetryType != "logs" {
			return fmt.Errorf("blob_format %q is only supported for logs pipelines, got telemetry_type %q", c.BlobFormat, c.TelemetryType)
		}
	}

	// These only affect the incremental (append-growable logs) path. Set without their
	// prerequisite they'd be silent no-ops, so reject each rather than let it look active.
	if c.EnableIncrementalRead && (c.BlobFormat == BlobFormatOTLP || c.BlobFormat == "") {
		return errors.New("enable_incremental_read has no effect with blob_format otlp; use json or records-json")
	}
	if c.EnableIncrementalRead && c.BlobFormat == BlobFormatText {
		return errors.New("enable_incremental_read has no effect with blob_format text")
	}
	if c.FingerprintSize != 0 && !c.EnableIncrementalRead {
		return errors.New("fingerprint_size has no effect without enable_incremental_read")
	}
	if c.FingerprintSize != 0 && c.FingerprintSize < minFingerprintSize {
		return fmt.Errorf("fingerprint_size must be at least %d, or 0 for the default", minFingerprintSize)
	}
	if c.AssumeAppendOnly && !c.EnableIncrementalRead {
		return errors.New("assume_append_only has no effect without enable_incremental_read")
	}

	return nil
}
