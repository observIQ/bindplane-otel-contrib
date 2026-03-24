package taxii

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// HTTPServer is a TAXII 2.1 HTTP server. It uses a Backend for persistence and an optional AuthChecker for request validation.
type HTTPServer struct {
	Backend     Backend
	Auth        AuthChecker
	MaxPageSize int
	BasePath    string // e.g. "/taxii2" or "" for root
}

// DefaultMaxPageSize is the default maximum objects per page when not set.
const DefaultMaxPageSize = 100

// NewHTTPServer returns a TAXII 2.1 server with the given backend. Auth may be nil to allow unauthenticated access.
func NewHTTPServer(backend Backend, auth AuthChecker) *HTTPServer {
	s := &HTTPServer{
		Backend:     backend,
		Auth:        auth,
		MaxPageSize: DefaultMaxPageSize,
		BasePath:    "/taxii2",
	}
	return s
}

// Handler returns an http.Handler that serves TAXII 2.1 endpoints. Apply auth middleware if Auth is set.
func (s *HTTPServer) Handler() http.Handler {
	mux := http.NewServeMux()
	base := strings.TrimSuffix(s.BasePath, "/")
	if base == "" {
		base = "/"
	}
	mux.HandleFunc(base+"/", s.handleDiscoveryOrAPIRoot)
	return s.wrapAuth(mux)
}

func (s *HTTPServer) wrapAuth(h http.Handler) http.Handler {
	if s.Auth == nil {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := s.Auth.ValidateRequest(r); err != nil {
			s.writeError(w, err, http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// TAXII error body per spec.
type taxiiError struct {
	Title       string `json:"title"`
	HTTPStatus  string `json:"http_status"`
	Description string `json:"description"`
}

func (s *HTTPServer) writeError(w http.ResponseWriter, err error, status int) {
	w.Header().Set("Content-Type", MediaTypeTAXII21)
	w.WriteHeader(status)
	body := taxiiError{
		Title:       http.StatusText(status),
		HTTPStatus:  strconv.Itoa(status),
		Description: err.Error(),
	}
	_ = json.NewEncoder(w).Encode(body)
}

func (s *HTTPServer) writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", MediaTypeTAXII21)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *HTTPServer) parseFilter(r *http.Request) (FilterArgs, int, error) {
	q := r.URL.Query()
	filter := FilterArgs{Match: make(map[string]string)}
	for k, v := range q {
		if len(v) == 0 || v[0] == "" {
			continue
		}
		switch k {
		case "limit":
			n, err := strconv.Atoi(v[0])
			if err != nil || n <= 0 {
				return filter, 0, fmt.Errorf("invalid limit")
			}
			if n > s.MaxPageSize {
				n = s.MaxPageSize
			}
			filter.Limit = n
		case "next":
			filter.Next = v[0]
		case "added_after":
			filter.AddedAfter = v[0]
		default:
			if strings.HasPrefix(k, "match[") && strings.HasSuffix(k, "]") {
				key := k[6 : len(k)-1]
				filter.Match[key] = v[0]
			}
		}
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = s.MaxPageSize
	}
	return filter, limit, nil
}

// pathParts returns base path and the path after base (e.g. "api1", "api1/collections", "api1/collections/id/manifest").
func (s *HTTPServer) pathParts(r *http.Request) (base, rest string) {
	base = strings.TrimSuffix(s.BasePath, "/")
	if base == "" {
		base = "/"
	}
	p := r.URL.Path
	if !strings.HasPrefix(p, base) {
		return "", ""
	}
	rest = strings.TrimPrefix(p, base)
	rest = strings.TrimPrefix(rest, "/")
	return base, rest
}

func (s *HTTPServer) handleDiscoveryOrAPIRoot(w http.ResponseWriter, r *http.Request) {
	_, rest := s.pathParts(r)
	if rest == "" || rest == "/" {
		s.handleDiscovery(w, r)
		return
	}
	s.handleAPIRootAndRest(w, r)
}

func (s *HTTPServer) handleDiscovery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, fmt.Errorf("method not allowed"), http.StatusMethodNotAllowed)
		return
	}
	disc, err := s.Backend.ServerDiscovery()
	if err != nil || disc == nil {
		s.writeError(w, fmt.Errorf("server discovery not available"), http.StatusNotFound)
		return
	}
	s.writeJSON(w, http.StatusOK, disc)
}

