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

package blobconsume

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// recordCount unmarshals a reframed {"records":[...]} document and returns the
// number of records, failing the test if the document is not valid JSON.
func recordCount(t *testing.T, reframed []byte) int {
	t.Helper()
	if reframed == nil {
		return 0
	}
	var env struct {
		Records []json.RawMessage `json:"records"`
	}
	require.NoError(t, json.Unmarshal(reframed, &env), "reframed content must be valid JSON: %s", reframed)
	return len(env.Records)
}

func TestSplitRecordsJSONDelta(t *testing.T) {
	t.Run("at start, closing present", func(t *testing.T) {
		delta := []byte(`{"records":[{"a":1},{"b":2}]}`)
		reframed, consumed := SplitRecordsJSONDelta(delta, true)
		require.Equal(t, 2, recordCount(t, reframed))
		require.Equal(t, len(`{"records":[{"a":1},{"b":2}`), consumed, "stops after the last record; the closing ]} is left unconsumed")
	})

	t.Run("at start, unsealed (missing closing)", func(t *testing.T) {
		// Azure has written two records but not the closing "]}" yet.
		delta := []byte(`{"records":[{"a":1},{"b":2}`)
		reframed, consumed := SplitRecordsJSONDelta(delta, true)
		require.Equal(t, 2, recordCount(t, reframed))
		// consumed stops right after the last complete record's "}".
		require.Equal(t, len(`{"records":[{"a":1},{"b":2}`), consumed)
	})

	t.Run("at start, unsealed with trailing comma", func(t *testing.T) {
		// A comma has been written ahead of a not-yet-written record.
		delta := []byte(`{"records":[{"a":1},{"b":2},`)
		reframed, consumed := SplitRecordsJSONDelta(delta, true)
		require.Equal(t, 2, recordCount(t, reframed))
		require.Equal(t, len(`{"records":[{"a":1},{"b":2}`), consumed, "trailing comma is not consumed")
	})

	t.Run("at start, partial trailing record", func(t *testing.T) {
		delta := []byte(`{"records":[{"a":1},{"b":`)
		reframed, consumed := SplitRecordsJSONDelta(delta, true)
		require.Equal(t, 1, recordCount(t, reframed))
		require.Equal(t, len(`{"records":[{"a":1}`), consumed)
	})

	t.Run("mid-array delta, unsealed", func(t *testing.T) {
		// Continuation delta starting with the comma before the next record.
		delta := []byte(`,{"c":3},{"d":4}`)
		reframed, consumed := SplitRecordsJSONDelta(delta, false)
		require.Equal(t, 2, recordCount(t, reframed))
		require.Equal(t, len(`,{"c":3},{"d":4}`), consumed)
	})

	t.Run("mid-array delta, closing present", func(t *testing.T) {
		delta := []byte(`,{"c":3}]}`)
		reframed, consumed := SplitRecordsJSONDelta(delta, false)
		require.Equal(t, 1, recordCount(t, reframed))
		require.Equal(t, len(`,{"c":3}`), consumed, "the closing ]} is left unconsumed")
	})

	t.Run("mid-array delta, only closing left", func(t *testing.T) {
		// Offset already past the last record; only "]}" remains. Nothing is
		// consumable and the footer is left in place (offset stays put).
		delta := []byte(`]}`)
		reframed, consumed := SplitRecordsJSONDelta(delta, false)
		require.Nil(t, reframed)
		require.Equal(t, 0, consumed, "the trailing ]} is left unconsumed")
	})

	t.Run("no complete record yet", func(t *testing.T) {
		delta := []byte(`,{"c":`)
		reframed, consumed := SplitRecordsJSONDelta(delta, false)
		require.Nil(t, reframed)
		require.Equal(t, 0, consumed)
	})

	t.Run("at start but envelope bracket not yet written", func(t *testing.T) {
		// The very first read caught the blob before "[" was flushed.
		delta := []byte(`{"records":`)
		reframed, consumed := SplitRecordsJSONDelta(delta, true)
		require.Nil(t, reframed)
		require.Equal(t, 0, consumed)
	})

	t.Run("at start, records is not the first key", func(t *testing.T) {
		// A records-json document may carry other keys before "records"; the array
		// must be located by key rather than by the first "[" in the document.
		delta := []byte(`{"time":"t","records":[{"a":1},{"b":2}]}`)
		reframed, consumed := SplitRecordsJSONDelta(delta, true)
		require.Equal(t, 2, recordCount(t, reframed))
		require.Equal(t, len(`{"time":"t","records":[{"a":1},{"b":2}`), consumed)
	})

	t.Run("at start, an earlier array-valued key is not mistaken for records", func(t *testing.T) {
		delta := []byte(`{"tags":["x","y"],"records":[{"a":1}]}`)
		reframed, consumed := SplitRecordsJSONDelta(delta, true)
		require.Equal(t, 1, recordCount(t, reframed))
		require.Equal(t, len(`{"tags":["x","y"],"records":[{"a":1}`), consumed)
	})

	t.Run("at start, records key present but its array not yet written", func(t *testing.T) {
		delta := []byte(`{"time":"t","records":`)
		reframed, consumed := SplitRecordsJSONDelta(delta, true)
		require.Nil(t, reframed)
		require.Equal(t, 0, consumed)
	})

	t.Run("at start, not a JSON object", func(t *testing.T) {
		reframed, consumed := SplitRecordsJSONDelta([]byte(`[1,2,3]`), true)
		require.Nil(t, reframed)
		require.Equal(t, 0, consumed)
	})

	t.Run("at start, records value is not an array", func(t *testing.T) {
		reframed, consumed := SplitRecordsJSONDelta([]byte(`{"records":5}`), true)
		require.Nil(t, reframed)
		require.Equal(t, 0, consumed)
	})

	t.Run("at start, unterminated key", func(t *testing.T) {
		reframed, consumed := SplitRecordsJSONDelta([]byte(`{"untermin`), true)
		require.Nil(t, reframed)
		require.Equal(t, 0, consumed)
	})

	t.Run("at start, a preceding key's value is incomplete", func(t *testing.T) {
		reframed, consumed := SplitRecordsJSONDelta([]byte(`{"meta":{"a":`), true)
		require.Nil(t, reframed)
		require.Equal(t, 0, consumed)
	})

	t.Run("at start, complete object with no records key", func(t *testing.T) {
		reframed, consumed := SplitRecordsJSONDelta([]byte(`{"a":1,"b":2}`), true)
		require.Nil(t, reframed)
		require.Equal(t, 0, consumed)
	})
}

