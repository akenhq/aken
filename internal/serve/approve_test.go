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

// answers reads the keys a prompt is given from text, one line per key, as a
// descriptor that is not a terminal does.
func answers(text string) *screen.Input {
	return screen.NewInput(strings.NewReader(text), -1, false)
}

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
	// Without a row outside the scope there is nothing to allow once, so o is not
	// offered and is answered like any other key the screen does not take.
	for input, expected := range map[string]verdict{"a\n": verdictApprove, "d\n": verdictDeny, "o\na\n": verdictApprove} {
		var out bytes.Buffer
		got, err := approve(answers(input), &out, job("j7", "plan", protocol.PlanParams{}), rows, nil, nil, 40)
		if err != nil || got != expected || !strings.HasPrefix(out.String(), want) {
			t.Fatalf("answered %v, %v, screen:\n%s", got, err, &out)
		}
	}
	var out bytes.Buffer
	got, err := approve(answers("v\na\n"), &out, job("j7", "read_file", protocol.ReadFileParams{Path: "/var/log/a"}), rows[:1], nil, nil, 40)
	if err != nil || got != verdictApprove || strings.Count(out.String(), "Job j7 from the agent: read_file") != 2 || !strings.Contains(out.String(), "{\n  \"path\": \"/var/log/a\"\n}") {
		t.Fatal(got, err, out.String())
	}
	if _, err := approve(answers(""), &out, job("j7", "read_file", nil), rows[:1], nil, nil, 40); err == nil {
		t.Fatal("ignored EOF")
	}
}

func TestApprovalScreenWidening(t *testing.T) {
	rows := []preparedJob{
		{job: protocol.Job{Name: "read_file"}, target: "/var/log/app.log", description: "lines 1-200"},
		{job: protocol.Job{Name: "list_dir"}, target: "/srv/app/logs", widening: true},
		{job: protocol.Job{Name: "read_file"}, target: "/srv/app/.env", description: "lines 1-50", sensitive: true, widening: true},
	}
	widenings := []widening{
		{row: 2, dirs: []string{"/srv/app/logs"}, paths: []string{"/srv/app/logs"}},
		{row: 3, dirs: []string{"/srv/app", "/data/app"}, paths: []string{"/srv/app/.env", "/data/app/.env"}},
	}
	head := "Job j7 from the agent: plan of 3 reads\n\n" +
		"  1  read_file   /var/log/app.log  lines 1-200\n" +
		"  2  list_dir  + /srv/app/logs\n" +
		"  3  read_file ! /srv/app/.env  lines 1-50\n" +
		"\nOutside the scope (/var/log). Approving adds these directories\nto the scope until the session ends:\n\n" +
		"  row 2  /srv/app/logs\n" +
		"  row 3  /srv/app (resolves to /data/app)\n"
	want := head +
		"\nAllowing once adds only these paths, and only for this job:\n\n" +
		"  row 2  /srv/app/logs\n" +
		"  row 3  /srv/app/.env (resolves to /data/app/.env)\n" +
		"\n[a] approve   [o] allow once   [d] deny   [v] view params\n>\n"
	for input, expected := range map[string]verdict{"a\n": verdictApprove, "o\n": verdictOnce, "d\n": verdictDeny} {
		var out bytes.Buffer
		got, err := approve(answers(input), &out, job("j7", "plan", protocol.PlanParams{}), rows, widenings, []string{"/var/log"}, 40)
		if err != nil || got != expected || out.String() != want {
			t.Fatalf("answered %v, %v, screen:\n%s", got, err, &out)
		}
	}
	// A glob row cannot be served by names, so the whole job loses the once option and
	// o is answered like any other key the screen does not take.
	widenings[0].paths, widenings[0].reason = nil, "a glob needs its directory"
	want = head + "\nThis job cannot be allowed once: a glob needs its directory.\n" +
		"\n[a] approve   [d] deny   [v] view params\n>\n"
	var out bytes.Buffer
	got, err := approve(answers("o\nd\n"), &out, job("j7", "plan", protocol.PlanParams{}), rows, widenings, []string{"/var/log"}, 40)
	if err != nil || got != verdictDeny || out.String() != want+">\n" {
		t.Fatalf("answered %v, %v, screen:\n%s", got, err, &out)
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
		got, err := approve(answers(input), &out, job("j1", name, map[string]any{}), rows, nil, nil, 10)
		if err != nil || got != verdictApprove || strings.Count(out.String(), "-- more: Enter, q to stop --") != 20 {
			t.Fatalf("paged approval: %v, %v, %s", got, err, &out)
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
	r := protocol.Result{Lines: []string{"41: \x1b[2J‮\xff flagged", "42: hex"}, Redaction: protocol.ResultRedaction{Flags: 1}}
	flags := []redact.Flag{{Line: 1}, {Line: 1}, {Line: 2, IDShaped: true}}
	for _, input := range []string{"s\n", "d\n"} {
		var out bytes.Buffer
		ok, err := reviewFlags(answers(input), &out, p, r, flags, time.Date(2026, 9, 13, 14, 2, 7, 0, time.UTC))
		if err != nil || ok != (input == "s\n") || strings.Count(out.String(), "app.log:41 !") != 1 || strings.Contains(out.String(), "42") || !strings.Contains(out.String(), `\x1b[2J\u{202e}\xff`) {
			t.Fatal(ok, err, out.String())
		}
	}
}
