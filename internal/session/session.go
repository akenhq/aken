// SPDX-License-Identifier: Apache-2.0
package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/akenhq/aken/protocol"
)

type Session struct {
	Version   int       `json:"version"`
	Token     string    `json:"token"`
	Relay     string    `json:"relay"`
	SessionID string    `json:"session_id"`
	ExpiresAt time.Time `json:"expires_at"`
	JoinedAt  time.Time `json:"joined_at"`
	JoinedVia string    `json:"joined_via"`
}

var ErrNoSession = errors.New("session: no session; run: aken-mcp join <token>")

func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "aken", "session.json"), nil
}

func Load(path string) (Session, error) {
	data, err := os.ReadFile(path) //nolint:gosec // The caller selects the local session file.
	if errors.Is(err, os.ErrNotExist) {
		return Session{}, ErrNoSession
	}
	if err != nil {
		return Session{}, err
	}
	var s Session
	if json.Unmarshal(data, &s) != nil || s.Version != 1 || s.Relay == "" || s.ExpiresAt.IsZero() || s.JoinedAt.IsZero() || (s.JoinedVia != "cli" && s.JoinedVia != "chat") {
		return Session{}, errors.New("session: malformed session file")
	}
	token, err := s.ParsedToken()
	if err != nil {
		return Session{}, err
	}
	defer token.Zero()
	if token.SessionID().String() != s.SessionID {
		return Session{}, errors.New("session: session id mismatch")
	}
	return s, nil
}

func Save(path string, s Session) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	directory, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	if err := directory.Chmod(".", 0o700); err != nil {
		_ = directory.Close()
		return err
	}
	if err := directory.Close(); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".session-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func Remove(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
func (s Session) ParsedToken() (protocol.Token, error) { return protocol.ParseToken(s.Token) }
func (s Session) Expired(now time.Time) bool           { return !now.Before(s.ExpiresAt) }
