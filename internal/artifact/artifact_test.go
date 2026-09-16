// SPDX-License-Identifier: Apache-2.0
package artifact

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/akenhq/aken/protocol"
	"github.com/akenhq/aken/relay"
)

func manifest(parts ...string) (protocol.Manifest, []byte) {
	m := protocol.Manifest{Version: 1, ChunkSize: protocol.ChunkSize, CreatedAt: time.Now().UTC()}
	var data []byte
	for i, part := range parts {
		m.Sources = append(m.Sources, protocol.ManifestSource{Name: string(rune('a' + i)), Kind: "file", Offset: int64(len(data)), Bytes: int64(len(part)), Lines: int64(strings.Count(part, "\n"))})
		data = append(data, part...)
	}
	m.TotalBytes = int64(len(data))
	m.ChunkCount, _ = protocol.ChunkCount(m.TotalBytes)
	for range m.ChunkCount {
		m.ChunksSHA256 = append(m.ChunksSHA256, strings.Repeat("0", 64))
	}
	return m, data
}

func TestFromPlaintext(t *testing.T) {
	for _, tt := range []struct {
		name, text string
		lines      []string
	}{
		{"normal", "a\nb\n", []string{"a", "b"}},
		{"empty line", "\n", []string{""}},
		{"utf8", "a\xff\nb\n", []string{"a�", "b"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m, data := manifest(tt.text, "")
			a, err := FromPlaintext(m, data)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(a.Sources[0].Lines, tt.lines) || len(a.Sources[1].Lines) != 0 {
				t.Fatal("unexpected lines", a.Sources)
			}
			if _, err := FromPlaintext(m, data[:len(data)-1]); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("length: %v", err)
			}
			m.Sources[0].Lines++
			if _, err := FromPlaintext(m, data); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("lines: %v", err)
			}
		})
	}
	m, data := manifest("a\n")
	m.Sources[0].Offset = 100
	if _, err := FromPlaintext(m, data); err == nil {
		t.Fatal("accepted invalid offset")
	}
}

func TestQueries(t *testing.T) {
	m, data := manifest("first\nerror <ip#1>\nafter\nerror again\nlast\n", "other error\n")
	a, err := FromPlaintext(m, data)
	if err != nil {
		t.Fatal(err)
	}
	source, err := a.Source("a")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name string
		got  []Line
		ns   []int
	}{
		{"tail", source.Tail(2), []int{4, 5}},
		{"clipped", source.Read(-1, 2), []int{1, 2}},
		{"past end", source.Read(8, 20), nil},
		{"context", source.Context(2, 1), []int{1, 2, 3}},
		{"redacted", source.Read(2, 2), []int{2}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var ns []int
			for _, line := range tt.got {
				ns = append(ns, line.N)
			}
			if !reflect.DeepEqual(ns, tt.ns) {
				t.Fatalf("lines %v, want %v", ns, tt.ns)
			}
		})
	}
	q := SearchQuery{Regex: regexp.MustCompile("error"), Before: 1, After: 1, Max: 1}
	var all []Line
	for range 3 {
		got, err := a.Search(q)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, got.Lines...)
		q.StartSource, q.StartLine = got.NextSource, got.NextLine
	}
	if len(all) != 6 || !all[1].Match || !all[3].Match || all[2].Match || RedactedLines(all) != 1 {
		t.Fatalf("search = %+v", all)
	}
	for _, name := range []string{"missing", "private-token"} {
		if _, err := a.Source(name); err == nil || !strings.Contains(err.Error(), "a, b") || strings.Contains(err.Error(), name) {
			t.Fatalf("source error = %v", err)
		}
	}
	q.Source = "missing"
	if _, err := a.Search(q); err == nil {
		t.Fatal("unknown source accepted")
	}
	q.Source, q.StartSource, q.StartLine, q.Max = "b", 0, 0, 50
	got, err := a.Search(q)
	if err != nil || !got.Done || got.Matches != 1 {
		t.Fatalf("restricted search = %+v, %v", got, err)
	}
	if RedactedLines([]Line{{Text: "<ip#0>"}, {Text: "<ip#1> <secret#2>"}, {Text: "<unknown#1>"}}) != 1 {
		t.Fatal("placeholder counts")
	}
}

