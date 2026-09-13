// SPDX-License-Identifier: Apache-2.0
package protocol

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestSessionVectors(t *testing.T) {
	data, err := os.ReadFile("../spec/vectors/session-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Version int               `json:"version"`
		Labels  map[string]string `json:"labels"`
		Vectors []struct {
			Name             string `json:"name"`
			Token            string `json:"token"`
			SessionID        string `json:"session_id"`
			ExchangeKey      string `json:"exchange_key_hex"`
			CollectorPrivate string `json:"collector_private_hex"`
			CollectorPublic  string `json:"collector_public_hex"`
			CollectorMAC     string `json:"collector_mac_hex"`
			MCPPrivate       string `json:"mcp_private_hex"`
			MCPPublic        string `json:"mcp_public_hex"`
			MCPMAC           string `json:"mcp_mac_hex"`
			SharedSecret     string `json:"shared_secret_hex"`
			TranscriptHash   string `json:"transcript_hash_hex"`
			ContentRoot      string `json:"content_root_hex"`
			JobKey           string `json:"job_key_hex"`
			ResultKey        string `json:"result_key_hex"`
			Job, Result      struct {
				Seq        uint64 `json:"seq"`
				Class      uint8  `json:"class"`
				Plaintext  string `json:"plaintext"`
				Canonical  string `json:"canonical_hex"`
				Ciphertext string `json:"ciphertext_hex"`
			}
		} `json:"vectors"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	if file.Version != 1 || len(file.Vectors) != 2 || file.Labels["transcript"] != sessionLabel || file.Labels["content_root_info"] != "content-root" || file.Labels["job_key_info"] != "job-key" || file.Labels["result_key_info"] != "result-key" {
		t.Fatal("vector metadata")
	}
	for _, v := range file.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			token, err := ParseToken(v.Token)
			if err != nil {
				t.Fatal(err)
			}
			id, exchange := token.SessionID(), token.ExchangeKey()
			if id.String() != v.SessionID {
				t.Fatal("session id")
			}
			collector, err := KeyPairFromPrivate([32]byte(decodeBlobHex(t, v.CollectorPrivate)))
			if err != nil {
				t.Fatal(err)
			}
			mcp, err := KeyPairFromPrivate([32]byte(decodeBlobHex(t, v.MCPPrivate)))
			if err != nil {
				t.Fatal(err)
			}
			ck, mk := collector.Public(), mcp.Public()
			cm, mm := CollectorMAC(exchange, id, ck), MCPMAC(exchange, id, ck, mk)
			if !VerifyCollectorMAC(exchange, id, ck, cm) || !VerifyMCPMAC(exchange, id, ck, mk, mm) {
				t.Fatal("valid MAC rejected")
			}
			root, err := ContentRoot(collector, mk, id, ck, mk)
			if err != nil {
				t.Fatal(err)
			}
			peerRoot, err := ContentRoot(mcp, ck, id, ck, mk)
			if err != nil || root != peerRoot {
				t.Fatal("exchange roots differ", err)
			}
			secret, err := collector.private.ECDH(mcp.private.PublicKey())
			if err != nil {
				t.Fatal(err)
			}
			transcript := append([]byte(sessionLabel), id[:]...)
			transcript = append(transcript, ck[:]...)
			transcript = append(transcript, mk[:]...)
			hash := sha256.Sum256(transcript)
			keys := DeriveSessionKeys(root)
			for _, item := range []struct {
				name string
				got  []byte
				want string
			}{
				{"exchange", exchange[:], v.ExchangeKey}, {"collector public", ck[:], v.CollectorPublic}, {"mcp public", mk[:], v.MCPPublic},
				{"collector MAC", cm[:], v.CollectorMAC}, {"mcp MAC", mm[:], v.MCPMAC}, {"shared secret", secret, v.SharedSecret},
				{"transcript", hash[:], v.TranscriptHash}, {"root", root[:], v.ContentRoot}, {"job key", keys.job[:], v.JobKey}, {"result key", keys.result[:], v.ResultKey},
			} {
				if hex.EncodeToString(item.got) != item.want {
					t.Errorf("%s differs", item.name)
				}
			}
			for _, result := range []bool{false, true} {
				wire, seal, open := v.Job, keys.SealJob, keys.OpenJob
				if result {
					wire, seal, open = v.Result, keys.SealResult, keys.OpenResult
				}
				e, err := seal(id, wire.Seq, wire.Class, []byte(wire.Plaintext))
				if err != nil {
					t.Fatal(err)
				}
				ad, err := e.Canonical()
				if err != nil || len(ad) != 30 || hex.EncodeToString(ad) != wire.Canonical || hex.EncodeToString(e.Payload) != wire.Ciphertext {
					t.Fatal("envelope differs", err)
				}
				e.Payload = decodeBlobHex(t, wire.Ciphertext)
				plain, err := open(e, id, wire.Seq)
				if err != nil || string(plain) != wire.Plaintext {
					t.Fatal("open differs", err)
				}
			}
		})
	}
}

func TestSessionAuthentication(t *testing.T) {
	collector, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	mcp, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	ck, mk := collector.Public(), mcp.Public()
	if ck == mk {
		t.Fatal("duplicate random keys")
	}
	token := NewToken()
	id, exchange := token.SessionID(), token.ExchangeKey()
	cm, mm := CollectorMAC(exchange, id, ck), MCPMAC(exchange, id, ck, mk)
	for _, change := range []string{"exchange", "id", "collector", "mcp", "mac"} {
		t.Run(change, func(t *testing.T) {
			ex, sid, c, m, cMAC, mMAC := exchange, id, ck, mk, cm, mm
			switch change {
			case "exchange":
				ex[0] ^= 1
			case "id":
				sid[0] ^= 1
			case "collector":
				c[0] ^= 1
			case "mcp":
				m[0] ^= 1
			case "mac":
				cMAC[0] ^= 1
				mMAC[0] ^= 1
			}
			if change != "mcp" && VerifyCollectorMAC(ex, sid, c, cMAC) {
				t.Fatal("bad collector MAC accepted")
			}
			if VerifyMCPMAC(ex, sid, c, m, mMAC) {
				t.Fatal("bad MCP MAC accepted")
			}
		})
	}
	if VerifyMCPMAC(exchange, id, ck, mk, cm) || VerifyCollectorMAC(exchange, id, ck, mm) {
		t.Fatal("MAC roles interchangeable")
	}
	for _, peer := range [][32]byte{{}, {1}} {
		if _, err := ContentRoot(collector, peer, id, ck, mk); err != ErrExchange {
			t.Fatal("low-order key", err)
		}
	}
	if _, err := ContentRoot(KeyPair{}, mk, id, ck, mk); err != ErrExchange {
		t.Fatal("zero keypair", err)
	}
	first, err := ContentRoot(collector, mk, id, ck, mk)
	if err != nil {
		t.Fatal(err)
	}
	swapped, err := ContentRoot(collector, mk, id, mk, ck)
	if err != nil || first == swapped {
		t.Fatal("transcript order not bound", err)
	}
}

func TestSessionEnvelopeRejections(t *testing.T) {
	keys := DeriveSessionKeys([32]byte{1})
	id := SessionID{1}
	for _, result := range []bool{false, true} {
		seal, open, capBytes := keys.SealJob, keys.OpenJob, MaxJobBytes
		if result {
			seal, open, capBytes = keys.SealResult, keys.OpenResult, MaxResultBytes
		}
		for _, tt := range []struct {
			seq   uint64
			class uint8
			size  int
		}{{0, 1, 1}, {MaxSessionSeq + 1, 1, 1}, {1, 0, 1}, {1, 4, 1}, {1, 1, 0}, {1, 1, capBytes - 15}} {
			if _, err := seal(id, tt.seq, tt.class, make([]byte, tt.size)); err != ErrEnvelope {
				t.Fatal("seal bounds", err)
			}
		}
		for _, tt := range []struct {
			seq   uint64
			class uint8
			size  int
		}{{1, 1, 1}, {MaxSessionSeq, 3, capBytes - 16}} {
			e, err := seal(id, tt.seq, tt.class, make([]byte, tt.size))
			if err != nil {
				t.Fatal(err)
			}
			plain, err := open(e, id, tt.seq)
			if err != nil || len(plain) != tt.size {
				t.Fatal("boundary round trip", err)
			}
		}
		original, err := seal(id, 1, 1, []byte("private plaintext"))
		if err != nil {
			t.Fatal(err)
		}
		for _, tt := range []struct {
			name   string
			change func(*Envelope)
			expect uint64
			want   error
		}{
			{"version", func(e *Envelope) { e.Version = 2 }, 2, ErrEnvelope},
			{"id syntax", func(e *Envelope) { e.SessionID = strings.Repeat("A", 32) }, 1, ErrEnvelope},
			{"wrong session", func(e *Envelope) { e.SessionID = SessionID{2}.String() }, 1, ErrEnvelope},
			{"seq zero", func(e *Envelope) { e.Seq = 0 }, 0, ErrEnvelope},
			{"seq cap", func(e *Envelope) { e.Seq = MaxSessionSeq + 1 }, MaxSessionSeq + 1, ErrEnvelope},
			{"class zero", func(e *Envelope) { e.Class = 0 }, 1, ErrEnvelope},
			{"class high", func(e *Envelope) { e.Class = 4 }, 1, ErrEnvelope},
			{"short", func(e *Envelope) { e.Payload = e.Payload[:16] }, 1, ErrEnvelope},
			{"large", func(e *Envelope) { e.Payload = make([]byte, capBytes+1) }, 1, ErrEnvelope},
			{"replay", func(*Envelope) {}, 2, ErrSequence},
			{"gap before auth", func(e *Envelope) { e.Seq = 3; e.Payload[0] ^= 1 }, 2, ErrSequence},
			{"seq bound", func(e *Envelope) { e.Seq = 2 }, 2, ErrSessionAuth},
			{"class bound", func(e *Envelope) { e.Class = 2 }, 1, ErrSessionAuth},
			{"payload", func(e *Envelope) { e.Payload[0] ^= 1 }, 1, ErrSessionAuth},
			{"tag", func(e *Envelope) { e.Payload[len(e.Payload)-1] ^= 1 }, 1, ErrSessionAuth},
			{"length bound", func(e *Envelope) { e.Payload = append(e.Payload, 0) }, 1, ErrSessionAuth},
		} {
			t.Run(tt.name, func(t *testing.T) {
				e := original
				e.Payload = bytes.Clone(e.Payload)
				tt.change(&e)
				plain, err := open(e, id, tt.expect)
				if err != tt.want || plain != nil {
					t.Fatalf("got %v, want %v", err, tt.want)
				}
			})
		}
		other := keys.OpenResult
		if result {
			other = keys.OpenJob
		}
		if _, err := other(original, id, 1); err != ErrSessionAuth {
			t.Fatal("direction key", err)
		}
	}
}
