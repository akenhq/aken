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
	"sync/atomic"
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
		{"session path on blob", "POST", base + "/join", "", "1", auth, 409, "wrong_mode"},
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

func sessionFixture(t *testing.T, s http.Handler) (protocol.SessionID, string, string) {
	t.Helper()
	token := protocol.NewToken()
	id := token.SessionID()
	path, auth := "/v0/sessions/"+id.String(), protocol.AuthorizationHeader(token.RelayCredential())
	body := fmt.Sprintf(`{"mode":"session","collector_key":%q,"collector_mac":%q}`, protocol.EncodeKey([32]byte{1}), protocol.EncodeKey([32]byte{2}))
	request(t, s, "PUT", path, []byte(body), "1", auth, 201, "")
	return id, path, auth
}

func joinBody() []byte {
	return []byte(fmt.Sprintf(`{"mcp_key":%q,"mcp_mac":%q,"via":"cli"}`, protocol.EncodeKey([32]byte{3}), protocol.EncodeKey([32]byte{4})))
}

func TestPersistentCreate(t *testing.T) {
	key := protocol.EncodeKey([32]byte{})
	for _, body := range []string{
		`{"mode":"other","chunk_count":1}`,
		`{"mode":"session"}`,
		fmt.Sprintf(`{"mode":"session","collector_key":%q}`, key),
		fmt.Sprintf(`{"mode":"session","collector_key":%q,"collector_mac":%q,"chunk_count":1}`, key, key),
		fmt.Sprintf(`{"mode":"session","collector_key":%q,"collector_mac":%q}`, key+"=", key),
		fmt.Sprintf(`{"mode":"session","collector_key":%q,"collector_mac":"AA"}`, key),
	} {
		request(t, NewHandler(NewMemoryStore(), Options{}), "PUT", "/v0/sessions/"+protocol.SessionID{}.String(), []byte(body), "1", protocol.AuthorizationHeader([32]byte{}), 400, "bad_request")
	}
	store := NewMemoryStore()
	s := NewHandler(store, Options{})
	id, path, auth := sessionFixture(t, s)
	w := request(t, s, "GET", path, nil, "1", auth, 200, "")
	var info protocol.SessionInfo
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Mode != "session" || info.Joined || info.ChunkCount != 0 || info.ChunksStored != 0 || info.ManifestStored || info.CollectorKey != protocol.EncodeKey([32]byte{1}) || info.CollectorMAC != protocol.EncodeKey([32]byte{2}) || time.Until(info.ExpiresAt) > protocol.DefaultSessionTTL || time.Until(info.ExpiresAt) < protocol.DefaultSessionTTL-time.Minute {
		t.Fatal("session metadata", info)
	}
	meta, err := store.Session(t.Context(), id)
	if err != nil || meta.Mode != "session" || meta.CollectorKey != ([32]byte{1}) || meta.CollectorMAC != ([32]byte{2}) {
		t.Fatal("store metadata", err)
	}
	for _, endpoint := range []string{"/blob/chunks/0", "/blob/manifest"} {
		for _, method := range []string{"GET", "PUT"} {
			request(t, s, method, path+endpoint, nil, "1", auth, 409, "wrong_mode")
		}
	}
	restarted := NewHandler(store, Options{})
	request(t, restarted, "GET", path, nil, "1", auth, 404, "not_found")
	request(t, restarted, "GET", path+"/join", nil, "1", auth, 404, "not_found")
}

func TestPersistentJoinAndRouting(t *testing.T) {
	s := NewHandler(NewMemoryStore(), Options{})
	_, path, auth := sessionFixture(t, s)
	for _, suffix := range []string{"/join", "/jobs", "/results"} {
		for _, method := range []string{"GET", "POST"} {
			request(t, s, method, path+suffix, nil, "", auth, 426, "unsupported_version")
			request(t, s, method, path+suffix, nil, "1", protocol.AuthorizationHeader([32]byte{}), 404, "not_found")
		}
		for _, method := range []string{"HEAD", "PUT", "PATCH", "DELETE"} {
			request(t, s, method, path+suffix, nil, "1", auth, 405, "method_not_allowed")
		}
		for _, wait := range []string{"31", "-1", "bad", "0.5", "", "1&wait=2"} {
			request(t, s, "GET", path+suffix+"?wait="+wait, nil, "1", auth, 400, "bad_request")
		}
		w := request(t, s, "GET", path+suffix, nil, "1", auth, 204, "")
		if w.Body.Len() != 0 {
			t.Fatal("204 has body")
		}
	}
	for _, body := range [][]byte{[]byte(`{`), []byte(`null`), []byte(`{}`), bytes.Replace(joinBody(), []byte(`"cli"`), []byte(`"web"`), 1), bytes.Replace(joinBody(), []byte(`"mcp_key"`), []byte(`"missing"`), 1), bytes.Replace(joinBody(), []byte(`"mcp_mac"`), []byte(`"missing"`), 1)} {
		request(t, s, "POST", path+"/join", body, "1", auth, 400, "bad_request")
	}
	w := request(t, s, "POST", path+"/join", joinBody(), "1", auth, 201, "")
	var joined protocol.JoinInfo
	if err := json.Unmarshal(w.Body.Bytes(), &joined); err != nil || joined.JoinedAt.IsZero() || joined.Via != "cli" {
		t.Fatal("join", err)
	}
	request(t, s, "POST", path+"/join", joinBody(), "1", auth, 409, "already_exists")
	got := request(t, s, "GET", path+"/join?wait=30", nil, "1", auth, 200, "")
	if got.Body.String() != w.Body.String() {
		t.Fatal("join changed")
	}
	w = request(t, s, "GET", path, nil, "1", auth, 200, "")
	var info protocol.SessionInfo
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil || !info.Joined {
		t.Fatal("joined metadata", err)
	}
	blob := "/v0/sessions/" + protocol.SessionID{}.String()
	request(t, s, "PUT", blob, []byte(`{"chunk_count":1}`), "1", auth, 201, "")
	for _, suffix := range []string{"/join", "/jobs", "/results"} {
		for _, method := range []string{"GET", "POST"} {
			request(t, s, method, blob+suffix, nil, "1", auth, 409, "wrong_mode")
		}
	}
}

