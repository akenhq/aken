// SPDX-License-Identifier: Apache-2.0
package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/akenhq/aken/internal/collect"
	"github.com/akenhq/aken/internal/screen"
)

const revealUsage = `Usage: aken reveal [flags] [ID [PLACEHOLDER...]]

Print the original values behind placeholders. The values come from the local
copies that collect and serve keep on this server; nothing is sent anywhere.

  aken reveal                     list local copies, newest first
  aken reveal ID                  print every placeholder of that copy and its value
  aken reveal ID PLACEHOLDER...   print only these placeholders, for example '<ip#3>' or ip#3

ID is a local copy directory name, such as 20260921T101500Z-1a2b3c4d, or a
session id prefix of at least 8 hex characters.

Flags:
  --state-dir DIR     where local copies are (default /var/lib/aken when writable, else $XDG_STATE_HOME/aken or ~/.local/state/aken)
`

// localCopy is one run of aken collect or one session of aken serve.
type localCopy struct {
	kind, name, dir, sessionID string
}

func runReveal(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("reveal", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = fmt.Fprint(stdout, revealUsage) }
	stateDir := flags.String("state-dir", "", "")
	if err := flags.Parse(args[1:]); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if *stateDir == "" {
		*stateDir = collect.DefaultStateDir()
	}
	fail := func(err error) int {
		_, _ = fmt.Fprintf(stderr, "aken: %s\n", screen.Visible([]byte(err.Error())))
		return 1
	}
	copies, err := localCopies(*stateDir)
	if err != nil {
		return fail(err)
	}
	if flags.NArg() == 0 {
		if len(copies) == 0 {
			_, _ = fmt.Fprintf(stdout, "No local copies under %s.\n", screen.Visible([]byte(*stateDir)))
			return 0
		}
		for _, c := range copies {
			_, _ = fmt.Fprintf(stdout, "%s\t%s\t%s\n", screen.Visible([]byte(c.name)), c.kind, screen.Visible([]byte(c.sessionID)))
		}
		return 0
	}
	c, err := findCopy(copies, flags.Arg(0))
	if err != nil {
		return fail(err)
	}
	data, err := os.ReadFile(filepath.Join(c.dir, "mapping.json"))
	if errors.Is(err, os.ErrNotExist) {
		return fail(fmt.Errorf("%s has no mapping.json; nothing was redacted yet", c.name))
	}
	if err != nil {
		return fail(err)
	}
	var mapping map[string]string
	if err := json.Unmarshal(data, &mapping); err != nil {
		return fail(fmt.Errorf("%s: mapping.json: %w", c.name, err))
	}
	placeholders := flags.Args()[1:]
	if len(placeholders) == 0 {
		for p := range mapping {
			placeholders = append(placeholders, p)
		}
		slices.SortFunc(placeholders, comparePlaceholders)
	}
	code := 0
	for _, p := range placeholders {
		if !strings.HasPrefix(p, "<") {
			p = "<" + p + ">"
		}
		value, ok := mapping[p]
		if !ok {
			_, _ = fmt.Fprintf(stderr, "aken: %s is not in %s\n", screen.Visible([]byte(p)), c.name)
			code = 1
			continue
		}
		_, _ = fmt.Fprintf(stdout, "%s\t%s\n", screen.Visible([]byte(p)), screen.Visible([]byte(value)))
	}
	return code
}

// localCopies returns the copies under runs/ and sessions/, newest first.
func localCopies(stateDir string) ([]localCopy, error) {
	var copies []localCopy
	for _, kind := range []struct{ dir, name, info string }{{"runs", "collect", "run.json"}, {"sessions", "serve", "session.json"}} {
		entries, err := os.ReadDir(filepath.Join(stateDir, kind.dir))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			c := localCopy{kind: kind.name, name: entry.Name(), dir: filepath.Join(stateDir, kind.dir, entry.Name())}
			var info struct {
				SessionID string `json:"session_id"`
			}
			if data, err := os.ReadFile(filepath.Join(c.dir, kind.info)); err == nil && json.Unmarshal(data, &info) == nil {
				c.sessionID = info.SessionID
			}
			copies = append(copies, c)
		}
	}
	slices.SortFunc(copies, func(a, b localCopy) int { return cmp.Compare(b.name, a.name) })
	return copies, nil
}

func findCopy(copies []localCopy, id string) (localCopy, error) {
	var matches []localCopy
	for _, c := range copies {
		if c.name == id || len(id) >= 8 && strings.HasPrefix(c.sessionID, id) {
			matches = append(matches, c)
		}
	}
	switch len(matches) {
	case 0:
		return localCopy{}, fmt.Errorf("no local copy matches %q; run \"aken reveal\" to list them", id)
	case 1:
		return matches[0], nil
	}
	names := make([]string, len(matches))
	for i, c := range matches {
		names[i] = c.name
	}
	return localCopy{}, fmt.Errorf("%q matches %s; use a directory name", id, strings.Join(names, ", "))
}

// comparePlaceholders orders <ip#2> before <ip#10>, grouped by category.
func comparePlaceholders(a, b string) int {
	category := func(p string) (string, int) {
		name, number, _ := strings.Cut(strings.Trim(p, "<>"), "#")
		n, _ := strconv.Atoi(number)
		return name, n
	}
	ac, an := category(a)
	bc, bn := category(b)
	return cmp.Or(cmp.Compare(ac, bc), cmp.Compare(an, bn), cmp.Compare(a, b))
}
