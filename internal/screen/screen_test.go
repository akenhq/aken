// SPDX-License-Identifier: Apache-2.0
package screen

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

func TestPage(t *testing.T) {
	lines := make([]string, 41)
	for i := range lines {
		lines[i] = "line"
	}
	var out bytes.Buffer
	if err := Page(bufio.NewReader(strings.NewReader("\n")), &out, lines, 0); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "-- more:") != 1 || strings.Count(out.String(), "line\n") != 41 {
		t.Fatal("wrong default page size")
	}
	out.Reset()
	if err := Page(bufio.NewReader(strings.NewReader("")), &out, lines, 1); err == nil {
		t.Fatal("expected paging input error")
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
		if got := Visible([]byte(tt.input)); got != tt.want {
			t.Errorf("Visible(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
