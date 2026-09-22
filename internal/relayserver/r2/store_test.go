// SPDX-License-Identifier: Apache-2.0
package r2

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akenhq/aken/protocol"
	"github.com/akenhq/aken/relay"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestConfigFromEnv(t *testing.T) {
	names := []string{"AKEN_R2_ACCOUNT_ID", "AKEN_R2_BUCKET", "AKEN_R2_ACCESS_KEY_ID", "AKEN_R2_SECRET_ACCESS_KEY"}
	for _, name := range names {
		t.Setenv(name, "value")
	}
	for _, name := range names {
		t.Setenv(name, "")
		if _, err := ConfigFromEnv(); err == nil || err.Error() != "missing environment variables: "+name {
			t.Fatalf("error = %v", err)
		}
		t.Setenv(name, "value")
	}
	for i, name := range names {
		t.Setenv(name, "")
		if _, err := ConfigFromEnv(); err == nil || err.Error() != "missing environment variables: "+strings.Join(names[:i+1], ", ") {
			t.Fatalf("error = %v", err)
		}
	}
	for _, name := range names {
		t.Setenv(name, "value")
	}
	cfg, err := ConfigFromEnv()
	if err != nil || cfg != (Config{"value", "value", "value", "value"}) {
		t.Fatal(cfg, err)
	}
	s, err := New(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	o := s.client.Options()
	if aws.ToString(o.BaseEndpoint) != "https://value.r2.cloudflarestorage.com" || o.Region != "auto" || o.UsePathStyle || o.RequestChecksumCalculation != aws.RequestChecksumCalculationWhenRequired || o.ResponseChecksumValidation != aws.ResponseChecksumValidationWhenRequired {
		t.Fatal("client options", o)
	}
	cred, err := o.Credentials.Retrieve(t.Context())
	if err != nil || cred.AccessKeyID != "value" || cred.SecretAccessKey != "value" {
		t.Fatal("credentials", err)
	}
}

type step struct {
	method, path, query string
	status              int
	body                string
	check               func(*http.Request)
}

func scriptedStore(t *testing.T, steps []step) *Store {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(calls.Add(1)) - 1
		if i >= len(steps) {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			w.WriteHeader(500)
			return
		}
		s := steps[i]
		path := "/bucket"
		if s.path != "" {
			path += "/" + s.path
		}
		query := r.URL.Query()
		query.Del("x-id")
		if r.Method != s.method || r.URL.Path != path || query.Encode() != s.query {
			t.Errorf("request %d = %s %s, want %s /bucket/%s?%s", i, r.Method, r.URL, s.method, s.path, s.query)
		}
		if s.check != nil {
			s.check(r)
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(s.status)
		_, _ = io.WriteString(w, s.body)
	}))
	t.Cleanup(func() {
		server.Close()
		if int(calls.Load()) != len(steps) {
			t.Errorf("calls = %d, want %d", calls.Load(), len(steps))
		}
	})
	s, err := New(t.Context(), Config{"account", "bucket", "access", "secret"})
	if err != nil {
		t.Fatal(err)
	}
	o := s.client.Options()
	o.BaseEndpoint = &server.URL
	o.UsePathStyle = true
	o.RetryMaxAttempts = 1
	s.client = s3.New(o)
	return s
}

