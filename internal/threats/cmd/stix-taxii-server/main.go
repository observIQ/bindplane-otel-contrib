// Command stix-taxii-server runs a TAXII 2.1 server with an in-memory backend.
// Optional initial data can be loaded from a medallion-style JSON file.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/observiq/bindplane-otel-collector/internal/threats/taxii"
)

func main() {
	var (
		addr        string
		basePath    string
		maxPageSize int
		users       string
		usersFile   string
		dataFile    string
	)
	flag.StringVar(&addr, "addr", "localhost:8080", "listen address")
	flag.StringVar(&basePath, "base", "/taxii2", "URL base path for TAXII (e.g. /taxii2)")
	flag.IntVar(&maxPageSize, "max-page-size", taxii.DefaultMaxPageSize, "maximum objects per page")
	flag.StringVar(&users, "users", "", "comma-separated user:password for HTTP Basic auth (optional)")
	flag.StringVar(&usersFile, "users-file", "", "path to file with lines of user:password (optional)")
	flag.StringVar(&dataFile, "data", "", "path to JSON file for initial backend data (medallion-style)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `Usage: stix-taxii-server [options]

Run a TAXII 2.1 server. Without -users and -users-file, the server allows unauthenticated access.

Options:
  -addr string
        listen address (default "localhost:8080")
  -base string
        URL base path (default "/taxii2")
  -max-page-size int
        maximum objects per page (default %d)
  -users string
        comma-separated user:password for HTTP Basic auth
  -users-file string
        path to file with one user:password per line
  -data string
        path to JSON file for initial backend data (medallion-style)
  -h, -help
        show this help
`, taxii.DefaultMaxPageSize)
	}
	flag.Parse()

	var backend *taxii.MemoryBackend
	if dataFile != "" {
		data, err := os.ReadFile(dataFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "stix-taxii-server: read data file: %v\n", err)
			os.Exit(1)
		}
		backend, err = taxii.NewMemoryBackendFromJSON(data)
		if err != nil {
			fmt.Fprintf(os.Stderr, "stix-taxii-server: load data: %v\n", err)
			os.Exit(1)
		}
	} else {
		backend = taxii.NewMemoryBackend()
		// Ensure at least discovery and one API root so server is usable
		backend.SetDiscovery(&taxii.Discovery{
			Title:       "STIX TAXII 2.1 Server",
			Description: "In-memory TAXII 2.1 server",
			APIRoots:    []string{basePath + "/api1"},
			Default:     basePath + "/api1",
		})
		backend.AddAPIRoot("api1", taxii.APIRootInfo{
			Title:            "Default API Root",
			MaxContentLength: 10485760,
		}, []taxii.CollectionInfo{
			{
				ID:       "default",
				Title:    "Default Collection",
				CanRead:  true,
				CanWrite: true,
			},
		})
	}

	userMap := parseUsers(users, usersFile)
	var auth taxii.AuthChecker
	if len(userMap) > 0 {
		auth = taxii.BasicAuthUsers(userMap)
	}

	srv := taxii.NewHTTPServer(backend, auth)
	srv.BasePath = strings.TrimSuffix(basePath, "/")
	if srv.BasePath == "" {
		srv.BasePath = "/taxii2"
	}
	srv.MaxPageSize = maxPageSize
	if srv.MaxPageSize <= 0 {
		srv.MaxPageSize = taxii.DefaultMaxPageSize
	}

	fmt.Fprintf(os.Stderr, "stix-taxii-server: listening on %s, base path %s\n", addr, srv.BasePath)
	if err := srv.ListenAndServe(addr); err != nil {
		fmt.Fprintf(os.Stderr, "stix-taxii-server: %v\n", err)
		os.Exit(1)
	}
}

func parseUsers(users, usersFile string) map[string]string {
	out := make(map[string]string)
	for _, s := range strings.Split(users, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		idx := strings.Index(s, ":")
		if idx <= 0 {
			continue
		}
		out[s[:idx]] = s[idx+1:]
	}
	if usersFile != "" {
		f, err := os.Open(usersFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "stix-taxii-server: open users file: %v\n", err)
			os.Exit(1)
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			idx := strings.Index(line, ":")
			if idx <= 0 {
				continue
			}
			out[line[:idx]] = line[idx+1:]
		}
		if err := sc.Err(); err != nil {
			fmt.Fprintf(os.Stderr, "stix-taxii-server: read users file: %v\n", err)
			os.Exit(1)
		}
	}
	return out
}
