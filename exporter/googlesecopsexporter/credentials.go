package googlesecopsexporter

import (
	"context"
	"errors"
	"fmt"
	"os"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	backstoryScope = "https://www.googleapis.com/auth/malachite-ingestion"
	chronicleScope = "https://www.googleapis.com/auth/cloud-platform"
)

// Override for testing
var tokenSource = func(ctx context.Context, cfg *Config) (oauth2.TokenSource, error) {
	creds, err := googleCredentials(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return creds.TokenSource, nil
}

func googleCredentials(ctx context.Context, cfg *Config) (*google.Credentials, error) {
	scope := chronicleScope
	if cfg.API == backstoryAPI {
		scope = backstoryScope
	}
	switch {
	case cfg.Creds != "":
		return google.CredentialsFromJSON(ctx, []byte(cfg.Creds), scope)
	case cfg.CredsFilePath != "":
		credsData, err := os.ReadFile(cfg.CredsFilePath)
		if err != nil {
			return nil, fmt.Errorf("read credentials file: %w", err)
		}

		if len(credsData) == 0 {
			return nil, errors.New("credentials file is empty")
		}

		return google.CredentialsFromJSON(ctx, credsData, scope)
	default:
		return google.FindDefaultCredentials(ctx, scope)
	}
}
