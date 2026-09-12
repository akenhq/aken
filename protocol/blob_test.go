// SPDX-License-Identifier: Apache-2.0
package protocol

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

type blobVectorFile struct {
	Salt   string `json:"salt"`
	Labels struct {
		ManifestKey string `json:"manifest_key"`
		ChunkKey    string `json:"chunk_key"`
	} `json:"labels"`
	ChunkSize int          `json:"chunk_size"`
	Vectors   []blobVector `json:"vectors"`
}

type blobVector struct {
	Name           string `json:"name"`
	ContentRootHex string `json:"content_root_hex"`
	ManifestKeyHex string `json:"manifest_key_hex"`
	ChunkKeyHex    string `json:"chunk_key_hex"`
	Plaintext      struct {
		Generator string `json:"generator"`
		Length    int    `json:"length"`
	} `json:"plaintext"`
	ChunkCount uint32 `json:"chunk_count"`
	Chunks     []struct {
		Index               uint64 `json:"index"`
		CiphertextHex       string `json:"ciphertext_hex,omitempty"`
		CiphertextSHA256    string `json:"ciphertext_sha256,omitempty"`
		CiphertextPrefixHex string `json:"ciphertext_prefix_hex,omitempty"`
	} `json:"chunks"`
	ManifestJSON          string `json:"manifest_json"`
	ManifestCiphertextHex string `json:"manifest_ciphertext_hex"`
}

func TestBlobVectors(t *testing.T) {
	data, err := os.ReadFile("../spec/vectors/blob-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var file blobVectorFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	if file.Salt != "aken/blob/v1" || file.Labels.ManifestKey != "manifest-key" || file.Labels.ChunkKey != "chunk-key" || file.ChunkSize != ChunkSize || len(file.Vectors) != 3 {
		t.Fatal("incorrect vector metadata")
	}
	for n, v := range file.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			root := decodeBlobHex(t, v.ContentRootHex)
			var wantRoot [32]byte
			if n == 1 {
				wantRoot = sha256.Sum256([]byte("aken blob vector 2"))
			}
			if n == 2 {
				for i := range wantRoot {
					wantRoot[i] = byte(i)
				}
			}
			if !bytes.Equal(root, wantRoot[:]) || v.Plaintext.Length != []int{1, ChunkSize, ChunkSize + 1000}[n] || v.Plaintext.Generator != "byte i = (7*i + 3) mod 251" {
				t.Fatal("incorrect vector input")
			}
			keys := DeriveBlobKeys([32]byte(root))
			if hex.EncodeToString(keys.manifest[:]) != v.ManifestKeyHex || hex.EncodeToString(keys.chunk[:]) != v.ChunkKeyHex {
				t.Fatal("derived keys differ")
			}
			plaintext := make([]byte, v.Plaintext.Length)
			for i := range plaintext {
				plaintext[i] = byte((7*i + 3) % 251)
			}
			chunks := SplitChunks(plaintext)
			if len(chunks) != len(v.Chunks) || uint32(len(chunks)) != v.ChunkCount {
				t.Fatal("chunk count differs")
			}
			var manifest Manifest
			if err := json.Unmarshal([]byte(v.ManifestJSON), &manifest); err != nil {
				t.Fatal(err)
			}
			if err := manifest.Validate(); err != nil {
				t.Fatal(err)
			}
			if manifest.CreatedAt.Format(time.RFC3339) != "2026-09-12T00:00:00Z" || manifest.Collector != "aken v0.1.0-test" || manifest.TotalBytes != int64(len(plaintext)) || manifest.ChunkCount != v.ChunkCount || len(manifest.Sources) != 1 || manifest.Sources[0].Kind != "file" || manifest.Redaction.Rules != 14 || len(manifest.Redaction.ByCategory) != 0 || manifest.Redaction.LinesRedacted != 0 || manifest.Redaction.Flags != 0 {
				t.Fatal("incorrect vector manifest")
			}
			for i, chunk := range chunks {
				vector := v.Chunks[i]
				if vector.Index != uint64(i) {
					t.Fatal("chunk index differs")
				}
				ciphertext, err := keys.SealChunk(uint64(i), v.ChunkCount, chunk)
				if err != nil {
					t.Fatal(err)
				}
				hash := sha256.Sum256(ciphertext)
				if hex.EncodeToString(hash[:]) != manifest.ChunksSHA256[i] {
					t.Fatal("manifest hash differs")
				}
				if len(chunk) == ChunkSize {
					if hex.EncodeToString(hash[:]) != vector.CiphertextSHA256 || hex.EncodeToString(ciphertext[:32]) != vector.CiphertextPrefixHex || vector.CiphertextHex != "" {
						t.Fatal("chunk digest or prefix differs")
					}
				} else {
					if !bytes.Equal(ciphertext, decodeBlobHex(t, vector.CiphertextHex)) || vector.CiphertextSHA256 != "" || vector.CiphertextPrefixHex != "" {
						t.Fatal("chunk ciphertext differs")
					}
				}
				opened, err := keys.OpenChunk(uint64(i), v.ChunkCount, ciphertext)
				if err != nil || !bytes.Equal(opened, chunk) {
					t.Fatal("chunk open differs", err)
				}
			}
			ciphertext, err := keys.SealManifest(v.ChunkCount, []byte(v.ManifestJSON))
			if err != nil || !bytes.Equal(ciphertext, decodeBlobHex(t, v.ManifestCiphertextHex)) {
				t.Fatal("manifest ciphertext differs", err)
			}
			opened, err := keys.OpenManifest(v.ChunkCount, decodeBlobHex(t, v.ManifestCiphertextHex))
			if err != nil || string(opened) != v.ManifestJSON {
				t.Fatal("manifest open differs", err)
			}
		})
	}
}

