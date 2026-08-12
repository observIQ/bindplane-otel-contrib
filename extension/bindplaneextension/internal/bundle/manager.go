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

import "context"

// Manager handles incoming OpAMP custom messages that request a support bundle.
//
//go:generate mockery --name Manager --filename mock_manager.go --structname MockManager --output mocks
type Manager interface {
	// HandleRequest processes a single server-initiated bundle request.
	// capability and msgType must match Capability and RequestType.
	// data is the raw CustomMessage.Data bytes (YAML-encoded RequestPayload).
	// uploadURL is where the finished bundle (or error notification) should be POSTed.
	HandleRequest(ctx context.Context, capability string, msgType string, data []byte, uploadURL string)
}
