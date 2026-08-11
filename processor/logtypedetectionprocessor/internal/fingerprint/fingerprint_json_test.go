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
		strings.Repeat(`{"a":`, maxLogDepth+1) + "1" + strings.Repeat("}", maxLogDepth+1),
	}

	for _, c := range cases {
		require.Zero(t, fingerprintJSON(c), "expected no fingerprint for %q", c)
	}
}

func TestFingerprintJSONStructure(t *testing.T) {
	cases := []struct {
		title        string
		jsonA, jsonB string
		shouldEqual  bool
	}{
		{
			title:       "same structure",
			jsonA:       `{"a":1,"b":"x","c":true}`,
			jsonB:       `{"a":9999,"b":"totally different","c":false}`,
			shouldEqual: true,
		},
		{
			title:       "nested same structure",
			jsonA:       `{"a":{"b":{"c":"one"}}}`,
			jsonB:       `{"a":{"b":{"c":"two"}}}`,
			shouldEqual: true,
		},
		{
			title:       "different array values",
			jsonA:       `{"a":[1,2,3]}`,
			jsonB:       `{"a":[9]}`,
			shouldEqual: true,
		},
		{
			title:       "whitespace",
			jsonA:       `{"a":1}`,
			jsonB:       "{\n  \"a\" : 1\n}",
			shouldEqual: true,
		},
		{
			title:       "escaped characters",
			jsonA:       `{"a":"plain"}`,
			jsonB:       `{"a":"esc\"aped"}`,
			shouldEqual: true,
		},
		{
			title:       "different key",
			jsonA:       `{"a":1}`,
			jsonB:       `{"b":1}`,
			shouldEqual: false,
		},
		{
			title:       "extra key",
			jsonA:       `{"a":1}`,
			jsonB:       `{"a":1,"b":2}`,
			shouldEqual: false,
		},
		{
			title:       "nested object",
			jsonA:       `{"a":{"b":1}}`,
			jsonB:       `{"a":1}`,
			shouldEqual: false,
		},
		{
			title:       "different type",
			jsonA:       `{"a":[1]}`,
			jsonB:       `{"a":1}`,
			shouldEqual: false,
		},
		{
			title:       "different order",
			jsonA:       `{"a":1,"b":2}`,
			jsonB:       `{"b":2,"a":1}`,
			shouldEqual: false,
		},
	}

	for _, c := range cases {
		t.Run(c.title, func(t *testing.T) {
			fingerprintA := fingerprintJSON(c.jsonA)
			fingerprintB := fingerprintJSON(c.jsonB)
			require.NotZero(t, fingerprintA)
			if c.shouldEqual {
				require.Equal(t, fingerprintA, fingerprintB)
			} else {
				require.NotEqual(t, fingerprintA, fingerprintB)
			}
		})
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
