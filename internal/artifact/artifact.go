// SPDX-License-Identifier: Apache-2.0
package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/akenhq/aken/protocol"
)

type Artifact struct {
	Manifest protocol.Manifest
	Sources  []*Source
}
type Source struct {
	Meta  protocol.ManifestSource
	Lines []string
}
type Line struct {
	Source string
	N      int
	Text   string
	Match  bool
}

var ErrIntegrity = errors.New("artifact: integrity check failed")
var ErrNotFound = errors.New("artifact not found on the relay: it expired, was deleted, or the upload did not finish")

func Load(ctx context.Context, client *protocol.RelayClient, token protocol.Token) (*Artifact, error) {
	info, err := client.Session(ctx, token.SessionID())
	if protocol.IsNotFound(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	encrypted, err := client.GetManifest(ctx, token.SessionID())
	if protocol.IsNotFound(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	keys := protocol.DeriveBlobKeys(token.ContentRoot())
	data, err := keys.OpenManifest(info.ChunkCount, encrypted)
	if err != nil {
		return nil, err
	}
	var m protocol.Manifest
	if json.Unmarshal(data, &m) != nil {
		return nil, ErrIntegrity
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	if m.ChunkCount != info.ChunkCount {
		return nil, ErrIntegrity
	}
	plaintext := make([]byte, m.TotalBytes)
	indexes := make(chan uint32, m.ChunkCount)
	for i := range m.ChunkCount {
		indexes <- i
	}
	close(indexes)
	var wg sync.WaitGroup
	var once sync.Once
	var loadErr error
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for range 4 {
		wg.Go(func() {
			for i := range indexes {
				if ctx.Err() != nil {
					return
				}
				chunk, err := client.GetChunk(ctx, token.SessionID(), uint64(i))
				if protocol.IsNotFound(err) {
					err = ErrNotFound
				}
				if err == nil {
					hash := sha256.Sum256(chunk)
					if hex.EncodeToString(hash[:]) != m.ChunksSHA256[i] {
						err = ErrIntegrity
					}
				}
				if err == nil {
					chunk, err = keys.OpenChunk(uint64(i), m.ChunkCount, chunk)
				}
				start := int64(i) * protocol.ChunkSize
				if err == nil && int64(len(chunk)) != min(protocol.ChunkSize, int64(len(plaintext))-start) {
					err = ErrIntegrity
				}
				if err != nil {
					once.Do(func() { loadErr = err; cancel() })
					return
				}
				copy(plaintext[start:], chunk)
			}
		})
	}
	wg.Wait()
	if loadErr != nil {
		return nil, loadErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return FromPlaintext(m, plaintext)
}

func FromPlaintext(m protocol.Manifest, plaintext []byte) (*Artifact, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	if int64(len(plaintext)) != m.TotalBytes {
		return nil, ErrIntegrity
	}
	a := &Artifact{Manifest: m}
	for _, meta := range m.Sources {
		text := string(plaintext[meta.Offset : meta.Offset+meta.Bytes])
		lines := strings.Split(text, "\n")
		if lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		if int64(len(lines)) != meta.Lines {
			return nil, ErrIntegrity
		}
		for i := range lines {
			lines[i] = strings.ToValidUTF8(lines[i], "�")
		}
		a.Sources = append(a.Sources, &Source{Meta: meta, Lines: lines})
	}
	return a, nil
}

func (a *Artifact) Source(name string) (*Source, error) {
	names := make([]string, 0, len(a.Sources))
	for _, s := range a.Sources {
		if s.Meta.Name == name {
			return s, nil
		}
		names = append(names, s.Meta.Name)
	}
	return nil, fmt.Errorf("unknown source; available sources: %s", strings.Join(names, ", "))
}

type SearchQuery struct {
	Regex                  *regexp.Regexp
	Source                 string
	Before, After, Max     int
	StartSource, StartLine int
}
type SearchResult struct {
	Lines                []Line
	Matches              int
	NextSource, NextLine int
	Done                 bool
}

func (a *Artifact) Search(q SearchQuery) (SearchResult, error) {
	var result SearchResult
	if q.Source != "" {
		if _, err := a.Source(q.Source); err != nil {
			return result, err
		}
	}
	if q.Regex == nil || q.Before < 0 || q.After < 0 || q.Max < 1 || q.StartSource < 0 || q.StartSource > len(a.Sources) || q.StartLine < 0 {
		return result, errors.New("invalid search query")
	}
	for si := q.StartSource; si < len(a.Sources); si++ {
		s := a.Sources[si]
		if q.Source != "" && q.Source != s.Meta.Name {
			continue
		}
		start := 1
		if si == q.StartSource {
			start = max(1, q.StartLine)
		}
		emitted := start - 1
		for n := start; n <= len(s.Lines); n++ {
			if !q.Regex.MatchString(s.Lines[n-1]) {
				continue
			}
			for _, line := range s.Read(max(emitted+1, n-q.Before), min(len(s.Lines), n+q.After)) {
				line.Match = q.Regex.MatchString(line.Text)
				if line.Match {
					result.Matches++
				}
				result.Lines = append(result.Lines, line)
				emitted = line.N
			}
			n = emitted
			result.NextSource, result.NextLine = si, emitted+1
			if result.Matches >= q.Max {
				if emitted == len(s.Lines) {
					result.NextSource, result.NextLine = si+1, 1
					result.Done = q.Source != "" || si+1 == len(a.Sources)
				}
				return result, nil
			}
		}
	}
	result.Done = true
	result.NextSource, result.NextLine = len(a.Sources), 1
	return result, nil
}

func (s *Source) Tail(n int) []Line { return s.Read(max(1, len(s.Lines)-n+1), len(s.Lines)) }
func (s *Source) Read(from, to int) []Line {
	var lines []Line
	for n := max(1, from); n <= min(to, len(s.Lines)); n++ {
		lines = append(lines, Line{Source: s.Meta.Name, N: n, Text: s.Lines[n-1]})
	}
	return lines
}
func (s *Source) Context(line, around int) []Line {
	return s.Read(max(1, line-around), line+min(around, len(s.Lines)-line))
}

var placeholder = regexp.MustCompile(protocol.PlaceholderPattern)

func RedactedLines(lines []Line) int {
	count := 0
	for _, line := range lines {
		if placeholder.MatchString(line.Text) {
			count++
		}
	}
	return count
}
