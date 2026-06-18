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

package posture

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func testLevels(t *testing.T) LevelSet {
	t.Helper()
	ls, err := NewLevelSet(DefaultLevels)
	require.NoError(t, err)
	return ls
}

func TestLevelSet(t *testing.T) {
	ls := testLevels(t)

	silent, err := ls.Parse("silent")
	require.NoError(t, err)
	assert.Equal(t, ls.Min(), silent)

	full, err := ls.Parse("full")
	require.NoError(t, err)
	assert.Equal(t, ls.Max(), full)
	assert.True(t, silent < full)

	assert.Equal(t, "medium", ls.Name(Level(2)))
	assert.Equal(t, ls.Max(), ls.Clamp(Level(99)))
	assert.Equal(t, ls.Min(), ls.Clamp(Level(-5)))

	_, err = ls.Parse("nope")
	assert.Error(t, err)

	_, err = NewLevelSet(nil)
	assert.Error(t, err)
	_, err = NewLevelSet([]string{"a", "a"})
	assert.Error(t, err)
}

func TestConfigValidate(t *testing.T) {
	require.NoError(t, Config{}.Validate())
	require.NoError(t, Config{Default: "medium"}.Validate())
	assert.Error(t, Config{Default: "bogus"}.Validate())
	assert.Error(t, Config{SignalFile: &SignalFileConfig{}}.Validate())
	assert.Error(t, Config{AutoDetect: &AutoDetectConfig{Floor: "bogus"}}.Validate())
	assert.Error(t, Config{AutoDetect: &AutoDetectConfig{FailureThreshold: -1}}.Validate())
}

func TestProviderMostRestrictiveWins(t *testing.T) {
	p, err := NewProvider(Config{
		Default:       "full",
		SignalFile:    &SignalFileConfig{Path: "/does/not/exist"},
		ControlServer: &ControlServerConfig{Endpoint: "127.0.0.1:0"},
	}, zap.NewNop())
	require.NoError(t, err)
	pr := p.(*provider)

	// Both sources start at the default (full).
	assert.Equal(t, pr.levels.Max(), p.Current())

	// One source drops to silent -> effective becomes silent (min wins).
	pr.setVote("signal_file", pr.levels.Min())
	assert.Equal(t, pr.levels.Min(), p.Current())

	// The other source going to full does not raise effective above the min.
	pr.setVote("control_server", pr.levels.Max())
	assert.Equal(t, pr.levels.Min(), p.Current())

	// Restoring the restrictive source raises effective back to full.
	pr.setVote("signal_file", pr.levels.Max())
	assert.Equal(t, pr.levels.Max(), p.Current())
}

func TestProviderSubscribe(t *testing.T) {
	p, err := NewProvider(Config{Default: "full", SignalFile: &SignalFileConfig{Path: "/nope"}}, zap.NewNop())
	require.NoError(t, err)
	pr := p.(*provider)

	ch, unsub := p.Subscribe()
	defer unsub()

	pr.setVote("signal_file", pr.levels.Min())
	select {
	case lvl := <-ch:
		assert.Equal(t, pr.levels.Min(), lvl)
	case <-time.After(time.Second):
		t.Fatal("expected a level notification")
	}

	// No change -> no notification.
	pr.setVote("signal_file", pr.levels.Min())
	select {
	case <-ch:
		t.Fatal("unexpected notification for unchanged level")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSignalFileSource(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "posture")
	require.NoError(t, os.WriteFile(path, []byte("low\n"), 0o600))

	p, err := NewProvider(Config{
		Default:    "full",
		SignalFile: &SignalFileConfig{Path: path, WatchInterval: 10 * time.Millisecond},
	}, zap.NewNop())
	require.NoError(t, err)
	pr := p.(*provider)
	require.NoError(t, pr.Start(context.Background()))
	defer pr.Shutdown()

	low, _ := pr.levels.Parse("low")
	assert.Equal(t, low, p.Current())

	require.NoError(t, os.WriteFile(path, []byte("medium"), 0o600))
	medium, _ := pr.levels.Parse("medium")
	require.Eventually(t, func() bool { return p.Current() == medium }, time.Second, 10*time.Millisecond)
}

func TestControlSource(t *testing.T) {
	// Reserve a port then release it for the source to bind.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	endpoint := ln.Addr().String()
	require.NoError(t, ln.Close())

	p, err := NewProvider(Config{
		Default:       "full",
		ControlServer: &ControlServerConfig{Endpoint: endpoint},
	}, zap.NewNop())
	require.NoError(t, err)
	pr := p.(*provider)
	require.NoError(t, pr.Start(context.Background()))
	defer pr.Shutdown()

	body, _ := json.Marshal(postureRequest{Level: "silent"})
	require.Eventually(t, func() bool {
		resp, err := http.Post(fmt.Sprintf("http://%s/posture", endpoint), "application/json", bytes.NewReader(body))
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 2*time.Second, 20*time.Millisecond)

	assert.Equal(t, pr.levels.Min(), p.Current())
}

func TestExportHealthDetector(t *testing.T) {
	ls := testLevels(t)
	now := time.Unix(0, 0)
	var got Level
	gotCalled := false
	d := newExportHealthDetector(&AutoDetectConfig{
		FailureThreshold:  2,
		RecoveryThreshold: 2,
		MinDwell:          time.Minute,
	}, ls, ls.Max(), zap.NewNop(), func(_ string, l Level) { got, gotCalled = l, true })
	d.now = func() time.Time { return now }

	// Two failures step down one level from full -> medium.
	d.record(false)
	assert.False(t, gotCalled)
	d.record(false)
	require.True(t, gotCalled)
	assert.Equal(t, ls.Max()-1, got)

	// Another two failures within min_dwell are suppressed.
	gotCalled = false
	d.record(false)
	d.record(false)
	assert.False(t, gotCalled, "min_dwell should suppress rapid changes")

	// After min_dwell elapses, two more failures step down again.
	now = now.Add(2 * time.Minute)
	d.record(false)
	d.record(false)
	require.True(t, gotCalled)
	assert.Equal(t, ls.Max()-2, got)
}
