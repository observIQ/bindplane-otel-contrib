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

// Bundler collects support bundles and delivers them to the server.
//
//go:generate mockery --name Bundler --filename mock_bundler.go --structname MockBundler --output mocks
type Bundler interface {
	// Collect gathers all configured artifacts and returns the encrypted bundle bytes.
	Collect(ctx context.Context) ([]byte, error)

	// SendToURL POSTs the bundle bytes to uploadURL.
	// sessionID is forwarded as the X-Bindplane-Session-ID header.
	SendToURL(ctx context.Context, data []byte, sessionID string, uploadURL string) error

	// SendErrorToURL notifies the server that bundle collection failed.
	// The error message is posted as plain text with X-Bindplane-Support-Bundle-Status: failed.
	SendErrorToURL(ctx context.Context, sessionID string, errMessage string, uploadURL string) error
}