// handleAPIRootAndRest routes: /<api_root>/ , /<api_root>/status/<id>/ , /<api_root>/collections/ , /<api_root>/collections/<id>/ , manifest, objects, etc.
func (s *HTTPServer) handleAPIRootAndRest(w http.ResponseWriter, r *http.Request) {
	_, rest := s.pathParts(r)
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		s.writeError(w, fmt.Errorf("not found"), http.StatusNotFound)
		return
	}
	apiRoot := parts[0]
	if len(parts) == 1 {
		// GET /<api_root>/
		if r.Method != http.MethodGet {
			s.writeError(w, fmt.Errorf("method not allowed"), http.StatusMethodNotAllowed)
			return
		}
		info, err := s.Backend.GetAPIRootInformation(apiRoot)
		if err != nil || info == nil {
			s.writeError(w, fmt.Errorf("api root not found"), http.StatusNotFound)
			return
		}
		s.writeJSON(w, http.StatusOK, info)
		return
	}
	if parts[1] == "status" {
		if len(parts) < 3 {
			s.writeError(w, fmt.Errorf("not found"), http.StatusNotFound)
			return
		}
		statusID := parts[2]
		status, err := s.Backend.GetStatus(apiRoot, statusID)
		if err != nil || status == nil {
			s.writeError(w, fmt.Errorf("status not found"), http.StatusNotFound)
			return
		}
		s.writeJSON(w, http.StatusOK, status)
		return
	}
	if parts[1] == "collections" {
		s.handleCollections(w, r, apiRoot, parts[2:])
		return
	}
	s.writeError(w, fmt.Errorf("not found"), http.StatusNotFound)
}

func (s *HTTPServer) handleCollections(w http.ResponseWriter, r *http.Request, apiRoot string, parts []string) {
	if len(parts) == 0 || (len(parts) == 1 && parts[0] == "") {
		// GET /<api_root>/collections/
		if r.Method != http.MethodGet {
			s.writeError(w, fmt.Errorf("method not allowed"), http.StatusMethodNotAllowed)
			return
		}
		list, err := s.Backend.GetCollections(apiRoot)
		if err != nil || list == nil {
			s.writeError(w, fmt.Errorf("api root not found"), http.StatusNotFound)
			return
		}
		s.writeJSON(w, http.StatusOK, list)
		return
	}
	collectionID := parts[0]
	if _, err := s.Backend.GetCollection(apiRoot, collectionID); err != nil || s.getCollection(apiRoot, collectionID) == nil {
		s.writeError(w, fmt.Errorf("collection not found"), http.StatusNotFound)
		return
	}
	if len(parts) == 1 {
		// GET /<api_root>/collections/<id>/
		if r.Method != http.MethodGet {
			s.writeError(w, fmt.Errorf("method not allowed"), http.StatusMethodNotAllowed)
			return
		}
		col, _ := s.Backend.GetCollection(apiRoot, collectionID)
		s.writeJSON(w, http.StatusOK, col)
		return
	}
	switch parts[1] {
	case "manifest":
		s.handleManifest(w, r, apiRoot, collectionID)
	case "objects":
		if len(parts) == 2 {
			s.handleObjects(w, r, apiRoot, collectionID)
		} else if len(parts) == 4 && parts[3] == "versions" {
			s.handleObjectVersions(w, r, apiRoot, collectionID, parts[2])
		} else if len(parts) >= 3 {
			s.handleObject(w, r, apiRoot, collectionID, parts[2])
		} else {
			s.writeError(w, fmt.Errorf("not found"), http.StatusNotFound)
		}
	default:
		s.writeError(w, fmt.Errorf("not found"), http.StatusNotFound)
	}
}

