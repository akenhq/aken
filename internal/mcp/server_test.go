// SPDX-License-Identifier: Apache-2.0
package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/akenhq/aken/internal/artifact"
	"github.com/akenhq/aken/internal/session"
	"github.com/akenhq/aken/protocol"
	"github.com/akenhq/aken/relay"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func connect(t *testing.T, s *Server) *mcp.ClientSession {
	t.Helper()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := s.MCP().Connect(t.Context(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close(); _ = ss.Close() })
	return cs
}

func fixture(t *testing.T, parts ...string) (*Server, session.Session) {
	t.Helper()
	token := protocol.NewToken()
	now := time.Now().UTC().Truncate(time.Second)
	stored := session.Session{Version: 2, Mode: "blob", Token: token.Encode(), Relay: protocol.DefaultRelayURL, SessionID: token.SessionID().String(), ExpiresAt: now.Add(time.Hour), JoinedAt: now, JoinedVia: "cli"}
	s := &Server{SessionPath: filepath.Join(t.TempDir(), "session.json"), Version: "test", Now: func() time.Time { return now }}
	if err := session.Save(s.SessionPath, stored); err != nil {
		t.Fatal(err)
	}
	m := protocol.Manifest{Version: 1, ChunkSize: protocol.ChunkSize, CreatedAt: now, Redaction: protocol.RedactionSummary{LinesRedacted: 1, Flags: 2, Rules: 14, ByCategory: map[string]protocol.CategoryCount{"ip": {Values: 1, Lines: 1}}}}
	var plain []byte
	for i, part := range parts {
		m.Sources = append(m.Sources, protocol.ManifestSource{Name: string(rune('a' + i)), Kind: "file", Offset: int64(len(plain)), Bytes: int64(len(part)), Lines: int64(strings.Count(part, "\n")), Note: "test"})
		plain = append(plain, part...)
	}
	m.TotalBytes = int64(len(plain))
	m.ChunkCount, _ = protocol.ChunkCount(m.TotalBytes)
	for range m.ChunkCount {
		m.ChunksSHA256 = append(m.ChunksSHA256, strings.Repeat("0", 64))
	}
	a, err := artifact.FromPlaintext(m, plain)
	if err != nil {
		t.Fatal(err)
	}
	s.loaded, s.loadedFor = a, stored.SessionID
	return s, stored
}

func TestServer(t *testing.T) {
	for _, allow := range []bool{false, true} {
		s := &Server{SessionPath: filepath.Join(t.TempDir(), "session.json"), AllowChatJoin: allow, Version: "test"}
		cs := connect(t, s)
		initialized := cs.InitializeResult()
		if initialized.Instructions != "No session is joined yet: ask the human to run aken-mcp join on their machine.\n"+instructions || initialized.ServerInfo.Name != "aken-mcp" || initialized.ServerInfo.Version != "test" {
			t.Fatalf("initialize = %+v", initialized)
		}
		caps, err := json.Marshal(initialized.Capabilities)
		if err != nil || string(caps) != `{"tools":{}}` {
			t.Fatalf("capabilities = %s, %v", caps, err)
		}
		list, err := cs.ListTools(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, tool := range list.Tools {
			names = append(names, tool.Name)
			if tool.OutputSchema != nil {
				t.Errorf("%s has output schema", tool.Name)
			}
			if tool.Name != "join" && (tool.Annotations == nil || !tool.Annotations.ReadOnlyHint) {
				t.Errorf("%s is not read-only", tool.Name)
			}
			schema, err := json.Marshal(tool.InputSchema)
			if tool.Name == "join" && (strings.Contains(string(schema), `"relay"`) || !strings.Contains(string(schema), `"required":["token"]`) || tool.Description != "Store a session token so the tools can read its artifact on the relay this server was started with. The token then sits in this transcript; prefer aken-mcp join in a terminal.") {
				t.Fatalf("join contract = %s, %q", schema, tool.Description)
			}
			if err != nil || !strings.Contains(string(schema), `"additionalProperties":false`) {
				t.Fatalf("schema = %s, %v", schema, err)
			}
			var input struct {
				Properties map[string]struct {
					Description string
					Type        any
				}
			}
			if err := json.Unmarshal(schema, &input); err != nil {
				t.Fatal(err)
			}
			for name, property := range input.Properties {
				if property.Description == "" {
					t.Errorf("%s.%s has no description", tool.Name, name)
				}
				if tool.Name == "plan" && name == "jobs" && property.Type != "array" {
					t.Errorf("plan.jobs type = %v", property.Type)
				}
			}
		}
		want := []string{"context", "read", "search", "sources", "summary", "tail", "list_dir", "read_file", "search_files", "tail_file", "journal", "docker_logs", "systemctl_status", "ps", "df", "plan", "result"}
		if allow {
			want = append(want, "join")
		}
		slices.Sort(names)
		slices.Sort(want)
		if !reflect.DeepEqual(names, want) {
			t.Fatalf("tools = %v", names)
		}
		got, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "sources", Arguments: map[string]any{}})
		if err != nil || !got.IsError || !strings.Contains(got.Content[0].(*mcp.TextContent).Text, "no session") {
			t.Fatalf("no session = %+v, %v", got, err)
		}
	}
}

