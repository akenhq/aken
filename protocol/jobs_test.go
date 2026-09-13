// SPDX-License-Identifier: Apache-2.0
package protocol

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestJobWireTypes(t *testing.T) {
	for _, tt := range []struct {
		value any
		wire  string
	}{
		{Job{ID: "j1", Name: "df", Params: json.RawMessage(`{}`)}, `{"id":"j1","name":"df","params":{}}`},
		{PlanParams{Jobs: []Job{{ID: "j1.1", Name: "ps", Params: json.RawMessage(`{}`)}}}, `{"jobs":[{"id":"j1.1","name":"ps","params":{}}]}`},
		{ListDirParams{Path: "/var/log"}, `{"path":"/var/log"}`},
		{ReadFileParams{Path: "/var/log/a", From: 1, To: 500}, `{"path":"/var/log/a","from":1,"to":500}`},
		{ReadFileParams{Path: "/var/log/a"}, `{"path":"/var/log/a"}`},
		{SearchParams{Glob: "/var/log/*", Regex: "error", Since: "1h", Before: 1, After: 2, Max: 50, Cursor: "next"}, `{"glob":"/var/log/*","regex":"error","since":"1h","before":1,"after":2,"max":50,"cursor":"next"}`},
		{TailParams{Path: "/var/log/a", N: 100}, `{"path":"/var/log/a","n":100}`},
		{JournalParams{Unit: "a", Since: "1h", Until: "now", Regex: "error", Tail: 1, Max: 2, Cursor: "next"}, `{"unit":"a","since":"1h","until":"now","regex":"error","tail":1,"max":2,"cursor":"next"}`},
		{DockerLogsParams{Container: "a", Since: "1h", Until: "now", Regex: "error", Tail: 1, Max: 2, Cursor: "next"}, `{"container":"a","since":"1h","until":"now","regex":"error","tail":1,"max":2,"cursor":"next"}`},
		{SystemctlStatusParams{Unit: "a"}, `{"unit":"a"}`},
		{Result{ID: "j1", Status: "ok", Lines: []string{"redacted"}, Next: "2", Redaction: ResultRedaction{LinesRedacted: 1, ByCategory: map[string]CategoryCount{}, Flags: 2}}, `{"id":"j1","status":"ok","lines":["redacted"],"next":"2","redaction":{"lines_redacted":1,"by_category":{},"flags":2}}`},
		{Result{ID: "j1", Status: "denied", Error: "declined"}, `{"id":"j1","status":"denied","error":"declined","redaction":{"lines_redacted":0,"by_category":null,"flags":0}}`},
	} {
		t.Run(reflect.TypeOf(tt.value).Name(), func(t *testing.T) {
			data, err := json.Marshal(tt.value)
			if err != nil || string(data) != tt.wire {
				t.Fatalf("wire %s: %v", data, err)
			}
			got := reflect.New(reflect.TypeOf(tt.value))
			if err := json.Unmarshal([]byte(tt.wire), got.Interface()); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Elem().Interface(), tt.value) {
				t.Fatal("round trip changed value")
			}
		})
	}
}

func TestCatalog(t *testing.T) {
	names := []string{"list_dir", "read_file", "search", "tail", "journal", "docker_logs", "systemctl_status", "ps", "df"}
	if len(Catalog) != len(names) {
		t.Fatal("catalog length")
	}
	for i, name := range names {
		class, ok := CatalogClass(name)
		if !ok || class != ClassRead || Catalog[i] != (CatalogEntry{Name: name, Class: ClassRead}) {
			t.Fatal("catalog entry", name)
		}
	}
	for _, name := range []string{"", "plan", "DF", "exec", "search_files", "tail_file"} {
		if class, ok := CatalogClass(name); ok || class != 0 {
			t.Fatal("unknown catalog name", name)
		}
	}
	if MaxResultPlaintext != 256<<10 {
		t.Fatal("result text cap")
	}
}

func TestValidateJobID(t *testing.T) {
	for _, id := range []string{"j1", "j1.2", "A_z-9.", strings.Repeat("a", 64)} {
		if !ValidateJobID(id) {
			t.Fatal("valid id rejected", id)
		}
	}
	for _, id := range []string{"", strings.Repeat("a", 65), "a b", "a/b", "a\n", "a\x00", "é", "💡"} {
		if ValidateJobID(id) {
			t.Fatal("invalid id accepted", id)
		}
	}
}