func (s *HTTPServer) getCollection(apiRoot, collectionID string) *CollectionInfo {
	col, _ := s.Backend.GetCollection(apiRoot, collectionID)
	return col
}

func (s *HTTPServer) handleManifest(w http.ResponseWriter, r *http.Request, apiRoot, collectionID string) {
	if r.Method != http.MethodGet {
		s.writeError(w, fmt.Errorf("method not allowed"), http.StatusMethodNotAllowed)
		return
	}
	col := s.getCollection(apiRoot, collectionID)
	if col != nil && !col.CanRead {
		s.writeError(w, fmt.Errorf("forbidden"), http.StatusForbidden)
		return
	}
	filter, limit, err := s.parseFilter(r)
	if err != nil {
		s.writeError(w, err, http.StatusBadRequest)
		return
	}
	result, err := s.Backend.GetObjectManifest(apiRoot, collectionID, filter, limit)
	if err != nil {
		s.writeError(w, err, http.StatusBadRequest)
		return
	}
	if result == nil {
		result = &ManifestResult{Objects: []ManifestResource{}, Headers: make(map[string]string)}
	}
	for k, v := range result.Headers {
		w.Header().Set(k, v)
	}
	body := map[string]interface{}{
		"objects": result.Objects,
		"more":    result.More,
	}
	if result.Next != "" {
		body["next"] = result.Next
	}
	w.Header().Set("Content-Type", MediaTypeTAXII21)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(body)
}

func (s *HTTPServer) handleObjects(w http.ResponseWriter, r *http.Request, apiRoot, collectionID string) {
	col := s.getCollection(apiRoot, collectionID)
	if col == nil {
		s.writeError(w, fmt.Errorf("collection not found"), http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if !col.CanRead {
			s.writeError(w, fmt.Errorf("forbidden"), http.StatusForbidden)
			return
		}
		filter, limit, err := s.parseFilter(r)
		if err != nil {
			s.writeError(w, err, http.StatusBadRequest)
			return
		}
		result, err := s.Backend.GetObjects(apiRoot, collectionID, filter, limit)
		if err != nil {
			s.writeError(w, err, http.StatusBadRequest)
			return
		}
		if result == nil {
			result = &ObjectsResult{Objects: []map[string]interface{}{}, Headers: make(map[string]string)}
		}
		for k, v := range result.Headers {
			w.Header().Set(k, v)
		}
		body := map[string]interface{}{
			"objects": result.Objects,
			"more":    result.More,
		}
		if result.Next != "" {
			body["next"] = result.Next
		}
		w.Header().Set("Content-Type", MediaTypeTAXII21)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(body)
	case http.MethodPost:
		if !col.CanWrite {
			s.writeError(w, fmt.Errorf("forbidden"), http.StatusForbidden)
			return
		}
		ct := r.Header.Get("Content-Type")
		if !strings.Contains(ct, "application/taxii+json") {
			s.writeError(w, fmt.Errorf("invalid content type"), http.StatusUnsupportedMediaType)
			return
		}
		var envelope map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			s.writeError(w, err, http.StatusBadRequest)
			return
		}
		info, _ := s.Backend.GetAPIRootInformation(apiRoot)
		if info != nil && r.ContentLength > info.MaxContentLength {
			s.writeError(w, fmt.Errorf("content too large"), http.StatusRequestEntityTooLarge)
			return
		}
		status, err := s.Backend.AddObjects(apiRoot, collectionID, envelope, time.Now().UTC())
		if err != nil {
			s.writeError(w, err, http.StatusUnprocessableEntity)
			return
		}
		s.writeJSON(w, http.StatusAccepted, status)
	default:
		s.writeError(w, fmt.Errorf("method not allowed"), http.StatusMethodNotAllowed)
	}
}

