// SPDX-License-Identifier: Apache-2.0
package source

import (
	"bytes"
	"errors"
	"regexp"
	"strings"
	"time"
)

type Kind string

const (
	KindUnit      Kind = "unit"
	KindContainer Kind = "container"
	KindFile      Kind = "file"
)

type Spec struct {
	Kind   Kind
	Target string
	Match  string
}
type Source struct {
	Spec
	Name         string
	Lines        [][]byte
	Since, Until time.Time
	Stderr       string
	Note         string
}

var unitName = regexp.MustCompile(`^[A-Za-z0-9:_.@\\-]{1,256}$`)
var containerName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

func ValidateUnit(name string) error {
	if !unitName.MatchString(name) || strings.HasPrefix(name, "-") {
		return errors.New("invalid unit name")
	}
	return nil
}
func ValidateContainer(name string) error {
	if !containerName.MatchString(name) {
		return errors.New("invalid container name")
	}
	return nil
}
func splitLines(data []byte) [][]byte {
	if len(data) == 0 {
		return nil
	}
	lines := bytes.Split(data, []byte{'\n'})
	if len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func ReadFiles(f Files, paths, patterns []string) ([]*Source, error) {
	var sources []*Source
	seen := map[string]bool{}
	for _, path := range paths {
		s, err := f.readFile(path, seen)
		if err != nil {
			return nil, f.fileError(path, err)
		}
		if s != nil {
			sources = append(sources, s)
		}
	}
	for _, pattern := range patterns {
		matches, err := f.glob(pattern, seen)
		if err != nil {
			return nil, err
		}
		sources = append(sources, matches...)
	}
	return sources, nil
}

type CommandStderr struct {
	line []byte
	done bool
}

func (s *CommandStderr) Write(p []byte) (int, error) {
	for _, b := range p {
		if s.done {
			break
		}
		if b == '\n' {
			if len(bytes.TrimSpace(s.line)) > 0 {
				s.done = true
			} else {
				s.line = s.line[:0]
			}
		} else if len(s.line) < 200 {
			s.line = append(s.line, b)
		}
	}
	return len(p), nil
}

func (s *CommandStderr) String() string { return strings.TrimSpace(string(s.line)) }
