// SPDX-License-Identifier: Apache-2.0
package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/akenhq/aken/internal/artifact"
	"github.com/akenhq/aken/internal/session"
	"github.com/akenhq/aken/protocol"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const textCap = 200 << 10

type noArgs struct{}
type searchArgs struct {
	Regex  string `json:"regex" jsonschema:"RE2 regular expression to match against each line"`
	Source string `json:"source,omitempty" jsonschema:"source name from the sources tool; all sources when omitted"`
	Before int    `json:"before,omitempty" jsonschema:"context lines before each match, 0 to 50"`
	After  int    `json:"after,omitempty" jsonschema:"context lines after each match, 0 to 50"`
	Max    int    `json:"max,omitempty" jsonschema:"matches per call, 1 to 200, default 50"`
	Cursor string `json:"cursor,omitempty" jsonschema:"cursor from a previous search result to continue"`
}
type tailArgs struct {
	Source string `json:"source"`
	N      int    `json:"n,omitempty" jsonschema:"lines to return, 1 to 500, default 100"`
}
type readArgs struct {
	Source string `json:"source"`
	From   int    `json:"from" jsonschema:"first line, at least 1"`
	To     int    `json:"to" jsonschema:"last line, at least from"`
}
type contextArgs struct {
	Source string `json:"source"`
	Line   int    `json:"line" jsonschema:"center line, at least 1"`
	Around int    `json:"around,omitempty" jsonschema:"lines on each side, 0 to 200, default 20"`
}
type joinArgs struct {
	Token string `json:"token"`
}

func result(text string, meta any) (*mcp.CallToolResult, any, error) {
	if len(text) > textCap {
		return nil, nil, fmt.Errorf("response: line text exceeds 200 KiB")
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}, &mcp.TextContent{Text: string(data)}}}, nil, nil
}

func (s *Server) sources(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, stored, err := s.artifact(ctx)
	if err != nil {
		return nil, nil, err
	}
	var b strings.Builder
	for _, source := range a.Sources {
		m := source.Meta
		fmt.Fprintf(&b, "%s  %s  %d  %s..%s  %s\n", m.Name, m.Kind, m.Lines, m.Since, m.Until, m.Note)
	}
	fmt.Fprintf(&b, "artifact expires %s\n", stored.ExpiresAt.Format(time.RFC3339))
	return result(b.String(), map[string]any{"sources": len(a.Sources)})
}

func (s *Server) summary(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, stored, err := s.artifact(ctx)
	if err != nil {
		return nil, nil, err
	}
	var total int64
	for _, source := range a.Sources {
		total += source.Meta.Lines
	}
	r := a.Manifest.Redaction
	var b strings.Builder
	fmt.Fprintf(&b, "created %s\nexpires %s\nsources %d\ntotal lines %d\nlines redacted %d\nby category\n", a.Manifest.CreatedAt.Format(time.RFC3339), stored.ExpiresAt.Format(time.RFC3339), len(a.Sources), total, r.LinesRedacted)
	for _, category := range protocol.RedactionCategories {
		count := r.ByCategory[category]
		fmt.Fprintf(&b, "  %s  %d values  %d lines\n", category, count.Values, count.Lines)
	}
	fmt.Fprintf(&b, "flags %d\nrules %d\n", r.Flags, r.Rules)
	return result(b.String(), map[string]any{"lines": total, "lines_redacted": r.LinesRedacted, "flags": r.Flags})
}

func defaultInt(req *mcp.CallToolRequest, name string, value, fallback int) int {
	if value != 0 {
		return value
	}
	if req != nil && req.Params != nil {
		var args map[string]json.RawMessage
		if json.Unmarshal(req.Params.Arguments, &args) == nil {
			if _, ok := args[name]; ok {
				return value
			}
		}
	}
	return fallback
}

func formatLine(line artifact.Line) string { return fmt.Sprintf("%d: %s\n", line.N, line.Text) }

