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

package worker

import (
	"context"
	"time"

	"github.com/cenkalti/backoff/v4"
	"go.opentelemetry.io/collector/config/configretry"
	"go.opentelemetry.io/collector/consumer/consumererror"
	"go.uber.org/zap"
)

// newExponentialBackOff builds a cenkalti ExponentialBackOff from cfg. The caller is
// responsible for checking cfg.Enabled.
func newExponentialBackOff(cfg configretry.BackOffConfig) *backoff.ExponentialBackOff {
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = cfg.InitialInterval
	bo.RandomizationFactor = cfg.RandomizationFactor
	bo.Multiplier = cfg.Multiplier
	bo.MaxInterval = cfg.MaxInterval
	bo.MaxElapsedTime = cfg.MaxElapsedTime
	bo.Reset()
	return bo
}

// consumeWithRetry calls consume, retrying a transient failure with exponential backoff
// until it succeeds, returns a permanent error, exhausts the backoff, or ctx is cancelled.
// When cfg.Enabled is false it calls consume exactly once, preserving the prior behavior
// where redelivery is left to the broker's visibility timeout / ack deadline.
func consumeWithRetry(ctx context.Context, cfg configretry.BackOffConfig, logger *zap.Logger, consume func() error) error {
	err := consume()
	if err == nil || !cfg.Enabled || consumererror.IsPermanent(err) {
		return err
	}

	bo := newExponentialBackOff(cfg)
	for {
		delay := bo.NextBackOff()
		if delay == backoff.Stop {
			return err
		}
		logger.Debug("downstream consume failed; backing off before retry",
			zap.Duration("delay", delay), zap.Error(err))
		select {
		case <-ctx.Done():
			return err
		case <-time.After(delay):
		}
		if err = consume(); err == nil || consumererror.IsPermanent(err) {
			return err
		}
	}
}
