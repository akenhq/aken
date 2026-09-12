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
