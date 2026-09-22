// SPDX-License-Identifier: Apache-2.0
package source

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"slices"
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

func TestListAndReadLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "b.log")
	if err := os.WriteFile(path, []byte("a\nb\xff\nc"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "a"), 0o700); err != nil {
		t.Fatal(err)
	}
	files := Files{Allowed: []string{dir}}
	entries, err := files.List(dir)
	if err != nil || len(entries) != 2 || entries[0].Name != "a/" || entries[1].Name != "b.log" || entries[1].Size != 6 {
		t.Fatalf("list: %+v, %v", entries, err)
	}
	for _, tt := range []struct {
		from, to, total int
		want            string
		bad             bool
	}{
		{1, 1, 3, "a", false}, {2, 2, 3, "b\xff", false}, {2, 99, 3, "b\xff\nc", false}, {5, 9, 3, "", false}, {0, 1, 0, "", true}, {3, 2, 0, "", true},
	} {
		lines, total, err := files.ReadLines(path, tt.from, tt.to)
		if (err != nil) != tt.bad || total != tt.total || string(bytes.Join(lines, []byte{'\n'})) != tt.want {
			t.Fatalf("ReadLines(%d,%d)=%q,%d,%v", tt.from, tt.to, lines, total, err)
		}
	}
	if _, err := files.List(t.TempDir()); err == nil {
		t.Fatal("listed outside scope")
	}
	if _, _, err := files.ReadLines(dir, 1, 2); err == nil {
		t.Fatal("read directory")
	}
}

