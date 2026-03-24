// Package taxiiclient runs the TAXII client subcommands (stix taxii client discovery|collections|objects).
package taxiiclient

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	exchanges "github.com/observiq/bindplane-otel-collector/exchange"
	"github.com/observiq/bindplane-otel-collector/internal/threats/taxii"
)

// Options holds TAXII client options (from config + flags).
type Options struct {
	DiscoveryURL string
	Username     string
	Password     string
}

// Run runs the taxii client subcommand. args are the full args after "stix taxii client" (first non-flag is subcommand).
// opts are from config; flags in args override.
func Run(ctx context.Context, args []string, opts Options) int {
	fs := flag.NewFlagSet("taxii-client", flag.ExitOnError)
	fs.StringVar(&opts.DiscoveryURL, "url", opts.DiscoveryURL, "TAXII discovery URL")
	fs.StringVar(&opts.Username, "user", opts.Username, "HTTP Basic auth username")
	fs.StringVar(&opts.Password, "password", opts.Password, "HTTP Basic auth password")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintf(os.Stderr, "stix taxii client: requires command (discovery, collections, objects)\n")
		printUsage()
		return 1
	}
	subcmd := rest[0]
	subArgs := rest[1:]
	if opts.DiscoveryURL == "" {
		fmt.Fprintf(os.Stderr, "stix taxii client: discovery URL required (set -url or config)\n")
		return 1
	}
	var creds exchanges.Credentials
	if opts.Username != "" || opts.Password != "" {
		creds = &exchanges.BasicAuth{Username: opts.Username, Password: opts.Password}
	}
	client := taxii.NewClient(opts.DiscoveryURL, creds)

	switch subcmd {
	case "discovery", "server":
		return runDiscovery(client)
	case "collections":
		return runCollections(client)
	case "objects", "get":
		return runObjects(client, subArgs)
	default:
		fmt.Fprintf(os.Stderr, "stix taxii client: unknown command %q\n", subcmd)
		printUsage()
		return 1
	}
}

func printUsage() {
	fmt.Fprint(os.Stderr, `usage: stix taxii client <command> [options] [args]

commands:
  discovery   fetch and print server discovery (default API root)
  collections list collections in the default API root
  objects <collection-id> [--limit N]  get objects from a collection (writes STIX bundle JSON to stdout)

options (global for client):
  -url string       TAXII discovery URL (e.g. https://example.com/taxii2/) (or set in config)
  -user string      HTTP Basic auth username
  -password string  HTTP Basic auth password
`)
}

func runDiscovery(client *taxii.Client) int {
	server, err := client.GetServer()
	if err != nil {
		fmt.Fprintf(os.Stderr, "stix taxii client discovery: %v\n", err)
		return 1
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(server.Discovery); err != nil {
		fmt.Fprintf(os.Stderr, "stix taxii client discovery: %v\n", err)
		return 1
	}
	return 0
}

func runCollections(client *taxii.Client) int {
	server, err := client.GetServer()
	if err != nil {
		fmt.Fprintf(os.Stderr, "stix taxii client collections: %v\n", err)
		return 1
	}
	root := server.Default()
	if root == nil {
		fmt.Fprintf(os.Stderr, "stix taxii client collections: no API root\n")
		return 1
	}
	cols, err := root.Collections()
	if err != nil {
		fmt.Fprintf(os.Stderr, "stix taxii client collections: %v\n", err)
		return 1
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	var list []map[string]interface{}
	for _, c := range cols {
		if c.Info() != nil {
			list = append(list, map[string]interface{}{
				"id":        c.Info().ID,
				"title":     c.Info().Title,
				"can_read":  c.Info().CanRead,
				"can_write": c.Info().CanWrite,
			})
		}
	}
	if err := enc.Encode(list); err != nil {
		fmt.Fprintf(os.Stderr, "stix taxii client collections: %v\n", err)
		return 1
	}
	return 0
}

func runObjects(client *taxii.Client, args []string) int {
	fs := flag.NewFlagSet("objects", flag.ExitOnError)
	limit := fs.Int("limit", 100, "max objects per page")
	_ = fs.Parse(args)
	if fs.NArg() == 0 {
		fmt.Fprintf(os.Stderr, "stix taxii client objects: requires collection ID\n")
		return 1
	}
	collectionID := fs.Arg(0)

	server, err := client.GetServer()
	if err != nil {
		fmt.Fprintf(os.Stderr, "stix taxii client objects: %v\n", err)
		return 1
	}
	root := server.Default()
	if root == nil {
		fmt.Fprintf(os.Stderr, "stix taxii client objects: no API root\n")
		return 1
	}
	cols, err := root.Collections()
	if err != nil {
		fmt.Fprintf(os.Stderr, "stix taxii client objects: %v\n", err)
		return 1
	}
	var col *taxii.Collection
	for _, c := range cols {
		if c.Info() != nil && c.Info().ID == collectionID {
			col = c
			break
		}
	}
	if col == nil {
		fmt.Fprintf(os.Stderr, "stix taxii client objects: collection %q not found\n", collectionID)
		return 1
	}

	env, err := col.GetObjects(taxii.Filter{Limit: *limit})
	if err != nil {
		fmt.Fprintf(os.Stderr, "stix taxii client objects: %v\n", err)
		return 1
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(env); err != nil {
		fmt.Fprintf(os.Stderr, "stix taxii client objects: %v\n", err)
		return 1
	}
	return 0
}
