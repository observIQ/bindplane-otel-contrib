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

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// isTerminalJSONErr reports whether err is a JSON decode error no appended bytes can resolve: a
// syntax error is terminal; io.EOF / io.ErrUnexpectedEOF mean the value is still being written.
func isTerminalJSONErr(err error) bool {
	return err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF)
}

// SplitRecordsJSONDelta splits a byte-range delta from a records-json blob (shaped
// {"records":[ ... ]}, grown in place all hour) into a self-contained {"records":[ ... ]} document
// of only the complete records in the delta, plus the delta bytes they occupy.
//
// atStart is true when the delta begins at blob byte 0 (starts with the object envelope) and false
// when it begins partway through the array (starts with ",{...}"). It tolerates a missing trailing
// "]}" (mid-hour reads end after the last written record) and a trailing "," before an unwritten one.
//
// consumed is how far the caller advances its offset. It stops just after the last complete record
// and never includes the closing "]}" footer (Azure recommits that block as it appends, so consuming
// it would strand the offset); the blob is retired by seal/age-out, not by offset reaching size.
// reframed is nil and consumed 0 when no complete record is present yet.
func SplitRecordsJSONDelta(delta []byte, atStart bool) (reframed []byte, consumed int) {
	var body []byte
	var bodyStart int
	if atStart {
		b, ok := recordsArrayBody(delta)
		if !ok {
			return nil, 0 // envelope not written far enough to reach the "records" array
		}
		bodyStart = len(delta) - len(b)
		body = b
	} else {
		trimmed := bytes.TrimLeft(delta, " \t\r\n")
		trimmed = bytes.TrimPrefix(trimmed, []byte(","))
		bodyStart = len(delta) - len(trimmed)
		body = trimmed
	}

	// Decode elements from a synthetic "[" + body, tolerating a missing "]" (mid-hour) and a
	// partial trailing element.
	dec := json.NewDecoder(bytes.NewReader(append([]byte{'['}, body...)))
	_, _ = dec.Token() // the injected "["

	var elems []json.RawMessage
	lastEnd := 0 // byte position in body just after the last complete element
	for dec.More() {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			break // partial trailing element
		}
		// Halt on a non-object element: a scalar's complete form (e.g. "12") may still be the
		// prefix of a longer value being written ("123"), while an object is self-delimiting.
		if !isJSONObject(raw) {
			break
		}
		elems = append(elems, raw)
		lastEnd = int(dec.InputOffset()) - 1 // subtract the injected "["
	}

	if len(elems) == 0 {
		return nil, 0
	}

	// Advance only to just after the last complete record, never the closing "]}"
	// footer (see the doc comment for why).
	consumed = bodyStart + lastEnd

	reframed = make([]byte, 0, len(`{"records":[]}`)+lastEnd)
	reframed = append(reframed, `{"records":[`...)
	for i, e := range elems {
		if i > 0 {
			reframed = append(reframed, ',')
		}
		reframed = append(reframed, e...)
	}
	reframed = append(reframed, ']', '}')
	return reframed, consumed
}

// IsUnparseableRecordsJSONEnvelope reports whether delta, read from blob byte 0, can never frame a
// {"records":[...]} document however many bytes are appended: its top level is not an object,
// "records" is present but not an array, or the object is closed with no "records" key. It returns
// false for a still-growing envelope (EOF mid-structure) so the caller acts only on terminal input.
func IsUnparseableRecordsJSONEnvelope(delta []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(delta))
	tok, err := dec.Token()
	if err != nil {
		return isTerminalJSONErr(err)
	}
	if tok != json.Delim('{') {
		return true // a non-object top level never becomes {"records":...}
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return isTerminalJSONErr(err)
		}
		if key, _ := keyTok.(string); key == "records" {
			arrTok, err := dec.Token()
			if err != nil {
				return isTerminalJSONErr(err)
			}
			return arrTok != json.Delim('[') // present but not an array is terminal
		}
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return isTerminalJSONErr(err)
		}
	}
	// More() is false because the object closed, ended mid-object, or a syntax error follows. A
	// real "}" and a syntax error are terminal; an EOF means "records" may still be appended.
	closeTok, err := dec.Token()
	if err != nil {
		return isTerminalJSONErr(err)
	}
	return closeTok == json.Delim('}')
}

