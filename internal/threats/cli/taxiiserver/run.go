// Package taxiiserver runs a TAXII 2.1 server (stix taxii server).
package taxiiserver

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/observiq/bindplane-otel-collector/internal/threats/taxii"
)

// Options holds TAXII server options (from config + flags).
type Options struct {
	Addr        string
	Base        string
	MaxPageSize int
	Users       string
	UsersFile   string
	DataFile    string
}

// Run runs the TAXII server. It does not return on success (ListenAndServe blocks).
func Run(opts Options) error {
	basePath := opts.Base
	if basePath == "" {
		basePath = "/taxii2"
	}
	var backend *taxii.MemoryBackend
	if opts.DataFile != "" {
		data, err := os.ReadFile(opts.DataFile)
		if err != nil {
			return fmt.Errorf("read data file: %w", err)
		}
		backend, err = taxii.NewMemoryBackendFromJSON(data)
		if err != nil {
			return fmt.Errorf("load data: %w", err)
		}
	} else {
		backend = taxii.NewMemoryBackend()
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
			{ID: "default", Title: "Default Collection", CanRead: true, CanWrite: true},
		})
	}

	userMap := parseUsers(opts.Users, opts.UsersFile)
	var auth taxii.AuthChecker
	if len(userMap) > 0 {
		auth = taxii.BasicAuthUsers(userMap)
	}

	srv := taxii.NewHTTPServer(backend, auth)
	srv.BasePath = strings.TrimSuffix(basePath, "/")
	if srv.BasePath == "" {
		srv.BasePath = "/taxii2"
	}
	srv.MaxPageSize = opts.MaxPageSize
	if srv.MaxPageSize <= 0 {
		srv.MaxPageSize = taxii.DefaultMaxPageSize
	}

	fmt.Fprintf(os.Stderr, "stix taxii server: listening on %s, base path %s\n", opts.Addr, srv.BasePath)
	return srv.ListenAndServe(opts.Addr)
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
			fmt.Fprintf(os.Stderr, "stix taxii server: open users file: %v\n", err)
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
			fmt.Fprintf(os.Stderr, "stix taxii server: read users file: %v\n", err)
			os.Exit(1)
		}
	}
	return out
}
