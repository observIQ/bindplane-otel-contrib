// Command stix-validator validates STIX 2.1 JSON files. Same flags and exit
// codes as the Python stix2-validator; reads files or stdin.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/observiq/bindplane-otel-collector/internal/threats/stix/validator"
)

func main() {
	cfg, useStdin, files, outputJSON, err := parseFlags()
	if err != nil {
		fmt.Fprintf(os.Stderr, "stix-validator: %v\n", err)
		os.Exit(validator.ExitFailure)
	}
	if useStdin {
		results, err := validator.ValidateReaderWithConfig(os.Stdin, cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "stix-validator: %v\n", err)
			os.Exit(validator.ExitValidationError)
		}
		printResults(results, cfg.Options, outputJSON)
		os.Exit(validator.GetCode(results))
	}
	if len(files) == 0 {
		fmt.Fprintf(os.Stderr, "stix-validator: no JSON files found\n")
		os.Exit(validator.ExitValidationError)
	}
	results, err := validator.ValidateFilesWithConfig(files, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stix-validator: %v\n", err)
		os.Exit(validator.ExitFailure)
	}
	printResults(results, cfg.Options, outputJSON)
	os.Exit(validator.GetCode(results))
}

func parseFlags() (cfg validator.Config, useStdin bool, files []string, outputJSON bool, err error) {
	var (
		recursive   bool
		schemaDir   string
		version     string
		verbose     bool
		silent      bool
		disable     string
		enable      string
		strict      bool
		strictTypes bool
		strictProps bool
		enforceRefs bool
		interop     bool
		jsonOut     bool
	)
	flag.BoolVar(&recursive, "r", true, "recursively descend into input directories")
	flag.BoolVar(&recursive, "recursive", true, "same as -r")
	flag.StringVar(&schemaDir, "s", "", "custom schema directory")
	flag.StringVar(&schemaDir, "schemas", "", "same as -s")
	flag.StringVar(&version, "version", "2.1", "STIX version to validate against")
	flag.BoolVar(&verbose, "v", false, "verbose output")
	flag.BoolVar(&verbose, "verbose", false, "same as -v")
	flag.BoolVar(&silent, "q", false, "silent (no stdout)")
	flag.BoolVar(&silent, "silent", false, "same as -q")
	flag.StringVar(&disable, "d", "", "comma-separated list of checks to disable")
	flag.StringVar(&disable, "disable", "", "same as -d")
	flag.StringVar(&enable, "e", "", "comma-separated list of checks to enable")
	flag.StringVar(&enable, "enable", "", "same as -e")
	flag.BoolVar(&strict, "strict", false, "treat warnings as errors")
	flag.BoolVar(&strictTypes, "strict-types", false, "warn on custom object types")
	flag.BoolVar(&strictProps, "strict-properties", false, "warn on custom properties")
	flag.BoolVar(&enforceRefs, "enforce-refs", false, "ensure SDOs referenced by SROs are in same bundle")
	flag.BoolVar(&interop, "interop", false, "run with interop validation settings")
	flag.BoolVar(&jsonOut, "json", false, "output results as JSON")
	flag.BoolVar(&jsonOut, "j", false, "same as -json")
	flag.Usage = printUsage
	flag.Parse()

	if silent && verbose {
		return cfg, false, nil, false, fmt.Errorf("output can either be silent (-q) or verbose (-v), but not both")
	}

	cfg = validator.DefaultConfig()
	cfg.Options.SchemaDir = schemaDir
	cfg.Options.Version = version
	cfg.Options.Verbose = verbose
	cfg.Options.Silent = silent
	cfg.Options.Strict = strict
	cfg.Options.StrictTypes = strictTypes
	cfg.Options.StrictProperties = strictProps
	cfg.Options.EnforceRefs = enforceRefs
	cfg.Options.Interop = interop
	cfg.Options.Disabled = parseCommaList(disable)
	cfg.Options.Enabled = parseCommaList(enable)

	args := flag.Args()
	if len(args) == 0 {
		return cfg, true, nil, jsonOut, nil
	}
	files, err = expandPaths(args, recursive)
	return cfg, false, files, jsonOut, err
}

func parseCommaList(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func expandPaths(paths []string, recursive bool) ([]string, error) {
	var out []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			if os.IsNotExist(err) {
				// Treat as file path to be validated; ValidateFiles will return fatal for missing file
				if strings.HasSuffix(p, ".json") {
					out = append(out, p)
				}
				continue
			}
			return nil, err
		}
		if info.IsDir() {
			var added []string
			if recursive {
				added, err = listJSONFilesRecursive(p)
			} else {
				added, err = listJSONFilesTopLevel(p)
			}
			if err != nil {
				return nil, err
			}
			out = append(out, added...)
		} else if strings.HasSuffix(p, ".json") {
			out = append(out, p)
		}
	}
	return out, nil
}

func listJSONFilesTopLevel(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".json") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out, nil
}

func listJSONFilesRecursive(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(d.Name(), ".json") {
			out = append(out, path)
		}
		return nil
	})
	return out, err
}

func printResults(results []validator.FileResult, opts validator.Options, outputJSON bool) {
	if opts.Silent {
		return
	}
	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(results); err != nil {
			fmt.Fprintf(os.Stderr, "stix-validator: %v\n", err)
		}
		return
	}
	// Human-readable (implemented in next step)
	printHumanResults(results, opts)
}

func printHumanResults(results []validator.FileResult, opts validator.Options) {
	for _, fr := range results {
		printHorizontalRule()
		fmt.Printf("[-] Results for: %s\n", fr.Filepath)
		if fr.Result {
			fmt.Println("[+] STIX JSON: Valid")
		} else {
			fmt.Println("[X] STIX JSON: Invalid")
		}
		for _, o := range fr.ObjectResults {
			for _, w := range o.Warnings {
				fmt.Printf("    [!] Warning: %s\n", formatSchemaError(w))
			}
			for _, e := range o.Errors {
				fmt.Printf("    [X] %s\n", formatSchemaError(e))
			}
		}
		if fr.Fatal != nil {
			fmt.Printf("    [X] Fatal Error: %s\n", fr.Fatal.Message)
		}
	}
}

func formatSchemaError(e validator.SchemaError) string {
	if e.Path != "" {
		return e.Path + ": " + e.Message
	}
	return e.Message
}

func printHorizontalRule() {
	fmt.Println(strings.Repeat("=", 80))
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `Usage: stix-validator [options] [FILES...]

Validate STIX 2.1 JSON. If no FILES are given, reads from stdin.

Options:
  -r, -recursive     recursively descend into directories (default true)
  -s, -schemas       path to custom schema directory
  -version           STIX version, e.g. "2.1" (default "2.1")
  -v, -verbose       verbose output
  -q, -silent        suppress all stdout output
  -d, -disable       comma-separated checks to disable (e.g. 202,210)
  -e, -enable        comma-separated checks to enable
  -strict            treat warnings as errors
  -strict-types       warn on custom object types
  -strict-properties  warn on custom properties
  -enforce-refs       ensure SDOs referenced by SROs are in same bundle
  -interop            interop validation settings
  -j, -json          output results as JSON
  -h, -help          show this help

Exit codes: 0 success, 1 failure, 2 schema invalid, 16 (0x10) validation error.
`)
}
