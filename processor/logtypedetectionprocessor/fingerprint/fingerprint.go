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

package fingerprint

import "strings"

// maxLogDepth bounds object nesting when fingerprinting logs.
const maxLogDepth = 64

var spaceChars [256]bool

const (
	textPlaceholder  = `<txt>`
	valuePlaceholder = `<val>`
	arrayPlaceholder = `<arr>`
)

func init() {
	for _, c := range []byte(" \t\n\r") {
		spaceChars[c] = true
	}
}

// FingerprintLog creates a hash based off the structure of the log.
func FingerprintLog(data string) uint64 {
	data = strings.TrimSpace(data)
	if len(data) < 2 {
		return 0
	}

	first, last := data[0], data[len(data)-1]
	if (first == '{' && last == '}') || (first == '[' && last == ']') {
		if res := fingerprintJSON(data); res != 0 {
			return res
		}
	}

	if first == '<' && last == '>' {
		if res := fingerprintXML(data); res != 0 {
			return res
		}
	}

	return 0
}

const (
	fnvOffsetBasis = 14695981039346656037
	fnvPrime       = 1099511628211
)

// foldFNVHashString mixes additionalData into the running FNV-1a digest, hash.
// https://en.wikipedia.org/wiki/Fowler%E2%80%93Noll%E2%80%93Vo_hash_function
func foldFNVHashString(hash uint64, additionalData string) uint64 {
	for i := range len(additionalData) {
		hash = (hash ^ uint64(additionalData[i])) * fnvPrime
	}

	return hash
}

func foldFNVHashByte(hash uint64, b byte) uint64 {
	hash = (hash ^ uint64(b)) * fnvPrime
	return hash
}

func foldFNVHashUint(hash uint64, i uint64) uint64 {
	hash = (hash ^ i) * fnvPrime
	return hash
}