func TestWritesAndMetadata(t *testing.T) {
	id := protocol.NewToken().SessionID()
	p := prefix(id)
	hash := sha256.Sum256([]byte("credential"))
	meta := relay.SessionMeta{Mode: "session", CollectorKey: [32]byte{1}, CollectorMAC: [32]byte{2}, CredentialHash: hash, ExpiresAt: time.Now().Add(time.Hour), ChunkCount: 2}
	var encoded []byte
	conditional := func(r *http.Request) {
		if r.Header.Get("If-None-Match") != "*" {
			t.Error("missing conditional put")
		}
	}
	s := scriptedStore(t, []step{
		{"PUT", p + "meta.json", "", 200, "", func(r *http.Request) {
			conditional(r)
			var err error
			encoded, err = io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			var m metadata
			if err := json.Unmarshal(encoded, &m); err != nil {
				t.Error(err)
			}
			if m.Mode != meta.Mode || m.CollectorKey != protocol.EncodeKey(meta.CollectorKey) || m.CollectorMAC != protocol.EncodeKey(meta.CollectorMAC) {
				t.Errorf("session metadata = %s", encoded)
			}
			if m.CredentialHash != hex.EncodeToString(hash[:]) || !m.ExpiresAt.Equal(meta.ExpiresAt) || m.ChunkCount != 2 || m.CreatedAt.IsZero() || !strings.Contains(string(encoded), "Z\"") {
				t.Errorf("metadata = %s", encoded)
			}
		}},
		{"PUT", p + "meta.json", "", 412, `<Error><Code>PreconditionFailed</Code></Error>`, conditional},
		{"HEAD", p + "meta.json", "", 200, "", nil},
		{"PUT", p + "chunks/00007", "", 200, "", conditional},
		{"HEAD", p + "meta.json", "", 200, "", nil},
		{"PUT", p + "manifest", "", 412, `<Error><Code>PreconditionFailed</Code></Error>`, conditional},
		{"HEAD", p + "meta.json", "", 404, "", nil},
		{"HEAD", p + "meta.json", "", 404, "", nil},
		{"GET", p + "chunks/00007", "", 200, "ciphertext", nil},
		{"GET", p + "manifest", "", 404, `<Error><Code>NoSuchKey</Code></Error>`, nil},
		{"GET", p + "manifest", "", 403, `<Error><Code>AccessDenied</Code></Error>`, nil},
	})
	ctx := t.Context()
	if err := s.CreateSession(ctx, id, meta); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(ctx, id, meta); !errors.Is(err, relay.ErrExists) {
		t.Fatal(err)
	}
	if err := s.PutChunk(ctx, id, 7, []byte("ciphertext")); err != nil {
		t.Fatal(err)
	}
	if err := s.PutManifest(ctx, id, nil); !errors.Is(err, relay.ErrExists) {
		t.Fatal(err)
	}
	if err := s.PutChunk(ctx, id, 0, nil); !errors.Is(err, relay.ErrNotFound) {
		t.Fatal(err)
	}
	if err := s.PutManifest(ctx, id, nil); !errors.Is(err, relay.ErrNotFound) {
		t.Fatal(err)
	}
	if body, err := s.GetChunk(ctx, id, 7); err != nil || string(body) != "ciphertext" {
		t.Fatal(string(body), err)
	}
	if _, err := s.GetManifest(ctx, id); !errors.Is(err, relay.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := s.GetManifest(ctx, id); err == nil || !strings.HasPrefix(err.Error(), "r2 get object:") || strings.Contains(err.Error(), p) {
		t.Fatal(err)
	}
	s = scriptedStore(t, []step{
		{"GET", p + "meta.json", "", 200, string(encoded), nil},
		{"GET", "", "list-type=2&prefix=" + strings.ReplaceAll(p+"chunks/", "/", "%2F"), 200, "<ListBucketResult/>", nil},
		{"HEAD", p + "manifest", "", 404, "", nil},
	})
	if got, err := s.Session(ctx, id); err != nil || got.Mode != meta.Mode || got.CollectorKey != meta.CollectorKey || got.CollectorMAC != meta.CollectorMAC {
		t.Fatal(got, err)
	}
}

func TestInvalidCollectorMetadata(t *testing.T) {
	for _, field := range []string{"collector_key", "collector_mac"} {
		for _, value := range []string{"!", "AA", protocol.EncodeKey([32]byte{}) + "="} {
			id := protocol.NewToken().SessionID()
			body := fmt.Sprintf(`{"credential_hash":%q,%q:%q}`, strings.Repeat("00", 32), field, value)
			s := scriptedStore(t, []step{{"GET", prefix(id) + "meta.json", "", 200, body, nil}})
			if _, err := s.Session(t.Context(), id); err == nil || !strings.HasPrefix(err.Error(), "r2 decode metadata: invalid collector") {
				t.Fatal(field, value, err)
			}
		}
	}
}

func TestSessionPagination(t *testing.T) {
	id := protocol.NewToken().SessionID()
	p := prefix(id)
	body := fmt.Sprintf(`{"credential_hash":"%s","expires_at":"2026-09-14T00:00:00Z","chunk_count":2,"created_at":"2026-09-13T00:00:00Z"}`, strings.Repeat("ab", 32))
	s := scriptedStore(t, []step{
		{"GET", p + "meta.json", "", 200, body, nil},
		{"GET", "", "list-type=2&prefix=" + strings.ReplaceAll(p+"chunks/", "/", "%2F"), 200, `<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>next</NextContinuationToken><Contents><Key>one</Key></Contents></ListBucketResult>`, nil},
		{"GET", "", "continuation-token=next&list-type=2&prefix=" + strings.ReplaceAll(p+"chunks/", "/", "%2F"), 200, `<ListBucketResult><IsTruncated>false</IsTruncated><Contents><Key>two</Key></Contents></ListBucketResult>`, nil},
		{"HEAD", p + "manifest", "", 200, "", nil},
	})
	meta, err := s.Session(t.Context(), id)
	if err != nil || meta.Mode != "" || meta.CollectorKey != [32]byte{} || meta.CollectorMAC != [32]byte{} || meta.ChunkCount != 2 || meta.ChunksStored != 2 || !meta.ManifestStored || meta.CredentialHash[0] != 0xab {
		t.Fatal(meta, err)
	}
}

func TestDeleteBatches(t *testing.T) {
	id := protocol.NewToken().SessionID()
	query := "list-type=2&prefix=" + strings.ReplaceAll(prefix(id), "/", "%2F")
	contents := strings.Repeat("<Contents><Key>object</Key></Contents>", 1000)
	check := func(want int) func(*http.Request) {
		return func(r *http.Request) {
			var body struct {
				Objects []struct{ Key string } `xml:"Object"`
			}
			if err := xml.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Objects) != want {
				t.Errorf("delete batch = %d, error = %v", len(body.Objects), err)
			}
		}
	}
	s := scriptedStore(t, []step{
		{"GET", "", query, 200, "<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>next</NextContinuationToken>" + contents + "</ListBucketResult>", nil},
		{"GET", "", "continuation-token=next&" + query, 200, "<ListBucketResult><Contents><Key>last</Key></Contents></ListBucketResult>", nil},
		{"POST", "", "delete=", 200, "<DeleteResult/>", check(1000)},
		{"POST", "", "delete=", 200, "<DeleteResult/>", check(1)},
		{"GET", "", query, 200, "<ListBucketResult/>", nil},
		{"GET", "", query, 200, "<ListBucketResult><Contents><Key>last</Key></Contents></ListBucketResult>", nil},
		{"POST", "", "delete=", 200, "<DeleteResult><Error><Key>last</Key><Code>AccessDenied</Code></Error></DeleteResult>", check(1)},
	})
	if err := s.DeleteSession(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSession(t.Context(), id); !errors.Is(err, relay.ErrNotFound) {
		t.Fatal(err)
	}
	if err := s.DeleteSession(t.Context(), id); err == nil || err.Error() != "r2 delete objects: object deletion failed" {
		t.Fatal(err)
	}
}

func TestSweep(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name             string
		expiry, modified time.Time
		missing, remove  bool
	}{
		{"live", now.Add(time.Hour), now, false, false},
		{"expired", now, now, false, true},
		{"old orphan", time.Time{}, now.Add(-time.Hour - time.Second), true, true},
		{"recent orphan", time.Time{}, now.Add(-time.Hour), true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			id := protocol.NewToken().SessionID()
			p := prefix(id)
			steps := []step{{"GET", "", "delimiter=%2F&list-type=2&prefix=sessions%2F", 200, "<ListBucketResult><CommonPrefixes><Prefix>" + p + "</Prefix></CommonPrefixes></ListBucketResult>", nil}}
			meta := step{"GET", p + "meta.json", "", 200, fmt.Sprintf(`{"credential_hash":"%s","expires_at":%q}`, strings.Repeat("00", 32), tt.expiry.Format(time.RFC3339)), nil}
			if tt.missing {
				meta.status = 404
				meta.body = `<Error><Code>NoSuchKey</Code></Error>`
			}
			steps = append(steps, meta)
			list := step{"GET", "", "list-type=2&prefix=" + strings.ReplaceAll(p, "/", "%2F"), 200, "<ListBucketResult><Contents><Key>" + p + "manifest</Key><LastModified>" + tt.modified.Format(time.RFC3339) + "</LastModified></Contents></ListBucketResult>", nil}
			if tt.missing {
				steps = append(steps, list)
			}
			if tt.remove {
				steps = append(steps, list, step{"POST", "", "delete=", 200, "<DeleteResult/>", nil})
			}
			s := scriptedStore(t, steps)
			deleted, live, err := s.Sweep(t.Context(), now)
			wantDeleted, wantLive := 0, 0
			if tt.remove {
				wantDeleted = 1
			}
			if !tt.missing && !tt.remove {
				wantLive = 1
			}
			if err != nil || deleted != wantDeleted || live != wantLive {
				t.Fatal(deleted, live, err)
			}
		})
	}
}

