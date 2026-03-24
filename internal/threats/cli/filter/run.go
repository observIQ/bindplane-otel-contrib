// package amqfilter runs the filter demo CLI: create separate filters (e.g. IPs, process names), hydrate with sample data, and check values for matches.
package amqfilter

import (
	"flag"
	"fmt"
	"os"
	"strings"

	filterpkg "github.com/observiq/bindplane-otel-collector/internal/filter"
)

const (
	nameIPs        = "ips"
	nameProcesses  = "process_names"
	estimatedIPs   = 10_000
	estimatedProcs = 5_000
	falsePosRate   = 0.01
)

// demoFilterSet creates a FilterSet with "ips" and "process_names", hydrates with sample data, and returns it.
func demoFilterSet() *filterpkg.FilterSet {
	set := filterpkg.NewFilterSet()
	ips := set.AddBloomFilter(nameIPs, estimatedIPs, falsePosRate)
	procs := set.AddBloomFilter(nameProcesses, estimatedProcs, falsePosRate)

	// Sample IPs (e.g. from threat intel)
	for _, ip := range []string{"192.168.1.1", "10.0.0.1", "1.2.3.4", "203.0.113.50", "198.51.100.10"} {
		ips.AddString(ip)
	}
	// Sample process names (e.g. from threat intel)
	for _, p := range []string{"cmd.exe", "powershell.exe", "malware.exe", "suspicious.bin", "wscript.exe"} {
		procs.AddString(p)
	}
	return set
}

// Run runs the filter subcommand. Subcommands: demo, check.
func Run(args []string) int {
	if len(args) == 0 {
		PrintUsage()
		return 1
	}
	cmd := strings.ToLower(args[0])
	rest := args[1:]

	switch cmd {
	case "demo":
		return runDemo(rest)
	case "check":
		return runCheck(rest)
	case "help", "-h", "--help":
		PrintUsage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "stix filter: unknown command %q (use demo or check)\n", cmd)
		PrintUsage()
		return 1
	}
}

func runDemo(_ []string) int {
	set := demoFilterSet()
	fmt.Println("Bloom filter demo: two filters (ips, process_names) with sample data.")
	fmt.Println("Checking a few values and returning matches:")
	fmt.Println()

	// Check some IPs
	for _, ip := range []string{"192.168.1.1", "10.0.0.99", "1.2.3.4"} {
		f := set.Filter(nameIPs)
		ok := f != nil && f.MayContainString(ip)
		fmt.Printf("  ip %q -> ips: %s\n", ip, matchResult(ok))
	}
	// Check some process names
	for _, p := range []string{"cmd.exe", "notepad.exe", "malware.exe"} {
		f := set.Filter(nameProcesses)
		ok := f != nil && f.MayContainString(p)
		fmt.Printf("  process %q -> process_names: %s\n", p, matchResult(ok))
	}
	return 0
}

func matchResult(mayContain bool) string {
	if mayContain {
		return "may be present (match)"
	}
	return "not present (no match)"
}

func runCheck(args []string) int {
	fs := flag.NewFlagSet("filter check", flag.ExitOnError)
	ip := fs.String("ip", "", "IP address to check against the ips filter")
	process := fs.String("process", "", "Process name to check against the process_names filter")
	_ = fs.Parse(args)

	if *ip == "" && *process == "" {
		fmt.Fprintf(os.Stderr, "stix filter check: provide at least one of -ip or -process\n")
		PrintUsage()
		return 1
	}

	set := demoFilterSet()
	anyMatch := false
	if *ip != "" {
		f := set.Filter(nameIPs)
		ok := f != nil && f.MayContainString(*ip)
		fmt.Printf("ips: %s\n", matchResult(ok))
		if ok {
			anyMatch = true
		}
	}
	if *process != "" {
		f := set.Filter(nameProcesses)
		ok := f != nil && f.MayContainString(*process)
		fmt.Printf("process_names: %s\n", matchResult(ok))
		if ok {
			anyMatch = true
		}
	}
	if anyMatch {
		fmt.Println("(At least one filter reported may be present; could be a false positive.)")
	}
	return 0
}

// PrintUsage prints filter subcommand usage to os.Stderr.
func PrintUsage() {
	fmt.Fprint(os.Stderr, `Usage: stix filter <command> [args]

Commands:
  demo    create two Bloom filters (ips, process_names), hydrate with sample
          data, and print example match results
  check   check values against the same demo filters; returns matches
          stix filter check -ip <ip> -process <name>

Examples:
  stix filter demo
  stix filter check -ip 192.168.1.1
  stix filter check -process cmd.exe
  stix filter check -ip 10.0.0.99 -process notepad.exe
`)
}
