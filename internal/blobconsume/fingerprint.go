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

package blobconsume //import "github.com/observiq/bindplane-otel-contrib/internal/blobconsume"

import "bytes"

// DefaultFingerprintSize is the number of leading bytes used to identify a blob when
// the receiver does not override it. It is large enough to cover a distinguishing
// prefix (past the shared {"records":[ envelope of flow logs) while keeping the
// identity read small; the receiver exposes fingerprint_size to tune it.
const DefaultFingerprintSize = 512

// Fingerprint returns a copy of the first size bytes of data (or all of data when
// shorter). It is a stable identity for a blob that only grows: as the blob gains
// bytes the fingerprint lengthens until it reaches size and then never changes.
// A non-positive size falls back to DefaultFingerprintSize so the result is always
// bounded (never the whole blob).
func Fingerprint(data []byte, size int) []byte {
	if size <= 0 {
		size = DefaultFingerprintSize
	}
	if len(data) > size {
		data = data[:size]
	}
	return append([]byte(nil), data...)
}

// SameBlob reports whether current identifies the same blob as stored: stored must be
// a non-empty prefix of current. Directional like filelog's Fingerprint.StartsWith, so
// a shrunk blob (current shorter than stored) counts as replaced, not grown.
func SameBlob(stored, current []byte) bool {
	if len(stored) == 0 || len(current) == 0 {
		return false
	}
	if len(stored) > len(current) {
		return false
	}
	return bytes.HasPrefix(current, stored)
}