func TestSearchPages(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	for _, path := range []string{a, b} {
		if err := os.WriteFile(path, []byte("before\nhit\nafter\nhit\nend\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	files := Files{Allowed: []string{dir}}
	q := SearchQuery{Paths: []string{a, b}, Before: 1, After: 1, Max: 1}
	for _, tt := range []struct{ cursor, next, prefix string }{
		{"", "0:4", a + ":1- before"}, {"0:4", "1:2", a + ":3- after"}, {"1:2", "1:4", b + ":1- before"}, {"1:4", "", b + ":3- after"},
	} {
		q.Cursor = tt.cursor
		r, err := files.Search("hit", q)
		if err != nil || r.Next != tt.next || len(r.Lines) != 3 || r.Lines[0] != tt.prefix {
			t.Fatalf("page %q: %+v, %v", tt.cursor, r, err)
		}
	}
	c := filepath.Join(dir, "c")
	if err := os.WriteFile(c, []byte("x1\nx2\nhit\nhit\nx5\nx6\nx7\nhit\nx9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		before, after, max int
		cursor, next       string
		want               []string
	}{
		{2, 2, 3, "", "", []string{"1- x1", "2- x2", "3: hit", "4: hit", "5- x5", "6- x6", "7- x7", "8: hit", "9- x9"}},
		{1, 1, 1, "", "0:4", []string{"2- x2", "3: hit", "4: hit"}},
		{1, 1, 1, "0:4", "0:8", []string{"3: hit", "4: hit", "5- x5"}},
		{0, 0, 5, "0:5", "", []string{"8: hit"}},
	} {
		r, err := files.Search("hit", SearchQuery{Paths: []string{c}, Before: tt.before, After: tt.after, Max: tt.max, Cursor: tt.cursor})
		for i := range tt.want {
			tt.want[i] = c + ":" + tt.want[i]
		}
		if err != nil || r.Next != tt.next || !slices.Equal(r.Lines, tt.want) {
			t.Fatalf("context %+v: %q, %q, %v", tt, r.Lines, r.Next, err)
		}
	}
	for _, cursor := range []string{"bad", "-1:1", "0:0", "2:1", "0:99999999999999999999999"} {
		q.Cursor = cursor
		if _, err := files.Search("hit", q); err == nil {
			t.Fatalf("accepted cursor %q", cursor)
		}
	}
	q.Cursor = ""
	if _, err := files.Search("[", q); err == nil {
		t.Fatal("accepted regex")
	}
	q.Paths = []string{filepath.Join(t.TempDir(), "outside")}
	if _, err := files.Search("hit", q); err == nil {
		t.Fatal("searched outside scope")
	}
}

func TestGrantedPaths(t *testing.T) {
	dir := t.TempDir()
	granted, neighbour := filepath.Join(dir, "granted.log"), filepath.Join(dir, "neighbour.log")
	for _, path := range []string{granted, neighbour} {
		if err := os.WriteFile(path, []byte("line\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	nested := filepath.Join(dir, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(nested, "inner.log")
	if err := os.WriteFile(inner, []byte("line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	files := Files{Paths: []string{granted, nested}}
	// A granted name covers that one name, its parent directory does not come with it,
	// and neither does anything under a granted directory.
	for path, want := range map[string]bool{granted: true, nested: true, neighbour: false, dir: false, inner: false} {
		if files.Permits(path) != want {
			t.Errorf("Permits(%s) = %v, want %v", path, !want, want)
		}
		file, err := files.Open(path)
		if err == nil {
			_ = file.Close()
		}
		if (err == nil) != want {
			t.Errorf("Open(%s) = %v, want allowed=%v", path, err, want)
		}
	}
	root := Files{Paths: []string{"/"}}
	if _, err := root.Open("/"); err == nil {
		t.Fatal("opened the filesystem root as a granted name")
	}
}

func TestGzip(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	// Two gzip members, as appending to a rotated log produces; readers must see both.
	for _, part := range []string{"one\ntwo\n", "three\nfour\n"} {
		w := gzip.NewWriter(&buf)
		if _, err := w.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "error.log.2.gz")
	truncated := filepath.Join(dir, "cut.gz")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(truncated, buf.Bytes()[:buf.Len()/2], 0o600); err != nil {
		t.Fatal(err)
	}
	files := Files{Allowed: []string{dir}}
	lines, total, err := files.ReadLines(path, 2, 3)
	if err != nil || total != 4 || string(bytes.Join(lines, []byte{'\n'})) != "two\nthree" {
		t.Fatalf("ReadLines: %q, %d, %v", lines, total, err)
	}
	lines, total, err = files.TailLines(path, 2)
	if err != nil || total != 4 || string(bytes.Join(lines, []byte{'\n'})) != "three\nfour" {
		t.Fatalf("TailLines: %q, %d, %v", lines, total, err)
	}
	r, err := files.Search("^t", SearchQuery{Paths: []string{path}, Max: 5})
	if err != nil || !slices.Equal(r.Lines, []string{path + ":2: two", path + ":3: three"}) {
		t.Fatalf("Search: %q, %v", r.Lines, err)
	}
	for _, tail := range []int{0, 3} {
		s, err := Files{Allowed: []string{dir}, Tail: tail}.ReadFile(path)
		want := map[int]string{0: "one\ntwo\nthree\nfour", 3: "two\nthree\nfour"}[tail]
		if err != nil || string(bytes.Join(s.Lines, []byte{'\n'})) != want {
			t.Fatalf("ReadFile tail %d: %v", tail, err)
		}
	}
	if _, _, err := files.TailLines(truncated, 2); err == nil || err.Error() != "gzip file is truncated or corrupt" {
		t.Fatalf("truncated: %v", err)
	}
	if _, err := files.ReadFile(truncated); err == nil || err.Error() != "gzip file is truncated or corrupt" {
		t.Fatalf("truncated ReadFile: %v", err)
	}
}

func TestCollectFileErrors(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.log")
	if err := os.WriteFile(good, []byte("line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	escape := filepath.Join(dir, "escape.log")
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), escape); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	files := Files{Allowed: []string{dir}, Stderr: &stderr}
	for _, tt := range []struct{ path, want string }{
		{filepath.Join(dir, "missing"), "cannot read " + filepath.Join(dir, "missing") + ": no such file or directory"},
		{dir, "cannot read " + dir + ": not a regular file"},
		{escape, "refusing " + escape + ": it is a symlink that leaves the allowed directories (" + dir + ")"},
	} {
		if _, err := files.ReadFile(tt.path); err == nil || err.Error() != tt.want {
			t.Fatalf("%s: %v", tt.path, err)
		}
	}
	sources, err := files.Glob(filepath.Join(dir, "*.log"))
	want := "aken: skipped " + escape + ": it is a symlink that leaves the allowed directories (" + dir + ")\n"
	if err != nil || len(sources) != 1 || sources[0].Target != good || stderr.String() != want {
		t.Fatalf("sources %v, err %v, stderr %q", sources, err, stderr.String())
	}
	if os.Geteuid() != 0 {
		if err := os.Chmod(good, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := files.ReadFile(good); err == nil || err.Error() != "cannot read "+good+": permission denied" {
			t.Fatal(err)
		}
	}
}
