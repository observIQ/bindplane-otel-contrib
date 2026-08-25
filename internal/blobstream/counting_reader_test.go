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

package blobstream

import (
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

// transientThenEOF hiccups with a non-EOF error on the first read, then serves data, then
// ends cleanly — modelling a reader that recovers from a transient error.
type transientThenEOF struct{ step int }

func (r *transientThenEOF) Read(p []byte) (int, error) {
	r.step++
	switch r.step {
	case 1:
		return 0, errors.New("transient")
	case 2:
		return copy(p, []byte("data")), nil
	default:
		return 0, io.EOF
	}
}

// TestCountingReader_TransientErrorClearedOnSuccess asserts a transient read error that a
// later successful read recovers from does not linger and misclassify a clean EOF as a
// broken stream.
func TestCountingReader_TransientErrorClearedOnSuccess(t *testing.T) {
	r := &countingReader{reader: &transientThenEOF{}}
	buf := make([]byte, 8)
	for {
		if _, err := r.Read(buf); errors.Is(err, io.EOF) {
			break
		}
	}
	require.NoError(t, r.ReadErr(), "a transient error recovered by a later read must not persist")
	require.True(t, r.AtEOF())
}
