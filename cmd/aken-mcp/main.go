// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/akenhq/aken/internal/buildinfo"
	akenmcp "github.com/akenhq/aken/internal/mcp"
	"github.com/akenhq/aken/internal/session"
	"github.com/akenhq/aken/protocol"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/term"
)

const usage = `aken-mcp is the Aken local MCP server. It runs on your machine, holds the
session token, and exposes the tools your coding agent uses to read what the
collector uploaded. It talks to the relay only, never to your server.

Usage:
  aken-mcp <command> [flags]

Commands:
  join      Store a session token from the collector; reads it from stdin when omitted
  serve     Serve the MCP tools over stdio
  status    Print the current session and when it expires
  end       Delete the artifact on the relay and forget the session
  version   Print the version
  help      Print this help

Run "aken-mcp <command> --help" for details.
`

const joinUsage = "Usage: aken-mcp join [TOKEN] [--relay URL]\nStore a session token; without TOKEN, read one line from stdin.\n  --relay URL   relay base URL (default https://relay.aken.dev)\n"
const serveUsage = "Usage: aken-mcp serve [--allow-chat-join] [--relay URL]\nServe the MCP tools over stdio.\n  --allow-chat-join   expose the join tool; tokens then remain in the chat transcript\n  --relay URL         relay the join tool uses (default https://relay.aken.dev)\n"
const statusUsage = "Usage: aken-mcp status\nPrint the current session and when it expires.\n"
const endUsage = "Usage: aken-mcp end\nDelete the artifact on the relay and forget the session.\n"

