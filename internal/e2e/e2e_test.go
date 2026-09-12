// SPDX-License-Identifier: Apache-2.0
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/akenhq/aken/internal/collect"
	"github.com/akenhq/aken/internal/devrelay"
	akenmcp "github.com/akenhq/aken/internal/mcp"
	"github.com/akenhq/aken/internal/session"
	"github.com/akenhq/aken/protocol"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestOneShot(t *testing.T) {
	t.Log("1. Start the relay and create 40 log lines")
	relay := httptest.NewServer(devrelay.New())
	t.Cleanup(relay.Close)
	dir := t.TempDir()
	path := filepath.Join(dir, "logs", "app.log")
	state := filepath.Join(dir, "state")
	for _, directory := range []string{filepath.Dir(path), state} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	originals := []string{"203.0.113.7", "alice@example.com", "AKIAIOSFODNN7EXAMPLE", "hunter2hunter2"}
	var log strings.Builder
	for n := 1; n <= 40; n++ {
		message := "request completed"
		switch n {
		case 2:
			message = "user=" + originals[1]
		case 3:
			message = "access_key=" + originals[2]
		case 4:
			message = "password=" + originals[3]
		default:
			if n%2 == 0 {
				message += " from " + originals[0]
			}
		}
		fmt.Fprintf(&log, "event %d %s\n", n, message)
	}
	if err := os.WriteFile(path, []byte(log.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Log("2. Collect, review, and send")
	now := time.Date(2026, time.September, 12, 14, 0, 0, 0, time.UTC)
	var stdout, stderr bytes.Buffer
	code := collect.Run(t.Context(), collect.Options{
		Files: []string{path}, Allow: []string{dir}, RelayURL: relay.URL,
		TTL: time.Hour, StateDir: state, Retention: 0,
		Since: now.Add(-time.Hour), Until: now, Now: func() time.Time { return now },
		Collector: "test",
	}, strings.NewReader("s\n"), &stdout, &stderr, true, 40)
	if code != 0 {
		t.Fatalf("collect exit = %d, stderr = %q", code, stderr.String())
	}

	t.Log("3. Check the token and redaction review")
	tokens := regexp.MustCompile(`akn1_[a-z2-7]{52}`).FindAllString(stdout.String(), -1)
	if len(tokens) != 1 {
		t.Fatalf("token occurrences = %d, want 1", len(tokens))
	}
	for _, category := range []string{"ip", "email", "secret"} {
		pattern := `(?m)^  ` + category + `[ \t]+[1-9][0-9]* values?[ \t]+in[ \t]+[1-9][0-9]* lines?$`
		if !regexp.MustCompile(pattern).MatchString(stdout.String()) {
			t.Errorf("review lacks positive value and line counts for %s", category)
		}
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
	token, err := protocol.ParseToken(tokens[0])
	if err != nil {
		t.Fatal(err)
	}
	defer token.Zero()

	t.Log("4. Check the local copy, mapping, and permissions")
	runs := filepath.Join(state, "runs")
	entries, err := os.ReadDir(runs)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !entries[0].IsDir() {
		t.Fatalf("runs entries = %v, want one directory", entries)
	}
	runDir := filepath.Join(runs, entries[0].Name())
	wantName := now.Format("20060102T150405Z") + "-" + token.SessionID().String()[:8]
	if entries[0].Name() != wantName {
		t.Errorf("run directory = %q, want %q", entries[0].Name(), wantName)
	}
	info, err := entries[0].Info()
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("run directory mode = %o, want 700", info.Mode().Perm())
	}
	files, err := os.ReadDir(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 4 {
		t.Fatalf("local files = %d, want 4", len(files))
	}
	local := make(map[string]string)
	for _, file := range files {
		info, err := file.Info()
		if err != nil {
			t.Fatal(err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o400 {
			t.Errorf("%s mode = %v, want regular file with mode 400", file.Name(), info.Mode())
		}
		data, err := os.ReadFile(filepath.Join(runDir, file.Name()))
		if err != nil {
			t.Fatal(err)
		}
		local[file.Name()] = string(data)
	}
	for _, name := range []string{"artifact.txt", "manifest.json", "mapping.json", "run.json"} {
		if _, ok := local[name]; !ok {
			t.Fatalf("missing local file %s", name)
		}
	}
	placeholders := []string{"<ip#1>", "<email#1>", "<secret#1>"}
	for _, placeholder := range placeholders {
		if !strings.Contains(local["artifact.txt"], placeholder) {
			t.Errorf("artifact lacks %s", placeholder)
		}
	}
	for _, original := range originals {
		if strings.Contains(local["artifact.txt"], original) {
			t.Errorf("artifact contains original %q", original)
		}
	}
	var mapping map[string]string
	if err := json.Unmarshal([]byte(local["mapping.json"]), &mapping); err != nil {
		t.Fatal(err)
	}
	if mapping["<ip#1>"] != originals[0] {
		t.Errorf("IP mapping = %q, want %q", mapping["<ip#1>"], originals[0])
	}
	if strings.Contains(local["run.json"], "akn1_") {
		t.Error("run.json contains a token")
	}

	t.Log("5. Check that relay blobs contain no originals or placeholders")
	client, err := protocol.NewRelayClient(relay.URL, token.RelayCredential())
	if err != nil {
		t.Fatal(err)
	}
	chunk, err := client.GetChunk(t.Context(), token.SessionID(), 0)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := client.GetManifest(t.Context(), token.SessionID())
	if err != nil {
		t.Fatal(err)
	}
	for name, blob := range map[string][]byte{"chunk": chunk, "manifest": manifest} {
		for _, value := range append(slices.Clone(originals), placeholders...) {
			if bytes.Contains(blob, []byte(value)) {
				t.Errorf("relay %s contains %q", name, value)
			}
		}
		for placeholder := range mapping {
			if bytes.Contains(blob, []byte(placeholder)) {
				t.Errorf("relay %s contains %s", name, placeholder)
			}
		}
	}

	t.Log("6. Load the artifact through MCP and call all six tools")
	remote, err := client.Session(t.Context(), token.SessionID())
	if err != nil {
		t.Fatal(err)
	}
	s := &akenmcp.Server{SessionPath: filepath.Join(t.TempDir(), "session.json"), Version: "test"}
	if err := session.Save(s.SessionPath, session.Session{
		Version: 1, Token: tokens[0], Relay: relay.URL, SessionID: token.SessionID().String(),
		ExpiresAt: remote.ExpiresAt, JoinedAt: now, JoinedVia: "cli",
	}); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, s)
	initialized := cs.InitializeResult()
	if initialized.Instructions == "" || initialized.Capabilities.Tools == nil {
		t.Fatalf("initialize lacks instructions or tools capability: %+v", initialized)
	}
	listed, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"context", "read", "search", "sources", "summary", "tail"}) {
		t.Fatalf("tools = %v, want six read-only tools and no join", names)
	}
	source := "file:" + path
	for _, tt := range []struct {
		name string
		args map[string]any
		meta map[string]any
	}{
		{"sources", map[string]any{}, map[string]any{"sources": float64(1)}},
		{"summary", map[string]any{}, map[string]any{"lines": float64(40)}},
		{"search", map[string]any{"regex": "<ip#1>"}, nil},
		{"read", map[string]any{"source": source, "from": 1, "to": 5}, map[string]any{"lines": float64(5)}},
		{"tail", map[string]any{"source": source, "n": 3}, map[string]any{"lines": float64(3), "first_line": float64(38), "last_line": float64(40)}},
		{"context", map[string]any{"source": source, "line": 10, "around": 2}, map[string]any{"lines": float64(5), "first_line": float64(8), "last_line": float64(12)}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			text, meta, isError := call(t, cs, tt.name, tt.args)
			if isError {
				t.Fatalf("tool error: %s", text)
			}
			for key, want := range tt.meta {
				if meta[key] != want {
					t.Errorf("%s = %v, want %v", key, meta[key], want)
				}
			}
			if tt.name == "search" {
				for _, key := range []string{"matches", "lines_redacted"} {
					if count, ok := meta[key].(float64); !ok || count < 1 {
						t.Errorf("%s = %v, want at least 1", key, meta[key])
					}
				}
				if !strings.Contains(text, "<ip#1>") {
					t.Error("search text lacks the matching placeholder")
				}
			}
			for _, original := range originals {
				if strings.Contains(text, original) || strings.Contains(fmt.Sprint(meta), original) {
					t.Errorf("tool result contains original %q", original)
				}
			}
		})
	}

	t.Log("7. Check the missing-session guidance")
	missing := connect(t, &akenmcp.Server{SessionPath: filepath.Join(t.TempDir(), "missing.json"), Version: "test"})
	text, _, isError := call(t, missing, "sources", map[string]any{})
	if !isError || !strings.Contains(text, "aken-mcp join") {
		t.Fatalf("missing session: isError = %v, text = %q", isError, text)
	}
}

func connect(t *testing.T, s *akenmcp.Server) *sdk.ClientSession {
	t.Helper()
	st, ct := sdk.NewInMemoryTransports()
	srv := s.MCP()
	ss, err := srv.Connect(t.Context(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "e2e", Version: "test"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func call(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any) (text string, meta map[string]any, isError bool) {
	t.Helper()
	got, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Content) == 0 {
		t.Fatalf("%s returned no content", name)
	}
	first, ok := got.Content[0].(*sdk.TextContent)
	if !ok {
		t.Fatalf("%s first content is not text", name)
	}
	if got.IsError && len(got.Content) == 1 {
		return first.Text, nil, true
	}
	if len(got.Content) != 2 {
		t.Fatalf("%s content items = %d, want 2", name, len(got.Content))
	}
	second, ok := got.Content[1].(*sdk.TextContent)
	if !ok {
		t.Fatalf("%s second content is not text", name)
	}
	if strings.ContainsAny(second.Text, "\r\n") {
		t.Fatalf("%s metadata spans lines", name)
	}
	if err := json.Unmarshal([]byte(second.Text), &meta); err != nil || meta == nil {
		t.Fatalf("%s metadata is not a JSON object: %v", name, err)
	}
	return first.Text, meta, got.IsError
}
