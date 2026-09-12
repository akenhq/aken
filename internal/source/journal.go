// SPDX-License-Identifier: Apache-2.0
package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

var execCommand = exec.CommandContext

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
	path := "/usr/bin/journalctl"
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		path = "/bin/journalctl"
	}
	if _, err := os.Stat(path); err != nil {
		return nil, errors.New("journalctl not found")
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
