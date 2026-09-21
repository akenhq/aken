// SPDX-License-Identifier: Apache-2.0
package serve

import (
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

var approvalChoices = []screen.Choice{{Key: 'a', Label: "approve"}, {Key: 'd', Label: "deny"}, {Key: 'v', Label: "view params"}}
var flagChoices = []screen.Choice{{Key: 's', Label: "send"}, {Key: 'd', Label: "drop"}}

func approve(stdin *screen.Input, stdout io.Writer, job protocol.Job, jobs []preparedJob, pageLines int) (bool, error) {
	for {
		title := job.Name
		if job.Name == "plan" {
			title = fmt.Sprintf("plan of %d reads", len(jobs))
		}
		_, _ = fmt.Fprintf(stdout, "Job %s from the agent: %s\n\n", visible(job.ID), visible(title))
		for i, p := range jobs {
			marker := " "
			if p.sensitive {
				marker = "!"
			}
			_, _ = fmt.Fprintf(stdout, "  %d  %-10s%s %s", i+1, p.job.Name, marker, visible(p.target))
			if p.description != "" {
				_, _ = fmt.Fprintf(stdout, "  %s", visible(p.description))
			}
			_, _ = fmt.Fprintln(stdout)
		}
		_, _ = fmt.Fprintln(stdout)
		screen.Choices(stdout, approvalChoices)
		key, err := screen.Ask(stdin, stdout, approvalChoices)
		if err != nil {
			return false, err
		}
		switch key {
		case 'a':
			return true, nil
		case 'd':
			return false, nil
		}
		// The params are paged, then the screen is drawn again so the answer
		// is given to the job description rather than to the last page.
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
	}
}

func reviewFlags(stdin *screen.Input, stdout io.Writer, p preparedJob, result protocol.Result, flags []redact.Flag, now time.Time) (bool, error) {
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
	screen.Choices(stdout, flagChoices)
	key, err := screen.Ask(stdin, stdout, flagChoices)
	if err != nil {
		return false, err
	}
	return key == 's', nil
}
