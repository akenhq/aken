// SPDX-License-Identifier: Apache-2.0
package mcp

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/akenhq/aken/internal/artifact"
	"github.com/akenhq/aken/internal/session"
	"github.com/akenhq/aken/protocol"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const instructions = "Read-only tools over one log artifact that a human collected with aken collect on a server, redacted there and uploaded encrypted; nothing you do here touches the server. Start with summary, then sources. Use search with a RE2 regex and before/after context instead of reading whole sources; use context around a line number and read for exact ranges (at most 500 lines per call, next_from continues). Line numbers are 1-based per source. Values were replaced with placeholders such as <ip#3> or <secret#1>; within this artifact the same placeholder always stands for the same original value, so you can correlate by placeholder but never recover the value. Every response ends with a JSON line: lines_redacted is how many returned lines contain placeholders. The artifact expires at the time summary reports; after that every tool returns an error and the human must collect again."

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
}

func (s *Server) MCP() *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "aken-mcp", Version: s.Version}, &mcp.ServerOptions{
		Instructions: instructions,
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
	})
	mcp.AddTool(srv, &mcp.Tool{Name: "sources", Description: "List the sources in the artifact with their line counts and time windows.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, s.sources)
	mcp.AddTool(srv, &mcp.Tool{Name: "summary", Description: "Show what was collected and what the redaction changed: counts by category, flagged strings, rules.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, s.summary)
	mcp.AddTool(srv, &mcp.Tool{Name: "search", Description: "Search lines with a RE2 regex, optionally in one source, with context lines. Paged; pass the returned cursor to continue.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, s.search)
	mcp.AddTool(srv, &mcp.Tool{Name: "tail", Description: "Return the last n lines of a source.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, s.tail)
	mcp.AddTool(srv, &mcp.Tool{Name: "read", Description: "Return lines from..to of a source, at most 500 per call; use next_from to continue.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, s.read)
	mcp.AddTool(srv, &mcp.Tool{Name: "context", Description: "Return the lines around one line number of a source.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, s.context)
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
