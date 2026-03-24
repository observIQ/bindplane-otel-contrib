// Package exchange runs exchange subcommands: list domains, get <exchange> <opts> (e.g. get otx pulses).
package exchange

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/observiq/bindplane-otel-collector/exchange/abuseipdb"
	"github.com/observiq/bindplane-otel-collector/exchange/otx"
)

const envVarOTXAPIKey = "OTX_API_KEY"
const envVarAbuseIPDBAPIKey = "ABUSEIPDB_API_KEY"

// Known exchange domain names (used by list and get).
var KnownExchanges = []string{"otx", "abuseipdb"}

// Run runs the exchange subcommand. args are the arguments after "stix exchange".
// First arg must be "list" or "get". For "get", next arg is exchange name (e.g. otx), then exchange-specific opts.
// apiKeyFromConfig can be from config; if empty, env and .env are used for OTX.
func Run(ctx context.Context, args []string, apiKeyFromConfig string) int {
	if len(args) == 0 {
		PrintUsage()
		return 1
	}
	cmd := strings.ToLower(args[0])
	rest := args[1:]

	switch cmd {
	case "list":
		return runList()
	case "get":
		if len(rest) == 0 {
			fmt.Fprintf(os.Stderr, "stix exchange get: requires exchange name (e.g. otx)\n")
			PrintUsage()
			return 1
		}
		exchangeName := strings.ToLower(rest[0])
		exchangeArgs := rest[1:]
		return runGet(ctx, exchangeName, exchangeArgs, apiKeyFromConfig)
	default:
		fmt.Fprintf(os.Stderr, "stix exchange: unknown command %q (use list or get)\n", cmd)
		PrintUsage()
		return 1
	}
}

func runList() int {
	fmt.Println("Available exchanges:")
	for _, name := range KnownExchanges {
		fmt.Printf("  %s\n", name)
	}
	return 0
}

func runGet(ctx context.Context, exchangeName string, exchangeArgs []string, apiKeyFromConfig string) int {
	switch exchangeName {
	case "otx":
		return runOTX(ctx, exchangeArgs, apiKeyFromConfig)
	case "abuseipdb":
		return runAbuseIPDB(ctx, exchangeArgs, apiKeyFromConfig)
	default:
		fmt.Fprintf(os.Stderr, "stix exchange get: unknown exchange %q (use stix exchange list)\n", exchangeName)
		return 1
	}
}

// runOTX handles "stix exchange get otx <subcommand> [args]". Subcommand: user, pulses, pulse <id>.
func runOTX(ctx context.Context, args []string, apiKeyFromConfig string) int {
	loadEnvFromFile(".")
	key := apiKeyFromConfig
	if key == "" {
		key = os.Getenv(envVarOTXAPIKey)
	}
	if key == "" {
		fmt.Fprintf(os.Stderr, "stix exchange get otx: %s not set (set it, add to .env, or set in config)\n", envVarOTXAPIKey)
		return 1
	}

	client := otx.NewClientWithAPIKey(key)

	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "stix exchange get otx: requires subcommand (user, pulses, pulse <id>)\n")
		printOTXUsage()
		return 1
	}
	subcmd := strings.ToLower(args[0])
	rest := args[1:]

	switch subcmd {
	case "user":
		return runUser(ctx, client, rest)
	case "pulses":
		return runPulses(ctx, client, rest)
	case "pulse":
		return runPulse(ctx, client, rest)
	default:
		fmt.Fprintf(os.Stderr, "stix exchange get otx: unknown subcommand %q\n", subcmd)
		printOTXUsage()
		return 1
	}
}

func PrintUsage() {
	fmt.Fprint(os.Stderr, `usage: stix exchange <command> [options] [args]

commands:
  list                      list available exchange domains (e.g. otx, abuseipdb)
  get <exchange> <opts>     get data from an exchange (e.g. stix exchange get otx pulses)

examples:
  stix exchange list
  stix exchange get otx user
  stix exchange get otx pulses [--limit N] [--stix]
  stix exchange get otx pulse <id> [--stix]
  stix exchange get abuseipdb check <ip> [--stix] [--json]
  stix exchange get abuseipdb blacklist [--confidence-min N] [--stix] [--json]

environment (for otx):
  OTX_API_KEY               API key (or set in .env or config)

environment (for abuseipdb):
  ABUSEIPDB_API_KEY         API key (or set in .env or config)
`)
}

