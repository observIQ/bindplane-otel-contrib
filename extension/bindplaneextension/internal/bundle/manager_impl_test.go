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

package bundle

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

// fakeBundler is a test double for Bundler.
type fakeBundler struct {
	collectData []byte
	collectErr  error
	sendCalls   []sendCall
	errorCalls  []errorCall
}

type sendCall struct {
	data      []byte
	sessionID string
	uploadURL string
}

type errorCall struct {
	sessionID  string
	errMessage string
	uploadURL  string
}

func (f *fakeBundler) Collect(_ context.Context) ([]byte, error) {
	return f.collectData, f.collectErr
}

func (f *fakeBundler) SendToURL(_ context.Context, data []byte, sessionID string, uploadURL string) error {
	f.sendCalls = append(f.sendCalls, sendCall{data: data, sessionID: sessionID, uploadURL: uploadURL})
	return nil
}

func (f *fakeBundler) SendErrorToURL(_ context.Context, sessionID string, errMessage string, uploadURL string) error {
	f.errorCalls = append(f.errorCalls, errorCall{sessionID: sessionID, errMessage: errMessage, uploadURL: uploadURL})
	return nil
}

func newManager(t *testing.T, b Bundler) Manager {
	t.Helper()
	return NewDefaultManager(zaptest.NewLogger(t, zaptest.Level(zap.WarnLevel)), b)
}

func TestDefaultManager_HandleRequest_success(t *testing.T) {
	fb := &fakeBundler{collectData: []byte("bundle-bytes")}
	m := newManager(t, fb)

	m.HandleRequest(context.Background(), Capability, RequestType,
		[]byte("session_id: sess-1\n"), "https://example.com/upload")

	require.Len(t, fb.sendCalls, 1)
	assert.Equal(t, "sess-1", fb.sendCalls[0].sessionID)
	assert.Equal(t, "https://example.com/upload", fb.sendCalls[0].uploadURL)
	assert.Equal(t, []byte("bundle-bytes"), fb.sendCalls[0].data)
	assert.Empty(t, fb.errorCalls)
}

func TestDefaultManager_HandleRequest_collectFails(t *testing.T) {
	fb := &fakeBundler{collectErr: errors.New("disk full")}
	m := newManager(t, fb)

	m.HandleRequest(context.Background(), Capability, RequestType,
		[]byte("session_id: sess-2\n"), "https://example.com/upload")

	assert.Empty(t, fb.sendCalls)
	require.Len(t, fb.errorCalls, 1)
	assert.Equal(t, "sess-2", fb.errorCalls[0].sessionID)
	assert.Contains(t, fb.errorCalls[0].errMessage, "disk full")
}

func TestDefaultManager_HandleRequest_wrongCapability(t *testing.T) {
	fb := &fakeBundler{collectData: []byte("bundle")}
	m := newManager(t, fb)

	m.HandleRequest(context.Background(), "other.capability", RequestType,
		[]byte("session_id: sess-3\n"), "https://example.com/upload")

	assert.Empty(t, fb.sendCalls)
	assert.Empty(t, fb.errorCalls)
}

func TestDefaultManager_HandleRequest_wrongType(t *testing.T) {
	fb := &fakeBundler{collectData: []byte("bundle")}
	m := newManager(t, fb)

	m.HandleRequest(context.Background(), Capability, "otherType",
		[]byte("session_id: sess-4\n"), "https://example.com/upload")

	assert.Empty(t, fb.sendCalls)
	assert.Empty(t, fb.errorCalls)
}

func TestDefaultManager_HandleRequest_missingSessionID(t *testing.T) {
	fb := &fakeBundler{collectData: []byte("bundle")}
	m := newManager(t, fb)

	m.HandleRequest(context.Background(), Capability, RequestType,
		[]byte("session_id: \n"), "https://example.com/upload")

	assert.Empty(t, fb.sendCalls)
	assert.Empty(t, fb.errorCalls)
}

func TestDefaultManager_HandleRequest_emptyUploadURL(t *testing.T) {
	fb := &fakeBundler{collectData: []byte("bundle")}
	m := newManager(t, fb)

	m.HandleRequest(context.Background(), Capability, RequestType,
		[]byte("session_id: sess-5\n"), "")

	assert.Empty(t, fb.sendCalls)
	assert.Empty(t, fb.errorCalls)
}
