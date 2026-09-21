// SPDX-License-Identifier: Apache-2.0
package serve

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/akenhq/aken/internal/redact"
	"github.com/akenhq/aken/internal/screen"
	"github.com/akenhq/aken/protocol"
)

func visible(s string) string { return screen.Visible([]byte(s)) }

// verdict is what the person at the terminal answered on the approval screen.
type verdict int

const (
	verdictDeny verdict = iota
	verdictApprove
	verdictOnce
)

// widening is a row whose path lies outside the scope, with the two grants that
// would let it run: dirs, added to the scope until the session ends, and paths,
// the single names this job alone needs. paths is empty when the row cannot be
// served by names alone, which is why reason then says what it needs instead.
type widening struct {
	row         int
	dirs, paths []string
	reason      string
}

// grantNote names a grant, and the path it resolves to when a link makes the two
// differ, so neither form is a surprise after the answer.
func grantNote(grant []string) string {
	if len(grant) > 1 {
		return grant[0] + " (resolves to " + grant[1] + ")"
	}
	return grant[0]
}

// onceReason returns the reason no row can be allowed once, or "" when every row
// can be.
func onceReason(widenings []widening) string {
	for _, w := range widenings {
		if len(w.paths) == 0 {
			return w.reason
		}
	}
	return ""
}

func approve(stdin *bufio.Reader, stdout io.Writer, job protocol.Job, jobs []preparedJob, widenings []widening, scope []string, pageLines int) (verdict, error) {
	for {
		title := job.Name
		if job.Name == "plan" {
			title = fmt.Sprintf("plan of %d reads", len(jobs))
		}
		_, _ = fmt.Fprintf(stdout, "Job %s from the agent: %s\n\n", visible(job.ID), visible(title))
		for i, p := range jobs {
			marker := " "
			switch {
			case p.sensitive:
				marker = "!"
			case p.widening:
				marker = "+"
			}
			_, _ = fmt.Fprintf(stdout, "  %d  %-10s%s %s", i+1, p.job.Name, marker, visible(p.target))
			if p.description != "" {
				_, _ = fmt.Fprintf(stdout, "  %s", visible(p.description))
			}
			_, _ = fmt.Fprintln(stdout)
		}
		once := len(widenings) > 0 && onceReason(widenings) == ""
		if len(widenings) > 0 {
			_, _ = fmt.Fprintf(stdout, "\nOutside the scope (%s). Approving adds these directories\nto the scope until the session ends:\n\n", visible(strings.Join(scope, ", ")))
			for _, w := range widenings {
				_, _ = fmt.Fprintf(stdout, "  row %d  %s\n", w.row, visible(grantNote(w.dirs)))
			}
			if once {
				_, _ = fmt.Fprint(stdout, "\nAllowing once adds only these paths, and only for this job:\n\n")
				for _, w := range widenings {
					_, _ = fmt.Fprintf(stdout, "  row %d  %s\n", w.row, visible(grantNote(w.paths)))
				}
			} else {
				_, _ = fmt.Fprintf(stdout, "\nThis job cannot be allowed once: %s.\n", visible(onceReason(widenings)))
			}
		}
		if once {
			_, _ = fmt.Fprint(stdout, "\n[a] approve   [o] allow once   [d] deny   [v] view params\n>\n")
		} else {
			_, _ = fmt.Fprint(stdout, "\n[a] approve   [d] deny   [v] view params\n>\n")
		}
		for {
			input, err := stdin.ReadString('\n')
			if err != nil {
				return verdictDeny, err
			}
			switch answer := strings.TrimSpace(input); {
			case answer == "a":
				return verdictApprove, nil
			case answer == "o" && once:
				return verdictOnce, nil
			case answer == "d":
				return verdictDeny, nil
			case answer == "v":
				var out bytes.Buffer
				if err := json.Indent(&out, job.Params, "", "  "); err != nil {
					return verdictDeny, err
				}
				lines := strings.Split(out.String(), "\n")
				for i, line := range lines {
					lines[i] = visible(line)
				}
				lines = append(lines, "Resolved paths:")
				for _, p := range jobs {
					for _, path := range p.paths {
						lines = append(lines, visible(path))
					}
				}
				if err := screen.Page(stdin, stdout, lines, pageLines); err != nil {
					return verdictDeny, err
				}
			default:
				_, _ = fmt.Fprint(stdout, ">\n")
				continue
			}
			break
		}
	}
}

func reviewFlags(stdin *bufio.Reader, stdout io.Writer, p preparedJob, result protocol.Result, flags []redact.Flag, now time.Time) (bool, error) {
	_, _ = fmt.Fprintf(stdout, "%s  %s %s  %d lines, %d strings to inspect\n", now.UTC().Format("15:04:05Z"), p.job.Name, visible(p.target), len(result.Lines), result.Redaction.Flags)
	seen := map[int]bool{}
	for _, flag := range flags {
		if flag.IDShaped || flag.Line < 1 || flag.Line > len(result.Lines) || seen[flag.Line] {
			continue
		}
		seen[flag.Line] = true
		line := result.Lines[flag.Line-1]
		location := fmt.Sprintf("%s:%d", filepath.Base(p.target), flag.Line)
		if p.job.Name == "read_file" || p.job.Name == "tail" {
			if num, text, ok := strings.Cut(line, ": "); ok {
				location = filepath.Base(p.target) + ":" + num
				line = text
			}
		}
		_, _ = fmt.Fprintf(stdout, "  %s ! %s\n", visible(location), visible(line))
	}
	_, _ = fmt.Fprint(stdout, "[s] send   [d] drop\n>\n")
	for {
		input, err := stdin.ReadString('\n')
		if err != nil {
			return false, err
		}
		switch strings.TrimSpace(input) {
		case "s":
			return true, nil
		case "d":
			return false, nil
		}
		_, _ = fmt.Fprint(stdout, ">\n")
	}
}
