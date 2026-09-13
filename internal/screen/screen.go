// SPDX-License-Identifier: Apache-2.0
package screen

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

func SizeText(n int64) string {
	if n >= 1<<20 {
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
}
func DurationText(d time.Duration) string {
	if d%time.Hour == 0 {
		return fmt.Sprintf("%dh", d/time.Hour)
	}
	if d%time.Minute == 0 {
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	return d.String()
}

func Plural(n int64, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
func Visible(line []byte) string {
	var out strings.Builder
	for len(line) > 0 {
		r, size := utf8.DecodeRune(line)
		switch {
		case r == '\t':
			out.WriteRune(r)
		case r < 0x20 || r == 0x7f || r == utf8.RuneError && size == 1:
			fmt.Fprintf(&out, "\\x%02x", line[0])
		case !unicode.IsGraphic(r):
			out.WriteString("\\u{" + strconv.FormatInt(int64(r), 16) + "}")
		default:
			out.WriteRune(r)
		}
		line = line[size:]
	}
	return out.String()
}
func Page(stdin *bufio.Reader, stdout io.Writer, lines []string, pageLines int) error {
	if pageLines <= 0 {
		pageLines = 40
	}
	for i, line := range lines {
		_, _ = fmt.Fprintln(stdout, line)
		if (i+1)%pageLines == 0 && i+1 < len(lines) {
			_, _ = fmt.Fprintln(stdout, "-- more: Enter, q to stop --")
			input, err := stdin.ReadString('\n')
			if err != nil {
				return err
			}
			if strings.TrimSpace(input) == "q" {
				return nil
			}
		}
	}
	return nil
}
