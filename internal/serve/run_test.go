// SPDX-License-Identifier: Apache-2.0
package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akenhq/aken/internal/devrelay"
	"github.com/akenhq/aken/protocol"
)

type output struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	changed chan struct{}
}

func newOutput() *output { return &output{changed: make(chan struct{}, 1)} }
func (o *output) Write(p []byte) (int, error) {
	o.mu.Lock()
	n, err := o.buf.Write(p)
	o.mu.Unlock()
	select {
	case o.changed <- struct{}{}:
	default:
	}
	return n, err
}
func (o *output) String() string { o.mu.Lock(); defer o.mu.Unlock(); return o.buf.String() }
func (o *output) wait(t *testing.T, text string) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for !strings.Contains(o.String(), text) {
		select {
		case <-o.changed:
		case <-timer.C:
			t.Fatalf("waiting for %q; output: %s", text, o.String())
		}
	}
}

type liveTest struct {
	o                 Options
	out, errs         *output
	ctx               context.Context
	cancel            context.CancelFunc
	done              chan int
	client            *protocol.RelayClient
	token             protocol.Token
	keys              protocol.SessionKeys
	jobSeq, resultSeq uint64
	stopped           bool
}

func startLive(t *testing.T, level int, input io.Reader, ttl time.Duration) *liveTest {
	t.Helper()
	server := httptest.NewServer(devrelay.New())
	t.Cleanup(server.Close)
	dir := t.TempDir()
	o := Options{Level: level, TTL: ttl, RelayURL: server.URL, Allow: []string{dir}, StateDir: t.TempDir(), Now: time.Now, Argv: []string{"serve"}}
	ctx, cancel := context.WithCancel(context.Background())
	h := &liveTest{o: o, out: newOutput(), errs: newOutput(), ctx: ctx, cancel: cancel, done: make(chan int, 1), jobSeq: 1, resultSeq: 1}
	go func() { h.done <- Run(ctx, o, input, h.out, h.errs, 40) }()
	t.Cleanup(func() {
		if !h.stopped {
			h.stop(t)
		}
		h.token.Zero()
	})
	h.out.wait(t, "Waiting for the local MCP")
	encoded := regexp.MustCompile(`akn1_[a-z2-7]{52}`).FindString(h.out.String())
	var err error
	h.token, err = protocol.ParseToken(encoded)
	if err != nil {
		t.Fatal(err)
	}
	h.client, err = protocol.NewRelayClient(server.URL, h.token.RelayCredential())
	if err != nil {
		t.Fatal(err)
	}
	return h
}
func (h *liveTest) join(t *testing.T, via string, bad bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(h.ctx, 5*time.Second)
	defer cancel()
	info, err := h.client.Session(ctx, h.token.SessionID())
	if err != nil {
		t.Fatal(err)
	}
	collector, ok := protocol.DecodeKey(info.CollectorKey)
	if !ok {
		t.Fatal("collector key")
	}
	mac, ok := protocol.DecodeKey(info.CollectorMAC)
	if !ok || !protocol.VerifyCollectorMAC(h.token.ExchangeKey(), h.token.SessionID(), collector, mac) {
		t.Fatal("collector MAC")
	}
	pair, err := protocol.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	mac = protocol.MCPMAC(h.token.ExchangeKey(), h.token.SessionID(), collector, pair.Public())
	if bad {
		mac[0] ^= 1
	}
	if _, err = h.client.Join(ctx, h.token.SessionID(), pair.Public(), mac, via); err != nil {
		t.Fatal(err)
	}
	if bad {
		h.errs.wait(t, "join rejected: bad authentication")
		return
	}
	root, err := protocol.ContentRoot(pair, collector, h.token.SessionID(), collector, pair.Public())
	if err != nil {
		t.Fatal(err)
	}
	h.keys = protocol.DeriveSessionKeys(root)
	h.out.wait(t, "Joined via "+via)
}
func (h *liveTest) post(t *testing.T, job protocol.Job, class uint8) {
	t.Helper()
	data, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := h.keys.SealJob(h.token.SessionID(), h.jobSeq, class, data)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(h.ctx, 5*time.Second)
	defer cancel()
	if err = h.client.PostJob(ctx, h.token.SessionID(), envelope); err != nil {
		t.Fatal(err)
	}
	h.jobSeq++
}
func (h *liveTest) results(t *testing.T, n int) []protocol.Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(h.ctx, 5*time.Second)
	defer cancel()
	var results []protocol.Result
	for len(results) < n {
		envelopes, err := h.client.PollResults(ctx, h.token.SessionID(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range envelopes {
			data, err := h.keys.OpenResult(e, h.token.SessionID(), h.resultSeq)
			if err != nil {
				t.Fatal(err)
			}
			h.resultSeq++
			var r protocol.Result
			if err = json.Unmarshal(data, &r); err != nil {
				t.Fatal(err)
			}
			results = append(results, r)
		}
	}
	return results
}
func (h *liveTest) stop(t *testing.T) int {
	t.Helper()
	h.cancel()
	h.stopped = true
	select {
	case code := <-h.done:
		return code
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop")
		return -1
	}
}
func job(id, name string, params any) protocol.Job {
	data, _ := json.Marshal(params)
	return protocol.Job{ID: id, Name: name, Params: data}
}
func writeLog(t *testing.T, h *liveTest, body string) string {
	t.Helper()
	path := filepath.Join(h.o.Allow[0], "app.log")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLiveDecisions(t *testing.T) {
	for _, tt := range []struct {
		name, via, input, body, status string
		level                          int
		screen                         bool
		flags                          int64
	}{
		{"approve", "cli", "v\na\n", "from 203.0.113.5\n", "ok", 1, true, 0},
		{"deny", "cli", "d\n", "ordinary\n", "denied", 1, true, 0},
		{"level zero", "cli", "", "from 203.0.113.5\n", "ok", 0, false, 0},
		{"chat forces review", "chat", "a\n", "ordinary\n", "ok", 0, true, 0},
		{"flag send", "cli", "a\ns\n", "aB3dE5gH7jK9mN1pQ2sT4vW6\n", "ok", 1, true, 1},
		{"flag drop", "cli", "a\nd\n", "aB3dE5gH7jK9mN1pQ2sT4vW6\n", "denied", 1, true, 0},
		{"flag zero", "cli", "", "aB3dE5gH7jK9mN1pQ2sT4vW6\n", "ok", 0, false, 1},
		{"hex does not pause", "cli", "a\n", "0123456789abcdef0123456789abcdef\n", "ok", 1, true, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := startLive(t, tt.level, strings.NewReader(tt.input), time.Hour)
			path := writeLog(t, h, tt.body)
			h.join(t, tt.via, false)
			h.post(t, job("j1", "read_file", protocol.ReadFileParams{Path: path}), protocol.ClassRead)
			r := h.results(t, 1)[0]
			if r.Status != tt.status || r.Redaction.Flags != tt.flags {
				t.Fatalf("result: %+v", r)
			}
			if r.Status == "ok" && strings.Contains(tt.body, "203.0.113.5") && (r.Lines[0] != "1: from <ip#1>" || r.Redaction.LinesRedacted != 1) {
				t.Fatalf("redaction: %+v", r)
			}
			if tt.name == "flag drop" && (r.Error != "dropped after review" || len(r.Lines) != 0) {
				t.Fatalf("drop: %+v", r)
			}
			h.out.wait(t, map[bool]string{true: "lines sent", false: "denied:"}[r.Status == "ok"])
			if code := h.stop(t); code != 0 {
				t.Fatalf("exit %d: %s", code, h.errs.String())
			}
			if strings.Contains(h.out.String(), "Job j1 from the agent") != tt.screen {
				t.Fatal(h.out.String())
			}
			if strings.Count(h.out.String(), h.token.Encode()) != 1 {
				t.Fatal("token not printed exactly once")
			}
			if !strings.Contains(h.out.String(), "Session ended: 1 jobs") {
				t.Fatal(h.out.String())
			}
			dirs, err := filepath.Glob(filepath.Join(h.o.StateDir, "sessions", "*"))
			if err != nil || len(dirs) != 1 {
				t.Fatal(dirs, err)
			}
			var info sessionInfo
			data, err := os.ReadFile(filepath.Join(dirs[0], "session.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(data, &info); err != nil || info.EndedAt.IsZero() || info.JoinedAt.IsZero() || info.JoinedVia != tt.via {
				t.Fatalf("session: %+v %v", info, err)
			}
			if tt.via == "chat" && info.Level != 1 {
				t.Fatal("chat level not saved")
			}
			data, err = os.ReadFile(filepath.Join(dirs[0], "results", "1.txt"))
			if err != nil {
				t.Fatal(err)
			}
			want := ""
			for _, line := range r.Lines {
				want += line + "\n"
			}
			if string(data) != want {
				t.Fatalf("audit result %q != %q", data, want)
			}
			if err := filepath.WalkDir(dirs[0], func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					return nil
				}
				data, err := os.ReadFile(path)
				if bytes.Contains(data, []byte(h.token.Encode())) {
					t.Error("token in audit")
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLivePlanAndRejections(t *testing.T) {
	for _, deny := range []bool{false, true} {
		t.Run(fmt.Sprint(deny), func(t *testing.T) {
			input := "a\n"
			if deny {
				input = "d\n"
			}
			h := startLive(t, 1, strings.NewReader(input), time.Hour)
			path := writeLog(t, h, "203.0.113.5\nsecond\n")
			h.join(t, "cli", false)
			plan := protocol.PlanParams{Jobs: []protocol.Job{job("j1.1", "read_file", protocol.ReadFileParams{Path: path, To: 1}), job("j1.2", "tail", protocol.TailParams{Path: path, N: 2})}}
			h.post(t, job("j1", "plan", plan), protocol.ClassRead)
			results := h.results(t, 2)
			for i, r := range results {
				if r.ID != plan.Jobs[i].ID || (r.Status == "denied") != deny {
					t.Fatalf("results: %+v", results)
				}
			}
			if !deny && (results[0].Lines[0] != "1: <ip#1>" || results[1].Lines[0] != "1: <ip#1>") {
				t.Fatal("unstable placeholders", results)
			}
			h.out.wait(t, map[bool]string{true: "denied:", false: "2 lines sent"}[deny])
			h.stop(t)
			if strings.Count(h.out.String(), "[a] approve") != 1 || !strings.Contains(h.out.String(), "plan of 2 reads") {
				t.Fatal(h.out.String())
			}
		})
	}
	h := startLive(t, 0, strings.NewReader(""), time.Hour)
	writeLog(t, h, "ordinary\n")
	h.join(t, "cli", false)
	for i, tt := range []struct {
		job    protocol.Job
		class  uint8
		status string
	}{
		{job("j1", "exec", map[string]string{}), 1, "rejected"},
		{job("j2", "read_file", protocol.ReadFileParams{Path: "/etc/shadow"}), 1, "rejected"},
		{job("j4", "plan", protocol.PlanParams{}), 1, "rejected"},
		{job("j5", "plan", protocol.PlanParams{Jobs: []protocol.Job{job("nested", "plan", protocol.PlanParams{})}}), 1, "rejected"},
		{job("j6", "read_file", protocol.ReadFileParams{Path: filepath.Join(h.o.Allow[0], "missing")}), 1, "error"},
	} {
		h.post(t, tt.job, tt.class)
		r := h.results(t, 1)[0]
		if r.Status != tt.status || r.ID != tt.job.ID {
			t.Fatalf("case %d: %+v", i, r)
		}
		if strings.Contains(r.Error, "/etc") {
			t.Fatal("outside path in result error")
		}
	}
	h.out.wait(t, "cannot read file")
	h.stop(t)
}

func TestLiveEnding(t *testing.T) {
	t.Run("bad join", func(t *testing.T) {
		h := startLive(t, 0, strings.NewReader(""), time.Hour)
		h.join(t, "cli", true)
		if code := h.stop(t); code != 0 {
			t.Fatal(code)
		}
		if strings.Contains(h.out.String(), "Joined via") {
			t.Fatal("accepted bad MAC")
		}
	})
	t.Run("relay deletion while reviewing", func(t *testing.T) {
		reader, writer := io.Pipe()
		defer func() { _ = reader.Close() }()
		defer func() { _ = writer.Close() }()
		h := startLive(t, 1, reader, time.Hour)
		path := writeLog(t, h, "ordinary\n")
		h.join(t, "cli", false)
		h.post(t, job("j1", "tail", protocol.TailParams{Path: path}), 1)
		h.out.wait(t, "[a] approve")
		if err := h.client.DeleteSession(h.ctx, h.token.SessionID()); err != nil {
			t.Fatal(err)
		}
		select {
		case code := <-h.done:
			h.stopped = true
			if code != 1 {
				t.Fatal(code)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("blocked at approval")
		}
		if got := h.errs.String(); got != "aken: the session was ended on the relay\n" {
			t.Fatalf("stderr = %q", got)
		}
		if !strings.Contains(h.out.String(), "Session ended:") {
			t.Fatal("missing ending summary")
		}
	})
	t.Run("expiry before join", func(t *testing.T) {
		h := startLive(t, 0, strings.NewReader(""), time.Second)
		select {
		case code := <-h.done:
			h.stopped = true
			if code != 1 {
				t.Fatal(code)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("did not expire")
		}
		expires := regexp.MustCompile(`expires ([^\n]+)`).FindStringSubmatch(h.out.String())
		if len(expires) != 2 {
			t.Fatal("missing expiry time")
		}
		if got, want := h.errs.String(), "aken: the session expired at "+expires[1]+"\n"; got != want {
			t.Fatalf("stderr = %q, want %q", got, want)
		}
		if !strings.Contains(h.out.String(), "Session ended:") {
			t.Fatal("missing ending summary")
		}
	})
	t.Run("authentication and sequence", func(t *testing.T) {
		h := startLive(t, 0, strings.NewReader(""), time.Hour)
		h.join(t, "cli", false)
		data, _ := json.Marshal(job("j1", "df", map[string]string{}))
		e, err := h.keys.SealJob(h.token.SessionID(), 1, 1, data)
		if err != nil {
			t.Fatal(err)
		}
		e.Payload[0] ^= 1
		if err = h.client.PostJob(h.ctx, h.token.SessionID(), e); err != nil {
			t.Fatal(err)
		}
		h.jobSeq = 2
		h.post(t, job("j2", "df", map[string]string{}), 1)
		h.errs.wait(t, "unexpected sequence number")
		h.stop(t)
		if strings.Count(h.errs.String(), "dropped a job") != 2 || !strings.Contains(h.out.String(), "Session ended: 0 jobs") {
			t.Fatal(h.errs.String(), h.out.String())
		}
	})
}

func TestUnsupportedRelayAndOptions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/info" {
			t.Error("unexpected request", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(protocol.Info{})
	}))
	defer server.Close()
	o := Options{TTL: time.Hour, RelayURL: server.URL, StateDir: t.TempDir()}
	var out, errs bytes.Buffer
	if code := Run(context.Background(), o, strings.NewReader(""), &out, &errs, 40); code != 1 || !strings.Contains(errs.String(), "does not support live sessions") || out.Len() != 0 {
		t.Fatalf("code %d, %s %s", code, &out, &errs)
	}
	for _, change := range []func(*Options){func(o *Options) { o.Level = 2 }, func(o *Options) { o.TTL = 0 }, func(o *Options) { o.TTL = 25 * time.Hour }, func(o *Options) { o.Retention = -1 }, func(o *Options) { o.Allow = []string{"relative"} }, func(o *Options) { o.RelayURL = "bad" }} {
		bad := o
		change(&bad)
		if validate(bad) == nil {
			t.Fatal("accepted invalid options")
		}
	}
}
