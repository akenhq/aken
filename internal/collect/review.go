// SPDX-License-Identifier: Apache-2.0
package collect

import (
	"bufio"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/akenhq/aken/internal/redact"
	"github.com/akenhq/aken/protocol"
)

type screenSource struct {
	manifest protocol.ManifestSource
	lines    [][]byte
	flags    []redact.Flag
}
type screenData struct {
	sources                  []screenSource
	manifest                 protocol.Manifest
	options                  Options
	defaultRules, extraRules int
	rulesPath                string
	categories               []string
	linesCollapsed           int64
	idFlags                  int64
}

func sizeText(n int64) string {
	if n >= 1<<20 {
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
}
func durationText(d time.Duration) string {
	if d%time.Hour == 0 {
		return fmt.Sprintf("%dh", d/time.Hour)
	}
	if d%time.Minute == 0 {
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	return d.String()
}

func plural(n int64, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
func renderScreen(w io.Writer, s screenData) {
	first := "aken collect: review before anything leaves this machine"
	if s.options.DryRun {
		first = "aken collect --dry-run: nothing will be uploaded"
	}
	_, _ = fmt.Fprintf(w, "%s\n\nSources\n", first)
	width := 0
	var total int64
	for _, src := range s.sources {
		width = max(width, len(src.manifest.Name))
		total += src.manifest.Lines
	}
	for _, src := range s.sources {
		m := src.manifest
		note := m.Note
		if m.Since != "" {
			note = m.Since + " to " + m.Until
		}
		_, _ = fmt.Fprintf(w, "  %-*s%4d lines", width+2, m.Name, m.Lines)
		if note != "" {
			_, _ = fmt.Fprintf(w, "   %s", note)
		}
		_, _ = fmt.Fprintln(w)
	}
	_, _ = fmt.Fprintf(w, "\nRedaction   %d rules (%d default", s.manifest.Redaction.Rules, s.defaultRules)
	if s.rulesPath != "" {
		_, _ = fmt.Fprintf(w, ", %d from %s", s.extraRules, s.rulesPath)
	}
	_, _ = fmt.Fprint(w, ")")
	if len(s.options.Keep) > 0 {
		_, _ = fmt.Fprintf(w, "; kept: %s", strings.Join(s.options.Keep, ", "))
	}
	if len(s.options.KeepCategories) > 0 {
		_, _ = fmt.Fprintf(w, "; off: %s", strings.Join(s.options.KeepCategories, ", "))
	}
	_, _ = fmt.Fprintln(w)
	for _, c := range []string{"ip", "secret", "jwt", "key", "token", "email", "phone", "name", "address"} {
		count := s.manifest.Redaction.ByCategory[c]
		off := slices.Contains(s.options.KeepCategories, c)
		if c == "phone" || c == "name" || c == "address" {
			if !off && !slices.Contains(s.categories, c) {
				continue
			}
		}
		if off {
			_, _ = fmt.Fprintf(w, "  %-12s off\n", c)
		} else if count.Values == 0 {
			_, _ = fmt.Fprintf(w, "  %-12s 0\n", c)
		} else {
			_, _ = fmt.Fprintf(w, "  %-10s%4d %-6s in %4d %s", c, count.Values, plural(count.Values, "value", "values"), count.Lines, plural(count.Lines, "line", "lines"))
			if c == "key" && s.linesCollapsed > 0 {
				_, _ = fmt.Fprintf(w, " (%d lines collapsed)", s.linesCollapsed)
			}
			_, _ = fmt.Fprintln(w)
		}
	}
	_, _ = fmt.Fprintf(w, "  %d of %d lines changed\n\nFlags   ", s.manifest.Redaction.LinesRedacted, total)
	n := s.manifest.Redaction.Flags
	if n == 0 && s.idFlags == 0 {
		_, _ = fmt.Fprint(w, "none")
	} else {
		_, _ = fmt.Fprintf(w, "%d %s to inspect", n, plural(n, "string", "strings"))
		var locations []string
		for _, src := range s.sources {
			var nums []string
			seen := map[int]bool{}
			for _, f := range src.flags {
				if !f.IDShaped && !seen[f.Line] {
					if len(nums) < 20 {
						nums = append(nums, strconv.Itoa(f.Line))
					}
					seen[f.Line] = true
				}
			}
			if len(nums) > 0 {
				location := src.manifest.Name + " lines " + strings.Join(nums, ", ")
				if len(seen) > 20 {
					location += fmt.Sprintf(" and %d more", len(seen)-20)
				}
				locations = append(locations, location)
			}
		}
		if n > 0 {
			_, _ = fmt.Fprintf(w, ": %s", strings.Join(locations, "; "))
		}
		if s.idFlags > 0 {
			_, _ = fmt.Fprintf(w, "; %d hex %s or hashes not listed", s.idFlags, plural(s.idFlags, "id", "ids"))
		}
		_, _ = fmt.Fprint(w, ".")
		if n > 0 {
			_, _ = fmt.Fprint(w, " Press f to view the lines to inspect.")
		}
	}
	_, _ = fmt.Fprint(w, "\n\nUpload   ")
	if s.options.DryRun {
		_, _ = fmt.Fprint(w, "would upload ")
	}
	_, _ = fmt.Fprintf(w, "%d lines, %s, %d %s, TTL %s, relay %s\n", total, sizeText(s.manifest.TotalBytes), s.manifest.ChunkCount, plural(int64(s.manifest.ChunkCount), "chunk", "chunks"), durationText(s.options.TTL), s.options.RelayURL)
	retention := "kept forever"
	if s.options.Retention > 0 {
		retention = fmt.Sprintf("kept %g days", s.options.Retention.Hours()/24)
	}
	_, _ = fmt.Fprintf(w, "Local    %s/ (%s; includes the placeholder mapping)\n\n", filepath.Join(s.options.StateDir, "runs"), retention)
	if s.options.DryRun {
		_, _ = fmt.Fprint(w, "[v] view everything   [f] view flagged lines   [q] quit\n>\n")
	} else {
		_, _ = fmt.Fprint(w, "[s] send   [v] view everything   [f] view flagged lines   [a] abort\n>\n")
	}
}
func review(stdin *bufio.Reader, stdout io.Writer, s screenData, dryRun bool, pageLines int) (rune, error) {
	renderScreen(stdout, s)
	for {
		input, err := stdin.ReadString('\n')
		if err != nil {
			return 0, err
		}
		switch strings.TrimSpace(input) {
		case "s":
			if !dryRun {
				return 's', nil
			}
		case "a":
			if !dryRun {
				return 'a', nil
			}
		case "q":
			if dryRun {
				return 'q', nil
			}
		case "v", "f":
			flaggedOnly := strings.TrimSpace(input) == "f"
			var lines []string
			for _, src := range s.sources {
				flagged := map[int]bool{}
				for _, f := range src.flags {
					if !f.IDShaped {
						flagged[f.Line] = true
					}
				}
				for i, line := range src.lines {
					if flaggedOnly && !flagged[i+1] {
						continue
					}
					gutter := "|"
					if flagged[i+1] {
						gutter = "!"
					}
					lines = append(lines, fmt.Sprintf("%s:%d %s %s", src.manifest.Name, i+1, gutter, visible(line)))
				}
			}
			if flaggedOnly && len(lines) == 0 {
				_, _ = fmt.Fprintln(stdout, "no flagged lines")
			} else if err := page(stdin, stdout, lines, pageLines); err != nil {
				return 0, err
			}
		}
		_, _ = fmt.Fprint(stdout, ">\n")
	}
}
func visible(line []byte) string {
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
func page(stdin *bufio.Reader, stdout io.Writer, lines []string, pageLines int) error {
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
