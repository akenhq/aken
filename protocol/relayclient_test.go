// SPDX-License-Identifier: Apache-2.0
package protocol_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/akenhq/aken/protocol"
	"github.com/akenhq/aken/relay"
)

func TestRelayClient(t *testing.T) {
	server := httptest.NewServer(relay.NewHandler(relay.NewMemoryStore(), relay.Options{}))
	defer server.Close()
	token := protocol.NewToken()
	client, err := protocol.NewRelayClient(server.URL, token.RelayCredential())
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	info, err := client.Info(ctx)
	if err != nil || info.Caps != protocol.DefaultCaps {
		t.Fatal("info", info, err)
	}
	session, err := client.CreateSession(ctx, token.SessionID(), time.Minute, 2)
	if err != nil || session.ChunkCount != 2 || session.ManifestStored {
		t.Fatal("create", session, err)
	}
	chunks := [][]byte{bytes.Repeat([]byte{7}, protocol.ChunkSize+protocol.ChunkOverhead), bytes.Repeat([]byte{9}, 17)}
	for i, chunk := range chunks {
		if err := client.PutChunk(ctx, token.SessionID(), uint64(i), chunk); err != nil {
			t.Fatal(err)
		}
		if err := client.PutChunk(ctx, token.SessionID(), uint64(i), chunk); err != nil {
			t.Fatal("duplicate", err)
		}
	}
	manifest := []byte("encrypted manifest")
	if err := client.PutManifest(ctx, token.SessionID(), manifest); err != nil {
		t.Fatal(err)
	}
	got, err := client.GetManifest(ctx, token.SessionID())
	if err != nil || !bytes.Equal(got, manifest) {
		t.Fatal("get manifest", err)
	}
	for i, chunk := range chunks {
		got, err := client.GetChunk(ctx, token.SessionID(), uint64(i))
		if err != nil || !bytes.Equal(got, chunk) {
			t.Fatal("get chunk", err)
		}
	}
	session, err = client.Session(ctx, token.SessionID())
	if err != nil || session.ChunksStored != 2 || !session.ManifestStored {
		t.Fatal("session", session, err)
	}
	if err := client.DeleteSession(ctx, token.SessionID()); err != nil {
		t.Fatal(err)
	}
	_, err = client.Session(ctx, token.SessionID())
	var relayErr *protocol.RelayError
	if !protocol.IsNotFound(fmt.Errorf("wrapped: %w", err)) || !errors.As(err, &relayErr) || relayErr.Code != "not_found" {
		t.Fatal("missing", err)
	}
	if strings.Contains(err.Error(), server.URL) || strings.Contains(err.Error(), protocol.AuthorizationHeader(token.RelayCredential())) || err.Error() != "relay: 404 not_found: not found" {
		t.Fatal("unsafe or incorrect error", err)
	}
	if protocol.IsNotFound(nil) || protocol.IsNotFound(errors.New("404")) {
		t.Fatal("false not found")
	}
}

func TestRelayURL(t *testing.T) {
	for _, tt := range []struct {
		url   string
		valid bool
	}{
		{"https://example.com", true}, {"https://example.com:8443/", true},
		{"http://localhost:7788", true}, {"http://127.0.0.1", true}, {"http://127.23.4.5", true}, {"http://[::1]:7788/", true},
		{"http://example.com", false}, {"http://192.168.1.1", false}, {"http://[::2]", false},
		{"https://example.com/path", false}, {"https://user:private@example.com", false},
		{"https://example.com?x=1", false}, {"https://example.com?", false}, {"https://example.com#x", false}, {"https://example.com#", false},
		{"ftp://localhost", false}, {"https://", false}, {"://invalid", false}, {"", false},
	} {
		t.Run(tt.url, func(t *testing.T) {
			client, err := protocol.NewRelayClient(tt.url, [32]byte{})
			if (err == nil) != tt.valid {
				t.Fatalf("error = %v", err)
			}
			if tt.valid && strings.HasSuffix(client.BaseURL, "/") {
				t.Fatal("trailing slash")
			}
			if err != nil && err.Error() != fmt.Sprintf("invalid relay URL %q: use https://, or http:// only for a loopback address such as http://127.0.0.1:7788, with no path, query or user info", tt.url) {
				t.Fatal(err)
			}
		})
	}
}

