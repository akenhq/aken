// SPDX-License-Identifier: Apache-2.0
package relay

import (
	"context"
	"errors"
	"time"

	"github.com/akenhq/aken/protocol"
)

var (
	ErrNotFound = errors.New("relay: not found")
	ErrExists   = errors.New("relay: already exists")
)

type SessionMeta struct {
	Mode           string
	CollectorKey   [32]byte
	CollectorMAC   [32]byte
	CredentialHash [32]byte
	ExpiresAt      time.Time
	ChunkCount     uint32
	ChunksStored   uint32
	ManifestStored bool
}

// Store keeps sessions and their ciphertext. Every method is safe for concurrent use.
// Objects are written once: a second write of the same session, chunk or manifest returns ErrExists.
// Expiry is the handler's job; a store may delete expired sessions on its own as well.
type Store interface {
	CreateSession(ctx context.Context, id protocol.SessionID, meta SessionMeta) error
	Session(ctx context.Context, id protocol.SessionID) (SessionMeta, error)
	PutChunk(ctx context.Context, id protocol.SessionID, index uint64, ciphertext []byte) error
	GetChunk(ctx context.Context, id protocol.SessionID, index uint64) ([]byte, error)
	PutManifest(ctx context.Context, id protocol.SessionID, ciphertext []byte) error
	GetManifest(ctx context.Context, id protocol.SessionID) ([]byte, error)
	DeleteSession(ctx context.Context, id protocol.SessionID) error
}
