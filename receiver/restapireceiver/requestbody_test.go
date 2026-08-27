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
			body: `{"limit": {{ .Limit }}{{ if .Cursor }}, "after": "{{ .Cursor }}"{{ end }}}`,
		},
		{
			name: "nested and array shapes",
			body: `{"meta":{"pagination":{"pageSize":{{ .PageSize }}}},"data":[{"start":"{{ .StartTime }}"}]}`,
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
			body:        `{"limit": {{ .Limit }},{{ if .Cursor }} "after": "{{ .Cursor }}"{{ end }}}`,
			expectedErr: "the first request rendered",
		},
		{
			name: "dangling comma only on the continuation request",
			// The mirror image: valid on the first request, broken once the
			// cursor appears. Rendering only one sample would miss it.
			body:        `{"limit": {{ .Limit }}{{ if .Cursor }}, "after": "{{ .Cursor }}",{{ end }}}`,
			expectedErr: "the continuation request rendered",
		},
		{
			name:        "unquoted string value",
			body:        `{"after": {{ .Cursor }}}`,
			expectedErr: "request_body must render to valid JSON",
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
	t.Run("quoting decides the JSON type", func(t *testing.T) {
		cfg := postConfig(`{"limit": {{ .Limit }}, "after": "{{ .Cursor }}"}`)
		tmpl, err := parseRequestBodyTemplate(cfg.RequestBody)
		require.NoError(t, err)

		state := newPaginationState(cfg)
		state.CurrentOffsetToken = "0012345"

		body, err := renderRequestBody(tmpl, newRequestBodyData(cfg, state))
		require.NoError(t, err)
		// limit is a bare number; a digit-only cursor stays a string.
		require.JSONEq(t, `{"limit":100,"after":"0012345"}`, string(body))
		require.Contains(t, string(body), `"after": "0012345"`)
	})

	t.Run("a cursor containing quotes stays valid JSON", func(t *testing.T) {
		cfg := postConfig(`{"after": "{{ .Cursor }}"}`)
		tmpl, err := parseRequestBodyTemplate(cfg.RequestBody)
		require.NoError(t, err)

		state := newPaginationState(cfg)
		state.CurrentOffsetToken = `tok"en\with/specials`

		body, err := renderRequestBody(tmpl, newRequestBodyData(cfg, state))
		require.NoError(t, err)

		var decoded map[string]any
		require.NoError(t, json.Unmarshal(body, &decoded))
		require.Equal(t, `tok"en\with/specials`, decoded["after"])
	})
}
