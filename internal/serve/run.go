// SPDX-License-Identifier: Apache-2.0
package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/akenhq/aken/internal/collect"
	"github.com/akenhq/aken/internal/redact"
	"github.com/akenhq/aken/internal/screen"
	"github.com/akenhq/aken/internal/source"
	"github.com/akenhq/aken/protocol"
)

type session struct {
	o                            Options
	client                       *protocol.RelayClient
	id                           protocol.SessionID
	keys                         protocol.SessionKeys
	engine                       *redact.Engine
	audit                        *audit
	files                        source.Files
	stdin                        *screen.Input
	stdout, stderr               io.Writer
	pageLines                    int
	nextJob, nextResult          uint64
	jobs, sent, denied, rejected int
}

func Run(ctx context.Context, o Options, stdin *screen.Input, stdout, stderr io.Writer, pageLines int) int {
	fail := func(err error) int { _, _ = fmt.Fprintf(stderr, "aken: %s\n", err); return 1 }
	if err := validate(o); err != nil {
		return fail(err)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.StateDir == "" {
		o.StateDir = collect.DefaultStateDir()
	}
	for i, path := range o.Allow {
		o.Allow[i] = filepath.Clean(path)
	}
	client, err := protocol.NewRelayClient(o.RelayURL, [32]byte{})
	if err != nil {
		return fail(err)
	}
	info, err := client.Info(ctx)
	if err != nil {
		return fail(err)
	}
	if info.Caps.SessionTTLDefaultSeconds == 0 {
		return fail(fmt.Errorf("relay %s does not support live sessions", visible(o.RelayURL)))
	}
	engine, _, rulesPath, defaults, extras, err := collect.LoadRules(o.RulesFile, o.Keep, o.KeepCategories)
	if err != nil {
		return fail(err)
	}
	created := o.Now().UTC()
	if o.Retention > 0 {
		if err := collect.Prune(filepath.Join(o.StateDir, "sessions"), created.Add(-o.Retention)); err != nil {
			return fail(err)
		}
	}
	token := protocol.NewToken()
	defer token.Zero()
	id, exchange := token.SessionID(), token.ExchangeKey()
	client, err = protocol.NewRelayClient(o.RelayURL, token.RelayCredential())
	if err != nil {
		return fail(err)
	}
	pair, err := protocol.GenerateKeyPair()
	if err != nil {
		return fail(err)
	}
	remote, err := client.CreatePersistentSession(ctx, id, o.TTL, pair.Public(), protocol.CollectorMAC(exchange, id, pair.Public()))
	if err != nil {
		return fail(err)
	}
	a, err := newAudit(o, id, created, remote.ExpiresAt)
	if err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = client.DeleteSession(cleanupCtx, id)
		return fail(err)
	}
	lifetime, cancelLife := context.WithTimeout(ctx, max(0, remote.ExpiresAt.Sub(o.Now())))
	defer cancelLife()
	active, cancel := context.WithCancelCause(lifetime)
	defer cancel(nil)
	s := session{o: o, client: client, id: id, engine: engine, audit: a, files: source.Files{Allowed: append([]string{"/var/log"}, o.Allow...)}, stdin: stdin.Until(active), stdout: stdout, stderr: stderr, pageLines: pageLines, nextJob: 1, nextResult: 1}
	_, _ = fmt.Fprintf(stdout, "aken serve: session open on %s, level %d, expires %s\nScope    %s\nLocal    %s\nRedaction   %d rules (%d default", visible(o.RelayURL), o.Level, remote.ExpiresAt.UTC().Format(time.RFC3339), visible(strings.Join(s.files.Allowed, ", ")), visible(a.dir), defaults+extras, defaults)
	if rulesPath != "" {
		_, _ = fmt.Fprintf(stdout, ", %d from %s", extras, visible(rulesPath))
	}
	_, _ = fmt.Fprint(stdout, ")")
	if len(o.Keep) > 0 {
		_, _ = fmt.Fprintf(stdout, "; kept: %s", visible(strings.Join(o.Keep, ", ")))
	}
	if len(o.KeepCategories) > 0 {
		_, _ = fmt.Fprintf(stdout, "; off: %s", visible(strings.Join(o.KeepCategories, ", ")))
	}
	_, _ = fmt.Fprintf(stdout, "\n\nSession token. Paste it into `aken-mcp join` on your machine, not into the agent chat:\n\n  %s\n\nWaiting for the local MCP to join. Ctrl-C ends the session.\n", token.Encode())
	token.Zero()
	err = s.join(active, pair, exchange)
	if err == nil {
		jobs := make(chan protocol.Envelope, 64)
		go poll(active, client, id, jobs, cancel)
		err = s.loop(active, jobs)
	}
	cancel(nil)
	if errors.Is(err, screen.ErrInterrupted) {
		// Raw mode swallows the signal, so Ctrl-C at a prompt ends the session here.
		_, _ = fmt.Fprintln(stdout, "Ctrl-C: ending the session.")
		err = nil
	}
	code := 0
	if ctx.Err() == nil && err != nil {
		// The relay's clock and the local lifetime timer race at expiry; either signal means the session expired.
		var relayErr *protocol.RelayError
		relayEnded := (errors.As(context.Cause(active), &relayErr) || errors.As(err, &relayErr)) && (relayErr.Status == 404 || relayErr.Status == 409)
		if errors.Is(lifetime.Err(), context.DeadlineExceeded) || (relayEnded && !o.Now().Before(remote.ExpiresAt)) {
			err = fmt.Errorf("the session expired at %s", remote.ExpiresAt.UTC().Format(time.RFC3339))
		} else if relayEnded {
			err = errors.New("the session was ended on the relay")
		}
		code = fail(err)
	}
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cleanupCancel()
	if err := client.DeleteSession(cleanupCtx, id); err != nil && !protocol.IsNotFound(err) {
		code = fail(err)
	}
	if err := a.mapping(engine.Mapping()); err != nil {
		code = fail(err)
	}
	a.session.EndedAt = o.Now().UTC()
	if err := a.writeSession(); err != nil {
		code = fail(err)
	}
	if err := a.log.Close(); err != nil {
		code = fail(err)
	}
	_, _ = fmt.Fprintf(stdout, "Session ended: %d jobs, %d sent, %d denied, %d rejected. Local copy: %s\n", s.jobs, s.sent, s.denied, s.rejected, visible(a.dir))
	return code
}

