// SPDX-License-Identifier: Apache-2.0
package collect

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/akenhq/aken/internal/redact"
	"github.com/akenhq/aken/protocol"
)

func testScreen() screenData {
	m := protocol.ManifestSource{Name: "unit:api", Kind: "unit", Target: "api", Lines: 2, Since: "2026-09-12T13:04:11Z", Until: "2026-09-12T14:04:11Z"}
	return screenData{
		sources:  []screenSource{{manifest: m, lines: [][]byte{[]byte("hello <ip#1>"), []byte("aB3dE5gH7jK9mN1pQ2sT4")}, flags: []redact.Flag{{Line: 2, Value: "aB3dE5gH7jK9mN1pQ2sT4"}}}},
		manifest: protocol.Manifest{TotalBytes: 1024, ChunkCount: 1, Redaction: protocol.RedactionSummary{Rules: 12, LinesRedacted: 1, Flags: 1, ByCategory: map[string]protocol.CategoryCount{"ip": {Values: 1, Lines: 1}}}},
		options:  Options{Keep: []string{"10.0.0.5"}, KeepCategories: []string{"email"}, TTL: 4 * time.Hour, RelayURL: protocol.DefaultRelayURL, StateDir: "/var/lib/aken", Retention: 720 * time.Hour}, defaultRules: 12,
	}
}
func TestRenderScreen(t *testing.T) {
	s := testScreen()
	var out bytes.Buffer
	renderScreen(&out, s)
	want := `aken collect: review before anything leaves this machine

Sources
  unit:api     2 lines   2026-09-12T13:04:11Z to 2026-09-12T14:04:11Z

Redaction   12 rules (12 default); kept: 10.0.0.5; off: email
  ip           1 value  in    1 line
  secret       0
  jwt          0
  key          0
  token        0
  email        off
  1 of 2 lines changed

Flags   1 string to inspect: unit:api lines 2. Press f to view the lines to inspect.

Upload   2 lines, 1.0 KiB, 1 chunk, TTL 4h, relay https://relay.aken.dev
Local    /var/lib/aken/runs/ (kept 30 days; includes the placeholder mapping)

[s] send   [v] view everything   [f] view flagged lines   [a] abort
>
`
	if out.String() != want {
		t.Fatalf("screen =\n%s\nwant\n%s", out.String(), want)
	}
	s.options.DryRun = true
	out.Reset()
	renderScreen(&out, s)
	for _, text := range []string{"aken collect --dry-run: nothing will be uploaded", "Upload   would upload", "[v] view everything   [f] view flagged lines   [q] quit"} {
		if !strings.Contains(out.String(), text) {
			t.Errorf("missing %q", text)
		}
	}
}
func TestReview(t *testing.T) {
	for _, tt := range []struct {
		name, input  string
		dry          bool
		choice       rune
		want, absent string
	}{
		{"send", "s\n", false, 's', "review before", ""},
		{"abort", "a\n", false, 'a', "review before", ""},
		{"view", "v\n\ns\n", false, 's', "unit:api:1 | hello <ip#1>", ""},
		{"flags", "f\ns\n", false, 's', "unit:api:2 ! aB3dE5gH7jK9mN1pQ2sT4", "unit:api:1 |"},
		{"stop page", "v\nq\na\n", false, 'a', "-- more: Enter, q to stop --", "unit:api:2 !"},
		{"dry quit", "s\na\nq\n", true, 'q', "nothing will be uploaded", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := testScreen()
			s.options.DryRun = tt.dry
			var out bytes.Buffer
			choice, err := review(bufio.NewReader(strings.NewReader(tt.input)), &out, s, tt.dry, 1)
			if err != nil || choice != tt.choice || !strings.Contains(out.String(), tt.want) || tt.absent != "" && strings.Contains(out.String(), tt.absent) {
				t.Fatalf("choice %q, err %v, output %s", choice, err, out.String())
			}
		})
	}
	s := testScreen()
	s.sources[0].flags = nil
	var out bytes.Buffer
	if _, err := review(bufio.NewReader(strings.NewReader("f\ns\n")), &out, s, false, 40); err != nil || !strings.Contains(out.String(), "no flagged lines\n>\n") {
		t.Fatalf("empty flags: %s, %v", out.String(), err)
	}
	if _, err := review(bufio.NewReader(strings.NewReader("")), &out, s, false, 40); err == nil {
		t.Fatal("expected input error")
	}
}
func TestPage(t *testing.T) {
	lines := make([]string, 41)
	for i := range lines {
		lines[i] = "line"
	}
	var out bytes.Buffer
	if err := page(bufio.NewReader(strings.NewReader("\n")), &out, lines, 0); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "-- more:") != 1 || strings.Count(out.String(), "line\n") != 41 {
		t.Fatal("wrong default page size")
	}
	out.Reset()
	if err := page(bufio.NewReader(strings.NewReader("")), &out, lines, 1); err == nil {
		t.Fatal("expected paging input error")
	}
}

