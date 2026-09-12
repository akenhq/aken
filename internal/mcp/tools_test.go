// SPDX-License-Identifier: Apache-2.0
package mcp

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/akenhq/aken/protocol"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, map[string]any) {
	t.Helper()
	got, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if got.IsError || len(got.Content) != 2 || got.StructuredContent != nil {
		t.Fatalf("%s result = %+v", name, got)
	}
	first, ok := got.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatal("first content is not text")
	}
	second, ok := got.Content[1].(*mcp.TextContent)
	if !ok {
		t.Fatal("second content is not text")
	}
	if strings.Contains(second.Text, "\n") {
		t.Fatal("metadata spans lines")
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(second.Text), &meta); err != nil {
		t.Fatal(err)
	}
	if len(first.Text) > textCap {
		t.Fatalf("text exceeds cap: %d", len(first.Text))
	}
	return first.Text, meta
}

func TestTools(t *testing.T) {
	s, stored := fixture(t, "first\nerror <ip#1>\nafter\nerror again\nlast\n", "other\n")
	cs := connect(t, s)
	for _, tt := range []struct {
		name string
		args map[string]any
		text string
		meta string
	}{
		{"sources", map[string]any{}, "a  file  5  ..  test\nb  file  1  ..  test\nartifact expires " + stored.ExpiresAt.Format(time.RFC3339) + "\n", `{"sources":2}`},
		{"tail", map[string]any{"source": "a", "n": 2}, "4: error again\n5: last\n", `{"lines":2,"lines_redacted":0,"first_line":4,"last_line":5}`},
		{"read", map[string]any{"source": "a", "from": 1, "to": 2}, "1: first\n2: error <ip#1>\n", `{"lines":2,"lines_redacted":1}`},
		{"context", map[string]any{"source": "a", "line": 2, "around": 0}, "2: error <ip#1>\n", `{"lines":1,"lines_redacted":1,"first_line":2,"last_line":2}`},
		{"context", map[string]any{"source": "a", "line": 2}, "1: first\n2: error <ip#1>\n3: after\n4: error again\n5: last\n", `{"lines":5,"lines_redacted":1,"first_line":1,"last_line":5}`},
		{"search", map[string]any{"regex": "error", "before": 1, "after": 1}, "a:1- first\na:2: error <ip#1>\na:3- after\na:4: error again\na:5- last\n", `{"matches":2,"lines_redacted":1,"truncated":false,"cursor":""}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			text, meta := call(t, cs, tt.name, tt.args)
			if text != tt.text {
				t.Fatalf("text = %q, want %q", text, tt.text)
			}
			data, err := json.Marshal(meta)
			if err != nil {
				t.Fatal(err)
			}
			var want map[string]any
			if err := json.Unmarshal([]byte(tt.meta), &want); err != nil {
				t.Fatal(err)
			}
			expected, err := json.Marshal(want)
			if err != nil || string(data) != string(expected) {
				t.Fatalf("metadata = %s, want %s", data, expected)
			}
		})
	}
	text, meta := call(t, cs, "summary", map[string]any{})
	for _, fragment := range []string{"created ", "expires ", "sources 2", "total lines 6", "lines redacted 1", "ip  1 values  1 lines", "flags 2", "rules 14"} {
		if !strings.Contains(text, fragment) {
			t.Errorf("summary missing %q", fragment)
		}
	}
	if meta["lines"] != float64(6) || meta["lines_redacted"] != float64(1) || meta["flags"] != float64(2) {
		t.Fatal(meta)
	}
}

func TestToolErrors(t *testing.T) {
	s, _ := fixture(t, "line\n")
	s.AllowChatJoin = true
	cs := connect(t, s)
	for _, tt := range []struct {
		name    string
		args    map[string]any
		message string
	}{
		{"search", map[string]any{"regex": "[private-token"}, "regex:"},
		{"search", map[string]any{"regex": "x", "before": -1}, "before:"},
		{"search", map[string]any{"regex": "x", "after": 51}, "after:"},
		{"search", map[string]any{"regex": "x", "max": 0}, "max:"},
		{"search", map[string]any{"regex": "x", "max": 201}, "max:"},
		{"search", map[string]any{"regex": "x", "cursor": "private-token"}, "cursor:"},
		{"search", map[string]any{"regex": "x", "cursor": base64.RawURLEncoding.EncodeToString([]byte(`{"sid":"00000000000000000000000000000000","s":0,"l":1}`))}, "cursor: invalid cursor"},
		{"search", map[string]any{"regex": "x", "cursor": base64.RawURLEncoding.EncodeToString([]byte(`{"s":0,"l":1}`))}, "cursor: invalid cursor"},
		{"tail", map[string]any{"source": "a", "n": 0}, "n:"},
		{"tail", map[string]any{"source": "a", "n": 501}, "n:"},
		{"read", map[string]any{"source": "a", "from": 0, "to": 1}, "from:"},
		{"read", map[string]any{"source": "a", "from": 2, "to": 1}, "to:"},
		{"read", map[string]any{"source": "a", "from": 2, "to": 2}, "from: beyond the last line (1)"},
		{"context", map[string]any{"source": "a", "line": 0}, "line:"},
		{"context", map[string]any{"source": "a", "line": 2}, "line: beyond the last line (1)"},
		{"context", map[string]any{"source": "a", "line": 1, "around": 201}, "around:"},
		{"tail", map[string]any{"source": "private-token"}, "available sources:"},
		{"join", map[string]any{"token": "private-token"}, "invalid token"},
		{"join", map[string]any{"token": "unused", "relay": "https://example.com"}, "relay"},
	} {
		t.Run(tt.name+tt.message, func(t *testing.T) {
			got, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: tt.name, Arguments: tt.args})
			if err != nil || !got.IsError {
				t.Fatalf("expected tool error: %+v, %v", got, err)
			}
			text := got.Content[0].(*mcp.TextContent).Text
			if !strings.Contains(text, tt.message) || strings.Contains(text, "private-token") {
				t.Fatalf("error text = %q", text)
			}
		})
	}
	s.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	got, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "sources", Arguments: map[string]any{}})
	if err != nil || !got.IsError || !strings.Contains(got.Content[0].(*mcp.TextContent).Text, "the session expired at") {
		t.Fatalf("expiry = %+v, %v", got, err)
	}
}

func TestLineCaps(t *testing.T) {
	for _, width := range []int{10, 1000} {
		s, _ := fixture(t, strings.Repeat(strings.Repeat("x", width)+" <ip#1>\n", 600))
		cs := connect(t, s)
		text, meta := call(t, cs, "read", map[string]any{"source": "a", "from": 1, "to": 600})
		count := int(meta["lines"].(float64))
		if count > 500 || meta["next_from"] != float64(count+1) || meta["lines_redacted"] != float64(count) || strings.Count(text, "\n") != count {
			t.Fatal(meta)
		}
		_, next := call(t, cs, "read", map[string]any{"source": "a", "from": count + 1, "to": 600})
		if int(next["lines"].(float64)) == 0 {
			t.Fatal("read did not advance")
		}
		_, tail := call(t, cs, "tail", map[string]any{"source": "a", "n": 500})
		if tail["last_line"] != float64(600) || tail["first_line"] != 601-tail["lines"].(float64) {
			t.Fatal(tail)
		}
		_, context := call(t, cs, "context", map[string]any{"source": "a", "line": 300, "around": 200})
		if context["first_line"] != float64(100) {
			t.Fatal(context)
		}
	}
}

func TestSearchAdjacentMatches(t *testing.T) {
	s, _ := fixture(t, "error one\nerror two\n")
	text, meta := call(t, connect(t, s), "search", map[string]any{"regex": "error", "after": 1, "max": 1})
	if text != "a:1: error one\na:2: error two\n" || meta["matches"] != float64(2) || meta["truncated"] != false || meta["cursor"] != "" {
		t.Fatalf("search = %q, %v", text, meta)
	}
}

func TestSearchPages(t *testing.T) {
	var plain strings.Builder
	for i := range 220 {
		fmt.Fprintf(&plain, "match %d %s\n", i, strings.Repeat("x", 2100))
	}
	s, stored := fixture(t, plain.String(), "match final\n")
	cs := connect(t, s)
	args := map[string]any{"regex": "match", "max": 200}
	matches := 0
	for pages := 0; pages < 5; pages++ {
		text, meta := call(t, cs, "search", args)
		matches += int(meta["matches"].(float64))
		if pages == 0 && !strings.Contains(text, " …[+108 bytes]") {
			t.Fatalf("missing long-line suffix: %.100s", text)
		}
		cursor := meta["cursor"].(string)
		if cursor == "" {
			break
		}
		if meta["truncated"] != true {
			t.Fatal("cursor without truncation")
		}
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			t.Fatal(err)
		}
		var position map[string]any
		if err := json.Unmarshal(raw, &position); err != nil || len(position) != 3 || position["sid"] != stored.SessionID {
			t.Fatalf("cursor = %s, %v", raw, err)
		}
		args["cursor"] = cursor
	}
	if matches != 221 {
		t.Fatalf("matches = %d, want 221", matches)
	}
}

func TestSearchRedactionAfterCut(t *testing.T) {
	s, _ := fixture(t, "match "+strings.Repeat("x", 2100)+" <ip#1>\n")
	_, meta := call(t, connect(t, s), "search", map[string]any{"regex": "match"})
	if meta["lines_redacted"] != float64(0) {
		t.Fatal("counted a placeholder outside the returned text")
	}
}

func TestJoinRelay(t *testing.T) {
	for _, relay := range []string{"", "https://example.com"} {
		token := protocol.NewToken()
		want := relay
		if want == "" {
			want = protocol.DefaultRelayURL
		}
		stop := errors.New("test client")
		s := &Server{Relay: relay, NewClient: func(got string, credential [32]byte) (*protocol.RelayClient, error) {
			if got != want || credential != token.RelayCredential() {
				t.Fatalf("join used relay %q or incorrect credential", got)
			}
			return nil, stop
		}}
		if _, _, err := s.join(t.Context(), nil, joinArgs{Token: token.Encode()}); !errors.Is(err, stop) {
			t.Fatalf("join error = %v", err)
		}
	}
}
