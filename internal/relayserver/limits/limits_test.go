// SPDX-License-Identifier: Apache-2.0
package limits

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/akenhq/aken/protocol"
)

func TestClientIP(t *testing.T) {
	for _, tt := range []struct {
		peer, header string
		trust        bool
		want         string
	}{
		{"192.0.2.1:123", "203.0.113.1", false, "192.0.2.1"},
		{"[2001:db8::1]:123", "", false, "2001:db8::1"},
		{"bad", "203.0.113.1", false, "unknown"},
		{"192.0.2.1:123", "203.0.113.1", true, "203.0.113.1"},
		{"192.0.2.1:123", "2001:db8::2", true, "2001:db8::2"},
		{"192.0.2.1:123", "", true, "unknown"},
		{"192.0.2.1:123", "203.0.113.1, 203.0.113.2", true, "unknown"},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = tt.peer
		r.Header.Set("CF-Connecting-IP", tt.header)
		r.Header.Set("X-Forwarded-For", "203.0.113.3")
		if got := ClientIP(r, tt.trust); got != tt.want {
			t.Errorf("ClientIP(%+v) = %q", tt, got)
		}
	}
}

func request(handler http.Handler, method, path, peer string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	r.RemoteAddr = peer
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestLimits(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	l := New(Config{RequestsPerMinute: 60, CreatesPerHour: 2, MaxLiveSessions: 3}, func() time.Time { return now })
	h := l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	check := func(method, path, ip string, status int, code, retry string) {
		t.Helper()
		w := request(h, method, path, ip)
		if w.Code != status || w.Header().Get("Retry-After") != retry {
			t.Fatalf("response = %d %v, want %d retry %q", w.Code, w.Header(), status, retry)
		}
		if code != "" {
			var body protocol.ErrorResponse
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Error != code || w.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("body = %s, error = %v", w.Body, err)
			}
			message := "the relay is at capacity; retry later"
			if code == "rate_limited" {
				message = "too many requests from this address; retry in " + retry + "s"
			}
			if body.Message != message {
				t.Fatalf("message = %q, want %q", body.Message, message)
			}
		}
	}
	for range 60 {
		check("GET", "/v0/info", "192.0.2.1:1", 204, "", "")
	}
	check("GET", "/v0/info", "192.0.2.1:2", 429, "rate_limited", "1")
	check("GET", "/healthz", "192.0.2.1:1", 204, "", "")
	check("GET", "/v0/info", "192.0.2.2:1", 204, "", "")
	now = now.Add(time.Second)
	check("GET", "/v0/info", "192.0.2.1:1", 204, "", "")
	path := "/v0/sessions/" + protocol.NewToken().SessionID().String()
	for range 2 {
		check("PUT", path, "192.0.2.2:1", 204, "", "")
	}
	check("PUT", path, "192.0.2.2:1", 429, "rate_limited", "1800")
	check("PUT", path+"/blob/manifest", "192.0.2.2:1", 204, "", "")
	check("GET", path, "192.0.2.2:1", 204, "", "")
	now = now.Add(30 * time.Minute)
	check("PUT", path, "192.0.2.2:1", 204, "", "")
	l.SetLiveSessions(3)
	check("PUT", path, "192.0.2.3:1", 503, "over_capacity", "")
	check("GET", path, "192.0.2.3:1", 204, "", "")
	l.SetLiveSessions(2)
	check("PUT", path, "192.0.2.3:1", 204, "", "")
	l.Prune(now.Add(time.Hour))
	if len(l.ips) != 2 {
		t.Fatalf("at idle boundary: %d IPs", len(l.ips))
	}
	l.Prune(now.Add(time.Hour + time.Nanosecond))
	if len(l.ips) != 0 {
		t.Fatalf("after prune: %d IPs", len(l.ips))
	}
}

