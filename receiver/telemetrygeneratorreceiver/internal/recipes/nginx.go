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
	nginxgen "github.com/observiq/blitz/generator/nginx"
	"go.uber.org/zap"
)

func init() { register("nginx", nginx) }

func nginx(logger *zap.Logger, consumer embed.LogConsumer, p Params) ([]embed.ProducerModule, error) {
	workers, rate := p.resolve(1, time.Second)
	gen, err := nginxgen.New(logger, workers, rate, consumer)
	if err != nil {
		return nil, err
	}
	return []embed.ProducerModule{gen}, nil
}
