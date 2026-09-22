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
		{"long version", []string{"--version"}, 1000, 0, "aken dev (", ""},
		{"short version", []string{"-V"}, 1000, 0, "aken dev (", ""},
		{"help collect", []string{"help", "collect"}, 0, 0, collectUsage, ""},
		{"help serve", []string{"help", "serve"}, 0, 0, serveUsage, ""},
		{"bad category", []string{"collect", "--unit", "api", "--keep-category", "X"}, 1000, 2, "", "aken: unknown redaction category \"X\"; use one of: secret token jwt key ip email phone name address\n"},
		{"relative allow", []string{"collect", "--unit", "api", "--allow", "logs"}, 1000, 2, "", "aken: --allow directory must be absolute\n"},
		{"bad relay", []string{"collect", "--unit", "api", "--relay", "http://example.com"}, 1000, 2, "", "aken: invalid relay URL"},
		{"version", []string{"version"}, 1000, 0, "aken dev (", ""},
		{"help", []string{"help"}, 1000, 0, usage, ""},
		{"long help", []string{"--help"}, 1000, 0, usage, ""},
		{"short help", []string{"-h"}, 1000, 0, usage, ""},
		{"no args", nil, 1000, 2, "", usage},
		{"unknown", []string{"bogus"}, 1000, 2, "", "aken: unknown command \"bogus\"\nRun \"aken help\" for usage.\n"},
		{"collect help", []string{"collect", "--help"}, 1000, 0, collectUsage, ""},
		{"root help", []string{"collect", "--help"}, 0, 0, collectUsage, ""},
		{"short collect help", []string{"collect", "-h"}, 0, 0, collectUsage, ""},
		{"root", []string{"collect", "--unit", "api"}, 0, 1, "", "aken: refusing to run as root"},
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
		{"invalid flag", []string{"collect", "--bogus"}, 1000, 2, "", "aken: flag provided but not defined: -bogus. Run \"aken collect --help\".\n"},
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

func TestServeFlags(t *testing.T) {
	original := geteuid
	t.Cleanup(func() { geteuid = original })
	for _, tt := range []struct {
		name      string
		args      []string
		uid, code int
		want      string
	}{
		{"help", []string{"--help"}, 1000, 0, serveUsage},
		{"root bad flags", []string{"--bogus"}, 0, 2, "aken: flag provided but not defined: -bogus. Run \"aken serve --help\".\n"},
		{"root help", []string{"--help"}, 0, 0, serveUsage},
		{"root level", []string{"--level", "5"}, 0, 2, "aken: --level must be 0 or 1\n"},
		{"root", nil, 0, 1, "aken: refusing to run as root. The installed aken command switches to the aken user for you: run sudo aken serve. From a source build, run it as any unprivileged user.\n"},
		{"unknown flag", []string{"--exec"}, 1000, 2, "flag provided but not defined"},
		{"positional", []string{"extra"}, 1000, 2, "unexpected positional arguments"},
		{"bad level", []string{"--level", "2"}, 1000, 2, "--level must be"},
		{"negative level", []string{"--level", "-1"}, 1000, 2, "--level must be"},
		{"long ttl", []string{"--ttl", "25h"}, 1000, 2, "--ttl must be"},
		{"zero ttl", []string{"--ttl", "0"}, 1000, 2, "--ttl must be"},
		{"bad ttl", []string{"--ttl", "x"}, 1000, 2, "invalid value"},
		{"negative retention", []string{"--retention", "-1h"}, 1000, 2, "--retention must not"},
		{"relative scope", []string{"--allow", "logs"}, 1000, 2, "--allow directory must be absolute"},
		{"category", []string{"--keep-category", "X"}, 1000, 2, "aken: unknown redaction category \"X\"; use one of: secret token jwt key ip email phone name address\n"},
		{"bad relay", []string{"--relay", "http://example.com"}, 1000, 2, "invalid relay URL"},
		{"terminal", nil, 1000, 1, "aken: serve needs a terminal"},
		{"all flags", []string{"--level", "0", "--ttl", "24h", "--relay", "https://relay.aken.dev", "--allow", "/tmp", "--allow", "/srv", "--keep", "value", "--keep-category", "email", "--rules", "/tmp/rules", "--state-dir", "/tmp/state", "--retention", "0"}, 1000, 1, "aken: serve needs a terminal"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			geteuid = func() int { return tt.uid }
			var out, errs bytes.Buffer
			code := run(append([]string{"serve"}, tt.args...), &out, &errs)
			if code != tt.code || !strings.Contains(out.String()+errs.String(), tt.want) || (code != 0 && out.Len() != 0) || (code == 0 && errs.Len() != 0) {
				t.Fatalf("exit %d; stdout %q; stderr %q", code, out.String(), errs.String())
			}
		})
	}
}
