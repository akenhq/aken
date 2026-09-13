// SPDX-License-Identifier: Apache-2.0
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/akenhq/aken/internal/devrelay"
	"github.com/akenhq/aken/internal/session"
	"github.com/akenhq/aken/protocol"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func liveFixture(t *testing.T) (*Server, *protocol.RelayClient, protocol.SessionKeys, protocol.SessionID) {
	t.Helper()
	relay := httptest.NewServer(devrelay.New())
	t.Cleanup(relay.Close)
	token := protocol.NewToken()
	client, err := protocol.NewRelayClient(relay.URL, token.RelayCredential())
	if err != nil {
		t.Fatal(err)
	}
	pair, err := protocol.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	mac := protocol.CollectorMAC(token.ExchangeKey(), token.SessionID(), pair.Public())
	info, err := client.CreatePersistentSession(t.Context(), token.SessionID(), time.Hour, pair.Public(), mac)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{SessionPath: filepath.Join(t.TempDir(), "session.json"), Relay: relay.URL, AllowChatJoin: true}
	cs := connect(t, s)
	text, _ := call(t, cs, "join", map[string]any{"token": token.Encode()})
	want := fmt.Sprintf("Joined live session %s via chat. It expires at %s. This token has been in the chat transcript.", token.SessionID(), info.ExpiresAt.UTC().Format(time.RFC3339))
	if text != want {
		t.Fatalf("join = %q", text)
	}
	joined, ok, err := client.WaitJoin(t.Context(), token.SessionID(), 0)
	if err != nil || !ok || joined.Via != "chat" {
		t.Fatalf("chat join: %v", err)
	}
	mcpKey, keyOK := protocol.DecodeKey(joined.MCPKey)
	mcpMAC, macOK := protocol.DecodeKey(joined.MCPMAC)
	if !keyOK || !macOK || !protocol.VerifyMCPMAC(token.ExchangeKey(), token.SessionID(), pair.Public(), mcpKey, mcpMAC) {
		t.Fatal("invalid MCP authentication")
	}
	root, err := protocol.ContentRoot(pair, mcpKey, token.SessionID(), pair.Public(), mcpKey)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := session.Load(s.SessionPath)
	if err != nil || stored.ContentRoot != protocol.EncodeKey(root) || stored.JoinedVia != "chat" || stored.NextJobSeq != 1 || stored.NextResultSeq != 1 {
		t.Fatalf("chat session not saved: %v", err)
	}
	return s, client, protocol.DeriveSessionKeys(root), token.SessionID()
}

func fakeCollector(t *testing.T, s *Server, client *protocol.RelayClient, keys protocol.SessionKeys, id protocol.SessionID, respond func(protocol.Job) []protocol.Result) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done })
	go func() {
		defer close(done)
		jobSeq, resultSeq := uint64(1), uint64(1)
		for {
			envelopes, err := client.PollJobs(ctx, id, 30*time.Second)
			if err != nil {
				if ctx.Err() == nil {
					t.Errorf("collector poll: %v", err)
				}
				return
			}
			for _, envelope := range envelopes {
				stored, err := session.Load(s.SessionPath)
				if err != nil || stored.NextJobSeq <= envelope.Seq {
					t.Error("job was posted before its counter was persisted")
					return
				}
				raw, err := keys.OpenJob(envelope, id, jobSeq)
				if err != nil {
					t.Errorf("collector open: %v", err)
					return
				}
				jobSeq++
				var job protocol.Job
				if err := json.Unmarshal(raw, &job); err != nil {
					t.Errorf("collector decode: %v", err)
					return
				}
				if job.ID != fmt.Sprintf("j%d", envelope.Seq) || envelope.Class != protocol.ClassRead {
					t.Error("wrong job id or class")
				}
				for _, got := range respond(job) {
					raw, err := json.Marshal(got)
					if err != nil {
						t.Errorf("collector marshal: %v", err)
						return
					}
					sealed, err := keys.SealResult(id, resultSeq, protocol.ClassRead, raw)
					if err == nil {
						err = client.PostResult(ctx, id, sealed)
					}
					if err != nil {
						t.Errorf("collector result: %v", err)
						return
					}
					resultSeq++
				}
			}
		}
	}()
}

