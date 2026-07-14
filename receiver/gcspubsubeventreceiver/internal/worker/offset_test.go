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

package worker_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/observiq/bindplane-otel-contrib/receiver/gcspubsubeventreceiver/internal/worker"
)

func TestNewOffset(t *testing.T) {
	t.Parallel()

	o := worker.NewOffset(42)
	require.Equal(t, int64(42), o.Offset)
	require.Equal(t, 0, o.EntryIndex)
}

func TestNewArchiveOffset(t *testing.T) {
	t.Parallel()

	o := worker.NewArchiveOffset(3, 512)
	require.Equal(t, 3, o.EntryIndex)
	require.Equal(t, int64(512), o.Offset)
}

// TestOffset_MarshalUnmarshalArchive verifies the entry index round-trips.
func TestOffset_MarshalUnmarshalArchive(t *testing.T) {
	t.Parallel()

	original := worker.NewArchiveOffset(7, 4096)
	data, err := original.Marshal()
	require.NoError(t, err)

	restored := worker.NewOffset(0)
	require.NoError(t, restored.Unmarshal(data))
	require.Equal(t, 7, restored.EntryIndex)
	require.Equal(t, int64(4096), restored.Offset)
}

// TestOffset_UnmarshalLegacyBlob verifies a legacy {"offset":N} blob (written
// before the archive entry index existed) unmarshals with EntryIndex defaulting
// to 0, so stored offsets from earlier receiver versions still resume correctly.
func TestOffset_UnmarshalLegacyBlob(t *testing.T) {
	t.Parallel()

	o := worker.NewOffset(0)
	require.NoError(t, o.Unmarshal([]byte(`{"offset":123}`)))
	require.Equal(t, 0, o.EntryIndex)
	require.Equal(t, int64(123), o.Offset)
}

// TestOffset_NonArchiveMarshalOmitsEntryIndex verifies a non-archive offset
// marshals to the legacy shape (no entry_index field), keeping the stored format
// unchanged for the common non-archive path.
func TestOffset_NonArchiveMarshalOmitsEntryIndex(t *testing.T) {
	t.Parallel()

	data, err := worker.NewOffset(55).Marshal()
	require.NoError(t, err)
	require.JSONEq(t, `{"offset":55}`, string(data))
}

func TestOffset_MarshalUnmarshal(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name  string
		value int64
	}{
		{name: "zero", value: 0},
		{name: "positive", value: 1024},
		{name: "large int64", value: math.MaxInt64},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			original := worker.NewOffset(tc.value)

			data, err := original.Marshal()
			require.NoError(t, err)

			restored := worker.NewOffset(0)
			err = restored.Unmarshal(data)
			require.NoError(t, err)

			require.Equal(t, original.Offset, restored.Offset)
		})
	}
}

func TestOffset_UnmarshalEmpty(t *testing.T) {
	t.Parallel()

	o := worker.NewOffset(999)
	err := o.Unmarshal([]byte{})
	require.NoError(t, err)
	// The offset must remain unchanged when empty data is provided.
	require.Equal(t, int64(999), o.Offset)
}

func TestOffset_UnmarshalInvalidJSON(t *testing.T) {
	t.Parallel()

	o := worker.NewOffset(0)
	err := o.Unmarshal([]byte("not json"))
	require.Error(t, err)
}

func TestOffsetStorageKey(t *testing.T) {
	t.Parallel()

	require.Equal(t, "_gcs_pub_event_offset", worker.OffsetStorageKey)
}
