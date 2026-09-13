// SPDX-License-Identifier: Apache-2.0
package redact

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/akenhq/aken/rules"
)

func defaults(t *testing.T) File {
	t.Helper()
	f, err := ParseRules(rules.Default)
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func TestDefaultRules(t *testing.T) {
	for _, tt := range []struct{ name, positive, negative, category string }{
		{"aws-access-key-id", "AKIA1234567890ABCDEF", "AKIA123", "secret"},
		{"github-token", "ghp_" + strings.Repeat("a", 36), "ghp_short", "secret"},
		{"slack-token", "xoxb-1234567890-abc", "xoxb-short", "secret"},
		{"stripe-key", "sk_live_12345678901234567890", "sk_live_short", "secret"},
		{"google-api-key", "AIza" + strings.Repeat("a", 35), "AIza-short", "secret"},
		{"sendgrid-key", "SG.abcdefghijklmnop.1234567890123456", "SG.short.short", "secret"},
		{"jwt", "eyJabcde.eyJabcde.abcdefgh", "eyJabc.short.short", "jwt"},
		{"bearer-token", "Bearer abcdefghijklmnop", "Bearer short", "token"},
		{"basic-auth-header", "Authorization: Basic dXNlcjpwYXNzd29yZDEyMw==", "Basic short", "secret"},
		{"basic-auth-url", "https://user:password@host", "https://user@host", "secret"},
		{"generic-assignment", "password=abcdefgh", "password=short", "secret"},
		{"ipv4", "203.0.113.5", "999.999.999.999", "ip"},
		{"ipv6", "2001:db8::1", "12:30:45", "ip"},
		{"email", "person@example.org", "person@localhost", "email"},
		{"phone", "+49 123 456 7890", "123", "phone"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := defaults(t)
			var rule Rule
			for _, r := range f.Rules {
				if r.Name == tt.name {
					rule = r
				}
			}
			enabled := true
			rule.Enabled = &enabled
			e, err := Compile(File{Version: 1, Rules: []Rule{rule}}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			r := e.Redact([][]byte{[]byte(tt.positive), []byte(tt.negative)})
			if !bytes.Contains(r.Lines[0], []byte("<"+tt.category+"#1>")) || string(r.Lines[1]) != tt.negative {
				t.Fatalf("lines = %q", r.Lines)
			}
		})
	}
}
func TestIPs(t *testing.T) {
	e, err := Compile(defaults(t), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"2001:db8::1", "fe80::1ff:fe23:4567:890a", "1:2:3:4:5:6:7:8", "::ffff:192.0.2.1"} {
		result := e.Redact([][]byte{[]byte(value)})
		if !bytes.Contains(result.Lines[0], []byte("<ip#")) || bytes.Contains(result.Lines[0], []byte("192.0.2.1")) {
			t.Fatalf("unredacted IP %q: %q", value, result.Lines)
		}
	}
	for _, value := range []string{"127.0.0.1", "0.0.0.0", "::1", "localhost", "12:30:45", "aa:bb:cc:dd:ee:ff", "2026-09-12T14:39:28", "version 1.2.3", "999.999.999.999"} {
		if got := e.Redact([][]byte{[]byte(value)}); string(got.Lines[0]) != value {
			t.Fatalf("changed %q to %q", value, got.Lines)
		}
	}
	e, err = Compile(File{Version: 1, Rules: []Rule{{Name: "loose", Category: "ip", Regex: `[0-9:.]+`}}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := e.Redact([][]byte{[]byte("12:30:45 1.2.3")}); string(got.Lines[0]) != "12:30:45 1.2.3" {
		t.Fatal("IP matches were not validated")
	}
}
func TestPlaceholdersAndKeep(t *testing.T) {
	e, err := Compile(defaults(t), []string{"10.0.0.5"}, []string{"email"})
	if err != nil {
		t.Fatal(err)
	}
	first := e.Redact([][]byte{[]byte("203.0.113.5 203.0.113.5 10.0.0.5 a@example.org"), []byte("password=abcdefgh Bearer abcdefghijklmnop")})
	if string(first.Lines[0]) != "<ip#1> <ip#1> 10.0.0.5 a@example.org" || first.LinesRedacted != 2 || first.ByCategory["ip"].Values != 1 || first.ByCategory["ip"].Lines != 1 || first.Rules != 13 {
		t.Fatalf("result = %+v", first)
	}
	second := e.Redact([][]byte{[]byte("203.0.113.6 203.0.113.5")})
	if string(second.Lines[0]) != "<ip#2> <ip#1>" || second.ByCategory["ip"].Values != 2 || second.ByCategory["ip"].Lines != 1 {
		t.Fatalf("result = %+v", second)
	}
	if e.Mapping()["<ip#1>"] != "203.0.113.5" {
		t.Fatal("mapping missing")
	}
}
func TestRuleOrder(t *testing.T) {
	e, err := Compile(File{Version: 1, Rules: []Rule{{Name: "first", Category: "secret", Regex: `first`}, {Name: "second", Category: "name", Regex: `secret|second`}}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := e.Redact([][]byte{[]byte("firstsecond")})
	if string(got.Lines[0]) != "<secret#1><name#1>" {
		t.Fatalf("lines = %q", got.Lines)
	}
}
func TestKeyBlocks(t *testing.T) {
	block := []byte("prefix -----BEGIN RSA PRIVATE KEY-----\nabcdef12345\n-----END RSA PRIVATE KEY-----")
	e, err := Compile(defaults(t), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	result := e.Redact(bytes.Split(append(append([]byte{}, block...), []byte("\nafter")...), []byte{'\n'}))
	if len(result.Lines) != 2 || string(result.Lines[0]) != "<key#1>" || string(result.Lines[1]) != "after" || result.Mapping["<key#1>"] != string(block) || result.LinesCollapsed != 2 {
		t.Fatalf("result = %+v", result)
	}
	again := e.Redact(bytes.Split(block, []byte{'\n'}))
	if string(again.Lines[0]) != "<key#1>" {
		t.Fatal("block placeholder changed")
	}
	// The markers are assembled so the secret scanner does not read the test itself as a key.
	dashes := strings.Repeat("-", 5)
	begin := "log: " + dashes + "BEGIN PRIVATE KEY" + dashes
	unclosed := e.Redact([][]byte{[]byte(begin), []byte("secret"), []byte("password=abcdefghij")})
	if len(unclosed.Lines) != 3 || string(unclosed.Lines[0]) != "<key#2>" || string(unclosed.Lines[1]) != "secret" || string(unclosed.Lines[2]) != "password=<secret#1>" || unclosed.Mapping["<key#2>"] != begin || unclosed.LinesCollapsed != 0 {
		t.Fatalf("unterminated key = %+v", unclosed)
	}
	next := e.Redact([][]byte{[]byte("next source")})
	if string(next.Lines[0]) != "next source" {
		t.Fatal("key state leaked")
	}
	off, err := Compile(defaults(t), nil, []string{"key"})
	if err != nil {
		t.Fatal(err)
	}
	if got := off.Redact(bytes.Split(block, []byte{'\n'})); len(got.Lines) != 3 {
		t.Fatal("key category not disabled")
	}
}
func TestRulesValidation(t *testing.T) {
	for _, data := range []string{
		`{"version":2}`, `{"version":1,"unknown":true}`, `{"version":1} {}`, `{"version":1,"rules":[{"name":"x","category":"ip","regex":"["}]}`,
		`{"version":1,"rules":[{"name":"X","category":"ip","regex":"x"}]}`,
		`{"version":1,"rules":[{"name":"x","category":"bad","regex":"x"}]}`,
		`{"version":1,"rules":[{"name":"x","category":"ip","regex":"x","group":1}]}`,
		`{"version":1,"rules":[{"name":"x","category":"ip","regex":"x","group":-1}]}`,
		`{"version":1,"rules":[{"name":"x","category":"ip","regex":"x"},{"name":"x","category":"ip","regex":"x"}]}`,
		`{"version":1,"rules":[{"name":"x","category":"ip","regex":"x","extra":0}]}`,
	} {
		t.Run(data, func(t *testing.T) {
			if _, err := ParseRules([]byte(data)); err == nil {
				t.Fatal("accepted invalid rules")
			}
		})
	}
	if _, err := Compile(defaults(t), nil, []string{"other"}); err == nil {
		t.Fatal("accepted unknown category")
	}
}
func TestMerge(t *testing.T) {
	base := defaults(t)
	extra, err := ParseRules([]byte(`{"version":1,"rules":[{"name":"ipv4","category":"ip","regex":"never","enabled":false},{"name":"custom","category":"name","regex":"Alice"}],"keep":["127.0.0.1","kept"]}`))
	if err != nil {
		t.Fatal(err)
	}
	merged, err := Merge(base, extra)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Rules) != 16 || len(merged.Keep) != 5 || merged.Rules[11].Regex != "never" || base.Rules[11].Regex == "never" {
		t.Fatal("incorrect merge")
	}
	e, err := Compile(merged, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := e.Redact([][]byte{[]byte("Alice 203.0.113.5")}); string(got.Lines[0]) != "<name#1> 203.0.113.5" {
		t.Fatalf("result = %+v", got)
	}
}
func TestFlags(t *testing.T) {
	value := "aB3dE5gH7jK9mN1pQ2sT4vW6"
	for _, tt := range []struct {
		line string
		keep map[string]bool
		want []Flag
	}{
		{value, nil, []Flag{{Value: value}}}, {value + " " + value, nil, []Flag{{Value: value}}}, {value, map[string]bool{value: true}, nil},
		{"aaaaaaaaaaaaaaaaaaaa1", nil, nil}, {"abcdefghijklmnopqrstuv", nil, nil}, {"12345678901234567890123", nil, nil}, {"aB3dE5gH7jK9", nil, nil}, {"<secret#12345678901234567890>", nil, nil},
	} {
		if got := flags([]byte(tt.line), tt.keep); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("flags(%q) = %+v", tt.line, got)
		}
	}
	if entropy([]byte("abcd")) != 2 || entropy(nil) != 0 {
		t.Fatal("incorrect entropy")
	}
	e, err := Compile(defaults(t), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	result := e.Redact([][]byte{[]byte("password=" + value), []byte(value)})
	if len(result.Flags) != 1 || result.Flags[0].Line != 2 {
		t.Fatalf("flags = %+v", result.Flags)
	}
}

func TestSensitiveFields(t *testing.T) {
	for _, tt := range []struct{ input, want string }{
		{"DB_PASSWORD=hunter2hunter2", "DB_PASSWORD=<secret#1>"},
		{"POSTGRES_PASSWORD=Pa$$w0rd!!x", "POSTGRES_PASSWORD=<secret#1>"},
		{"AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "AWS_SECRET_ACCESS_KEY=<secret#1>"},
		{"access_token=abcdefghij", "access_token=<secret#1>"},
		{`refresh_token="abcdefghij"`, `refresh_token="<secret#1>"`},
		{`"accessToken":"abcdefghij"`, `"accessToken":"<secret#1>"`},
		{"SECRET_KEY=abcdefghij", "SECRET_KEY=<secret#1>"},
		{"auth_token: abcdefghij", "auth_token: <secret#1>"},
		{"Authorization: Basic dXNlcjpwYXNzd29yZDEyMw==", "Authorization: Basic <secret#1>"},
		{"redis://:hunter2hunter2@redis:6379/0", "redis://:<secret#1>@redis:6379/0"},
		{"password=abc12345&next=/home", "password=<secret#1>&next=/home"},
		{"cwd=/srv/app/current", "cwd=/srv/app/current"},
		{"tokens_used=12345678", "tokens_used=12345678"},
		{"max_tokens=40960000", "max_tokens=40960000"},
		{"secretary=firstname.lastname", "secretary=firstname.lastname"},
		{"authenticated=true12345", "authenticated=true12345"},
		{"timeout=30000000", "timeout=30000000"},
		{"https://host:8080/@user", "https://host:8080/@user"},
		{"12:30:45 done", "12:30:45 done"},
	} {
		t.Run(tt.input, func(t *testing.T) {
			e, err := Compile(defaults(t), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := e.Redact([][]byte{[]byte(tt.input)}); string(got.Lines[0]) != tt.want {
				t.Fatalf("lines = %q, want %q", got.Lines, tt.want)
			}
		})
	}
}

func TestKeyBlockBound(t *testing.T) {
	for _, end := range []int{0, 1, 64, 65} {
		e, err := Compile(defaults(t), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		lines := bytes.Split([]byte(strings.Repeat("body\n", 65)+"body"), []byte{'\n'})
		dashes := strings.Repeat("-", 5)
		lines[0] = []byte(dashes + "BEGIN PRIVATE KEY" + dashes)
		lines[end] = append(lines[end], []byte(dashes+"END PRIVATE KEY"+dashes)...)
		lines = append(lines, []byte(dashes+"END PRIVATE KEY"+dashes))
		collapsed := end
		if end > 64 {
			collapsed = 0
		}
		got := e.Redact(lines)
		if got.LinesCollapsed != int64(collapsed) || len(got.Lines) != len(lines)-collapsed || string(got.Lines[0]) != "<key#1>" || !reflect.DeepEqual(got.Lines[1:], lines[collapsed+1:]) {
			t.Fatalf("END offset %d: result = %+v", end, got)
		}
	}
}

func TestIDShapedFlags(t *testing.T) {
	e, err := Compile(File{Version: 1}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		value string
		id    bool
	}{
		{"507f1f77bcf86cd799439012", true},
		{strings.Repeat("0123456789abcdef", 2), true},
		{strings.Repeat("0123456789ABCDEF", 4), true},
		{"01234567-89aB-cDeF-0123-456789abcdef", true},
		{"aB3dE5gH7jK9mN1pQ2sT4+/=", false},
		{"0123456789abcDEF0123456789", false},
		{strings.Repeat("0123456789abcdef", 5), false},
	} {
		t.Run(tt.value, func(t *testing.T) {
			got := e.Redact([][]byte{[]byte(tt.value)}).Flags
			want := []Flag{{Line: 1, Value: tt.value, IDShaped: tt.id}}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("flags = %+v, want %+v", got, want)
			}
		})
	}
}