func TestSplitLineDelta(t *testing.T) {
	t.Run("multiple complete lines", func(t *testing.T) {
		delta := []byte("a\nb\nc\n")
		complete, consumed := SplitLineDelta(delta)
		require.Equal(t, []byte("a\nb\nc\n"), complete)
		require.Equal(t, 6, consumed)
	})

	t.Run("trailing partial line", func(t *testing.T) {
		delta := []byte("a\nb\nc")
		complete, consumed := SplitLineDelta(delta)
		require.Equal(t, []byte("a\nb\n"), complete)
		require.Equal(t, 4, consumed)
	})

	t.Run("no newline yet", func(t *testing.T) {
		delta := []byte("abc")
		complete, consumed := SplitLineDelta(delta)
		require.Nil(t, complete)
		require.Equal(t, 0, consumed)
	})
}

func TestSplitRecordsJSONDelta_NonObjectElements(t *testing.T) {
	// records-json elements must be JSON objects (the whole-blob consumer unmarshals
	// records into []map[string]any). A non-object element halts consumption rather
	// than being framed and handed to a consumer that would reject it.
	t.Run("a non-object element halts consumption after the preceding objects", func(t *testing.T) {
		reframed, consumed := SplitRecordsJSONDelta([]byte(`{"records":[{"a":1},123]}`), true)
		require.Equal(t, `{"records":[{"a":1}]}`, string(reframed), "only the object is emitted, never the scalar")
		require.Equal(t, 1, recordCount(t, reframed))
		require.Equal(t, len(`{"records":[{"a":1}`), consumed, "offset stops just after the object, before the scalar")
	})

	t.Run("a leading non-object element yields nothing", func(t *testing.T) {
		reframed, consumed := SplitRecordsJSONDelta([]byte(`{"records":[123,{"a":1}]}`), true)
		require.Nil(t, reframed)
		require.Equal(t, 0, consumed)
	})

	t.Run("scalars and arrays of every kind halt", func(t *testing.T) {
		for _, elem := range []string{`123`, `-1`, `"s"`, `true`, `null`, `[1]`} {
			reframed, consumed := SplitRecordsJSONDelta([]byte(`{"records":[`+elem), true)
			require.Nil(t, reframed, "element %q is not an object", elem)
			require.Equal(t, 0, consumed, "element %q is not an object", elem)
		}
	})
}

