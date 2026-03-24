package taxii

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
)

// AuthChecker validates incoming request credentials. Used by the TAXII server
// (exchange as receiver). Return an error to reject the request (server responds 401).
type AuthChecker interface {
	ValidateRequest(r *http.Request) error
}

func BasicAuthUsers(users map[string]string) AuthChecker {
	if users == nil {
		users = make(map[string]string)
	}
	return &basicAuthUsers{users: users}
}

type basicAuthUsers struct {
	users map[string]string
}

var errMissingAuth = errors.New("taxii: missing or invalid authorization")
var errUnauthorized = errors.New("taxii: unauthorized")

func (b *basicAuthUsers) ValidateRequest(r *http.Request) error {
	const prefix = "Basic "
	s := r.Header.Get("Authorization")
	if s == "" || !strings.HasPrefix(s, prefix) {
		return errMissingAuth
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, prefix))
	if err != nil {
		return errMissingAuth
	}
	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return errMissingAuth
	}
	user, pass := parts[0], parts[1]
	expected, ok := b.users[user]
	if !ok || expected != pass {
		return errUnauthorized
	}
	return nil
}
