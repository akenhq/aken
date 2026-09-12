// SPDX-License-Identifier: Apache-2.0
package protocol

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"time"
)

const (
	BlobVersion        = 1
	ChunkSize          = 1 << 20
	ChunkOverhead      = 16
	MaxChunkCount      = 128
	MaxManifestBytes   = 1 << 20
	PlaceholderPattern = `<(secret|token|jwt|key|ip|email|phone|name|address)#[1-9][0-9]*>`
)

var RedactionCategories = []string{"secret", "token", "jwt", "key", "ip", "email", "phone", "name", "address"}
var (
	ErrBlobAuth = errors.New("protocol: blob authentication failed")
	ErrBlobSize = errors.New("protocol: blob size out of range")
)

type BlobKeys struct{ manifest, chunk [32]byte }

func DeriveBlobKeys(contentRoot [32]byte) BlobKeys {
	derive := func(label string) [32]byte {
		key, err := hkdf.Key(sha256.New, contentRoot[:], []byte("aken/blob/v1"), label, 32)
		if err != nil {
			// The fixed output length is within HKDF's limit.
			panic(err)
		}
		return [32]byte(key)
	}
	return BlobKeys{manifest: derive("manifest-key"), chunk: derive("chunk-key")}
}

func ChunkCount(plaintextLen int64) (uint32, error) {
	if plaintextLen <= 0 || plaintextLen > MaxChunkCount*ChunkSize {
		return 0, ErrBlobSize
	}
	return uint32((plaintextLen + ChunkSize - 1) / ChunkSize), nil
}

func SplitChunks(plaintext []byte) [][]byte {
	var chunks [][]byte
	for len(plaintext) > 0 {
		n := min(len(plaintext), ChunkSize)
		chunks = append(chunks, plaintext[:n])
		plaintext = plaintext[n:]
	}
	return chunks
}

func blobAD(index uint64, count uint32) []byte {
	ad := make([]byte, 13)
	ad[0] = BlobVersion
	binary.BigEndian.PutUint64(ad[1:9], index)
	binary.BigEndian.PutUint32(ad[9:], count)
	return ad
}

func blobNonce(index uint64) []byte {
	nonce := make([]byte, 12)
	binary.BigEndian.PutUint64(nonce[4:], index)
	return nonce
}

func blobAEAD(key [32]byte) cipher.AEAD {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		panic(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic(err)
	}
	return aead
}

func (k BlobKeys) SealChunk(index uint64, count uint32, plaintext []byte) ([]byte, error) {
	if count == 0 || count > MaxChunkCount || index >= uint64(count) || len(plaintext) == 0 || len(plaintext) > ChunkSize || (index < uint64(count-1) && len(plaintext) != ChunkSize) {
		return nil, ErrBlobSize
	}
	return blobAEAD(k.chunk).Seal(nil, blobNonce(index), plaintext, blobAD(index, count)), nil
}

func (k BlobKeys) OpenChunk(index uint64, count uint32, ciphertext []byte) ([]byte, error) {
	if count == 0 || count > MaxChunkCount || index >= uint64(count) || len(ciphertext) < 17 || len(ciphertext) > ChunkSize+ChunkOverhead {
		return nil, ErrBlobSize
	}
	plaintext, err := blobAEAD(k.chunk).Open(nil, blobNonce(index), ciphertext, blobAD(index, count))
	if err != nil {
		return nil, ErrBlobAuth
	}
	return plaintext, nil
}

func (k BlobKeys) SealManifest(count uint32, manifestJSON []byte) ([]byte, error) {
	if len(manifestJSON) == 0 || len(manifestJSON) > MaxManifestBytes-ChunkOverhead {
		return nil, ErrBlobSize
	}
	return blobAEAD(k.manifest).Seal(nil, blobNonce(0), manifestJSON, blobAD(0, count)), nil
}

func (k BlobKeys) OpenManifest(count uint32, ciphertext []byte) ([]byte, error) {
	plaintext, err := blobAEAD(k.manifest).Open(nil, blobNonce(0), ciphertext, blobAD(0, count))
	if err != nil {
		return nil, ErrBlobAuth
	}
	return plaintext, nil
}

type Manifest struct {
	Version      int              `json:"version"`
	CreatedAt    time.Time        `json:"created_at"`
	Collector    string           `json:"collector"`
	ChunkSize    int              `json:"chunk_size"`
	ChunkCount   uint32           `json:"chunk_count"`
	TotalBytes   int64            `json:"total_bytes"`
	ChunksSHA256 []string         `json:"chunks_sha256"`
	Sources      []ManifestSource `json:"sources"`
	Redaction    RedactionSummary `json:"redaction"`
}

type ManifestSource struct {
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	Target        string `json:"target"`
	Since         string `json:"since,omitempty"`
	Until         string `json:"until,omitempty"`
	Offset        int64  `json:"offset"`
	Bytes         int64  `json:"bytes"`
	Lines         int64  `json:"lines"`
	LinesRedacted int64  `json:"lines_redacted"`
	Note          string `json:"note,omitempty"`
}

type RedactionSummary struct {
	LinesRedacted int64                    `json:"lines_redacted"`
	ByCategory    map[string]CategoryCount `json:"by_category"`
	Flags         int64                    `json:"flags"`
	Rules         int                      `json:"rules"`
}

type CategoryCount struct {
	Values int64 `json:"values"`
	Lines  int64 `json:"lines"`
}

func (m *Manifest) Validate() error {
	if m.Version != BlobVersion {
		return fmt.Errorf("protocol: manifest version %d not supported", m.Version)
	}
	if m.ChunkSize != ChunkSize {
		return errors.New("protocol: manifest chunk size not supported")
	}
	count, err := ChunkCount(m.TotalBytes)
	if err != nil || m.ChunkCount != count || int(m.ChunkCount) != len(m.ChunksSHA256) {
		return errors.New("protocol: manifest chunk count mismatch")
	}
	for _, hash := range m.ChunksSHA256 {
		if !lowerHex(hash, 64) {
			return errors.New("protocol: manifest chunk hash invalid")
		}
	}
	if len(m.Sources) == 0 {
		return errors.New("protocol: manifest has no sources")
	}
	var offset int64
	for _, source := range m.Sources {
		if source.Offset != offset || source.Bytes < 0 || source.Bytes > m.TotalBytes-offset {
			return errors.New("protocol: manifest source range invalid")
		}
		if source.Kind != "unit" && source.Kind != "container" && source.Kind != "file" {
			return errors.New("protocol: manifest source kind invalid")
		}
		offset += source.Bytes
	}
	if offset != m.TotalBytes {
		return errors.New("protocol: manifest sources do not cover plaintext")
	}
	for category := range m.Redaction.ByCategory {
		if !slices.Contains(RedactionCategories, category) {
			return errors.New("protocol: manifest redaction category invalid")
		}
	}
	return nil
}

func lowerHex(s string, length int) bool {
	if len(s) != length {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
