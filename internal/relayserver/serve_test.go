// SPDX-License-Identifier: Apache-2.0
package relayserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/akenhq/aken/internal/relayserver/limits"
	"github.com/akenhq/aken/protocol"
	"github.com/akenhq/aken/relay"
)

func TestRunMemory(t *testing.T) { testRun(t, "memory", "") }

func TestRunDir(t *testing.T) { testRun(t, "dir", t.TempDir()) }

func testRun(t *testing.T, store, dataDir string) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	reader, writer := io.Pipe()
	var stderr bytes.Buffer
	stopped := make(chan error, 1)
	go func() {
		stopped <- Run(ctx, Config{Listen: "127.0.0.1:0", Store: store, DataDir: dataDir, Limits: limits.Config{RequestsPerMinute: 600, CreatesPerHour: 10, MaxLiveSessions: 200}}, writer, &stderr)
	}()
	ready := make(chan string, 1)
	go func() {
		buf := make([]byte, 512)
		n, err := reader.Read(buf)
		if err == nil {
			ready <- strings.Fields(string(buf[:n]))[3]
		}
		_, _ = io.Copy(io.Discard, reader)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-stopped:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(12 * time.Second):
			t.Error("shutdown timed out")
		}
		_ = writer.Close()
		_ = reader.Close()
		if stderr.Len() != 0 {
			t.Error(stderr.String())
		}
	})
	var url string
	select {
	case url = <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("startup timed out")
	}
	token := protocol.NewToken()
	client, err := protocol.NewRelayClient(url, token.RelayCredential())
	if err != nil {
		t.Fatal(err)
	}
	info, err := client.Info(t.Context())
	if err != nil || info.Caps != protocol.DefaultCaps {
		t.Fatal(info, err)
	}
	id := token.SessionID()
	if _, err := client.CreateSession(t.Context(), id, time.Hour, 2); err != nil {
		t.Fatal(err)
	}
	for i, chunk := range [][]byte{bytes.Repeat([]byte{1}, protocol.ChunkSize+protocol.ChunkOverhead), bytes.Repeat([]byte{2}, 17)} {
		if err := client.PutChunk(t.Context(), id, uint64(i), chunk); err != nil {
			t.Fatal(err)
		}
		if got, err := client.GetChunk(t.Context(), id, uint64(i)); err != nil || !bytes.Equal(got, chunk) {
			t.Fatal("chunk differs", err)
		}
	}
	manifest := []byte("opaque manifest")
	if err := client.PutManifest(t.Context(), id, manifest); err != nil {
		t.Fatal(err)
	}
	if got, err := client.GetManifest(t.Context(), id); err != nil || !bytes.Equal(got, manifest) {
		t.Fatal("manifest differs", err)
	}
	if got, err := client.Session(t.Context(), id); err != nil || got.ChunksStored != 2 || !got.ManifestStored {
		t.Fatal(got, err)
	}
	if err := client.DeleteSession(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Session(t.Context(), id); !protocol.IsNotFound(err) {
		t.Fatal(err)
	}
	for range 65 {
		r, err := http.NewRequestWithContext(t.Context(), "GET", url+"/healthz", nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := (&http.Client{Timeout: 5 * time.Second}).Do(r)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil || response.StatusCode != 200 || string(body) != "ok" {
			t.Fatal(response.StatusCode, string(body), err)
		}
	}
}

func TestLogging(t *testing.T) {
	var out bytes.Buffer
	token := protocol.NewToken()
	sid := token.SessionID().String()
	l := limits.New(limits.Config{RequestsPerMinute: 600, CreatesPerHour: 10, MaxLiveSessions: 1}, nil)
	l.SetLiveSessions(1)
	h := logging(l.Middleware(relay.NewHandler(relay.NewMemoryStore(), relay.Options{})), slog.New(slog.NewJSONHandler(&out, nil)), true)
	r := httptest.NewRequest("PUT", "/v0/sessions/"+sid+"?secret=query", strings.NewReader("secret body"))
	r.Header.Set("Authorization", protocol.AuthorizationHeader(token.RelayCredential()))
	r.Header.Set("CF-Connecting-IP", "203.0.113.1")
	h.ServeHTTP(httptest.NewRecorder(), r)
	var entry map[string]any
	if err := json.Unmarshal(out.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if entry["method"] != "PUT" || entry["route"] != "/v0/sessions/{sid}" || entry["session_id"] != sid || entry["status"] != float64(503) || entry["bytes"] != float64(len("{\"error\":\"over_capacity\"}\n")) || entry["client_ip"] != "203.0.113.1" || entry["duration"] == nil {
		t.Fatal(entry)
	}
	if strings.Contains(out.String(), "secret") || strings.Contains(out.String(), protocol.AuthorizationHeader(token.RelayCredential())) {
		t.Fatal("sensitive request logged")
	}
	for _, path := range []string{"/v0/sessions/secret", "/secret", "/v0/sessions/" + sid + "/secret"} {
		pattern, id := logRoute(path)
		if strings.Contains(pattern, "secret") || id != "" {
			t.Fatal(pattern, id)
		}
	}
	for _, route := range []string{"join", "jobs", "results"} {
		pattern, id := logRoute("/v0/sessions/" + sid + "/" + route)
		if pattern != "/v0/sessions/{sid}/"+route || id != sid {
			t.Fatal(pattern, id)
		}
	}
}

func TestSweepMemory(t *testing.T) {
	store := relay.NewMemoryStore()
	now := time.Now().UTC()
	expired, live := protocol.NewToken().SessionID(), protocol.NewToken().SessionID()
	for id, expiry := range map[protocol.SessionID]time.Time{expired: now, live: now.Add(time.Second)} {
		if err := store.CreateSession(t.Context(), id, relay.SessionMeta{ExpiresAt: expiry, ChunkCount: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := sweep(t.Context(), store, relay.NewHandler(store, relay.Options{}), limits.New(limits.Config{}, nil), now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Session(t.Context(), expired); !errors.Is(err, relay.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := store.Session(t.Context(), live); err != nil {
		t.Fatal(err)
	}
}

func TestSweepLiveSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		now := time.Now().UTC()
		store := relay.NewMemoryStore()
		handler := relay.NewHandler(store, relay.Options{Now: func() time.Time { return now }})
		token := protocol.NewToken()
		request := func(method, suffix, body string) *http.Request {
			r := httptest.NewRequest(method, "/v0/sessions/"+token.SessionID().String()+suffix, strings.NewReader(body))
			r.Header.Set(protocol.ProtocolHeader, "1")
			r.Header.Set("Authorization", protocol.AuthorizationHeader(token.RelayCredential()))
			return r
		}
		created := httptest.NewRecorder()
		key := protocol.EncodeKey([32]byte{1})
		handler.ServeHTTP(created, request("PUT", "", `{"mode":"session","ttl_seconds":60,"collector_key":"`+key+`","collector_mac":"`+key+`"}`))
		if created.Code != http.StatusCreated {
			t.Fatal(created.Code, created.Body.String())
		}
		poll := httptest.NewRecorder()
		go handler.ServeHTTP(poll, request("GET", "/join?wait=30", ""))
		synctest.Wait()
		now = now.Add(time.Minute)
		if err := sweep(t.Context(), store, handler, limits.New(limits.Config{}, nil), now); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if poll.Code != http.StatusNotFound {
			t.Fatal(poll.Code, poll.Body.String())
		}
	})
}

func TestListenWarning(t *testing.T) {
	for _, tt := range []struct {
		listen         string
		trust, warning bool
	}{
		{"127.0.0.1:0", true, false}, {"localhost:0", true, false},
		{"0.0.0.0:0", true, true}, {"0.0.0.0:0", false, false},
	} {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		var out, stderr bytes.Buffer
		if err := Run(ctx, Config{Listen: tt.listen, Store: "memory", BehindCloudflare: tt.trust}, &out, &stderr); err != nil {
			t.Fatal(err)
		}
		want := ""
		if tt.warning {
			want = "aken-relay: --behind-cloudflare trusts CF-Connecting-IP; make sure nothing but the tunnel can reach " + tt.listen + "\n"
		}
		if stderr.String() != want {
			t.Errorf("warning = %q, want %q", stderr.String(), want)
		}
	}
}