func TestRelayRetry(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(protocol.ProtocolHeader) != "1" || r.Header.Get("Authorization") != protocol.AuthorizationHeader([32]byte{}) || r.Header.Get("Content-Type") != "application/octet-stream" {
			t.Error("missing request headers")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != "identical ciphertext" {
			t.Error("retry changed body", err)
		}
		if attempts.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(201)
	}))
	defer server.Close()
	client, err := protocol.NewRelayClient(server.URL, [32]byte{})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.PutManifest(t.Context(), protocol.SessionID{}, []byte("identical ciphertext")); err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 2 {
		t.Fatal("expected two attempts")
	}
}

type relayTransport func(*http.Request) (*http.Response, error)

func (f relayTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRelayTransportRetry(t *testing.T) {
	client, err := protocol.NewRelayClient("https://example.com", [32]byte{})
	if err != nil {
		t.Fatal(err)
	}
	attempts := 0
	client.HTTP = &http.Client{Transport: relayTransport(func(_ *http.Request) (*http.Response, error) {
		attempts++
		return nil, errors.New("private transport detail")
	})}
	_, err = client.Info(t.Context())
	// The transport cause stays in the error so the human learns why the upload failed;
	// the URL carries only the public session id, never a credential.
	if attempts != 3 || err == nil || err.Error() != "cannot reach the relay at https://example.com: private transport detail" {
		t.Fatal("retry limit or error", attempts, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	attempts = 0
	client.HTTP.Transport = relayTransport(func(_ *http.Request) (*http.Response, error) { attempts++; cancel(); return nil, errors.New("failed") })
	_, err = client.Info(ctx)
	if err != context.Canceled || attempts != 1 {
		t.Fatal("cancellation", attempts, err)
	}
}

func TestRelayResponses(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		body   string
		code   string
		chunk  bool
	}{
		{"json error", 404, `{"error":"not_found","message":"not found"}`, "not_found", false},
		{"plain error", 400, "bad", "http_400", false},
		{"empty error", 400, `{}`, "http_400", false},
		{"oversized error", 400, strings.Repeat("x", (2<<20)+1), "http_400", false},
		{"invalid JSON", 200, "bad", "", false},
		{"other JSON", 200, `{"hello":"world"}`, "", false},
		{"oversized JSON", 200, strings.Repeat("x", (2<<20)+1), "", false},
		{"oversized chunk", 200, strings.Repeat("x", protocol.ChunkSize+protocol.ChunkOverhead+1), "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				attempts.Add(1)
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer server.Close()
			client, err := protocol.NewRelayClient(server.URL, [32]byte{})
			if err != nil {
				t.Fatal(err)
			}
			if tt.chunk {
				_, err = client.GetChunk(t.Context(), protocol.SessionID{}, 0)
			} else {
				_, err = client.Info(t.Context())
			}
			if err == nil || attempts.Load() != 1 {
				t.Fatal("expected error without retry", err)
			}
			if tt.status == 404 {
				if err.Error() != server.URL+" is not an Aken relay: it has no /v0/info" {
					t.Fatal(err)
				}
			} else if tt.code != "" {
				var relayErr *protocol.RelayError
				if !errors.As(err, &relayErr) || relayErr.Status != tt.status || relayErr.Code != tt.code {
					t.Fatal("error mapping", err)
				}
			}
		})
	}
}

func TestRelayHeaders(t *testing.T) {
	credential := protocol.NewToken().RelayCredential()
	header := protocol.AuthorizationHeader(credential)
	got, ok := protocol.ParseAuthorizationHeader(header)
	if !ok || got != credential {
		t.Fatal("credential round trip")
	}
	for _, header := range []string{"", "bearer " + header[7:], "Bearer", "Bearer ", header + "=", "Bearer AA", "Bearer " + strings.Repeat("a", 44)} {
		if _, ok := protocol.ParseAuthorizationHeader(header); ok {
			t.Fatalf("accepted invalid header %q", header)
		}
	}
	id := protocol.NewToken().SessionID()
	if got, ok := protocol.ParseSessionID(id.String()); !ok || got != id {
		t.Fatal("session id round trip")
	}
	for _, value := range []string{"", strings.Repeat("a", 31), strings.Repeat("A", 32), strings.Repeat("g", 32)} {
		if _, ok := protocol.ParseSessionID(value); ok {
			t.Fatalf("accepted invalid session id %q", value)
		}
	}
}

func TestRelayErrorMessage(t *testing.T) {
	for _, tt := range []struct{ name, message, want string }{
		{"large", "\x1b\n" + strings.Repeat("x", (1<<20)-2), "\ufffd\ufffd" + strings.Repeat("x", 194)},
		{"controls", "a\t\r\x7f\u0085\u202e\u2066 z", "a" + strings.Repeat("\ufffd", 6) + " z"},
		{"printable", "café 世界 😀", "café 世界 😀"},
		{"multibyte boundary", strings.Repeat("x", 199) + "é", strings.Repeat("x", 199)},
		{"replacement boundary", strings.Repeat("x", 198) + "\x1b", strings.Repeat("x", 198)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body, err := json.Marshal(protocol.ErrorResponse{Error: "bad_request", Message: tt.message})
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write(body)
			}))
			defer server.Close()
			client, err := protocol.NewRelayClient(server.URL, [32]byte{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Info(t.Context())
			var relayErr *protocol.RelayError
			if !errors.As(err, &relayErr) || relayErr.Message != tt.want || len(relayErr.Message) > 200 || !utf8.ValidString(relayErr.Message) || strings.ContainsFunc(relayErr.Message, func(r rune) bool { return !unicode.IsPrint(r) }) {
				t.Fatalf("relay error = %v", err)
			}
		})
	}
}

