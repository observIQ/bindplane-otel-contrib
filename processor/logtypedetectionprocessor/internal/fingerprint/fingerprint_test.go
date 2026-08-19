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
	"encoding/csv"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFingerprintJSONLogsNoCollisions(t *testing.T) {
	testFingerprintCorpusNoCollisions(t, "testdata/jsonLogs.csv")
}

func TestFingerprintCLFLogsNoCollisions(t *testing.T) {
	testFingerprintCorpusNoCollisions(t, "testdata/clfLogs.csv")
}

func TestFingerprintXMLLogsNoCollisions(t *testing.T) {
	testFingerprintCorpusNoCollisions(t, "testdata/xmlLogs.csv")
}

// TODO: BP-74 enable this test once we have a way to fingerprint generic data
// func TestFingerprintSysLogsNoCollisions(t *testing.T) {
// 	testFingerprintCorpusNoCollisions(t, "testdata/sysLogs.csv")
// }

func testFingerprintCorpusNoCollisions(t *testing.T, path string) {
	f, err := os.Open(path)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, f.Close())
	}()

	records, err := csv.NewReader(f).ReadAll()
	require.NoError(t, err)
	require.Greater(t, len(records), 1)

	seen := map[uint64]string{}
	for _, r := range records[1:] {
		logType, body := r[0], r[1]
		fp := HashLog(body)
		require.NotZero(t, fp, "no fingerprint for %s: %s", logType, body)

		if prev, ok := seen[fp]; ok {
			require.Equal(t, prev, logType, "fingerprint %x collides between %s and %s", fp, prev, logType)
			continue
		}
		seen[fp] = logType
	}
}