func TestValidRecordsJSONDoc(t *testing.T) {
	require.True(t, ValidRecordsJSONDoc([]byte(`{"records":[{"a":1}]}`)))
	require.True(t, ValidRecordsJSONDoc([]byte(`{"records":[]}`)), "empty records array is valid")
	require.True(t, ValidRecordsJSONDoc([]byte(`{"records":[{"a":1}],"resourceId":"x","count":1}`)), "envelope keys after the array are valid")
	require.True(t, ValidRecordsJSONDoc([]byte(`{"foo":1}`)), "no records key is valid (nothing to ingest)")
	require.False(t, ValidRecordsJSONDoc([]byte(`[{"a":1},{"b":2}]`)), "a top-level array is not a records-json document")
	require.False(t, ValidRecordsJSONDoc([]byte(`{"records":[123]}`)), "a non-object element is invalid")
	require.False(t, ValidRecordsJSONDoc([]byte(`{"records":[`)), "truncated input is invalid")
}

func TestIsJSONObject(t *testing.T) {
	require.True(t, isJSONObject(json.RawMessage(`{"a":1}`)))
	require.False(t, isJSONObject(json.RawMessage(`123`)))
	require.False(t, isJSONObject(json.RawMessage(`"s"`)))
	require.False(t, isJSONObject(json.RawMessage(`[1]`)))
	require.False(t, isJSONObject(json.RawMessage(``)))
}

func TestExportedIsJSONObject(t *testing.T) {
	require.True(t, IsJSONObject([]byte(`{"a":1}`)), "a JSON object")
	require.True(t, IsJSONObject([]byte("  \n{\"a\":1}\n ")), "surrounding whitespace is tolerated")
	require.False(t, IsJSONObject([]byte(`42`)), "a valid non-object scalar is not an object")
	require.False(t, IsJSONObject([]byte(`["a"]`)), "a valid array is not an object")
	require.False(t, IsJSONObject([]byte(`{"a":1`)), "an incomplete/invalid object fails validation")
	require.False(t, IsJSONObject([]byte(``)), "empty is not an object")
}

func TestIsUnparseableRecordsJSONEnvelope(t *testing.T) {
	cases := []struct {
		name    string
		delta   string
		want    bool
		comment string
	}{
		// Terminal: can never become {"records":[...]} no matter what is appended.
		{"top-level array", `[{"a":1}]`, true, "not an object at all"},
		{"top-level scalar", `42`, true, "not an object at all"},
		{"leading whitespace then array", "  \n[1]", true, "whitespace is skipped, still not an object"},
		{"records not an array", `{"records":42}`, true, "records present but not an array"},
		{"closed object, no records", `{"foo":1}`, true, "object is closed and has no records key"},
		{"empty closed object", `{}`, true, "closed object, no records key"},
		{"earlier key then records-scalar", `{"tags":["x"],"records":5}`, true, "records reached and is not an array"},

		// Not terminal: still growing, so the caller must wait rather than quarantine.
		{"empty delta", ``, false, "no bytes yet"},
		{"open object only", `{`, false, "object just started"},
		{"partial key", `{"reco`, false, "key still being written"},
		{"partial value after a key", `{"foo":{"a":`, false, "an earlier key's value is still being written"},
		{"key present, no array yet", `{"records":`, false, "the [ is not written yet"},
		{"empty records array", `{"records":[`, false, "valid envelope, no records yet"},
		{"partial first record", `{"records":[{"a":`, false, "first record still being written"},
		{"earlier key, object not closed", `{"foo":1`, false, "records may still be appended"},
		{"earlier array key, growing", `{"tags":["x"]`, false, "records may still be appended"},

		// Terminal via a syntax error, not a structural mismatch: a complete-but-malformed
		// value can never parse however many bytes follow, so it must quarantine.
		{"malformed value before records", `{"foo":@bad,"records":[{"a":1}]}`, true, "a syntax error in an earlier value is terminal"},
		{"top-level syntax garbage", `@bad`, true, "an invalid leading byte can never become an object"},
		{"malformed trailing bytes after a value", `{"foo":1@}`, true, "syntax garbage where a comma or close belongs is terminal"},
		{"well-formed envelope, bad array element", `{"records":[{"b":@bad}]}`, false, "the envelope is fine; the bad element is the array walk's concern"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, IsUnparseableRecordsJSONEnvelope([]byte(tc.delta)), tc.comment)
		})
	}
}

