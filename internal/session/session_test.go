// SPDX-License-Identifier: Apache-2.0
package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/akenhq/aken/protocol"
)

func TestSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aken", "session.json")
	if _, err := Load(path); !errors.Is(err, ErrNoSession) {
		t.Fatalf("missing: %v", err)
	}
	if err := Remove(path); err != nil {
		t.Fatal(err)
	}
	token := protocol.NewToken()
	now := time.Now().UTC()
	s := Session{Version: 2, Mode: "blob", Token: token.Encode(), Relay: protocol.DefaultRelayURL, SessionID: token.SessionID().String(), ExpiresAt: now.Add(time.Hour), JoinedAt: now, JoinedVia: "cli"}
	for _, via := range []string{"cli", "chat"} {
		s.JoinedVia = via
		if err := Save(path, s); err != nil {
			t.Fatal(err)
		}
		got, err := Load(path)
		if err != nil || !reflect.DeepEqual(got, s) {
			t.Fatalf("round trip failed: %v", err)
		}
		for _, item := range []struct {
			path string
			mode os.FileMode
		}{{path, 0o600}, {filepath.Dir(path), 0o700}} {
			info, err := os.Stat(item.path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != item.mode {
				t.Errorf("mode = %o, want %o", info.Mode().Perm(), item.mode)
			}
		}
	}
	for _, tt := range []struct {
		now     time.Time
		expired bool
	}{{now, false}, {s.ExpiresAt, true}, {s.ExpiresAt.Add(time.Second), true}} {
		if s.Expired(tt.now) != tt.expired {
			t.Errorf("Expired(%v) = %v", tt.now, !tt.expired)
		}
	}
	if err := Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); !errors.Is(err, ErrNoSession) {
		t.Fatal(err)
	}
}

func TestMalformed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	for _, data := range []string{`{`, `{}`, `{"version": 1, "token": "private-token", "expires_at": "private-time"}`, `{"version": 2}`} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Load(path)
		if err == nil || strings.Contains(err.Error(), "private") {
			t.Fatalf("unsafe or missing error: %v", err)
		}
	}
}

func TestDefaultPath(t *testing.T) {
	dir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	path, err := DefaultPath()
	if err != nil || path != filepath.Join(dir, "aken", "session.json") {
		t.Fatalf("DefaultPath = %q, %v", path, err)
	}
}

func TestCheckJoin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	token, other := protocol.NewToken(), protocol.NewToken()
	now := time.Now().UTC()
	stored := Session{Mode: "session", Token: token.Encode(), Relay: protocol.DefaultRelayURL, SessionID: token.SessionID().String(), ExpiresAt: now.Add(time.Hour), JoinedAt: now, JoinedVia: "cli", ContentRoot: protocol.EncodeKey([32]byte{1}), NextJobSeq: 1, NextResultSeq: 1}
	if err := CheckJoin(path, token); err != nil {
		t.Fatal("missing file blocked join", err)
	}
	for _, mode := range []string{"blob", "session"} {
		for _, expiry := range []time.Time{now.Add(time.Hour), now.Add(-time.Hour)} {
			stored.Mode, stored.ExpiresAt = mode, expiry
			if err := Save(path, stored); err != nil {
				t.Fatal(err)
			}
			err := CheckJoin(path, token)
			if mode == "session" {
				if err == nil || err.Error() != "this token was already used to join a live session; run aken serve again and join its new token" {
					t.Fatalf("live re-join: %v", err)
				}
			} else if err != nil {
				t.Fatal("blob re-join blocked", err)
			}
			if err := CheckJoin(path, other); err != nil {
				t.Fatal("new token blocked", err)
			}
		}
	}
	for _, raw := range []string{`{`, `{}`, `{"mode":"session","session_id":"` + stored.SessionID + `"}`} {
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := CheckJoin(path, token); err != nil {
			t.Fatal("malformed file blocked join", err)
		}
	}
}

func TestLiveSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	token := protocol.NewToken()
	now := time.Now().UTC()
	stored := Session{Version: 2, Mode: "session", Token: token.Encode(), Relay: protocol.DefaultRelayURL, SessionID: token.SessionID().String(), ExpiresAt: now.Add(time.Hour), JoinedAt: now, JoinedVia: "cli", ContentRoot: protocol.EncodeKey([32]byte{1}), NextJobSeq: 7, NextResultSeq: 11}
	if err := Save(path, stored); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || !reflect.DeepEqual(got, stored) || !got.Live() {
		t.Fatalf("live round trip: %v", err)
	}
	root, err := got.ParsedContentRoot()
	if err != nil || root != [32]byte{1} {
		t.Fatal("content root did not round trip")
	}
	for _, mutate := range []func(*Session){
		func(s *Session) { s.Version = 3 },
		func(s *Session) { s.Mode = "private-mode" },
		func(s *Session) { s.ContentRoot = "private-root" },
		func(s *Session) { s.ContentRoot = "" },
		func(s *Session) { s.ContentRoot += "=" },
		func(s *Session) { s.NextJobSeq = 0 },
		func(s *Session) { s.NextResultSeq = 0 },
		func(s *Session) { s.NextJobSeq = protocol.MaxSessionSeq + 2 },
		func(s *Session) { s.NextResultSeq = protocol.MaxSessionSeq + 2 },
		func(s *Session) { s.SessionID = strings.Repeat("0", 32) },
	} {
		bad := stored
		mutate(&bad)
		raw, err := json.Marshal(bad)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil || strings.Contains(err.Error(), "private") {
			t.Fatalf("unsafe or missing error: %v", err)
		}
	}
}

func TestLegacySession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	token := protocol.NewToken()
	now := time.Now().UTC()
	for _, version := range []int{1, 2} {
		stored := Session{Version: version, Token: token.Encode(), Relay: protocol.DefaultRelayURL, SessionID: token.SessionID().String(), ExpiresAt: now.Add(time.Hour), JoinedAt: now, JoinedVia: "cli"}
		raw, err := json.Marshal(stored)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := Load(path)
		if err != nil || got.Mode != "blob" || got.Live() {
			t.Fatalf("legacy load: %v", err)
		}
		if err := Save(path, got); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		if fields["version"] != float64(2) || fields["mode"] != "blob" || fields["content_root"] != nil || fields["next_job_seq"] != nil || fields["next_result_seq"] != nil {
			t.Fatal("wrong version 2 blob fields")
		}
	}
}
