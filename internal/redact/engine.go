// SPDX-License-Identifier: Apache-2.0
package redact

import (
	"bytes"
	"fmt"
	"net/netip"
	"regexp"
	"slices"

	"github.com/akenhq/aken/protocol"
)

type Engine struct {
	rules     []compiledRule
	keep, off map[string]bool
	values    map[string]map[string]string
}
type Result struct {
	Lines          [][]byte
	LinesRedacted  int64
	LinesCollapsed int64
	ByCategory     map[string]protocol.CategoryCount
	Flags          []Flag
	Mapping        map[string]string
	Rules          int
}
type Flag struct {
	Line  int
	Value string
}

var keyBegin = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)
var keyEnd = regexp.MustCompile(`-----END [A-Z ]*PRIVATE KEY-----`)

func (e *Engine) placeholder(category, value string) string {
	if e.values[category] == nil {
		e.values[category] = map[string]string{}
	}
	if p, ok := e.values[category][value]; ok {
		return p
	}
	p := fmt.Sprintf("<%s#%d>", category, len(e.values[category])+1)
	e.values[category][value] = p
	return p
}
func (e *Engine) Mapping() map[string]string {
	out := map[string]string{}
	for _, values := range e.values {
		for v, p := range values {
			out[p] = v
		}
	}
	return out
}

type replacement struct {
	start, end int
	text       string
}

func (e *Engine) Redact(lines [][]byte) Result {
	result := Result{ByCategory: map[string]protocol.CategoryCount{}, Rules: len(e.rules)}
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		changed := map[string]bool{}
		if !e.off["key"] && keyBegin.Match(line) {
			end := i
			for j := i; j < len(lines) && j <= i+64; j++ {
				if keyEnd.Match(lines[j]) {
					end = j
					break
				}
			}
			value := string(bytes.Join(lines[i:end+1], []byte{'\n'}))
			if !e.keep[value] {
				line = []byte(e.placeholder("key", value))
				changed["key"] = true
				result.LinesCollapsed += int64(end - i)
				i = end
			}
		}
		if !changed["key"] {
			taken := make([]bool, len(line))
			var replacements []replacement
			for _, rule := range e.rules {
				// Search untouched spans so an earlier replacement cannot hide a later match.
				for start := 0; start < len(line); {
					if taken[start] {
						start++
						continue
					}
					end := start
					for end < len(line) && !taken[end] {
						end++
					}
					for _, match := range rule.re.FindAllSubmatchIndex(line[start:end], -1) {
						a, b := match[rule.Group*2], match[rule.Group*2+1]
						if a < 0 || a == b {
							continue
						}
						a += start
						b += start
						value := string(line[a:b])
						if e.keep[value] {
							continue
						}
						if rule.Category == "ip" {
							if _, err := netip.ParseAddr(value); err != nil {
								continue
							}
						}
						replacements = append(replacements, replacement{a, b, e.placeholder(rule.Category, value)})
						for j := a; j < b; j++ {
							taken[j] = true
						}
						changed[rule.Category] = true
					}
					start = end
				}
			}
			slices.SortFunc(replacements, func(a, b replacement) int { return a.start - b.start })
			var out bytes.Buffer
			pos := 0
			for _, r := range replacements {
				out.Write(line[pos:r.start])
				out.WriteString(r.text)
				pos = r.end
			}
			out.Write(line[pos:])
			line = out.Bytes()
		}
		result.Lines = append(result.Lines, line)
		if len(changed) > 0 {
			result.LinesRedacted++
		}
		for c := range changed {
			count := result.ByCategory[c]
			count.Lines++
			result.ByCategory[c] = count
		}
		for _, value := range flags(line, e.keep) {
			result.Flags = append(result.Flags, Flag{Line: len(result.Lines), Value: value})
		}
	}
	for c, values := range e.values {
		count := result.ByCategory[c]
		count.Values = int64(len(values))
		result.ByCategory[c] = count
	}
	result.Mapping = e.Mapping()
	return result
}
