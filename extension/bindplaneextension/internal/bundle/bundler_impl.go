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
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/observiq/bindplane-otel-contrib/pkg/support-util/bundle"
	"github.com/observiq/bindplane-otel-contrib/pkg/support-util/bundle/sources"
)

// defaultEncryptionPublicKeyPEM is the RSA public key used to produce encrypted BNDL files
// when no override path is configured. The server holds the corresponding private key.
var defaultEncryptionPublicKeyPEM = []byte(`-----BEGIN PUBLIC KEY-----
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAtIPujVs2zj7vhIU4A8tJ
suf05io4mbE3xmWxj0m2naDDemY7a4Ic5TJGGAV/+M3hYHtNDEt7glTI30kcqPR1
KUdl5idU2DpeOphHQ/2LbgirrP49rz+wOchp6F6M3V2Au8HUhpAXWsCc6+93ub+F
dDXu7ocKTHR0h0Baz4kKEpPEBuNY858hCjkhkdMXh3eg2iD3B2o227oJf41iomGm
BfMkTuwC1NDYmAIhtLC4ojS7qJ3M+G/Cd+XqPvurlWNKQRUoFkcv5P31akqoZEK0
Y8GlDX9ckM8HIgnHi/UaSUOESexPLDzCQTzqJ1kBeS7C3rorifzZu2n9AVoltLmm
zwIDAQAB
-----END PUBLIC KEY-----`)

type defaultBundler struct {
	httpClient *http.Client
	opts       Options
}

// NewDefaultBundler returns a Bundler backed by bindplane-support-util.
// If httpClient is nil, http.DefaultClient is used.
func NewDefaultBundler(httpClient *http.Client, opts Options) Bundler {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &defaultBundler{httpClient: httpClient, opts: opts}
}

// Collect gathers configured artifacts and returns the encrypted bundle bytes.
func (b *defaultBundler) Collect(_ context.Context) ([]byte, error) {
	opts := bundle.DefaultBundleOptions()
	opts.OutputDir = b.opts.OutputDir
	if opts.OutputDir == "" {
		opts.OutputDir = os.TempDir()
	}

	opts.Collector.ConfigPath = b.opts.CollectorConfigPath
	opts.Collector.ManagerConfigPath = b.opts.CollectorManagerConfig
	opts.Collector.LogDir = b.opts.CollectorLogDir
	opts.Collector.ProfileDir = b.opts.CollectorProfileDir
	opts.Collector.InstallRoot = b.opts.CollectorInstallRoot
	opts.IncludeConfig = b.opts.IncludeConfig
	opts.IncludeLogs = b.opts.IncludeLogs
	opts.IncludeSystemInfo = b.opts.IncludeSystemInfo
	opts.IncludeProfiles = b.opts.IncludeProfiles
	opts.IncludeNetworkState = b.opts.IncludeNetworkState
	if b.opts.ProfileMaxAge > 0 {
		opts.Collector.ProfileMaxAge = b.opts.ProfileMaxAge
	}

	encKey := b.opts.EncryptionPublicKeyPEM
	if len(encKey) == 0 {
		encKey = defaultEncryptionPublicKeyPEM
	}
	opts.Encryption.Enabled = true
	opts.Encryption.PublicKeyPEM = encKey

	var artifactSources []bundle.ArtifactSource
	if b.opts.CollectorLogDir != "" {
		artifactSources = append(artifactSources, sources.NewCollectorLogSource(b.opts.CollectorLogDir))
	}
	if b.opts.CollectorConfigPath != "" {
		artifactSources = append(artifactSources, sources.NewCollectorConfigSource(b.opts.CollectorConfigPath))
	}
	if b.opts.CollectorManagerConfig != "" {
		artifactSources = append(artifactSources, sources.NewCollectorManagerConfigSource(b.opts.CollectorManagerConfig))
	}
	if b.opts.ManagerConfigPath != "" {
		artifactSources = append(artifactSources, sources.NewConfigSource(b.opts.ManagerConfigPath))
	}
	if b.opts.CollectorProfileDir != "" && b.opts.IncludeProfiles {
		artifactSources = append(artifactSources, sources.NewCollectorProfileSource(b.opts.CollectorProfileDir))
	}
	if b.opts.CollectorInstallRoot != "" {
		artifactSources = append(artifactSources, sources.NewCollectorRootExtrasSource())
		artifactSources = append(artifactSources, sources.NewCollectorPluginsSource())
	}
	if b.opts.IncludeSystemInfo {
		artifactSources = append(artifactSources, sources.NewSystemInfoSource())
	}

	path, err := bundle.CreateBundleFromOptions(opts, artifactSources)
	if err != nil {
		return nil, fmt.Errorf("create bundle: %w", err)
	}
	defer os.Remove(path)

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read bundle file: %w", err)
	}
	return data, nil
}

// SendToURL POSTs the encrypted bundle bytes to uploadURL.
func (b *defaultBundler) SendToURL(ctx context.Context, data []byte, sessionID string, uploadURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create bundle upload request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Bindplane-Session-ID", sessionID)

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("upload support bundle: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("support bundle upload returned status %d", resp.StatusCode)
	}
	return nil
}

// SendErrorToURL notifies the server that bundle collection failed.
func (b *defaultBundler) SendErrorToURL(ctx context.Context, sessionID string, errMessage string, uploadURL string) error {
	if sessionID == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, bytes.NewReader([]byte(errMessage)))
	if err != nil {
		return fmt.Errorf("create bundle error request: %w", err)
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	req.Header.Set("X-Bindplane-Session-ID", sessionID)
	req.Header.Set("X-Bindplane-Support-Bundle-Status", "failed")

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send support bundle error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("support bundle error notification returned status %d", resp.StatusCode)
	}
	return nil
}
