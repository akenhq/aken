// SPDX-License-Identifier: Apache-2.0
package redact

import (
	"math"
	"regexp"
)

var entropyRun = regexp.MustCompile(`[A-Za-z0-9+/=_-]{20,}`)

func flags(line []byte, keep map[string]bool) []string {
	var out []string
	seen := map[string]bool{}
	for _, value := range entropyRun.FindAll(line, -1) {
		s := string(value)
		if keep[s] || seen[s] {
			continue
		}
		letter, digit := false, false
		for _, c := range value {
			letter = letter || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
			digit = digit || c >= '0' && c <= '9'
		}
		if letter && digit && entropy(value) >= 3.5 {
			out = append(out, s)
			seen[s] = true
		}
	}
	return out
}
func entropy(s []byte) float64 {
	var counts [256]int
	for _, c := range s {
		counts[c]++
	}
	var h float64
	for _, n := range counts {
		if n > 0 {
			p := float64(n) / float64(len(s))
			h -= p * math.Log2(p)
		}
	}
	return h
}