func TestSearchAdjacentMatches(t *testing.T) {
	for _, tt := range []struct {
		name       string
		parts      []string
		nextSource int
		nextLine   int
		done       bool
	}{
		{"within source", []string{"error one\nerror two\nerror three\n"}, 0, 3, false},
		{"next source", []string{"error one\nerror two\n", "error three\n"}, 1, 1, false},
		{"finished", []string{"error one\nerror two\n"}, 1, 1, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m, data := manifest(tt.parts...)
			a, err := FromPlaintext(m, data)
			if err != nil {
				t.Fatal(err)
			}
			q := SearchQuery{Regex: regexp.MustCompile("error"), After: 1, Max: 1}
			got, err := a.Search(q)
			if err != nil {
				t.Fatal(err)
			}
			if got.Matches != 2 || len(got.Lines) != 2 || !got.Lines[0].Match || !got.Lines[1].Match || got.NextSource != tt.nextSource || got.NextLine != tt.nextLine || got.Done != tt.done {
				t.Fatalf("search = %+v", got)
			}
			if !got.Done {
				q.StartSource, q.StartLine = got.NextSource, got.NextLine
				next, err := a.Search(q)
				if err != nil || next.Matches != 1 || len(next.Lines) != 1 || next.Lines[0].Text != "error three" || !next.Done {
					t.Fatalf("continuation = %+v, %v", next, err)
				}
			}
		})
	}
}

func TestLoad(t *testing.T) {
	for _, mode := range []string{"ok", "hash", "auth", "manifest auth", "manifest missing", "session missing", "chunk length"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(relay.NewHandler(relay.NewMemoryStore(), relay.Options{}))
			defer server.Close()
			token := protocol.NewToken()
			client, err := protocol.NewRelayClient(server.URL, token.RelayCredential())
			if err != nil {
				t.Fatal(err)
			}
			text := bytes.Repeat([]byte("line <ip#1>\n"), protocol.ChunkSize/4)
			m, data := manifest(string(text))
			if mode != "session missing" {
				if _, err := client.CreateSession(t.Context(), token.SessionID(), time.Hour, m.ChunkCount); err != nil {
					t.Fatal(err)
				}
			}
			keys := protocol.DeriveBlobKeys(token.ContentRoot())
			if mode != "session missing" && mode != "manifest missing" {
				for i, plain := range protocol.SplitChunks(data) {
					if mode == "chunk length" && i == int(m.ChunkCount)-1 {
						plain = plain[:len(plain)-1]
					}
					chunk, err := keys.SealChunk(uint64(i), m.ChunkCount, plain)
					if err != nil {
						t.Fatal(err)
					}
					if mode == "auth" {
						chunk[0] ^= 1
					}
					hash := sha256.Sum256(chunk)
					m.ChunksSHA256[i] = hex.EncodeToString(hash[:])
					if mode == "hash" {
						chunk[0] ^= 1
					}
					if err := client.PutChunk(t.Context(), token.SessionID(), uint64(i), chunk); err != nil {
						t.Fatal(err)
					}
				}
				raw, err := json.Marshal(m)
				if err != nil {
					t.Fatal(err)
				}
				encrypted, err := keys.SealManifest(m.ChunkCount, raw)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "manifest auth" {
					encrypted[0] ^= 1
				}
				if err := client.PutManifest(t.Context(), token.SessionID(), encrypted); err != nil {
					t.Fatal(err)
				}
			}
			a, err := Load(t.Context(), client, token)
			switch mode {
			case "ok":
				if err != nil {
					t.Fatal(err)
				}
				if len(a.Sources[0].Lines) != strings.Count(string(data), "\n") {
					t.Fatal("line count")
				}
			case "hash", "chunk length":
				if !errors.Is(err, ErrIntegrity) {
					t.Fatalf("integrity error = %v", err)
				}
			case "auth", "manifest auth":
				if !errors.Is(err, protocol.ErrBlobAuth) {
					t.Fatalf("auth error = %v", err)
				}
			default:
				if !errors.Is(err, ErrNotFound) {
					t.Fatalf("missing error = %v", err)
				}
			}
		})
	}
}