// recordsArrayBody returns the bytes of delta just past the "records" array's opening "[", and
// whether that opening was found. It locates "records" structurally (not the first array) so an
// earlier array-valued key does not mislead it. ok is false when the envelope is not yet written
// far enough to reach the array. On a pathological duplicate-"records" document (which Azure never
// writes) it frames from the first such key, whereas the whole-blob json.Unmarshal path takes the
// last; the divergence is harmless because valid records-json has a single "records" key.
func recordsArrayBody(delta []byte) ([]byte, bool) {
	dec := json.NewDecoder(bytes.NewReader(delta))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, false
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, false // key not fully written yet
		}
		if key, _ := keyTok.(string); key == "records" {
			arrTok, err := dec.Token()
			if err != nil || arrTok != json.Delim('[') {
				return nil, false // "[" not written yet, or "records" is not an array
			}
			return delta[dec.InputOffset():], true
		}
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return nil, false // value for this key not fully written yet
		}
	}
	return nil, false
}

// nextRecordsElementStuck reports whether the next records-array element can never become a record,
// so the caller must quarantine rather than wait. body is the bytes just after the array "[" or a
// continuation delta's leading ",". A non-"{" element (scalar/array) never becomes a record. A "{"
// element is decoded so a complete-but-malformed object (terminal) is told apart from one still
// being written (EOF); a first-byte check cannot, and mis-reading a malformed object as "growing"
// strands the offset and loses the records after it. Returns false for an empty/whitespace body, a
// closing "]", or a growing/valid object.
func nextRecordsElementStuck(body []byte) bool {
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	if len(trimmed) == 0 {
		return false
	}
	switch trimmed[0] {
	case ']':
		return false
	case '{':
		var obj json.RawMessage
		return isTerminalJSONErr(json.NewDecoder(bytes.NewReader(trimmed)).Decode(&obj))
	default:
		return true
	}
}

// RecordsJSONDeltaStuck reports whether a records-json delta read at the current offset can never
// make progress and should be quarantined rather than retried: the envelope is unparseable
// (atStart only), or the next array element can never become a record (a non-object value, or a
// complete-but-malformed object). It returns false for an incomplete/growing delta so the caller
// keeps waiting in that case. This is what makes the incremental path emit the records before a
// bad element and then quarantine the blob, rather than silently stalling on it until seal.
func RecordsJSONDeltaStuck(delta []byte, atStart bool) bool {
	if atStart {
		if IsUnparseableRecordsJSONEnvelope(delta) {
			return true
		}
		body, ok := recordsArrayBody(delta)
		if !ok {
			return false // envelope not fully written yet
		}
		return nextRecordsElementStuck(body)
	}
	trimmed := bytes.TrimLeft(delta, " \t\r\n")
	trimmed = bytes.TrimPrefix(trimmed, []byte(","))
	return nextRecordsElementStuck(trimmed)
}

// isJSONObject reports whether raw is a JSON object ("{...}"). A RawMessage holds the
// value's exact bytes with no leading whitespace, so the first byte suffices.
func isJSONObject(raw json.RawMessage) bool {
	return len(raw) > 0 && raw[0] == '{'
}

// IsJSONObject reports whether b is a single valid JSON object. Unlike isJSONObject it
// validates b and tolerates surrounding whitespace, so callers can gate on the NDJSON
// consumer's per-line object requirement rather than mere json.Valid.
func IsJSONObject(b []byte) bool {
	trimmed := bytes.TrimSpace(b)
	return isJSONObject(json.RawMessage(trimmed)) && json.Valid(trimmed)
}

// ValidRecordsJSONDoc reports whether b parses under the same schema
// RecordsJSONLogsConsumer uses ([]map[string]any): a top-level array or a non-object
// element fails; an object with an absent or empty records array passes.
func ValidRecordsJSONDoc(b []byte) bool {
	var doc struct {
		Records []map[string]any `json:"records"`
	}
	return json.Unmarshal(b, &doc) == nil
}

// SplitLineDelta splits a newline-delimited delta (used for the json/NDJSON and text
// formats) into the bytes up to and including the last newline and the number of
// bytes consumed. A trailing partial line with no newline yet is left for the next
// read. Returns nil, 0 when no complete line is present.
func SplitLineDelta(delta []byte) (complete []byte, consumed int) {
	idx := bytes.LastIndexByte(delta, '\n')
	if idx < 0 {
		return nil, 0
	}
	return delta[:idx+1], idx + 1
}