func TestPersistentQueueValidation(t *testing.T) {
	for _, kind := range []string{"jobs", "results"} {
		t.Run(kind, func(t *testing.T) {
			s := NewHandler(NewMemoryStore(), Options{})
			id, path, auth := sessionFixture(t, s)
			path += "/" + kind
			request(t, s, "POST", path, []byte(`bad`), "1", auth, 409, "not_joined")
			request(t, s, "POST", strings.TrimSuffix(path, "/"+kind)+"/join", joinBody(), "1", auth, 201, "")
			capBytes := protocol.MaxJobBytes
			if kind == "results" {
				capBytes = protocol.MaxResultBytes
			}
			envelope := protocol.Envelope{Version: 1, SessionID: id.String(), Seq: 1, Class: 1, Payload: make([]byte, 17)}
			post := func(e protocol.Envelope, status int, code string) {
				t.Helper()
				body, err := json.Marshal(e)
				if err != nil {
					t.Fatal(err)
				}
				w := request(t, s, "POST", path, body, "1", auth, status, code)
				if status == 202 && w.Body.Len() != 0 {
					t.Fatal("202 has body")
				}
			}
			for _, tt := range []struct {
				name   string
				change func(*protocol.Envelope)
				status int
				code   string
			}{
				{"version", func(e *protocol.Envelope) { e.Version = 2 }, 400, "bad_request"},
				{"id", func(e *protocol.Envelope) { e.SessionID = protocol.SessionID{}.String() }, 400, "bad_request"},
				{"seq zero", func(e *protocol.Envelope) { e.Seq = 0 }, 400, "bad_request"},
				{"seq overflow", func(e *protocol.Envelope) { e.Seq = protocol.MaxSessionSeq + 1 }, 400, "bad_request"},
				{"class zero", func(e *protocol.Envelope) { e.Class = 0 }, 400, "bad_request"},
				{"class four", func(e *protocol.Envelope) { e.Class = 4 }, 400, "bad_request"},
				{"short", func(e *protocol.Envelope) { e.Payload = make([]byte, 16) }, 400, "bad_request"},
				{"class", func(e *protocol.Envelope) { e.Class = 2 }, 403, "class_not_allowed"},
				{"exec", func(e *protocol.Envelope) { e.Class = 3 }, 403, "class_not_allowed"},
				{"large", func(e *protocol.Envelope) { e.Payload = make([]byte, capBytes+1) }, 413, "too_large"},
				{"class before size", func(e *protocol.Envelope) { e.Class = 2; e.Payload = make([]byte, capBytes+1) }, 403, "class_not_allowed"},
				{"invalid before size", func(e *protocol.Envelope) { e.Version = 2; e.Payload = make([]byte, capBytes+1) }, 400, "bad_request"},
				{"gap", func(e *protocol.Envelope) { e.Seq = 3 }, 409, "bad_sequence"},
			} {
				t.Run(tt.name, func(t *testing.T) { e := envelope; tt.change(&e); post(e, tt.status, tt.code) })
			}
			for _, body := range []string{`{`, `null`, `{} {}`, `{"payload":"!"}`} {
				request(t, s, "POST", path, []byte(body), "1", auth, 400, "bad_request")
			}
			for seq := uint64(1); seq <= 64; seq++ {
				e := envelope
				e.Seq = seq
				post(e, 202, "")
			}
			e := envelope
			e.Seq = 64
			post(e, 202, "")
			e.Payload = bytes.Repeat([]byte{1}, 17)
			post(e, 409, "bad_sequence")
			e = envelope
			e.Seq = 66
			post(e, 409, "bad_sequence")
			e.Seq = 65
			post(e, 429, "queue_full")
			w := request(t, s, "GET", path, nil, "1", auth, 200, "")
			var messages protocol.Messages
			if err := json.Unmarshal(w.Body.Bytes(), &messages); err != nil || len(messages.Messages) != 64 {
				t.Fatal("queued messages", err)
			}
			for i, e := range messages.Messages {
				if e.Seq != uint64(i+1) {
					t.Fatal("queue order")
				}
			}
			e = envelope
			e.Seq = 64
			post(e, 202, "")
			request(t, s, "GET", path, nil, "1", auth, 204, "")
			e.Seq = 1
			post(e, 409, "bad_sequence")
			e.Seq = 65
			post(e, 202, "")
		})
	}
}

