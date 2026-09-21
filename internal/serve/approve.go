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

// widening is a row whose path lies outside the scope, together with the
// directories approving it would add for the rest of the session.
type widening struct {
	row  int
	dirs []string
}

func (w widening) note() string {
	if len(w.dirs) > 1 {
		return w.dirs[0] + " (resolves to " + w.dirs[1] + ")"
	}
	return w.dirs[0]
}

func approve(stdin *bufio.Reader, stdout io.Writer, job protocol.Job, jobs []preparedJob, widenings []widening, scope []string, pageLines int) (bool, error) {
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
		if len(widenings) > 0 {
			_, _ = fmt.Fprintf(stdout, "\nOutside the scope (%s). Approving adds these directories\nto the scope until the session ends:\n\n", visible(strings.Join(scope, ", ")))
			for _, w := range widenings {
				_, _ = fmt.Fprintf(stdout, "  row %d  %s\n", w.row, visible(w.note()))
			}
		}
		_, _ = fmt.Fprint(stdout, "\n[a] approve   [d] deny   [v] view params\n>\n")
		for {
			input, err := stdin.ReadString('\n')
			if err != nil {
				return false, err
			}
			switch strings.TrimSpace(input) {
			case "a":
				return true, nil
			case "d":
				return false, nil
			case "v":
				var out bytes.Buffer
				if err := json.Indent(&out, job.Params, "", "  "); err != nil {
					return false, err
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
					return false, err
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
