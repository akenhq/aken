// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/akenhq/aken/internal/buildinfo"
	"github.com/akenhq/aken/internal/collect"
	"github.com/akenhq/aken/internal/serve"
	"github.com/akenhq/aken/internal/source"
	"github.com/akenhq/aken/protocol"
	"golang.org/x/term"
)

const usage = `aken is the Aken collector. It runs on your server as an unprivileged user,
makes outbound HTTPS requests only, and runs jobs from a fixed catalog.

Usage:
  aken <command> [flags]

Commands:
  collect   Collect logs, redact them here, review, and upload one encrypted artifact
  serve     Open a live session and run approved catalog jobs
  version   Print the version
  help      Print this help

Run "aken <command> --help" for details.
`

const collectUsage = `Usage: aken collect [flags]

Sources (at least one; each flag repeats):
  --unit NAME         journald unit, for example nginx or nginx.service
  --container NAME    docker container that logs through the journald driver
  --file PATH         plain text file; absolute path under /var/log or a --allow directory
  --glob PATTERN      files matching a glob; every match must be under an allowed directory

Selection:
  --since T           start of the window for journald sources; --glob skips files last modified before T (default 1h)
  --until T           end of the window for journald sources (default now)
  --tail N            keep only the last N lines of each file source (default 0, everything)
  --allow DIR         extra directory that --file and --glob may read from (repeatable; /var/log is always allowed)

Redaction:
  --keep VALUE        never replace this exact value (repeatable; shown on the review screen)
  --keep-category C   switch a category off for this run (repeatable): secret token jwt key ip email phone name address
  --rules FILE        extra rules file (default /etc/aken/rules.json when it exists)

Upload:
  --ttl D             artifact lifetime on the relay (default 4h, maximum 24h)
  --relay URL         relay base URL (default https://relay.aken.dev)
  --dry-run           collect, redact and show the review screen; upload nothing

Local copy:
  --state-dir DIR     where local copies go (default /var/lib/aken when writable, else $XDG_STATE_HOME/aken or ~/.local/state/aken)
  --retention D       delete local copies older than this before the run (default 720h; 0 keeps everything)

T is a duration before now (30m, 2h, 3d), an RFC 3339 time (2026-09-12T10:00:00Z), a local time
(2026-09-12 10:00, 2026-09-12 10:00:00) or a local date (2026-09-12).
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
	case "serve":
		return runServe(args, stdout, stderr)
	case "collect":
		return runCollect(args, stdout, stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "aken: unknown command %q\nRun \"aken help\" for usage.\n", args[0])
		return 2
	}
}

type stringList []string

func (s *stringList) String() string         { return "" }
func (s *stringList) Set(value string) error { *s = append(*s, value); return nil }

func runCollect(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("collect", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = fmt.Fprint(stdout, collectUsage) }
	var o collect.Options
	for _, f := range []struct {
		name  string
		value *[]string
	}{
		{"unit", &o.Units}, {"container", &o.Containers}, {"file", &o.Files}, {"glob", &o.Globs},
		{"allow", &o.Allow}, {"keep", &o.Keep}, {"keep-category", &o.KeepCategories},
	} {
		flags.Var((*stringList)(f.value), f.name, "")
	}
	since := flags.String("since", "1h", "")
	until := flags.String("until", "0s", "")
	flags.IntVar(&o.Tail, "tail", 0, "")
	flags.StringVar(&o.RulesFile, "rules", "", "")
	flags.DurationVar(&o.TTL, "ttl", protocol.DefaultTTL, "")
	flags.StringVar(&o.RelayURL, "relay", protocol.DefaultRelayURL, "")
	flags.BoolVar(&o.DryRun, "dry-run", false, "")
	flags.StringVar(&o.StateDir, "state-dir", "", "")
	flags.DurationVar(&o.Retention, "retention", 720*time.Hour, "")
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
	usageError := func(message string) int { _, _ = fmt.Fprintf(stderr, "aken: %s\n", message); return 2 }
	if flags.NArg() != 0 {
		return usageError("unexpected positional arguments")
	}
	if len(o.Units)+len(o.Containers)+len(o.Files)+len(o.Globs) == 0 {
		return usageError("at least one source is required")
	}
	now := time.Now()
	var err error
	o.Since, err = collect.ParseTime(*since, now)
	if err != nil {
		return usageError("--since: " + err.Error())
	}
	o.Until, err = collect.ParseTime(*until, now)
	if err != nil {
		return usageError("--until: " + err.Error())
	}
	if !o.Since.Before(o.Until) {
		return usageError("--since must be before --until")
	}
	if o.Tail < 0 {
		return usageError("--tail must not be negative")
	}
	if o.TTL <= 0 || o.TTL > protocol.MaxTTL {
		return usageError("--ttl must be greater than 0 and at most 24h")
	}
	if o.Retention < 0 {
		return usageError("--retention must not be negative")
	}
	for _, name := range o.Units {
		if err := source.ValidateUnit(name); err != nil {
			return usageError(err.Error())
		}
	}
	for _, name := range o.Containers {
		if err := source.ValidateContainer(name); err != nil {
			return usageError(err.Error())
		}
	}
	o.Argv = args
	o.Collector = buildinfo.String("aken")
	interactive := term.IsTerminal(int(os.Stdin.Fd()))
	pageLines := 40
	if _, height, err := term.GetSize(int(os.Stdout.Fd())); err == nil && height > 2 {
		pageLines = height - 2
	}
	return collect.Run(context.Background(), o, os.Stdin, stdout, stderr, interactive, pageLines)
}

const serveUsage = `Usage: aken serve [flags]

