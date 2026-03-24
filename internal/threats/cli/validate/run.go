// Package validate runs STIX validation (stix validate).
package validate

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/observiq/bindplane-otel-collector/internal/threats/stix/validator"
)

// Run runs the validate subcommand. args are the arguments after "stix validate".
// baseCfg is the validator config from the loaded CLI config (can be zero value).
// Flags override baseCfg. Returns exit code.
func Run(args []string, baseCfg validator.Config) int {
	cfg, useStdin, files, outputJSON, err := parseFlags(args, baseCfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stix validate: %v\n", err)
		return validator.ExitFailure
	}
	if useStdin {
		results, err := validator.ValidateReaderWithConfig(os.Stdin, cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "stix validate: %v\n", err)
			return validator.ExitValidationError
		}
		printResults(results, cfg.Options, outputJSON)
		return validator.GetCode(results)
	}
	if len(files) == 0 {
		fmt.Fprintf(os.Stderr, "stix validate: no JSON files found\n")
		return validator.ExitValidationError
	}
	results, err := validator.ValidateFilesWithConfig(files, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stix validate: %v\n", err)
		return validator.ExitFailure
	}
	printResults(results, cfg.Options, outputJSON)
	return validator.GetCode(results)
}

func parseFlags(args []string, baseCfg validator.Config) (cfg validator.Config, useStdin bool, files []string, outputJSON bool, err error) {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
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
	fs.BoolVar(&recursive, "r", true, "recursively descend into input directories")
	fs.BoolVar(&recursive, "recursive", true, "same as -r")
	fs.StringVar(&schemaDir, "s", "", "custom schema directory")
	fs.StringVar(&schemaDir, "schemas", "", "same as -s")
	fs.StringVar(&version, "version", "2.1", "STIX version to validate against")
	fs.BoolVar(&verbose, "v", false, "verbose output")
	fs.BoolVar(&verbose, "verbose", false, "same as -v")
	fs.BoolVar(&silent, "q", false, "silent (no stdout)")
	fs.BoolVar(&silent, "silent", false, "same as -q")
	fs.StringVar(&disable, "d", "", "comma-separated list of checks to disable")
	fs.StringVar(&disable, "disable", "", "same as -d")
	fs.StringVar(&enable, "e", "", "comma-separated list of checks to enable")
	fs.StringVar(&enable, "enable", "", "same as -e")
	fs.BoolVar(&strict, "strict", false, "treat warnings as errors")
	fs.BoolVar(&strictTypes, "strict-types", false, "warn on custom object types")
	fs.BoolVar(&strictProps, "strict-properties", false, "warn on custom properties")
	fs.BoolVar(&enforceRefs, "enforce-refs", false, "ensure SDOs referenced by SROs are in same bundle")
	fs.BoolVar(&interop, "interop", false, "run with interop validation settings")
	fs.BoolVar(&jsonOut, "json", false, "output results as JSON")
	fs.BoolVar(&jsonOut, "j", false, "same as -json")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, validateUsage)
	}
	if err := fs.Parse(args); err != nil {
		return cfg, false, nil, false, err
	}

	if silent && verbose {
		return cfg, false, nil, false, fmt.Errorf("output can either be silent (-q) or verbose (-v), but not both")
	}

	cfg = baseCfg
	if schemaDir != "" {
		cfg.Options.SchemaDir = schemaDir
	}
	if version != "" {
		cfg.Options.Version = version
	}
	cfg.Options.Verbose = cfg.Options.Verbose || verbose
	cfg.Options.Silent = cfg.Options.Silent || silent
	cfg.Options.Strict = cfg.Options.Strict || strict
	cfg.Options.StrictTypes = cfg.Options.StrictTypes || strictTypes
	cfg.Options.StrictProperties = cfg.Options.StrictProperties || strictProps
	cfg.Options.EnforceRefs = cfg.Options.EnforceRefs || enforceRefs
	cfg.Options.Interop = cfg.Options.Interop || interop
	if disable != "" {
		cfg.Options.Disabled = parseCommaList(disable)
	}
	if enable != "" {
		cfg.Options.Enabled = parseCommaList(enable)
	}

	rest := fs.Args()
	if len(rest) == 0 {
		return cfg, true, nil, jsonOut, nil
	}
	files, err = expandPaths(rest, recursive)
	return cfg, false, files, jsonOut, err
}

const validateUsage = `Usage: stix validate [options] [FILES...]

Validate STIX 2.1 JSON. If no FILES are given, reads from stdin.

Options:
  -r, -recursive       recursively descend into directories (default true)
  -s, -schemas         path to custom schema directory
  -version             STIX version, e.g. "2.1" (default "2.1")
  -v, -verbose         verbose output
  -q, -silent          suppress all stdout output
  -d, -disable         comma-separated checks to disable (e.g. 202,210)
  -e, -enable          comma-separated checks to enable
  -strict              treat warnings as errors
  -strict-types        warn on custom object types
  -strict-properties   warn on custom properties
  -enforce-refs        ensure SDOs referenced by SROs are in same bundle
  -interop             interop validation settings
  -j, -json            output results as JSON
  -h, -help            show this help

Exit codes: 0 success, 1 failure, 2 schema invalid, 16 (0x10) validation error.
`

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
			if os.IsNotExist(err) && strings.HasSuffix(p, ".json") {
				out = append(out, p)
			}
			continue
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
		_ = enc.Encode(results)
		return
	}
	for _, fr := range results {
		fmt.Println(strings.Repeat("=", 80))
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
