// SPDX-License-Identifier: Apache-2.0
package source

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

func (f Files) open(path string) (*os.File, error) {
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
	file, err := f.open(path)
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
		file, err := f.open(path)
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
