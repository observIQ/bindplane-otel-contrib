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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHashFileSHA256(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	require.NoError(t, os.WriteFile(p, []byte("abc"), 0o600))

	hex, skipped, reason, err := hashFileSHA256(p, 1024)
	require.NoError(t, err)
	require.False(t, skipped)
	require.Empty(t, reason)
	require.Len(t, hex, 64)
}

func TestHashFileSHA256TooLarge(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "big.txt")
	require.NoError(t, os.WriteFile(p, []byte("abcd"), 0o600))

	_, skipped, reason, err := hashFileSHA256(p, 2)
	require.NoError(t, err)
	require.True(t, skipped)
	require.Equal(t, "file exceeds max_bytes", reason)
}
