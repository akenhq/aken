// SPDX-License-Identifier: Apache-2.0
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/akenhq/aken/internal/session"
	"github.com/akenhq/aken/protocol"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	jobWait    = 120 * time.Second
	resultWait = 60 * time.Second
)

type listDirArgs struct {
	Path string `json:"path" jsonschema:"absolute directory path; one outside the session scope needs the operator to add it at the terminal"`
}
type readFileArgs struct {
	Path string `json:"path"`
	From int    `json:"from,omitempty" jsonschema:"first line, default 1"`
	To   int    `json:"to,omitempty" jsonschema:"last line, default from+499; at most 500 lines per result"`
}
type searchFilesArgs struct {
	Glob   string `json:"glob" jsonschema:"absolute glob; one outside the session scope needs the operator to add it at the terminal"`
	Regex  string `json:"regex" jsonschema:"RE2 regular expression"`
	Since  string `json:"since,omitempty"`
	Before int    `json:"before,omitempty" jsonschema:"context lines before, 0 to 50"`
	After  int    `json:"after,omitempty" jsonschema:"context lines after, 0 to 50"`
	Max    int    `json:"max,omitempty" jsonschema:"matches per result, 1 to 200, default 50"`
	Cursor string `json:"cursor,omitempty" jsonschema:"next cursor from the previous result"`
}
type tailFileArgs struct {
	Path string `json:"path"`
	N    int    `json:"n,omitempty" jsonschema:"lines to return, 1 to 500, default 100"`
}
type journalArgs struct {
	Unit   string `json:"unit"`
	Since  string `json:"since,omitempty" jsonschema:"start of window, default 1h"`
	Until  string `json:"until,omitempty" jsonschema:"end of window, default now"`
	Regex  string `json:"regex,omitempty" jsonschema:"RE2 regular expression"`
	Tail   int    `json:"tail,omitempty" jsonschema:"last 1 to 500 lines; excludes regex, max and cursor"`
	Max    int    `json:"max,omitempty" jsonschema:"lines per result, 1 to 500, default 200"`
	Cursor string `json:"cursor,omitempty"`
}
type dockerLogsArgs struct {
	Container string `json:"container"`
	Since     string `json:"since,omitempty" jsonschema:"start of window, default 1h"`
	Until     string `json:"until,omitempty" jsonschema:"end of window, default now"`
	Regex     string `json:"regex,omitempty" jsonschema:"RE2 regular expression"`
	Tail      int    `json:"tail,omitempty" jsonschema:"last 1 to 500 lines; excludes regex, max and cursor"`
	Max       int    `json:"max,omitempty" jsonschema:"lines per result, 1 to 500, default 200"`
	Cursor    string `json:"cursor,omitempty"`
}
type systemctlStatusArgs struct {
	Unit string `json:"unit"`
}
type planJob struct {
	Name   string         `json:"name" jsonschema:"catalog name; search and tail are the catalog names for search_files and tail_file"`
	Params map[string]any `json:"params"`
}
type planArgs struct {
	Jobs []planJob `json:"jobs" jsonschema:"1 to 40 catalog jobs, in order; no nested plans"`
}
type resultArgs struct {
	ID string `json:"id" jsonschema:"job id from a pending call"`
}

func (s *Server) listDir(ctx context.Context, _ *mcp.CallToolRequest, in listDirArgs) (*mcp.CallToolResult, any, error) {
	return s.submitAndAwait(ctx, "list_dir", protocol.ListDirParams(in))
}
func (s *Server) readFile(ctx context.Context, req *mcp.CallToolRequest, in readFileArgs) (*mcp.CallToolResult, any, error) {
	in.From = defaultInt(req, "from", in.From, 1)
	in.To = defaultInt(req, "to", in.To, in.From+499)
	return s.submitAndAwait(ctx, "read_file", protocol.ReadFileParams(in))
}
func (s *Server) searchFiles(ctx context.Context, req *mcp.CallToolRequest, in searchFilesArgs) (*mcp.CallToolResult, any, error) {
	in.Max = defaultInt(req, "max", in.Max, 50)
	return s.submitAndAwait(ctx, "search", protocol.SearchParams(in))
}
func (s *Server) tailFile(ctx context.Context, req *mcp.CallToolRequest, in tailFileArgs) (*mcp.CallToolResult, any, error) {
	in.N = defaultInt(req, "n", in.N, 100)
	return s.submitAndAwait(ctx, "tail", protocol.TailParams(in))
}
func (s *Server) journal(ctx context.Context, _ *mcp.CallToolRequest, in journalArgs) (*mcp.CallToolResult, any, error) {
	return s.submitAndAwait(ctx, "journal", protocol.JournalParams(in))
}
func (s *Server) dockerLogs(ctx context.Context, _ *mcp.CallToolRequest, in dockerLogsArgs) (*mcp.CallToolResult, any, error) {
	return s.submitAndAwait(ctx, "docker_logs", protocol.DockerLogsParams(in))
}
func (s *Server) systemctlStatus(ctx context.Context, _ *mcp.CallToolRequest, in systemctlStatusArgs) (*mcp.CallToolResult, any, error) {
	return s.submitAndAwait(ctx, "systemctl_status", protocol.SystemctlStatusParams(in))
}
func (s *Server) ps(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
	return s.submitAndAwait(ctx, "ps", noArgs{})
}
func (s *Server) df(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
	return s.submitAndAwait(ctx, "df", noArgs{})
}

