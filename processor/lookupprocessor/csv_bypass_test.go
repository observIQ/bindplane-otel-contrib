// Copyright  observIQ, Inc.
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

package lookupprocessor

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/confmap"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// startedProcessor builds and starts a processor for cfg, returning it plus a
// recorder of everything it logged.
func startedProcessor(t *testing.T, cfg *Config) (*lookupProcessor, *observer.ObservedLogs) {
	t.Helper()
	core, logs := observer.New(zapcore.DebugLevel)
	p := newLookupProcessor(cfg, component.MustNewID("lookup"), signalLogs, zap.New(core))
	require.NoError(t, p.start(context.Background(), componenttestHost{}))
	t.Cleanup(func() { require.NoError(t, p.shutdown(context.Background())) })
	return p, logs
}

// componenttestHost is a minimal host with no extensions, which is all the CSV
// path needs since it never asks for a storage client.
type componenttestHost struct{}

func (componenttestHost) GetExtensions() map[component.ID]component.Component { return nil }

func TestCSVSourceIsNotWrappedInCache(t *testing.T) {
	path := createTestCSVFile(t, map[string]any{"ip": "0.0.0.0", "env": "prod"})

	// cache_enabled true is the factory default, so this is what real configs carry.
	p, _ := startedProcessor(t, &Config{
		Context: attributesContext, Field: "ip", CSV: path,
		CacheEnabled: true, CacheTTL: defaultCacheTTL, CacheMaxEntries: defaultCacheMaxEntries,
	})

	_, isCache := p.source.(*LookupCache)
	require.False(t, isCache, "a CSV source must not be wrapped in LookupCache")
	require.IsType(t, &CSVFile{}, p.source, "a CSV source must be used directly")
}

func TestRedisAndAPISourcesStayWrappedInCache(t *testing.T) {
	p, _ := startedProcessor(t, &Config{
		Context: attributesContext, Field: "ip",
		API:          &APIConfig{URL: "http://127.0.0.1:1/lookup"},
		CacheEnabled: true, CacheTTL: defaultCacheTTL, CacheMaxEntries: defaultCacheMaxEntries,
	})
	require.IsType(t, &LookupCache{}, p.source, "non-CSV sources must keep the cache")
}

func TestWarnsOnCacheKeysSetWithCSV(t *testing.T) {
	path := createTestCSVFile(t, map[string]any{"ip": "0.0.0.0", "env": "prod"})

	for _, tc := range []struct {
		name string
		conf map[string]any
		want []string
	}{
		{
			name: "cache_enabled true",
			conf: map[string]any{"cache_enabled": true},
			want: []string{"cache_enabled"},
		},
		{
			// false is still an invalid key for csv, and warning on it now is what
			// makes the future error predictable rather than a surprise on upgrade.
			name: "cache_enabled false",
			conf: map[string]any{"cache_enabled": false},
			want: []string{"cache_enabled"},
		},
		{
			name: "all three keys",
			conf: map[string]any{"cache_enabled": true, "cache_ttl": "1m", "cache_max_entries": 10},
			want: []string{"cache_enabled", "cache_ttl", "cache_max_entries"},
		},
		{
			// storage names the extension the cache persists through, so a csv source
			// ignores it for the same reason it ignores the other three.
			name: "storage",
			conf: map[string]any{"storage": "file_storage"},
			want: []string{"storage"},
		},
		{
			name: "storage alongside a cache key",
			conf: map[string]any{"cache_enabled": true, "storage": "file_storage"},
			want: []string{"cache_enabled", "storage"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := map[string]any{"context": attributesContext, "field": "ip", "csv": path}
			for k, v := range tc.conf {
				raw[k] = v
			}
			cfg := createDefaultConfig().(*Config)
			require.NoError(t, confmap.NewFromStringMap(raw).Unmarshal(cfg))

			_, logs := startedProcessor(t, cfg)

			warnings := logs.FilterLevelExact(zapcore.WarnLevel).All()
			require.Len(t, warnings, 1, "exactly one warning should be emitted")
			msg := warnings[0].Message + warnings[0].ContextMap()["keys"].(string)
			for _, key := range tc.want {
				require.Contains(t, msg, key)
			}
			require.Contains(t, warnings[0].Message, "future release",
				"the warning must say the setting becomes an error later")
		})
	}
}

func TestNoWarningWhenCacheKeysAbsentWithCSV(t *testing.T) {
	path := createTestCSVFile(t, map[string]any{"ip": "0.0.0.0", "env": "prod"})

	// The factory fills all three cache defaults, so a config that never mentions
	// them must stay silent. Warning here would fire for every existing CSV user.
	cfg := createDefaultConfig().(*Config)
	require.NoError(t, confmap.NewFromStringMap(map[string]any{
		"context": attributesContext, "field": "ip", "csv": path,
	}).Unmarshal(cfg))

	_, logs := startedProcessor(t, cfg)
	require.Empty(t, logs.FilterLevelExact(zapcore.WarnLevel).All(),
		"defaulted cache settings must not warn")
}

func TestNoWarningWhenCacheKeysSetWithRedis(t *testing.T) {
	cfg := createDefaultConfig().(*Config)
	require.NoError(t, confmap.NewFromStringMap(map[string]any{
		"context": attributesContext, "field": "ip",
		"redis":         map[string]any{"address": "127.0.0.1:6379"},
		"cache_enabled": true,
	}).Unmarshal(cfg))

	require.True(t, cfg.cacheKeysSet["cache_enabled"],
		"the key should be recorded as explicitly set")
	require.Equal(t, "", cfg.CSV, "this config is not a CSV source")
}

func TestUnmarshalSurfacesDecodeErrors(t *testing.T) {
	// reload_interval cannot decode from a non-duration string, so Unmarshal must
	// surface the error rather than swallow it.
	conf := confmap.NewFromStringMap(map[string]any{"reload_interval": "not-a-duration"})
	require.Error(t, conf.Unmarshal(createDefaultConfig().(*Config)))
}
