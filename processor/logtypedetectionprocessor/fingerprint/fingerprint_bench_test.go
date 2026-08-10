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
)

func BenchmarkFingerprintJSONLogs(b *testing.B) {
	benchmarkFingerprintCorpus(b, "testdata/jsonLogs.csv")
}

func BenchmarkFingerprintXMLLogs(b *testing.B) {
	benchmarkFingerprintCorpus(b, "testdata/xmlLogs.csv")
}

func benchmarkFingerprintCorpus(b *testing.B, path string) {
	f, err := os.Open(path)
	if err != nil {
		b.Fatal(err)
	}
	records, err := csv.NewReader(f).ReadAll()
	if err != nil {
		b.Fatal(err)
	}
	if err := f.Close(); err != nil {
		b.Fatal(err)
	}

	bodies := make([]string, 0, len(records)-1)
	bytes := 0
	for _, r := range records[1:] {
		bodies = append(bodies, r[1])
		bytes += len(r[1])
	}

	b.SetBytes(int64(bytes))
	b.ReportAllocs()
	b.ResetTimer()
	n := 0
	for b.Loop() {
		for i := range bodies {
			if FingerprintLog(bodies[i]) == 0 {
				b.Fatal("no fingerprint")
			}
		}
		n += len(bodies)
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(n), "ns/record")
}
