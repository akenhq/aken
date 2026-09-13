// SPDX-License-Identifier: Apache-2.0
package relay

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/akenhq/aken/protocol"
)

func request(t *testing.T, s http.Handler, method, path string, body []byte, version, auth string, status int, code string) *httptest.ResponseRecorder {
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

func TestHandlerAPI(t *testing.T) {
	store := NewMemoryStore()
	s := NewHandler(store, Options{})
	token := protocol.NewToken()
	id, credential := token.SessionID(), token.RelayCredential()
	path, auth := "/v0/sessions/"+id.String(), protocol.AuthorizationHeader(credential)
	w := request(t, s, "GET", "/v0/info", nil, "", "", 200, "")
	var info protocol.Info
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if len(info.ProtocolVersions) != 1 || info.ProtocolVersions[0] != 1 || info.Caps != protocol.DefaultCaps {
		t.Fatal("incorrect info", info)
	}
	w = request(t, s, "PUT", path, []byte(`{"chunk_count":2}`), "1", auth, 201, "")
	var created protocol.SessionInfo
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.SessionID != id.String() || created.ChunkCount != 2 || created.ChunksStored != 0 || created.ManifestStored || time.Until(created.ExpiresAt) > protocol.DefaultTTL || time.Until(created.ExpiresAt) < protocol.DefaultTTL-time.Minute {
		t.Fatal("incorrect creation", created)
	}
	if store.sessions[id].credHash != sha256.Sum256(credential[:]) {
		t.Fatal("credential was not hashed")
	}
	request(t, s, "PUT", path, []byte(`{"chunk_count":2}`), "1", auth, 409, "already_exists")
	request(t, s, "GET", path+"/blob/chunks/0", nil, "1", auth, 404, "not_found")
	request(t, s, "GET", path+"/blob/manifest", nil, "1", auth, 404, "not_found")
	request(t, s, "PUT", path+"/blob/manifest", []byte("manifest"), "1", auth, 409, "chunks_missing")
	chunks := [][]byte{bytes.Repeat([]byte{1}, protocol.ChunkSize+protocol.ChunkOverhead), bytes.Repeat([]byte{2}, 17)}
	for i, chunk := range chunks {
		chunkPath := path + "/blob/chunks/" + string(rune('0'+i))
		request(t, s, "PUT", chunkPath, chunk, "1", auth, 201, "")
		request(t, s, "PUT", chunkPath, bytes.Repeat([]byte{3}, len(chunk)), "1", auth, 409, "already_exists")
		w := request(t, s, "GET", chunkPath, nil, "1", auth, 200, "")
		if !bytes.Equal(w.Body.Bytes(), chunk) || w.Header().Get("Content-Type") != "application/octet-stream" {
			t.Fatal("chunk changed")
		}
	}
	manifest := []byte("encrypted manifest")
	request(t, s, "PUT", path+"/blob/manifest", manifest, "1", auth, 201, "")
	request(t, s, "PUT", path+"/blob/manifest", []byte("replacement"), "1", auth, 409, "already_exists")
	w = request(t, s, "GET", path+"/blob/manifest", nil, "1", auth, 200, "")
	if !bytes.Equal(w.Body.Bytes(), manifest) || w.Header().Get("Content-Type") != "application/octet-stream" {
		t.Fatal("manifest changed")
	}
	w = request(t, s, "GET", path, nil, "1", auth, 200, "")
	var stored protocol.SessionInfo
	if err := json.Unmarshal(w.Body.Bytes(), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.ChunksStored != 2 || !stored.ManifestStored || !stored.ExpiresAt.Equal(created.ExpiresAt) {
		t.Fatal("incorrect stored state", stored)
	}
	w = request(t, s, "DELETE", path, nil, "1", auth, 204, "")
	if w.Body.Len() != 0 {
		t.Fatal("delete has body")
	}
	request(t, s, "GET", path, nil, "1", auth, 404, "not_found")
	request(t, s, "DELETE", path, nil, "1", auth, 404, "not_found")
}

func TestHandlerRejections(t *testing.T) {
	auth := protocol.AuthorizationHeader([32]byte{})
	base := "/v0/sessions/" + strings.Repeat("a", 32)
	for _, tt := range []struct {
		name, method, path, body, version, auth string
		status                                  int
		code                                    string
	}{
		{"missing version", "GET", base, "", "", auth, 426, "unsupported_version"},
		{"unsupported version", "GET", base, "", "2", auth, 426, "unsupported_version"},
		{"version before id", "GET", "/v0/sessions/bad", "", "", auth, 426, "unsupported_version"},
		{"invalid id", "GET", "/v0/sessions/bad", "", "1", auth, 404, "not_found"},
		{"missing credential", "GET", base, "", "1", "", 404, "not_found"},
		{"bad credential", "GET", base, "", "1", "Bearer bad", 404, "not_found"},
		{"wrong credential", "GET", base, "", "1", protocol.AuthorizationHeader([32]byte{1}), 404, "not_found"},
		{"unknown session", "GET", "/v0/sessions/" + strings.Repeat("b", 32), "", "1", auth, 404, "not_found"},
		{"create missing credential", "PUT", base, `{"chunk_count":1}`, "1", "", 404, "not_found"},
		{"index at count", "PUT", base + "/blob/chunks/2", strings.Repeat("x", 17), "1", auth, 400, "bad_request"},
		{"invalid index", "PUT", base + "/blob/chunks/no", strings.Repeat("x", 17), "1", auth, 400, "bad_request"},
		{"negative index", "PUT", base + "/blob/chunks/-1", strings.Repeat("x", 17), "1", auth, 400, "bad_request"},
		{"overflow index", "PUT", base + "/blob/chunks/18446744073709551616", strings.Repeat("x", 17), "1", auth, 400, "bad_request"},
		{"missing chunk", "GET", base + "/blob/chunks/2", "", "1", auth, 404, "not_found"},
		{"invalid get index", "GET", base + "/blob/chunks/no", "", "1", auth, 404, "not_found"},
		{"short non-last", "PUT", base + "/blob/chunks/0", strings.Repeat("x", 17), "1", auth, 400, "bad_request"},
		{"short last", "PUT", base + "/blob/chunks/1", strings.Repeat("x", 16), "1", auth, 400, "bad_request"},
		{"empty last", "PUT", base + "/blob/chunks/1", "", "1", auth, 400, "bad_request"},
		{"large chunk before index", "PUT", base + "/blob/chunks/99", strings.Repeat("x", protocol.ChunkSize+protocol.ChunkOverhead+1), "1", auth, 413, "too_large"},
		{"large manifest before chunks", "PUT", base + "/blob/manifest", strings.Repeat("x", protocol.MaxManifestBytes+1), "1", auth, 413, "too_large"},
		{"unknown path", "GET", "/unknown", "", "", "", 404, "not_found"},
		{"no listing", "GET", "/v0/sessions", "", "1", auth, 404, "not_found"},
		{"reserved path", "POST", base + "/join", "", "1", auth, 404, "not_found"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := NewMemoryStore()
			s := NewHandler(store, Options{})
			request(t, s, "PUT", base, []byte(`{"chunk_count":2}`), "1", auth, 201, "")
			w := request(t, s, tt.method, tt.path, []byte(tt.body), tt.version, tt.auth, tt.status, tt.code)
			if tt.status == 426 && !strings.Contains(w.Body.String(), `"protocol_versions":[1]`) {
				t.Fatal("missing versions")
			}
		})
	}
	for _, path := range []string{"/v0/info", base, base + "/blob/chunks/0", base + "/blob/manifest"} {
		for _, method := range []string{"POST", "PATCH", "HEAD"} {
			request(t, NewHandler(NewMemoryStore(), Options{}), method, path, nil, "1", auth, 405, "method_not_allowed")
		}
	}
}

func TestHandlerCreateValidation(t *testing.T) {
	for _, tt := range []struct {
		body   string
		status int
		code   string
	}{
		{`{`, 400, "bad_request"}, {`null`, 400, "bad_request"}, {`{} {}`, 400, "bad_request"},
		{`{}`, 400, "bad_request"}, {`{"chunk_count":0}`, 400, "bad_request"}, {`{"chunk_count":129}`, 400, "bad_request"},
		{`{"chunk_count":-1}`, 400, "bad_request"}, {`{"chunk_count":1,"ttl_seconds":-1}`, 400, "bad_request"},
		{`{"chunk_count":1,"ttl_seconds":86401}`, 400, "ttl_too_long"},
		{`{"chunk_count":128,"ttl_seconds":86400}`, 201, ""},
		{strings.Repeat(" ", (2<<20)+1), 413, "too_large"},
	} {
		request(t, NewHandler(NewMemoryStore(), Options{}), "PUT", "/v0/sessions/"+strings.Repeat("a", 32), []byte(tt.body), "1", protocol.AuthorizationHeader([32]byte{}), tt.status, tt.code)
	}
}

func TestHandlerExpiry(t *testing.T) {
	for _, sweep := range []bool{false, true} {
		store := NewMemoryStore()
		now := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
		s := NewHandler(store, Options{Now: func() time.Time { return now }})
		id := protocol.SessionID{}
		path, auth := "/v0/sessions/"+id.String(), protocol.AuthorizationHeader([32]byte{})
		request(t, s, "PUT", path, []byte(`{"chunk_count":1,"ttl_seconds":60}`), "1", auth, 201, "")
		now = now.Add(61 * time.Second)
		if sweep {
			store.Sweep(now)
		} else {
			request(t, s, "GET", path, nil, "1", auth, 404, "not_found")
		}
		if len(store.sessions) != 0 {
			t.Fatal("expired session retained")
		}
		store.Sweep(now)
		request(t, s, "PUT", path, []byte(`{"chunk_count":1,"ttl_seconds":60}`), "1", auth, 201, "")
		now = now.Add(60 * time.Second)
		request(t, s, "PUT", path, []byte(`{"chunk_count":1}`), "1", auth, 201, "")
	}
}

func TestHandlerCaps(t *testing.T) {
	caps := protocol.Caps{ManifestBytes: 8, ChunkBytes: 17, ChunkCount: 1, TTLDefaultSeconds: 30, TTLMaxSeconds: 60}
	now := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	s := NewHandler(NewMemoryStore(), Options{Caps: caps, Now: func() time.Time { return now }})
	path, auth := "/v0/sessions/"+protocol.SessionID{}.String(), protocol.AuthorizationHeader([32]byte{})
	w := request(t, s, "GET", "/v0/info", nil, "", "", 200, "")
	var info protocol.Info
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil || info.Caps != caps {
		t.Fatalf("caps = %+v, error = %v", info.Caps, err)
	}
	request(t, s, "PUT", path, []byte(`{"chunk_count":2}`), "1", auth, 400, "bad_request")
	request(t, s, "PUT", path, []byte(`{"chunk_count":1,"ttl_seconds":61}`), "1", auth, 400, "ttl_too_long")
	w = request(t, s, "PUT", path, []byte(`{"chunk_count":1}`), "1", auth, 201, "")
	var created protocol.SessionInfo
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || !created.ExpiresAt.Equal(now.Add(30*time.Second)) {
		t.Fatalf("creation = %+v, error = %v", created, err)
	}
	request(t, s, "PUT", path+"/blob/chunks/99", bytes.Repeat([]byte{1}, 18), "1", auth, 413, "too_large")
	request(t, s, "PUT", path+"/blob/manifest", []byte("oversized"), "1", auth, 413, "too_large")
	request(t, s, "PUT", path+"/blob/chunks/0", bytes.Repeat([]byte{1}, 17), "1", auth, 201, "")
	request(t, s, "PUT", path+"/blob/manifest", []byte("manifest"), "1", auth, 201, "")
}

type failingSessionStore struct {
	Store
	err error
}

func (s failingSessionStore) Session(context.Context, protocol.SessionID) (SessionMeta, error) {
	return SessionMeta{}, s.err
}

func TestHandlerStoreErrors(t *testing.T) {
	path, auth := "/v0/sessions/"+protocol.SessionID{}.String(), protocol.AuthorizationHeader([32]byte{})
	for _, tt := range []struct {
		err     error
		status  int
		code    string
		message string
	}{
		{fmt.Errorf("private detail: %w", ErrNotFound), 404, "not_found", "not found"},
		{fmt.Errorf("private detail: %w", ErrExists), 409, "already_exists", "already exists"},
		{fmt.Errorf("private store detail"), 500, "internal", "internal error"},
	} {
		s := NewHandler(failingSessionStore{err: tt.err}, Options{})
		request(t, s, "GET", path, nil, "1", "Bearer bad", 404, "not_found")
		w := request(t, s, "GET", path, nil, "1", auth, tt.status, tt.code)
		var response protocol.ErrorResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Message != tt.message {
			t.Fatalf("error response = %+v, error = %v", response, err)
		}
	}
}