func TestPersistentRelayClient(t *testing.T) {
	server := httptest.NewServer(relay.NewHandler(relay.NewMemoryStore(), relay.Options{}))
	defer server.Close()
	token := protocol.NewToken()
	id := token.SessionID()
	client, err := protocol.NewRelayClient(server.URL, token.RelayCredential())
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	ck, cm, mk, mm := [32]byte{1}, [32]byte{2}, [32]byte{3}, [32]byte{4}
	created, err := client.CreatePersistentSession(ctx, id, 0, ck, cm)
	if err != nil || created.Mode != "session" || created.Joined || created.CollectorKey != protocol.EncodeKey(ck) || created.CollectorMAC != protocol.EncodeKey(cm) {
		t.Fatal("create", created, err)
	}
	if info, ok, err := client.WaitJoin(ctx, id, 0); err != nil || ok || info != (protocol.JoinInfo{}) {
		t.Fatal("empty join", info, ok, err)
	}
	joined, err := client.Join(ctx, id, mk, mm, "chat")
	if err != nil || joined.MCPKey != protocol.EncodeKey(mk) || joined.MCPMAC != protocol.EncodeKey(mm) || joined.CollectorKey != created.CollectorKey || joined.CollectorMAC != created.CollectorMAC || joined.Via != "chat" || joined.JoinedAt.IsZero() {
		t.Fatal("join", joined, err)
	}
	if got, ok, err := client.WaitJoin(ctx, id, 0); err != nil || !ok || got != joined {
		t.Fatal("wait join", got, ok, err)
	}
	for _, result := range []bool{false, true} {
		post, poll, size := client.PostJob, client.PollJobs, protocol.MaxJobBytes
		if result {
			post, poll, size = client.PostResult, client.PollResults, protocol.MaxResultBytes
		}
		if got, err := poll(ctx, id, 0); err != nil || got != nil {
			t.Fatal("empty poll", err)
		}
		for seq := uint64(1); seq <= 3; seq++ {
			e := protocol.Envelope{Version: 1, SessionID: id.String(), Seq: seq, Class: 1, Payload: bytes.Repeat([]byte{byte(seq)}, size)}
			if err := post(ctx, id, e); err != nil {
				t.Fatal("post", err)
			}
			if err := post(ctx, id, e); err != nil {
				t.Fatal("replay", err)
			}
		}
		got, err := poll(ctx, id, 0)
		if err != nil || len(got) != 3 {
			t.Fatal("poll", len(got), err)
		}
		for i, e := range got {
			if e.Seq != uint64(i+1) || !bytes.Equal(e.Payload, bytes.Repeat([]byte{byte(i + 1)}, size)) {
				t.Fatal("messages changed")
			}
		}
	}
	if err := client.DeleteSession(ctx, id); err != nil {
		t.Fatal(err)
	}
}