func decodeBlobHex(t *testing.T, value string) []byte {
	t.Helper()
	data, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestBlobAuthentication(t *testing.T) {
	keys := DeriveBlobKeys([32]byte{})
	ciphertext, err := keys.SealChunk(0, 2, make([]byte, ChunkSize))
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name  string
		index uint64
		count uint32
		flip  bool
	}{
		{"tamper", 0, 2, true}, {"index", 1, 2, false}, {"count", 0, 3, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			input := bytes.Clone(ciphertext)
			if tt.flip {
				input[0] ^= 1
			}
			if _, err := keys.OpenChunk(tt.index, tt.count, input); err != ErrBlobAuth {
				t.Fatalf("error = %v", err)
			}
		})
	}
	manifest, err := keys.SealManifest(2, []byte(`{"version":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := keys.OpenManifest(3, manifest); err != ErrBlobAuth {
		t.Fatalf("count error = %v", err)
	}
	manifest[0] ^= 1
	if _, err := keys.OpenManifest(2, manifest); err != ErrBlobAuth {
		t.Fatalf("tamper error = %v", err)
	}
	if _, err := keys.OpenManifest(2, nil); err != ErrBlobAuth {
		t.Fatalf("empty error = %v", err)
	}
	other := DeriveBlobKeys([32]byte{1})
	if _, err := other.OpenChunk(0, 2, ciphertext); err != ErrBlobAuth {
		t.Fatalf("key error = %v", err)
	}
}

func TestBlobSizes(t *testing.T) {
	keys := DeriveBlobKeys([32]byte{})
	for _, tt := range []struct {
		name   string
		index  uint64
		count  uint32
		length int
		want   error
	}{
		{"zero count", 0, 0, 1, ErrBlobSize}, {"over count", 0, MaxChunkCount + 1, 1, ErrBlobSize},
		{"index at count", 1, 1, 1, ErrBlobSize}, {"index overflow", math.MaxUint64, 1, 1, ErrBlobSize},
		{"empty", 0, 1, 0, ErrBlobSize}, {"over size", 0, 1, ChunkSize + 1, ErrBlobSize},
		{"short non-last", 0, 2, 1, ErrBlobSize}, {"full non-last", 0, 2, ChunkSize, nil},
		{"short last", 1, 2, 1, nil}, {"full last", 0, 1, ChunkSize, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := keys.SealChunk(tt.index, tt.count, make([]byte, tt.length)); err != tt.want {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}
	for _, tt := range []struct {
		index  uint64
		count  uint32
		length int
	}{
		{0, 0, 17}, {0, MaxChunkCount + 1, 17}, {1, 1, 17}, {0, 1, 16}, {0, 1, ChunkSize + ChunkOverhead + 1},
	} {
		if _, err := keys.OpenChunk(tt.index, tt.count, make([]byte, tt.length)); err != ErrBlobSize {
			t.Fatalf("open size error = %v", err)
		}
	}
	for _, length := range []int{0, 1, MaxManifestBytes - ChunkOverhead, MaxManifestBytes - ChunkOverhead + 1} {
		_, err := keys.SealManifest(1, make([]byte, length))
		if length == 0 || length > MaxManifestBytes-ChunkOverhead {
			if err != ErrBlobSize {
				t.Fatalf("manifest size error = %v", err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
}

func TestChunkLayout(t *testing.T) {
	for _, tt := range []struct {
		length int64
		count  uint32
		want   error
	}{
		{-1, 0, ErrBlobSize}, {0, 0, ErrBlobSize}, {1, 1, nil}, {ChunkSize, 1, nil}, {ChunkSize + 1, 2, nil},
		{MaxChunkCount * ChunkSize, MaxChunkCount, nil}, {MaxChunkCount*ChunkSize + 1, 0, ErrBlobSize}, {math.MaxInt64, 0, ErrBlobSize},
	} {
		count, err := ChunkCount(tt.length)
		if count != tt.count || err != tt.want {
			t.Fatalf("ChunkCount(%d) = %d, %v", tt.length, count, err)
		}
	}
	for _, length := range []int{0, 1, ChunkSize, ChunkSize + 1} {
		input := make([]byte, length)
		chunks := SplitChunks(input)
		if length == 0 && chunks != nil {
			t.Fatal("empty split is not nil")
		}
		if len(chunks) != (length+ChunkSize-1)/ChunkSize || !bytes.Equal(bytes.Join(chunks, nil), input) {
			t.Fatal("split differs")
		}
		for i, chunk := range chunks {
			if &chunk[0] != &input[i*ChunkSize] {
				t.Fatal("split copied input")
			}
		}
	}
}

func validManifest() Manifest {
	return Manifest{Version: 1, ChunkSize: ChunkSize, ChunkCount: 1, TotalBytes: 1, ChunksSHA256: []string{strings.Repeat("a", 64)}, Sources: []ManifestSource{{Kind: "file", Bytes: 1}}, Redaction: RedactionSummary{ByCategory: map[string]CategoryCount{"ip": {Values: 1, Lines: 1}}}}
}

func TestManifestValidate(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*Manifest)
	}{
		{"version", func(m *Manifest) { m.Version = 2 }},
		{"chunk size", func(m *Manifest) { m.ChunkSize-- }},
		{"hash count", func(m *Manifest) { m.ChunksSHA256 = nil }},
		{"chunk count", func(m *Manifest) { m.ChunkCount++ }},
		{"total bytes", func(m *Manifest) { m.TotalBytes = 0 }},
		{"negative bytes", func(m *Manifest) { m.TotalBytes = -1 }},
		{"over cap", func(m *Manifest) { m.TotalBytes = MaxChunkCount*ChunkSize + 1 }},
		{"short hash", func(m *Manifest) { m.ChunksSHA256[0] = "a" }},
		{"uppercase hash", func(m *Manifest) { m.ChunksSHA256[0] = strings.Repeat("A", 64) }},
		{"invalid hex", func(m *Manifest) { m.ChunksSHA256[0] = strings.Repeat("g", 64) }},
		{"no sources", func(m *Manifest) { m.Sources = nil }},
		{"offset", func(m *Manifest) { m.Sources[0].Offset = 1 }},
		{"gap", func(m *Manifest) { m.Sources = append(m.Sources, ManifestSource{Kind: "file", Offset: 2}) }},
		{"negative source bytes", func(m *Manifest) { m.Sources[0].Bytes = -1 }},
		{"source overflow", func(m *Manifest) { m.Sources[0].Bytes = math.MaxInt64 }},
		{"incomplete coverage", func(m *Manifest) { m.Sources[0].Bytes = 0 }},
		{"kind", func(m *Manifest) { m.Sources[0].Kind = "private-input" }},
		{"category", func(m *Manifest) { m.Redaction.ByCategory["private-input"] = CategoryCount{} }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := validManifest()
			tt.change(&m)
			err := m.Validate()
			if err == nil || strings.Contains(err.Error(), "private-input") {
				t.Fatalf("unsafe or missing validation error: %v", err)
			}
		})
	}
	for _, kind := range []string{"unit", "container", "file"} {
		m := validManifest()
		m.Sources[0].Kind = kind
		if err := m.Validate(); err != nil {
			t.Fatal(err)
		}
	}
}
