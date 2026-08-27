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

package restapireceiver // import "github.com/observiq/bindplane-otel-contrib/receiver/restapireceiver"

import (
	"bytes"
	"encoding/json"
	"fmt"
	"text/template"
)

// requestBodyTemplateName labels the template in parse and execute errors.
const requestBodyTemplateName = "request_body"

// sampleCursor stands in for a real pagination token when Validate renders the
// template to check the continuation request. Its value is arbitrary; only its
// non-emptiness matters, since that is what a {{ if .Cursor }} guard tests.
const sampleCursor = "sample-cursor"

// parseRequestBodyTemplate parses a request_body template. The template renders
// against requestBodyData, so a reference to a field that does not exist fails
// when it is executed rather than here.
func parseRequestBodyTemplate(body string) (*template.Template, error) {
	return template.New(requestBodyTemplateName).Parse(body)
}

// renderRequestBody renders the template for one request.
func renderRequestBody(tmpl *template.Template, data requestBodyData) ([]byte, error) {
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// validateRequestBodyTemplate parses the configured template and renders it
// twice — once as the first request of a run and once as a continuation
// request — requiring valid JSON both times.
//
// Rendering both cases is what makes templating safe to hand to users: it
// catches the two mistakes the syntax actually invites, at startup rather than
// on every poll. A dangling comma inside a {{ if .Cursor }} guard only produces
// invalid JSON in one of the two cases, and an unquoted value that is not a
// legal JSON number only shows up once rendered. Because requestBodyData is a
// struct, a misspelled field such as {{ .Cursr }} also fails here.
func validateRequestBodyTemplate(cfg *Config) error {
	if cfg.RequestBody == "" {
		return nil
	}

	tmpl, err := parseRequestBodyTemplate(cfg.RequestBody)
	if err != nil {
		return fmt.Errorf("request_body template is invalid: %w", err)
	}

	firstPage := newPaginationState(cfg)

	// A continuation request: a cursor is in hand and the offset and page have
	// advanced past the first page.
	continuation := *firstPage
	continuation.CurrentOffsetToken = sampleCursor
	continuation.CurrentOffset += continuation.Limit
	continuation.CurrentPage++
	continuation.PagesFetched = 1

	for _, sample := range []struct {
		label string
		state *paginationState
	}{
		{"first", firstPage},
		{"continuation", &continuation},
	} {
		rendered, err := renderRequestBody(tmpl, newRequestBodyData(cfg, sample.state))
		if err != nil {
			return fmt.Errorf("request_body template failed to render the %s request: %w", sample.label, err)
		}
		if !json.Valid(rendered) {
			return fmt.Errorf("request_body must render to valid JSON, but the %s request rendered: %s", sample.label, string(rendered))
		}
	}

	return nil
}