func printOTXUsage() {
	fmt.Fprint(os.Stderr, `stix exchange get otx: user | pulses [--limit N] [--stix] | pulse <id> [--stix]
`)
}

func loadEnvAbuseIPDB(dir string) {
	if os.Getenv(envVarAbuseIPDBAPIKey) != "" {
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
		if strings.HasPrefix(line, envVarAbuseIPDBAPIKey+"=") {
			val := strings.TrimPrefix(line, envVarAbuseIPDBAPIKey+"=")
			val = strings.Trim(val, "\"' \t")
			_ = os.Setenv(envVarAbuseIPDBAPIKey, val)
			return
		}
	}
}

// runAbuseIPDB handles "stix exchange get abuseipdb <subcommand> [args]". Subcommand: check <ip>, blacklist.
func runAbuseIPDB(ctx context.Context, args []string, apiKeyFromConfig string) int {
	loadEnvAbuseIPDB(".")
	key := apiKeyFromConfig
	if key == "" {
		key = os.Getenv(envVarAbuseIPDBAPIKey)
	}
	if key == "" {
		fmt.Fprintf(os.Stderr, "stix exchange get abuseipdb: %s not set (set it, add to .env, or set in config)\n", envVarAbuseIPDBAPIKey)
		return 1
	}
	client := abuseipdb.NewClientWithAPIKey(key)

	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "stix exchange get abuseipdb: requires subcommand (check <ip> | blacklist)\n")
		printAbuseIPDBUsage()
		return 1
	}
	subcmd := strings.ToLower(args[0])
	rest := args[1:]

	switch subcmd {
	case "check":
		return runAbuseIPDBCheck(ctx, client, rest)
	case "blacklist":
		return runAbuseIPDBBlacklist(ctx, client, rest)
	default:
		fmt.Fprintf(os.Stderr, "stix exchange get abuseipdb: unknown subcommand %q\n", subcmd)
		printAbuseIPDBUsage()
		return 1
	}
}

func printAbuseIPDBUsage() {
	fmt.Fprint(os.Stderr, `stix exchange get abuseipdb: check <ip> [--max-age-days N] [--verbose] [--stix] [--json] | blacklist [--confidence-min N] [--limit N] [--stix] [--json]
`)
}

func runAbuseIPDBCheck(ctx context.Context, client *abuseipdb.Client, args []string) int {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	maxAgeDays := fs.Int("max-age-days", 0, "only reports within this many days (default 30)")
	verbose := fs.Bool("verbose", false, "include reports in response")
	stixOut := fs.Bool("stix", false, "output as STIX 2.1 bundle JSON")
	jsonOut := fs.Bool("json", false, "output raw API response as JSON")
	_ = fs.Parse(args)
	if fs.NArg() == 0 {
		fmt.Fprintf(os.Stderr, "stix exchange get abuseipdb check: requires IP address\n")
		printAbuseIPDBUsage()
		return 1
	}
	ipAddress := fs.Arg(0)

	opts := &abuseipdb.CheckOptions{MaxAgeInDays: *maxAgeDays, Verbose: *verbose}
	data, err := client.GetCheck(ctx, ipAddress, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stix exchange get abuseipdb check: %v\n", err)
		return 1
	}

	if *stixOut {
		bundle, err := client.ToSTIXCheck(data)
		if err != nil {
			fmt.Fprintf(os.Stderr, "stix exchange get abuseipdb check --stix: %v\n", err)
			return 1
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(bundle)
		return 0
	}
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(data)
		return 0
	}
	fmt.Printf("IP:             %s\n", data.IpAddress)
	fmt.Printf("Abuse score:    %d%%\n", data.AbuseConfidenceScore)
	fmt.Printf("Country:       %s (%s)\n", data.CountryCode, data.CountryName)
	fmt.Printf("ISP:           %s\n", data.Isp)
	fmt.Printf("Total reports:  %d\n", data.TotalReports)
	fmt.Printf("Last reported: %s\n", data.LastReportedAt)
	return 0
}

