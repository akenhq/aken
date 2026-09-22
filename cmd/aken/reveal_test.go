// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestReveal(t *testing.T) {
	state := t.TempDir()
	write := func(path, data string) {
		t.Helper()
		path = filepath.Join(state, path)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o400); err != nil {
			t.Fatal(err)
		}
	}
	write("runs/20260920T080000Z-1a2b3c4d/run.json", `{"session_id": "1a2b3c4d5e6f"}`)
	write("runs/20260920T080000Z-1a2b3c4d/mapping.json", `{"<ip#10>": "203.0.113.10", "<ip#2>": "203.0.113.2", "<email#1>": "a@example.com"}`)
	write("sessions/20260921T090000Z-1a2b3c4e/session.json", `{"session_id": "1a2b3c4e0000"}`)
	write("sessions/20260921T090000Z-1a2b3c4e/mapping.json", `{"<secret#1>": "hunter2\u001b[2J"}`)
	if err := os.MkdirAll(filepath.Join(state, "sessions/20260921T100000Z-99999999"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name           string
		args           []string
		code           int
		stdout, stderr string
	}{
		{"help", []string{"reveal", "--help"}, 0, revealUsage, ""},
		{"list", []string{"reveal"}, 0, "20260921T100000Z-99999999\tserve\t\n20260921T090000Z-1a2b3c4e\tserve\t1a2b3c4e0000\n20260920T080000Z-1a2b3c4d\tcollect\t1a2b3c4d5e6f\n", ""},
		{"all by name", []string{"reveal", "20260920T080000Z-1a2b3c4d"}, 0, "<email#1>\ta@example.com\n<ip#2>\t203.0.113.2\n<ip#10>\t203.0.113.10\n", ""},
		{"by session id", []string{"reveal", "1a2b3c4d5e", "<ip#2>", "email#1"}, 0, "<ip#2>\t203.0.113.2\n<email#1>\ta@example.com\n", ""},
		{"control characters escaped", []string{"reveal", "1a2b3c4e"}, 0, "<secret#1>\thunter2\\x1b[2J\n", ""},
		{"unknown placeholder", []string{"reveal", "1a2b3c4d", "ip#3", "ip#2"}, 1, "<ip#2>\t203.0.113.2\n", "aken: <ip#3> is not in 20260920T080000Z-1a2b3c4d\n"},
		{"short prefix", []string{"reveal", "1a2b"}, 1, "", "aken: no local copy matches \"1a2b\"; run \"aken reveal\" to list them\n"},
		{"no mapping", []string{"reveal", "20260921T100000Z-99999999"}, 1, "", "aken: 20260921T100000Z-99999999 has no mapping.json; nothing was redacted yet\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			args := append([]string{tt.args[0], "--state-dir", state}, tt.args[1:]...)
			if code := run(args, &stdout, &stderr); code != tt.code {
				t.Fatalf("exit = %d, want %d; stderr %q", code, tt.code, stderr.String())
			}
			if got := stdout.String(); got != tt.stdout {
				t.Errorf("stdout = %q, want %q", got, tt.stdout)
			}
			if got := stderr.String(); !bytes.HasPrefix([]byte(got), []byte(tt.stderr)) || (tt.stderr == "" && got != "") {
				t.Errorf("stderr = %q, want prefix %q", got, tt.stderr)
			}
		})
	}
	t.Run("ambiguous prefix", func(t *testing.T) {
		write("runs/20260922T080000Z-1a2b3c4d/run.json", `{"session_id": "1a2b3c4d9999"}`)
		var stdout, stderr bytes.Buffer
		if code := run([]string{"reveal", "--state-dir", state, "1a2b3c4d"}, &stdout, &stderr); code != 1 {
			t.Fatalf("exit = %d, want 1", code)
		}
		want := "aken: \"1a2b3c4d\" matches 20260922T080000Z-1a2b3c4d, 20260920T080000Z-1a2b3c4d; use a directory name\n"
		if stderr.String() != want {
			t.Errorf("stderr = %q, want %q", stderr.String(), want)
		}
	})
}