var sessionPath string

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return 2
	}
	command := args[0]
	switch command {
	case "help", "-h", "--help":
		if command == "help" && len(args) > 1 {
			return run(append(args[1:], "--help"), stdin, stdout, stderr)
		}
		_, _ = fmt.Fprint(stdout, usage)
		return 0
	case "version", "--version", "-V":
		if len(args) != 1 {
			_, _ = fmt.Fprintln(stderr, "aken-mcp: version takes no arguments")
			return 2
		}
		_, _ = fmt.Fprintln(stdout, buildinfo.String("aken-mcp"))
		return 0
	case "join", "serve", "status", "end":
	default:
		_, _ = fmt.Fprintf(stderr, "aken-mcp: unknown command %q. Run \"aken-mcp help\".\n", command)
		return 2
	}
	commandUsage := map[string]string{"join": joinUsage, "serve": serveUsage, "status": statusUsage, "end": endUsage}[command]
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	// Flag errors may contain a token, so inspect them before printing.
	flags.SetOutput(io.Discard)
	flags.Usage = func() { _, _ = fmt.Fprint(stdout, commandUsage) }
	relay := protocol.DefaultRelayURL
	allowChatJoin := false
	if command == "join" || command == "serve" {
		flags.StringVar(&relay, "relay", relay, "relay base URL")
	}
	if command == "serve" {
		flags.BoolVar(&allowChatJoin, "allow-chat-join", false, "expose the join tool")
	}
	rest := args[1:]
	var positional []string
	for len(rest) > 0 {
		if err := flags.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			message := err.Error()
			if strings.Contains(message, "akn1_") {
				message = "invalid flags"
			}
			_, _ = fmt.Fprintln(stderr, "aken-mcp:", message)
			return 2
		}
		rest = flags.Args()
		if len(rest) == 0 {
			break
		}
		positional = append(positional, rest[0])
		rest = rest[1:]
	}
	if (command == "join" && len(positional) > 1) || (command != "join" && len(positional) > 0) {
		_, _ = fmt.Fprintln(stderr, "aken-mcp: unexpected arguments")
		return 2
	}
	path := sessionPath
	if path == "" {
		var err error
		path, err = session.DefaultPath()
		if err != nil {
			return failure(stderr, err)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch command {
	case "join":
		text := ""
		if len(positional) == 1 {
			text = positional[0]
		} else if file, ok := stdin.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
			_, _ = fmt.Fprint(stderr, "Token: ")
			password, err := term.ReadPassword(int(file.Fd()))
			_, _ = fmt.Fprintln(stderr)
			if err != nil {
				return failure(stderr, errors.New("cannot read token from stdin"))
			}
			text = strings.TrimSpace(string(password))
		} else {
			scanner := bufio.NewScanner(stdin)
			if scanner.Scan() {
				text = strings.TrimSpace(scanner.Text())
			}
			if scanner.Err() != nil {
				return failure(stderr, errors.New("cannot read token from stdin"))
			}
		}
		if text == "" {
			return failure(stderr, errors.New("no token given; paste the token that aken serve or aken collect printed on your server"))
		}
		token, err := protocol.ParseToken(text)
		if err != nil {
			if lower, err := protocol.ParseToken(strings.ToLower(text)); err == nil {
				lower.Zero()
				return failure(stderr, errors.New("invalid token: it contains capital letters; copy it again from the server terminal"))
			}
			return failure(stderr, errors.New("invalid token: a token is akn1_ followed by 52 lowercase letters and digits; copy the whole line from the server terminal"))
		}
		defer token.Zero()
		if err := session.CheckJoin(path, token); err != nil {
			return failure(stderr, err)
		}
		client, err := protocol.NewRelayClient(relay, token.RelayCredential())
		if err != nil {
			return failure(stderr, err)
		}
		info, err := client.Session(ctx, token.SessionID())
		if protocol.IsNotFound(err) {
			return failure(stderr, fmt.Errorf("no artifact for this token on %s: it expired, was deleted, or the upload did not finish; if the collector used another relay, join with aken-mcp join --relay <URL>", client.BaseURL))
		}
		if err != nil {
			return failure(stderr, akenmcp.RelayLimitHint(err))
		}
		stored, err := session.Join(ctx, client, token, info, "cli", time.Now())
		if err != nil {
			return failure(stderr, err)
		}
		if err := session.Save(path, stored); err != nil {
			return failure(stderr, err)
		}
		if stored.Live() {
			_, _ = fmt.Fprintf(stdout, "Joined live session %s via cli. It expires at %s.\n", stored.SessionID, stored.ExpiresAt.UTC().Format(time.RFC3339))
			return 0
		}
		_, _ = fmt.Fprintf(stdout, "Joined session %s. The artifact expires at %s.\n", stored.SessionID, stored.ExpiresAt.Format(time.RFC3339))
	case "serve":
		s := &akenmcp.Server{SessionPath: path, Relay: relay, AllowChatJoin: allowChatJoin, Version: buildinfo.Version()}
		if err := s.MCP().Run(ctx, &mcp.StdioTransport{}); err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, mcp.ErrConnectionClosed) {
			return failure(stderr, err)
		}
	case "status", "end":
		stored, err := session.Load(path)
		if err != nil {
			return failure(stderr, err)
		}
		if command == "status" {
			expired := ""
			if stored.Expired(time.Now()) {
				expired = " (expired; run aken-mcp join with a new token)"
			}
			mode := "one-shot"
			if stored.Live() {
				mode = "live"
			}
			_, _ = fmt.Fprintf(stdout, "Session %s on %s, mode %s, joined %s via %s, expires %s%s\n", stored.SessionID, stored.Relay, mode, stored.JoinedAt.UTC().Format(time.RFC3339), stored.JoinedVia, stored.ExpiresAt.UTC().Format(time.RFC3339), expired)
			return 0
		}
		token, err := stored.ParsedToken()
		if err != nil {
			return failure(stderr, err)
		}
		defer token.Zero()
		client, err := protocol.NewRelayClient(stored.Relay, token.RelayCredential())
		if err != nil {
			return failure(stderr, err)
		}
		err = client.DeleteSession(ctx, token.SessionID())
		if err != nil && !protocol.IsNotFound(err) {
			return failure(stderr, err)
		}
		if err := session.Remove(path); err != nil {
			return failure(stderr, err)
		}
		if protocol.IsNotFound(err) {
			_, _ = fmt.Fprintf(stdout, "Session %s was already gone from the relay; forgot it locally.\n", stored.SessionID)
			return 0
		}
		if stored.Live() {
			_, _ = fmt.Fprintf(stdout, "Ended live session %s; the collector's aken serve stops on its next poll.\n", stored.SessionID)
			return 0
		}
		_, _ = fmt.Fprintf(stdout, "Ended session %s; the artifact is deleted from the relay.\n", stored.SessionID)
	}
	return 0
}

func failure(stderr io.Writer, err error) int {
	_, _ = fmt.Fprintln(stderr, "aken-mcp:", err)
	return 1
}