func TestAllowance(t *testing.T) {
	for _, mode := range []string{"", "off", "observe", "enforce"} {
		t.Run(mode, func(t *testing.T) {
			now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
			var logs bytes.Buffer
			l := New(Config{RequestsPerMinute: 600, CreatesPerHour: 10, MaxLiveSessions: 200, AllowanceMode: mode, AllowanceServers: 2, AllowanceWindow: time.Hour, Logger: slog.New(slog.NewJSONHandler(&logs, nil))}, func() time.Time { return now })
			h := l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "PUT" {
					w.WriteHeader(201)
				}
			}))
			paths := make([]string, 3)
			for i := range paths {
				paths[i] = "/v0/sessions/" + protocol.NewToken().SessionID().String()
				if w := request(h, "PUT", paths[i], fmt.Sprintf("192.0.2.%d:1", i+1)); w.Code != 201 {
					t.Fatal(w.Code)
				}
			}
			for _, path := range paths[:2] {
				if w := request(h, "GET", path, "203.0.113.1:1"); w.Code != 200 {
					t.Fatal(w.Code)
				}
				now = now.Add(time.Minute)
			}
			w := request(h, "GET", paths[2], "203.0.113.1:2")
			if mode == "enforce" {
				var body protocol.ErrorResponse
				err := json.Unmarshal(w.Body.Bytes(), &body)
				if err != nil || w.Code != 429 || w.Header().Get("Retry-After") != "3480" || body.Error != "server_limit" || body.Message != "anonymous allowance reached: 2 servers per 1h0m0s from this address; retry in 58m0s" {
					t.Fatal(w.Code, w.Header(), w.Body.String(), err)
				}
			} else if w.Code != 200 {
				t.Fatal(w.Code)
			}
			if mode == "observe" {
				request(h, "GET", paths[2], "203.0.113.1:2")
			}
			if mode == "observe" || mode == "enforce" {
				var entry map[string]any
				if err := json.Unmarshal(logs.Bytes(), &entry); err != nil || entry["msg"] != "anonymous allowance exceeded" || entry["client_ip"] != "203.0.113.1" || entry["mode"] != mode || entry["servers"] != float64(2) || entry["session_id"] != strings.TrimPrefix(paths[2], "/v0/sessions/") {
					t.Fatal(logs.String(), err)
				}
			} else if len(l.creators) != 0 || len(l.paired) != 0 || logs.Len() != 0 {
				t.Fatal("off recorded allowance state")
			}
			if w := request(h, "GET", paths[1], "203.0.113.1:1"); w.Code != 200 {
				t.Fatal("existing pair", w.Code)
			}
			if w := request(h, "GET", paths[2], "203.0.113.2:1"); w.Code != 200 {
				t.Fatal("another developer", w.Code)
			}
			now = now.Add(time.Hour)
			if w := request(h, "GET", paths[2], "203.0.113.1:1"); w.Code != 200 {
				t.Fatal("expired window", w.Code)
			}
			l.Prune(now.Add(25 * time.Hour))
			if len(l.creators) != 0 || len(l.paired) != 0 {
				t.Fatal("expired allowance state")
			}
		})
	}
}

func TestAllowanceAddressesAndFailures(t *testing.T) {
	now := time.Now()
	l := New(Config{RequestsPerMinute: 600, CreatesPerHour: 10, MaxLiveSessions: 200, AllowanceMode: "enforce", AllowanceServers: 1, AllowanceWindow: time.Hour}, func() time.Time { return now })
	status := 201
	h := l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
	first := "/v0/sessions/" + protocol.NewToken().SessionID().String()
	second := "/v0/sessions/" + protocol.NewToken().SessionID().String()
	request(h, "PUT", first, "[2001:db8:1::1]:1")
	request(h, "PUT", second, "192.0.2.1:1")
	status = 200
	request(h, "GET", first, "[2001:db8:1::2]:1")
	request(h, "GET", second, "192.0.2.1:2")
	request(h, "GET", first, "bad")
	request(h, "GET", "/v0/sessions/invalid", "203.0.113.1:1")
	request(h, "GET", "/v0/sessions/"+protocol.NewToken().SessionID().String(), "203.0.113.1:1")
	if len(l.paired) != 0 {
		t.Fatal("same address, unknown session or unknown address was counted")
	}
	status = 403
	failed := "/v0/sessions/" + protocol.NewToken().SessionID().String()
	request(h, "PUT", failed, "192.0.2.2:1")
	request(h, "GET", first, "203.0.113.1:1")
	if len(l.creators) != 2 || len(l.paired) != 0 {
		t.Fatal("failed request recorded")
	}
	status = 200
	request(h, "GET", first+"/join", "[2001:db8:2::1]:1")
	if w := request(h, "GET", second, "[2001:db8:2::2]:1"); w.Code != 429 {
		t.Fatal("IPv6 prefix did not share allowance", w.Code)
	}
	now = now.Add(time.Hour)
	l.Prune(now)
	if len(l.paired) != 0 || len(l.creators) != 2 {
		t.Fatal("prune at window boundary")
	}
	l.Prune(now.Add(24*time.Hour + time.Nanosecond))
	if len(l.creators) != 0 {
		t.Fatal("creators survived TTL plus margin")
	}
}
