// Copyright observIQ, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package posture provides a shared connectivity/EMCON posture provider used by
// the posture extension and the posture processor. A posture is an ordered
// "level" (e.g. silent < low < medium < full); higher levels permit more
// telemetry to egress. The provider combines one or more posture sources
// (a local signal file, a local HTTP control endpoint, and export-health
// auto-detection) using a most-restrictive-wins policy so that the most
// cautious source always dominates.
package posture

import (
	"errors"
	"fmt"
	"time"
)

// DefaultLevels is the ordered set of posture levels used when none are configured.
var DefaultLevels = []string{"silent", "low", "medium", "full"}

const (
	defaultWatchInterval     = 1 * time.Second
	defaultFailureThreshold  = 3
	defaultRecoveryThreshold = 5
	defaultMinDwell          = 30 * time.Second
)

// Config configures a posture Provider.
type Config struct {
	// Levels is the ordered list of posture level names, lowest (most
	// restrictive) first. Defaults to DefaultLevels.
	Levels []string `mapstructure:"levels"`
	// Default is the level used before any source has reported, and the fallback
	// when no sources are enabled. Defaults to the lowest level.
	Default string `mapstructure:"default"`
	// SignalFile enables a local file whose contents name the current level.
	SignalFile *SignalFileConfig `mapstructure:"signal_file,omitempty"`
	// ControlServer enables a local HTTP endpoint to read and set the level.
	ControlServer *ControlServerConfig `mapstructure:"control_server,omitempty"`
	// AutoDetect enables stepping the level based on export success/failure.
	AutoDetect *AutoDetectConfig `mapstructure:"auto_detect,omitempty"`
}

// SignalFileConfig configures the local signal-file source.
type SignalFileConfig struct {
	// Path is the file polled for the current level name.
	Path string `mapstructure:"path"`
	// WatchInterval is how often the file is polled. Defaults to 1s.
	WatchInterval time.Duration `mapstructure:"watch_interval"`
}

// ControlServerConfig configures the local HTTP control source.
type ControlServerConfig struct {
	// Endpoint is the host:port the control server binds to. Should be a local
	// address (e.g. 127.0.0.1:12345).
	Endpoint string `mapstructure:"endpoint"`
}

// AutoDetectConfig configures the export-health source.
type AutoDetectConfig struct {
	// FailureThreshold is the number of consecutive export failures that step the
	// level down by one. Defaults to 3.
	FailureThreshold int `mapstructure:"failure_threshold"`
	// RecoveryThreshold is the number of consecutive export successes that step
	// the level up by one. Defaults to 5.
	RecoveryThreshold int `mapstructure:"recovery_threshold"`
	// MinDwell is the minimum time between auto-detect level changes, to prevent
	// flapping. Defaults to 30s.
	MinDwell time.Duration `mapstructure:"min_dwell"`
	// Floor is the lowest level auto-detect will drop to. Defaults to the lowest
	// configured level.
	Floor string `mapstructure:"floor"`
}

// Validate validates the posture configuration.
func (c Config) Validate() error {
	ls, err := NewLevelSet(c.levelsOrDefault())
	if err != nil {
		return err
	}

	if c.Default != "" {
		if _, err := ls.Parse(c.Default); err != nil {
			return fmt.Errorf("default: %w", err)
		}
	}

	if c.SignalFile != nil && c.SignalFile.Path == "" {
		return errors.New("signal_file.path is required when signal_file is set")
	}

	if ac := c.AutoDetect; ac != nil {
		if ac.FailureThreshold < 0 || ac.RecoveryThreshold < 0 {
			return errors.New("auto_detect thresholds must not be negative")
		}
		if ac.Floor != "" {
			if _, err := ls.Parse(ac.Floor); err != nil {
				return fmt.Errorf("auto_detect.floor: %w", err)
			}
		}
	}

	return nil
}

func (c Config) levelsOrDefault() []string {
	if len(c.Levels) == 0 {
		return DefaultLevels
	}
	return c.Levels
}
