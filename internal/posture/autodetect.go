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

package posture

import (
	"sync"
	"time"

	"go.uber.org/zap"
)

// exportHealthDetector steps the posture level down after consecutive export
// failures and back up after consecutive successes. Hysteresis (separate
// thresholds) plus a minimum dwell time prevent flapping on a marginal link.
//
// Unlike the other sources it has no background goroutine: it reacts to
// RecordExportResult calls from the drain/export path.
type exportHealthDetector struct {
	levels            LevelSet
	floor             Level
	ceiling           Level
	failureThreshold  int
	recoveryThreshold int
	minDwell          time.Duration
	logger            *zap.Logger
	setVote           func(string, Level)
	now               func() time.Time // injectable for tests

	mu             sync.Mutex
	vote           Level
	consecFailures int
	consecSuccess  int
	lastChange     time.Time
}

func newExportHealthDetector(cfg *AutoDetectConfig, levels LevelSet, def Level, logger *zap.Logger, setVote func(string, Level)) *exportHealthDetector {
	floor := levels.Min()
	if cfg.Floor != "" {
		// Validated in Config.Validate; ignore error and fall back to Min on parse failure.
		if l, err := levels.Parse(cfg.Floor); err == nil {
			floor = l
		}
	}
	failureThreshold := cfg.FailureThreshold
	if failureThreshold <= 0 {
		failureThreshold = defaultFailureThreshold
	}
	recoveryThreshold := cfg.RecoveryThreshold
	if recoveryThreshold <= 0 {
		recoveryThreshold = defaultRecoveryThreshold
	}
	minDwell := cfg.MinDwell
	if minDwell <= 0 {
		minDwell = defaultMinDwell
	}
	return &exportHealthDetector{
		levels:            levels,
		floor:             floor,
		ceiling:           def,
		failureThreshold:  failureThreshold,
		recoveryThreshold: recoveryThreshold,
		minDwell:          minDwell,
		logger:            logger.Named("auto_detect"),
		setVote:           setVote,
		now:               time.Now,
		vote:              def,
	}
}

func (d *exportHealthDetector) record(success bool) {
	d.mu.Lock()
	if success {
		d.consecSuccess++
		d.consecFailures = 0
	} else {
		d.consecFailures++
		d.consecSuccess = 0
	}

	var newVote Level
	var change bool
	switch {
	case !success && d.consecFailures >= d.failureThreshold && d.vote > d.floor:
		newVote, change = d.vote-1, true
	case success && d.consecSuccess >= d.recoveryThreshold && d.vote < d.ceiling:
		newVote, change = d.vote+1, true
	}

	if change {
		if now := d.now(); now.Sub(d.lastChange) < d.minDwell && !d.lastChange.IsZero() {
			change = false
		} else {
			d.vote = newVote
			d.lastChange = now
			d.consecFailures = 0
			d.consecSuccess = 0
		}
	}
	d.mu.Unlock()

	if change {
		d.logger.Info("auto-detect stepped posture", zap.Bool("export_success", success), zap.String("level", d.levels.Name(newVote)))
		d.setVote("auto_detect", newVote)
	}
}
