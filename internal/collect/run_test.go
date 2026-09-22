// SPDX-License-Identifier: Apache-2.0
package collect

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/akenhq/aken/internal/screen"
	"github.com/akenhq/aken/protocol"
	"github.com/akenhq/aken/relay"
)

func testOptions(t *testing.T, body string) Options {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "app.log")
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 12, 14, 4, 11, 0, time.UTC)
	return Options{Files: []string{file}, Allow: []string{dir}, Since: now.Add(-time.Hour), Until: now, TTL: protocol.DefaultTTL, RelayURL: protocol.DefaultRelayURL, StateDir: filepath.Join(dir, "state"), Retention: 720 * time.Hour, Now: func() time.Time { return now }, Collector: "aken test", Argv: []string{"collect", "--file", file}}
}
func TestRunDecisions(t *testing.T) {
	for _, tt := range []struct {
		name, input      string
		dry, interactive bool
		code             int
		want             string
	}{
		{"dry pipe", "", true, false, 0, ""}, {"dry quit", "q\n", true, true, 0, ""}, {"dry view", "v\nq\n", true, true, 0, ""},
		{"no terminal", "s\n", false, false, 1, "aken: the review screen needs a terminal"}, {"abort", "a\n", false, true, 3, "aken: aborted, nothing was uploaded"}, {"eof", "", false, true, 1, "aken: EOF"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			o := testOptions(t, "from 203.0.113.5\n")
			o.DryRun = tt.dry
			if !tt.dry && tt.interactive {
				server := httptest.NewServer(relay.NewHandler(relay.NewMemoryStore(), relay.Options{}))
				defer server.Close()
				o.RelayURL = server.URL
			}
			var stdout, stderr bytes.Buffer
			code := Run(context.Background(), o, screen.NewInput(strings.NewReader(tt.input), -1, tt.interactive), &stdout, &stderr, 40)
			if code != tt.code || !strings.Contains(stderr.String(), tt.want) {
				t.Fatalf("code %d, stderr %s", code, stderr.String())
			}
			if _, err := os.Stat(o.StateDir); !os.IsNotExist(err) {
				t.Fatalf("created state directory: %v", err)
			}
			if strings.Contains(stdout.String(), "akn1_") {
				t.Fatal("token printed")
			}
		})
	}
}
func TestRunValidation(t *testing.T) {
	for _, tt := range []struct {
		name    string
		change  func(*Options)
		message string
	}{
		{"no source", func(o *Options) { o.Files = nil }, "at least one source"},
		{"unit", func(o *Options) { o.Units = []string{"-bad"} }, "invalid unit"},
		{"container", func(o *Options) { o.Containers = []string{"bad name"} }, "invalid container"},
		{"window", func(o *Options) { o.Since = o.Until }, "--since"},
		{"ttl zero", func(o *Options) { o.TTL = 0 }, "--ttl"},
		{"ttl high", func(o *Options) { o.TTL = 25 * time.Hour }, "--ttl"},
		{"allow", func(o *Options) { o.Allow = []string{"relative"} }, "--allow"},
		{"relay", func(o *Options) { o.RelayURL = "http://example.org" }, "invalid relay URL"},
		{"tail", func(o *Options) { o.Tail = -1 }, "--tail"},
		{"retention", func(o *Options) { o.Retention = -1 }, "--retention"},
		{"category", func(o *Options) { o.KeepCategories = []string{"unknown"} }, "unknown redaction category"},
		{"rules missing", func(o *Options) { o.RulesFile = filepath.Join(o.Allow[0], "missing") }, "no such file"},
		{"empty", func(o *Options) {
			if err := os.WriteFile(o.Files[0], nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}, "no log lines collected"},
		{"outside", func(o *Options) { o.Allow = nil }, "outside the allowed directories"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			o := testOptions(t, "line\n")
			o.DryRun = true
			tt.change(&o)
			var out, errs bytes.Buffer
			if code := Run(context.Background(), o, screen.NewInput(strings.NewReader(""), -1, false), &out, &errs, 40); code != 1 || !strings.Contains(errs.String(), tt.message) {
				t.Fatalf("code %d, error %s", code, errs.String())
			}
		})
	}
}
func TestUploadRoundTrip(t *testing.T) {
	for _, large := range []bool{false, true} {
		t.Run(fmt.Sprint(large), func(t *testing.T) {
			body := "from 203.0.113.5 password=abcdefgh\n" + strings.Repeat("0123456789abcdef0123456789abcdef aB3dE5gH7jK9mN1pQ2sT4vW6\n", 2)
			if large {
				body += strings.Repeat("ordinary log line\n", 70000)
			}
			o := testOptions(t, body)
			handler := relay.NewHandler(relay.NewMemoryStore(), relay.Options{Now: o.Now})
			infoRequests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v0/info" {
					infoRequests++
					if r.Header.Get("Authorization") != protocol.AuthorizationHeader([32]byte{}) {
						t.Error("preflight has credentials")
					}
				}
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			o.RelayURL = server.URL
			// Both explicit duplicates and glob aliases must produce a single source.
			o.Files = append(o.Files, o.Files[0])
			o.Globs = []string{filepath.Join(o.Allow[0], "*.log")}
			if err := os.Chtimes(o.Files[0], o.Until, o.Until); err != nil {
				t.Fatal(err)
			}
			var out, errs bytes.Buffer
			input := "s\n"
			if !large {
				input = "v\ns\n"
			}
			if code := Run(context.Background(), o, screen.NewInput(strings.NewReader(input), -1, true), &out, &errs, 40); code != 0 {
				t.Fatalf("code %d: %s", code, errs.String())
			}
			if infoRequests != 1 {
				t.Fatalf("info requests = %d", infoRequests)
			}
			encoded := regexp.MustCompile(`akn1_[a-z2-7]{52}`).FindString(out.String())
			token, err := protocol.ParseToken(encoded)
			if err != nil {
				t.Fatal("missing valid token")
			}
			defer token.Zero()
			client, err := protocol.NewRelayClient(server.URL, token.RelayCredential())
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			info, err := client.Session(ctx, token.SessionID())
			if err != nil {
				t.Fatal(err)
			}
			keys := protocol.DeriveBlobKeys(token.ContentRoot())
			ciphertext, err := client.GetManifest(ctx, token.SessionID())
			if err != nil {
				t.Fatal(err)
			}
			manifestJSON, err := keys.OpenManifest(info.ChunkCount, ciphertext)
			if err != nil {
				t.Fatal(err)
			}
			var manifest protocol.Manifest
			if err := json.Unmarshal(manifestJSON, &manifest); err != nil {
				t.Fatal(err)
			}
			if err := manifest.Validate(); err != nil {
				t.Fatal(err)
			}
			if len(manifest.Sources) != 1 || manifest.Collector != "aken test" || manifest.Redaction.LinesRedacted != 1 || manifest.Redaction.Flags != 1 || (large && manifest.ChunkCount < 2) {
				t.Fatalf("manifest = %+v", manifest)
			}
			progress := "Creating the session on " + server.URL + "...\n"
			for i := uint32(1); i <= manifest.ChunkCount; i++ {
				progress += fmt.Sprintf("Uploading chunk %d of %d (%s of %s)...\n", i, manifest.ChunkCount, screen.SizeText(min(int64(i)*protocol.ChunkSize, manifest.TotalBytes)), screen.SizeText(manifest.TotalBytes))
			}
			progress += "Uploading the manifest...\nUploaded "
			if !strings.Contains(out.String(), progress) || !strings.Contains(out.String(), "; 1 hex id or hashes not listed.") {
				t.Fatalf("missing progress or distinct id count: %s", out.String())
			}
			var plaintext []byte
			for i := uint32(0); i < info.ChunkCount; i++ {
				chunk, err := client.GetChunk(ctx, token.SessionID(), uint64(i))
				if err != nil {
					t.Fatal(err)
				}
				hash := sha256.Sum256(chunk)
				if hex.EncodeToString(hash[:]) != manifest.ChunksSHA256[i] {
					t.Fatal("hash mismatch")
				}
				opened, err := keys.OpenChunk(uint64(i), info.ChunkCount, chunk)
				if err != nil {
					t.Fatal(err)
				}
				plaintext = append(plaintext, opened...)
			}
			expected := strings.ReplaceAll(strings.ReplaceAll(body, "203.0.113.5", "<ip#1>"), "abcdefgh", "<secret#1>")
			if !large && !strings.Contains(out.String(), "file:"+o.Files[0]+":1 | from <ip#1> password=<secret#1>") {
				t.Fatal("reviewed bytes differ from uploaded bytes")
			}
			if string(plaintext) != expected {
				t.Fatal("uploaded bytes differ from redacted text")
			}
			dirs, err := os.ReadDir(filepath.Join(o.StateDir, "runs"))
			if err != nil || len(dirs) != 1 {
				t.Fatalf("local dirs = %v, %v", dirs, err)
			}
			dir := filepath.Join(o.StateDir, "runs", dirs[0].Name())
			if dirs[0].Name() != "20260912T140411Z-"+token.SessionID().String()[:8] {
				t.Fatal("wrong directory name")
			}
			for _, p := range []string{filepath.Join(o.StateDir, "runs"), dir} {
				st, err := os.Stat(p)
				if err != nil || st.Mode().Perm() != 0o700 {
					t.Fatalf("directory permissions: %v, %v", st, err)
				}
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 4 {
				t.Fatalf("local files = %v, %v", entries, err)
			}
			for _, entry := range entries {
				data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
				if err != nil {
					t.Fatal(err)
				}
				st, err := entry.Info()
				if err != nil {
					t.Fatal(err)
				}
				if st.Mode().Perm() != 0o400 || bytes.Contains(data, []byte(encoded)) {
					t.Fatal("incorrect file permissions or token persisted")
				}
				switch entry.Name() {
				case "artifact.txt":
					if !bytes.Equal(data, plaintext) {
						t.Fatal("local artifact differs")
					}
				case "manifest.json":
					if !bytes.Equal(data, manifestJSON) {
						t.Fatal("local manifest differs")
					}
				case "mapping.json":
					var mapping map[string]string
					if err := json.Unmarshal(data, &mapping); err != nil {
						t.Fatal(err)
					}
					if mapping["<ip#1>"] != "203.0.113.5" || mapping["<secret#1>"] != "abcdefgh" {
						t.Fatal("incorrect mapping")
					}
				case "run.json":
					var run runInfo
					if err := json.Unmarshal(data, &run); err != nil {
						t.Fatal(err)
					}
					if run.SessionID != token.SessionID().String() || !run.ExpiresAt.Equal(info.ExpiresAt) || !reflect.DeepEqual(run.Argv, o.Argv) {
						t.Fatalf("run = %+v", run)
					}
				default:
					t.Fatal("unexpected local file", entry.Name())
				}
			}
			if !strings.Contains(out.String(), "When the relay is not the default, join with: aken-mcp join --relay "+server.URL) {
				t.Fatal("missing nondefault relay instruction")
			}
		})
	}
}
func TestUploadFailures(t *testing.T) {
	for _, stage := range []string{"chunk cap", "ttl cap", "create", "local", "chunk", "manifest"} {
		t.Run(stage, func(t *testing.T) {
			o := testOptions(t, "line\n")
			handler := relay.NewHandler(relay.NewMemoryStore(), relay.Options{Now: o.Now})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v0/info" && (stage == "chunk cap" || stage == "ttl cap") {
					caps := protocol.DefaultCaps
					if stage == "chunk cap" {
						caps.ChunkCount = 0
					} else {
						caps.TTLMaxSeconds = 1
					}
					_ = json.NewEncoder(w).Encode(protocol.Info{ProtocolVersions: []int{1}, Caps: caps})
					return
				}
				fail := r.Method == http.MethodPut && (stage == "create" && !strings.Contains(r.URL.Path, "/blob/") || stage == "chunk" && strings.Contains(r.URL.Path, "/chunks/") || stage == "manifest" && strings.HasSuffix(r.URL.Path, "/manifest"))
				if fail {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":"test_failure"}`))
					return
				}
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			o.RelayURL = server.URL
			if stage == "local" {
				if err := os.WriteFile(o.StateDir, []byte("not a directory"), 0o600); err != nil {
					t.Fatal(err)
				}
				o.Retention = 0
			}
			var out, errs bytes.Buffer
			if code := Run(context.Background(), o, screen.NewInput(strings.NewReader("s\n"), -1, true), &out, &errs, 40); code != 1 {
				t.Fatalf("code = %d", code)
			}
			if strings.Contains(out.String(), "akn1_") {
				t.Fatal("printed token on failure")
			}
			if got := strings.Contains(errs.String(), "Nothing was uploaded; run the same command again.\n"); got != (stage != "manifest") {
				t.Fatal(errs.String())
			}
			if stage == "chunk" || stage == "manifest" || stage == "local" {
				if !strings.Contains(errs.String(), "aken: upload failed:") || !strings.Contains(errs.String(), "nothing readable reached the relay") {
					t.Fatal("missing upload failure message", errs.String())
				}
			}
		})
	}
}
func TestPruneAndDryRun(t *testing.T) {
	for _, dry := range []bool{false, true} {
		t.Run(fmt.Sprint(dry), func(t *testing.T) {
			o := testOptions(t, "line\n")
			o.DryRun = dry
			server := httptest.NewServer(relay.NewHandler(relay.NewMemoryStore(), relay.Options{}))
			defer server.Close()
			o.RelayURL = server.URL
			runs := filepath.Join(o.StateDir, "runs")
			for _, name := range []string{"20200101T000000Z-old", "20260912T140411Z-current", "unrelated", "not-a-timestamp-xxx"} {
				if err := os.MkdirAll(filepath.Join(runs, name), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(runs, "20200101T000000Z-file"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			var out, errs bytes.Buffer
			input := "a\n"
			want := 3
			if dry {
				input = "q\n"
				want = 0
			}
			if code := Run(context.Background(), o, screen.NewInput(strings.NewReader(input), -1, true), &out, &errs, 40); code != want {
				t.Fatal(code, errs.String())
			}
			entries, err := os.ReadDir(runs)
			if err != nil {
				t.Fatal(err)
			}
			count := 4
			if dry {
				count = 5
			}
			if len(entries) != count {
				t.Fatalf("entries = %v", entries)
			}
		})
	}
}
func TestParseTime(t *testing.T) {
	now := time.Date(2026, 9, 12, 14, 0, 0, 0, time.Local)
	for _, tt := range []struct {
		input string
		want  time.Time
	}{
		{"30m", now.Add(-30 * time.Minute)}, {"2h", now.Add(-2 * time.Hour)}, {"3d", now.Add(-72 * time.Hour)},
		{"2026-09-12T10:00:00Z", time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)},
		{"2026-09-12 10:00", time.Date(2026, 9, 12, 10, 0, 0, 0, time.Local)},
		{"2026-09-12 10:00:01", time.Date(2026, 9, 12, 10, 0, 1, 0, time.Local)},
		{"2026-09-12", time.Date(2026, 9, 12, 0, 0, 0, 0, time.Local)},
	} {
		got, err := ParseTime(tt.input, now)
		if err != nil || !got.Equal(tt.want) {
			t.Errorf("ParseTime(%q) = %v, %v", tt.input, got, err)
		}
	}
	for _, s := range []string{"now", "-1h", "-1d", "999999999999999d", "bad", ""} {
		if _, err := ParseTime(s, now); err == nil {
			t.Errorf("accepted %q", s)
		}
	}
}

func TestCustomRulesAndDistinctFlags(t *testing.T) {
	value := "aB3dE5gH7jK9mN1pQ2sT4vW6"
	o := testOptions(t, "Alice 203.0.113.5 "+value+"\n"+value+"\n")
	o.DryRun = true
	o.RulesFile = filepath.Join(o.Allow[0], "rules.json")
	if err := os.WriteFile(o.RulesFile, []byte(`{"version":1,"rules":[{"name":"person","category":"name","regex":"Alice"}],"keep":["203.0.113.5"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(o.Allow[0], "other.log")
	if err := os.WriteFile(other, []byte(value+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	o.Files = append(o.Files, other)
	var out, errs bytes.Buffer
	if code := Run(context.Background(), o, screen.NewInput(strings.NewReader("v\nq\n"), -1, true), &out, &errs, 40); code != 0 {
		t.Fatal(code, errs.String())
	}
	for _, want := range []string{"15 rules (14 default, 1 from " + o.RulesFile + ")", "<name#1> 203.0.113.5", "Flags   1 string to inspect", "lines 1, 2"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in %s", want, out.String())
		}
	}
	if err := os.WriteFile(o.RulesFile, []byte(`{"version":1,"unknown":"sensitive-content"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	o.Files = []string{"/outside/allowed/scope"}
	out.Reset()
	errs.Reset()
	if code := Run(context.Background(), o, screen.NewInput(strings.NewReader(""), -1, false), &out, &errs, 40); code != 1 || errs.String() != "aken: --rules: invalid rules JSON\n" {
		t.Fatal(code, errs.String())
	}
}

func TestPreflightBeforeSources(t *testing.T) {
	o := testOptions(t, "line\n")
	server := httptest.NewServer(http.NotFoundHandler())
	o.RelayURL = server.URL
	server.Close()
	o.Files[0] = filepath.Join(o.Allow[0], "missing")
	for _, interactive := range []bool{false, true} {
		var out, errs bytes.Buffer
		want := "aken: the review screen needs a terminal; use --dry-run to check a collection without one"
		if interactive {
			want = "aken: cannot reach the relay at " + o.RelayURL + ":"
		}
		if code := Run(t.Context(), o, screen.NewInput(strings.NewReader("s"), -1, interactive), &out, &errs, 40); code != 1 || !strings.HasPrefix(errs.String(), want) || out.Len() != 0 {
			t.Fatalf("code %d: %s / %s", code, &out, &errs)
		}
	}
}
