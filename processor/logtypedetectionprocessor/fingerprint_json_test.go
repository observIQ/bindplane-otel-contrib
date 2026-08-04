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

package logtypedetectionprocessor

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFingerprintJSONInvalid(t *testing.T) {
	cases := []string{
		"{",
		"{}}",
		`{"a":1`,
		`{"a":{"b":1}`,
		`{"a":{"b":1]}`,
		`{"a":"unterminated}`,
		`{"a":["unterminated]}`,
		`{"a":[1,2}`,
		`[{"a":1}`,
		`{"a":1]`,
		`{a:1]`,
		strings.Repeat(`{"a":`, maxDepth+1) + "1" + strings.Repeat("}", maxDepth+1),
	}

	for _, c := range cases {
		require.Zero(t, fingerprintJSON(c), "expected no fingerprint for %q", c)
	}
}

func TestFingerprintValidJSON(t *testing.T) {
	cases := []string{
		"{}",
		"[]",
		`{"a":1}`,
		`{"a":"1"}`,
		`{"a":[1,2,3]}`,
		`{"a":{"b":[{"c":1}]}}`,
		`[{"a":1},{"a":2}]`,
		`{"a":"esc\"aped"}`,
		"\n" + `{"a":1}\n` + "\n",
	}

	for _, c := range cases {
		require.NotZero(t, fingerprintJSON(c), "expected a fingerprint for %q", c)
	}
}
