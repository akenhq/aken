// SPDX-License-Identifier: Apache-2.0
package mcp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/akenhq/aken/internal/artifact"
	"github.com/akenhq/aken/internal/session"
	"github.com/akenhq/aken/protocol"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const instructions = "Read-only tools over one log artifact that a human collected with aken collect on a server, redacted there and uploaded encrypted; nothing you do here touches the server. Start with summary, then sources. Use search with a RE2 regex and before/after context instead of reading whole sources; use context around a line number and read for exact ranges (at most 500 lines per call, next_from continues). Line numbers are 1-based per source. Values were replaced with placeholders such as <ip#3> or <secret#1>; within this artifact the same placeholder always stands for the same original value, so you can correlate by placeholder but never recover the value. Every response ends with a JSON line: lines_redacted is how many returned lines contain placeholders. The artifact expires at the time summary reports; after that every tool returns an error and the human must collect again.\n\nIn live sessions, the tools run on the server as typed jobs the human approves in a terminal. Page with from or cursor. Prefer search_files and journal with regex over reading whole files. Every result is redacted with the same placeholders across the session."

type Server struct {
	SessionPath   string
	Relay         string
	AllowChatJoin bool
	Version       string
	Now           func() time.Time
	NewClient     func(relay string, credential [32]byte) (*protocol.RelayClient, error)
	mu            sync.Mutex
	loaded        *artifact.Artifact
	loadedFor     string
	results       map[string]protocol.Result
	resultsFor    string
}

