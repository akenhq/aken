// SPDX-License-Identifier: Apache-2.0
package session

import (
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
	s := Session{Version: 1, Token: token.Encode(), Relay: protocol.DefaultRelayURL, SessionID: token.SessionID().String(), ExpiresAt: now.Add(time.Hour), JoinedAt: now, JoinedVia: "cli"}
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