func (s *Server) submitAndAwait(ctx context.Context, name string, params any) (*mcp.CallToolResult, any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys, client, stored, err := s.live(ctx)
	if err != nil {
		return nil, nil, err
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, nil, errors.New("job: invalid parameters")
	}
	job := protocol.Job{ID: fmt.Sprintf("j%d", stored.NextJobSeq), Name: name, Params: raw}
	if err := s.submit(ctx, keys, client, &stored, job); err != nil {
		return nil, nil, err
	}
	if err := s.await(ctx, keys, client, &stored, []string{job.ID}, time.Now().Add(jobWait)); err != nil {
		return nil, nil, err
	}
	return renderResult(s.results[job.ID])
}

func (s *Server) submit(ctx context.Context, keys protocol.SessionKeys, client *protocol.RelayClient, stored *session.Session, job protocol.Job) error {
	raw, err := json.Marshal(job)
	if err != nil || len(raw) > protocol.MaxJobBytes-16 {
		return errors.New("job: parameters exceed the job limit")
	}
	seq := stored.NextJobSeq
	if seq >= protocol.MaxSessionSeq {
		return errors.New("session sequence exhausted; restart the collector's aken serve and join the new token")
	}
	stored.NextJobSeq++
	if err := session.Save(s.SessionPath, *stored); err != nil {
		return err
	}
	id, _ := protocol.ParseSessionID(stored.SessionID)
	envelope, err := keys.SealJob(id, seq, protocol.ClassRead, raw)
	if err != nil {
		return err
	}
	return liveRelayError(client.PostJob(ctx, id, envelope))
}

func liveRelayError(err error) error {
	var relayErr *protocol.RelayError
	if errors.As(err, &relayErr) && relayErr.Code == "bad_sequence" {
		return errors.New("session sequence mismatch; restart the collector's aken serve and join the new token")
	}
	return RelayLimitHint(err)
}

// RelayLimitHint turns a server_limit relay error into an error that names the way around it.
func RelayLimitHint(err error) error {
	var relayErr *protocol.RelayError
	if errors.As(err, &relayErr) && relayErr.Code == "server_limit" {
		return fmt.Errorf("%s. To avoid this limit, run your own relay: https://github.com/akenhq/aken/blob/main/docs/relay.md", relayErr.Message)
	}
	return err
}

func pending(id string) error {
	return fmt.Errorf("awaiting approval or still running in the server terminal; call result with id %s", id)
}