func TestLiveTools(t *testing.T) {
	s, client, keys, id := liveFixture(t)
	jobs := make(chan protocol.Job, 20)
	fakeCollector(t, s, client, keys, id, func(job protocol.Job) []protocol.Result {
		jobs <- job
		return []protocol.Result{{ID: job.ID, Status: "ok", Lines: []string{"1: <ip#1>", "2: done"}, Next: "next-page", Redaction: protocol.ResultRedaction{LinesRedacted: 1, Flags: 2}}}
	})
	cs := connect(t, s)
	for i, tt := range []struct {
		tool, name string
		args       map[string]any
		want       string
	}{
		{"list_dir", "list_dir", map[string]any{"path": "/var/log"}, `{"path":"/var/log"}`},
		{"read_file", "read_file", map[string]any{"path": "/var/log/a"}, `{"path":"/var/log/a","from":1,"to":500}`},
		{"read_file", "read_file", map[string]any{"path": "/var/log/a", "from": 501, "to": 600}, `{"path":"/var/log/a","from":501,"to":600}`},
		{"search_files", "search", map[string]any{"glob": "/var/log/*", "regex": "error"}, `{"glob":"/var/log/*","regex":"error","max":50}`},
		{"search_files", "search", map[string]any{"glob": "/var/log/*", "regex": "error", "since": "1h", "before": 2, "after": 3, "max": 5, "cursor": "next-page"}, `{"glob":"/var/log/*","regex":"error","since":"1h","before":2,"after":3,"max":5,"cursor":"next-page"}`},
		{"tail_file", "tail", map[string]any{"path": "/var/log/a"}, `{"path":"/var/log/a","n":100}`},
		{"tail_file", "tail", map[string]any{"path": "/var/log/a", "n": 5}, `{"path":"/var/log/a","n":5}`},
		{"journal", "journal", map[string]any{"unit": "app", "since": "2h", "until": "1h", "regex": "error", "max": 10, "cursor": "next-page"}, `{"unit":"app","since":"2h","until":"1h","regex":"error","max":10,"cursor":"next-page"}`},
		{"docker_logs", "docker_logs", map[string]any{"container": "app", "since": "2h", "until": "1h", "tail": 10}, `{"container":"app","since":"2h","until":"1h","tail":10}`},
		{"systemctl_status", "systemctl_status", map[string]any{"unit": "app"}, `{"unit":"app"}`},
		{"ps", "ps", map[string]any{}, `{}`},
		{"df", "df", map[string]any{}, `{}`},
	} {
		text, meta := call(t, cs, tt.tool, tt.args)
		job := <-jobs
		var got, want map[string]any
		if json.Unmarshal(job.Params, &got) != nil || json.Unmarshal([]byte(tt.want), &want) != nil || !reflect.DeepEqual(got, want) || job.Name != tt.name {
			t.Fatalf("%s job = %s %s", tt.tool, job.Name, job.Params)
		}
		if text != "1: <ip#1>\n2: done\n" || !reflect.DeepEqual(meta, map[string]any{"id": fmt.Sprintf("j%d", i+1), "status": "ok", "lines": float64(2), "lines_redacted": float64(1), "flags": float64(2), "next": "next-page"}) {
			t.Fatalf("%s output = %q, %v", tt.tool, text, meta)
		}
	}
	stored, err := session.Load(s.SessionPath)
	if err != nil || stored.NextJobSeq != 13 || stored.NextResultSeq != 13 {
		t.Fatalf("sequence persistence: %v", err)
	}
	// The same collector must accept jobs after a fresh MCP process loads the file.
	restarted := &Server{SessionPath: s.SessionPath}
	_, meta := call(t, connect(t, restarted), "ps", map[string]any{})
	if meta["id"] != "j13" {
		t.Fatal(meta)
	}
	stored, err = session.Load(s.SessionPath)
	if err != nil || stored.NextJobSeq != 14 || stored.NextResultSeq != 14 {
		t.Fatalf("restart sequence persistence: %v", err)
	}
}

