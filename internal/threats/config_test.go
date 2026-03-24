// Copyright observIQ, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package threats

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_noFile(t *testing.T) {
	ctx := context.Background()
	cfg, err := Load(ctx, LoadOptions{SkipEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.Validator.Version != "2.1" {
		t.Errorf("validator version: got %q, want %q", cfg.Validator.Version, "2.1")
	}
	if cfg.TaxiiServer.Addr != "localhost:8080" {
		t.Errorf("taxii_server addr: got %q, want %q", cfg.TaxiiServer.Addr, "localhost:8080")
	}
}

func TestLoad_baseFile(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "base.yaml")
	mustWrite(basePath, `
validator:
  version: "2.1"
  verbose: true
taxii_server:
  addr: "127.0.0.1:9090"
  base: "/taxii2"
`)

	ctx := context.Background()
	cfg, err := Load(ctx, LoadOptions{BasePath: basePath, SkipEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Validator.Verbose {
		t.Error("expected verbose true from base")
	}
	if cfg.TaxiiServer.Addr != "127.0.0.1:9090" {
		t.Errorf("expected base addr, got %q", cfg.TaxiiServer.Addr)
	}
	if cfg.TaxiiServer.Base != "/taxii2" {
		t.Errorf("expected base path from base file, got %q", cfg.TaxiiServer.Base)
	}
}

func TestLoad_baseAndOverride(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "base.yaml")
	overridePath := filepath.Join(dir, "override.yaml")
	mustWrite(basePath, `
validator:
  version: "2.1"
  verbose: true
taxii_server:
  addr: "127.0.0.1:9090"
  base: "/taxii2"
`)
	mustWrite(overridePath, `
taxii_server:
  addr: "0.0.0.0:8080"
validator:
  silent: true
`)

	ctx := context.Background()
	cfg, err := Load(ctx, LoadOptions{BasePath: basePath, OverridePath: overridePath, SkipEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Validator.Verbose {
		t.Error("expected verbose true from base")
	}
	if !cfg.Validator.Silent {
		t.Error("expected silent true from override")
	}
	if cfg.TaxiiServer.Addr != "0.0.0.0:8080" {
		t.Errorf("expected override addr, got %q", cfg.TaxiiServer.Addr)
	}
	if cfg.TaxiiServer.Base != "/taxii2" {
		t.Errorf("expected base from base file, got %q", cfg.TaxiiServer.Base)
	}
}

func TestLoad_envSubstitution(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "config.yaml")
	// Use OTel confmap syntax: ${env:VAR}
	mustWrite(basePath, `
taxii_server:
  addr: "${env:TEST_ADDR}"
`)

	t.Setenv("TEST_ADDR", "192.168.1.1:9000")

	ctx := context.Background()
	cfg, err := Load(ctx, LoadOptions{BasePath: basePath, SkipEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TaxiiServer.Addr != "192.168.1.1:9000" {
		t.Errorf("expected env substitution, got %q", cfg.TaxiiServer.Addr)
	}
}

func TestValidatorConfig(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Validator.Version = "2.1"
	cfg.Validator.Strict = true
	cfg.Validator.MaxConcurrentObjects = 2

	vc := cfg.ValidatorConfig()
	if vc.Options.Version != "2.1" {
		t.Errorf("version: got %q", vc.Options.Version)
	}
	if !vc.Options.Strict {
		t.Error("expected strict true")
	}
	if vc.MaxConcurrentObjects != 2 {
		t.Errorf("MaxConcurrentObjects: got %d", vc.MaxConcurrentObjects)
	}
}

func TestConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *Config
		wantErr bool
	}{
		{
			name:    "default config is valid",
			cfg:     DefaultConfig(),
			wantErr: false,
		},
		{
			name: "invalid version",
			cfg: &Config{
				Validator: ValidatorConfig{Version: "3.0"},
			},
			wantErr: true,
		},
		{
			name: "invalid addr",
			cfg: &Config{
				TaxiiServer: TaxiiServerConfig{Addr: "not-a-valid-addr"},
			},
			wantErr: true,
		},
		{
			name: "negative max_page_size",
			cfg: &Config{
				TaxiiServer: TaxiiServerConfig{MaxPageSize: -1},
			},
			wantErr: true,
		},
		{
			name: "negative max_concurrent_objects",
			cfg: &Config{
				Validator: ValidatorConfig{MaxConcurrentObjects: -1},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Validator.Version != "2.1" {
		t.Errorf("expected default version 2.1, got %q", cfg.Validator.Version)
	}
	if !cfg.Validator.PreserveObjectOrder {
		t.Error("expected PreserveObjectOrder true by default")
	}
	if cfg.TaxiiServer.Addr != "localhost:8080" {
		t.Errorf("expected default addr localhost:8080, got %q", cfg.TaxiiServer.Addr)
	}
	if cfg.TaxiiServer.Base != "/taxii2" {
		t.Errorf("expected default base /taxii2, got %q", cfg.TaxiiServer.Base)
	}
	if cfg.TaxiiServer.MaxPageSize != 10000 {
		t.Errorf("expected default max_page_size 10000, got %d", cfg.TaxiiServer.MaxPageSize)
	}
}

func mustWrite(path, content string) {
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		panic(err)
	}
}