func (s *Server) await(ctx context.Context, keys protocol.SessionKeys, client *protocol.RelayClient, stored *session.Session, ids []string, deadline time.Time) error {
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	id, _ := protocol.ParseSessionID(stored.SessionID)
	for {
		missing := ""
		for _, jobID := range ids {
			if _, ok := s.results[jobID]; !ok {
				missing = jobID
				break
			}
		}
		if missing == "" {
			return nil
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return pending(missing)
		}
		envelopes, err := client.PollResults(ctx, id, 30*time.Second)
		if err != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return pending(missing)
			}
			return liveRelayError(err)
		}
		var sequenceError bool
		for _, envelope := range envelopes {
			raw, err := keys.OpenResult(envelope, id, stored.NextResultSeq)
			if err != nil {
				_, _ = fmt.Fprintf(os.Stderr, "aken-mcp: dropped a result: %v\n", err)
				sequenceError = sequenceError || errors.Is(err, protocol.ErrSequence)
				continue
			}
			stored.NextResultSeq++
			if err := session.Save(s.SessionPath, *stored); err != nil {
				return err
			}
			var got protocol.Result
			if json.Unmarshal(raw, &got) != nil || !protocol.ValidateJobID(got.ID) || (got.Status != "ok" && got.Status != "denied" && got.Status != "rejected" && got.Status != "error") {
				// A malformed result may still identify the job whose call must finish.
				var identity struct {
					ID string `json:"id"`
				}
				_ = json.Unmarshal(raw, &identity)
				got = protocol.Result{ID: identity.ID, Status: "error", Error: "invalid result from collector"}
			}
			s.results[got.ID] = got
		}
		if sequenceError {
			return errors.New("result sequence mismatch; restart the collector's aken serve and join the new token")
		}
	}
}

func renderResult(got protocol.Result) (*mcp.CallToolResult, any, error) {
	meta := map[string]any{"id": got.ID, "status": got.Status, "lines": len(got.Lines), "lines_redacted": got.Redaction.LinesRedacted, "flags": got.Redaction.Flags, "next": got.Next}
	text := strings.Join(got.Lines, "\n")
	if len(got.Lines) > 0 {
		text += "\n"
	}
	if got.Status != "ok" {
		meta["error"] = got.Error
		text = got.Status + ": " + got.Error + "\n" + text
	}
	out, _, err := result("", meta)
	if err != nil {
		return nil, nil, err
	}
	// Live results have a protocol cap of 256 KiB, above the artifact text cap.
	out.Content[0] = &mcp.TextContent{Text: text}
	out.IsError = got.Status != "ok"
	return out, nil, nil
}

func (s *Server) plan(ctx context.Context, _ *mcp.CallToolRequest, in planArgs) (*mcp.CallToolResult, any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys, client, stored, err := s.live(ctx)
	if err != nil {
		return nil, nil, err
	}
	if len(in.Jobs) < 1 || len(in.Jobs) > 40 {
		return nil, nil, errors.New("jobs: must contain 1 to 40 catalog jobs")
	}
	outerID := fmt.Sprintf("j%d", stored.NextJobSeq)
	params := protocol.PlanParams{}
	var ids []string
	for i, entry := range in.Jobs {
		if _, ok := protocol.CatalogClass(entry.Name); !ok {
			return nil, nil, errors.New("jobs: unknown catalog name")
		}
		if entry.Params == nil {
			return nil, nil, errors.New("jobs: params must be an object")
		}
		raw, err := json.Marshal(entry.Params)
		if err != nil {
			return nil, nil, errors.New("jobs: invalid parameters")
		}
		id := fmt.Sprintf("%s.%d", outerID, i+1)
		ids = append(ids, id)
		params.Jobs = append(params.Jobs, protocol.Job{ID: id, Name: entry.Name, Params: raw})
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, nil, err
	}
	if err := s.submit(ctx, keys, client, &stored, protocol.Job{ID: outerID, Name: "plan", Params: raw}); err != nil {
		return nil, nil, err
	}
	if err := s.await(ctx, keys, client, &stored, ids, time.Now().Add(jobWait)); err != nil {
		return nil, nil, err
	}
	out := &mcp.CallToolResult{}
	for i, id := range ids {
		got := s.results[id]
		block, _, err := renderResult(got)
		if err != nil {
			return nil, nil, err
		}
		text := block.Content[0].(*mcp.TextContent)
		text.Text = fmt.Sprintf("## %d %s %s\n", i+1, in.Jobs[i].Name, got.Status) + text.Text
		out.Content = append(out.Content, block.Content...)
		out.IsError = out.IsError || block.IsError
	}
	return out, nil, nil
}

func (s *Server) jobResult(ctx context.Context, _ *mcp.CallToolRequest, in resultArgs) (*mcp.CallToolResult, any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys, client, stored, err := s.live(ctx)
	if err != nil {
		return nil, nil, err
	}
	if !protocol.ValidateJobID(in.ID) {
		return nil, nil, errors.New("id: invalid job id")
	}
	if err := s.await(ctx, keys, client, &stored, []string{in.ID}, time.Now().Add(resultWait)); err != nil {
		return nil, nil, err
	}
	return renderResult(s.results[in.ID])
}
