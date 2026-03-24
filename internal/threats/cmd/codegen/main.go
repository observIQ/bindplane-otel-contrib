// Command codegen generates Go code from STIX schemas, enums, and IANA registries.
//
// Usage:
//
//	codegen <command> [flags]
//
// Commands:
//
//	enums      Generate enum constants from JSON (validator and/or matcher)
//	schemas    Generate STIX schema models
//	validator  Generate compiled validator schemas
//	iana       Generate IANA registry data (MIME types, charsets)
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/observiq/bindplane-otel-collector/internal/threats/generate/enumcodegen"
	"github.com/observiq/bindplane-otel-collector/internal/threats/generate/ianacodegen"
	codegen "github.com/observiq/bindplane-otel-collector/internal/threats/generate/schemacodegen"
	"github.com/observiq/bindplane-otel-collector/internal/threats/generate/validatorcodegen"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	os.Args = append([]string{os.Args[0]}, os.Args[2:]...)

	switch cmd {
	case "enums":
		runEnums()
	case "schemas":
		runSchemas()
	case "validator":
		runValidator()
	case "iana":
		runIANA()
	case "-h", "--help", "help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`codegen - Generate Go code from STIX schemas, enums, and IANA registries

Usage:
  codegen <command> [flags]

Commands:
  enums      Generate enum constants from JSON (validator and/or matcher)
  schemas    Generate STIX schema models
  validator  Generate compiled validator schemas
  iana       Generate IANA registry data (MIME types, charsets)

Run 'codegen <command> -h' for command-specific help.`)
}

func runEnums() {
	fs := flag.NewFlagSet("enums", flag.ExitOnError)
	in := fs.String("in", "", "Path to enums_v21.json (from scripts/dump_v21_enums.py)")
	out := fs.String("out", "internal/threats/stix/validator", "Path to output directory for enums_generated.go")
	matcherIn := fs.String("matcher-in", "", "Path to enums_matcher.json; if set, -matcher-out is required")
	matcherOut := fs.String("matcher-out", "", "Path to output directory for matcher enums_generated.go")
	fs.Parse(os.Args[1:])

	generated := false

	if *in != "" {
		inPath, err := filepath.Abs(*in)
		if err != nil {
			fatal(err)
		}
		outDir, err := filepath.Abs(*out)
		if err != nil {
			fatal(err)
		}
		if err := enumcodegen.Run(inPath, outDir); err != nil {
			fatal(err)
		}
		fmt.Printf("Generated %s/enums_generated.go from %s\n", outDir, inPath)
		generated = true
	}

	if *matcherIn != "" {
		if *matcherOut == "" {
			fatal(fmt.Errorf("-matcher-in requires -matcher-out"))
		}
		inPath, err := filepath.Abs(*matcherIn)
		if err != nil {
			fatal(err)
		}
		outDir, err := filepath.Abs(*matcherOut)
		if err != nil {
			fatal(err)
		}
		if err := enumcodegen.RunForPackage(inPath, outDir, "matcher",
			"Source: scripts/dump_matcher_enums.py from cti-pattern-matcher stix2matcher/enums.py."); err != nil {
			fatal(err)
		}
		fmt.Printf("Generated %s/enums_generated.go from %s\n", outDir, inPath)
		generated = true
	}

	if !generated {
		fatal(fmt.Errorf("must set -in (validator enums) and/or -matcher-in with -matcher-out (matcher enums)"))
	}
}

func runSchemas() {
	fs := flag.NewFlagSet("schemas", flag.ExitOnError)
	in := fs.String("in", "", "Path to JSON schemas directory (e.g. cti-stix2-json-schemas/schemas)")
	out := fs.String("out", "stix", "Path to output package directory for generated Go")
	fs.Parse(os.Args[1:])

	if *in == "" {
		fatal(fmt.Errorf("missing -in (schemas directory)"))
	}
	inDir, err := filepath.Abs(*in)
	if err != nil {
		fatal(err)
	}
	outDir, err := filepath.Abs(*out)
	if err != nil {
		fatal(err)
	}

	if err := codegen.Run(inDir, outDir); err != nil {
		fatal(err)
	}
	fmt.Printf("Generated %s from %s\n", outDir, inDir)
}

func runValidator() {
	fs := flag.NewFlagSet("validator", flag.ExitOnError)
	in := fs.String("in", "", "Path to JSON schemas directory (e.g. cti-stix2-json-schemas/schemas)")
	out := fs.String("out", "internal/threats/stix/validator", "Path to output directory for schemas_generated.go")
	fs.Parse(os.Args[1:])

	if *in == "" {
		fatal(fmt.Errorf("missing -in (schemas directory)"))
	}
	schemaDir, err := filepath.Abs(*in)
	if err != nil {
		fatal(err)
	}
	outDir, err := filepath.Abs(*out)
	if err != nil {
		fatal(err)
	}

	if err := validatorcodegen.Run(schemaDir, outDir); err != nil {
		fatal(err)
	}
	fmt.Printf("Generated %s/schemas_generated.go from %s\n", outDir, schemaDir)
}

func runIANA() {
	fs := flag.NewFlagSet("iana", flag.ExitOnError)
	assets := fs.String("assets", "cti-stix-validator/stix2validator/v21/assets", "Path to IANA CSV assets directory")
	out := fs.String("out", "stix", "Path to output package directory for iana.go")
	fs.Parse(os.Args[1:])

	assetsDir, err := filepath.Abs(*assets)
	if err != nil {
		fatal(err)
	}
	outDir, err := filepath.Abs(*out)
	if err != nil {
		fatal(err)
	}

	if err := ianacodegen.Run(assetsDir, outDir); err != nil {
		fatal(err)
	}
	fmt.Printf("Generated %s/iana.go from %s\n", outDir, assetsDir)
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "codegen: %v\n", err)
	os.Exit(1)
}
