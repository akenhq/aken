// SPDX-License-Identifier: Apache-2.0
package protocol

import (
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	TokenVersion    = 1
	TokenSecretSize = 32
)

const tokenSalt = "aken/token/v1"

var ErrInvalidToken = errors.New("protocol: invalid token")

var tokenEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

type Token struct{ secret [TokenSecretSize]byte }

func NewToken() Token {
	var t Token
	// Since Go 1.24, crypto/rand.Read never returns an error; it crashes the
	// process if the operating system's random source fails.
	rand.Read(t.secret[:])
	return t
}

func ParseToken(s string) (Token, error) {
	// Only ASCII whitespace is trimmed, so every implementation agrees on rule 1 in spec/token.md.
	s = strings.Trim(s, " \t\r\n")
	if !strings.HasPrefix(s, "akn1_") || len(s) != 57 {
		return Token{}, ErrInvalidToken
	}
	secret, err := tokenEncoding.DecodeString(strings.ToUpper(s[5:]))
	if err != nil || len(secret) != TokenSecretSize {
		return Token{}, ErrInvalidToken
	}
	var t Token
	copy(t.secret[:], secret)
	// Re-encoding rejects uppercase and non-canonical trailing bits.
	if t.Encode() != s {
		return Token{}, ErrInvalidToken
	}
	return t, nil
}

func (t Token) Encode() string {
	return "akn1_" + strings.ToLower(tokenEncoding.EncodeToString(t.secret[:]))
}

func (t Token) String() string   { return "akn1_[redacted]" }
func (t Token) GoString() string { return "protocol.Token{[redacted]}" }

// Format covers every fmt verb, including %d and %x, which would otherwise
// print the secret bytes through reflection.
func (t Token) Format(f fmt.State, verb rune) {
	if verb == 'v' && f.Flag('#') {
		_, _ = io.WriteString(f, t.GoString())
		return
	}
	_, _ = io.WriteString(f, t.String())
}
func (t *Token) Zero() { clear(t.secret[:]) }

type SessionID [16]byte

func (id SessionID) String() string { return hex.EncodeToString(id[:]) }

func (t Token) SessionID() SessionID {
	return SessionID(t.derive("session-id", 16))
}

func (t Token) RelayCredential() [32]byte {
	return [32]byte(t.derive("relay-credential", 32))
}

func (t Token) ContentRoot() [32]byte {
	return [32]byte(t.derive("content-root", 32))
}

func (t Token) ExchangeKey() [32]byte { return [32]byte(t.derive("exchange-key", 32)) }

func (t Token) derive(info string, n int) []byte {
	key, err := hkdf.Key(sha256.New, t.secret[:], []byte(tokenSalt), info, n)
	if err != nil {
		// The fixed output lengths are within HKDF's limit.
		panic(err)
	}
	return key
}
