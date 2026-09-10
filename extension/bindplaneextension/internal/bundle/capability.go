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

// Capability is the OpAMP custom-message capability string advertised to the server.
// The server uses this to know the agent can produce support bundles.
const Capability = "com.bindplane.supportbundle"

// RequestType is the OpAMP custom-message type sent by the server to request a bundle.
const RequestType = "requestSupportBundle"
