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

package fileintegrityreceiver

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

// hashFileSHA256 reads up to maxBytes from path and returns lowercase hex SHA-256.
// If the file is not regular, skipped is true with reason. If stat size exceeds maxBytes, skips hashing.
func hashFileSHA256(path string, maxBytes int64) (digestHex string, skipped bool, reason string, err error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", false, "", err
	}
	if !fi.Mode().IsRegular() {
		return "", true, "not a regular file", nil
	}
	if fi.Size() > maxBytes {
		return "", true, "file exceeds max_bytes", nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", false, "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, maxBytes)); err != nil {
		return "", false, "", err
	}
	return hex.EncodeToString(h.Sum(nil)), false, "", nil
}
