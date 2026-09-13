// SPDX-License-Identifier: Apache-2.0
package protocol

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

type tokenVectors struct {
	Valid []struct {
		Name               string `json:"name"`
		SecretHex          string `json:"secret_hex"`
		Token              string `json:"token"`
		SessionID          string `json:"session_id"`
		RelayCredentialHex string `json:"relay_credential_hex"`
		ContentRootHex     string `json:"content_root_hex"`
		ExchangeKeyHex     string `json:"exchange_key_hex"`
	} `json:"valid"`
	Invalid []struct {
		Name  string `json:"name"`
		Token string `json:"token"`
	} `json:"invalid"`
}

func readVectors(t *testing.T) tokenVectors {
	t.Helper()
	data, err := os.ReadFile("../spec/vectors/token-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors tokenVectors
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	if len(vectors.Valid) != 4 || len(vectors.Invalid) != 10 {
		t.Fatal("expected four valid and ten invalid vectors")
	}
	return vectors
}

func TestVectorsValid(t *testing.T) {
	for _, vector := range readVectors(t).Valid {
		t.Run(vector.Name, func(t *testing.T) {
			token, err := ParseToken(vector.Token)
			if err != nil {
				t.Fatal(err)
			}
			credential, root, exchange := token.RelayCredential(), token.ContentRoot(), token.ExchangeKey()
			for _, value := range []struct{ name, got, want string }{
				{"token", token.Encode(), vector.Token},
				{"secret", hex.EncodeToString(token.secret[:]), vector.SecretHex},
				{"session id", token.SessionID().String(), vector.SessionID},
				{"relay credential", hex.EncodeToString(credential[:]), vector.RelayCredentialHex},
				{"content root", hex.EncodeToString(root[:]), vector.ContentRootHex},
				{"exchange key", hex.EncodeToString(exchange[:]), vector.ExchangeKeyHex},
			} {
				if value.got != value.want {
					t.Errorf("%s = %q, want %q", value.name, value.got, value.want)
				}
			}
		})
	}
}

func TestVectorsInvalid(t *testing.T) {
	for _, vector := range readVectors(t).Invalid {
		t.Run(vector.Name, func(t *testing.T) {
			_, err := ParseToken(vector.Token)
			if !errors.Is(err, ErrInvalidToken) || err != ErrInvalidToken {
				t.Fatal("expected unwrapped ErrInvalidToken")
			}
			if vector.Token != "" && strings.Contains(err.Error(), vector.Token) {
				t.Fatal("error contains the input token")
			}
		})
	}
}

func TestParseTrimsWhitespace(t *testing.T) {
	encoded := readVectors(t).Valid[0].Token
	token, err := ParseToken(" \n" + encoded + " \n")
	if err != nil {
		t.Fatal(err)
	}
	if token.Encode() != encoded {
		t.Fatal("trimmed token differs")
	}
}

func TestNewToken(t *testing.T) {
	first, second := NewToken(), NewToken()
	if first == second {
		t.Fatal("new tokens are equal")
	}
	for _, token := range []Token{first, second} {
		encoded := token.Encode()
		if len(encoded) != 57 || !strings.HasPrefix(encoded, "akn1_") {
			t.Fatal("wrong token format")
		}
		parsed, err := ParseToken(encoded)
		if err != nil {
			t.Fatal(err)
		}
		if parsed != token {
			t.Fatal("round trip changed the token")
		}
	}
}

func TestDerivedValuesDistinct(t *testing.T) {
	token := NewToken()
	id, credential, root := token.SessionID(), token.RelayCredential(), token.ContentRoot()
	values := [][]byte{id[:], credential[:], root[:]}
	for i, value := range values {
		if bytes.Equal(value, make([]byte, len(value))) {
			t.Fatal("derived value is all zeros")
		}
		for _, other := range values[:i] {
			if bytes.Equal(value, other) {
				t.Fatal("derived values are equal")
			}
		}
	}
}

func TestRedactedFormatting(t *testing.T) {
	token := NewToken()
	for _, value := range []struct{ got, want string }{
		{fmt.Sprint(token), "akn1_[redacted]"},
		{fmt.Sprintf("%v", token), "akn1_[redacted]"},
		{fmt.Sprintf("%s", token), "akn1_[redacted]"}, //nolint:staticcheck // Exercise %s formatting to catch secret exposure.
		{fmt.Sprintf("%#v", token), "protocol.Token{[redacted]}"},
		{fmt.Sprintf("%d", token), "akn1_[redacted]"},
		{fmt.Sprintf("%x", token), "akn1_[redacted]"},
		{fmt.Sprintf("%+v", token), "akn1_[redacted]"},
		{fmt.Sprint(&token), "akn1_[redacted]"},
	} {
		if strings.Contains(value.got, token.Encode()[5:]) {
			t.Fatal("formatting exposed the token body")
		}
		if value.got != value.want {
			t.Fatal("unexpected redacted format")
		}
	}
}

func TestZero(t *testing.T) {
	token := NewToken()
	token.Zero()
	if token.Encode() != readVectors(t).Valid[0].Token {
		t.Fatal("zeroed token differs from all-zero vector")
	}
}
