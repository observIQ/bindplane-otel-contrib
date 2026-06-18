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
	"context"
	"os"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// signalFileSource polls a local file whose contents name the current level.
// It is the primary local trigger that keeps working when the management
// (OpAMP) link is down.
type signalFileSource struct {
	path     string
	interval time.Duration
	levels   LevelSet
	logger   *zap.Logger
	setVote  func(string, Level)

	wg   sync.WaitGroup
	done chan struct{}
}

func newSignalFileSource(cfg *SignalFileConfig, levels LevelSet, logger *zap.Logger, setVote func(string, Level)) *signalFileSource {
	interval := cfg.WatchInterval
	if interval <= 0 {
		interval = defaultWatchInterval
	}
	return &signalFileSource{
		path:     cfg.Path,
		interval: interval,
		levels:   levels,
		logger:   logger.Named("signal_file"),
		setVote:  setVote,
		done:     make(chan struct{}),
	}
}

func (s *signalFileSource) name() string { return "signal_file" }

func (s *signalFileSource) start(ctx context.Context) error {
	// Read once synchronously so the initial posture reflects the file.
	s.poll()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.done:
				return
			case <-ticker.C:
				s.poll()
			}
		}
	}()
	return nil
}

func (s *signalFileSource) stop() {
	close(s.done)
	s.wg.Wait()
}

func (s *signalFileSource) poll() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		// Missing/unreadable file: keep the previous vote rather than forcing a
		// posture change. Debug-level since this is expected before first write.
		s.logger.Debug("could not read signal file", zap.String("path", s.path), zap.Error(err))
		return
	}
	name := strings.TrimSpace(string(data))
	if name == "" {
		return
	}
	level, err := s.levels.Parse(name)
	if err != nil {
		s.logger.Warn("invalid posture level in signal file", zap.String("path", s.path), zap.String("contents", name), zap.Error(err))
		return
	}
	s.setVote(s.name(), level)
}
