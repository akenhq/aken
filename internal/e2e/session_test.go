// SPDX-License-Identifier: Apache-2.0
package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	akenmcp "github.com/akenhq/aken/internal/mcp"
	"github.com/akenhq/aken/internal/serve"
	"github.com/akenhq/aken/internal/session"
	"github.com/akenhq/aken/internal/source"
	"github.com/akenhq/aken/protocol"
	"github.com/akenhq/aken/relay"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const approvalFooter = "\n[a] approve   [d] deny   [v] view params\n>\n"
const flaggedValue = "aB3dE5gH7jK9mN1pQ2sT4vW6"

func TestLiveSession(t *testing.T) {
	t.Run("level 1", func(t *testing.T) {
		t.Log("1. Open the collector, authenticate the join, and connect the live MCP")
		h := newLiveSession(t, 1)
		cs := connect(t, &akenmcp.Server{SessionPath: h.sessionPath, Version: "test", Now: h.options.Now})
		if !strings.Contains(cs.InitializeResult().Instructions, "Every result is redacted with the same placeholders across the session.") {
			t.Fatal("missing live session instructions")
		}

		t.Log("2. Approve read_file and check redacted lines and metadata")
		await := h.call(t, cs, "read_file", map[string]any{"path": h.path, "from": 1, "to": 2})
		h.answer(t, "Job j1 from the agent: read_file\n\n  1  read_file   "+h.path+"  lines 1-2\n"+approvalFooter, "a")
		read := "1: request from <ip#1>\n2: request completed\n"
		checkLiveResult(t, await(), read, liveMeta("j1", "ok", 2, 1, 0, "3", ""))
		h.out.until(t, "14:00:00Z  read_file "+h.path+"  2 lines sent, 1 redacted (ip 1 values), 0 flags\n")

		t.Log("3. Deny a three-job plan on one approval screen")
		await = h.call(t, cs, "plan", map[string]any{"jobs": []map[string]any{
			{"name": "read_file", "params": map[string]any{"path": h.path, "from": 1, "to": 2}},
			{"name": "tail", "params": map[string]any{"path": h.path, "n": 1}},
			{"name": "list_dir", "params": map[string]any{"path": h.options.Allow[0]}},
		}})
		h.answer(t, "Job j2 from the agent: plan of 3 reads\n\n  1  read_file   "+h.path+"  lines 1-2\n  2  tail        "+h.path+"  last 1 lines\n  3  list_dir    "+h.options.Allow[0]+"\n"+approvalFooter, "d")
		plan := await()
		if !plan.IsError || len(plan.Content) != 6 {
			t.Fatalf("plan: isError=%v, content=%d", plan.IsError, len(plan.Content))
		}
		for i, name := range []string{"read_file", "tail", "list_dir"} {
			block := &sdk.CallToolResult{IsError: true, Content: plan.Content[2*i : 2*i+2]}
			checkLiveResult(t, block, fmt.Sprintf("## %d %s denied\ndenied: denied by user\n", i+1, name), liveMeta(fmt.Sprintf("j2.%d", i+1), "denied", 0, 0, 0, "", "denied by user"))
		}
		h.out.until(t, "14:00:00Z  list_dir "+h.options.Allow[0]+"  denied: denied by user\n")

		t.Log("4. Reject an outside-scope path whose directory cannot be added, without prompting")
		outsideDir := t.TempDir()
		outside := filepath.Join(outsideDir, "outside.log")
		writeSessionFile(t, outside, "request from 203.0.113.7\n")
		scope := "/var/log, " + h.options.Allow[0]
		missing := filepath.Join(outsideDir, "gone", "outside.log")
		refusal := "outside the scope (" + scope + "); it cannot be added to the scope: the directory does not exist"
		checkLiveResult(t, h.call(t, cs, "read_file", map[string]any{"path": missing})(), "rejected: "+refusal+"\n", liveMeta("j3", "rejected", 0, 0, 0, "", refusal))
		noApproval(t, h.out.until(t, "14:00:00Z  read_file "+missing+"  rejected: "+refusal+"\n"))

		sibling := filepath.Join(outsideDir, "sibling.log")
		writeSessionFile(t, sibling, "sibling request from 203.0.113.7\n")
		widening := func(id, path string) string {
			return "Job " + id + " from the agent: read_file\n\n  1  read_file + " + path + "  lines 1-1\n" +
				"\nOutside the scope (" + scope + "). Approving adds these directories\nto the scope until the session ends:\n\n" +
				"  row 1  " + outsideDir + "\n" +
				"\nAllowing once adds only these paths, and only for this job:\n\n" +
				"  row 1  " + path + "\n" +
				"\n[a] approve   [o] allow once   [d] deny   [v] view params\n>\n"
		}

		t.Log("5. Decline to add an outside-scope directory on the approval screen")
		await = h.call(t, cs, "read_file", map[string]any{"path": outside, "from": 1, "to": 1})
		h.answer(t, widening("j4", outside), "d")
		refused := "denied by user; nothing was added to the scope"
		checkLiveResult(t, await(), "denied: "+refused+"\n", liveMeta("j4", "denied", 0, 0, 0, "", refused))
		h.out.until(t, "14:00:00Z  read_file "+outside+"  denied: "+refused+"\n")
		if _, err := os.Stat(filepath.Join(h.auditPath, "results", "5.txt")); err != nil {
			t.Fatal("the declined read has no audit copy", err)
		}

		t.Log("6. Allow one path for one job and read it")
		await = h.call(t, cs, "read_file", map[string]any{"path": outside, "from": 1, "to": 1})
		h.answer(t, widening("j5", outside), "o")
		checkLiveResult(t, await(), "1: request from <ip#1>\n", liveMeta("j5", "ok", 1, 1, 0, "", ""))
		h.out.until(t, "14:00:00Z  scope    "+outside+" added for this job\n")
		h.out.until(t, "14:00:00Z  read_file "+outside+"  1 lines sent, 1 redacted (ip 1 values), 0 flags\n")

		t.Log("7. The one-job grant is gone, so a neighbour still asks; add the directory instead")
		await = h.call(t, cs, "read_file", map[string]any{"path": sibling, "from": 1, "to": 1})
		h.answer(t, widening("j6", sibling), "a")
		h.scope = append(h.scope, outsideDir)
		checkLiveResult(t, await(), "1: sibling request from <ip#1>\n", liveMeta("j6", "ok", 1, 1, 0, "", ""))
		h.out.until(t, "14:00:00Z  scope    "+outsideDir+" added for this session\n")
		h.out.until(t, "14:00:00Z  read_file "+sibling+"  1 lines sent, 1 redacted (ip 1 values), 0 flags\n")

		t.Log("8. The session scope now covers the directory, so the first path asks nothing")
		await = h.call(t, cs, "read_file", map[string]any{"path": outside, "from": 1, "to": 1})
		h.answer(t, "Job j7 from the agent: read_file\n\n  1  read_file   "+outside+"  lines 1-1\n"+approvalFooter, "a")
		checkLiveResult(t, await(), "1: request from <ip#1>\n", liveMeta("j7", "ok", 1, 1, 0, "", ""))
		h.out.until(t, "14:00:00Z  read_file "+outside+"  1 lines sent, 1 redacted (ip 1 values), 0 flags\n")

		t.Log("9. Approve tail_file, inspect the flagged string, and drop the result")
		await = h.call(t, cs, "tail_file", map[string]any{"path": h.flagPath, "n": 1})
		h.answer(t, "Job j8 from the agent: tail\n\n  1  tail        "+h.flagPath+"  last 1 lines\n"+approvalFooter, "a")
		h.answer(t, "14:00:00Z  tail "+h.flagPath+"  1 lines, 1 strings to inspect\n  flagged.log:2 ! "+flaggedValue+"\n[s] send   [d] drop\n>\n", "d")
		checkLiveResult(t, await(), "denied: dropped after review\n", liveMeta("j8", "denied", 0, 0, 0, "", "dropped after review"))
		h.out.until(t, "14:00:00Z  tail "+h.flagPath+"  denied: dropped after review\n")

		t.Log("10. Page search_files across two results with stable placeholders")
		var pages []string
		cursor := ""
		for i, line := range []int{1, 3} {
			args := map[string]any{"glob": h.path, "regex": "request from", "max": 1, "cursor": cursor}
			await = h.call(t, cs, "search_files", args)
			h.answer(t, fmt.Sprintf("Job j%d from the agent: search\n\n  1  search      %s  regex request from  1 files; first: %s\n", i+9, h.path, h.path)+approvalFooter, "a")
			got := await()
			meta := liveMetadata(t, got.Content[1])
			next, ok := meta["next"].(string)
			if !ok || (i == 0 && next == "") || (i == 1 && next != "") {
				t.Fatalf("page %d next = %v", i+1, meta["next"])
			}
			pages = append(pages, fmt.Sprintf("%s:%d: request from <ip#1>\n", h.path, line))
			checkLiveResult(t, got, pages[i], liveMeta(fmt.Sprintf("j%d", i+9), "ok", 1, 1, 0, next, ""))
			cursor = next
			h.out.until(t, "14:00:00Z  search "+h.path+"  1 lines sent, 1 redacted (ip 1 values), 0 flags\n")
		}
		stored, err := session.Load(h.sessionPath)
		if err != nil || stored.NextJobSeq != 11 || stored.NextResultSeq != 13 {
			t.Fatalf("persisted counters: job=%d result=%d err=%v", stored.NextJobSeq, stored.NextResultSeq, err)
		}

		t.Log("11. End through DeleteSession and check the audit copy and summary")
		h.end(t, "12 jobs, 6 sent, 5 denied, 1 rejected", "")
		if strings.Count(h.out.text.String(), "\n>\n") != 10 {
			t.Fatal("unexpected number of approval and review prompts")
		}
		one, both := "1: request from <ip#1>\n", "1: sibling request from <ip#1>\n"
		h.checkAudit(t, []string{read, "", "", "", "", "", one, both, one, "", pages[0], pages[1]}, map[string][]string{
			"j1":   {"received", "approved", "sent"},
			"j2.1": {"received", "denied", "sent"}, "j2.2": {"received", "denied", "sent"}, "j2.3": {"received", "denied", "sent"},
			"j3": {"received", "rejected", "sent"}, "j4": {"received", "denied", "sent"},
			"j5": {"received", "scope-added-once", "approved", "sent"}, "j6": {"received", "scope-added", "approved", "sent"},
			"j7": {"received", "approved", "sent"}, "j8": {"received", "approved", "dropped", "sent"},
			"j9": {"received", "approved", "sent"}, "j10": {"received", "approved", "sent"},
		})
	})

	t.Run("level 0", func(t *testing.T) {
		t.Log("12. Run file jobs and available df and ps tools without prompts")
		h := newLiveSession(t, 0)
		cs := connect(t, &akenmcp.Server{SessionPath: h.sessionPath, Version: "test", Now: h.options.Now})
		read := "1: request from <ip#1>\n2: request completed\n3: request from <ip#1>\n"
		checkLiveResult(t, h.call(t, cs, "read_file", map[string]any{"path": h.path})(), read, liveMeta("j1", "ok", 3, 2, 0, "", ""))
		flagged := "2: " + flaggedValue + "\n"
		checkLiveResult(t, h.call(t, cs, "tail_file", map[string]any{"path": h.flagPath, "n": 1})(), flagged, liveMeta("j2", "ok", 1, 0, 1, "", ""))
		h.out.until(t, "14:00:00Z  tail "+h.flagPath+"  1 lines sent, 0 redacted, 1 flags\n")

		t.Log("13. Reject an outside-scope file: level 0 has no screen to widen the scope on")
		outside := filepath.Join(t.TempDir(), "outside.log")
		writeSessionFile(t, outside, "must never be sent\n")
		refusal := "outside the scope (/var/log, " + h.options.Allow[0] + "); restart aken serve with --allow DIR to widen it"
		checkLiveResult(t, h.call(t, cs, "read_file", map[string]any{"path": outside})(), "rejected: "+refusal+"\n", liveMeta("j3", "rejected", 0, 0, 0, "", refusal))
		h.out.until(t, "14:00:00Z  read_file "+outside+"  rejected: "+refusal+"\n")
		results := []string{read, flagged, ""}
		events := map[string][]string{"j1": {"received", "approved", "sent"}, "j2": {"received", "approved", "sent"}, "j3": {"received", "rejected", "sent"}}
		for _, name := range []string{"df", "ps"} {
			t.Run(name, func(t *testing.T) {
				if _, err := source.BinaryPath(name); err != nil {
					t.Skip(err)
				}
				got := h.call(t, cs, name, map[string]any{})()
				if got.IsError || len(got.Content) != 2 {
					t.Fatalf("%s failed: %+v", name, got)
				}
				text := got.Content[0].(*sdk.TextContent).Text
				meta := liveMetadata(t, got.Content[1])
				id := fmt.Sprintf("j%d", len(results)+1)
				if meta["id"] != id || meta["status"] != "ok" || meta["lines"] != float64(strings.Count(text, "\n")) || text == "" {
					t.Fatalf("%s metadata = %v", name, meta)
				}
				header := map[string]string{"df": "Filesystem", "ps": "PID"}[name]
				if !strings.Contains(strings.SplitN(text, "\n", 2)[0], header) {
					t.Fatalf("%s lacks header %q", name, header)
				}
				results = append(results, text)
				events[id] = []string{"received", "approved", "sent"}
				// The collector counts a job after it posts the result, so the session must not end before its summary line.
				h.out.untilPrefix(t, "14:00:00Z  "+name+"   ")
			})
		}
		h.end(t, fmt.Sprintf("%d jobs, %d sent, 0 denied, 1 rejected", len(results), len(results)-1), "")
		noApproval(t, h.out.text.String())
		h.checkAudit(t, results, events)
	})
}

