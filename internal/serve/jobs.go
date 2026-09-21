// SPDX-License-Identifier: Apache-2.0
package serve

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/akenhq/aken/internal/collect"
	"github.com/akenhq/aken/internal/source"
	"github.com/akenhq/aken/protocol"
)

var execCommand = exec.CommandContext
var readJournal = source.ReadJournal
var resolveContainers = source.ResolveContainers

type preparedJob struct {
	job                 protocol.Job
	paths               []string
	target, description string
	sensitive           bool
	read                protocol.ReadFileParams
	tail                protocol.TailParams
	search              protocol.SearchParams
	journal             protocol.JournalParams
	spec                source.Spec
	since, until        time.Time
}

func decode(params json.RawMessage, into any) error {
	if len(params) == 0 || bytes.Equal(bytes.TrimSpace(params), []byte("null")) {
		return errors.New("invalid params")
	}
	d := json.NewDecoder(bytes.NewReader(params))
	d.DisallowUnknownFields()
	if err := d.Decode(into); err != nil {
		return errors.New("invalid params")
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("invalid params")
	}
	return nil
}

func inside(path string, roots []string) bool {
	for _, root := range roots {
		rel, err := filepath.Rel(filepath.Clean(root), path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func resolve(files source.Files, path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("path must be absolute")
	}
	path = filepath.Clean(path)
	if !inside(path, files.Allowed) {
		return "", outsideScope(files)
	}
	file, err := files.Open(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", failed("path cannot be opened inside the scope", err)
	}
	if file != nil {
		_ = file.Close()
	}
	canonical, err := filepath.EvalSymlinks(path)
	if errors.Is(err, os.ErrNotExist) {
		parent := filepath.Dir(path)
		for {
			resolved, e := filepath.EvalSymlinks(parent)
			if e == nil {
				rel, _ := filepath.Rel(parent, path)
				canonical = filepath.Join(resolved, rel)
				break
			}
			if !errors.Is(e, os.ErrNotExist) || parent == filepath.Dir(parent) {
				return "", errors.New("path cannot be resolved")
			}
			parent = filepath.Dir(parent)
		}
	} else if err != nil {
		return "", errors.New("path cannot be resolved")
	}
	if !inside(canonical, files.Allowed) {
		return "", outsideScope(files)
	}
	return canonical, nil
}

// outsideScope names the allowed directories so the agent can stay inside them and the human knows
// which flag widens them. The directories are the scope itself, so naming them discloses nothing new.
func outsideScope(files source.Files) error {
	return fmt.Errorf("outside the scope (%s); restart aken serve with --allow DIR to widen it", strings.Join(files.Allowed, ", "))
}

// failed keeps an approved job's error useful without echoing paths: an *fs.PathError is reduced to its
// cause, and every other error from package source or the OS is fixed text.
func failed(what string, err error) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		err = pathErr.Err
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%s: no such file", what)
	case errors.Is(err, fs.ErrPermission):
		return fmt.Errorf("%s: permission denied for the user running aken serve", what)
	}
	return fmt.Errorf("%s: %s", what, err)
}

func sensitive(path string) bool {
	for _, part := range strings.Split(filepath.Clean(path), string(filepath.Separator)) {
		if part == ".ssh" || part == ".gnupg" || part == ".aws" {
			return true
		}
	}
	base := filepath.Base(path)
	if base == ".env" || strings.HasPrefix(base, ".env.") || base == "shadow" || base == "gshadow" || strings.HasPrefix(base, "id_") {
		return true
	}
	for _, suffix := range []string{".pem", ".key", ".p12", ".pfx", ".kdbx", ".keystore", "_rsa", "_ed25519", "_ecdsa", "_dsa"} {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	return false
}

func expand(files source.Files, pattern string, since time.Time) ([]string, error) {
	if !filepath.IsAbs(pattern) {
		return nil, errors.New("glob must be absolute")
	}
	base := pattern
	if i := strings.IndexAny(pattern, "*?[\\"); i >= 0 {
		base = pattern[:i]
	}
	base = filepath.Dir(base)
	if _, err := resolve(files, base); err != nil {
		return nil, err
	}
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, errors.New("invalid glob")
	}
	var paths []string
	seen := map[string]bool{}
	for _, path := range matches {
		path, err = resolve(files, path)
		if err != nil || seen[path] {
			continue
		}
		file, err := files.Open(path)
		if err != nil {
			continue
		}
		info, err := file.Stat()
		_ = file.Close()
		if err != nil || !info.Mode().IsRegular() || info.ModTime().Before(since) {
			continue
		}
		paths = append(paths, path)
		seen[path] = true
		if len(paths) > 200 {
			return nil, errors.New("glob matches more than 200 files")
		}
	}
	return paths, nil
}

