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

package worker

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// TestArchive_TruncatedEntryIsSkipped asserts a truncated entry drops that entry alone.
// The other entries read fine, so failing the whole object would discard good records
// and send them to the dead-letter queue with the bad one.
func TestArchive_TruncatedEntryIsSkipped(t *testing.T) {
	t.Parallel()

	good := tarFile{name: "a.log", body: []byte("kept1\nkept2\n")}
	big := tarFile{name: "b.log", body: []byte(strings.Repeat("padding-line\n", 200))}
	full := tarBytes(t, []tarFile{good, big})

	// Cut inside the second entry's data, so its header reads and its body does not.
	firstEntryEnd := 512 + roundUp512(len(good.body))
	cut := firstEntryEnd + 512 + 100
	require.Less(t, cut, len(full))

	core, logs := observer.New(zap.WarnLevel)
	ap := &archiveProducer{
		stream: LogStream{Name: "o", MaxLogSize: testMaxLogSize, Logger: zap.New(core), TryDecoding: true},
		open:   func() (archiveBackend, error) { return newTarBackend(bytes.NewReader(full[:cut])), nil },
		limits: defaultArchiveLimits(),
	}

	seq, err := ap.records(context.Background(), Offset{})
	require.NoError(t, err)

	var bodies []string
	var fatal error
	for rec, rerr := range seq {
		if rerr != nil {
			if isDLQConditionError(rerr) {
				fatal = rerr
			}
			continue
		}
		bodies = append(bodies, rec.(string))
	}

	require.NoError(t, fatal, "a truncated entry must not fail the whole object")
	require.GreaterOrEqual(t, len(bodies), 2)
	require.Equal(t, []string{"kept1", "kept2"}, bodies[:2], "the readable entry is still delivered")
	require.Positive(t, logs.FilterMessageSnippet("skipping unparseable archive entry").Len(),
		"the skipped entry must be reported")
}

func roundUp512(n int) int {
	if n%512 == 0 {
		return n
	}
	return n + (512 - n%512)
}
