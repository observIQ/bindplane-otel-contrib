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

package bytebatcherprocessor

import (
	"errors"
	"time"
)

type Config struct {
	FlushInterval time.Duration `mapstructure:"flush_interval"`
	Bytes         int           `mapstructure:"bytes"`
}

func (c *Config) Validate() error {
	if c.FlushInterval <= 0 {
		return errors.New("flush_interval must be greater than 0")
	}
	if c.Bytes <= 0 {
		return errors.New("bytes must be greater than 0")
	}
	return nil
}
