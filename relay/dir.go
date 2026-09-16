// SPDX-License-Identifier: Apache-2.0
package relay

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/akenhq/aken/protocol"
)

type DirStore struct{ root *os.Root }

func NewDirStore(root string) (*DirStore, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	return &DirStore{root: dir}, nil
}

func dirError(err error) error {
	if os.IsNotExist(err) {
		return ErrNotFound
	}
	if os.IsExist(err) {
		return ErrExists
	}
	return err
}

func writeOnce(root *os.Root, path string, body []byte) error {
	var random [8]byte
	rand.Read(random[:])
	tmp := filepath.Join(filepath.Dir(path), ".tmp-"+hex.EncodeToString(random[:]))
	file, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return dirError(err)
	}
	defer func() { _ = root.Remove(tmp) }()
	_, err = file.Write(body)
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return dirError(root.Link(tmp, path))
}

func (d *DirStore) CreateSession(_ context.Context, id protocol.SessionID, meta SessionMeta) error {
	dir := id.String()
	if err := d.root.Mkdir(dir, 0o700); err != nil {
		return dirError(err)
	}
	body, err := json.Marshal(struct {
		Mode           string
		CollectorKey   [32]byte
		CollectorMAC   [32]byte
		CredentialHash [32]byte
		ExpiresAt      time.Time
		ChunkCount     uint32
	}{meta.Mode, meta.CollectorKey, meta.CollectorMAC, meta.CredentialHash, meta.ExpiresAt, meta.ChunkCount})
	if err != nil {
		return err
	}
	if err := writeOnce(d.root, filepath.Join(dir, "meta.json"), body); err != nil {
		return err
	}
	return d.root.Mkdir(filepath.Join(dir, "chunks"), 0o700)
}

func (d *DirStore) readMeta(id protocol.SessionID) (SessionMeta, error) {
	body, err := d.root.ReadFile(filepath.Join(id.String(), "meta.json"))
	if err != nil {
		return SessionMeta{}, dirError(err)
	}
	var meta SessionMeta
	err = json.Unmarshal(body, &meta)
	return meta, err
}

func (d *DirStore) Session(_ context.Context, id protocol.SessionID) (SessionMeta, error) {
	meta, err := d.readMeta(id)
	if err != nil {
		return meta, err
	}
	dir := id.String()
	file, err := d.root.Open(filepath.Join(dir, "chunks"))
	if err != nil {
		return meta, dirError(err)
	}
	defer func() { _ = file.Close() }()
	chunks, err := file.ReadDir(-1)
	if err != nil {
		return meta, dirError(err)
	}
	for _, chunk := range chunks {
		index, err := strconv.ParseUint(chunk.Name(), 10, 64)
		if err == nil && index < uint64(meta.ChunkCount) {
			meta.ChunksStored++
		}
	}
	_, err = d.root.Stat(filepath.Join(dir, "manifest"))
	meta.ManifestStored = err == nil
	if os.IsNotExist(err) {
		err = nil
	}
	return meta, err
}

func (d *DirStore) PutChunk(_ context.Context, id protocol.SessionID, index uint64, ciphertext []byte) error {
	meta, err := d.readMeta(id)
	if err != nil {
		return err
	}
	if index >= uint64(meta.ChunkCount) {
		return ErrNotFound
	}
	return writeOnce(d.root, filepath.Join(id.String(), "chunks", strconv.FormatUint(index, 10)), ciphertext)
}

func (d *DirStore) GetChunk(_ context.Context, id protocol.SessionID, index uint64) ([]byte, error) {
	meta, err := d.readMeta(id)
	if err != nil {
		return nil, err
	}
	if index >= uint64(meta.ChunkCount) {
		return nil, ErrNotFound
	}
	body, err := d.root.ReadFile(filepath.Join(id.String(), "chunks", strconv.FormatUint(index, 10)))
	return body, dirError(err)
}

func (d *DirStore) PutManifest(_ context.Context, id protocol.SessionID, ciphertext []byte) error {
	if _, err := d.readMeta(id); err != nil {
		return err
	}
	return writeOnce(d.root, filepath.Join(id.String(), "manifest"), ciphertext)
}

func (d *DirStore) GetManifest(_ context.Context, id protocol.SessionID) ([]byte, error) {
	if _, err := d.readMeta(id); err != nil {
		return nil, err
	}
	body, err := d.root.ReadFile(filepath.Join(id.String(), "manifest"))
	return body, dirError(err)
}

func (d *DirStore) DeleteSession(_ context.Context, id protocol.SessionID) error {
	dir := id.String()
	if _, err := d.root.Stat(dir); err != nil {
		return dirError(err)
	}
	return d.root.RemoveAll(dir)
}

func (d *DirStore) Sweep(now time.Time) (deleted, live int, err error) {
	file, err := d.root.Open(".")
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = file.Close() }()
	entries, err := file.ReadDir(-1)
	if err != nil {
		return 0, 0, err
	}
	for _, entry := range entries {
		id, ok := protocol.ParseSessionID(entry.Name())
		if !entry.IsDir() || !ok {
			continue
		}
		meta, err := d.readMeta(id)
		if err != nil {
			continue
		}
		if now.Before(meta.ExpiresAt) {
			live++
			continue
		}
		if err := d.root.RemoveAll(entry.Name()); err != nil {
			return deleted, live, err
		}
		deleted++
	}
	return deleted, live, nil
}
