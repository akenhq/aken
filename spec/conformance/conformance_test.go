// SPDX-License-Identifier: Apache-2.0
package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/akenhq/aken/internal/devrelay"
	"github.com/akenhq/aken/protocol"
)

func relayURL(t *testing.T) string {
	t.Helper()
	if url := os.Getenv("AKEN_RELAY_URL"); url != "" {
		return url
	}
	server := httptest.NewServer(devrelay.New())
	t.Cleanup(server.Close)
	return server.URL
}

func newClient(t *testing.T) (*protocol.RelayClient, protocol.Token) {
	t.Helper()
	token := protocol.NewToken()
	client, err := protocol.NewRelayClient(relayURL(t), token.RelayCredential())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// The test context is canceled before cleanup runs.
		ctx := context.Background()
		if err := client.DeleteSession(ctx, token.SessionID()); err != nil && !protocol.IsNotFound(err) {
			t.Error("cleanup", err)
		}
	})
	return client, token
}

func relayError(t *testing.T, err error, status int, code string) {
	t.Helper()
	var got *protocol.RelayError
	if !errors.As(err, &got) || got.Status != status || got.Code != code {
		t.Fatalf("error = %v, want %d %s", err, status, code)
	}
}

func raw(t *testing.T, client *protocol.RelayClient, token protocol.Token, method, suffix string, body []byte, version string, status int, code string) []byte {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, client.BaseURL+"/v0/sessions/"+token.SessionID().String()+suffix, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(protocol.ProtocolHeader, version)
	req.Header.Set("Authorization", protocol.AuthorizationHeader(token.RelayCredential()))
	req.Header.Set("Content-Type", "application/octet-stream")
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != status {
		t.Fatalf("status = %d, want %d: %s", response.StatusCode, status, data)
	}
	if code != "" {
		var got protocol.ErrorResponse
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		if got.Error != code {
			t.Fatalf("code = %q, want %q", got.Error, code)
		}
	}
	return data
}

func TestInfoAndSession(t *testing.T) {
	client, token := newClient(t)
	info, err := client.Info(t.Context())
	if err != nil || !slices.Contains(info.ProtocolVersions, 1) || info.Caps != protocol.DefaultCaps {
		t.Fatal("info", info, err)
	}
	created, err := client.CreateSession(t.Context(), token.SessionID(), 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if created.SessionID != token.SessionID().String() || created.ChunkCount != 1 || created.ChunksStored != 0 || created.ManifestStored || created.ExpiresAt.IsZero() {
		t.Fatal("create response", created)
	}
	got, err := client.Session(t.Context(), token.SessionID())
	if err != nil || got != created {
		t.Fatal("session response", got, err)
	}
	_, err = client.CreateSession(t.Context(), token.SessionID(), 0, 1)
	relayError(t, err, 409, "already_exists")
	if err := client.DeleteSession(t.Context(), token.SessionID()); err != nil {
		t.Fatal(err)
	}
	_, err = client.Session(t.Context(), token.SessionID())
	relayError(t, err, 404, "not_found")
}

func TestAuthentication(t *testing.T) {
	client, token := newClient(t)
	if _, err := client.CreateSession(t.Context(), token.SessionID(), 0, 1); err != nil {
		t.Fatal(err)
	}
	body := raw(t, client, token, "GET", "", nil, "", 426, "unsupported_version")
	var version struct {
		ProtocolVersions []int `json:"protocol_versions"`
	}
	if err := json.Unmarshal(body, &version); err != nil || !slices.Contains(version.ProtocolVersions, 1) {
		t.Fatal("version response", err)
	}
	wrong, err := protocol.NewRelayClient(client.BaseURL, protocol.NewToken().RelayCredential())
	if err != nil {
		t.Fatal(err)
	}
	_, err = wrong.Session(t.Context(), token.SessionID())
	relayError(t, err, 404, "not_found")
	_, err = client.Session(t.Context(), protocol.NewToken().SessionID())
	relayError(t, err, 404, "not_found")
}

func TestTTL(t *testing.T) {
	client, token := newClient(t)
	_, err := client.CreateSession(t.Context(), token.SessionID(), protocol.MaxTTL+time.Second, 1)
	relayError(t, err, 400, "ttl_too_long")
}

func TestChunkRejections(t *testing.T) {
	for _, tt := range []struct {
		name   string
		index  uint64
		size   int
		status int
		code   string
	}{
		{"index at count", 2, 17, 400, "bad_request"},
		{"index above count", 3, 17, 400, "bad_request"},
		{"short non-last", 0, 17, 400, "bad_request"},
		{"over cap", 0, protocol.ChunkSize + protocol.ChunkOverhead + 1, 413, "too_large"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client, token := newClient(t)
			if _, err := client.CreateSession(t.Context(), token.SessionID(), 0, 2); err != nil {
				t.Fatal(err)
			}
			relayError(t, client.PutChunk(t.Context(), token.SessionID(), tt.index, make([]byte, tt.size)), tt.status, tt.code)
		})
	}
}

