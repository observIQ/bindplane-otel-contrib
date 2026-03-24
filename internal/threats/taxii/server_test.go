package taxii

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPServer_Discovery(t *testing.T) {
	backend := NewMemoryBackend()
	backend.SetDiscovery(&Discovery{
		Title: "Test", APIRoots: []string{"/taxii2/api1"}, Default: "/taxii2/api1",
	})
	backend.AddAPIRoot("api1", APIRootInfo{Title: "API 1", MaxContentLength: 10000}, nil)
	srv := NewHTTPServer(backend, nil)
	srv.BasePath = "/taxii2"
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/taxii2/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var disc Discovery
	if err := json.NewDecoder(resp.Body).Decode(&disc); err != nil {
		t.Fatal(err)
	}
	if disc.Title != "Test" || len(disc.APIRoots) != 1 {
		t.Errorf("discovery: %+v", disc)
	}
}

func TestHTTPServer_Collections(t *testing.T) {
	backend := NewMemoryBackend()
	backend.SetDiscovery(&Discovery{Title: "T", APIRoots: []string{"/taxii2/api1"}, Default: "/taxii2/api1"})
	backend.AddAPIRoot("api1", APIRootInfo{Title: "API 1", MaxContentLength: 10000}, []CollectionInfo{
		{ID: "c1", Title: "Col 1", CanRead: true, CanWrite: true},
	})
	srv := NewHTTPServer(backend, nil)
	srv.BasePath = "/taxii2"
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/taxii2/api1/collections/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var list CollectionsList
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list.Collections) != 1 || list.Collections[0].ID != "c1" {
		t.Errorf("collections: %+v", list)
	}
}

func TestHTTPServer_GetObjects_Empty(t *testing.T) {
	backend := NewMemoryBackend()
	backend.SetDiscovery(&Discovery{Title: "T", APIRoots: []string{"/taxii2/api1"}, Default: "/taxii2/api1"})
	backend.AddAPIRoot("api1", APIRootInfo{Title: "API 1", MaxContentLength: 10000}, []CollectionInfo{
		{ID: "c1", Title: "Col 1", CanRead: true, CanWrite: true},
	})
	srv := NewHTTPServer(backend, nil)
	srv.BasePath = "/taxii2"
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/taxii2/api1/collections/c1/objects/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var env struct {
		Objects []map[string]interface{} `json:"objects"`
		More    bool                     `json:"more"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	if len(env.Objects) != 0 || env.More {
		t.Errorf("objects: %+v", env)
	}
}

func TestHTTPServer_AddObjects_GetObject(t *testing.T) {
	backend := NewMemoryBackend()
	backend.SetDiscovery(&Discovery{Title: "T", APIRoots: []string{"/taxii2/api1"}, Default: "/taxii2/api1"})
	backend.AddAPIRoot("api1", APIRootInfo{Title: "API 1", MaxContentLength: 10000}, []CollectionInfo{
		{ID: "c1", Title: "Col 1", CanRead: true, CanWrite: true},
	})
	srv := NewHTTPServer(backend, nil)
	srv.BasePath = "/taxii2"
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	envelope := map[string]interface{}{
		"objects": []map[string]interface{}{
			{"type": "indicator", "id": "indicator--a", "spec_version": "2.1", "created": "2020-01-01T00:00:00Z", "modified": "2020-01-01T00:00:00Z", "pattern": "[file:name='x']"},
		},
	}
	body, _ := json.Marshal(envelope)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/taxii2/api1/collections/c1/objects/", bytes.NewReader(body))
	req.Header.Set("Content-Type", MediaTypeTAXII21)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("add objects status: %d", resp.StatusCode)
	}

	resp2, err := http.Get(ts.URL + "/taxii2/api1/collections/c1/objects/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("get objects status: %d", resp2.StatusCode)
	}
	var env struct {
		Objects []map[string]interface{} `json:"objects"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	if len(env.Objects) != 1 {
		t.Fatalf("objects: got %d", len(env.Objects))
	}
	if env.Objects[0]["id"] != "indicator--a" {
		t.Errorf("object id: %v", env.Objects[0]["id"])
	}
}

func TestHTTPServer_Auth_401(t *testing.T) {
	backend := NewMemoryBackend()
	backend.SetDiscovery(&Discovery{Title: "T", APIRoots: []string{"/taxii2/api1"}, Default: "/taxii2/api1"})
	backend.AddAPIRoot("api1", APIRootInfo{Title: "API 1", MaxContentLength: 10000}, nil)
	auth := BasicAuthUsers(map[string]string{"u": "p"})
	srv := NewHTTPServer(backend, auth)
	srv.BasePath = "/taxii2"
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/taxii2/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("without auth: status %d", resp.StatusCode)
	}
}
