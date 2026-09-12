// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/akenhq/aken/internal/buildinfo"
	"github.com/akenhq/aken/internal/devrelay"
)

const usage = `aken-devrelay is an in-memory relay for tests and local development. It keeps
nothing on disk, has no accounts and no policy, and is not for production.

Usage:
  aken-devrelay <command> [flags]

Commands:
  serve     Serve the relay API on localhost
  version   Print the version
  help      Print this help

Run "aken-devrelay <command> --help" for details.
`

const serveUsage = `Usage: aken-devrelay serve [--listen ADDR]

Serves relay API v0 in memory on ADDR (default 127.0.0.1:7788). Artifacts live until
their TTL and are lost on exit. No accounts, no limits beyond the protocol caps, no TLS.
Not for production.
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
		flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
		flags.SetOutput(stderr)
		flags.Usage = func() { _, _ = fmt.Fprint(stdout, serveUsage) }
		listen := flags.String("listen", "127.0.0.1:7788", "listen address")
		if err := flags.Parse(args[1:]); err != nil {
			if err == flag.ErrHelp {
				return 0
			}
			return 2
		}
		if flags.NArg() != 0 {
			_, _ = fmt.Fprintln(stderr, "aken-devrelay: unexpected arguments")
			return 2
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := serve(ctx, *listen, stdout, stderr); err != nil {
			_, _ = fmt.Fprintf(stderr, "aken-devrelay: %v\n", err)
			return 1
		}
		return 0
	default:
		_, _ = fmt.Fprintf(stderr, "aken-devrelay: unknown command %q\nRun \"aken-devrelay help\" for usage.\n", args[0])
		return 2
	}
}

func serve(ctx context.Context, listen string, stdout, stderr io.Writer) error {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return err
	}
	if host != "localhost" && !net.ParseIP(host).IsLoopback() {
		_, _ = fmt.Fprintln(stderr, "aken-devrelay: warning: listen address is not loopback")
	}
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	relay := devrelay.New()
	server := &http.Server{Addr: listen, Handler: relay, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 5 * time.Minute, WriteTimeout: 5 * time.Minute}
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				relay.Sweep()
			case <-done:
				return
			}
		}
	}()
	stopped := make(chan error, 1)
	go func() { stopped <- server.Serve(listener) }()
	_, _ = fmt.Fprintf(stdout, "aken-devrelay listening on http://%s (in-memory, not for production)\n", listen)
	select {
	case err := <-stopped:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}
