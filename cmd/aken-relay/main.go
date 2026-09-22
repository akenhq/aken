// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/akenhq/aken/internal/buildinfo"
	"github.com/akenhq/aken/internal/relayserver"
	"github.com/akenhq/aken/internal/relayserver/r2"
	"github.com/akenhq/aken/protocol"
	"github.com/akenhq/aken/relay"
)

const usage = `aken-relay serves relay API v0 with memory, directory or R2 storage.

Usage:
  aken-relay <command> [flags]

Commands:
  serve                  Serve the relay API
  admin delete-session   Delete a session from R2
  version                Print the version
  help                   Print this help

Run "aken-relay serve --help" for details.
`

const serveUsage = `Usage: aken-relay serve [flags]

  --listen ADDR             Listen address (default 127.0.0.1:7788)
  --store memory|dir|r2     Storage backend (default memory)
  --data-dir PATH           Directory for --store dir
  --behind-cloudflare       Trust CF-Connecting-IP (default false)
  --requests-per-minute N   Requests per IP per minute (default 600)
  --creates-per-hour N      Creates per IP per hour (default 10)
  --max-live-sessions N     Capacity from the last dir/R2 sweep (default 200)
  --allowance-mode MODE     off, observe or enforce (default off)
  --allowance-servers N     Servers per developer address (default 2)
  --allowance-window D      Rolling allowance window (default 24h)

Clients accept plain http:// only on loopback. For remote clients, terminate TLS in a reverse proxy or tunnel in front of --listen.
`

const adminUsage = "Usage: aken-relay admin delete-session <session id>\n"

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help":
		if args[0] == "help" && len(args) == 2 && args[1] == "serve" {
			_, _ = fmt.Fprint(stdout, serveUsage)
			return 0
		}
		_, _ = fmt.Fprint(stdout, usage)
		return 0
	case "version", "--version", "-V":
		_, _ = fmt.Fprintln(stdout, buildinfo.String("aken-relay"))
		return 0
	case "serve":
		flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
		flags.SetOutput(stderr)
		flags.Usage = func() { _, _ = fmt.Fprint(stdout, serveUsage) }
		var cfg relayserver.Config
		flags.StringVar(&cfg.Listen, "listen", "127.0.0.1:7788", "listen address")
		flags.StringVar(&cfg.Store, "store", "memory", "storage backend")
		flags.StringVar(&cfg.DataDir, "data-dir", "", "directory for dir storage")
		flags.BoolVar(&cfg.BehindCloudflare, "behind-cloudflare", false, "trust CF-Connecting-IP")
		flags.IntVar(&cfg.Limits.RequestsPerMinute, "requests-per-minute", 600, "requests per IP per minute")
		flags.IntVar(&cfg.Limits.CreatesPerHour, "creates-per-hour", 10, "creates per IP per hour")
		flags.IntVar(&cfg.Limits.MaxLiveSessions, "max-live-sessions", 200, "live session capacity")
		flags.StringVar(&cfg.Limits.AllowanceMode, "allowance-mode", "off", "anonymous allowance mode")
		flags.IntVar(&cfg.Limits.AllowanceServers, "allowance-servers", 2, "servers per developer address")
		flags.DurationVar(&cfg.Limits.AllowanceWindow, "allowance-window", 24*time.Hour, "rolling allowance window")
		if err := flags.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		if flags.NArg() != 0 {
			_, _ = fmt.Fprintln(stderr, "aken-relay: unexpected arguments")
			return 2
		}
		if cfg.Store != "memory" && cfg.Store != "dir" && cfg.Store != "r2" {
			_, _ = fmt.Fprintln(stderr, "aken-relay: --store must be memory, dir or r2")
			return 2
		}
		if cfg.Store == "dir" && cfg.DataDir == "" {
			_, _ = fmt.Fprintln(stderr, "aken-relay: --data-dir is required with --store dir")
			return 2
		}
		if cfg.Store != "dir" && cfg.DataDir != "" {
			_, _ = fmt.Fprintln(stderr, "aken-relay: --data-dir needs --store dir")
			return 2
		}
		if cfg.Limits.AllowanceMode != "off" && cfg.Limits.AllowanceMode != "observe" && cfg.Limits.AllowanceMode != "enforce" {
			_, _ = fmt.Fprintln(stderr, "aken-relay: --allowance-mode must be off, observe or enforce")
			return 2
		}
		if cfg.Limits.RequestsPerMinute <= 0 || cfg.Limits.CreatesPerHour <= 0 || cfg.Limits.MaxLiveSessions <= 0 || cfg.Limits.AllowanceServers <= 0 || cfg.Limits.AllowanceWindow <= 0 {
			_, _ = fmt.Fprintln(stderr, "aken-relay: limits must be positive")
			return 2
		}
		if cfg.Store == "r2" {
			var err error
			cfg.R2, err = r2.ConfigFromEnv()
			if err != nil {
				return failure(stderr, err)
			}
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := relayserver.Run(ctx, cfg, stdout, stderr); err != nil {
			return failure(stderr, err)
		}
		return 0
	case "admin":
		if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
			_, _ = fmt.Fprint(stdout, adminUsage)
			return 0
		}
		if len(args) != 3 || args[1] != "delete-session" {
			_, _ = fmt.Fprint(stderr, adminUsage)
			return 2
		}
		id, ok := protocol.ParseSessionID(args[2])
		if !ok {
			_, _ = fmt.Fprintln(stderr, "aken-relay: invalid session id: expected 32 lowercase hexadecimal characters")
			return 2
		}
		cfg, err := r2.ConfigFromEnv()
		if err != nil {
			return failure(stderr, err)
		}
		ctx := context.Background()
		store, err := r2.New(ctx, cfg)
		if err != nil {
			return failure(stderr, err)
		}
		if err := store.DeleteSession(ctx, id); err != nil {
			if errors.Is(err, relay.ErrNotFound) {
				_, _ = fmt.Fprintln(stdout, "no such session")
				return 0
			}
			return failure(stderr, err)
		}
		_, _ = fmt.Fprintf(stdout, "deleted session %s\n", id)
		return 0
	default:
		_, _ = fmt.Fprintf(stderr, "aken-relay: unknown command %q\nRun \"aken-relay help\" for usage.\n", args[0])
		return 2
	}
}

func failure(stderr io.Writer, err error) int {
	_, _ = fmt.Fprintf(stderr, "aken-relay: %v\n", err)
	return 1
}