func prepare(ctx context.Context, job protocol.Job, class uint8, files source.Files, now time.Time) (preparedJob, error) {
	p := preparedJob{job: job}
	if !protocol.ValidateJobID(job.ID) {
		return p, errors.New("invalid job id")
	}
	expected, ok := protocol.CatalogClass(job.Name)
	if !ok {
		return p, errors.New("unknown job name")
	}
	if expected != class {
		return p, errors.New("class mismatch")
	}
	var err error
	switch job.Name {
	case "list_dir":
		var q protocol.ListDirParams
		err = decode(job.Params, &q)
		p.target = q.Path
	case "read_file":
		err = decode(job.Params, &p.read)
		q := &p.read
		p.target = q.Path
		if q.From == 0 {
			q.From = 1
		}
		if q.To == 0 && q.From <= int(^uint(0)>>1)-499 {
			q.To = q.From + 499
		}
		if q.From < 1 || q.To < q.From || q.To-q.From >= 500 {
			return p, errors.New("invalid line range")
		}
		p.description = fmt.Sprintf("lines %d-%d", q.From, q.To)
	case "tail":
		err = decode(job.Params, &p.tail)
		p.target = p.tail.Path
		if p.tail.N == 0 {
			p.tail.N = 100
		}
		if p.tail.N < 1 || p.tail.N > 500 {
			return p, errors.New("invalid tail count")
		}
		p.description = fmt.Sprintf("last %d lines", p.tail.N)
	case "search":
		if err = decode(job.Params, &p.search); err != nil {
			return p, err
		}
		q := &p.search
		p.target = q.Glob
		if q.Max == 0 {
			q.Max = 50
		}
		if q.Before < 0 || q.Before > 50 || q.After < 0 || q.After > 50 || q.Max < 1 || q.Max > 200 {
			return p, errors.New("invalid search limits")
		}
		if _, err = regexp.Compile(q.Regex); err != nil {
			return p, errors.New("invalid regex")
		}
		var since time.Time
		if q.Since != "" {
			since, err = collect.ParseTime(q.Since, now)
			if err != nil {
				return p, errors.New("invalid since")
			}
		}
		p.paths, err = expand(files, q.Glob, since)
		if err != nil {
			return p, err
		}
		if q.Cursor != "" {
			a, b, ok := strings.Cut(q.Cursor, ":")
			i, e1 := strconv.Atoi(a)
			n, e2 := strconv.Atoi(b)
			if !ok || e1 != nil || e2 != nil || i < 0 || i >= len(p.paths) || n < 1 {
				return p, errors.New("invalid cursor")
			}
		}
		p.description = fmt.Sprintf("regex %s  %d files", q.Regex, len(p.paths))
		if len(p.paths) > 0 {
			p.description += "; first: " + strings.Join(p.paths[:min(3, len(p.paths))], ", ")
			if len(p.paths) > 3 {
				p.description += fmt.Sprintf(" and %d more", len(p.paths)-3)
			}
		}
	case "journal", "docker_logs":
		if job.Name == "journal" {
			err = decode(job.Params, &p.journal)
			p.spec = source.Spec{Kind: source.KindUnit, Target: p.journal.Unit}
			if err == nil {
				err = source.ValidateUnit(p.spec.Target)
			}
		} else {
			var q protocol.DockerLogsParams
			err = decode(job.Params, &q)
			p.journal = protocol.JournalParams{Since: q.Since, Until: q.Until, Regex: q.Regex, Tail: q.Tail, Max: q.Max, Cursor: q.Cursor}
			p.spec = source.Spec{Kind: source.KindContainer, Target: q.Container}
			if err == nil {
				err = source.ValidateContainer(q.Container)
			}
		}
		if err != nil {
			return p, err
		}
		if err = prepareJournal(ctx, &p, now); err != nil {
			return p, err
		}
	case "systemctl_status":
		var q protocol.SystemctlStatusParams
		err = decode(job.Params, &q)
		if err == nil {
			err = source.ValidateUnit(q.Unit)
		}
		p.target = q.Unit
	case "ps", "df":
	}
	if err != nil {
		return p, err
	}
	if job.Name == "read_file" || job.Name == "tail" || job.Name == "list_dir" {
		target, err := resolve(files, p.target)
		if err != nil {
			return p, err
		}
		p.target = target
		p.paths = []string{target}
	}
	for _, path := range p.paths {
		p.sensitive = p.sensitive || sensitive(path)
	}
	return p, nil
}

