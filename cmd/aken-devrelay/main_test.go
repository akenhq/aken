// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	for _, tt := range []struct {
		name           string
		args           []string
		code           int
		stdout, stderr string
	}{
		{"version", []string{"version"}, 0, "aken-devrelay dev (", ""},
		{"help", []string{"help"}, 0, usage, ""},
		{"short help", []string{"-h"}, 0, usage, ""},
		{"long help", []string{"--help"}, 0, usage, ""},
		{"no args", nil, 2, "", usage},
		{"unknown", []string{"bogus"}, 2, "", "aken-devrelay: unknown command \"bogus\"\nRun \"aken-devrelay help\" for usage.\n"},
		{"serve help", []string{"serve", "--help"}, 0, serveUsage, ""},
		{"invalid flag", []string{"serve", "--bogus"}, 2, serveUsage, "flag provided but not defined: -bogus\n"},
		{"unexpected argument", []string{"serve", "extra"}, 2, "", "aken-devrelay: unexpected arguments\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(tt.args, &stdout, &stderr); code != tt.code {
				t.Fatalf("exit = %d, want %d", code, tt.code)
			}
			for _, output := range []struct{ name, got, want string }{
				{"stdout", stdout.String(), tt.stdout}, {"stderr", stderr.String(), tt.stderr},
			} {
				if !strings.HasPrefix(output.got, output.want) || (output.want == "" && output.got != "") {
					t.Errorf("%s = %q, want prefix %q", output.name, output.got, output.want)
				}
			}
		})
	}
}
