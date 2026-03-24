package taxii

import (
	"net/http"
	"net/http/httptest"
	"testing"

	exchanges "github.com/observiq/bindplane-otel-collector/exchange"
)

func TestClient_GetServer(t *testing.T) {
	discovery := `{"title":"Test Server","description":"","api_roots":["api1/"],"default":"api1/"}`
	apiRoot := `{"title":"API 1","versions":["application/taxii+json;version=2.1"],"max_content_length":1000000}`
	collections := `{"collections":[{"id":"col1","title":"Collection 1","can_read":true,"can_write":true}]}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", MediaTypeTAXII21)
		switch r.URL.Path {
		case "/taxii2/", "/":
			w.Write([]byte(discovery))
		case "/taxii2/api1/", "/api1/":
			w.Write([]byte(apiRoot))
		case "/taxii2/api1/collections/", "/api1/collections/":
			w.Write([]byte(collections))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	base := srv.URL + "/taxii2/"
	if base[len(base)-1] != '/' {
		base += "/"
	}
	client := NewClient(srv.URL+"/taxii2/", nil)
	server, err := client.GetServer()
	if err != nil {
		t.Fatal(err)
	}
	if server.Title != "Test Server" {
		t.Errorf("title: got %q", server.Title)
	}
	if len(server.APIRoots()) != 1 {
		t.Fatalf("api_roots: got %d", len(server.APIRoots()))
	}
	root := server.Default()
	if root == nil {
		t.Fatal("default api root is nil")
	}
	info, err := root.Info()
	if err != nil {
		t.Fatal(err)
	}
	if info.Title != "API 1" {
		t.Errorf("api root title: got %q", info.Title)
	}
	cols, err := root.Collections()
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 1 || cols[0].Info().ID != "col1" {
		t.Errorf("collections: got %d, first id %q", len(cols), "")
		if len(cols) > 0 {
			t.Errorf("first collection id: %q", cols[0].Info().ID)
		}
	}
}

func TestClient_GetServer_WithAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, _ := r.BasicAuth()
		if user != "u" || pass != "p" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", MediaTypeTAXII21)
		w.Write([]byte(`{"title":"Auth Server","api_roots":[]}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL+"/", &exchanges.BasicAuth{Username: "u", Password: "p"})
	server, err := client.GetServer()
	if err != nil {
		t.Fatal(err)
	}
	if server.Title != "Auth Server" {
		t.Errorf("title: got %q", server.Title)
	}
}

func TestFilter_Query(t *testing.T) {
	f := Filter{Limit: 10, AddedAfter: "2020-01-01T00:00:00Z", Match: map[string]string{"id": "x"}}
	q := f.Query()
	if q.Get("limit") != "10" {
		t.Errorf("limit: %q", q.Get("limit"))
	}
	if q.Get("added_after") != "2020-01-01T00:00:00Z" {
		t.Errorf("added_after: %q", q.Get("added_after"))
	}
	if q.Get("match[id]") != "x" {
		t.Errorf("match[id]: %q", q.Get("match[id]"))
	}
}
