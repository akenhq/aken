// SPDX-License-Identifier: Apache-2.0
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/akenhq/aken/internal/buildinfo"
)

const usage = `aken-mcp is the Aken local MCP server. It runs on your machine, holds the
session keys, and exposes the tools your coding agent uses to read what the
collector uploaded. It talks to the relay only, never to your server.

Usage:
  aken-mcp <command> [flags]

Commands:
  join      Join a session with the token the collector printed (phase 1)
  serve     Serve the MCP tools over stdio (phase 1)
  version   Print the version
  help      Print this help

Run "aken-mcp <command> --help" for details.
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
		_, _ = fmt.Fprintln(stdout, buildinfo.String("aken-mcp"))
		return 0
	case "join", "serve":
		commandUsage := "Usage: aken-mcp join\nStores the session locally so the stdio server can pick it up. Not implemented yet.\n"
		if args[0] == "serve" {
			commandUsage = "Usage: aken-mcp serve\nServes the MCP tools over stdio for your coding agent. Not implemented yet.\n"
		}
		flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
		flags.SetOutput(stderr)
		flags.Usage = func() { _, _ = fmt.Fprint(stdout, commandUsage) }
		if err := flags.Parse(args[1:]); err != nil {
			if err == flag.ErrHelp {
				return 0
			}
			return 2
		}
		_, _ = fmt.Fprintf(stderr, "aken-mcp %s: not implemented yet (phase 1)\n", args[0])
		return 2
	default:
		_, _ = fmt.Fprintf(stderr, "aken-mcp: unknown command %q\nRun \"aken-mcp help\" for usage.\n", args[0])
		return 2
	}
}
