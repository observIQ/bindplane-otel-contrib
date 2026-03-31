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

package fileintegrityreceiver

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// HashingConfig configures optional SHA-256 hashing of file contents on applicable events.
type HashingConfig struct {
	Enabled  bool          `mapstructure:"enabled"`
	Debounce time.Duration `mapstructure:"debounce"`
	MaxBytes int64         `mapstructure:"max_bytes"`
}

// Config configures the file integrity monitoring receiver.
type Config struct {
	// Paths lists files or directories to watch. Paths must exist when the receiver starts.
	Paths []string `mapstructure:"paths"`

	// Recursive watches subdirectories when the path is a directory (fsnotify is non-recursive by default).
	Recursive bool `mapstructure:"recursive"`

	// Exclude lists glob patterns (using path/filepath syntax) or plain paths; plain paths match the path itself or any path under that prefix.
	Exclude []string `mapstructure:"exclude"`

	// Hashing enables debounced SHA-256 hashing for regular files on create/write events.
	Hashing HashingConfig `mapstructure:"hashing"`
}

// Validate checks the configuration.
func (c *Config) Validate() error {
	if len(c.Paths) == 0 {
		return errors.New("paths must contain at least one path")
	}
	for _, p := range c.Paths {
		if p == "" {
			return errors.New("paths entries must be non-empty")
		}
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("paths: stat %q: %w", p, err)
		}
	}
	if c.Hashing.Enabled {
		if c.Hashing.Debounce <= 0 {
			return errors.New("hashing.debounce must be positive when hashing is enabled")
		}
		if c.Hashing.MaxBytes <= 0 {
			return errors.New("hashing.max_bytes must be positive when hashing is enabled")
		}
	}
	return nil
}
