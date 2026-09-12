// SPDX-License-Identifier: Apache-2.0
package source

import (
	"context"
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
