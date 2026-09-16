// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	t.Setenv("AKEN_R2_ACCOUNT_ID", "")
	for _, tt := range []struct {
		name           string
		args           []string
		code           int
		stdout, stderr string
	}{
		{"version", []string{"version"}, 0, "aken-relay dev (", ""},
		{"help", []string{"help"}, 0, usage, ""},
		{"short help", []string{"-h"}, 0, usage, ""},
		{"long help", []string{"--help"}, 0, usage, ""},
		{"no args", nil, 2, "", usage},
		{"unknown", []string{"bogus"}, 2, "", "aken-relay: unknown command \"bogus\"\nRun \"aken-relay help\" for usage.\n"},
		{"serve help", []string{"serve", "--help"}, 0, serveUsage, ""},
		{"invalid flag", []string{"serve", "--bogus"}, 2, serveUsage, "flag provided but not defined: -bogus\n"},
		{"invalid integer", []string{"serve", "--creates-per-hour", "x"}, 2, serveUsage, "invalid value"},
		{"unexpected argument", []string{"serve", "extra"}, 2, "", "aken-relay: unexpected arguments\n"},
		{"store", []string{"serve", "--store", "disk"}, 2, "", "aken-relay: --store must be memory, dir or r2\n"},
		{"dir missing", []string{"serve", "--store", "dir"}, 2, "", "aken-relay: --data-dir is required with --store dir\n"},
		{"dir unexpected", []string{"serve", "--data-dir", "data"}, 2, "", "aken-relay: --data-dir needs --store dir\n"},
		{"allowance mode", []string{"serve", "--allowance-mode", "invalid"}, 2, "", "aken-relay: --allowance-mode must be off, observe or enforce\n"},
		{"allowance servers", []string{"serve", "--allowance-servers", "0"}, 2, "", "aken-relay: limits must be positive\n"},
		{"allowance window", []string{"serve", "--allowance-window", "-1h"}, 2, "", "aken-relay: limits must be positive\n"},
		{"requests", []string{"serve", "--requests-per-minute", "0"}, 2, "", "aken-relay: limits must be positive\n"},
		{"creates", []string{"serve", "--creates-per-hour", "-1"}, 2, "", "aken-relay: limits must be positive\n"},
		{"capacity", []string{"serve", "--max-live-sessions", "0"}, 2, "", "aken-relay: limits must be positive\n"},
		{"environment", []string{"serve", "--store", "r2"}, 1, "", "aken-relay: missing AKEN_R2_ACCOUNT_ID\n"},
		{"listen", []string{"serve", "--listen", "invalid"}, 1, "", "aken-relay: address invalid:"},
		{"admin help", []string{"admin", "--help"}, 0, adminUsage, ""},
		{"admin missing", []string{"admin"}, 2, "", adminUsage},
		{"admin unknown", []string{"admin", "bogus", "id"}, 2, "", adminUsage},
		{"admin id missing", []string{"admin", "delete-session"}, 2, "", adminUsage},
		{"admin id invalid", []string{"admin", "delete-session", "invalid"}, 2, "", "aken-relay: invalid session id\n"},
		{"admin env", []string{"admin", "delete-session", strings.Repeat("a", 32)}, 1, "", "aken-relay: missing AKEN_R2_ACCOUNT_ID\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(tt.args, &stdout, &stderr); code != tt.code {
				t.Fatalf("exit = %d, want %d", code, tt.code)
			}
			for _, out := range []struct{ name, got, want string }{{"stdout", stdout.String(), tt.stdout}, {"stderr", stderr.String(), tt.stderr}} {
				if !strings.HasPrefix(out.got, out.want) || (out.want == "" && out.got != "") {
					t.Errorf("%s = %q, want prefix %q", out.name, out.got, out.want)
				}
			}
		})
	}
}