Session:
  --level N           0 runs every catalog job without asking; 1 asks for approval per job or plan (default 1)
  --ttl D             session lifetime on the relay (default 8h, maximum 24h)
  --relay URL         relay base URL (default https://relay.aken.dev)
  --allow DIR         extra directory that file jobs may read from (repeatable; /var/log is always allowed)

Redaction:
  --keep VALUE        never replace this exact value (repeatable; shown when the session opens)
  --keep-category C   switch a category off for this session (repeatable): secret token jwt key ip email phone name address
  --rules FILE        extra rules file (default /etc/aken/rules.json when it exists)

Local copy:
  --state-dir DIR     where local copies go (default /var/lib/aken when writable, else $XDG_STATE_HOME/aken or ~/.local/state/aken)
  --retention D       delete local copies older than this before the session (default 720h; 0 keeps everything)
`

func runServe(args []string, stdout, stderr io.Writer) int {
	if geteuid() == 0 {
		_, _ = fmt.Fprintln(stderr, "aken: refusing to run as root. Run it as the dedicated unprivileged user, for example: sudo -u aken aken serve")
		return 1
	}
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = fmt.Fprint(stdout, serveUsage) }
	var o serve.Options
	flags.IntVar(&o.Level, "level", 1, "")
	flags.DurationVar(&o.TTL, "ttl", protocol.DefaultSessionTTL, "")
	flags.StringVar(&o.RelayURL, "relay", protocol.DefaultRelayURL, "")
	for _, f := range []struct {
		name  string
		value *[]string
	}{{"allow", &o.Allow}, {"keep", &o.Keep}, {"keep-category", &o.KeepCategories}} {
		flags.Var((*stringList)(f.value), f.name, "")
	}
	flags.StringVar(&o.RulesFile, "rules", "", "")
	flags.StringVar(&o.StateDir, "state-dir", "", "")
	flags.DurationVar(&o.Retention, "retention", 720*time.Hour, "")
	if err := flags.Parse(args[1:]); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	usageError := func(message string) int { _, _ = fmt.Fprintf(stderr, "aken: %s\n", message); return 2 }
	if flags.NArg() != 0 {
		return usageError("unexpected positional arguments")
	}
	if o.Level != 0 && o.Level != 1 {
		return usageError("--level must be 0 or 1")
	}
	if o.TTL <= 0 || o.TTL > protocol.MaxTTL {
		return usageError("--ttl must be greater than 0 and at most 24h")
	}
	if o.Retention < 0 {
		return usageError("--retention must not be negative")
	}
	for _, dir := range o.Allow {
		if !filepath.IsAbs(dir) {
			return usageError("--allow directory must be absolute")
		}
	}
	if _, err := protocol.NewRelayClient(o.RelayURL, [32]byte{}); err != nil {
		return usageError(err.Error())
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		_, _ = fmt.Fprintln(stderr, "aken: serve needs a terminal")
		return 1
	}
	o.Argv = args
	o.Collector = buildinfo.String("aken")
	pageLines := 40
	if _, height, err := term.GetSize(int(os.Stdout.Fd())); err == nil && height > 2 {
		pageLines = height - 2
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return serve.Run(ctx, o, os.Stdin, stdout, stderr, pageLines)
}