func TestRecordsJSONDeltaStuck(t *testing.T) {
	cases := []struct {
		name    string
		delta   string
		atStart bool
		want    bool
	}{
		// atStart: unparseable envelope (delegates to IsUnparseableRecordsJSONEnvelope).
		{"atStart top-level array", `[{"a":1}]`, true, true},
		{"atStart closed object no records", `{"a":1}`, true, true},
		// atStart: valid envelope, first array element decides.
		{"atStart leading non-object", `{"records":[42,{"a":1}]}`, true, true},
		{"atStart leading object (growing)", `{"records":[{"a":`, true, false},
		{"atStart first element consumable then bad", `{"records":[{"a":1},42`, true, false}, // offset 0 makes progress, not stuck
		{"atStart empty array so far", `{"records":[`, true, false},
		{"atStart envelope incomplete", `{"records":`, true, false},
		// continuation: the leftover after the last consumed record decides.
		{"continuation non-object element", `,42,{"b":2}]}`, false, true},
		{"continuation leading whitespace non-object", "  ,\n 42", false, true},
		{"continuation next object", `,{"c":3}`, false, false},
		{"continuation growing object", `,{"c":`, false, false},
		{"continuation footer only", `]}`, false, false},
		{"continuation trailing comma only", `,`, false, false},
		{"continuation empty", ``, false, false},
		// A complete-but-malformed object element looks like a growing object to a
		// first-byte check but can never parse. It must quarantine, else valid records
		// after it are silently lost until seal.
		{"continuation malformed object element", `,{"b":@bad},{"c":3}]}`, false, true},
		{"atStart malformed leading object element", `{"records":[{"b":@bad},{"c":3}]}`, true, true},
		{"atStart malformed value before records key", `{"foo":@bad,"records":[{"a":1}]}`, true, true},
		// A genuinely-growing object (still being written) yields EOF, not a syntax
		// error, so it must keep waiting rather than quarantine.
		{"continuation growing multi-field object", `,{"b":1,"c":`, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, RecordsJSONDeltaStuck([]byte(tc.delta), tc.atStart))
		})
	}
}

func TestRecordsJSONDeltaStuck_MalformedObjectNotLost(t *testing.T) {
	// {"a":1} is emitted, then a complete-but-malformed object {"b":@bad} follows,
	// then a valid {"c":3}. The offset advances past {"a":1}; on the continuation
	// delta the malformed object must be reported stuck so the blob is quarantined,
	// rather than stalling forever and silently losing {"c":3}.
	full := []byte(`{"records":[{"a":1},{"b":@bad},{"c":3}]}`)

	// Poll 1: the leading valid object is emitted; progress is made, so not stuck.
	reframed, consumed := SplitRecordsJSONDelta(full, true)
	require.Equal(t, `{"records":[{"a":1}]}`, string(reframed))
	require.Equal(t, len(`{"records":[{"a":1}`), consumed)
	require.False(t, RecordsJSONDeltaStuck(full, true), "offset 0 made progress, not stuck")

	// Poll 2: the continuation delta leads with the malformed object. No record is
	// framable, and it must be reported stuck (the fix) rather than waiting forever.
	cont := full[consumed:] // ",{"b":@bad},{"c":3}]}"
	reframed, consumed2 := SplitRecordsJSONDelta(cont, false)
	require.Nil(t, reframed)
	require.Equal(t, 0, consumed2)
	require.True(t, RecordsJSONDeltaStuck(cont, false), "a malformed object element must quarantine, not stall")
}
