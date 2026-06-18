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
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"go.uber.org/zap"
)

// controlSource exposes a small local HTTP endpoint to read and set the posture
// level. It is the explicit-command trigger that does not depend on the OpAMP
// management link. Bind it to a local address (e.g. 127.0.0.1).
type controlSource struct {
	endpoint string
	levels   LevelSet
	logger   *zap.Logger
	setVote  func(string, Level)
	snapshot func() (Level, map[string]Level)

	server *http.Server
	wg     sync.WaitGroup
}

func newControlSource(cfg *ControlServerConfig, levels LevelSet, logger *zap.Logger, setVote func(string, Level), snapshot func() (Level, map[string]Level)) *controlSource {
	return &controlSource{
		endpoint: cfg.Endpoint,
		levels:   levels,
		logger:   logger.Named("control_server"),
		setVote:  setVote,
		snapshot: snapshot,
	}
}

func (s *controlSource) name() string { return "control_server" }

func (s *controlSource) start(_ context.Context) error {
	ln, err := net.Listen("tcp", s.endpoint)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/posture", s.handlePosture)
	s.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := s.server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error("control server stopped", zap.Error(err))
		}
	}()
	s.logger.Info("posture control server listening", zap.String("endpoint", s.endpoint))
	return nil
}

func (s *controlSource) stop() {
	if s.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.server.Shutdown(ctx)
	}
	s.wg.Wait()
}

type postureRequest struct {
	Level string `json:"level"`
}

type postureResponse struct {
	Effective string            `json:"effective"`
	Votes     map[string]string `json:"votes"`
}

func (s *controlSource) handlePosture(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.writeStatus(w)
	case http.MethodPost, http.MethodPut:
		var req postureRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		level, err := s.levels.Parse(req.Level)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.setVote(s.name(), level)
		s.writeStatus(w)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *controlSource) writeStatus(w http.ResponseWriter) {
	effective, votes := s.snapshot()
	resp := postureResponse{
		Effective: s.levels.Name(effective),
		Votes:     make(map[string]string, len(votes)),
	}
	for k, v := range votes {
		resp.Votes[k] = s.levels.Name(v)
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		s.logger.Debug("failed to write posture status", zap.Error(err))
	}
}