func TestLiveStatusesAndPlan(t *testing.T) {
	s, client, keys, id := liveFixture(t)
	fakeCollector(t, s, client, keys, id, func(job protocol.Job) []protocol.Result {
		if job.Name != "plan" {
			return []protocol.Result{{ID: job.ID, Status: job.Name, Error: "collector reason"}}
		}
		var plan protocol.PlanParams
		if json.Unmarshal(job.Params, &plan) != nil || len(plan.Jobs) != 4 {
			t.Error("invalid plan")
			return nil
		}
		var results []protocol.Result
		for i, status := range []string{"ok", "denied", "rejected", "error"} {
			if plan.Jobs[i].ID != fmt.Sprintf("%s.%d", job.ID, i+1) || plan.Jobs[i].Name != "ps" || string(plan.Jobs[i].Params) != "{}" {
				t.Error("wrong inner job")
			}
			got := protocol.Result{ID: plan.Jobs[i].ID, Status: status}
			if status == "ok" {
				got.Lines = []string{"output"}
			} else {
				got.Error = "collector reason"
			}
			results = append([]protocol.Result{got}, results...)
		}
		return results
	})
	cs := connect(t, s)
	jobs := []map[string]any{}
	for range 4 {
		jobs = append(jobs, map[string]any{"name": "ps", "params": map[string]any{}})
	}
	got, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "plan", Arguments: map[string]any{"jobs": jobs}})
	if err != nil || !got.IsError || len(got.Content) != 8 {
		t.Fatalf("plan: %+v, %v", got, err)
	}
	for i, status := range []string{"ok", "denied", "rejected", "error"} {
		text := got.Content[2*i].(*mcp.TextContent).Text
		if !strings.HasPrefix(text, fmt.Sprintf("## %d ps %s\n", i+1, status)) {
			t.Fatalf("plan order: %q", text)
		}
		one, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "result", Arguments: map[string]any{"id": fmt.Sprintf("j1.%d", i+1)}})
		if err != nil || one.IsError != (status != "ok") || len(one.Content) != 2 {
			t.Fatalf("result status: %+v, %v", one, err)
		}
		var meta map[string]any
		if json.Unmarshal([]byte(one.Content[1].(*mcp.TextContent).Text), &meta) != nil || meta["status"] != status || (status != "ok" && meta["error"] != "collector reason") {
			t.Fatal("missing status or error metadata")
		}
	}
	stored, err := session.Load(s.SessionPath)
	if err != nil || stored.NextJobSeq != 2 || stored.NextResultSeq != 5 {
		t.Fatalf("plan counters: %v", err)
	}
}

func TestPlanValidation(t *testing.T) {
	s, client, _, id := liveFixture(t)
	cs := connect(t, s)
	tooMany := make([]map[string]any, 41)
	for i := range tooMany {
		tooMany[i] = map[string]any{"name": "ps", "params": map[string]any{}}
	}
	for _, jobs := range []any{
		[]any{}, tooMany,
		[]any{map[string]any{"name": "ps", "params": map[string]any{}}, map[string]any{"name": "private-name", "params": map[string]any{}}},
		[]any{map[string]any{"name": "plan", "params": map[string]any{}}},
		[]any{map[string]any{"name": "search_files", "params": map[string]any{}}},
		[]any{map[string]any{"name": "ps", "params": nil}},
	} {
		got, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "plan", Arguments: map[string]any{"jobs": jobs}})
		if err != nil || !got.IsError || strings.Contains(got.Content[0].(*mcp.TextContent).Text, "private-name") {
			t.Fatalf("plan validation: %+v, %v", got, err)
		}
	}
	stored, err := session.Load(s.SessionPath)
	if err != nil || stored.NextJobSeq != 1 {
		t.Fatalf("invalid plan consumed a sequence: %v", err)
	}
	if jobs, err := client.PollJobs(t.Context(), id, 0); err != nil || len(jobs) != 0 {
		t.Fatalf("invalid plan was posted: %v", err)
	}
}