func TestPersistentPollLifecycle(t *testing.T) {
	for _, action := range []string{"join", "jobs", "results", "delete", "expiry", "sweep", "cancel"} {
		t.Run(action, func(t *testing.T) {
			store := NewMemoryStore()
			var offset atomic.Int64
			s := NewHandler(store, Options{Now: func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }})
			id, path, auth := sessionFixture(t, s)
			live := s.live.get(id)
			kind := "jobs"
			if action == "join" {
				kind = "join"
			}
			if action == "results" {
				kind = "results"
			}
			if action != "join" {
				request(t, s, "POST", path+"/join", joinBody(), "1", auth, 201, "")
			}
			if action == "expiry" {
				live.expiresAt = time.Now().Add(50 * time.Millisecond)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			r := httptest.NewRequest("GET", path+"/"+kind+"?wait=30", nil).WithContext(ctx)
			r.Header.Set(protocol.ProtocolHeader, "1")
			r.Header.Set("Authorization", auth)
			w := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { s.ServeHTTP(w, r); close(done) }()
			// No event may be lost between reading the queue and starting the wait.
			switch action {
			case "join":
				request(t, s, "POST", path+"/join", joinBody(), "1", auth, 201, "")
			case "jobs", "results":
				body, err := json.Marshal(protocol.Envelope{Version: 1, SessionID: id.String(), Seq: 1, Class: 1, Payload: make([]byte, 17)})
				if err != nil {
					t.Fatal(err)
				}
				request(t, s, "POST", path+"/"+kind, body, "1", auth, 202, "")
			case "delete":
				request(t, s, "DELETE", path, nil, "1", auth, 204, "")
			case "sweep":
				offset.Store(int64(9 * time.Hour))
				s.Sweep()
			case "cancel":
				cancel()
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("poll did not wake")
			}
			want := 200
			if action == "delete" || action == "expiry" || action == "sweep" {
				want = 404
			}
			if action != "cancel" && w.Code != want {
				t.Fatal("poll status", w.Code, w.Body.String())
			}
			if action == "expiry" || action == "sweep" || action == "delete" {
				if s.live.get(id) != nil {
					t.Fatal("live session retained")
				}
			}
		})
	}
}

func TestPersistentConcurrentDelivery(t *testing.T) {
	s := NewHandler(NewMemoryStore(), Options{})
	id, path, auth := sessionFixture(t, s)
	request(t, s, "POST", path+"/join", joinBody(), "1", auth, 201, "")
	body, err := json.Marshal(protocol.Envelope{Version: 1, SessionID: id.String(), Seq: 1, Class: 1, Payload: make([]byte, 17)})
	if err != nil {
		t.Fatal(err)
	}
	responses := make(chan *httptest.ResponseRecorder, 8)
	send := func(method string, body []byte) {
		r := httptest.NewRequest(method, path+"/jobs", bytes.NewReader(body))
		r.Header.Set(protocol.ProtocolHeader, "1")
		r.Header.Set("Authorization", auth)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		responses <- w
	}
	for range 8 {
		go send("POST", body)
	}
	for range 8 {
		if w := <-responses; w.Code != 202 {
			t.Fatal("concurrent replay", w.Code)
		}
	}
	for range 8 {
		go send("GET", nil)
	}
	delivered := 0
	for range 8 {
		w := <-responses
		switch w.Code {
		case 200:
			var messages protocol.Messages
			if err := json.Unmarshal(w.Body.Bytes(), &messages); err != nil {
				t.Fatal(err)
			}
			delivered += len(messages.Messages)
		case 204:
		default:
			t.Fatal("concurrent poll", w.Code)
		}
	}
	if delivered != 1 {
		t.Fatal("delivery count", delivered)
	}
}

func TestOldPollCannotDeleteReplacement(t *testing.T) {
	store := NewMemoryStore()
	s := NewHandler(store, Options{})
	id, path, auth := sessionFixture(t, s)
	old := s.live.get(id)
	request(t, s, "DELETE", path, nil, "1", auth, 204, "")
	body := fmt.Sprintf(`{"mode":"session","collector_key":%q,"collector_mac":%q}`, protocol.EncodeKey([32]byte{1}), protocol.EncodeKey([32]byte{2}))
	request(t, s, "PUT", path, []byte(body), "1", auth, 201, "")
	w := httptest.NewRecorder()
	s.poll(w, httptest.NewRequest("GET", path+"/jobs", nil), id, old, "jobs", 0)
	if w.Code != 404 {
		t.Fatal("old poll", w.Code)
	}
	request(t, s, "GET", path, nil, "1", auth, 200, "")
	if s.live.get(id) == nil || s.live.get(id) == old {
		t.Fatal("replacement lost")
	}
}