func TestLoadCacheAndJoin(t *testing.T) {
	s, stored := fixture(t, "hello <ip#1>\n")
	server := httptest.NewServer(relay.NewHandler(relay.NewMemoryStore(), relay.Options{}))
	defer server.Close()
	token, err := stored.ParsedToken()
	if err != nil {
		t.Fatal(err)
	}
	client, err := protocol.NewRelayClient(server.URL, token.RelayCredential())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateSession(t.Context(), token.SessionID(), time.Hour, 1); err != nil {
		t.Fatal(err)
	}
	keys := protocol.DeriveBlobKeys(token.ContentRoot())
	chunk, err := keys.SealChunk(0, 1, []byte("hello <ip#1>\n"))
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(chunk)
	m := s.loaded.Manifest
	m.ChunksSHA256[0] = hex.EncodeToString(hash[:])
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := keys.SealManifest(1, raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.PutChunk(t.Context(), token.SessionID(), 0, chunk); err != nil {
		t.Fatal(err)
	}
	if err := client.PutManifest(t.Context(), token.SessionID(), encrypted); err != nil {
		t.Fatal(err)
	}
	stored.Relay = server.URL
	if err := session.Save(s.SessionPath, stored); err != nil {
		t.Fatal(err)
	}
	s.loaded = nil
	s.AllowChatJoin = true
	s.Relay = server.URL
	cs := connect(t, s)
	call(t, cs, "sources", map[string]any{})
	if err := client.DeleteSession(t.Context(), token.SessionID()); err != nil {
		t.Fatal(err)
	}
	call(t, cs, "tail", map[string]any{"source": "a"})
	if _, err := client.CreateSession(t.Context(), token.SessionID(), time.Hour, 1); err != nil {
		t.Fatal(err)
	}
	text, meta := call(t, cs, "join", map[string]any{"token": token.Encode()})
	if !strings.HasSuffix(text, "This token has been in the chat transcript.") || meta["session_id"] != stored.SessionID {
		t.Fatalf("join = %s, %v", text, meta)
	}
	joined, err := session.Load(s.SessionPath)
	if err != nil || joined.JoinedVia != "chat" || joined.Relay != server.URL || s.loaded != nil {
		t.Fatalf("join did not replace session: %v", err)
	}
	got, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "sources", Arguments: map[string]any{}})
	if err != nil || !got.IsError || !strings.Contains(got.Content[0].(*mcp.TextContent).Text, "artifact not found") {
		t.Fatalf("missing manifest = %+v, %v", got, err)
	}
}

func TestSessionInstructions(t *testing.T) {
	blob, stored := fixture(t, "line\n")
	live, _, _, _ := liveFixture(t)
	liveStored, err := session.Load(live.SessionPath)
	if err != nil {
		t.Fatal(err)
	}
	broken := &Server{SessionPath: filepath.Join(t.TempDir(), "session.json")}
	if err := os.WriteFile(broken.SessionPath, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		s    *Server
		line string
	}{
		{blob, fmt.Sprintf("Current session: one-shot artifact %s, expires %s. Use summary, sources, search, tail, read and context.", stored.SessionID, stored.ExpiresAt.UTC().Format(time.RFC3339))},
		{live, fmt.Sprintf("Current session: live session %s, expires %s. Use list_dir, read_file, search_files, tail_file, journal, docker_logs, systemctl_status, ps, df and plan.", liveStored.SessionID, liveStored.ExpiresAt.UTC().Format(time.RFC3339))},
		{broken, "No session is joined yet: ask the human to run aken-mcp join on their machine."},
	} {
		if got := connect(t, tt.s).InitializeResult().Instructions; got != tt.line+"\n"+instructions {
			t.Fatalf("instructions = %q", got)
		}
	}
}
