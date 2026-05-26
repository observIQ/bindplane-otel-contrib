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

package recipes

import (
	"time"

	"github.com/observiq/blitz/embed"
	jsongen "github.com/observiq/blitz/generator/json"
	"go.uber.org/zap"
)

func init() { register("pii-stress", piiStress) }

// piiStress runs the JSON generator's PII log mode at a higher default
// rate than the other recipes. The default of 100ms-per-worker produces
// ~10 records/sec/worker — enough to exercise downstream PII-redaction
// pipelines without saturating a developer machine. Users wanting a
// truly stressful rate pass workers + rate explicitly.
func piiStress(logger *zap.Logger, consumer embed.LogConsumer, p Params) ([]embed.ProducerModule, error) {
	workers, rate := p.resolve(2, 100*time.Millisecond)
	gen, err := jsongen.New(logger, workers, rate, jsongen.LogTypePII, consumer)
	if err != nil {
		return nil, err
	}
	return []embed.ProducerModule{gen}, nil
}
