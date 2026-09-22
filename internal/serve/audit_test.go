// SPDX-License-Identifier: Apache-2.0
package serve

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akenhq/aken/internal/collect"
	"github.com/akenhq/aken/protocol"
)

func TestAudit(t *testing.T) {
	now := time.Date(2026, 9, 13, 14, 0, 0, 0, time.UTC)
	id := protocol.SessionID{0x1a, 0x2b, 0x3c, 0x4d}
	o := Options{StateDir: t.TempDir(), Level: 1, RelayURL: protocol.DefaultRelayURL, Argv: []string{"serve", "--level", "1"}}
	a, err := newAudit(o, []string{"/var/log"}, id, now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.log.Close() }()
	if filepath.Base(a.dir) != "20260913T140000Z-1a2b3c4d" {
		t.Fatal(a.dir)
	}
	p := preparedJob{job: job("j1", "read_file", protocol.ReadFileParams{Path: "/var/log/app"}), paths: []string{"/var/log/app"}}
	r := protocol.Result{ID: "j1", Status: "ok", Lines: []string{"1: <ip#1>", "2: end"}}
	for _, event := range []string{"received", "approved", "sent"} {
		if err := a.event(now, 1, p, event, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.result(1, r); err != nil {
		t.Fatal(err)
	}
	if err := a.result(1, r); err == nil {
		t.Fatal("overwrote result")
	}
	mapping := map[string]string{"<ip#1>": "203.0.113.5"}
	if err := a.mapping(mapping); err != nil {
		t.Fatal(err)
	}
	mapping["<ip#2>"] = "203.0.113.6"
	if err := a.mapping(mapping); err != nil {
		t.Fatal(err)
	}
	a.session.JoinedAt = now.Add(time.Minute)
	a.session.JoinedVia = "cli"
	a.session.EndedAt = now.Add(2 * time.Minute)
	if err := a.writeSession(); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		path string
		mode os.FileMode
	}{{"", 0o700}, {"results", 0o700}, {"results/1.txt", 0o400}, {"jobs.log", 0o600}, {"session.json", 0o600}, {"mapping.json", 0o600}} {
		info, err := os.Stat(filepath.Join(a.dir, tt.path))
		if err != nil || info.Mode().Perm() != tt.mode {
			t.Fatal(tt.path, info, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(a.dir, "jobs.log"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 {
		t.Fatal(string(data))
	}
	for i, line := range lines {
		var event auditEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil || event.Event != []string{"received", "approved", "sent"}[i] || event.JobID != "j1" {
			t.Fatal(event, err)
		}
	}
	data, err = os.ReadFile(filepath.Join(a.dir, "mapping.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(data, &got); err != nil || len(got) != 2 {
		t.Fatal(got, err)
	}
	matches, err := filepath.Glob(filepath.Join(a.dir, ".aken-*"))
	if err != nil || len(matches) != 0 {
		t.Fatal(matches, err)
	}
	old := filepath.Join(o.StateDir, "sessions", "20200101T000000Z-old")
	if err := os.Mkdir(old, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := collect.Prune(filepath.Join(o.StateDir, "sessions"), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("old session kept")
	}
	if _, err := os.Stat(a.dir); err != nil {
		t.Fatal("current session pruned")
	}
}
