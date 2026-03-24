package taxii

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBasicAuthUsers_Valid(t *testing.T) {
	auth := BasicAuthUsers(map[string]string{"alice": "secret", "bob": "pass2"})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("alice:secret")))
	if err := auth.ValidateRequest(req); err != nil {
		t.Errorf("expected valid: %v", err)
	}
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("bob:pass2")))
	if err := auth.ValidateRequest(req); err != nil {
		t.Errorf("expected valid for bob: %v", err)
	}
}

func TestBasicAuthUsers_Invalid(t *testing.T) {
	auth := BasicAuthUsers(map[string]string{"alice": "secret"})
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("alice:wrong")))
	if err := auth.ValidateRequest(req); err == nil {
		t.Error("expected error for wrong password")
	}
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("unknown:secret")))
	if err := auth.ValidateRequest(req); err == nil {
		t.Error("expected error for unknown user")
	}
}

func TestBasicAuthUsers_Missing(t *testing.T) {
	auth := BasicAuthUsers(map[string]string{"alice": "secret"})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if err := auth.ValidateRequest(req); err == nil {
		t.Error("expected error for missing Authorization")
	}
	req.Header.Set("Authorization", "Bearer token")
	if err := auth.ValidateRequest(req); err == nil {
		t.Error("expected error for non-Basic scheme")
	}
	req.Header.Set("Authorization", "Basic not-valid-base64!!!")
	if err := auth.ValidateRequest(req); err == nil {
		t.Error("expected error for invalid base64")
	}
}