func TestR2Integration(t *testing.T) {
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Skip("R2 integration requires all four AKEN_R2_* variables")
	}
	s, err := New(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	id := protocol.NewToken().SessionID()
	t.Cleanup(func() {
		if err := s.DeleteSession(context.Background(), id); err != nil && !errors.Is(err, relay.ErrNotFound) {
			t.Error(err)
		}
	})
	meta := relay.SessionMeta{CredentialHash: sha256.Sum256([]byte("test credential")), ExpiresAt: time.Now().UTC().Add(time.Hour), ChunkCount: 1}
	if err := s.CreateSession(t.Context(), id, meta); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(t.Context(), id, meta); !errors.Is(err, relay.ErrExists) {
		t.Fatal(err)
	}
	data := []byte("opaque ciphertext")
	if err := s.PutChunk(t.Context(), id, 0, data); err != nil {
		t.Fatal(err)
	}
	if err := s.PutChunk(t.Context(), id, 0, data); !errors.Is(err, relay.ErrExists) {
		t.Fatal(err)
	}
	if err := s.PutManifest(t.Context(), id, data); err != nil {
		t.Fatal(err)
	}
	if err := s.PutManifest(t.Context(), id, data); !errors.Is(err, relay.ErrExists) {
		t.Fatal(err)
	}
	got, err := s.Session(t.Context(), id)
	if err != nil || got.ChunksStored != 1 || !got.ManifestStored || got.CredentialHash != meta.CredentialHash || !got.ExpiresAt.Equal(meta.ExpiresAt) {
		t.Fatal(got, err)
	}
	for _, get := range []func() ([]byte, error){func() ([]byte, error) { return s.GetChunk(t.Context(), id, 0) }, func() ([]byte, error) { return s.GetManifest(t.Context(), id) }} {
		if got, err := get(); err != nil || !bytes.Equal(got, data) {
			t.Fatal(got, err)
		}
	}
	if err := s.DeleteSession(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Session(t.Context(), id); !errors.Is(err, relay.ErrNotFound) {
		t.Fatal(err)
	}
}
