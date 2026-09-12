// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	if err := os.WriteFile(path, []byte("from 203.0.113.5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	original := geteuid
	t.Cleanup(func() { geteuid = original })
	for _, tt := range []struct {
		name   string
		args   []string
		uid    int
		code   int
		stdout string
		stderr string
	}{
		{"version", []string{"version"}, 1000, 0, "aken dev (", ""},
		{"help", []string{"help"}, 1000, 0, usage, ""},
		{"long help", []string{"--help"}, 1000, 0, usage, ""},
		{"short help", []string{"-h"}, 1000, 0, usage, ""},
		{"no args", nil, 1000, 2, "", usage},
		{"unknown", []string{"bogus"}, 1000, 2, "", "aken: unknown command \"bogus\"\nRun \"aken help\" for usage.\n"},
		{"collect help", []string{"collect", "--help"}, 1000, 0, collectUsage, ""},
		{"root help", []string{"collect", "--help"}, 0, 0, collectUsage, ""},
		{"short collect help", []string{"collect", "-h"}, 0, 0, collectUsage, ""},
		{"root", []string{"collect"}, 0, 1, "", "aken: refusing to run as root"},
		{"collect", []string{"collect"}, 1000, 2, "", "aken: at least one source is required\n"},
		{"dry run", []string{"collect", "--dry-run", "--file", path, "--allow", dir}, 1000, 0, "aken collect --dry-run: nothing will be uploaded", ""},
		{"positional", []string{"collect", "extra"}, 1000, 2, "", "aken: unexpected positional arguments"},
		{"bad since", []string{"collect", "--file", path, "--since", "bad"}, 1000, 2, "", "aken: --since:"},
		{"bad until", []string{"collect", "--file", path, "--until", "bad"}, 1000, 2, "", "aken: --until:"},
		{"reversed time", []string{"collect", "--file", path, "--since", "0s", "--until", "1h"}, 1000, 2, "", "aken: --since must be before --until"},
		{"bad tail", []string{"collect", "--file", path, "--tail", "-1"}, 1000, 2, "", "aken: --tail"},
		{"bad ttl", []string{"collect", "--file", path, "--ttl", "25h"}, 1000, 2, "", "aken: --ttl"},
		{"bad retention", []string{"collect", "--file", path, "--retention", "-1h"}, 1000, 2, "", "aken: --retention"},
		{"bad unit", []string{"collect", "--unit", "--help"}, 1000, 2, "", "aken: invalid unit name"},
		{"bad container", []string{"collect", "--container", "a b"}, 1000, 2, "", "aken: invalid container name"},
		{"invalid flag", []string{"collect", "--bogus"}, 1000, 2, collectUsage, "flag provided but not defined: -bogus\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			geteuid = func() int { return tt.uid }
			var stdout, stderr bytes.Buffer
			if code := run(tt.args, &stdout, &stderr); code != tt.code {
				t.Fatalf("exit = %d, want %d", code, tt.code)
			}
			for _, output := range []struct{ name, got, want string }{
				{"stdout", stdout.String(), tt.stdout},
				{"stderr", stderr.String(), tt.stderr},
			} {
				if !strings.HasPrefix(output.got, output.want) || (output.want == "" && output.got != "") {
					t.Errorf("%s = %q, want prefix %q", output.name, output.got, output.want)
				}
			}
		})
	}
}
