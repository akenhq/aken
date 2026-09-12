// SPDX-License-Identifier: Apache-2.0
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/akenhq/aken/internal/buildinfo"
)

const usage = `aken is the Aken collector. It runs on your server as an unprivileged user,
makes outbound HTTPS requests only, and runs jobs from a fixed catalog.

Usage:
  aken <command> [flags]

Commands:
  collect   Collect logs, redact them locally, and upload one encrypted artifact (phase 1)
  version   Print the version
  help      Print this help

Run "aken <command> --help" for details.
`

const collectUsage = `Usage: aken collect [flags]

Collects log lines from configured sources, redacts them on this machine,
shows you exactly what will leave the box, and uploads one encrypted
artifact with a short TTL. Not implemented yet: this is the phase 0 skeleton.
`

var geteuid = os.Geteuid // replaced in tests

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
		_, _ = fmt.Fprintln(stdout, buildinfo.String("aken"))
		return 0
	case "collect":
		flags := flag.NewFlagSet("collect", flag.ContinueOnError)
		flags.SetOutput(stderr)
		flags.Usage = func() { _, _ = fmt.Fprint(stdout, collectUsage) }
		if err := flags.Parse(args[1:]); err != nil {
			if err == flag.ErrHelp {
				return 0
			}
			return 2
		}

		if geteuid() == 0 {
			_, _ = fmt.Fprintln(stderr, "aken: refusing to run as root. Run it as the dedicated unprivileged user, for example: sudo -u aken aken collect")
			return 1
		}
		_, _ = fmt.Fprintf(stderr, "aken %s: not implemented yet (phase 1)\n", args[0])
		return 2
	default:
		_, _ = fmt.Fprintf(stderr, "aken: unknown command %q\nRun \"aken help\" for usage.\n", args[0])
		return 2
	}
}
