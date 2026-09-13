// SPDX-License-Identifier: Apache-2.0
package protocol

import (
	"encoding/json"
	"regexp"
)

const MaxResultPlaintext = 256 << 10

type Job struct {
	ID     string          `json:"id"`
	Name   string          `json:"name"`
	Params json.RawMessage `json:"params"`
}

type PlanParams struct {
	Jobs []Job `json:"jobs"`
}

type Result struct {
	ID        string          `json:"id"`
	Status    string          `json:"status"`
	Error     string          `json:"error,omitempty"`
	Lines     []string        `json:"lines,omitempty"`
	Next      string          `json:"next,omitempty"`
	Redaction ResultRedaction `json:"redaction"`
}

type ResultRedaction struct {
	LinesRedacted int64                    `json:"lines_redacted"`
	ByCategory    map[string]CategoryCount `json:"by_category"`
	Flags         int64                    `json:"flags"`
}

type CatalogEntry struct {
	Name  string
	Class uint8
}

var Catalog = []CatalogEntry{{"list_dir", 1}, {"read_file", 1}, {"search", 1}, {"tail", 1}, {"journal", 1}, {"docker_logs", 1}, {"systemctl_status", 1}, {"ps", 1}, {"df", 1}}

func CatalogClass(name string) (uint8, bool) {
	for _, entry := range Catalog {
		if entry.Name == name {
			return entry.Class, true
		}
	}
	return 0, false
}

var jobIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func ValidateJobID(id string) bool { return jobIDPattern.MatchString(id) }

type ListDirParams struct {
	Path string `json:"path"`
}

type ReadFileParams struct {
	Path string `json:"path"`
	From int    `json:"from,omitempty"`
	To   int    `json:"to,omitempty"`
}

type SearchParams struct {
	Glob   string `json:"glob"`
	Regex  string `json:"regex"`
	Since  string `json:"since,omitempty"`
	Before int    `json:"before,omitempty"`
	After  int    `json:"after,omitempty"`
	Max    int    `json:"max,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}

type TailParams struct {
	Path string `json:"path"`
	N    int    `json:"n,omitempty"`
}

type JournalParams struct {
	Unit   string `json:"unit"`
	Since  string `json:"since,omitempty"`
	Until  string `json:"until,omitempty"`
	Regex  string `json:"regex,omitempty"`
	Tail   int    `json:"tail,omitempty"`
	Max    int    `json:"max,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}

type DockerLogsParams struct {
	Container string `json:"container"`
	Since     string `json:"since,omitempty"`
	Until     string `json:"until,omitempty"`
	Regex     string `json:"regex,omitempty"`
	Tail      int    `json:"tail,omitempty"`
	Max       int    `json:"max,omitempty"`
	Cursor    string `json:"cursor,omitempty"`
}

type SystemctlStatusParams struct {
	Unit string `json:"unit"`
}
