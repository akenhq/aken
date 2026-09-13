// SPDX-License-Identifier: Apache-2.0
package serve

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/akenhq/aken/protocol"
)

type sessionInfo struct {
	SessionID string    `json:"session_id"`
	Relay     string    `json:"relay"`
	Level     int       `json:"level"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	JoinedAt  time.Time `json:"joined_at"`
	JoinedVia string    `json:"joined_via"`
	EndedAt   time.Time `json:"ended_at"`
	Argv      []string  `json:"argv"`
}

type auditEvent struct {
	Time          time.Time       `json:"time"`
	Seq           uint64          `json:"seq"`
	JobID         string          `json:"job_id"`
	Name          string          `json:"name"`
	Event         string          `json:"event"`
	Params        json.RawMessage `json:"params"`
	Paths         []string        `json:"paths"`
	Status        string          `json:"status"`
	Lines         int             `json:"lines"`
	LinesRedacted int64           `json:"lines_redacted"`
	Flags         int64           `json:"flags"`
	Error         string          `json:"error"`
}

type audit struct {
	dir         string
	log         *os.File
	session     sessionInfo
	mappingSize int
}

func newAudit(o Options, id protocol.SessionID, created, expires time.Time) (*audit, error) {
	dir := filepath.Join(o.StateDir, "sessions", created.UTC().Format("20060102T150405Z")+"-"+id.String()[:8])
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return nil, err
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Mkdir(filepath.Join(dir, "results"), 0o700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	log, err := root.OpenFile("jobs.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	a := &audit{dir: dir, log: log, session: sessionInfo{SessionID: id.String(), Relay: o.RelayURL, Level: o.Level, CreatedAt: created, ExpiresAt: expires, Argv: o.Argv}, mappingSize: -1}
	if err := a.writeSession(); err != nil {
		_ = log.Close()
		return nil, err
	}
	return a, nil
}

func atomicJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".aken-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if _, err = file.Write(append(data, '\n')); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
func (a *audit) writeSession() error {
	return atomicJSON(filepath.Join(a.dir, "session.json"), a.session)
}
func (a *audit) mapping(values map[string]string) error {
	if len(values) == a.mappingSize {
		return nil
	}
	if err := atomicJSON(filepath.Join(a.dir, "mapping.json"), values); err != nil {
		return err
	}
	a.mappingSize = len(values)
	return nil
}
func (a *audit) event(now time.Time, seq uint64, p preparedJob, event string, r protocol.Result) error {
	params := p.job.Params
	if !json.Valid(params) {
		params = json.RawMessage(`{}`)
	}
	return json.NewEncoder(a.log).Encode(auditEvent{Time: now.UTC(), Seq: seq, JobID: p.job.ID, Name: p.job.Name, Event: event, Params: params, Paths: p.paths, Status: r.Status, Lines: len(r.Lines), LinesRedacted: r.Redaction.LinesRedacted, Flags: r.Redaction.Flags, Error: r.Error})
}
func (a *audit) result(seq uint64, result protocol.Result) error {
	root, err := os.OpenRoot(filepath.Join(a.dir, "results"))
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	file, err := root.OpenFile(strconv.FormatUint(seq, 10)+".txt", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	for _, line := range result.Lines {
		if _, err = fmt.Fprintln(file, line); err != nil {
			_ = file.Close()
			return err
		}
	}
	if err = file.Chmod(0o400); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