func (s *HTTPServer) handleObject(w http.ResponseWriter, r *http.Request, apiRoot, collectionID, objectID string) {
	col := s.getCollection(apiRoot, collectionID)
	if col == nil {
		s.writeError(w, fmt.Errorf("collection not found"), http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if !col.CanRead {
			s.writeError(w, fmt.Errorf("forbidden"), http.StatusForbidden)
			return
		}
		filter, limit, err := s.parseFilter(r)
		if err != nil {
			s.writeError(w, err, http.StatusBadRequest)
			return
		}
		result, err := s.Backend.GetObject(apiRoot, collectionID, objectID, filter, limit)
		if err != nil {
			s.writeError(w, err, http.StatusBadRequest)
			return
		}
		if result == nil || len(result.Objects) == 0 {
			s.writeError(w, fmt.Errorf("object not found"), http.StatusNotFound)
			return
		}
		for k, v := range result.Headers {
			w.Header().Set(k, v)
		}
		body := map[string]interface{}{
			"objects": result.Objects,
			"more":    result.More,
		}
		if result.Next != "" {
			body["next"] = result.Next
		}
		w.Header().Set("Content-Type", MediaTypeTAXII21)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(body)
	case http.MethodDelete:
		if !col.CanWrite {
			s.writeError(w, fmt.Errorf("forbidden"), http.StatusForbidden)
			return
		}
		filter, _, err := s.parseFilter(r)
		if err != nil {
			s.writeError(w, err, http.StatusBadRequest)
			return
		}
		if err := s.Backend.DeleteObject(apiRoot, collectionID, objectID, filter); err != nil {
			s.writeError(w, err, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", MediaTypeTAXII21)
		w.WriteHeader(http.StatusOK)
	default:
		s.writeError(w, fmt.Errorf("method not allowed"), http.StatusMethodNotAllowed)
	}
}

func (s *HTTPServer) handleObjectVersions(w http.ResponseWriter, r *http.Request, apiRoot, collectionID, objectID string) {
	if r.Method != http.MethodGet {
		s.writeError(w, fmt.Errorf("method not allowed"), http.StatusMethodNotAllowed)
		return
	}
	col := s.getCollection(apiRoot, collectionID)
	if col != nil && !col.CanRead {
		s.writeError(w, fmt.Errorf("forbidden"), http.StatusForbidden)
		return
	}
	filter, limit, err := s.parseFilter(r)
	if err != nil {
		s.writeError(w, err, http.StatusBadRequest)
		return
	}
	result, err := s.Backend.GetObjectVersions(apiRoot, collectionID, objectID, filter, limit)
	if err != nil {
		s.writeError(w, err, http.StatusBadRequest)
		return
	}
	if result == nil || len(result.Versions) == 0 {
		s.writeError(w, fmt.Errorf("object not found"), http.StatusNotFound)
		return
	}
	body := map[string]interface{}{
		"versions": result.Versions,
		"more":     result.More,
	}
	if result.Next != "" {
		body["next"] = result.Next
	}
	w.Header().Set("Content-Type", MediaTypeTAXII21)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(body)
}

// ListenAndServe listens on addr and serves TAXII 2.1. It uses the same pattern as http.ListenAndServe.
func (s *HTTPServer) ListenAndServe(addr string) error {
	return http.ListenAndServe(addr, s.Handler())
}

// ParseFilterFromURL parses query parameters from url.Values into FilterArgs and limit. Used by tests.
func ParseFilterFromURL(q url.Values, maxPageSize int) (FilterArgs, int, error) {
	filter := FilterArgs{Match: make(map[string]string)}
	for k, v := range q {
		if len(v) == 0 || v[0] == "" {
			continue
		}
		switch k {
		case "limit":
			n, err := strconv.Atoi(v[0])
			if err != nil || n <= 0 {
				return filter, 0, fmt.Errorf("invalid limit")
			}
			if maxPageSize > 0 && n > maxPageSize {
				n = maxPageSize
			}
			filter.Limit = n
		case "next":
			filter.Next = v[0]
		case "added_after":
			filter.AddedAfter = v[0]
		default:
			if strings.HasPrefix(k, "match[") && strings.HasSuffix(k, "]") {
				key := k[6 : len(k)-1]
				filter.Match[key] = v[0]
			}
		}
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = maxPageSize
	}
	if limit <= 0 {
		limit = DefaultMaxPageSize
	}
	return filter, limit, nil
}
