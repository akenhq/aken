// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akenhq/aken/internal/devrelay"
	"github.com/akenhq/aken/internal/session"
	"github.com/akenhq/aken/protocol"
)

func TestRun(t *testing.T) {
	original := sessionPath
	sessionPath = filepath.Join(t.TempDir(), "session.json")
	t.Cleanup(func() { sessionPath = original })
	for _, tt := range []struct {
		name           string
		args           []string
		code           int
		stdout, stderr string
	}{
		{"version", []string{"version"}, 0, "aken-mcp dev (", ""},
		{"help", []string{"help"}, 0, usage, ""},
		{"long help", []string{"--help"}, 0, usage, ""},
		{"short help", []string{"-h"}, 0, usage, ""},
		{"no args", nil, 2, "", usage},
		{"unknown", []string{"bogus"}, 2, "", "aken-mcp: unknown command\n"},
		{"join help", []string{"join", "--help"}, 0, joinUsage, ""},
		{"serve help", []string{"serve", "--help"}, 0, serveUsage, ""},
		{"serve relay", []string{"serve", "--relay", "https://example.com", "--help"}, 0, serveUsage, ""},
		{"serve missing relay", []string{"serve", "--relay"}, 2, serveUsage, "aken-mcp: invalid flags\n"},
		{"status help", []string{"status", "--help"}, 0, statusUsage, ""},
		{"end help", []string{"end", "--help"}, 0, endUsage, ""},
		{"status missing", []string{"status"}, 1, "", "aken-mcp: no session\n"},
		{"end missing", []string{"end"}, 1, "", "aken-mcp: no session\n"},
		{"invalid token", []string{"join", "private-token"}, 1, "", "aken-mcp: invalid token\n"},
		{"empty token", []string{"join"}, 1, "", "aken-mcp: invalid token\n"},
		{"extra token", []string{"join", "private-token", "extra"}, 2, "", "aken-mcp: unexpected arguments\n"},
		{"serve extra", []string{"serve", "extra"}, 2, "", "aken-mcp: unexpected arguments\n"},
		{"bad flag", []string{"join", "--private-token"}, 2, joinUsage, "aken-mcp: invalid flags\n"},
		{"missing relay", []string{"join", "--relay"}, 2, joinUsage, "aken-mcp: invalid flags\n"},
		{"bad bool", []string{"serve", "--allow-chat-join=private-token"}, 2, serveUsage, "aken-mcp: invalid flags\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(tt.args, strings.NewReader(""), &stdout, &stderr); code != tt.code {
				t.Fatalf("exit = %d, want %d", code, tt.code)
			}
			for _, output := range []struct{ name, got, want string }{{"stdout", stdout.String(), tt.stdout}, {"stderr", stderr.String(), tt.stderr}} {
				if !strings.HasPrefix(output.got, output.want) || (output.want == "" && output.got != "") {
					t.Errorf("%s = %q, want prefix %q", output.name, output.got, output.want)
				}
				if strings.Contains(output.got, "private-token") {
					t.Error("token leaked")
				}
			}
		})
	}
}

func TestJoinStatusEnd(t *testing.T) {
	original := sessionPath
	sessionPath = filepath.Join(t.TempDir(), "aken", "session.json")
	t.Cleanup(func() { sessionPath = original })
	relay := httptest.NewServer(devrelay.New())
	defer relay.Close()
	token := protocol.NewToken()
	client, err := protocol.NewRelayClient(relay.URL, token.RelayCredential())
	if err != nil {
		t.Fatal(err)
	}
	invoke := func(args []string, stdin string, wantCode int, want string) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		code := run(args, strings.NewReader(stdin), &stdout, &stderr)
		if code != wantCode || !strings.Contains(stdout.String()+stderr.String(), want) {
			t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
		}
		if strings.Contains(stdout.String()+stderr.String(), token.Encode()) {
			t.Fatal("token leaked")
		}
	}
	invoke([]string{"join", "--relay", relay.URL}, token.Encode()+"\n", 1, "aken-mcp: no artifact for this token on "+relay.URL+": it expired, was deleted, or the upload did not finish\n")
	info, err := client.CreateSession(t.Context(), token.SessionID(), time.Hour, 1)
	if err != nil {
		t.Fatal(err)
	}
	joined := "Joined session " + token.SessionID().String() + ". The artifact expires at " + info.ExpiresAt.Format(time.RFC3339) + ".\n"
	invoke([]string{"join", "--relay", relay.URL}, "  "+token.Encode()+"  \nignored", 0, joined)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	if _, err := writer.WriteString("  " + token.Encode() + "  \nignored"); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"join", "--relay", relay.URL}, reader, &stdout, &stderr); code != 0 || stdout.String() != joined || stderr.Len() != 0 {
		t.Fatalf("piped join: exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
	stored, err := session.Load(sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	if stored.JoinedVia != "cli" || stored.Token != token.Encode() || stored.Relay != relay.URL || !stored.ExpiresAt.Equal(info.ExpiresAt) {
		t.Fatal("wrong stored session")
	}
	invoke([]string{"status"}, "", 0, "Session "+stored.SessionID+" on "+relay.URL)
	stored.ExpiresAt = time.Now().Add(-time.Minute)
	if err := session.Save(sessionPath, stored); err != nil {
		t.Fatal(err)
	}
	invoke([]string{"status"}, "", 0, " (expired)\n")
	invoke([]string{"join", token.Encode(), "--relay", relay.URL}, "", 0, joined)
	invoke([]string{"end"}, "", 0, "Ended session "+stored.SessionID+"; the artifact is deleted from the relay.\n")
	if _, err := client.Session(t.Context(), token.SessionID()); !protocol.IsNotFound(err) {
		t.Fatalf("not deleted: %v", err)
	}
	if err := session.Save(sessionPath, stored); err != nil {
		t.Fatal(err)
	}
	invoke([]string{"end"}, "", 0, "Ended session ")
	invoke([]string{"status"}, "", 1, "aken-mcp: no session\n")
}
