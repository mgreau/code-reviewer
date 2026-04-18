/*
Copyright 2025 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	// Support both `reviewer <subcommand>` and `reviewer <flags>` (the latter
	// is the legacy one-shot CLI shape).
	switch cmd {
	case "review":
		os.Args = append([]string{os.Args[0]}, os.Args[2:]...)
		runReview()
	case "serve":
		os.Args = append([]string{os.Args[0]}, os.Args[2:]...)
		runServe()
	case "-h", "--help", "help":
		usage()
	default:
		// Legacy shape: flags directly after program name.
		runReview()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `reviewer — AI-powered GitHub PR reviewer

Usage:
  reviewer review <flags>    Review a single PR (default when no subcommand is given)
  reviewer serve  <flags>    Run as an on-demand webhook server (@mention trigger)
  reviewer help              Show this message

Run 'reviewer review -h' or 'reviewer serve -h' for per-command flags.`)
	flag.PrintDefaults()
}
