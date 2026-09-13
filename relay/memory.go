// SPDX-License-Identifier: Apache-2.0
package relay

import (
	"bytes"
	"context"
	"sync"
	"time"

	"github.com/akenhq/aken/protocol"
)

type MemoryStore struct {
	mu       sync.Mutex
	sessions map[protocol.SessionID]*memorySession
}

type memorySession struct {
	credHash   [32]byte
	expiresAt  time.Time
	chunkCount uint32
	chunks     [][]byte
	stored     uint32
	manifest   []byte
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{sessions: make(map[protocol.SessionID]*memorySession)}
}

func (m *MemoryStore) CreateSession(_ context.Context, id protocol.SessionID, meta SessionMeta) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessions[id] != nil {
		return ErrExists
	}
	m.sessions[id] = &memorySession{credHash: meta.CredentialHash, expiresAt: meta.ExpiresAt, chunkCount: meta.ChunkCount, chunks: make([][]byte, meta.ChunkCount)}
	return nil
}

func (m *MemoryStore) Session(_ context.Context, id protocol.SessionID) (SessionMeta, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess := m.sessions[id]
	if sess == nil {
		return SessionMeta{}, ErrNotFound
	}
	return SessionMeta{CredentialHash: sess.credHash, ExpiresAt: sess.expiresAt, ChunkCount: sess.chunkCount, ChunksStored: sess.stored, ManifestStored: sess.manifest != nil}, nil
}

func (m *MemoryStore) PutChunk(_ context.Context, id protocol.SessionID, index uint64, ciphertext []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess := m.sessions[id]
	if sess == nil || index >= uint64(sess.chunkCount) {
		return ErrNotFound
	}
	if sess.chunks[index] != nil {
		return ErrExists
	}
	sess.chunks[index] = append([]byte{}, ciphertext...)
	sess.stored++
	return nil
}

func (m *MemoryStore) GetChunk(_ context.Context, id protocol.SessionID, index uint64) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess := m.sessions[id]
	if sess == nil || index >= uint64(sess.chunkCount) || sess.chunks[index] == nil {
		return nil, ErrNotFound
	}
	return bytes.Clone(sess.chunks[index]), nil
}

func (m *MemoryStore) PutManifest(_ context.Context, id protocol.SessionID, ciphertext []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess := m.sessions[id]
	if sess == nil {
		return ErrNotFound
	}
	if sess.manifest != nil {
		return ErrExists
	}
	sess.manifest = append([]byte{}, ciphertext...)
	return nil
}

func (m *MemoryStore) GetManifest(_ context.Context, id protocol.SessionID) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess := m.sessions[id]
	if sess == nil || sess.manifest == nil {
		return nil, ErrNotFound
	}
	return bytes.Clone(sess.manifest), nil
}

func (m *MemoryStore) DeleteSession(_ context.Context, id protocol.SessionID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessions[id] == nil {
		return ErrNotFound
	}
	delete(m.sessions, id)
	return nil
}

func (m *MemoryStore) Sweep(now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, sess := range m.sessions {
		if !now.Before(sess.expiresAt) {
			delete(m.sessions, id)
		}
	}
}
