// SPDX-License-Identifier: Apache-2.0
package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/akenhq/aken/internal/source"
	"github.com/akenhq/aken/protocol"
)

func TestPrepare(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	if err := os.WriteFile(path, []byte("ordinary\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("app.log", filepath.Join(dir, "alias")); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	files := source.Files{Allowed: []string{dir}}
	for _, tt := range []struct {
		name   string
		params any
		bad    bool
	}{
		{"read_file", protocol.ReadFileParams{Path: path}, false},
		{"read_file", protocol.ReadFileParams{Path: filepath.Join(dir, "alias")}, false},
		{"read_file", protocol.ReadFileParams{Path: path, From: -1}, true},
		{"read_file", protocol.ReadFileParams{Path: path, To: 501}, true},
		{"read_file", protocol.ReadFileParams{Path: path, From: 3, To: 2}, true},
		{"read_file", protocol.ReadFileParams{Path: path, From: int(^uint(0) >> 1)}, true},
		{"read_file", protocol.ReadFileParams{Path: "relative"}, true},
		{"read_file", protocol.ReadFileParams{Path: "/etc/shadow"}, true},
		{"read_file", protocol.ReadFileParams{Path: filepath.Join(dir, "escape", "missing")}, true},
		{"tail", protocol.TailParams{Path: path, N: 501}, true},
		{"tail", protocol.TailParams{Path: path, N: -1}, true},
		{"search", protocol.SearchParams{Glob: filepath.Join(dir, "*.log"), Regex: "ordinary"}, false},
		{"search", protocol.SearchParams{Glob: "/etc/*", Regex: "a"}, true},
		{"search", protocol.SearchParams{Glob: filepath.Join(dir, "*"), Regex: "["}, true},
		{"search", protocol.SearchParams{Glob: filepath.Join(dir, "*"), Max: 201}, true},
		{"search", protocol.SearchParams{Glob: filepath.Join(dir, "*"), Before: 51}, true},
		{"search", protocol.SearchParams{Glob: filepath.Join(dir, "*"), After: -1}, true},
		{"search", protocol.SearchParams{Glob: filepath.Join(dir, "*"), Since: "bad"}, true},
		{"search", protocol.SearchParams{Glob: filepath.Join(dir, "*"), Cursor: "99:1"}, true},
		{"journal", protocol.JournalParams{Unit: "api"}, false},
		{"journal", protocol.JournalParams{Unit: "--help"}, true},
		{"journal", protocol.JournalParams{Unit: "api", Tail: 1, Max: 2}, true},
		{"journal", protocol.JournalParams{Unit: "api", Tail: 1, Regex: "a"}, true},
		{"journal", protocol.JournalParams{Unit: "api", Tail: 1, Cursor: "0"}, true},
		{"journal", protocol.JournalParams{Unit: "api", Since: "0s"}, true},
		{"journal", protocol.JournalParams{Unit: "api", Until: "bad"}, true},
		{"journal", protocol.JournalParams{Unit: "api", Cursor: "-1"}, true},
		{"docker_logs", protocol.DockerLogsParams{Container: "--help"}, true},
		{"docker_logs", protocol.DockerLogsParams{Container: "abcdef123456"}, false},
		{"systemctl_status", protocol.SystemctlStatusParams{Unit: "api.service"}, false},
		{"systemctl_status", protocol.SystemctlStatusParams{Unit: "a;b"}, true},
		{"ps", nil, false}, {"df", nil, false}, {"exec", nil, true},
		{"read_file", map[string]any{"path": path, "exec": "id"}, true},
	} {
		t.Run(fmt.Sprintf("%s/%v", tt.name, tt.params), func(t *testing.T) {
			p, err := prepare(context.Background(), job("j1", tt.name, tt.params), 1, files, time.Now())
			if (err != nil) != tt.bad {
				t.Fatalf("prepared: %+v, %v", p, err)
			}
			if err == nil && tt.name == "read_file" && (p.target != path || p.read.From != 1 || p.read.To != 500) {
				t.Fatalf("defaults or resolution: %+v", p)
			}
		})
	}
	if _, err := prepare(context.Background(), job("j1", "read_file", protocol.ReadFileParams{Path: path}), protocol.ClassWrite, files, time.Now()); err == nil || err.Error() != "class mismatch" {
		t.Fatal("class mismatch accepted", err)
	}
	for _, raw := range []string{`null`, `[]`, `{"path":"x"} {}`, `{"path":2}`, `{`} {
		if _, err := prepare(context.Background(), protocol.Job{ID: "j1", Name: "read_file", Params: json.RawMessage(raw)}, 1, files, time.Now()); err == nil {
			t.Fatal("accepted", raw)
		}
	}
}

func TestSensitiveAndGlob(t *testing.T) {
	for _, name := range []string{".env", ".env.prod", "shadow", "gshadow", "a.pem", "a.key", "a.p12", "a.pfx", "a.kdbx", "a.keystore", "id_any", "host_rsa", "host_ed25519", "host_ecdsa", "host_dsa", ".ssh/config", ".gnupg/config", ".aws/credentials"} {
		if !sensitive("/srv/" + name) {
			t.Error("not sensitive", name)
		}
	}
	for _, name := range []string{"env", ".environment", "shadow.log", "key.log", "ssh/config", "identity", "app.log"} {
		if sensitive("/srv/" + name) {
			t.Error("sensitive", name)
		}
	}
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "escape.log")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "a.log")
	if err := os.WriteFile(path, []byte("ordinary"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	files := source.Files{Allowed: []string{dir}}
	paths, err := expand(files, filepath.Join(dir, "*.log"), time.Time{})
	if err != nil || !reflect.DeepEqual(paths, []string{path}) {
		t.Fatal(paths, err)
	}
	paths, err = expand(files, filepath.Join(dir, "*.log"), time.Now().Add(-time.Hour))
	if err != nil || len(paths) != 0 {
		t.Fatal(paths, err)
	}
	for i := 0; i < 201; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%03d", i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := expand(files, filepath.Join(dir, "f*"), time.Time{}); err == nil {
		t.Fatal("accepted 201 files")
	}
}

func TestFileJobs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	if err := os.WriteFile(path, []byte("one\ntwo\xff\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	files := source.Files{Allowed: []string{dir}}
	for _, tt := range []struct {
		name   string
		params any
		want   []string
		next   string
	}{
		{"read_file", protocol.ReadFileParams{Path: path, From: 2, To: 2}, []string{"2: two\xff"}, "3"},
		{"read_file", protocol.ReadFileParams{Path: path, From: 3, To: 5}, []string{"3: three"}, ""},
		{"tail", protocol.TailParams{Path: path, N: 2}, []string{"2: two\xff", "3: three"}, ""},
		{"search", protocol.SearchParams{Glob: filepath.Join(dir, "*"), Regex: "two", Before: 1, After: 1}, []string{path + ":1- one", path + ":2: two\xff", path + ":3- three"}, ""},
	} {
		p, err := prepare(context.Background(), job("j1", tt.name, tt.params), 1, files, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		lines, next, err := execute(context.Background(), p, files)
		if err != nil || next != tt.next || !reflect.DeepEqual(lines, tt.want) {
			t.Fatalf("%s: %q,%q,%v", tt.name, lines, next, err)
		}
	}
	for i := 0; i < 501; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%03d", i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	lines, next, err := listDir(files, dir)
	if err != nil || len(lines) != 500 || next != "f499" {
		t.Fatal(len(lines), next, err)
	}
}

func TestCommandHelper(t *testing.T) {
	for i, arg := range os.Args {
		if arg == "--serve-command-helper" {
			switch os.Args[i+1] {
			case "bytes":
				_, _ = os.Stdout.Write([]byte("one\ntwo\xff\n"))
			case "many":
				for j := 0; j < 510; j++ {
					_, _ = fmt.Fprintln(os.Stdout, j)
				}
			case "large":
				_, _ = os.Stdout.WriteString(strings.Repeat("x", 9<<20))
			case "wait":
				time.Sleep(10 * time.Second)
			}
			code := 0
			_, _ = fmt.Sscan(os.Args[i+2], &code)
			os.Exit(code)
		}
	}
}
func TestCommands(t *testing.T) {
	original := execCommand
	t.Cleanup(func() { execCommand = original })
	for _, tt := range []struct {
		name, mode string
		code       int
		bad        bool
		args       []string
		count      int
	}{
		{"systemctl", "bytes", 0, false, []string{"status", "--no-pager", "--lines=0", "api"}, 2},
		{"systemctl", "bytes", 4, false, []string{"status", "--no-pager", "--lines=0", "api"}, 2},
		{"systemctl", "bytes", 5, true, []string{"status", "--no-pager", "--lines=0", "api"}, 0},
		{"ps", "many", 0, false, []string{"-eo", "pid,ppid,user,%cpu,%mem,rss,etimes,args", "--sort=-%cpu"}, 500},
		{"df", "bytes", 0, false, []string{"-hP"}, 2},
		{"df", "bytes", 1, true, []string{"-hP"}, 0},
	} {
		t.Run(fmt.Sprintf("%s/%d", tt.name, tt.code), func(t *testing.T) {
			execCommand = func(ctx context.Context, path string, args ...string) *exec.Cmd {
				if path != "/usr/bin/"+tt.name && path != "/bin/"+tt.name || !reflect.DeepEqual(args, tt.args) {
					t.Fatalf("command %s %v", path, args)
				}
				return exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCommandHelper$", "--", "--serve-command-helper", tt.mode, fmt.Sprint(tt.code))
			}
			p := preparedJob{job: protocol.Job{Name: tt.name}, target: "api"}
			if tt.name == "systemctl" {
				p.job.Name = "systemctl_status"
			}
			lines, _, err := execute(context.Background(), p, source.Files{})
			if (err != nil) != tt.bad || len(lines) != tt.count {
				t.Fatal(lines, err)
			}
			if tt.mode == "bytes" && !tt.bad && lines[1] != "two\xff" {
				t.Fatal("bytes changed")
			}
		})
	}
	execCommand = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCommandHelper$", "--", "--serve-command-helper", "large", "0")
	}
	lines, err := command(context.Background(), "df", "-hP")
	if err != nil || len(lines) != 1 || len(lines[0]) != 8<<20 {
		t.Fatal("command cap", len(lines), err)
	}
	execCommand = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCommandHelper$", "--", "--serve-command-helper", "wait", "0")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := command(ctx, "df", "-hP"); err == nil {
		t.Fatal("command ignored cancellation")
	}
}

func TestResultCaps(t *testing.T) {
	p := preparedJob{job: protocol.Job{Name: "read_file"}}
	r := protocol.Result{Lines: []string{"1: small", "2: " + strings.Repeat("x", protocol.MaxResultPlaintext), "3: end"}}
	capResult(&r, p)
	if len(r.Lines) != 1 || r.Next != "2" {
		t.Fatalf("cap: %+v", r)
	}
	r = protocol.Result{Lines: []string{"1: " + strings.Repeat("\x00", 170000), "2: " + strings.Repeat("\x00", 50000)}}
	capResult(&r, p)
	data, _ := json.Marshal(r)
	if len(data) > protocol.MaxResultBytes-16 || len(r.Lines) != 1 || r.Next != "2" {
		t.Fatal("JSON cap", len(data), len(r.Lines), r.Next)
	}
}

func TestJournalJobs(t *testing.T) {
	originalRead, originalResolve := readJournal, resolveContainers
	t.Cleanup(func() { readJournal = originalRead; resolveContainers = originalResolve })
	now := time.Date(2026, 9, 13, 14, 0, 0, 0, time.UTC)
	var gotSpec source.Spec
	readJournal = func(ctx context.Context, spec source.Spec, since, until time.Time) (*source.Source, error) {
		gotSpec = spec
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 60*time.Second {
			t.Error("missing job timeout")
		}
		if !since.Equal(now.Add(-time.Hour)) || !until.Equal(now) {
			t.Fatal("window", since, until)
		}
		return &source.Source{Lines: [][]byte{[]byte("first"), []byte("hit one"), []byte("third"), []byte("hit two\xff"), []byte("last")}}, nil
	}
	resolveContainers = func(context.Context, string) ([]string, error) { return []string{"api-1"}, nil }
	for _, tt := range []struct {
		name   string
		params any
		want   []string
		next   string
	}{
		{"journal", protocol.JournalParams{Unit: "api", Max: 2}, []string{"first", "hit one"}, "2"},
		{"journal", protocol.JournalParams{Unit: "api", Max: 2, Cursor: "2"}, []string{"third", "hit two\xff"}, "4"},
		{"journal", protocol.JournalParams{Unit: "api", Max: 2, Cursor: "4"}, []string{"last"}, ""},
		{"journal", protocol.JournalParams{Unit: "api", Regex: "hit", Max: 1}, []string{"hit one"}, "1"},
		{"journal", protocol.JournalParams{Unit: "api", Tail: 2}, []string{"hit two\xff", "last"}, ""},
		{"docker_logs", protocol.DockerLogsParams{Container: "api", Tail: 1}, []string{"last"}, ""},
	} {
		p, err := prepare(context.Background(), job("j1", tt.name, tt.params), 1, source.Files{}, now)
		if err != nil {
			t.Fatal(err)
		}
		lines, next, err := execute(context.Background(), p, source.Files{})
		if err != nil || next != tt.next || !reflect.DeepEqual(lines, tt.want) {
			t.Fatal(tt.name, lines, next, err)
		}
		if tt.name == "docker_logs" && (gotSpec.Target != "api-1" || gotSpec.Kind != source.KindContainer) {
			t.Fatal(gotSpec)
		}
	}
	resolveContainers = func(context.Context, string) ([]string, error) { return []string{"api-a", "api-b"}, nil }
	if _, err := prepare(context.Background(), job("j1", "docker_logs", protocol.DockerLogsParams{Container: "api"}), 1, source.Files{}, now); err == nil || err.Error() != "name matches several containers" {
		t.Fatal("ambiguous name", err)
	}
	resolveContainers = originalResolve
	for _, id := range []string{"abcdef123456", strings.Repeat("a", 64)} {
		p, err := prepare(context.Background(), job("j1", "docker_logs", protocol.DockerLogsParams{Container: id}), 1, source.Files{}, now)
		if err != nil {
			t.Fatal(err)
		}
		match := "CONTAINER_ID=" + id
		if len(id) == 64 {
			match = "CONTAINER_ID_FULL=" + id
		}
		if p.spec.Match != match {
			t.Fatal(p.spec)
		}
	}
}

func TestJournalPagesBeyondEightMiB(t *testing.T) {
	original := readJournal
	t.Cleanup(func() { readJournal = original })
	window := make([][]byte, 10)
	line := []byte(strings.Repeat("x", 1<<20))
	for i := range window[:9] {
		window[i] = line
	}
	window[9] = []byte("last")
	readJournal = func(context.Context, source.Spec, time.Time, time.Time) (*source.Source, error) {
		return &source.Source{Lines: window}, nil
	}
	for _, tt := range []struct{ cursor, next, want string }{
		{"8", "9", string(line)},
		{"9", "", "last"},
	} {
		p := preparedJob{journal: protocol.JournalParams{Max: 1, Cursor: tt.cursor}}
		lines, next, err := journal(context.Background(), p)
		if err != nil || next != tt.next || len(lines) != 1 || lines[0] != tt.want {
			t.Fatalf("cursor %s: %d lines, next %q, error %v", tt.cursor, len(lines), next, err)
		}
	}
}

func TestScopeRefusal(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	path := filepath.Join(outside, "app.log")
	if err := os.WriteFile(path, []byte("ordinary\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	files := source.Files{Allowed: []string{dir}}
	for _, tt := range []struct {
		name   string
		params any
		dir    string
	}{
		{"read_file", protocol.ReadFileParams{Path: path}, outside},
		{"tail", protocol.TailParams{Path: path}, outside},
		{"list_dir", protocol.ListDirParams{Path: outside}, outside},
		{"search", protocol.SearchParams{Glob: filepath.Join(outside, "*.log"), Regex: "a"}, outside},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p, err := prepare(context.Background(), job("j1", tt.name, tt.params), 1, files, time.Now())
			var scoped *scopeError
			if !errors.As(err, &scoped) || scoped.dir != tt.dir {
				t.Fatalf("error = %v, want the scope refusal for %s", err, tt.dir)
			}
			if err.Error() != "outside the session scope ("+dir+")" {
				t.Fatalf("message = %q", err)
			}
			// The row keeps the requested path so the approval screen can name it.
			if want := fmt.Sprint(reflect.ValueOf(tt.params).Field(0)); p.target != want {
				t.Fatalf("target = %q, want %q", p.target, want)
			}
		})
	}
	// A link that leaves the scope is refused outright, never offered for approval.
	var scoped *scopeError
	_, err := prepare(context.Background(), job("j1", "read_file", protocol.ReadFileParams{Path: filepath.Join(dir, "escape", "app.log")}), 1, files, time.Now())
	if err == nil || errors.As(err, &scoped) {
		t.Fatalf("link escape = %v", err)
	}
}

func TestScopeGrant(t *testing.T) {
	dir := t.TempDir()
	logs := filepath.Join(dir, "logs")
	if err := os.Mkdir(logs, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(logs, link); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "app.log")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		dir  string
		want []string
		bad  string
	}{
		{dir: logs, want: []string{logs}},
		{dir: link, want: []string{link, logs}},
		{dir: file, bad: "it is not a directory"},
		{dir: filepath.Join(dir, "gone"), bad: "the directory does not exist"},
	} {
		dirs, err := scopeGrant(tt.dir)
		if tt.bad != "" {
			if err == nil || err.Error() != tt.bad {
				t.Fatalf("%s: err = %v, want %q", tt.dir, err, tt.bad)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(dirs, tt.want) {
			t.Fatalf("%s: %v, %v", tt.dir, dirs, err)
		}
	}
}
