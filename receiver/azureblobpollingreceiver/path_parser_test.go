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

package azureblobpollingreceiver //import "github.com/observiq/bindplane-otel-contrib/receiver/azureblobpollingreceiver"

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseTimeFromPattern(t *testing.T) {
	t.Run("Placeholder pattern - full datetime", func(t *testing.T) {
		pattern := "{year}/{month}/{day}/{hour}/{minute}/{second}/data.json"
		blobPath := "2024/03/15/14/30/45/data.json"

		parsedTime, err := parseTimeFromPattern(blobPath, pattern, true)
		require.NoError(t, err)
		require.NotNil(t, parsedTime)

		expected := time.Date(2024, 3, 15, 14, 30, 45, 0, time.UTC)
		require.Equal(t, expected, *parsedTime)
	})

	t.Run("Placeholder pattern - date only", func(t *testing.T) {
		pattern := "{year}/{month}/{day}/data.json"
		blobPath := "2024/03/15/data.json"

		parsedTime, err := parseTimeFromPattern(blobPath, pattern, true)
		require.NoError(t, err)
		require.NotNil(t, parsedTime)

		expected := time.Date(2024, 3, 15, 0, 0, 0, 0, time.UTC)
		require.Equal(t, expected, *parsedTime)
	})

	t.Run("Placeholder pattern - with prefix", func(t *testing.T) {
		pattern := "logs/{year}/{month}/{day}/file.json"
		blobPath := "logs/2024/03/15/file.json"

		parsedTime, err := parseTimeFromPattern(blobPath, pattern, true)
		require.NoError(t, err)
		require.NotNil(t, parsedTime)

		expected := time.Date(2024, 3, 15, 0, 0, 0, 0, time.UTC)
		require.Equal(t, expected, *parsedTime)
	})

	t.Run("Placeholder pattern - year and month only", func(t *testing.T) {
		pattern := "{year}/{month}/data.json"
		blobPath := "2024/03/data.json"

		parsedTime, err := parseTimeFromPattern(blobPath, pattern, true)
		require.NoError(t, err)
		require.NotNil(t, parsedTime)

		// Day defaults to 1 when not provided
		expected := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
		require.Equal(t, expected, *parsedTime)
	})

	t.Run("Placeholder pattern - missing year", func(t *testing.T) {
		pattern := "{month}/{day}/data.json"
		blobPath := "03/15/data.json"

		parsedTime, err := parseTimeFromPattern(blobPath, pattern, true)
		require.Error(t, err)
		require.Nil(t, parsedTime)
		require.Contains(t, err.Error(), "year is required")
	})

	t.Run("Placeholder pattern - path mismatch", func(t *testing.T) {
		pattern := "{year}/{month}/{day}/data.json"
		blobPath := "2024/03/data.json" // Missing day

		parsedTime, err := parseTimeFromPattern(blobPath, pattern, true)
		require.Error(t, err)
		require.Nil(t, parsedTime)
		require.Contains(t, err.Error(), "does not match pattern")
	})

	t.Run("Go time format - date and time", func(t *testing.T) {
		pattern := "2006/01/02/15/04"
		blobPath := "2024/03/15/14/30/data.json"

		parsedTime, err := parseTimeFromPattern(blobPath, pattern, true)
		require.NoError(t, err)
		require.NotNil(t, parsedTime)

		expected := time.Date(2024, 3, 15, 14, 30, 0, 0, time.UTC)
		require.Equal(t, expected, *parsedTime)
	})

	t.Run("Go time format - date only", func(t *testing.T) {
		pattern := "2006/01/02"
		blobPath := "2024/03/15/data.json"

		parsedTime, err := parseTimeFromPattern(blobPath, pattern, true)
		require.NoError(t, err)
		require.NotNil(t, parsedTime)

		expected := time.Date(2024, 3, 15, 0, 0, 0, 0, time.UTC)
		require.Equal(t, expected, *parsedTime)
	})

	t.Run("Go time format - with prefix", func(t *testing.T) {
		pattern := "logs/2006/01/02"
		blobPath := "logs/2024/03/15/file.json"

		parsedTime, err := parseTimeFromPattern(blobPath, pattern, true)
		require.NoError(t, err)
		require.NotNil(t, parsedTime)

		expected := time.Date(2024, 3, 15, 0, 0, 0, 0, time.UTC)
		require.Equal(t, expected, *parsedTime)
	})

	t.Run("Go time format - fewer path components in blob", func(t *testing.T) {
		pattern := "2006/01/02/15"
		blobPath := "2024/03/15" // Missing hour component

		parsedTime, err := parseTimeFromPattern(blobPath, pattern, true)
		require.Error(t, err)
		require.Nil(t, parsedTime)
		require.Contains(t, err.Error(), "fewer components")
	})

	t.Run("Go time format - invalid time values", func(t *testing.T) {
		pattern := "2006/01/02"
		blobPath := "2024/13/45/data.json" // Invalid month and day

		parsedTime, err := parseTimeFromPattern(blobPath, pattern, true)
		require.Error(t, err)
		require.Nil(t, parsedTime)
	})

	t.Run("Placeholder pattern - special characters in path", func(t *testing.T) {
		pattern := "data.backup/{year}-{month}-{day}/file.json"
		blobPath := "data.backup/2024-03-15/file.json"

		parsedTime, err := parseTimeFromPattern(blobPath, pattern, true)
		require.NoError(t, err)
		require.NotNil(t, parsedTime)

		expected := time.Date(2024, 3, 15, 0, 0, 0, 0, time.UTC)
		require.Equal(t, expected, *parsedTime)
	})

	t.Run("Placeholder pattern - unanchored matches anywhere in path (NSG flow-log layout)", func(t *testing.T) {
		// Azure NSG flow logs are stored at paths like:
		//   flowLogResourceID=/<SUB>_<RG>/<NSG>/y=YYYY/m=MM/d=DD/h=HH/m=MM/macAddress=.../PT1H.json
		// When the root_folder has been trimmed before parsing, the pattern is
		// invoked unanchored so the y=... segment is found wherever it sits.
		pattern := "y={year}/m={month}/d={day}/h={hour}/m={minute}"
		blobPath := "flowLogResourceID=/00000000-0000-0000-0000-000000000000_EXAMPLERG/EXAMPLE_NSG/y=2026/m=05/d=16/h=16/m=00/macAddress=AABBCCDDEEFF/PT1H.json"

		parsedTime, err := parseTimeFromPattern(blobPath, pattern, false)
		require.NoError(t, err)
		require.NotNil(t, parsedTime)

		expected := time.Date(2026, 5, 16, 16, 0, 0, 0, time.UTC)
		require.Equal(t, expected, *parsedTime)
	})

	t.Run("Placeholder pattern - anchored rejects unanchored match", func(t *testing.T) {
		// Same NSG-style path but parsed anchored (no root_folder trimmed):
		// must NOT match, since the pattern doesn't appear at the start.
		pattern := "y={year}/m={month}/d={day}/h={hour}/m={minute}"
		blobPath := "flowLogResourceID=/X/Y/y=2026/m=05/d=16/h=16/m=00/PT1H.json"

		parsedTime, err := parseTimeFromPattern(blobPath, pattern, true)
		require.Error(t, err)
		require.Nil(t, parsedTime)
		require.Contains(t, err.Error(), "does not match pattern")
	})
}
