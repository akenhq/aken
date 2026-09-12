// SPDX-License-Identifier: Apache-2.0
package devrelay

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/akenhq/aken/protocol"
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

func TestServerAPI(t *testing.T) {
	s := New()
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
	if s.sessions[id].credHash != sha256.Sum256(credential[:]) {
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

func TestServerRejections(t *testing.T) {
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
			s := New()
			request(t, s, "PUT", base, []byte(`{"chunk_count":2}`), "1", auth, 201, "")
			w := request(t, s, tt.method, tt.path, []byte(tt.body), tt.version, tt.auth, tt.status, tt.code)
			if tt.status == 426 && !strings.Contains(w.Body.String(), `"protocol_versions":[1]`) {
				t.Fatal("missing versions")
			}
		})
	}
	for _, path := range []string{"/v0/info", base, base + "/blob/chunks/0", base + "/blob/manifest"} {
		for _, method := range []string{"POST", "PATCH", "HEAD"} {
			request(t, New(), method, path, nil, "1", auth, 405, "method_not_allowed")
		}
	}
}

func TestServerCreateValidation(t *testing.T) {
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
		request(t, New(), "PUT", "/v0/sessions/"+strings.Repeat("a", 32), []byte(tt.body), "1", protocol.AuthorizationHeader([32]byte{}), tt.status, tt.code)
	}
}

func TestServerExpiry(t *testing.T) {
	for _, sweep := range []bool{false, true} {
		s := New()
		now := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
		s.Now = func() time.Time { return now }
		id := protocol.SessionID{}
		path, auth := "/v0/sessions/"+id.String(), protocol.AuthorizationHeader([32]byte{})
		request(t, s, "PUT", path, []byte(`{"chunk_count":1,"ttl_seconds":60}`), "1", auth, 201, "")
		now = now.Add(61 * time.Second)
		if sweep {
			s.Sweep()
		} else {
			request(t, s, "GET", path, nil, "1", auth, 404, "not_found")
		}
		if len(s.sessions) != 0 {
			t.Fatal("expired session retained")
		}
		s.Sweep()
		request(t, s, "PUT", path, []byte(`{"chunk_count":1,"ttl_seconds":60}`), "1", auth, 201, "")
		now = now.Add(60 * time.Second)
		request(t, s, "PUT", path, []byte(`{"chunk_count":1}`), "1", auth, 201, "")
	}
}
