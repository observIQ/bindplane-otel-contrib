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
	"context"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// TestBufferedReader_LogsWhenSizeUnknown asserts that when the object's size is unknown,
// the reader records that a short download cannot be detected — so a silently-truncated
// transcoded object is visible rather than passing as complete.
func TestBufferedReader_LogsWhenSizeUnknown(t *testing.T) {
	core, logs := observer.New(zap.DebugLevel)
	stream := &LogStream{
		Body:       io.NopCloser(strings.NewReader("line\n")),
		MaxLogSize: 4096,
		Logger:     zap.New(core),
		Size:       0, // unknown, e.g. a GCS transcoded object
	}

	_, err := stream.BufferedReader(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1,
		logs.FilterMessage("object size unknown; a short download cannot be detected and reads as complete").Len(),
		"an unknown object size must be logged as unverifiable completeness")
}

// TestBufferedReader_NoLogWhenSizeKnown asserts a known size does not log the warning.
func TestBufferedReader_NoLogWhenSizeKnown(t *testing.T) {
	core, logs := observer.New(zap.DebugLevel)
	stream := &LogStream{
		Body:       io.NopCloser(strings.NewReader("line\n")),
		MaxLogSize: 4096,
		Logger:     zap.New(core),
		Size:       5,
	}

	_, err := stream.BufferedReader(context.Background())
	require.NoError(t, err)
	require.Zero(t, logs.FilterMessage("object size unknown; a short download cannot be detected and reads as complete").Len())
}