func (s *session) join(ctx context.Context, pair protocol.KeyPair, exchange [32]byte) error {
	for {
		join, joined, err := s.client.WaitJoin(ctx, s.id, min(30*time.Second, s.audit.session.ExpiresAt.Sub(s.o.Now())))
		if err != nil {
			// A 404 here means the session is gone; an unsupported relay was refused before the session was created.
			return err
		}
		if !joined {
			continue
		}
		key, ok := protocol.DecodeKey(join.MCPKey)
		mac, macOK := protocol.DecodeKey(join.MCPMAC)
		if !ok || !macOK || !protocol.VerifyMCPMAC(exchange, s.id, pair.Public(), key, mac) {
			_, _ = fmt.Fprintln(s.stderr, "aken: join rejected: bad authentication")
			timer := time.NewTimer(min(30*time.Second, max(0, s.audit.session.ExpiresAt.Sub(s.o.Now()))))
			select {
			case <-ctx.Done():
				timer.Stop()
				return context.Cause(ctx)
			case <-timer.C:
			}
			continue
		}
		root, err := protocol.ContentRoot(pair, key, s.id, pair.Public(), key)
		if err != nil {
			return err
		}
		s.keys = protocol.DeriveSessionKeys(root)
		s.audit.session.JoinedAt = join.JoinedAt
		s.audit.session.JoinedVia = join.Via
		stamp := join.JoinedAt.UTC().Format("15:04:05Z")
		if join.Via == "chat" && s.o.Level == 0 {
			s.o.Level = 1
			s.audit.session.Level = 1
			_, _ = fmt.Fprintf(s.stdout, "Joined via chat at %s: the token has been in a transcript, so this session runs at level 1.\n", stamp)
		} else {
			_, _ = fmt.Fprintf(s.stdout, "Joined via %s at %s. Waiting for jobs.\n", visible(join.Via), stamp)
		}
		return s.audit.writeSession()
	}
}