func lineResult(lines []artifact.Line, tail bool, readTo int) (*mcp.CallToolResult, any, error) {
	if tail {
		size := 0
		start := len(lines)
		for start > 0 {
			n := len(formatLine(lines[start-1]))
			if size+n > textCap {
				break
			}
			size += n
			start--
		}
		lines = lines[start:]
	}
	var b strings.Builder
	end := 0
	for _, line := range lines {
		text := formatLine(line)
		if b.Len()+len(text) > textCap {
			break
		}
		b.WriteString(text)
		end++
	}
	next := 0
	if end < len(lines) {
		next = lines[end].N
	}
	lines = lines[:end]
	meta := map[string]any{"lines": len(lines), "lines_redacted": artifact.RedactedLines(lines)}
	first, last := 0, 0
	if len(lines) > 0 {
		first, last = lines[0].N, lines[len(lines)-1].N
	}
	if readTo > 0 {
		if next == 0 && last > 0 && last < readTo {
			next = last + 1
		}
		if next > 0 {
			meta["next_from"] = next
		}
	} else {
		meta["first_line"], meta["last_line"] = first, last
	}
	return result(b.String(), meta)
}

func (s *Server) tail(ctx context.Context, req *mcp.CallToolRequest, in tailArgs) (*mcp.CallToolResult, any, error) {
	in.N = defaultInt(req, "n", in.N, 100)
	if in.N < 1 || in.N > 500 {
		return nil, nil, fmt.Errorf("n: must be between 1 and 500")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, _, err := s.artifact(ctx)
	if err != nil {
		return nil, nil, err
	}
	source, err := a.Source(in.Source)
	if err != nil {
		return nil, nil, err
	}
	return lineResult(source.Tail(in.N), true, 0)
}
func (s *Server) read(ctx context.Context, _ *mcp.CallToolRequest, in readArgs) (*mcp.CallToolResult, any, error) {
	if in.From < 1 {
		return nil, nil, fmt.Errorf("from: must be at least 1")
	}
	if in.To < in.From {
		return nil, nil, fmt.Errorf("to: must be at least from")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, _, err := s.artifact(ctx)
	if err != nil {
		return nil, nil, err
	}
	source, err := a.Source(in.Source)
	if err != nil {
		return nil, nil, err
	}
	if in.From > len(source.Lines) {
		return nil, nil, fmt.Errorf("from: beyond the last line (%d)", len(source.Lines))
	}
	to := min(in.To, len(source.Lines))
	end := in.From + min(499, in.To-in.From)
	return lineResult(source.Read(in.From, end), false, max(1, to))
}
func (s *Server) context(ctx context.Context, req *mcp.CallToolRequest, in contextArgs) (*mcp.CallToolResult, any, error) {
	in.Around = defaultInt(req, "around", in.Around, 20)
	if in.Line < 1 {
		return nil, nil, fmt.Errorf("line: must be at least 1")
	}
	if in.Around < 0 || in.Around > 200 {
		return nil, nil, fmt.Errorf("around: must be between 0 and 200")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, _, err := s.artifact(ctx)
	if err != nil {
		return nil, nil, err
	}
	source, err := a.Source(in.Source)
	if err != nil {
		return nil, nil, err
	}
	if in.Line > len(source.Lines) {
		return nil, nil, fmt.Errorf("line: beyond the last line (%d)", len(source.Lines))
	}
	return lineResult(source.Context(in.Line, in.Around), false, 0)
}
func (s *Server) join(ctx context.Context, _ *mcp.CallToolRequest, in joinArgs) (*mcp.CallToolResult, any, error) {
	token, err := protocol.ParseToken(in.Token)
	if err != nil {
		return nil, nil, err
	}
	defer token.Zero()
	relay := s.Relay
	if relay == "" {
		relay = protocol.DefaultRelayURL
	}
	client, err := s.client(relay, token.RelayCredential())
	if err != nil {
		return nil, nil, err
	}
	info, err := client.Session(ctx, token.SessionID())
	if protocol.IsNotFound(err) {
		return nil, nil, fmt.Errorf("no artifact for this token on %s: it expired, was deleted, or the upload did not finish", client.BaseURL)
	}
	if err != nil {
		return nil, nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stored := session.Session{Version: 1, Token: token.Encode(), Relay: client.BaseURL, SessionID: token.SessionID().String(), ExpiresAt: info.ExpiresAt, JoinedAt: s.now(), JoinedVia: "chat"}
	if err := session.Save(s.SessionPath, stored); err != nil {
		return nil, nil, err
	}
	s.loaded, s.loadedFor = nil, ""
	return result(fmt.Sprintf("Joined session %s. The artifact expires at %s. This token has been in the chat transcript.", stored.SessionID, stored.ExpiresAt.Format(time.RFC3339)), map[string]any{"session_id": stored.SessionID, "expires_at": stored.ExpiresAt.Format(time.RFC3339)})
}

func (s *Server) search(ctx context.Context, req *mcp.CallToolRequest, in searchArgs) (*mcp.CallToolResult, any, error) {
	in.Max = defaultInt(req, "max", in.Max, 50)
	if in.Before < 0 || in.Before > 50 {
		return nil, nil, fmt.Errorf("before: must be between 0 and 50")
	}
	if in.After < 0 || in.After > 50 {
		return nil, nil, fmt.Errorf("after: must be between 0 and 50")
	}
	if in.Max < 1 || in.Max > 200 {
		return nil, nil, fmt.Errorf("max: must be between 1 and 200")
	}
	regex, err := regexp.Compile(in.Regex)
	if err != nil {
		return nil, nil, fmt.Errorf("regex: invalid RE2 regular expression")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, stored, err := s.artifact(ctx)
	if err != nil {
		return nil, nil, err
	}
	position := struct {
		SessionID string `json:"sid"`
		Source    int    `json:"s"`
		Line      int    `json:"l"`
	}{SessionID: stored.SessionID, Line: 1}
	if in.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(in.Cursor)
		var fields map[string]json.RawMessage
		if err != nil || json.Unmarshal(raw, &fields) != nil || len(fields) != 3 || fields["sid"] == nil || fields["s"] == nil || fields["l"] == nil || string(fields["sid"]) == "null" || string(fields["s"]) == "null" || string(fields["l"]) == "null" || json.Unmarshal(raw, &position) != nil || position.SessionID != stored.SessionID || position.Source < 0 || position.Source >= len(a.Sources) || position.Line < 1 || position.Line > len(a.Sources[position.Source].Lines)+1 {
			return nil, nil, fmt.Errorf("cursor: invalid cursor")
		}
		if in.Source != "" && a.Sources[position.Source].Meta.Name != in.Source {
			return nil, nil, fmt.Errorf("cursor: source does not match")
		}
	}
	found, err := a.Search(artifact.SearchQuery{Regex: regex, Source: in.Source, Before: in.Before, After: in.After, Max: in.Max, StartSource: position.Source, StartLine: position.Line})
	if err != nil {
		return nil, nil, err
	}
	var b strings.Builder
	end, matches := 0, 0
	position.Source, position.Line = found.NextSource, found.NextLine
	truncated := !found.Done
	for _, line := range found.Lines {
		marker, text := "-", line.Text
		if line.Match {
			marker = ":"
			if len(text) > 2000 {
				text = strings.ToValidUTF8(text[:2000], "�") + fmt.Sprintf(" …[+%d bytes]", len(text)-2000)
			}
		}
		formatted := fmt.Sprintf("%s:%d%s %s\n", line.Source, line.N, marker, text)
		if b.Len()+len(formatted) > textCap {
			truncated = true
			for i, source := range a.Sources {
				if source.Meta.Name == line.Source {
					position.Source = i
					break
				}
			}
			position.Line = line.N
			break
		}
		b.WriteString(formatted)
		found.Lines[end].Text = text
		end++
		if line.Match {
			matches++
		}
	}
	cursor := ""
	if truncated {
		raw, err := json.Marshal(position)
		if err != nil {
			return nil, nil, err
		}
		cursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	return result(b.String(), map[string]any{"matches": matches, "lines_redacted": artifact.RedactedLines(found.Lines[:end]), "truncated": truncated, "cursor": cursor})
}
