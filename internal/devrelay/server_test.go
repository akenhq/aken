// SPDX-License-Identifier: Apache-2.0
package devrelay

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/akenhq/aken/protocol"
	"github.com/akenhq/aken/relay"
)

func request(t *testing.T, s *Server, method, path string, body []byte, version, auth string, status int, code string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r.Header.Set(protocol.ProtocolHeader, version)
	r.Header.Set("Authorization", auth)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s %s: status = %d, want %d; %s", method, path, w.Code, status, w.Body.String())
	}
	if code != "" {
		var response protocol.ErrorResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Error != code || w.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("error = %q, want %q", response.Error, code)
		}
	}
	return w
}

func TestServerRoundTrip(t *testing.T) {
	s := New()
	now := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	token := protocol.NewToken()
	path, auth := "/v0/sessions/"+token.SessionID().String(), protocol.AuthorizationHeader(token.RelayCredential())
	w := request(t, s, "PUT", path, []byte(`{"chunk_count":1,"ttl_seconds":60}`), "1", auth, 201, "")
	var created protocol.SessionInfo
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !created.ExpiresAt.Equal(now.Add(time.Minute)) {
		t.Fatal("incorrect expiry", created)
	}
	chunk := bytes.Repeat([]byte{1}, 17)
	request(t, s, "PUT", path+"/blob/chunks/0", chunk, "1", auth, 201, "")
	w = request(t, s, "GET", path+"/blob/chunks/0", nil, "1", auth, 200, "")
	if !bytes.Equal(w.Body.Bytes(), chunk) {
		t.Fatal("chunk changed")
	}
	request(t, s, "PUT", path+"/blob/manifest", []byte("manifest"), "1", auth, 201, "")
	w = request(t, s, "GET", path+"/blob/manifest", nil, "1", auth, 200, "")
	if w.Body.String() != "manifest" {
		t.Fatal("manifest changed")
	}
	now = now.Add(time.Minute)
	s.Sweep()
	if _, err := s.store.Session(t.Context(), token.SessionID()); err != relay.ErrNotFound {
		t.Fatalf("swept session: %v", err)
	}
	request(t, s, "GET", path, nil, "1", auth, 404, "not_found")
}

func TestServerPersistentJoin(t *testing.T) {
	s := New()
	now := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	token := protocol.NewToken()
	path, auth := "/v0/sessions/"+token.SessionID().String(), protocol.AuthorizationHeader(token.RelayCredential())
	create, err := json.Marshal(protocol.CreateSessionRequest{Mode: "session", CollectorKey: protocol.EncodeKey([32]byte{1}), CollectorMAC: protocol.EncodeKey([32]byte{2})})
	if err != nil {
		t.Fatal(err)
	}
	request(t, s, "PUT", path, create, "1", auth, 201, "")
	join, err := json.Marshal(protocol.JoinRequest{MCPKey: protocol.EncodeKey([32]byte{3}), MCPMAC: protocol.EncodeKey([32]byte{4}), Via: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	request(t, s, "POST", path+"/join", join, "1", auth, 201, "")
	w := request(t, s, "GET", path+"/join?wait=0", nil, "1", auth, 200, "")
	var info protocol.JoinInfo
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil || info.MCPKey != protocol.EncodeKey([32]byte{3}) || info.CollectorMAC != protocol.EncodeKey([32]byte{2}) || !info.JoinedAt.Equal(now) {
		t.Fatal("join", info, err)
	}
	now = now.Add(protocol.DefaultSessionTTL)
	s.Sweep()
	request(t, s, "GET", path+"/join", nil, "1", auth, 404, "not_found")
	if _, err := s.store.Session(t.Context(), token.SessionID()); err != relay.ErrNotFound {
		t.Fatal("swept metadata", err)
	}
	request(t, s, "PUT", path, create, "1", auth, 201, "")
	request(t, s, "GET", path+"/join", nil, "1", auth, 204, "")
}
