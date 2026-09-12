// SPDX-License-Identifier: Apache-2.0
package source

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestFiles(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	path := filepath.Join(dir, "app.log")
	other := filepath.Join(outside, "secret")
	for _, p := range []string{path, other} {
		if err := os.WriteFile(p, []byte("one\ntwo\nthree\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(other, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("app.log", filepath.Join(dir, "inside")); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, path string
		tail       int
		want       string
		bad        bool
	}{
		{"all", path, 0, "one\ntwo\nthree", false}, {"tail", path, 2, "two\nthree", false}, {"long tail", path, 20, "one\ntwo\nthree", false},
		{"outside", other, 0, "", true}, {"relative", "app.log", 0, "", true}, {"unclean", dir + "/./app.log", 0, "", true},
		{"directory", dir, 0, "", true}, {"escape", filepath.Join(dir, "escape"), 0, "", true}, {"inside", filepath.Join(dir, "inside"), 1, "three", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, err := (Files{Allowed: []string{dir}, Tail: tt.tail}).ReadFile(tt.path)
			if tt.bad {
				if err == nil {
					t.Fatal("wanted error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := string(bytes.Join(s.Lines, []byte{'\n'})); got != tt.want {
				t.Fatalf("lines = %q", got)
			}
			if tt.tail > 0 && !strings.HasPrefix(s.Note, "last ") {
				t.Fatal("missing note")
			}
		})
	}
}
func TestTailBoundaries(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		tail       int
		want       string
	}{
		{"no newline", "a\nb", 1, "b"}, {"blank final line", "a\n\n", 1, ""}, {"empty", "", 2, ""},
		{"large prefix", strings.Repeat("x", 128<<10) + "\nlast\n", 1, "last"},
		{"long last line", "a\n" + strings.Repeat("z", 70<<10), 1, strings.Repeat("z", 70<<10)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "log")
			if err := os.WriteFile(path, []byte(tt.body), 0o600); err != nil {
				t.Fatal(err)
			}
			s, err := (Files{Allowed: []string{dir}, Tail: tt.tail}).ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(bytes.Join(s.Lines, []byte{'\n'})) != tt.want {
				t.Fatal("incorrect tail")
			}
		})
	}
}
func TestGlob(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	for _, name := range []string{"b.log", "old.log", "a.log"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
		stamp := now
		if name == "old.log" {
			stamp = now.Add(-2 * time.Hour)
		}
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "dir.log"), 0o700); err != nil {
		t.Fatal(err)
	}
	f := Files{Allowed: []string{dir}, ModifiedAfter: now.Add(-time.Hour)}
	sources, err := f.Glob(filepath.Join(dir, "*.log"))
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 2 || filepath.Base(sources[0].Target) != "a.log" || filepath.Base(sources[1].Target) != "b.log" {
		t.Fatalf("matches = %+v", sources)
	}
	if _, err := f.ReadFile(filepath.Join(dir, "old.log")); err != nil {
		t.Fatal("mtime filtered explicit file", err)
	}
	if _, err := f.Glob("["); err == nil {
		t.Fatal("accepted bad glob")
	}
}

func TestSpecialFileAndDeduplication(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := Files{Allowed: []string{dir}}
	if _, err := f.ReadFile(filepath.Join(dir, "pipe")); err == nil {
		t.Fatal("accepted a FIFO")
	}
	if matches, err := f.Glob(filepath.Join(dir, "pipe")); err != nil || len(matches) != 0 {
		t.Fatalf("FIFO glob: %v, %v", matches, err)
	}
	path := filepath.Join(dir, "app.log")
	if err := os.WriteFile(path, []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("app.log", filepath.Join(dir, "alias.log")); err != nil {
		t.Fatal(err)
	}
	matches, err := ReadFiles(f, []string{path, path}, []string{filepath.Join(dir, "*.log")})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Target != path || string(matches[0].Lines[0]) != "first" {
		t.Fatal("duplicate canonical path included")
	}
}
