// SPDX-License-Identifier: Apache-2.0
package protocol

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"time"
)

const (
	ProtocolVersion = 1
	ProtocolHeader  = "Aken-Protocol"
	DefaultRelayURL = "https://relay.aken.dev"
	DefaultTTL      = 4 * time.Hour
	MaxTTL          = 24 * time.Hour
)

type Caps struct {
	ManifestBytes     int64 `json:"manifest_bytes"`
	ChunkBytes        int64 `json:"chunk_bytes"`
	ChunkCount        int   `json:"chunk_count"`
	TTLDefaultSeconds int64 `json:"ttl_default_seconds"`
	TTLMaxSeconds     int64 `json:"ttl_max_seconds"`
}

var DefaultCaps = Caps{ManifestBytes: MaxManifestBytes, ChunkBytes: ChunkSize + ChunkOverhead, ChunkCount: MaxChunkCount, TTLDefaultSeconds: 14400, TTLMaxSeconds: 86400}

type Info struct {
	ProtocolVersions []int `json:"protocol_versions"`
	Caps             Caps  `json:"caps"`
}

type CreateSessionRequest struct {
	TTLSeconds int64  `json:"ttl_seconds"`
	ChunkCount uint32 `json:"chunk_count"`
}

type SessionInfo struct {
	SessionID      string    `json:"session_id"`
	ExpiresAt      time.Time `json:"expires_at"`
	ChunkCount     uint32    `json:"chunk_count"`
	ChunksStored   uint32    `json:"chunks_stored"`
	ManifestStored bool      `json:"manifest_stored"`
}

type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
}

func AuthorizationHeader(credential [32]byte) string {
	return "Bearer " + base64.RawURLEncoding.EncodeToString(credential[:])
}

func ParseAuthorizationHeader(value string) ([32]byte, bool) {
	if !strings.HasPrefix(value, "Bearer ") {
		return [32]byte{}, false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value[len("Bearer "):])
	if err != nil || len(decoded) != 32 {
		return [32]byte{}, false
	}
	return [32]byte(decoded), true
}

func ParseSessionID(s string) (SessionID, bool) {
	if !lowerHex(s, 32) {
		return SessionID{}, false
	}
	decoded, err := hex.DecodeString(s)
	if err != nil {
		return SessionID{}, false
	}
	return SessionID(decoded), true
}
