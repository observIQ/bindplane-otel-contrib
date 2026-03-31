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

func TestCompileExcludes(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "fim")
	require.NoError(t, os.MkdirAll(nested, 0o755))

	ms := compileExcludes([]string{filepath.Join(nested, "*"), "*.tmp"})
	require.True(t, ms[0](filepath.Clean(filepath.Join(nested, "x"))))
	require.False(t, ms[0](filepath.Clean(filepath.Join(root, "other", "x"))))
	require.True(t, ms[1](filepath.Clean("foo.tmp")))
	require.False(t, ms[1](filepath.Clean("foo.txt")))
}
