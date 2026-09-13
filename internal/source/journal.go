// SPDX-License-Identifier: Apache-2.0
package source

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
)

var execCommand = exec.CommandContext

func ResolveContainers(ctx context.Context, name string) ([]string, error) {
	if err := ValidateContainer(name); err != nil {
		return nil, err
	}
	if len(name) == 12 || len(name) == 64 {
		if _, err := hex.DecodeString(name); err == nil {
			return []string{name}, nil
		}
	}
	path, err := journalPath()
	if err != nil {
		return nil, err
	}
	cmd := execCommand(ctx, path, "--no-pager", "-q", "-F", "CONTAINER_NAME") //nolint:gosec // fixed journalctl path and arguments
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		first, _, _ := strings.Cut(stderr.String(), "\n")
		return nil, fmt.Errorf("journalctl: %w: %s", err, first)
	}
	var names []string
	for line := range strings.SplitSeq(stdout.String(), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			names = append(names, line)
		}
	}
	slices.Sort(names)
	names = slices.Compact(names)
	if slices.Contains(names, name) {
		return []string{name}, nil
	}
	var matches []string
	for _, candidate := range names {
		if strings.HasPrefix(candidate, name+".") || strings.HasPrefix(candidate, name+"_") || strings.HasPrefix(candidate, name+"-") {
			matches = append(matches, candidate)
		}
	}
	if len(matches) > 0 {
		return matches, nil
	}
	if len(names) == 0 {
		return nil, errors.New("no container logs in the journal; is the docker logging driver journald? See docs/docker.md")
	}
	return nil, fmt.Errorf("no container named %q in the journal; known names: %s", name, strings.Join(names[:min(20, len(names))], ", "))
}

func journalPath() (string, error) {
	path := "/usr/bin/journalctl"
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		path = "/bin/journalctl"
	}
	if _, err := os.Stat(path); err != nil {
		return "", errors.New("journalctl not found")
	}
	return path, nil
}

func ReadJournal(ctx context.Context, spec Spec, since, until time.Time) (*Source, error) {
	var match string
	switch spec.Kind {
	case KindUnit:
		if err := ValidateUnit(spec.Target); err != nil {
			return nil, err
		}
		match = "--unit=" + spec.Target
	case KindContainer:
		if err := ValidateContainer(spec.Target); err != nil {
			return nil, err
		}
		match = "CONTAINER_NAME=" + spec.Target
	default:
		return nil, errors.New("invalid journal source kind")
	}
	if spec.Match != "" {
		match = spec.Match
	}
	path, err := journalPath()
	if err != nil {
		return nil, err
	}
	argv := []string{"--no-pager", "-q", "--utc", "-o", "short-iso-precise", "--no-hostname", fmt.Sprintf("--since=@%d", since.Unix()), fmt.Sprintf("--until=@%d", until.Unix()), match}
	cmd := execCommand(ctx, path, argv...) //nolint:gosec // argv is fixed and the name is validated
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		first, _, _ := strings.Cut(stderr.String(), "\n")
		return nil, fmt.Errorf("journalctl: %w: %s", err, first)
	}
	return &Source{Spec: spec, Name: string(spec.Kind) + ":" + spec.Target, Lines: splitLines(stdout.Bytes()), Since: since, Until: until}, nil
}
