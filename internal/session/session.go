// SPDX-License-Identifier: Apache-2.0
package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/akenhq/aken/protocol"
)

type Session struct {
	Version       int       `json:"version"`
	Mode          string    `json:"mode"`
	ContentRoot   string    `json:"content_root,omitempty"`
	NextJobSeq    uint64    `json:"next_job_seq,omitempty"`
	NextResultSeq uint64    `json:"next_result_seq,omitempty"`
	Token         string    `json:"token"`
	Relay         string    `json:"relay"`
	SessionID     string    `json:"session_id"`
	ExpiresAt     time.Time `json:"expires_at"`
	JoinedAt      time.Time `json:"joined_at"`
	JoinedVia     string    `json:"joined_via"`
}

var ErrNoSession = errors.New("no session; run aken-mcp join and paste the token from your server")

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
	malformed := fmt.Errorf("session: malformed session file %s; run aken-mcp join again to replace it", path)
	var s Session
	if json.Unmarshal(data, &s) != nil || (s.Version != 1 && s.Version != 2) || s.Relay == "" || s.ExpiresAt.IsZero() || s.JoinedAt.IsZero() || (s.JoinedVia != "cli" && s.JoinedVia != "chat") {
		return Session{}, malformed
	}
	if s.Version == 1 || s.Mode == "" {
		s.Mode = "blob"
	}
	if s.Mode != "blob" && s.Mode != "session" {
		return Session{}, malformed
	}
	if s.Live() {
		if _, err := s.ParsedContentRoot(); err != nil {
			return Session{}, malformed
		}
		if s.NextJobSeq < 1 || s.NextResultSeq < 1 || s.NextJobSeq > protocol.MaxSessionSeq+1 || s.NextResultSeq > protocol.MaxSessionSeq+1 {
			return Session{}, malformed
		}
	}
	token, err := s.ParsedToken()
	if err != nil {
		return Session{}, malformed
	}
	defer token.Zero()
	if token.SessionID().String() != s.SessionID {
		return Session{}, malformed
	}
	return s, nil
}

func Save(path string, s Session) error {
	s.Version = 2
	if s.Mode == "" {
		s.Mode = "blob"
	}
	if !s.Live() {
		s.ContentRoot, s.NextJobSeq, s.NextResultSeq = "", 0, 0
	}
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

func (s Session) Live() bool { return s.Mode == "session" }

func CheckJoin(path string, token protocol.Token) error {
	stored, err := Load(path)
	if errors.Is(err, ErrNoSession) {
		return nil
	}
	if err == nil && stored.Live() && stored.SessionID == token.SessionID().String() {
		return errors.New("this token was already used to join a live session; run aken serve again and join its new token")
	}
	return nil
}

func (s Session) ParsedContentRoot() ([32]byte, error) {
	root, ok := protocol.DecodeKey(s.ContentRoot)
	if !ok {
		return [32]byte{}, errors.New("session: malformed content root")
	}
	return root, nil
}

func Join(ctx context.Context, client *protocol.RelayClient, token protocol.Token, info protocol.SessionInfo, via string, now time.Time) (Session, error) {
	stored := Session{Version: 2, Mode: "blob", Token: token.Encode(), Relay: client.BaseURL, SessionID: token.SessionID().String(), ExpiresAt: info.ExpiresAt, JoinedAt: now.UTC(), JoinedVia: via}
	if info.Mode == "" || info.Mode == "blob" {
		return stored, nil
	}
	if info.Mode != "session" {
		return Session{}, errors.New("session: unknown relay session mode")
	}
	collectorKey, keyOK := protocol.DecodeKey(info.CollectorKey)
	collectorMAC, macOK := protocol.DecodeKey(info.CollectorMAC)
	if !keyOK || !macOK || !protocol.VerifyCollectorMAC(token.ExchangeKey(), token.SessionID(), collectorKey, collectorMAC) {
		return Session{}, errors.New("join rejected: bad authentication")
	}
	pair, err := protocol.GenerateKeyPair()
	if err != nil {
		return Session{}, err
	}
	mac := protocol.MCPMAC(token.ExchangeKey(), token.SessionID(), collectorKey, pair.Public())
	joined, err := client.Join(ctx, token.SessionID(), pair.Public(), mac, via)
	if protocol.IsNotFound(err) {
		return Session{}, errors.New("this relay does not support persistent sessions")
	}
	if err != nil {
		return Session{}, err
	}
	joinedKey, keyOK := protocol.DecodeKey(joined.CollectorKey)
	joinedMAC, macOK := protocol.DecodeKey(joined.CollectorMAC)
	if !keyOK || !macOK || joinedKey != collectorKey || !protocol.VerifyCollectorMAC(token.ExchangeKey(), token.SessionID(), joinedKey, joinedMAC) {
		return Session{}, errors.New("join rejected: bad authentication")
	}
	root, err := protocol.ContentRoot(pair, collectorKey, token.SessionID(), collectorKey, pair.Public())
	if err != nil {
		return Session{}, err
	}
	stored.Mode, stored.ContentRoot = "session", protocol.EncodeKey(root)
	stored.NextJobSeq, stored.NextResultSeq = 1, 1
	stored.JoinedAt = joined.JoinedAt.UTC()
	return stored, nil
}