func TestModifiedMCP(t *testing.T) {
	t.Run("forged", func(t *testing.T) {
		t.Log("1. Drop a wrong-key envelope and the following unexpected sequence")
		h := newLiveSession(t, 1)
		wrong := protocol.DeriveSessionKeys([32]byte{1})
		h.post(t, h.seal(t, wrong, 1, protocol.ClassRead, "read_file"), "")
		h.errs.until(t, "aken: dropped a job: protocol: session authentication failed\n")
		h.post(t, h.seal(t, h.keys, 2, protocol.ClassRead, "read_file"), "")
		h.errs.until(t, "aken: dropped a job: protocol: unexpected sequence number\n")
		messages, err := h.client.PollResults(h.ctx, h.id, 0)
		if err != nil || len(messages) != 0 {
			t.Fatalf("dropped jobs returned results: %d, %v", len(messages), err)
		}
		h.end(t, "0 jobs, 0 sent, 0 denied, 0 rejected", "aken: dropped a job: protocol: session authentication failed\naken: dropped a job: protocol: unexpected sequence number\n")
		noApproval(t, h.out.text.String())
	})
	t.Run("replayed", func(t *testing.T) {
		t.Log("2. Approve the original envelope, then replay its identical bytes")
		h := newLiveSession(t, 1)
		e := h.seal(t, h.keys, 1, protocol.ClassRead, "read_file")
		h.post(t, e, "")
		h.answer(t, "Job j1 from the agent: read_file\n\n  1  read_file   "+h.path+"  lines 1-500\n"+approvalFooter, "a")
		r := h.result(t, 1, "j1", "ok", "")
		if !reflect.DeepEqual(r.Lines, []string{"1: request from <ip#1>", "2: request completed", "3: request from <ip#1>"}) {
			t.Fatalf("original result = %v", r.Lines)
		}
		h.out.until(t, "14:00:00Z  read_file "+h.path+"  3 lines sent, 2 redacted (ip 1 values), 0 flags\n")
		before := h.out.text.Len()
		h.post(t, e, "")
		// A later result proves the collector has passed the replay in the ordered queue.
		h.post(t, h.seal(t, h.keys, 2, protocol.ClassRead, "unknown"), "")
		h.result(t, 2, "j2", "rejected", "unknown job name")
		h.out.until(t, "14:00:00Z  unknown "+h.path+"  rejected: unknown job name\n")
		h.post(t, e, "bad_sequence")
		h.end(t, "2 jobs, 1 sent, 0 denied, 1 rejected", "")
		noApproval(t, h.out.text.String()[before:])
	})
	t.Run("reordered", func(t *testing.T) {
		t.Log("3. Reject sequence 3 before sequence 2 at the relay")
		h := newLiveSession(t, 1)
		h.post(t, h.seal(t, h.keys, 1, protocol.ClassRead, "unknown"), "")
		h.result(t, 1, "j1", "rejected", "unknown job name")
		h.post(t, h.seal(t, h.keys, 3, protocol.ClassRead, "read_file"), "bad_sequence")
		h.post(t, h.seal(t, h.keys, 2, protocol.ClassRead, "unknown"), "")
		h.result(t, 2, "j2", "rejected", "unknown job name")
		h.out.until(t, "14:00:00Z  unknown "+h.path+"  rejected: unknown job name\n")
		h.out.until(t, "14:00:00Z  unknown "+h.path+"  rejected: unknown job name\n")
		h.end(t, "2 jobs, 0 sent, 0 denied, 2 rejected", "")
		noApproval(t, h.out.text.String())
	})
	t.Run("mislabeled", func(t *testing.T) {
		t.Log("4. Reject class 2 at the relay and an authenticated unknown job at the collector")
		h := newLiveSession(t, 1)
		h.post(t, h.seal(t, h.keys, 1, protocol.ClassWrite, "read_file"), "class_not_allowed")
		h.post(t, h.seal(t, h.keys, 1, protocol.ClassRead, "unknown"), "")
		h.result(t, 1, "j1", "rejected", "unknown job name")
		h.out.until(t, "14:00:00Z  unknown "+h.path+"  rejected: unknown job name\n")
		h.end(t, "1 jobs, 0 sent, 0 denied, 1 rejected", "")
		noApproval(t, h.out.text.String())
	})
}

