// SPDX-License-Identifier: Apache-2.0
package protocol

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
)

const (
	SessionVersion       = 1
	ClassRead      uint8 = 1
	ClassWrite     uint8 = 2
	ClassExec      uint8 = 3
	MaxJobBytes          = 64 << 10
	MaxResultBytes       = 1 << 20
	MaxSessionSeq        = 1<<32 - 1
)

const sessionLabel = "aken/session/v1"

var (
	ErrEnvelope    = errors.New("protocol: invalid envelope")
	ErrSequence    = errors.New("protocol: unexpected sequence number")
	ErrSessionAuth = errors.New("protocol: session authentication failed")
	ErrExchange    = errors.New("protocol: key exchange failed")
)

type KeyPair struct{ private *ecdh.PrivateKey }

func GenerateKeyPair() (KeyPair, error) {
	private, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return KeyPair{}, ErrExchange
	}
	return KeyPair{private: private}, nil
}

func KeyPairFromPrivate(private [32]byte) (KeyPair, error) {
	key, err := ecdh.X25519().NewPrivateKey(private[:])
	if err != nil {
		return KeyPair{}, ErrExchange
	}
	return KeyPair{private: key}, nil
}

func (k KeyPair) Public() [32]byte {
	if k.private == nil {
		return [32]byte{}
	}
	return [32]byte(k.private.PublicKey().Bytes())
}

func CollectorMAC(exchangeKey [32]byte, id SessionID, collectorKey [32]byte) [32]byte {
	mac := hmac.New(sha256.New, exchangeKey[:])
	mac.Write([]byte(sessionLabel))
	mac.Write([]byte{1})
	mac.Write(id[:])
	mac.Write(collectorKey[:])
	return [32]byte(mac.Sum(nil))
}

func MCPMAC(exchangeKey [32]byte, id SessionID, collectorKey, mcpKey [32]byte) [32]byte {
	mac := hmac.New(sha256.New, exchangeKey[:])
	mac.Write([]byte(sessionLabel))
	mac.Write([]byte{2})
	mac.Write(id[:])
	mac.Write(collectorKey[:])
	mac.Write(mcpKey[:])
	return [32]byte(mac.Sum(nil))
}

func VerifyCollectorMAC(exchangeKey [32]byte, id SessionID, collectorKey, mac [32]byte) bool {
	expected := CollectorMAC(exchangeKey, id, collectorKey)
	return hmac.Equal(expected[:], mac[:])
}

func VerifyMCPMAC(exchangeKey [32]byte, id SessionID, collectorKey, mcpKey, mac [32]byte) bool {
	expected := MCPMAC(exchangeKey, id, collectorKey, mcpKey)
	return hmac.Equal(expected[:], mac[:])
}

func ContentRoot(k KeyPair, peer [32]byte, id SessionID, collectorKey, mcpKey [32]byte) ([32]byte, error) {
	if k.private == nil {
		return [32]byte{}, ErrExchange
	}
	public, err := ecdh.X25519().NewPublicKey(peer[:])
	if err != nil {
		return [32]byte{}, ErrExchange
	}
	secret, err := k.private.ECDH(public)
	if err != nil {
		return [32]byte{}, ErrExchange
	}
	transcript := sha256.New()
	transcript.Write([]byte(sessionLabel))
	transcript.Write(id[:])
	transcript.Write(collectorKey[:])
	transcript.Write(mcpKey[:])
	root, err := hkdf.Key(sha256.New, secret, transcript.Sum(nil), "content-root", 32)
	if err != nil {
		return [32]byte{}, ErrExchange
	}
	return [32]byte(root), nil
}

type SessionKeys struct{ job, result [32]byte }

func DeriveSessionKeys(contentRoot [32]byte) SessionKeys {
	derive := func(info string) [32]byte {
		key, err := hkdf.Key(sha256.New, contentRoot[:], []byte(sessionLabel), info, 32)
		if err != nil {
			// The fixed output length is within HKDF's limit.
			panic(err)
		}
		return [32]byte(key)
	}
	return SessionKeys{job: derive("job-key"), result: derive("result-key")}
}

type Envelope struct {
	Version   uint8  `json:"version"`
	SessionID string `json:"session_id"`
	Seq       uint64 `json:"seq"`
	Class     uint8  `json:"class"`
	Payload   []byte `json:"payload"`
}

func (e Envelope) Canonical() ([]byte, error) {
	id, ok := ParseSessionID(e.SessionID)
	payloadLen := uint64(len(e.Payload))
	if !ok || e.Version != SessionVersion || e.Seq == 0 || e.Seq > MaxSessionSeq || e.Class < ClassRead || e.Class > ClassExec || len(e.Payload) <= 16 || payloadLen > 1<<32-1 {
		return nil, ErrEnvelope
	}
	ad := make([]byte, 30)
	ad[0] = e.Version
	copy(ad[1:17], id[:])
	binary.BigEndian.PutUint64(ad[17:25], e.Seq)
	ad[25] = e.Class
	binary.BigEndian.PutUint32(ad[26:], uint32(payloadLen))
	return ad, nil
}

func (e Envelope) Validate(id SessionID, maxPayload int) error {
	if _, err := e.Canonical(); err != nil || e.SessionID != id.String() || len(e.Payload) > maxPayload {
		return ErrEnvelope
	}
	return nil
}

func sessionAEAD(key [32]byte) cipher.AEAD {
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

func sessionNonce(seq uint64) []byte {
	nonce := make([]byte, 12)
	binary.BigEndian.PutUint64(nonce[4:], seq)
	return nonce
}

func sealSession(key [32]byte, id SessionID, seq uint64, class uint8, plaintext []byte, capBytes int) (Envelope, error) {
	if len(plaintext) == 0 || len(plaintext) > capBytes-16 {
		return Envelope{}, ErrEnvelope
	}
	e := Envelope{Version: SessionVersion, SessionID: id.String(), Seq: seq, Class: class, Payload: make([]byte, len(plaintext)+16)}
	ad, err := e.Canonical()
	if err != nil {
		return Envelope{}, ErrEnvelope
	}
	e.Payload = sessionAEAD(key).Seal(e.Payload[:0], sessionNonce(seq), plaintext, ad)
	return e, nil
}

func openSession(key [32]byte, e Envelope, id SessionID, expectSeq uint64, capBytes int) ([]byte, error) {
	if err := e.Validate(id, capBytes); err != nil {
		return nil, ErrEnvelope
	}
	if e.Seq != expectSeq {
		return nil, ErrSequence
	}
	ad, _ := e.Canonical()
	plaintext, err := sessionAEAD(key).Open(nil, sessionNonce(e.Seq), e.Payload, ad)
	if err != nil {
		return nil, ErrSessionAuth
	}
	return plaintext, nil
}

func (k SessionKeys) SealJob(id SessionID, seq uint64, class uint8, plaintext []byte) (Envelope, error) {
	return sealSession(k.job, id, seq, class, plaintext, MaxJobBytes)
}
func (k SessionKeys) OpenJob(e Envelope, id SessionID, expectSeq uint64) ([]byte, error) {
	return openSession(k.job, e, id, expectSeq, MaxJobBytes)
}
func (k SessionKeys) SealResult(id SessionID, seq uint64, class uint8, plaintext []byte) (Envelope, error) {
	return sealSession(k.result, id, seq, class, plaintext, MaxResultBytes)
}
func (k SessionKeys) OpenResult(e Envelope, id SessionID, expectSeq uint64) ([]byte, error) {
	return openSession(k.result, e, id, expectSeq, MaxResultBytes)
}