func TestUpload(t *testing.T) {
	client, token := newClient(t)
	id := token.SessionID()
	if _, err := client.CreateSession(t.Context(), id, 0, 3); err != nil {
		t.Fatal(err)
	}
	chunks := [][]byte{bytes.Repeat([]byte{1}, protocol.ChunkSize+protocol.ChunkOverhead), bytes.Repeat([]byte{2}, protocol.ChunkSize+protocol.ChunkOverhead), bytes.Repeat([]byte{3}, 1016)}
	manifest := []byte("opaque encrypted manifest")
	for i, chunk := range chunks {
		if i == len(chunks)-1 {
			relayError(t, client.PutManifest(t.Context(), id, manifest), 409, "chunks_missing")
		}
		if err := client.PutChunk(t.Context(), id, uint64(i), chunk); err != nil {
			t.Fatal(err)
		}
		raw(t, client, token, "PUT", "/blob/chunks/"+strconv.Itoa(i), chunk, "1", 409, "already_exists")
	}
	if err := client.PutManifest(t.Context(), id, manifest); err != nil {
		t.Fatal(err)
	}
	relayError(t, client.PutManifest(t.Context(), id, manifest), 409, "already_exists")
	got, err := client.GetManifest(t.Context(), id)
	if err != nil || !bytes.Equal(got, manifest) {
		t.Fatal("manifest differs", err)
	}
	for i, chunk := range chunks {
		got, err := client.GetChunk(t.Context(), id, uint64(i))
		if err != nil || !bytes.Equal(got, chunk) {
			t.Fatal("chunk differs", i, err)
		}
	}
	session, err := client.Session(t.Context(), id)
	if err != nil || session.ChunksStored != 3 || !session.ManifestStored {
		t.Fatal("upload incomplete", session, err)
	}
}

