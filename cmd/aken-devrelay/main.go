// SPDX-License-Identifier: Apache-2.0
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/akenhq/aken/internal/buildinfo"
)

const usage = `aken-devrelay is an in-memory relay for tests and local development. It keeps
nothing on disk, has no accounts and no policy, and is not for production.

Usage:
  aken-devrelay <command> [flags]

Commands:
  serve     Serve the relay API on localhost (phase 1)
  version   Print the version
  help      Print this help

Run "aken-devrelay <command> --help" for details.
`

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help":
		_, _ = fmt.Fprint(stdout, usage)
		return 0
	case "version":
		_, _ = fmt.Fprintln(stdout, buildinfo.String("aken-devrelay"))
		return 0
	case "serve":
		commandUsage := "Usage: aken-devrelay serve\nServes the relay API on 127.0.0.1 for local development. Not implemented yet.\n"
		flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
		flags.SetOutput(stderr)
		flags.Usage = func() { _, _ = fmt.Fprint(stdout, commandUsage) }
		if err := flags.Parse(args[1:]); err != nil {
			if err == flag.ErrHelp {
				return 0
			}
			return 2
		}
		_, _ = fmt.Fprintf(stderr, "aken-devrelay %s: not implemented yet (phase 1)\n", args[0])
		return 2
	default:
		_, _ = fmt.Fprintf(stderr, "aken-devrelay: unknown command %q\nRun \"aken-devrelay help\" for usage.\n", args[0])
		return 2
	}
}
