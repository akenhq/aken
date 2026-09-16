// SPDX-License-Identifier: Apache-2.0
package limits

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/akenhq/aken/protocol"
	"golang.org/x/time/rate"
)

type Config struct {
	RequestsPerMinute, CreatesPerHour, MaxLiveSessions int
	BehindCloudflare                                   bool
	AllowanceMode                                      string
	AllowanceServers                                   int
	AllowanceWindow                                    time.Duration
	Logger                                             *slog.Logger
}

func ClientIP(r *http.Request, behindCloudflare bool) string {
	var host string
	if behindCloudflare {
		host = r.Header.Get("CF-Connecting-IP")
	} else {
		host, _, _ = net.SplitHostPort(r.RemoteAddr)
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return "unknown"
}

type buckets struct {
	requests, creates *rate.Limiter
	lastSeen          time.Time
}

type creator struct {
	key string
	at  time.Time
}

type Limiter struct {
	mu       sync.Mutex
	cfg      Config
	now      func() time.Time
	ips      map[string]*buckets
	live     int
	creators map[protocol.SessionID]creator
	paired   map[string]map[string]time.Time
}

func New(cfg Config, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{cfg: cfg, now: now, ips: make(map[string]*buckets), creators: make(map[protocol.SessionID]creator), paired: make(map[string]map[string]time.Time)}
}

func (l *Limiter) SetLiveSessions(n int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.live = n
}

func (l *Limiter) Prune(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for ip, b := range l.ips {
		if now.Sub(b.lastSeen) > time.Hour {
			delete(l.ips, ip)
		}
	}
	for id, creator := range l.creators {
		if now.Sub(creator.at) > time.Duration(protocol.DefaultCaps.TTLMaxSeconds)*time.Second+time.Hour {
			delete(l.creators, id)
		}
	}
	for dev, pairs := range l.paired {
		for server, last := range pairs {
			if !last.After(now.Add(-l.cfg.AllowanceWindow)) {
				delete(pairs, server)
			}
		}
		if len(pairs) == 0 {
			delete(l.paired, dev)
		}
	}
}

func (l *Limiter) check(ip string, create bool) (status int, retry int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b := l.ips[ip]
	if b == nil {
		b = &buckets{
			requests: rate.NewLimiter(rate.Limit(float64(l.cfg.RequestsPerMinute)/60), 60),
			creates:  rate.NewLimiter(rate.Every(time.Hour/time.Duration(l.cfg.CreatesPerHour)), l.cfg.CreatesPerHour),
		}
		l.ips[ip] = b
	}
	b.lastSeen = now
	for _, bucket := range []*rate.Limiter{b.requests, b.creates} {
		if bucket == b.creates {
			if !create {
				break
			}
			if l.live >= l.cfg.MaxLiveSessions {
				return http.StatusServiceUnavailable, 0
			}
		}
		if !bucket.AllowN(now, 1) {
			seconds := (1 - bucket.TokensAt(now)) / float64(bucket.Limit())
			return http.StatusTooManyRequests, max(1, int(math.Ceil(seconds)))
		}
	}
	return 0, 0
}

func (l *Limiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		suffix, session := strings.CutPrefix(r.URL.Path, "/v0/sessions/")
		create := r.Method == http.MethodPut && session && suffix != "" && !strings.Contains(suffix, "/")
		ip := ClientIP(r, l.cfg.BehindCloudflare)
		status, retry := l.check(ip, create)
		if status == 0 {
			l.allowance(next, w, r, ip, create)
			return
		}
		code := "over_capacity"
		if status == http.StatusTooManyRequests {
			code = "rate_limited"
			w.Header().Set("Retry-After", strconv.Itoa(retry))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(protocol.ErrorResponse{Error: code})
	})
}

func (l *Limiter) allowance(next http.Handler, w http.ResponseWriter, r *http.Request, ip string, create bool) {
	if l.cfg.AllowanceMode == "" || l.cfg.AllowanceMode == "off" {
		next.ServeHTTP(w, r)
		return
	}
	suffix, session := strings.CutPrefix(r.URL.Path, "/v0/sessions/")
	segment, _, _ := strings.Cut(suffix, "/")
	id, valid := protocol.ParseSessionID(segment)
	address := net.ParseIP(ip)
	if !session || !valid || address == nil {
		next.ServeHTTP(w, r)
		return
	}
	key := ip
	if address.To4() == nil {
		key = (&net.IPNet{IP: address.Mask(net.CIDRMask(64, 128)), Mask: net.CIDRMask(64, 128)}).String()
	}
	now := l.now()
	if create {
		recorded := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorded, r)
		if recorded.status == http.StatusCreated {
			l.mu.Lock()
			l.creators[id] = creator{key, now}
			l.mu.Unlock()
		}
		return
	}
	l.mu.Lock()
	server, found := l.creators[id]
	if !found || server.key == key {
		l.mu.Unlock()
		next.ServeHTTP(w, r)
		return
	}
	pairs := l.paired[key]
	for server, last := range pairs {
		if !last.After(now.Add(-l.cfg.AllowanceWindow)) {
			delete(pairs, server)
		}
	}
	if _, exists := pairs[server.key]; exists {
		pairs[server.key] = now
		l.mu.Unlock()
		next.ServeHTTP(w, r)
		return
	}
	overrun := len(pairs) >= l.cfg.AllowanceServers
	var oldest time.Time
	for _, last := range pairs {
		if oldest.IsZero() || last.Before(oldest) {
			oldest = last
		}
	}
	l.mu.Unlock()
	if overrun {
		if l.cfg.Logger != nil {
			l.cfg.Logger.Info("anonymous allowance exceeded", "client_ip", ip, "session_id", id.String(), "servers", l.cfg.AllowanceServers, "mode", l.cfg.AllowanceMode)
		}
		if l.cfg.AllowanceMode == "enforce" {
			wait := oldest.Add(l.cfg.AllowanceWindow).Sub(now)
			w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(wait.Seconds())))))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(protocol.ErrorResponse{Error: "server_limit", Message: fmt.Sprintf("anonymous allowance reached: %d servers per %s from this address; retry in %s", l.cfg.AllowanceServers, l.cfg.AllowanceWindow, wait.Round(time.Minute))})
			return
		}
	}
	recorded := &responseWriter{ResponseWriter: w, status: http.StatusOK}
	next.ServeHTTP(recorded, r)
	if recorded.status < http.StatusBadRequest {
		l.mu.Lock()
		if l.paired[key] == nil {
			l.paired[key] = make(map[string]time.Time)
		}
		l.paired[key][server.key] = now
		l.mu.Unlock()
	}
}

type responseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *responseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status, w.wroteHeader = status, true
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}
