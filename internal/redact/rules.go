// SPDX-License-Identifier: Apache-2.0
package redact

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"slices"

	"github.com/akenhq/aken/protocol"
)

type Rule struct {
	Name     string `json:"name"`
	Category string `json:"category"`
	Regex    string `json:"regex"`
	Group    int    `json:"group,omitempty"`
	Enabled  *bool  `json:"enabled,omitempty"`
}
type File struct {
	Version int      `json:"version"`
	Rules   []Rule   `json:"rules"`
	Keep    []string `json:"keep,omitempty"`
}

func ParseRules(data []byte) (File, error) {
	var f File
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&f); err != nil {
		return File{}, errors.New("invalid rules JSON")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return File{}, errors.New("invalid rules JSON")
	}
	if _, err := Compile(f, nil, nil); err != nil {
		return File{}, err
	}
	return f, nil
}
func Merge(base, extra File) (File, error) {
	if _, err := Compile(base, nil, nil); err != nil {
		return File{}, err
	}
	if _, err := Compile(extra, nil, nil); err != nil {
		return File{}, err
	}
	merged := File{Version: 1, Rules: slices.Clone(base.Rules), Keep: slices.Clone(base.Keep)}
	for _, r := range extra.Rules {
		i := slices.IndexFunc(merged.Rules, func(old Rule) bool { return old.Name == r.Name })
		if i < 0 {
			merged.Rules = append(merged.Rules, r)
		} else {
			merged.Rules[i] = r
		}
	}
	for _, v := range extra.Keep {
		if !slices.Contains(merged.Keep, v) {
			merged.Keep = append(merged.Keep, v)
		}
	}
	return merged, nil
}

type compiledRule struct {
	Rule
	re *regexp.Regexp
}

var ruleName = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)

func Compile(f File, keepValues, keepCategories []string) (*Engine, error) {
	if f.Version != 1 {
		return nil, errors.New("unsupported rules version")
	}
	e := &Engine{keep: map[string]bool{}, off: map[string]bool{}, values: map[string]map[string]string{}}
	for _, c := range keepCategories {
		if !slices.Contains(protocol.RedactionCategories, c) {
			return nil, errors.New("unknown redaction category")
		}
		e.off[c] = true
	}
	for _, v := range f.Keep {
		e.keep[v] = true
	}
	for _, v := range keepValues {
		e.keep[v] = true
	}
	names := map[string]bool{}
	for _, r := range f.Rules {
		if !ruleName.MatchString(r.Name) || names[r.Name] {
			return nil, errors.New("invalid or duplicate rule name")
		}
		names[r.Name] = true
		if !slices.Contains(protocol.RedactionCategories, r.Category) {
			return nil, errors.New("unknown redaction category")
		}
		re, err := regexp.Compile(r.Regex)
		if err != nil {
			return nil, errors.New("invalid rule regex")
		}
		if r.Group < 0 || r.Group > re.NumSubexp() {
			return nil, errors.New("invalid rule capture group")
		}
		if (r.Enabled == nil || *r.Enabled) && !e.off[r.Category] {
			e.rules = append(e.rules, compiledRule{Rule: r, re: re})
		}
	}
	return e, nil
}
