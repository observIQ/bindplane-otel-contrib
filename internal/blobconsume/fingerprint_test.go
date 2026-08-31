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
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFingerprint(t *testing.T) {
	t.Run("truncates to size", func(t *testing.T) {
		require.Equal(t, []byte("hello"), Fingerprint([]byte("hello, world"), 5))
	})

	t.Run("returns all when shorter than size", func(t *testing.T) {
		require.Equal(t, []byte("hi"), Fingerprint([]byte("hi"), 5))
	})

	t.Run("non-positive size falls back to the default bound, not the whole input", func(t *testing.T) {
		big := make([]byte, DefaultFingerprintSize+100)
		require.Len(t, Fingerprint(big, 0), DefaultFingerprintSize, "size 0 uses the default cap")
		require.Len(t, Fingerprint(big, -1), DefaultFingerprintSize, "negative size uses the default cap")
	})

	t.Run("copies rather than aliasing the input", func(t *testing.T) {
		src := []byte("abcdef")
		fp := Fingerprint(src, 3)
		src[0] = 'z'
		require.Equal(t, []byte("abc"), fp)
	})

	t.Run("empty input yields empty fingerprint", func(t *testing.T) {
		require.Empty(t, Fingerprint(nil, 5))
	})
}

func TestSameBlob(t *testing.T) {
	t.Run("grown blob keeps prefix", func(t *testing.T) {
		require.True(t, SameBlob([]byte(`{"records":[{"a`), []byte(`{"records":[{"a":1},{"b`)))
	})

	t.Run("identical", func(t *testing.T) {
		require.True(t, SameBlob([]byte("abc"), []byte("abc")))
	})

	t.Run("stored fingerprint is a prefix of the grown current one", func(t *testing.T) {
		// stored (shorter) is a prefix of current (grown): same blob.
		require.True(t, SameBlob([]byte("abc"), []byte("abcdef")))
	})

	t.Run("a shrunk blob is not the same blob", func(t *testing.T) {
		// current is shorter than stored (the blob shrank/was replaced by a smaller
		// blob), so it is treated as a different blob even though it shares the prefix.
		// This matches filelog's directional Fingerprint.StartsWith.
		require.False(t, SameBlob([]byte("abcdef"), []byte("abc")))
	})

	t.Run("replaced blob diverges", func(t *testing.T) {
		require.False(t, SameBlob([]byte(`{"records":[{"a":1}`), []byte(`{"records":[{"z":9}`)))
	})

	t.Run("empty fingerprint never matches", func(t *testing.T) {
		require.False(t, SameBlob(nil, []byte("abc")))
		require.False(t, SameBlob([]byte("abc"), nil))
	})
}