func TestSessionClientWaitAndOldRelay(t *testing.T) {
	for _, method := range []string{"join", "jobs", "results"} {
		for _, tt := range []struct {
			wait  time.Duration
			query string
		}{{0, "0"}, {1500 * time.Millisecond, "1"}, {31 * time.Second, "30"}, {-time.Second, "0"}} {
			t.Run(method+tt.query, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != "GET" || !strings.HasSuffix(r.URL.Path, "/"+method) || r.URL.RawQuery != "wait="+tt.query || r.Header.Get(protocol.ProtocolHeader) != "1" || r.Header.Get("Authorization") != protocol.AuthorizationHeader([32]byte{}) {
						t.Error("incorrect poll request")
					}
					w.WriteHeader(204)
				}))
				defer server.Close()
				client, err := protocol.NewRelayClient(server.URL, [32]byte{})
				if err != nil {
					t.Fatal(err)
				}
				switch method {
				case "join":
					info, ok, err := client.WaitJoin(t.Context(), protocol.SessionID{}, tt.wait)
					if err != nil || ok || info != (protocol.JoinInfo{}) {
						t.Fatal("204 join", err)
					}
				case "jobs":
					got, err := client.PollJobs(t.Context(), protocol.SessionID{}, tt.wait)
					if err != nil || got != nil {
						t.Fatal("204 jobs", err)
					}
				case "results":
					got, err := client.PollResults(t.Context(), protocol.SessionID{}, tt.wait)
					if err != nil || got != nil {
						t.Fatal("204 results", err)
					}
				}
			})
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/join") {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"mode":"blob"}`)
	}))
	defer server.Close()
	client, err := protocol.NewRelayClient(server.URL, [32]byte{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Session(t.Context(), protocol.SessionID{}); err != nil {
		t.Fatal(err)
	}
	_, err = client.Join(t.Context(), protocol.SessionID{}, [32]byte{}, [32]byte{}, "cli")
	if !protocol.IsNotFound(err) {
		t.Fatal("old relay join", err)
	}
	_, _, err = client.WaitJoin(t.Context(), protocol.SessionID{}, 0)
	if !protocol.IsNotFound(err) {
		t.Fatal("old relay wait", err)
	}
}

func TestSessionKeyEncoding(t *testing.T) {
	key := [32]byte{255, 254, 253}
	encoded := protocol.EncodeKey(key)
	if got, ok := protocol.DecodeKey(encoded); !ok || got != key {
		t.Fatal("key round trip")
	}
	for _, value := range []string{"", encoded + "=", encoded + "\n", encoded[:42], strings.Repeat("/", 43), strings.Repeat("A", 42) + "B"} {
		if _, ok := protocol.DecodeKey(value); ok {
			t.Fatal("invalid key accepted")
		}
	}
}

func TestRelayLimits(t *testing.T) {
	for _, tt := range []struct{ code, retry, wait string }{
		{"rate_limited", "", "a moment"}, {"rate_limited", "45", "45 seconds"},
		{"rate_limited", "119", "119 seconds"}, {"rate_limited", "120", "2 minutes"},
		{"over_capacity", "121", "3 minutes"}, {"rate_limited", "invalid", "a moment"},
	} {
		t.Run(tt.code+"/"+tt.retry, func(t *testing.T) {
			attempts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				attempts++
				w.Header().Set("Retry-After", tt.retry)
				w.WriteHeader(http.StatusTooManyRequests)
				_ = json.NewEncoder(w).Encode(protocol.ErrorResponse{Error: tt.code})
			}))
			defer server.Close()
			client, err := protocol.NewRelayClient(server.URL, [32]byte{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Info(t.Context())
			reason := "the relay's rate limit for this address was reached"
			if tt.code == "over_capacity" {
				reason = "the relay is at capacity"
			}
			want := reason + "; retry in " + tt.wait + ". To avoid relay limits, run your own relay: https://github.com/akenhq/aken/blob/main/docs/relay.md"
			if err == nil || err.Error() != want || attempts != 1 {
				t.Fatalf("attempts %d: %v", attempts, err)
			}
		})
	}
	if got := (&protocol.RelayError{Status: 400, Code: "bad_request"}).Error(); got != "relay: 400 bad_request" {
		t.Fatal(got)
	}
}
