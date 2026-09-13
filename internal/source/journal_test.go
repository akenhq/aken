// SPDX-License-Identifier: Apache-2.0
package source

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNames(t *testing.T) {
	for _, tt := range []struct {
		name            string
		unit, container bool
	}{
		{"nginx.service", true, true}, {"a:b@c\\d", true, false}, {"api-1", true, true}, {"", false, false}, {"-bad", false, false}, {"a b", false, false}, {"a/b", false, false}, {"a;id", false, false}, {strings.Repeat("a", 128), true, true}, {strings.Repeat("a", 129), true, false}, {strings.Repeat("a", 257), false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if (ValidateUnit(tt.name) == nil) != tt.unit || (ValidateContainer(tt.name) == nil) != tt.container {
				t.Fatal("unexpected name validation")
			}
		})
	}
}
func TestJournalHelper(t *testing.T) {
	args := os.Args
	for i, a := range args {
		if a != "--journal-helper" {
			continue
		}
		switch args[i+1] {
		case "names":
			_, _ = os.Stdout.WriteString(args[i+2])
		case "lines":
			_, _ = os.Stdout.Write([]byte("one\ntwo\xff\n"))
		case "error":
			_, _ = os.Stderr.WriteString("permission denied\nnot shown\n")
			os.Exit(1)
		}
		os.Exit(0)
	}
}
func TestReadJournal(t *testing.T) {
	original := execCommand
	t.Cleanup(func() { execCommand = original })
	since := time.Unix(10, 0)
	until := time.Unix(20, 0)
	for _, tt := range []struct {
		name, mode string
		kind       Kind
		wantLines  int
		wantError  string
	}{
		{"unit", "lines", KindUnit, 2, ""}, {"container", "lines", KindContainer, 2, ""}, {"empty", "empty", KindUnit, 0, ""}, {"failure", "error", KindUnit, 0, "permission denied"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			execCommand = func(ctx context.Context, path string, args ...string) *exec.Cmd {
				match := "--unit=api"
				if tt.kind == KindContainer {
					match = "CONTAINER_NAME=api"
				}
				expected := []string{"--no-pager", "-q", "--utc", "-o", "short-iso-precise", "--no-hostname", "--since=@10", "--until=@20", match}
				if path != "/usr/bin/journalctl" && path != "/bin/journalctl" || !reflect.DeepEqual(args, expected) {
					t.Fatalf("command = %s %q", path, args)
				}
				return exec.CommandContext(ctx, os.Args[0], "-test.run=^TestJournalHelper$", "--", "--journal-helper", tt.mode)
			}
			s, err := ReadJournal(context.Background(), Spec{Kind: tt.kind, Target: "api"}, since, until)
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) || strings.Contains(err.Error(), "not shown") {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(s.Lines) != tt.wantLines || s.Name != string(tt.kind)+":api" || !s.Since.Equal(since) {
				t.Fatalf("source = %+v", s)
			}
			if tt.wantLines > 0 && string(s.Lines[1]) != "two\xff" {
				t.Fatal("bytes changed")
			}
		})
	}
	execCommand = func(context.Context, string, ...string) *exec.Cmd { t.Fatal("invalid name reached exec"); return nil }
	if _, err := ReadJournal(context.Background(), Spec{Kind: KindUnit, Target: "--help"}, since, until); err == nil {
		t.Fatal("accepted invalid name")
	}
}

func TestResolveContainers(t *testing.T) {
	original := execCommand
	t.Cleanup(func() { execCommand = original })
	var many []string
	for i := 0; i < 25; i++ {
		many = append(many, fmt.Sprintf("name%02d", i))
	}
	for _, tt := range []struct {
		name, journal, mode string
		want                []string
		wantError           string
	}{
		{"api", "api.1.task\napi_other\n api \n", "names", []string{"api"}, ""},
		{"api", "api_z\napiary\napi.1.task\napi-other\napi.1.task\n\n", "names", []string{"api-other", "api.1.task", "api_z"}, ""},
		{"abcdef123456", "", "", []string{"abcdef123456"}, ""},
		{strings.Repeat("aB12", 16), "", "", []string{strings.Repeat("aB12", 16)}, ""},
		{"ghijkl123456", "ghijkl123456\n", "names", []string{"ghijkl123456"}, ""},
		{"api", " c \n a\nb\n", "names", nil, `no container named "api" in the journal; known names: a, b, c`},
		{"api", strings.Join(many, "\n"), "names", nil, `no container named "api" in the journal; known names: ` + strings.Join(many[:20], ", ")},
		{"api", " \n\n", "names", nil, "no container logs in the journal; is the docker logging driver journald? See docs/docker.md"},
		{"api", "", "error", nil, "permission denied"},
		{"-bad", "", "", nil, "invalid container name"},
	} {
		t.Run(tt.name+tt.mode, func(t *testing.T) {
			execCommand = func(ctx context.Context, path string, args ...string) *exec.Cmd {
				if tt.mode == "" {
					t.Fatal("unexpected journal lookup")
				}
				if path != "/usr/bin/journalctl" && path != "/bin/journalctl" || !reflect.DeepEqual(args, []string{"--no-pager", "-q", "-F", "CONTAINER_NAME"}) {
					t.Fatalf("command = %s %q", path, args)
				}
				return exec.CommandContext(ctx, os.Args[0], "-test.run=^TestJournalHelper$", "--", "--journal-helper", tt.mode, tt.journal)
			}
			got, err := ResolveContainers(context.Background(), tt.name)
			if tt.wantError != "" {
				if err == nil || (tt.mode != "error" && err.Error() != tt.wantError) || !strings.Contains(err.Error(), tt.wantError) || strings.Contains(err.Error(), "not shown") {
					t.Fatalf("error = %v", err)
				}
			} else if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("names = %v, error = %v", got, err)
			}
		})
	}
}

func TestJournalMatch(t *testing.T) {
	original := execCommand
	t.Cleanup(func() { execCommand = original })
	for _, tt := range []struct{ target, match string }{
		{"abcdef123456", "CONTAINER_ID=abcdef123456"},
		{strings.Repeat("a", 64), "CONTAINER_ID_FULL=" + strings.Repeat("a", 64)},
		{"api.1.task", "CONTAINER_NAME=api.1.task"},
	} {
		execCommand = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
			if args[len(args)-1] != tt.match {
				t.Fatalf("match = %q", args[len(args)-1])
			}
			return exec.CommandContext(ctx, os.Args[0], "-test.run=^TestJournalHelper$", "--", "--journal-helper", "lines")
		}
		s, err := ReadJournal(context.Background(), Spec{Kind: KindContainer, Target: tt.target, Match: tt.match}, time.Unix(10, 0), time.Unix(20, 0))
		if err != nil || s.Name != "container:"+tt.target {
			t.Fatalf("source = %+v, error = %v", s, err)
		}
	}
}

func TestBinaryPath(t *testing.T) {
	for _, name := range []string{"journalctl", "ps", "df", "systemctl"} {
		path, err := BinaryPath(name)
		if err != nil || path != "/usr/bin/"+name && path != "/bin/"+name {
			t.Fatalf("BinaryPath(%q)=%q,%v", name, path, err)
		}
	}
	for _, name := range []string{"", "../bin/sh", "/bin/sh", "a/b", ".", "..", "aken-binary-that-does-not-exist"} {
		if _, err := BinaryPath(name); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
}