func createPersistent(t *testing.T, client *protocol.RelayClient, token protocol.Token) protocol.SessionInfo {
	t.Helper()
	info, err := client.CreatePersistentSession(t.Context(), token.SessionID(), 0, [32]byte{1}, [32]byte{2})
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func joinPersistent(t *testing.T, client *protocol.RelayClient, token protocol.Token) protocol.JoinInfo {
	t.Helper()
	info, err := client.Join(t.Context(), token.SessionID(), [32]byte{3}, [32]byte{4}, "cli")
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func TestPersistentSessionAndJoin(t *testing.T) {
	client, token := newClient(t)
	id := token.SessionID()
	created := createPersistent(t, client, token)
	if created.Mode != "session" || created.Joined || created.ChunkCount != 0 || created.ChunksStored != 0 || created.ManifestStored || created.CollectorKey != protocol.EncodeKey([32]byte{1}) || created.CollectorMAC != protocol.EncodeKey([32]byte{2}) || time.Until(created.ExpiresAt) > protocol.DefaultSessionTTL || time.Until(created.ExpiresAt) < protocol.DefaultSessionTTL-time.Minute {
		t.Fatal("session creation", created)
	}
	got, err := client.Session(t.Context(), id)
	if err != nil || got != created {
		t.Fatal("session read", got, err)
	}
	for _, suffix := range []string{"/blob/chunks/0", "/blob/manifest"} {
		for _, method := range []string{"GET", "PUT"} {
			raw(t, client, token, method, suffix, nil, "1", 409, "wrong_mode")
		}
	}
	if info, ok, err := client.WaitJoin(t.Context(), id, time.Second); err != nil || ok || info != (protocol.JoinInfo{}) {
		t.Fatal("unjoined wait", info, ok, err)
	}
	if body := raw(t, client, token, "GET", "/join?wait=0", nil, "1", 204, ""); len(body) != 0 {
		t.Fatal("204 body")
	}
	joined := joinPersistent(t, client, token)
	if joined.CollectorKey != created.CollectorKey || joined.CollectorMAC != created.CollectorMAC || joined.MCPKey != protocol.EncodeKey([32]byte{3}) || joined.MCPMAC != protocol.EncodeKey([32]byte{4}) || joined.Via != "cli" || joined.JoinedAt.IsZero() {
		t.Fatal("join response", joined)
	}
	_, err = client.Join(t.Context(), id, [32]byte{5}, [32]byte{6}, "chat")
	relayError(t, err, 409, "already_exists")
	info, ok, err := client.WaitJoin(t.Context(), id, 0)
	if err != nil || !ok || info != joined {
		t.Fatal("first join changed", info, ok, err)
	}
	got, err = client.Session(t.Context(), id)
	if err != nil || !got.Joined || !got.ExpiresAt.Equal(created.ExpiresAt) {
		t.Fatal("joined session", got, err)
	}
	for _, suffix := range []string{"/join", "/jobs", "/results"} {
		raw(t, client, token, "GET", suffix+"?wait=31", nil, "1", 400, "bad_request")
	}
}

func TestPersistentMessages(t *testing.T) {
	for _, kind := range []string{"jobs", "results"} {
		t.Run(kind, func(t *testing.T) {
			client, token := newClient(t)
			id := token.SessionID()
			createPersistent(t, client, token)
			post, poll, capBytes := client.PostJob, client.PollJobs, protocol.MaxJobBytes
			if kind == "results" {
				post, poll, capBytes = client.PostResult, client.PollResults, protocol.MaxResultBytes
			}
			e := protocol.Envelope{Version: 1, SessionID: id.String(), Seq: 1, Class: 1, Payload: bytes.Repeat([]byte{1}, 17)}
			relayError(t, post(t.Context(), id, e), 409, "not_joined")
			joinPersistent(t, client, token)
			body, err := json.Marshal(e)
			if err != nil {
				t.Fatal(err)
			}
			if data := raw(t, client, token, "POST", "/"+kind, body, "1", 202, ""); len(data) != 0 {
				t.Fatal("202 body")
			}
			if err := post(t.Context(), id, e); err != nil {
				t.Fatal("idempotent replay", err)
			}
			bad := e
			bad.Payload = bytes.Repeat([]byte{2}, 17)
			relayError(t, post(t.Context(), id, bad), 409, "bad_sequence")
			bad = e
			bad.Seq = 3
			relayError(t, post(t.Context(), id, bad), 409, "bad_sequence")
			bad = e
			bad.Seq = 2
			bad.Class = 2
			relayError(t, post(t.Context(), id, bad), 403, "class_not_allowed")
			bad = e
			bad.Seq = 2
			bad.Payload = make([]byte, capBytes+1)
			relayError(t, post(t.Context(), id, bad), 413, "too_large")
			bad = e
			bad.Version = 2
			relayError(t, post(t.Context(), id, bad), 400, "bad_request")
			messages, err := poll(t.Context(), id, 0)
			if err != nil || len(messages) != 1 || messages[0].Seq != 1 || !bytes.Equal(messages[0].Payload, e.Payload) {
				t.Fatal("messages", messages, err)
			}
			if err := post(t.Context(), id, e); err != nil {
				t.Fatal("replay after delivery", err)
			}
			if messages, err := poll(t.Context(), id, time.Second); err != nil || messages != nil {
				t.Fatal("duplicate delivery", messages, err)
			}
			for seq := uint64(2); seq <= 65; seq++ {
				e.Seq = seq
				if err := post(t.Context(), id, e); err != nil {
					t.Fatal("queue fill", err)
				}
			}
			if err := post(t.Context(), id, e); err != nil {
				t.Fatal("full queue replay", err)
			}
			e.Seq = 66
			relayError(t, post(t.Context(), id, e), 429, "queue_full")
			messages, err = poll(t.Context(), id, 0)
			if err != nil || len(messages) != 64 {
				t.Fatal("full queue drain", len(messages), err)
			}
			for i, e := range messages {
				if e.Seq != uint64(i+2) {
					t.Fatal("queue order")
				}
			}
			if err := post(t.Context(), id, e); err != nil {
				t.Fatal("queue after drain", err)
			}
		})
	}
}

func TestPersistentDeleteWakesPoll(t *testing.T) {
	client, token := newClient(t)
	createPersistent(t, client, token)
	joinPersistent(t, client, token)
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	go func() { _, err := client.PollJobs(ctx, token.SessionID(), 30*time.Second); done <- err }()
	select {
	case err := <-done:
		t.Fatal("empty poll returned early", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := client.DeleteSession(t.Context(), token.SessionID()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		relayError(t, err, 404, "not_found")
	case <-ctx.Done():
		t.Fatal("delete did not wake poll")
	}
}

func TestPersistentCompatibility(t *testing.T) {
	client, token := newClient(t)
	created, err := client.CreateSession(t.Context(), token.SessionID(), 0, 1)
	if err != nil || created.Mode != "blob" || created.Joined || created.CollectorKey != "" || created.CollectorMAC != "" {
		t.Fatal("blob compatibility", created, err)
	}
	for _, suffix := range []string{"/join", "/jobs", "/results"} {
		for _, method := range []string{"GET", "POST"} {
			raw(t, client, token, method, suffix, nil, "1", 409, "wrong_mode")
		}
	}
	if err := client.PutChunk(t.Context(), token.SessionID(), 0, make([]byte, 17)); err != nil {
		t.Fatal(err)
	}
	if err := client.PutManifest(t.Context(), token.SessionID(), []byte("manifest")); err != nil {
		t.Fatal(err)
	}
}