func prepareJournal(ctx context.Context, p *preparedJob, now time.Time) error {
	q := &p.journal
	if q.Tail < 0 || q.Tail > 500 || q.Max < 0 || q.Max > 500 || q.Tail > 0 && (q.Regex != "" || q.Max != 0 || q.Cursor != "") {
		return errors.New("invalid journal limits")
	}
	if q.Max == 0 {
		q.Max = 200
	}
	if _, err := regexp.Compile(q.Regex); err != nil {
		return errors.New("invalid regex")
	}
	if q.Cursor != "" {
		n, err := strconv.Atoi(q.Cursor)
		if err != nil || n < 0 {
			return errors.New("invalid cursor")
		}
	}
	if q.Since == "" {
		q.Since = "1h"
	}
	if q.Until == "" {
		q.Until = "0s"
	}
	var err error
	p.since, err = collect.ParseTime(q.Since, now)
	if err != nil {
		return errors.New("invalid since")
	}
	p.until, err = collect.ParseTime(q.Until, now)
	if err != nil || !p.since.Before(p.until) {
		return errors.New("invalid journal window")
	}
	if p.spec.Kind == source.KindContainer {
		ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		names, err := resolveContainers(ctx, p.spec.Target)
		if err != nil {
			return errors.New("cannot resolve container")
		}
		for _, name := range names {
			if source.ValidateContainer(name) != nil {
				return errors.New("invalid container name")
			}
		}
		if len(names) != 1 {
			return errors.New("name matches several containers")
		}
		p.spec.Target = names[0]
		if len(p.spec.Target) == 12 || len(p.spec.Target) == 64 {
			if _, err := hex.DecodeString(p.spec.Target); err == nil {
				field := "CONTAINER_ID="
				if len(p.spec.Target) == 64 {
					field = "CONTAINER_ID_FULL="
				}
				p.spec.Match = field + p.spec.Target
			}
		}
	}
	p.target = string(p.spec.Kind) + " " + p.spec.Target
	p.description = fmt.Sprintf("%s to %s  first %d lines", p.since.UTC().Format(time.RFC3339), p.until.UTC().Format(time.RFC3339), q.Max)
	if q.Tail > 0 {
		p.description = fmt.Sprintf("%s to %s  last %d lines", p.since.UTC().Format(time.RFC3339), p.until.UTC().Format(time.RFC3339), q.Tail)
	}
	return nil
}

