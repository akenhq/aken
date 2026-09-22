// SPDX-License-Identifier: Apache-2.0
package serve

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/akenhq/aken/internal/redact"
	"github.com/akenhq/aken/internal/screen"
	"github.com/akenhq/aken/protocol"
)

func TestApprovalScreen(t *testing.T) {
	rows := []preparedJob{
		{job: protocol.Job{Name: "read_file"}, target: "/var/log/nginx/error.log", description: "lines 1-200"},
		{job: protocol.Job{Name: "tail"}, target: "/var/log/app/app.log", description: "last 100 lines"},
		{job: protocol.Job{Name: "journal"}, target: "unit nginx.service", description: "2026-09-13T13:00:00Z to 2026-09-13T14:00:00Z  first 200 lines"},
		{job: protocol.Job{Name: "read_file"}, target: "/srv/app/.env", description: "lines 1-50", sensitive: true},
	}
	want := "Job j7 from the agent: plan of 4 reads\n\n" +
		"  1  read_file   /var/log/nginx/error.log  lines 1-200\n" +
		"  2  tail        /var/log/app/app.log  last 100 lines\n" +
		"  3  journal     unit nginx.service  2026-09-13T13:00:00Z to 2026-09-13T14:00:00Z  first 200 lines\n" +
		"  4  read_file ! /srv/app/.env  lines 1-50\n\n[a] approve   [d] deny   [v] view params\n>\n"
	for _, input := range []string{"a\n", "d\n"} {
		var out bytes.Buffer
		ok, err := approve(screen.NewInput(strings.NewReader(input), -1, false), &out, job("j7", "plan", protocol.PlanParams{}), rows, 40)
		if err != nil || ok != (input == "a\n") || out.String() != want {
			t.Fatalf("approved %v, %v, screen:\n%s", ok, err, &out)
		}
	}
	var out bytes.Buffer
	ok, err := approve(screen.NewInput(strings.NewReader("v\na\n"), -1, false), &out, job("j7", "read_file", protocol.ReadFileParams{Path: "/var/log/a"}), rows[:1], 40)
	if err != nil || !ok || strings.Count(out.String(), "Job j7 from the agent: read_file") != 2 || !strings.Contains(out.String(), "{\n  \"path\": \"/var/log/a\"\n}") {
		t.Fatal(ok, err, out.String())
	}
	if _, err := approve(screen.NewInput(strings.NewReader(""), -1, false), &out, job("j7", "read_file", nil), rows[:1], 40); err == nil {
		t.Fatal("ignored EOF")
	}
}
func TestApprovalResolvedPaths(t *testing.T) {
	paths := make([]string, 200)
	for i := range paths {
		paths[i] = fmt.Sprintf("/var/log/%03d\x1b\n.log", i)
	}
	for _, name := range []string{"search", "plan"} {
		rows := []preparedJob{{job: protocol.Job{Name: "search"}, paths: paths}}
		if name == "plan" {
			rows = append(rows, preparedJob{job: protocol.Job{Name: "tail"}, paths: []string{"/var/log/last.log"}})
		}
		var out bytes.Buffer
		input := "v\n" + strings.Repeat("\n", 20) + "a\n"
		ok, err := approve(screen.NewInput(strings.NewReader(input), -1, false), &out, job("j1", name, map[string]any{}), rows, 10)
		if err != nil || !ok || strings.Count(out.String(), "-- more: Enter, q to stop --") != 20 {
			t.Fatalf("paged approval: %v, %v, %s", ok, err, &out)
		}
		_, resolved, found := strings.Cut(out.String(), "{}\nResolved paths:\n")
		if !found {
			t.Fatal("missing paths after params")
		}
		for _, row := range rows {
			for _, path := range row.paths {
				if strings.Count(resolved, visible(path)+"\n") != 1 || strings.Contains(resolved, "\x1b") {
					t.Fatalf("missing or unsafe path %q", path)
				}
			}
		}
	}
}

func TestFlagReview(t *testing.T) {
	p := preparedJob{job: protocol.Job{Name: "tail"}, target: "/var/log/app/app.log"}
	r := protocol.Result{Lines: []string{"41: \x1b[2J\u202e\xff flagged", "42: hex"}, Redaction: protocol.ResultRedaction{Flags: 1}}
	flags := []redact.Flag{{Line: 1}, {Line: 1}, {Line: 2, IDShaped: true}}
	for _, input := range []string{"s\n", "d\n"} {
		var out bytes.Buffer
		ok, err := reviewFlags(screen.NewInput(strings.NewReader(input), -1, false), &out, p, r, flags, time.Date(2026, 9, 13, 14, 2, 7, 0, time.UTC))
		if err != nil || ok != (input == "s\n") || strings.Count(out.String(), "app.log:41 !") != 1 || strings.Contains(out.String(), "42") || !strings.Contains(out.String(), `\x1b[2J\u{202e}\xff`) {
			t.Fatal(ok, err, out.String())
		}
	}
}
