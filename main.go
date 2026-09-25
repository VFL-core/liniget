// liniget is a small wget-like file grabber.
//
// Usage:
//
//	liniget grab <url> [-f flag flag=value ...]
//	liniget -stp
//	liniget help
//
// Flags (space or comma separated after -f):
//
//	skip             skip download if the destination file already exists
//	overwrite        overwrite the destination file if it already exists
//	rename           save as "name (1).ext" if the destination exists
//	resume           resume a partially-downloaded file (single-thread only)
//	threads=N        number of concurrent connections for this download
//	limit=RATE       speed cap, e.g. limit=500k, limit=2m, limit=1.5g
//	retries=N        retry attempts on failure
//	timeout=SECS     per-request timeout in seconds
//	out=PATH         output file path or directory
//	name=FILENAME    override just the output filename
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "-stp", "setup":
		if err := runSetup(); err != nil {
			fmt.Fprintf(os.Stderr, "liniget: %v\n", err)
			os.Exit(1)
		}

	case "grab":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "liniget: grab requires a URL, e.g. liniget grab https://example.com/file.zip")
			os.Exit(1)
		}
		url := os.Args[2]

		var flagArgs []string
		rest := os.Args[3:]
		for i := 0; i < len(rest); i++ {
			if rest[i] == "-f" {
				flagArgs = rest[i+1:]
				break
			}
		}

		cfg, err := loadConfig()
		if err != nil {
			fmt.Fprintf(os.Stderr, "liniget: warning: %v (using defaults)\n", err)
		}

		if err := runGrab(url, flagArgs, cfg); err != nil {
			fmt.Fprintf(os.Stderr, "liniget: %v\n", err)
			os.Exit(1)
		}

	case "help", "-h", "--help":
		printUsage()

	default:
		fmt.Fprintf(os.Stderr, "liniget: unknown command %q\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`liniget - a small wget-like file grabber

Usage:
  liniget grab <url> [-f flag flag=value ...]
  liniget -stp
  liniget help

Flags for grab (space or comma separated after -f):
  skip             skip download if the destination file already exists
  overwrite        overwrite the destination file if it already exists
  rename           save as "name (1).ext" if the destination exists
  resume           resume a partially-downloaded file (single-thread only)
  threads=N        number of concurrent connections for this download
  limit=RATE       speed cap, e.g. limit=500k, limit=2m, limit=1.5g
  retries=N        retry attempts on failure
  timeout=SECS     per-request timeout in seconds
  out=PATH         output file path or directory
  name=FILENAME    override just the output filename

Examples:
  liniget grab https://example.com/file.iso
  liniget grab https://example.com/file.iso -f threads=8 limit=2m
  liniget grab https://example.com/file.iso -f skip out=/data/isos/
  liniget grab https://example.com/big.zip -f resume threads=1

Run "liniget -stp" first to set your defaults (thread count, download
directory, speed limit, retries, timeout, and what to do when a file
already exists).`)
}
