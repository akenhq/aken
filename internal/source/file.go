// SPDX-License-Identifier: Apache-2.0
package source

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const maxFileBytes = 128 << 20

type Files struct {
	Allowed       []string
	Tail          int
	ModifiedAfter time.Time
}

func (f Files) Open(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("file path must be absolute and clean")
	}
	for _, dir := range f.Allowed {
		rel, err := filepath.Rel(dir, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			root, err := os.OpenRoot(dir)
			if err != nil {
				return nil, err
			}
			defer func() { _ = root.Close() }()
			// Nonblocking open lets the regular-file check reject FIFOs without waiting for a writer.
			return root.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		}
	}
	return nil, errors.New("path is outside the allowed directories (/var/log; add --allow DIR)")
}
func (f Files) ReadFile(path string) (*Source, error) {
	return f.readFile(path, nil)
}
func (f Files) readFile(path string, seen map[string]bool) (*Source, error) {
	file, err := f.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("source is not a regular file")
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	if seen[canonical] {
		return nil, nil
	}
	var data []byte
	if f.Tail > 0 {
		data, err = readTail(file, info.Size(), f.Tail)
	} else {
		data, err = io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	}
	if err != nil {
		return nil, err
	}
	if len(data) > maxFileBytes {
		return nil, errors.New("file exceeds the 128 MiB artifact cap; narrow --since or use --tail")
	}
	s := &Source{Spec: Spec{Kind: KindFile, Target: path}, Name: "file:" + path, Lines: splitLines(data)}
	if f.Tail > 0 {
		if len(s.Lines) > f.Tail {
			s.Lines = s.Lines[len(s.Lines)-f.Tail:]
		}
		s.Note = fmt.Sprintf("last %d lines", f.Tail)
	}
	if seen != nil {
		seen[canonical] = true
	}
	return s, nil
}
func readTail(file *os.File, size int64, n int) ([]byte, error) {
	var blocks [][]byte
	var total, newlines int
	for pos := size; pos > 0 && newlines <= n; {
		length := min(pos, 64<<10)
		pos -= length
		block := make([]byte, int(length))
		if _, err := file.ReadAt(block, pos); err != nil {
			return nil, err
		}
		blocks = append(blocks, block)
		total += len(block)
		newlines += bytes.Count(block, []byte{'\n'})
		if total > maxFileBytes {
			return nil, errors.New("file tail exceeds the 128 MiB artifact cap; use --tail with fewer lines")
		}
	}
	data := make([]byte, 0, total)
	for i := len(blocks) - 1; i >= 0; i-- {
		data = append(data, blocks[i]...)
	}
	return data, nil
}
func (f Files) Glob(pattern string) ([]*Source, error) {
	return f.glob(pattern, map[string]bool{})
}
func (f Files) glob(pattern string, seen map[string]bool) ([]*Source, error) {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	var sources []*Source
	for _, path := range matches {
		file, err := f.Open(path)
		if err != nil {
			return nil, err
		}
		info, err := file.Stat()
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.ModTime().Before(f.ModifiedAfter) {
			continue
		}
		s, err := f.readFile(path, seen)
		if err != nil {
			return nil, err
		}
		if s != nil {
			sources = append(sources, s)
		}
	}
	return sources, nil
}

type Entry struct {
	Name    string
	Mode    os.FileMode
	Size    int64
	ModTime time.Time
}

func (f Files) List(path string) ([]Entry, error) {
	file, err := f.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	entries, err := file.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		name := entry.Name()
		if info.IsDir() {
			name += "/"
		}
		out = append(out, Entry{name, info.Mode(), info.Size(), info.ModTime()})
	}
	slices.SortFunc(out, func(a, b Entry) int {
		return strings.Compare(strings.TrimSuffix(a.Name, "/"), strings.TrimSuffix(b.Name, "/"))
	})
	return out, nil
}

func (f Files) ReadLines(path string, from, to int) (lines [][]byte, total int, err error) {
	if from < 1 || to < from {
		return nil, 0, errors.New("invalid line range")
	}
	file, err := f.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, 0, err
	}
	if !info.Mode().IsRegular() {
		return nil, 0, errors.New("source is not a regular file")
	}
	reader := bufio.NewReader(io.LimitReader(file, maxFileBytes+1))
	size := 0
	for {
		line, readErr := reader.ReadBytes('\n')
		size += len(line)
		if size > maxFileBytes {
			return nil, 0, errors.New("file exceeds the 128 MiB cap")
		}
		if len(line) > 0 {
			total++
			if total >= from && total <= to {
				lines = append(lines, bytes.TrimSuffix(line, []byte{'\n'}))
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return lines, total, nil
			}
			return nil, 0, readErr
		}
	}
}

type SearchQuery struct {
	Paths              []string
	Before, After, Max int
	Cursor             string
}
type SearchResult struct {
	Lines []string
	Next  string
}

func (f Files) Search(pattern string, q SearchQuery) (SearchResult, error) {
	var out SearchResult
	re, err := regexp.Compile(pattern)
	if err != nil {
		return out, errors.New("invalid regex")
	}
	if q.Before < 0 || q.Before > 50 || q.After < 0 || q.After > 50 || q.Max < 1 || q.Max > 200 || len(q.Paths) > 200 {
		return out, errors.New("invalid search limits")
	}
	fileIndex, first := 0, 1
	if q.Cursor != "" {
		a, b, ok := strings.Cut(q.Cursor, ":")
		var e1, e2 error
		fileIndex, e1 = strconv.Atoi(a)
		first, e2 = strconv.Atoi(b)
		if !ok || e1 != nil || e2 != nil || fileIndex < 0 || fileIndex >= len(q.Paths) || first < 1 {
			return out, errors.New("invalid cursor")
		}
	}
	matches := 0
	for i := fileIndex; i < len(q.Paths); i++ {
		lines, _, err := f.ReadLines(q.Paths[i], 1, int(^uint(0)>>1))
		if err != nil {
			return out, err
		}
		shown := map[int]bool{}
		for n := first - 1; n < len(lines); n++ {
			if !re.Match(lines[n]) {
				continue
			}
			if matches == q.Max {
				out.Next = fmt.Sprintf("%d:%d", i, n+1)
				return out, nil
			}
			matches++
			for j := max(0, n-q.Before); j < min(len(lines), n+q.After+1); j++ {
				if shown[j] {
					continue
				}
				shown[j] = true
				sep := "-"
				if re.Match(lines[j]) {
					sep = ":"
				}
				out.Lines = append(out.Lines, fmt.Sprintf("%s:%d%s %s", q.Paths[i], j+1, sep, lines[j]))
			}
		}
		first = 1
	}
	return out, nil
}