func (s *Server) MCP() *mcp.Server {
	current := "No session is joined yet: ask the human to run aken-mcp join on their machine."
	if stored, err := session.Load(s.SessionPath); err == nil {
		mode, tools := "one-shot artifact", "summary, sources, search, tail, read and context"
		if stored.Live() {
			mode, tools = "live session", "list_dir, read_file, search_files, tail_file, journal, docker_logs, systemctl_status, ps, df and plan"
		}
		current = fmt.Sprintf("Current session: %s %s, expires %s. Use %s.", mode, stored.SessionID, stored.ExpiresAt.UTC().Format(time.RFC3339), tools)
	}
	planSchema, err := jsonschema.For[planArgs](nil)
	if err != nil {
		panic(err)
	}
	planSchema.Properties["jobs"].Type = "array"
	planSchema.Properties["jobs"].Types = nil
	srv := mcp.NewServer(&mcp.Implementation{Name: "aken-mcp", Version: s.Version}, &mcp.ServerOptions{
		Instructions: current + "\n" + instructions,
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
	})
	mcp.AddTool(srv, &mcp.Tool{Name: "sources", Description: "List the sources in the artifact with their line counts and time windows.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, s.sources)
	mcp.AddTool(srv, &mcp.Tool{Name: "summary", Description: "Show what was collected and what the redaction changed: counts by category, flagged strings, rules.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, s.summary)
	mcp.AddTool(srv, &mcp.Tool{Name: "search", Description: "Search lines with a RE2 regex, optionally in one source, with context lines. Paged; pass the returned cursor to continue.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, s.search)
	mcp.AddTool(srv, &mcp.Tool{Name: "tail", Description: "Return the last n lines of a source.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, s.tail)
	mcp.AddTool(srv, &mcp.Tool{Name: "read", Description: "Return lines from..to of a source, at most 500 per call; use next_from to continue.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, s.read)
	mcp.AddTool(srv, &mcp.Tool{Name: "context", Description: "Return the lines around one line number of a source.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, s.context)
	mcp.AddTool(srv, &mcp.Tool{Name: "list_dir", Description: "List a directory on the server.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, s.listDir)
	mcp.AddTool(srv, &mcp.Tool{Name: "read_file", Description: "Read a file on the server; page with from and to.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, s.readFile)
	mcp.AddTool(srv, &mcp.Tool{Name: "search_files", Description: "Search server files with a RE2 regex; page with cursor.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, s.searchFiles)
	mcp.AddTool(srv, &mcp.Tool{Name: "tail_file", Description: "Read the last n lines of a server file.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, s.tailFile)
	mcp.AddTool(srv, &mcp.Tool{Name: "journal", Description: "Read a service journal; filter with regex and page with cursor.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, s.journal)
	mcp.AddTool(srv, &mcp.Tool{Name: "docker_logs", Description: "Read container logs; filter with regex and page with cursor.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, s.dockerLogs)
	mcp.AddTool(srv, &mcp.Tool{Name: "systemctl_status", Description: "Read a systemd service status.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, s.systemctlStatus)
	mcp.AddTool(srv, &mcp.Tool{Name: "ps", Description: "List server processes.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, s.ps)
	mcp.AddTool(srv, &mcp.Tool{Name: "df", Description: "Show server filesystem usage.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, s.df)
	mcp.AddTool(srv, &mcp.Tool{Name: "plan", Description: "Submit 1 to 40 catalog jobs for approval together. Use search or search_files and tail or tail_file in jobs.", InputSchema: planSchema, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, s.plan)
	mcp.AddTool(srv, &mcp.Tool{Name: "result", Description: "Wait for a job whose earlier call timed out.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, s.jobResult)
	if s.AllowChatJoin {
		mcp.AddTool(srv, &mcp.Tool{Name: "join", Description: "Store a session token so the tools can read its artifact on the relay this server was started with. The token then sits in this transcript; prefer aken-mcp join in a terminal."}, s.join)
	}
	return srv
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
func (s *Server) client(relay string, credential [32]byte) (*protocol.RelayClient, error) {
	if s.NewClient != nil {
		return s.NewClient(relay, credential)
	}
	return protocol.NewRelayClient(relay, credential)
}

// Handlers hold mu so a concurrent join cannot change the session during a response.
func (s *Server) artifact(ctx context.Context) (*artifact.Artifact, session.Session, error) {
	stored, err := session.Load(s.SessionPath)
	if err != nil {
		return nil, stored, err
	}
	if stored.Expired(s.now()) {
		return nil, stored, fmt.Errorf("the session expired at %s; run aken collect again and join the new token", stored.ExpiresAt.Format(time.RFC3339))
	}
	if stored.Live() {
		return nil, stored, errors.New("this is a live session: use read_file, search_files, tail_file, journal, docker_logs, systemctl_status, ps, df or plan")
	}
	if s.loaded != nil && s.loadedFor == stored.SessionID {
		return s.loaded, stored, nil
	}
	token, err := stored.ParsedToken()
	if err != nil {
		return nil, stored, err
	}
	defer token.Zero()
	client, err := s.client(stored.Relay, token.RelayCredential())
	if err != nil {
		return nil, stored, err
	}
	a, err := artifact.Load(ctx, client, token)
	if err != nil {
		return nil, stored, err
	}
	s.loaded, s.loadedFor = a, stored.SessionID
	return a, stored, nil
}

// Callers hold mu through submission and polling because relay delivery is at most once.
func (s *Server) live(ctx context.Context) (protocol.SessionKeys, *protocol.RelayClient, session.Session, error) {
	var keys protocol.SessionKeys
	stored, err := session.Load(s.SessionPath)
	if err != nil {
		return keys, nil, stored, err
	}
	if stored.Expired(s.now()) {
		return keys, nil, stored, fmt.Errorf("the session expired at %s; run aken serve again and join the new token", stored.ExpiresAt.UTC().Format(time.RFC3339))
	}
	if !stored.Live() {
		return keys, nil, stored, errors.New("this session is one-shot: use sources, summary, search, tail, read or context")
	}
	if err := ctx.Err(); err != nil {
		return keys, nil, stored, err
	}
	root, err := stored.ParsedContentRoot()
	if err != nil {
		return keys, nil, stored, err
	}
	token, err := stored.ParsedToken()
	if err != nil {
		return keys, nil, stored, err
	}
	defer token.Zero()
	client, err := s.client(stored.Relay, token.RelayCredential())
	if err != nil {
		return keys, nil, stored, err
	}
	if s.resultsFor != stored.SessionID {
		s.results, s.resultsFor = make(map[string]protocol.Result), stored.SessionID
	}
	return protocol.DeriveSessionKeys(root), client, stored, nil
}