func runAbuseIPDBBlacklist(ctx context.Context, client *abuseipdb.Client, args []string) int {
	fs := flag.NewFlagSet("blacklist", flag.ExitOnError)
	confidenceMin := fs.Int("confidence-min", 90, "minimum abuse confidence (25-100)")
	limit := fs.Int("limit", 0, "max entries to return")
	stixOut := fs.Bool("stix", false, "output as STIX 2.1 bundle JSON")
	jsonOut := fs.Bool("json", false, "output raw API response as JSON")
	_ = fs.Parse(args)

	opts := &abuseipdb.BlacklistOptions{ConfidenceMinimum: *confidenceMin}
	if *limit > 0 {
		opts.Limit = *limit
	}
	resp, err := client.GetBlacklist(ctx, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stix exchange get abuseipdb blacklist: %v\n", err)
		return 1
	}

	if *stixOut {
		bundle, err := client.ToSTIXBlacklist(resp)
		if err != nil {
			fmt.Fprintf(os.Stderr, "stix exchange get abuseipdb blacklist --stix: %v\n", err)
			return 1
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(bundle)
		return 0
	}
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(resp)
		return 0
	}
	fmt.Printf("generated: %s\n", resp.Meta.GeneratedAt)
	fmt.Printf("count:     %d\n", len(resp.Data))
	for i, e := range resp.Data {
		if i >= 20 {
			fmt.Printf("  ... and %d more\n", len(resp.Data)-20)
			break
		}
		fmt.Printf("  %s  (confidence %d%%)\n", e.IpAddress, e.AbuseConfidenceScore)
	}
	return 0
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

func runUser(ctx context.Context, client *otx.Client, args []string) int {
	fs := flag.NewFlagSet("user", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "output as JSON")
	_ = fs.Parse(args)

	u, err := client.GetUser(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stix exchange get otx user: %v\n", err)
		return 1
	}
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(u)
		return 0
	}
	fmt.Printf("username: %s\n", u.Username)
	if u.Email != "" {
		fmt.Printf("email:    %s\n", u.Email)
	}
	return 0
}

func runPulses(ctx context.Context, client *otx.Client, args []string) int {
	fs := flag.NewFlagSet("pulses", flag.ExitOnError)
	limit := fs.Int("limit", 20, "max number of pulses")
	jsonOut := fs.Bool("json", false, "output as JSON")
	stixOut := fs.Bool("stix", false, "fetch each pulse and output as STIX 2.1 bundle JSON (one per line)")
	_ = fs.Parse(args)

	list, err := client.GetSubscribedPulses(ctx, &otx.ListOptions{Limit: *limit})
	if err != nil {
		fmt.Fprintf(os.Stderr, "stix exchange get otx pulses: %v\n", err)
		return 1
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
				fmt.Fprintf(os.Stderr, "stix exchange get otx pulses: get pulse %s: %v\n", p.ID, err)
				return 1
			}
			bundle, err := client.ToSTIX(full)
			if err != nil {
				fmt.Fprintf(os.Stderr, "stix exchange get otx pulses: to STIX %s: %v\n", p.ID, err)
				return 1
			}
			_ = enc.Encode(bundle)
		}
		return 0
	}
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(list)
		return 0
	}
	fmt.Printf("count: %d\n", list.Count)
	for i, p := range list.Results {
		if p == nil {
			continue
		}
		fmt.Printf("  %d. %s (id: %s)\n", i+1, p.Name, p.ID)
	}
	return 0
}

func runPulse(ctx context.Context, client *otx.Client, args []string) int {
	fs := flag.NewFlagSet("pulse", flag.ExitOnError)
	stixOut := fs.Bool("stix", false, "output as STIX 2.1 bundle JSON")
	_ = fs.Parse(args)
	if fs.NArg() == 0 {
		fmt.Fprintf(os.Stderr, "stix exchange get otx pulse: requires pulse ID\n")
		return 1
	}
	pulseID := fs.Arg(0)

	p, err := client.GetPulse(ctx, pulseID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stix exchange get otx pulse: %v\n", err)
		return 1
	}

	if *stixOut {
		bundle, err := client.ToSTIX(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "stix exchange get otx pulse --stix: %v\n", err)
			return 1
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(bundle)
		return 0
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(p)
	return 0
}
