// Command stix is the unified STIX CLI: TAXII server/client, validate, match, exchange.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/observiq/bindplane-otel-collector/internal/threats/cli/exchange"
	"github.com/observiq/bindplane-otel-collector/internal/threats/cli/filter"
	"github.com/observiq/bindplane-otel-collector/internal/threats/cli/match"
	"github.com/observiq/bindplane-otel-collector/internal/threats/cli/taxiiclient"
	"github.com/observiq/bindplane-otel-collector/internal/threats/cli/taxiiserver"
	"github.com/observiq/bindplane-otel-collector/internal/threats/cli/validate"
	config "github.com/observiq/bindplane-otel-collector/internal/threats"
)

func main() {
	fs := flag.NewFlagSet("stix", flag.ExitOnError)
	configPath := fs.String("config", "", "path to base config file (or set STIX_CONFIG)")
	configOverride := fs.String("config-override", "", "path to override config file (or set STIX_CONFIG_OVERRIDE)")
	var showHelp bool
	fs.BoolVar(&showHelp, "help", false, "show help")
	fs.BoolVar(&showHelp, "h", false, "show help")
	fs.Usage = printUsage
	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(1)
	}
	args := fs.Args()

	if showHelp {
		printUsage()
		os.Exit(0)
	}
	if len(args) == 0 {
		printUsage()
		os.Exit(1)
	}

	// Load config (optional)
	opts := config.DefaultLoadOptions()
	if *configPath != "" {
		opts.BasePath = *configPath
	}
	if *configOverride != "" {
		opts.OverridePath = *configOverride
	}
	cfg, err := config.Load(context.Background(), opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stix: config: %v\n", err)
		os.Exit(1)
	}

	cmd := strings.ToLower(args[0])
	rest := args[1:]

	switch cmd {
	case "taxii":
		runTaxii(cfg, rest)
	case "validate":
		code := validate.Run(rest, cfg.ValidatorConfig())
		os.Exit(code)
	case "match":
		code := match.Run(rest, cfg.Match.Verbose)
		os.Exit(code)
	case "exchange":
		runExchange(cfg, rest)
	case "filter":
		runFilter(rest)
	case "help", "-h", "--help":
		printUsage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "stix: unknown command %q\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func runTaxii(cfg *config.Config, args []string) {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "stix taxii: requires subcommand (server, client)\n")
		printTaxiiUsage()
		os.Exit(1)
	}
	subcmd := strings.ToLower(args[0])
	rest := args[1:]

	switch subcmd {
	case "server":
		runTaxiiServer(cfg, rest)
	case "client":
		code := taxiiclient.Run(context.Background(), rest, taxiiClientOptions(cfg))
		os.Exit(code)
	case "help", "-h", "--help":
		printTaxiiUsage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "stix taxii: unknown subcommand %q\n", subcmd)
		printTaxiiUsage()
		os.Exit(1)
	}
}

func runTaxiiServer(cfg *config.Config, args []string) {
	fs := flag.NewFlagSet("taxii-server", flag.ExitOnError)
	s := &cfg.TaxiiServer
	addr := fs.String("addr", s.Addr, "listen address")
	base := fs.String("base", s.Base, "URL base path")
	maxPageSize := fs.Int("max-page-size", s.MaxPageSize, "maximum objects per page")
	users := fs.String("users", s.Users, "comma-separated user:password")
	usersFile := fs.String("users-file", s.UsersFile, "path to file with user:password per line")
	dataFile := fs.String("data", s.DataFile, "path to JSON file for initial data")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Usage: stix taxii server [options]

Run a TAXII 2.1 server. Options override config.

  -addr, -base, -max-page-size, -users, -users-file, -data
  -h, -help
`)
	}
	_ = fs.Parse(args)

	opts := taxiiserver.Options{
		Addr:        *addr,
		Base:        *base,
		MaxPageSize: *maxPageSize,
		Users:       *users,
		UsersFile:   *usersFile,
		DataFile:    *dataFile,
	}
	if opts.MaxPageSize <= 0 {
		opts.MaxPageSize = 10000
	}
	if err := taxiiserver.Run(opts); err != nil {
		fmt.Fprintf(os.Stderr, "stix taxii server: %v\n", err)
		os.Exit(1)
	}
}

func taxiiClientOptions(cfg *config.Config) taxiiclient.Options {
	return taxiiclient.Options{
		DiscoveryURL: cfg.TaxiiClient.DiscoveryURL,
		Username:     cfg.TaxiiClient.Username,
		Password:     cfg.TaxiiClient.Password,
	}
}

func runExchange(cfg *config.Config, args []string) {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "stix exchange: requires command (list or get <exchange> <opts>)\n")
		exchange.PrintUsage()
		os.Exit(1)
	}
	code := exchange.Run(context.Background(), args, cfg.Exchange.APIKey)
	os.Exit(code)
}

func runFilter(args []string) {
	if len(args) == 0 {
		filter.PrintUsage()
		os.Exit(1)
	}
	code := filter.Run(args)
	os.Exit(code)
}

func printUsage() {
	fmt.Fprint(os.Stderr, `Usage: stix [options] <command> [args]

Commands:
  taxii server     run a TAXII 2.1 server
  taxii client     TAXII client (discovery, collections, objects)
  validate         validate STIX 2.1 JSON (files or stdin)
  match            match a STIX 2.1 pattern against observed-data
  exchange         exchanges (list, get <exchange> <opts>)
  filter           Bloom filter demo (demo, check -ip / -process)

Options:
  -config string        path to base config file (or STIX_CONFIG)
  -config-override string  path to override config file (or STIX_CONFIG_OVERRIDE)
  -h, -help             show this help

Use "stix <command> -h" for command-specific help.
`)
}

func printTaxiiUsage() {
	fmt.Fprint(os.Stderr, `Usage: stix taxii <subcommand> [args]

Subcommands:
  server   run a TAXII 2.1 server (see stix taxii server -h)
  client   TAXII client: discovery, collections, objects (see stix taxii client -h)
`)
}
