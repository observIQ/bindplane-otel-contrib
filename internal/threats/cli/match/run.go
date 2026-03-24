// Package match runs the STIX pattern matcher (stix match).
package match

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/observiq/bindplane-otel-collector/internal/threats/stix/matcher"
)

// Run runs the match subcommand. args are the arguments after "stix match".
// verboseFromConfig is from config.
func Run(args []string, verboseFromConfig bool) int {
	fs := flag.NewFlagSet("match", flag.ExitOnError)
	pattern := fs.String("pattern", "", "STIX 2.1 pattern to match")
	input := fs.String("input", "", "path to JSON file (bundle or array of observed-data); default stdin")
	jsonOut := fs.Bool("json", false, "output result as JSON")
	verbose := fs.Bool("verbose", false, "verbose output")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Usage: stix match -pattern "<pattern>" [-input file.json] [-json] [-verbose]

Match a STIX 2.1 pattern against observed-data. Input is a JSON bundle or array of
observed-data SDOs (from file or stdin). Exits 0 if matched, 1 if not matched or error.

Options:
  -pattern string   STIX 2.1 pattern (e.g. "[file:name = 'x']")
  -input string    path to JSON file; default stdin
  -json            output match result as JSON
  -verbose         verbose output
  -h, -help        show this help
`)
	}
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *pattern == "" {
		fmt.Fprintf(os.Stderr, "stix match: -pattern required\n")
		return 1
	}

	var r io.Reader
	if *input != "" {
		f, err := os.Open(*input)
		if err != nil {
			fmt.Fprintf(os.Stderr, "stix match: %v\n", err)
			return 1
		}
		defer f.Close()
		r = f
	} else {
		r = os.Stdin
	}

	data, err := io.ReadAll(r)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stix match: %v\n", err)
		return 1
	}

	observedData, err := parseObservedData(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stix match: %v\n", err)
		return 1
	}

	opts := matcher.DefaultMatchOptions()
	opts.Verbose = verboseFromConfig || *verbose

	result, err := matcher.Match(*pattern, observedData, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stix match: %v\n", err)
		return 1
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
	} else {
		if result.Matched {
			fmt.Println("matched")
			for i, sdo := range result.SDOs {
				if id, _ := sdo["id"].(string); id != "" {
					fmt.Printf("  %d. %s\n", i+1, id)
				}
			}
		} else {
			fmt.Println("no match")
		}
	}

	if result.Matched {
		return 0
	}
	return 1
}

// parseObservedData decodes JSON as either a bundle (objects array) or an array of objects.
func parseObservedData(data []byte) ([]map[string]interface{}, error) {
	var raw json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	// Try as bundle first
	var bundle struct {
		Objects []map[string]interface{} `json:"objects"`
	}
	if err := json.Unmarshal(raw, &bundle); err == nil && len(bundle.Objects) > 0 {
		return bundle.Objects, nil
	}
	// Try as array
	var arr []map[string]interface{}
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr, nil
	}
	return nil, fmt.Errorf("input must be a JSON bundle with \"objects\" or an array of objects")
}
