// Command exchange is a CLI for STIX exchanges (e.g. OTX). It loads OTX_API_KEY
// from the environment or from a .env file in the current directory.
//
//	exchange user              - show current OTX user
//	exchange pulses [--limit N] [--stix] - list subscribed pulses; with --stix fetch each and output STIX bundle(s)
//	exchange pulse <id> [--stix] - fetch a pulse, optionally output as STIX bundle
//
// Set OTX_API_KEY or add OTX_API_KEY=... to .env in the current directory.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/observiq/bindplane-otel-collector/exchange/otx"
)

const envVarOTXAPIKey = "OTX_API_KEY"

func main() {
	loadEnvFromFile(".")
	key := os.Getenv(envVarOTXAPIKey)
	if key == "" {
		fmt.Fprintf(os.Stderr, "exchange: %s not set (set it or add to .env)\n", envVarOTXAPIKey)
		os.Exit(1)
	}

	client := otx.NewClientWithAPIKey(key)
	ctx := context.Background()

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}
	cmd := strings.ToLower(os.Args[1])
	args := os.Args[2:]

	switch cmd {
	case "user":
		runUser(ctx, client, args)
	case "pulses":
		runPulses(ctx, client, args)
	case "pulse":
		runPulse(ctx, client, args)
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "exchange: unknown command %q\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprint(os.Stderr, `usage: exchange <command> [options] [args]

commands:
  user                  show current OTX user
  pulses [--limit N] [--stix]  list subscribed pulses; with --stix output STIX 2.1 bundle JSON (one per pulse, NDJSON)
  pulse <id> [--stix]   fetch pulse by ID; with --stix output STIX 2.1 bundle JSON

environment:
  OTX_API_KEY           API key (or set in .env in current directory)
`)
}

func loadEnvFromFile(dir string) {
	if os.Getenv(envVarOTXAPIKey) != "" {
		return
	}
	f, err := os.Open(filepath.Join(dir, ".env"))
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, envVarOTXAPIKey+"=") {
			val := strings.TrimPrefix(line, envVarOTXAPIKey+"=")
			val = strings.Trim(val, "\"' \t")
			_ = os.Setenv(envVarOTXAPIKey, val)
			return
		}
	}
}

func runUser(ctx context.Context, client *otx.Client, args []string) {
	fs := flag.NewFlagSet("user", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "output as JSON")
	_ = fs.Parse(args)

	u, err := client.GetUser(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "exchange user: %v\n", err)
		os.Exit(1)
	}
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(u)
		return
	}
	fmt.Printf("username: %s\n", u.Username)
	if u.Email != "" {
		fmt.Printf("email:    %s\n", u.Email)
	}
}

func runPulses(ctx context.Context, client *otx.Client, args []string) {
	fs := flag.NewFlagSet("pulses", flag.ExitOnError)
	limit := fs.Int("limit", 20, "max number of pulses")
	jsonOut := fs.Bool("json", false, "output as JSON")
	stixOut := fs.Bool("stix", false, "fetch each pulse and output as STIX 2.1 bundle JSON (one per line)")
	_ = fs.Parse(args)

	list, err := client.GetSubscribedPulses(ctx, &otx.ListOptions{Limit: *limit})
	if err != nil {
		fmt.Fprintf(os.Stderr, "exchange pulses: %v\n", err)
		os.Exit(1)
	}
	if *stixOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		for _, p := range list.Results {
			if p == nil {
				continue
			}
			full, err := client.GetPulse(ctx, p.ID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "exchange pulses: get pulse %s: %v\n", p.ID, err)
				os.Exit(1)
			}
			bundle, err := client.ToSTIX(full)
			if err != nil {
				fmt.Fprintf(os.Stderr, "exchange pulses: to STIX %s: %v\n", p.ID, err)
				os.Exit(1)
			}
			_ = enc.Encode(bundle)
		}
		return
	}
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(list)
		return
	}
	fmt.Printf("count: %d\n", list.Count)
	for i, p := range list.Results {
		if p == nil {
			continue
		}
		fmt.Printf("  %d. %s (id: %s)\n", i+1, p.Name, p.ID)
	}
}

func runPulse(ctx context.Context, client *otx.Client, args []string) {
	fs := flag.NewFlagSet("pulse", flag.ExitOnError)
	stixOut := fs.Bool("stix", false, "output as STIX 2.1 bundle JSON")
	_ = fs.Parse(args)
	if fs.NArg() == 0 {
		fmt.Fprintf(os.Stderr, "exchange pulse: requires pulse ID\n")
		os.Exit(1)
	}
	pulseID := fs.Arg(0)

	p, err := client.GetPulse(ctx, pulseID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "exchange pulse: %v\n", err)
		os.Exit(1)
	}

	if *stixOut {
		bundle, err := client.ToSTIX(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "exchange pulse --stix: %v\n", err)
			os.Exit(1)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(bundle)
		return
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(p)
}