type sessionOutput struct {
	ctx   context.Context
	lines chan string
	text  strings.Builder
}

func sessionPipe(ctx context.Context) (*sessionOutput, *io.PipeWriter, *io.PipeReader) {
	r, w := io.Pipe()
	out := &sessionOutput{ctx: ctx, lines: make(chan string, 128)}
	go func() {
		defer close(out.lines)
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			select {
			case out.lines <- scanner.Text() + "\n":
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, w, r
}

func (o *sessionOutput) until(t *testing.T, want string) string {
	t.Helper()
	return o.wait(t, want, func(line string) bool { return line == want })
}

// A job whose counts depend on the machine is awaited by the prefix its summary line starts with.
func (o *sessionOutput) untilPrefix(t *testing.T, prefix string) string {
	t.Helper()
	return o.wait(t, prefix, func(line string) bool { return strings.HasPrefix(line, prefix) })
}

func (o *sessionOutput) wait(t *testing.T, want string, done func(string) bool) string {
	t.Helper()
	var chunk strings.Builder
	for {
		select {
		case line, ok := <-o.lines:
			if !ok {
				if want == "" {
					return chunk.String()
				}
				t.Fatalf("output closed waiting for %q; got %q", want, chunk.String())
			}
			o.text.WriteString(line)
			chunk.WriteString(line)
			if done(line) {
				return chunk.String()
			}
		case <-o.ctx.Done():
			t.Fatalf("timed out waiting for %q; got %q", want, chunk.String())
		}
	}
}

type liveSession struct {
	ctx                    context.Context
	options                serve.Options
	path, flagPath         string
	sessionPath, auditPath string
	out, errs              *sessionOutput
	input                  *io.PipeWriter
	done                   chan struct{}
	code                   int
	client                 *protocol.RelayClient
	id                     protocol.SessionID
	keys                   protocol.SessionKeys
	stored                 session.Session
	scope                  []any
}

func newLiveSession(t *testing.T, level int) *liveSession {
	t.Helper()
	now := time.Date(2026, time.September, 13, 14, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	handler := relay.NewHandler(relay.NewMemoryStore(), relay.Options{Now: clock})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	dir, state := t.TempDir(), t.TempDir()
	rules := filepath.Join(state, "rules.json")
	writeSessionFile(t, rules, `{"version":1,"rules":[]}`)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	h := &liveSession{ctx: ctx, done: make(chan struct{}), path: filepath.Join(dir, "app"), flagPath: filepath.Join(dir, "flagged.log"), sessionPath: filepath.Join(t.TempDir(), "session.json")}
	// The generated fixture path must not itself trigger entropy review in search output.
	h.options = serve.Options{Level: level, TTL: time.Hour, RelayURL: server.URL, Allow: []string{dir}, Keep: []string{h.path}, StateDir: state, RulesFile: rules, Now: clock, Argv: []string{"serve"}}
	writeSessionFile(t, h.path, "request from 203.0.113.7\nrequest completed\nrequest from 203.0.113.7\n")
	writeSessionFile(t, h.flagPath, "ordinary\n"+flaggedValue+"\n")
	in, input := io.Pipe()
	h.input = input
	out, stdout, outReader := sessionPipe(ctx)
	errs, stderr, errReader := sessionPipe(ctx)
	h.out, h.errs = out, errs
	go func() {
		defer close(h.done)
		h.code = serve.Run(ctx, h.options, in, stdout, stderr, 40)
		_ = stdout.Close()
		_ = stderr.Close()
	}()
	t.Cleanup(func() {
		cancel()
		for _, pipe := range []io.Closer{input, in, outReader, errReader} {
			_ = pipe.Close()
		}
		select {
		case <-h.done:
		case <-time.After(5 * time.Second):
			t.Error("collector did not stop during cleanup")
		}
	})
	opening := h.out.until(t, "Waiting for the local MCP to join. Ctrl-C ends the session.\n")
	tokens := regexp.MustCompile(`akn1_[a-z2-7]{52}`).FindAllString(opening, -1)
	if len(tokens) != 1 {
		t.Fatalf("opening token count = %d", len(tokens))
	}
	token, err := protocol.ParseToken(tokens[0])
	if err != nil {
		t.Fatal(err)
	}
	defer token.Zero()
	h.id = token.SessionID()
	h.auditPath = filepath.Join(state, "sessions", "20260913T140000Z-"+h.id.String()[:8])
	h.scope = []any{"/var/log", dir}
	want := fmt.Sprintf("aken serve: session open on %s, level %d, expires 2026-09-13T15:00:00Z\nScope    /var/log, %s\nLocal    %s\nRedaction   14 rules (14 default, 0 from %s); kept: %s\n\nSession token. Paste it into `aken-mcp join` on your machine, not into the agent chat:\n\n  %s\n\nWaiting for the local MCP to join. Ctrl-C ends the session.\n", server.URL, level, dir, h.auditPath, rules, h.path, tokens[0])
	if opening != want {
		t.Fatalf("opening = %q, want %q", opening, want)
	}
	h.client, err = protocol.NewRelayClient(server.URL, token.RelayCredential())
	if err != nil {
		t.Fatal(err)
	}
	info, err := h.client.Session(ctx, h.id)
	if err != nil || info.Mode != "session" || info.Joined {
		t.Fatalf("session mode=%s joined=%v err=%v", info.Mode, info.Joined, err)
	}
	h.stored, err = session.Join(ctx, h.client, token, info, "cli", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Save(h.sessionPath, h.stored); err != nil {
		t.Fatal(err)
	}
	root, err := h.stored.ParsedContentRoot()
	if err != nil {
		t.Fatal(err)
	}
	h.keys = protocol.DeriveSessionKeys(root)
	if got := h.out.until(t, "Joined via cli at 14:00:00Z. Waiting for jobs.\n"); got != "Joined via cli at 14:00:00Z. Waiting for jobs.\n" {
		t.Fatalf("join screen = %q", got)
	}
	return h
}

func (h *liveSession) call(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any) func() *sdk.CallToolResult {
	t.Helper()
	done := make(chan struct{})
	var got *sdk.CallToolResult
	var err error
	go func() {
		defer close(done)
		got, err = cs.CallTool(h.ctx, &sdk.CallToolParams{Name: name, Arguments: args})
	}()
	return func() *sdk.CallToolResult {
		t.Helper()
		select {
		case <-done:
			if err != nil {
				t.Fatal(err)
			}
			return got
		case <-h.ctx.Done():
			for len(h.out.lines) > 0 {
				t.Logf("pending stdout: %q", <-h.out.lines)
			}
			t.Fatalf("%s did not finish", name)
			return nil
		}
	}
}

func (h *liveSession) answer(t *testing.T, screen, answer string) {
	t.Helper()
	if got := h.out.until(t, ">\n"); got != screen {
		t.Fatalf("screen = %q, want %q", got, screen)
	}
	done := make(chan error, 1)
	go func() { _, err := io.WriteString(h.input, answer+"\n"); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-h.ctx.Done():
		t.Fatal("collector did not read the answer")
	}
}

func liveMeta(id, status string, lines, redacted, flags int, next, message string) map[string]any {
	m := map[string]any{"id": id, "status": status, "lines": float64(lines), "lines_redacted": float64(redacted), "flags": float64(flags), "next": next}
	if message != "" {
		m["error"] = message
	}
	return m
}

func liveMetadata(t *testing.T, content sdk.Content) map[string]any {
	t.Helper()
	text, ok := content.(*sdk.TextContent)
	if !ok || strings.ContainsAny(text.Text, "\r\n") {
		t.Fatal("metadata must be one text line")
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(text.Text), &meta); err != nil {
		t.Fatal(err)
	}
	return meta
}

func checkLiveResult(t *testing.T, got *sdk.CallToolResult, text string, meta map[string]any) {
	t.Helper()
	if got.IsError != (meta["status"] != "ok") || len(got.Content) != 2 {
		t.Fatalf("result: isError=%v, content=%d", got.IsError, len(got.Content))
	}
	first, ok := got.Content[0].(*sdk.TextContent)
	if !ok || first.Text != text {
		t.Fatalf("result text = %+v, want %q", got.Content[0], text)
	}
	if actual := liveMetadata(t, got.Content[1]); !reflect.DeepEqual(actual, meta) {
		t.Fatalf("metadata = %v, want %v", actual, meta)
	}
}

func noApproval(t *testing.T, text string) {
	t.Helper()
	for _, marker := range []string{"Job ", "[a] approve", "[s] send", "\n>\n"} {
		if strings.Contains(text, marker) {
			t.Fatalf("unexpected prompt: %q", text)
		}
	}
}

func (h *liveSession) seal(t *testing.T, keys protocol.SessionKeys, seq uint64, class uint8, name string) protocol.Envelope {
	t.Helper()
	params, err := json.Marshal(protocol.ReadFileParams{Path: h.path})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(protocol.Job{ID: fmt.Sprintf("j%d", seq), Name: name, Params: params})
	if err != nil {
		t.Fatal(err)
	}
	e, err := keys.SealJob(h.id, seq, class, data)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (h *liveSession) post(t *testing.T, e protocol.Envelope, code string) {
	t.Helper()
	err := h.client.PostJob(h.ctx, h.id, e)
	if code == "" {
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	var relayErr *protocol.RelayError
	status := map[string]int{"bad_sequence": 409, "class_not_allowed": 403}[code]
	if !errors.As(err, &relayErr) || relayErr.Code != code || relayErr.Status != status {
		t.Fatalf("PostJob = %v, want %d %s", err, status, code)
	}
}

func (h *liveSession) result(t *testing.T, seq uint64, id, status, message string) protocol.Result {
	t.Helper()
	es, err := h.client.PollResults(h.ctx, h.id, 30*time.Second)
	if err != nil || len(es) != 1 {
		t.Fatalf("results = %d, err=%v", len(es), err)
	}
	data, err := h.keys.OpenResult(es[0], h.id, seq)
	if err != nil {
		t.Fatal(err)
	}
	var r protocol.Result
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	if r.ID != id || r.Status != status || r.Error != message || (status != "ok" && len(r.Lines) != 0) {
		t.Fatalf("result = %+v", r)
	}
	return r
}

func (h *liveSession) end(t *testing.T, summary, diagnostics string) {
	t.Helper()
	if err := h.client.DeleteSession(h.ctx, h.id); err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.done:
	case <-h.ctx.Done():
		t.Fatal("DeleteSession did not stop the collector")
	}
	// The reader goroutines append lines after Run returns; wait for the last line of each stream.
	h.out.until(t, "Session ended: "+summary+". Local copy: "+h.auditPath+"\n")
	h.errs.until(t, "aken: the session was ended on the relay\n")
	if h.code != 1 || h.errs.text.String() != diagnostics+"aken: the session was ended on the relay\n" {
		t.Fatalf("collector exit=%d, stderr=%q", h.code, h.errs.text.String())
	}
	if !strings.HasSuffix(h.out.text.String(), "Session ended: "+summary+". Local copy: "+h.auditPath+"\n") {
		t.Fatalf("wrong ending summary: %q", h.out.text.String())
	}
	if _, err := h.client.Session(h.ctx, h.id); !protocol.IsNotFound(err) {
		t.Fatalf("ended session = %v", err)
	}
}

func writeSessionFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (h *liveSession) checkAudit(t *testing.T, results []string, events map[string][]string) {
	t.Helper()
	files := make(map[string]string)
	err := filepath.WalkDir(h.auditPath, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		mode := os.FileMode(0o600)
		if entry.IsDir() {
			mode = 0o700
		} else if filepath.Ext(path) == ".txt" {
			mode = 0o400
		}
		if info.Mode().Perm() != mode {
			t.Errorf("%s mode=%o, want %o", path, info.Mode().Perm(), mode)
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), h.stored.Token) {
			t.Errorf("token in %s", path)
		}
		rel, err := filepath.Rel(h.auditPath, path)
		files[rel] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(results)+3 {
		t.Fatalf("audit files=%d, want %d", len(files), len(results)+3)
	}
	for i, want := range results {
		name := filepath.Join("results", fmt.Sprintf("%d.txt", i+1))
		if got, ok := files[name]; !ok || got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	var mapping map[string]string
	if err := json.Unmarshal([]byte(files["mapping.json"]), &mapping); err != nil || mapping["<ip#1>"] != "203.0.113.7" {
		t.Fatalf("mapping missing original IP: %v", err)
	}
	var info map[string]any
	if err := json.Unmarshal([]byte(files["session.json"]), &info); err != nil {
		t.Fatal(err)
	}
	wantInfo := map[string]any{"session_id": h.id.String(), "relay": h.options.RelayURL, "level": float64(h.options.Level), "created_at": "2026-09-13T14:00:00Z", "expires_at": "2026-09-13T15:00:00Z", "joined_at": "2026-09-13T14:00:00Z", "joined_via": "cli", "scope": h.scope, "ended_at": "2026-09-13T14:00:00Z", "argv": []any{"serve"}}
	if !reflect.DeepEqual(info, wantInfo) {
		t.Errorf("session.json=%v, want %v", info, wantInfo)
	}
	actual := make(map[string][]string)
	for _, line := range strings.Split(strings.TrimSuffix(files["jobs.log"], "\n"), "\n") {
		var event struct {
			JobID string `json:"job_id"`
			Event string `json:"event"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		actual[event.JobID] = append(actual[event.JobID], event.Event)
	}
	if !reflect.DeepEqual(actual, events) {
		t.Errorf("audit events=%v, want %v", actual, events)
	}
}
