// SPDX-License-Identifier: Apache-2.0
package relay

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akenhq/aken/protocol"
)

func TestDirStore(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	store, err := NewDirStore(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	id := protocol.NewToken().SessionID()
	meta := SessionMeta{Mode: "blob", CollectorKey: [32]byte{1}, CollectorMAC: [32]byte{2}, CredentialHash: [32]byte{3}, ExpiresAt: time.Now().UTC().Add(time.Hour), ChunkCount: 1}
	if err := store.CreateSession(ctx, id, meta); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSession(ctx, id, SessionMeta{}); !errors.Is(err, ErrExists) {
		t.Fatal(err)
	}
	if got, err := store.Session(ctx, id); err != nil || got != meta {
		t.Fatal(got, err)
	}
	if _, err := store.GetChunk(ctx, id, 0); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := store.GetManifest(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := store.PutChunk(ctx, id, 0, []byte("chunk")); err != nil {
		t.Fatal(err)
	}
	if err := store.PutChunk(ctx, id, 0, []byte("changed")); !errors.Is(err, ErrExists) {
		t.Fatal(err)
	}
	if err := store.PutManifest(ctx, id, []byte("manifest")); err != nil {
		t.Fatal(err)
	}
	if err := store.PutManifest(ctx, id, []byte("changed")); !errors.Is(err, ErrExists) {
		t.Fatal(err)
	}
	store, err = NewDirStore(root)
	if err != nil {
		t.Fatal(err)
	}
	meta.ChunksStored, meta.ManifestStored = 1, true
	if got, err := store.Session(ctx, id); err != nil || got != meta {
		t.Fatal(got, err)
	}
	if got, err := store.GetChunk(ctx, id, 0); err != nil || string(got) != "chunk" {
		t.Fatal(string(got), err)
	}
	if got, err := store.GetManifest(ctx, id); err != nil || string(got) != "manifest" {
		t.Fatal(string(got), err)
	}
	for _, index := range []uint64{1, 2, ^uint64(0)} {
		if err := store.PutChunk(ctx, id, index, nil); !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
		if _, err := store.GetChunk(ctx, id, index); !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
	}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		want := os.FileMode(0o600)
		if entry.IsDir() {
			want = 0o700
		}
		if info.Mode().Perm() != want || strings.HasPrefix(entry.Name(), ".tmp-") {
			t.Errorf("unexpected file or mode: %s %s", path, info.Mode())
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteSession(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Session(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := store.DeleteSession(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := store.PutChunk(ctx, id, 0, nil); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := store.GetChunk(ctx, id, 0); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := store.PutManifest(ctx, id, nil); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := store.GetManifest(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestDirChunksStored(t *testing.T) {
	root := t.TempDir()
	store, err := NewDirStore(root)
	if err != nil {
		t.Fatal(err)
	}
	id := protocol.NewToken().SessionID()
	if err := store.CreateSession(t.Context(), id, SessionMeta{ChunkCount: 1}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".tmp-0123456789abcdef", "1"} {
		if err := os.WriteFile(filepath.Join(root, id.String(), "chunks", name), []byte("unfinished"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if meta, err := store.Session(t.Context(), id); err != nil || meta.ChunksStored != 0 {
		t.Fatal(meta, err)
	}
	if err := store.PutChunk(t.Context(), id, 0, []byte("chunk")); err != nil {
		t.Fatal(err)
	}
	if meta, err := store.Session(t.Context(), id); err != nil || meta.ChunksStored != 1 {
		t.Fatal(meta, err)
	}
}

func TestDirSweep(t *testing.T) {
	root := t.TempDir()
	store, err := NewDirStore(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	expired, live := protocol.NewToken().SessionID(), protocol.NewToken().SessionID()
	for id, expiry := range map[protocol.SessionID]time.Time{expired: now, live: now.Add(time.Second)} {
		if err := store.CreateSession(t.Context(), id, SessionMeta{ExpiresAt: expiry}); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"unrelated", protocol.NewToken().SessionID().String()} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if deleted, count, err := store.Sweep(now); err != nil || deleted != 1 || count != 1 {
		t.Fatal(deleted, count, err)
	}
	if _, err := store.Session(t.Context(), expired); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := store.Session(t.Context(), live); err != nil {
		t.Fatal(err)
	}
}