func TestPendingAndLateResult(t *testing.T) {
	s, client, keys, id := liveFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	_, _, err := s.ps(ctx, nil, noArgs{})
	want := "awaiting approval or still running in the server terminal; call result with id j1"
	if err == nil || err.Error() != want {
		t.Fatalf("pending: %v", err)
	}
	envelopes, err := client.PollJobs(t.Context(), id, 0)
	if err != nil || len(envelopes) != 1 {
		t.Fatalf("pending job: %v", err)
	}
	postResult(t, client, keys, id, 1, []byte(`{"id":"j1","status":"ok","lines":["late"]}`))
	text, meta := call(t, connect(t, s), "result", map[string]any{"id": "j1"})
	if text != "late\n" || meta["id"] != "j1" {
		t.Fatalf("late result: %q, %v", text, meta)
	}
	// Cached results must not poll, even after the relay session ends.
	if err := client.DeleteSession(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	call(t, connect(t, s), "result", map[string]any{"id": "j1"})
}

func postResult(t *testing.T, client *protocol.RelayClient, keys protocol.SessionKeys, id protocol.SessionID, seq uint64, raw []byte) {
	t.Helper()
	envelope, err := keys.SealResult(id, seq, protocol.ClassRead, raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.PostResult(t.Context(), id, envelope); err != nil {
		t.Fatal(err)
	}
}

func TestMalformedAndForgedResults(t *testing.T) {
	for _, forged := range []bool{false, true} {
		t.Run(fmt.Sprint(forged), func(t *testing.T) {
			s, client, keys, id := liveFixture(t)
			if forged {
				wrong := protocol.DeriveSessionKeys([32]byte{99})
				postResult(t, client, wrong, id, 1, []byte(`{"id":"j1","status":"ok","lines":["forged"]}`))
			} else {
				postResult(t, client, keys, id, 1, []byte(`{"id":"j1","status":"ok","lines":42}`))
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
			defer cancel()
			out, _, err := s.jobResult(ctx, nil, resultArgs{ID: "j1"})
			stored, loadErr := session.Load(s.SessionPath)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if forged {
				if err == nil || err.Error() != pending("j1").Error() || len(s.results) != 0 || stored.NextResultSeq != 1 {
					t.Fatalf("forged result accepted: %v", err)
				}
				postResult(t, client, keys, id, 2, []byte(`{"id":"j2","status":"ok"}`))
				_, _, err = s.jobResult(t.Context(), nil, resultArgs{ID: "j2"})
				if err == nil || !strings.Contains(err.Error(), "restart the collector's aken serve") || len(s.results) != 0 {
					t.Fatalf("sequence error: %v", err)
				}
			} else if err != nil || !out.IsError || s.results["j1"].Status != "error" || stored.NextResultSeq != 2 {
				t.Fatalf("malformed result not cached as error: %v", err)
			}
		})
	}
}

func TestLiveJoinAuthentication(t *testing.T) {
	for _, where := range []string{"session", "join", "unsupported"} {
		t.Run(where, func(t *testing.T) {
			token := protocol.NewToken()
			pair, err := protocol.GenerateKeyPair()
			if err != nil {
				t.Fatal(err)
			}
			mac := protocol.CollectorMAC(token.ExchangeKey(), token.SessionID(), pair.Public())
			backing := devrelay.New()
			relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/join") {
					if where == "unsupported" {
						http.NotFound(w, r)
						return
					}
					if where == "join" {
						w.WriteHeader(http.StatusCreated)
						_ = json.NewEncoder(w).Encode(protocol.JoinInfo{CollectorKey: protocol.EncodeKey(pair.Public()), CollectorMAC: protocol.EncodeKey([32]byte{})})
						return
					}
				}
				backing.ServeHTTP(w, r)
			}))
			defer relay.Close()
			client, err := protocol.NewRelayClient(relay.URL, token.RelayCredential())
			if err != nil {
				t.Fatal(err)
			}
			if where == "session" {
				mac[0] ^= 1
			}
			if _, err := client.CreatePersistentSession(t.Context(), token.SessionID(), time.Hour, pair.Public(), mac); err != nil {
				t.Fatal(err)
			}
			s := &Server{SessionPath: filepath.Join(t.TempDir(), "session.json"), Relay: relay.URL, AllowChatJoin: true}
			got, err := connect(t, s).CallTool(t.Context(), &mcp.CallToolParams{Name: "join", Arguments: map[string]any{"token": token.Encode()}})
			want := "aken-mcp: join rejected: bad authentication"
			if where == "unsupported" {
				want = "aken-mcp: this relay does not support persistent sessions"
			}
			if err != nil || !got.IsError || got.Content[0].(*mcp.TextContent).Text != want {
				t.Fatalf("authentication: %+v, %v", got, err)
			}
			if _, err := session.Load(s.SessionPath); !errors.Is(err, session.ErrNoSession) {
				t.Fatal("failed join saved session")
			}
		})
	}
}

func TestSubmitSaveFailure(t *testing.T) {
	s, client, keys, id := liveFixture(t)
	stored, err := session.Load(s.SessionPath)
	if err != nil {
		t.Fatal(err)
	}
	s.SessionPath = filepath.Join(s.SessionPath, "not-a-directory")
	job := protocol.Job{ID: "j1", Name: "ps", Params: json.RawMessage(`{}`)}
	if err := s.submit(t.Context(), keys, client, &stored, job); err == nil {
		t.Fatal("submission ignored save failure")
	}
	if jobs, err := client.PollJobs(t.Context(), id, 0); err != nil || len(jobs) != 0 {
		t.Fatalf("job posted despite save failure: %v", err)
	}
}

func TestLiveLargeResult(t *testing.T) {
	line := strings.Repeat("x", protocol.MaxResultPlaintext)
	out, _, err := renderResult(protocol.Result{ID: "j1", Status: "ok", Lines: []string{line}})
	if err != nil || out.Content[0].(*mcp.TextContent).Text != line+"\n" {
		t.Fatalf("live output was cut to artifact cap: %v", err)
	}
}