func poll(ctx context.Context, client *protocol.RelayClient, id protocol.SessionID, jobs chan<- protocol.Envelope, cancel context.CancelCauseFunc) {
	defer close(jobs)
	for {
		messages, err := client.PollJobs(ctx, id, 30*time.Second)
		if err != nil {
			cancel(err)
			return
		}
		for _, e := range messages {
			select {
			case jobs <- e:
			case <-ctx.Done():
				return
			}
		}
	}
}
func (s *session) loop(ctx context.Context, jobs <-chan protocol.Envelope) error {
	for {
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case envelope, ok := <-jobs:
			if !ok {
				return context.Cause(ctx)
			}
			data, err := s.keys.OpenJob(envelope, s.id, s.nextJob)
			if err != nil {
				_, _ = fmt.Fprintf(s.stderr, "aken: dropped a job: %s\n", err)
				continue
			}
			s.nextJob++
			if err := s.handle(ctx, envelope, data); err != nil {
				return err
			}
			if envelope.Seq == protocol.MaxSessionSeq || s.nextResult > protocol.MaxSessionSeq {
				return nil
			}
		}
	}
}
func (s *session) handle(ctx context.Context, envelope protocol.Envelope, data []byte) error {
	var job protocol.Job
	if err := decode(data, &job); err != nil {
		return s.reject(ctx, envelope.Seq, preparedJob{job: job}, "invalid job")
	}
	jobs := []protocol.Job{job}
	if job.Name == "plan" {
		var plan protocol.PlanParams
		if !protocol.ValidateJobID(job.ID) || decode(job.Params, &plan) != nil || len(plan.Jobs) < 1 || len(plan.Jobs) > 40 {
			return s.reject(ctx, envelope.Seq, preparedJob{job: job}, "invalid plan")
		}
		for _, j := range plan.Jobs {
			if j.Name == "plan" {
				return s.reject(ctx, envelope.Seq, preparedJob{job: job}, "nested plan")
			}
		}
		jobs = plan.Jobs
	}
	prepared := make([]preparedJob, len(jobs))
	validation := make([]error, len(jobs))
	var rows []preparedJob
	for i, j := range jobs {
		p, err := prepare(ctx, j, envelope.Class, s.files, s.o.Now())
		prepared[i] = p
		validation[i] = err
		if err := s.audit.event(s.o.Now(), envelope.Seq, p, "received", protocol.Result{}); err != nil {
			return err
		}
		if err != nil {
			continue
		}
		rows = append(rows, p)
	}
	approved := s.o.Level == 0
	var err error
	if len(rows) > 0 && s.o.Level == 1 {
		approved, err = approve(s.stdin, s.stdout, job, rows, s.pageLines)
		if err != nil {
			return err
		}
	}
	for i, p := range prepared {
		if err := validation[i]; err != nil {
			if err := s.reject(ctx, envelope.Seq, p, err.Error()); err != nil {
				return err
			}
			continue
		}
		if !approved {
			r := protocol.Result{ID: p.job.ID, Status: "denied", Error: "denied by user"}
			if err := s.audit.event(s.o.Now(), envelope.Seq, p, "denied", r); err != nil {
				return err
			}
			if err := s.send(ctx, p, r); err != nil {
				return err
			}
			continue
		}
		if err := s.audit.event(s.o.Now(), envelope.Seq, p, "approved", protocol.Result{}); err != nil {
			return err
		}
		lines, next, runErr := execute(ctx, p, s.files)
		result := protocol.Result{ID: p.job.ID, Status: "ok", Next: next}
		if runErr != nil {
			result.Status = "error"
			result.Error = runErr.Error()
		}
		raw := make([][]byte, len(lines))
		for i, line := range lines {
			raw[i] = []byte(line)
		}
		redacted := s.engine.Redact(raw)
		result.Redaction = protocol.ResultRedaction{LinesRedacted: redacted.LinesRedacted, ByCategory: redacted.ByCategory}
		for _, line := range redacted.Lines {
			result.Lines = append(result.Lines, string(line))
		}
		flags := map[string]bool{}
		for _, f := range redacted.Flags {
			if !f.IDShaped {
				flags[f.Value] = true
			}
		}
		result.Redaction.Flags = int64(len(flags))
		capResult(&result, p)
		clear(flags)
		for _, f := range redacted.Flags {
			if !f.IDShaped && f.Line <= len(result.Lines) {
				flags[f.Value] = true
			}
		}
		result.Redaction.Flags = int64(len(flags))
		if s.o.Level == 1 && len(flags) > 0 {
			send, err := reviewFlags(s.stdin, s.stdout, p, result, redacted.Flags, s.o.Now())
			if err != nil {
				return err
			}
			if !send {
				result = protocol.Result{ID: p.job.ID, Status: "denied", Error: "dropped after review"}
				if err := s.audit.event(s.o.Now(), envelope.Seq, p, "dropped", result); err != nil {
					return err
				}
			}
		}
		if err := s.send(ctx, p, result); err != nil {
			return err
		}
	}
	return nil
}
func (s *session) reject(ctx context.Context, seq uint64, p preparedJob, message string) error {
	r := protocol.Result{ID: p.job.ID, Status: "rejected", Error: message}
	if err := s.audit.event(s.o.Now(), seq, p, "rejected", r); err != nil {
		return err
	}
	return s.send(ctx, p, r)
}
func (s *session) send(ctx context.Context, p preparedJob, r protocol.Result) error {
	if s.nextResult > protocol.MaxSessionSeq {
		return errors.New("result sequence exhausted")
	}
	if err := s.audit.result(s.nextResult, r); err != nil {
		return err
	}
	if err := s.audit.mapping(s.engine.Mapping()); err != nil {
		return err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	envelope, err := s.keys.SealResult(s.id, s.nextResult, protocol.ClassRead, data)
	if err != nil {
		return err
	}
	if err := s.client.PostResult(ctx, s.id, envelope); err != nil {
		return err
	}
	if err := s.audit.event(s.o.Now(), s.nextResult, p, "sent", r); err != nil {
		return err
	}
	s.nextResult++
	s.jobs++
	switch r.Status {
	case "ok":
		s.sent++
	case "denied":
		s.denied++
	case "rejected":
		s.rejected++
	}
	s.summary(p, r)
	return nil
}
func (s *session) summary(p preparedJob, r protocol.Result) {
	target := p.target
	if target == "" {
		var q protocol.ListDirParams
		_ = json.Unmarshal(p.job.Params, &q)
		target = q.Path
	}
	_, _ = fmt.Fprintf(s.stdout, "%s  %s %s  ", s.o.Now().UTC().Format("15:04:05Z"), visible(p.job.Name), visible(target))
	if r.Status != "ok" {
		_, _ = fmt.Fprintf(s.stdout, "%s: %s\n", r.Status, visible(r.Error))
		return
	}
	var categories, counts []string
	for c, n := range r.Redaction.ByCategory {
		if n.Lines > 0 {
			categories = append(categories, c)
		}
	}
	slices.Sort(categories)
	for _, c := range categories {
		counts = append(counts, fmt.Sprintf("%s %d values", c, r.Redaction.ByCategory[c].Values))
	}
	_, _ = fmt.Fprintf(s.stdout, "%d lines sent, %d redacted", len(r.Lines), r.Redaction.LinesRedacted)
	if len(counts) > 0 {
		_, _ = fmt.Fprintf(s.stdout, " (%s)", strings.Join(counts, ", "))
	}
	_, _ = fmt.Fprintf(s.stdout, ", %d flags\n", r.Redaction.Flags)
}
func capResult(r *protocol.Result, p preparedJob) {
	size := 0
	for i, line := range r.Lines {
		size += len(line) + 1
		if size > protocol.MaxResultPlaintext {
			r.Next = cutCursor(p, r.Lines, i)
			r.Lines = r.Lines[:i]
			break
		}
	}
	// JSON escapes controls, so the line-text cap alone does not guarantee an envelope fits.
	for len(r.Lines) > 0 {
		data, _ := json.Marshal(r)
		if len(data) <= protocol.MaxResultBytes-16 {
			break
		}
		i := len(r.Lines) - 1
		r.Next = cutCursor(p, r.Lines, i)
		r.Lines = r.Lines[:i]
	}
}
func cutCursor(p preparedJob, lines []string, i int) string {
	line := lines[i]
	switch p.job.Name {
	case "read_file", "tail":
		n, _, _ := strings.Cut(line, ":")
		return n
	case "list_dir":
		parts := strings.SplitN(line, " ", 4)
		if len(parts) == 4 {
			return parts[3]
		}
	case "search":
		for index, path := range p.paths {
			if rest, ok := strings.CutPrefix(line, path+":"); ok {
				n, _, _ := strings.Cut(rest, ":")
				n, _, _ = strings.Cut(n, "-")
				return fmt.Sprintf("%d:%s", index, n)
			}
		}
	case "journal", "docker_logs":
		start, _ := strconv.Atoi(p.journal.Cursor)
		return strconv.Itoa(start + i)
	}
	return strconv.Itoa(i)
}
