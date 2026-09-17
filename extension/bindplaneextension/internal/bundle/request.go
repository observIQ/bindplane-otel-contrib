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

package bundle

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// RequestPayload is the YAML body the server embeds in a CustomMessage Data field
// when requesting a support bundle.
type RequestPayload struct {
	SessionID string `yaml:"session_id"`
}

// ParseRequestPayload decodes the YAML bytes from a CustomMessage and returns the payload.
// An empty data slice returns a zero-value payload without error.
func ParseRequestPayload(data []byte) (RequestPayload, error) {
	if len(data) == 0 {
		return RequestPayload{}, nil
	}
	var p RequestPayload
	if err := yaml.Unmarshal(data, &p); err != nil {
		return RequestPayload{}, fmt.Errorf("parse support bundle request payload: %w", err)
	}
	p.SessionID = strings.TrimSpace(p.SessionID)
	return p, nil
}