func TestCollapsedLines(t *testing.T) {
	s := testScreen()
	s.manifest.Redaction.ByCategory["key"] = protocol.CategoryCount{Values: 1, Lines: 1}
	for _, count := range []int64{0, 2} {
		s.linesCollapsed = count
		var out bytes.Buffer
		renderScreen(&out, s)
		want := "  key          1 value  in    1 line"
		if count > 0 {
			want += " (2 lines collapsed)"
		}
		if !strings.Contains(out.String(), want+"\n") {
			t.Fatalf("screen = %s", out.String())
		}
	}
}

func TestVisible(t *testing.T) {
	for _, tt := range []struct{ input, want string }{
		{"plain\t café 世界 😀\u00a0", "plain\t café 世界 😀\u00a0"},
		{"\x00\x1b\r\n\x7f\xff\xc0", `\x00\x1b\x0d\x0a\x7f\xff\xc0`},
		{"\u0080\u009f\u202a\u202b\u202c\u202d\u202e", `\u{80}\u{9f}\u{202a}\u{202b}\u{202c}\u{202d}\u{202e}`},
		{"\u2066\u2067\u2068\u2069\u200b\ufeff", `\u{2066}\u{2067}\u{2068}\u{2069}\u{200b}\u{feff}`},
		{"\ufffd\xe2\x82", "\ufffd" + `\xe2\x82`},
	} {
		if got := visible([]byte(tt.input)); got != tt.want {
			t.Errorf("visible(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestReviewEscapes(t *testing.T) {
	original := []byte("\x1b[2J\r\u202e\xff aB3dE5gH7jK9mN1pQ2sT4")
	e, err := redact.Compile(redact.File{Version: 1}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	result := e.Redact([][]byte{bytes.Clone(original)})
	s := testScreen()
	s.sources[0].lines, s.sources[0].flags = result.Lines, result.Flags
	for _, command := range []string{"v", "f"} {
		var out bytes.Buffer
		_, err := review(bufio.NewReader(strings.NewReader(command+"\ns\n")), &out, s, false, 40)
		if err != nil || !strings.Contains(out.String(), `unit:api:1 ! \x1b[2J\x0d\u{202e}\xff aB3dE5gH7jK9mN1pQ2sT4`) {
			t.Fatalf("viewer = %q, %v", out.String(), err)
		}
		if bytes.Contains(out.Bytes(), original) || !bytes.Equal(result.Lines[0], original) {
			t.Fatal("viewer exposed controls or changed artifact bytes")
		}
	}
}

func TestFlagsScreenAndView(t *testing.T) {
	for _, tt := range []struct {
		inspect int
		ids     int64
		want    string
	}{
		{0, 0, "Flags   none"},
		{0, 1, "Flags   0 strings to inspect; 1 hex id or hashes not listed."},
		{25, 2, "Flags   25 strings to inspect: unit:api lines 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20 and 5 more; unit:worker lines 1; 2 hex ids or hashes not listed. Press f to view the lines to inspect."},
	} {
		s := testScreen()
		s.sources[0].lines, s.sources[0].flags = nil, nil
		for i := 1; i <= tt.inspect; i++ {
			s.sources[0].lines = append(s.sources[0].lines, []byte(fmt.Sprintf("inspect %d", i)))
			s.sources[0].flags = append(s.sources[0].flags, redact.Flag{Line: i, Value: fmt.Sprint(i)}, redact.Flag{Line: i, Value: "duplicate"})
		}
		for i := int64(0); i < tt.ids; i++ {
			s.sources[0].lines = append(s.sources[0].lines, []byte("id only"))
			s.sources[0].flags = append(s.sources[0].flags, redact.Flag{Line: len(s.sources[0].lines), IDShaped: true})
		}
		if tt.inspect > 0 {
			s.sources = append(s.sources, screenSource{manifest: protocol.ManifestSource{Name: "unit:worker"}, lines: [][]byte{[]byte("inspect again")}, flags: []redact.Flag{{Line: 1}}})
		}
		s.manifest.Redaction.Flags, s.idFlags = int64(tt.inspect), tt.ids
		for _, command := range []string{"f", "v"} {
			var out bytes.Buffer
			_, err := review(bufio.NewReader(strings.NewReader(command+"\ns\n")), &out, s, false, 100)
			if err != nil || !strings.Contains(out.String(), tt.want+"\n") {
				t.Fatalf("screen = %s, error = %v", out.String(), err)
			}
			if strings.Contains(out.String(), "id only") != (command == "v" && tt.ids > 0) {
				t.Fatalf("incorrect id filtering: %s", out.String())
			}
			if tt.inspect > 0 && !strings.Contains(out.String(), "unit:api:25 ! inspect 25") {
				t.Fatalf("viewer truncated inspected lines: %s", out.String())
			}
		}
	}
}
