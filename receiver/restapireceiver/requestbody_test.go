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

package restapireceiver

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// postConfig builds a minimal POST config carrying the given template.
func postConfig(body string) *Config {
	return &Config{
		URL:         "https://api.example.com/alerts",
		Method:      methodPOST,
		AuthMode:    authModeNone,
		RequestBody: body,
		Pagination: PaginationConfig{
			Mode: paginationModeOffsetLimit,
			OffsetLimit: OffsetLimitPagination{
				Limit:               100,
				NextOffsetFieldName: "meta.after",
			},
		},
	}
}

// TestValidateRequestBodyTemplate is the guardrail that makes templating safe to
// hand to users: every way a template can be wrong must surface at startup
// rather than on every poll.
func TestValidateRequestBodyTemplate(t *testing.T) {
	testCases := []struct {
		name        string
		body        string
		expectedErr string
	}{
		{
			name: "empty template is allowed",
			body: "",
		},
		{
			name: "static body",
			body: `{"filter": "status:'new'"}`,
		},
		{
			name: "cursor guarded for the first request",
			body: `{"limit": {{ .Limit }}{{ if .Cursor }}, "after": {{ json .Cursor }}{{ end }}}`,
		},
		{
			name: "nested and array shapes",
			body: `{"meta":{"pagination":{"pageSize":{{ .PageSize }}}},"data":[{"start":{{ json .StartTime }}}]}`,
		},
		{
			name:        "unparseable template",
			body:        `{"limit": {{ .Limit }`,
			expectedErr: "request_body template is invalid",
		},
		{
			name:        "misspelled field",
			body:        `{"after": "{{ .Cursr }}"}`,
			expectedErr: "request_body template failed to render",
		},
		{
			name:        "not JSON at all",
			body:        `limit={{ .Limit }}`,
			expectedErr: "request_body must render to valid JSON",
		},
		{
			name: "dangling comma only on the first request",
			// The classic templating mistake: the comma belongs inside the guard,
			// so the first request renders {"limit": 100,} and only the
			// continuation request is valid.
			body:        `{"limit": {{ .Limit }},{{ if .Cursor }} "after": {{ json .Cursor }}{{ end }}}`,
			expectedErr: "the first request rendered",
		},
		{
			name: "dangling comma only on the continuation request",
			// The mirror image: valid on the first request, broken once the
			// cursor appears. Rendering only one sample would miss it.
			body:        `{"limit": {{ .Limit }}{{ if .Cursor }}, "after": {{ json .Cursor }},{{ end }}}`,
			expectedErr: "the continuation request rendered",
		},
		{
			name: "hand-quoted cursor is rejected",
			// The sample cursor contains a quote, so a template that quotes the
			// value itself instead of using {{ json . }} fails at startup rather
			// than on the first real token containing one.
			body:        `{"after": "{{ .Cursor }}"}`,
			expectedErr: "the continuation request rendered",
		},
		{
			name:        "bare string field is rejected",
			body:        `{"after": {{ .Cursor }}}`,
			expectedErr: "request_body must render to valid JSON",
		},
		{
			name: "json helper on a numeric field",
			body: `{"limit": {{ json .Limit }}}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRequestBodyTemplate(postConfig(tc.body))
			if tc.expectedErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.expectedErr)
		})
	}
}

func TestRenderRequestBody(t *testing.T) {
	render := func(t *testing.T, body, cursor string) []byte {
		t.Helper()
		cfg := postConfig(body)
		tmpl, err := parseRequestBodyTemplate(cfg.RequestBody)
		require.NoError(t, err)

		state := newPaginationState(cfg)
		state.CurrentOffsetToken = cursor

		rendered, err := renderRequestBody(tmpl, newRequestBodyData(cfg, state))
		require.NoError(t, err)
		return rendered
	}

	t.Run("json quotes strings, bare renders numbers", func(t *testing.T) {
		body := render(t, `{"limit": {{ .Limit }}, "after": {{ json .Cursor }}}`, "0012345")
		// limit is a bare number; a digit-only cursor stays a quoted string.
		require.JSONEq(t, `{"limit":100,"after":"0012345"}`, string(body))
		require.Contains(t, string(body), `"after": "0012345"`)
	})

	t.Run("json escapes quotes and backslashes", func(t *testing.T) {
		body := render(t, `{"after": {{ json .Cursor }}}`, `tok"en\with/specials`)

		var decoded map[string]any
		require.NoError(t, json.Unmarshal(body, &decoded))
		require.Equal(t, `tok"en\with/specials`, decoded["after"])
	})

	t.Run("json does not escape HTML characters", func(t *testing.T) {
		// A cursor is opaque and may contain these; they must survive literally
		// rather than becoming \u003c and friends.
		body := render(t, `{"after": {{ json .Cursor }}}`, "a<b>c&d")
		require.Contains(t, string(body), "a<b>c&d")
		require.NotContains(t, string(body), `\u003c`)
	})

	t.Run("values reach the template unescaped", func(t *testing.T) {
		// The data carries the raw token; escaping happens only where json is called.
		cfg := postConfig(`{"after": {{ json .Cursor }}}`)
		state := newPaginationState(cfg)
		state.CurrentOffsetToken = `raw"token`
		require.Equal(t, `raw"token`, newRequestBodyData(cfg, state).Cursor)
	})
}
