// SPDX-License-Identifier: Apache-2.0
package collect

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Options struct {
	Units, Containers, Files, Globs []string
	Since, Until                    time.Time
	Tail                            int
	Allow                           []string
	Keep, KeepCategories            []string
	RulesFile                       string
	TTL                             time.Duration
	RelayURL                        string
	DryRun                          bool
	StateDir                        string
	Retention                       time.Duration
	Argv                            []string
	Now                             func() time.Time
	Collector                       string
}

func ParseTime(s string, now time.Time) (time.Time, error) {
	if strings.HasSuffix(s, "d") {
		days, err := strconv.ParseInt(strings.TrimSuffix(s, "d"), 10, 64)
		if err == nil && days >= 0 && days <= int64((1<<63-1)/(24*time.Hour)) {
			return now.Add(-time.Duration(days) * 24 * time.Hour), nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil && d >= 0 {
		return now.Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, errors.New("expected a duration, RFC 3339 time, local time or local date")
}
func DefaultStateDir() string {
	if info, err := os.Stat("/var/lib/aken"); err == nil && info.IsDir() && syscall.Access("/var/lib/aken", 2) == nil {
		return "/var/lib/aken"
	}
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "aken")
	}
	return filepath.Join(os.Getenv("HOME"), ".local", "state", "aken")
}
func prune(runsDir string, olderThan time.Time) error {
	entries, err := os.ReadDir(runsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || len(entry.Name()) < 16 {
			continue
		}
		stamp, err := time.Parse("20060102T150405Z", entry.Name()[:16])
		if err == nil && stamp.Before(olderThan) {
			if err := os.RemoveAll(filepath.Join(runsDir, entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}
