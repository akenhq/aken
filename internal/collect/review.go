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

	"github.com/akenhq/aken/internal/redact"
	"github.com/akenhq/aken/internal/screen"
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
			_, _ = fmt.Fprintf(w, "  %-10s%4d %-6s in %4d %s", c, count.Values, screen.Plural(count.Values, "value", "values"), count.Lines, screen.Plural(count.Lines, "line", "lines"))
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
		_, _ = fmt.Fprintf(w, "%d %s to inspect", n, screen.Plural(n, "string", "strings"))
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
			_, _ = fmt.Fprintf(w, "; %d hex %s or hashes not listed", s.idFlags, screen.Plural(s.idFlags, "id", "ids"))
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
	_, _ = fmt.Fprintf(w, "%d lines, %s, %d %s, TTL %s, relay %s\n", total, screen.SizeText(s.manifest.TotalBytes), s.manifest.ChunkCount, screen.Plural(int64(s.manifest.ChunkCount), "chunk", "chunks"), screen.DurationText(s.options.TTL), s.options.RelayURL)
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
					lines = append(lines, fmt.Sprintf("%s:%d %s %s", src.manifest.Name, i+1, gutter, screen.Visible(line)))
				}
			}
			if flaggedOnly && len(lines) == 0 {
				_, _ = fmt.Fprintln(stdout, "no flagged lines")
			} else if err := screen.Page(stdin, stdout, lines, pageLines); err != nil {
				return 0, err
			}
		}
		_, _ = fmt.Fprint(stdout, ">\n")
	}
}