func execute(ctx context.Context, p preparedJob, files source.Files) ([]string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	switch p.job.Name {
	case "list_dir":
		return listDir(files, p.target)
	case "read_file":
		return readFile(files, p)
	case "search":
		return search(files, p)
	case "tail":
		return tail(files, p)
	case "journal", "docker_logs":
		return journal(ctx, p)
	case "systemctl_status":
		return systemctlStatus(ctx, p.target)
	case "ps":
		return ps(ctx)
	case "df":
		return df(ctx)
	}
	return nil, "", errors.New("unknown job name")
}
func listDir(files source.Files, path string) ([]string, string, error) {
	entries, err := files.List(path)
	if err != nil {
		return nil, "", failed("cannot list directory", err)
	}
	var lines []string
	for _, e := range entries[:min(500, len(entries))] {
		lines = append(lines, fmt.Sprintf("%s %d %s %s", e.Mode, e.Size, e.ModTime.UTC().Format(time.RFC3339), e.Name))
	}
	next := ""
	if len(entries) > 500 {
		next = entries[500].Name
	}
	return lines, next, nil
}
func readFile(files source.Files, p preparedJob) ([]string, string, error) {
	data, total, err := files.ReadLines(p.target, p.read.From, p.read.To)
	if err != nil {
		return nil, "", failed("cannot read file", err)
	}
	var lines []string
	for i, line := range data {
		lines = append(lines, fmt.Sprintf("%d: %s", p.read.From+i, line))
	}
	next := ""
	if p.read.To < total {
		next = strconv.Itoa(p.read.To + 1)
	}
	return lines, next, nil
}
func search(files source.Files, p preparedJob) ([]string, string, error) {
	q := p.search
	result, err := files.Search(q.Regex, source.SearchQuery{Paths: p.paths, Before: q.Before, After: q.After, Max: q.Max, Cursor: q.Cursor})
	if err != nil {
		return nil, "", failed("cannot search files", err)
	}
	return result.Lines, result.Next, nil
}
func tail(files source.Files, p preparedJob) ([]string, string, error) {
	data, total, err := files.TailLines(p.target, p.tail.N)
	if err != nil {
		return nil, "", failed("cannot read file", err)
	}
	var lines []string
	for i, line := range data {
		lines = append(lines, fmt.Sprintf("%d: %s", total-len(data)+1+i, line))
	}
	return lines, "", nil
}
func journal(ctx context.Context, p preparedJob) ([]string, string, error) {
	s, err := readJournal(ctx, p.spec, p.since, p.until)
	if err != nil {
		return nil, "", errors.New("cannot read journal")
	}
	re := regexp.MustCompile(p.journal.Regex)
	var lines []string
	for _, line := range s.Lines {
		if re.Match(line) {
			lines = append(lines, string(line))
		}
	}
	q := p.journal
	if q.Tail > 0 {
		return lines[max(0, len(lines)-q.Tail):], "", nil
	}
	start, _ := strconv.Atoi(q.Cursor)
	start = min(start, len(lines))
	end := min(start+q.Max, len(lines))
	next := ""
	if end < len(lines) {
		next = strconv.Itoa(end)
	}
	return lines[start:end], next, nil
}
func command(ctx context.Context, name string, args ...string) ([]string, error) {
	path, err := source.BinaryPath(name)
	if err != nil {
		return nil, err
	}
	cmd := execCommand(ctx, path, args...) //nolint:gosec // fixed binary paths and catalog argv; unit names are validated
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, errors.New("cannot read command output")
	}
	if err = cmd.Start(); err != nil {
		return nil, errors.New("cannot start command")
	}
	data, readErr := io.ReadAll(io.LimitReader(pipe, 8<<20))
	capped := len(data) == 8<<20
	if capped {
		_ = cmd.Process.Kill()
	}
	_ = pipe.Close()
	err = cmd.Wait()
	if ctx.Err() != nil {
		return nil, errors.New("command canceled")
	}
	if readErr != nil {
		return nil, errors.New("cannot read command output")
	}
	var exit *exec.ExitError
	acceptedExit := name == "systemctl" && errors.As(err, &exit) && exit.ExitCode() >= 0 && exit.ExitCode() <= 4
	if err != nil && !acceptedExit && !capped {
		return nil, errors.New("command failed")
	}
	if len(data) == 0 {
		return nil, nil
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n"), nil
}
func systemctlStatus(ctx context.Context, unit string) ([]string, string, error) {
	if err := source.ValidateUnit(unit); err != nil {
		return nil, "", err
	}
	lines, err := command(ctx, "systemctl", "status", "--no-pager", "--lines=0", unit)
	return lines, "", err
}
func ps(ctx context.Context) ([]string, string, error) {
	lines, err := command(ctx, "ps", "-eo", "pid,ppid,user,%cpu,%mem,rss,etimes,args", "--sort=-%cpu")
	next := ""
	if len(lines) > 500 {
		next = "500"
		lines = lines[:500]
	}
	return lines, next, err
}
func df(ctx context.Context) ([]string, string, error) {
	lines, err := command(ctx, "df", "-hP")
	return lines, "", err
}
