// SPDX-License-Identifier: Apache-2.0
package source

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
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
	// Allowed lists directories every path under them may be read from. Paths lists
	// single names granted on their own; a name in Paths covers that one name and
	// nothing under it.
	Allowed, Paths []string
	Tail           int
	ModifiedAfter  time.Time
}

func under(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Permits reports whether the scope covers path, either through an allowed
// directory or as a granted single name.
func (f Files) Permits(path string) bool {
	return slices.Contains(f.Paths, path) || slices.ContainsFunc(f.Allowed, func(dir string) bool { return under(dir, path) })
}

func (f Files) Open(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("file path must be absolute and clean")
	}
	for _, dir := range f.Allowed {
		if under(dir, path) {
			return openIn(dir, path)
		}
	}
	// A granted name is opened through its parent, so os.Root still refuses a link
	// that leaves the parent, and nothing else in the parent becomes readable.
	if slices.Contains(f.Paths, path) && filepath.Dir(path) != path {
		return openIn(filepath.Dir(path), path)
	}
	return nil, fmt.Errorf("path is outside the allowed directories (%s); add another with --allow DIR", strings.Join(f.Allowed, ", "))
}

func openIn(dir, path string) (*os.File, error) {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	// Nonblocking open lets the regular-file check reject FIFOs without waiting for a writer.
	return root.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK, 0)
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
	reader, compressed, err := decompress(file)
	if err != nil {
		return nil, err
	}
	var lines [][]byte
	switch {
	case compressed && f.Tail > 0:
		// A gzip stream cannot be read from the end, so keep the last lines while streaming it.
		lines, _, err = lastLines(reader, f.Tail)
	case f.Tail > 0:
		var data []byte
		data, err = readTail(file, info.Size(), f.Tail)
		lines = splitLines(data)
	default:
		var data []byte
		data, err = io.ReadAll(io.LimitReader(reader, maxFileBytes+1))
		if err == nil && len(data) > maxFileBytes {
			err = errors.New("file exceeds the 128 MiB artifact cap; narrow --since or use --tail")
		}
		lines = splitLines(data)
	}
	if err != nil {
		return nil, readError(err, compressed)
	}
	s := &Source{Spec: Spec{Kind: KindFile, Target: path}, Name: "file:" + path, Lines: lines}
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
	total, err = f.scan(path, func(n int, line []byte) bool {
		if n >= from && n <= to {
			lines = append(lines, slices.Clone(line))
		}
		return true
	})
	if err != nil {
		return nil, 0, err
	}
	return lines, total, nil
}

// TailLines returns the last n lines of a file and its total line count, so callers can number them.
func (f Files) TailLines(path string, n int) (lines [][]byte, total int, err error) {
	if n < 1 {
		return nil, 0, errors.New("invalid tail count")
	}
	err = f.open(path, func(r io.Reader) (err error) {
		lines, total, err = lastLines(r, n)
		return err
	})
	return lines, total, err
}

func lastLines(r io.Reader, n int) ([][]byte, int, error) {
	var lines [][]byte
	total, err := scanLines(r, func(_ int, line []byte) bool {
		if len(lines) == n {
			lines = append(lines[:0], lines[1:]...)
		}
		lines = append(lines, slices.Clone(line))
		return true
	})
	if err != nil {
		return nil, 0, err
	}
	return lines, total, nil
}

// scan streams a file's lines to fn, numbered from 1, until fn returns false. Only one line is held
// at a time, so live reads work on files of any size; a single line is still capped at maxFileBytes.
func (f Files) scan(path string, fn func(n int, line []byte) bool) (total int, err error) {
	err = f.open(path, func(r io.Reader) (err error) {
		total, err = scanLines(r, fn)
		return err
	})
	return total, err
}

// open passes read a regular file's content, decompressed if it is gzip.
func (f Files) open(path string, read func(io.Reader) error) error {
	file, err := f.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("source is not a regular file")
	}
	reader, compressed, err := decompress(file)
	if err != nil {
		return err
	}
	return readError(read(reader), compressed)
}

// decompress returns a reader over the file's text. Content that starts with the gzip magic bytes is
// decompressed, so rotated logs such as error.log.2.gz read as text whatever their name.
func decompress(file io.Reader) (io.Reader, bool, error) {
	reader := bufio.NewReaderSize(file, 64<<10)
	magic, err := reader.Peek(2)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, false, err
	}
	if !bytes.Equal(magic, []byte{0x1f, 0x8b}) {
		return reader, false, nil
	}
	gz, err := gzip.NewReader(reader)
	if err != nil {
		return nil, false, errors.New("gzip file is corrupt")
	}
	return gz, true, nil
}

func readError(err error, compressed bool) error {
	var corrupt flate.CorruptInputError
	if compressed && (errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, gzip.ErrChecksum) || errors.Is(err, gzip.ErrHeader) || errors.As(err, &corrupt)) {
		return errors.New("gzip file is truncated or corrupt")
	}
	return err
}

func scanLines(r io.Reader, fn func(n int, line []byte) bool) (int, error) {
	reader := bufio.NewReaderSize(r, 64<<10)
	var line []byte
	total := 0
	for {
		chunk, readErr := reader.ReadSlice('\n')
		if len(line)+len(chunk) > maxFileBytes {
			return 0, errors.New("file has a line longer than 128 MiB")
		}
		line = append(line, chunk...)
		if errors.Is(readErr, bufio.ErrBufferFull) {
			continue
		}
		if len(line) > 0 {
			total++
			if !fn(total, bytes.TrimSuffix(line, []byte{'\n'})) {
				return total, nil
			}
		}
		line = line[:0]
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return total, nil
			}
			return 0, readErr
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
		path := q.Paths[i]
		emit := func(n int, line []byte) {
			sep := "-"
			if re.Match(line) {
				sep = ":"
			}
			out.Lines = append(out.Lines, fmt.Sprintf("%s:%d%s %s", path, n, sep, line))
		}
		// before holds the last q.Before lines not yet shown; after counts context lines still owed.
		type held struct {
			n    int
			line []byte
		}
		var before []held
		after, done := 0, false
		_, err := f.scan(path, func(n int, line []byte) bool {
			hit := n >= first && re.Match(line)
			if hit && matches == q.Max && out.Next == "" {
				out.Next = fmt.Sprintf("%d:%d", i, n)
			}
			if out.Next != "" {
				if after == 0 {
					done = true
					return false
				}
				emit(n, line)
				after--
				return true
			}
			if hit {
				matches++
				for _, h := range before {
					emit(h.n, h.line)
				}
				before = before[:0]
				emit(n, line)
				after = q.After
				return true
			}
			if after > 0 {
				emit(n, line)
				after--
				return true
			}
			if q.Before > 0 {
				if len(before) == q.Before {
					before = append(before[:0], before[1:]...)
				}
				before = append(before, held{n, slices.Clone(line)})
			}
			return true
		})
		if err != nil {
			return SearchResult{}, err
		}
		if done || out.Next != "" {
			return out, nil
		}
		first = 1
	}
	return out, nil
}
