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
	apachegen "github.com/observiq/blitz/generator/apache"
	jsongen "github.com/observiq/blitz/generator/json"
	k8sgen "github.com/observiq/blitz/generator/kubernetes"
	"go.uber.org/zap"
)

func init() { register("kubernetes-cluster", kubernetesCluster) }

// kubernetesCluster mixes container-format logs (kubernetes), an
// application web tier (apache common), and structured JSON service
// logs. The blend approximates the spread of formats a real cluster's
// node-agent log shipper would see.
func kubernetesCluster(logger *zap.Logger, consumer embed.LogConsumer, p Params) ([]embed.ProducerModule, error) {
	workers, rate := p.resolve(1, time.Second)
	k8s, err := k8sgen.New(logger, workers, rate, "", consumer) // empty format → blitz default (cri-o)
	if err != nil {
		return nil, err
	}
	apache, err := apachegen.New(logger, workers, rate, consumer)
	if err != nil {
		return nil, err
	}
	json, err := jsongen.New(logger, workers, rate, jsongen.LogTypeDefault, consumer)
	if err != nil {
		return nil, err
	}
	return []embed.ProducerModule{k8s, apache, json}, nil
}
